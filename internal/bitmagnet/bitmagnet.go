package bitmagnet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/utils"
)

var client = utils.NewOptimizedClient(15 * time.Second)

const torrentContentSearchQuery = `
query TorrentContentSearch($input: TorrentContentSearchQueryInput!) {
  torrentContent {
    search(input: $input) {
      items {
        infoHash
        title
        seeders
        leechers
        publishedAt
        videoResolution
        languages { id }
        torrent {
          name
          size
          filesStatus
          filesCount
          hasFilesInfo
        }
      }
    }
  }
}`

const torrentFilesQuery = `
query TorrentFiles($input: TorrentFilesQueryInput!) {
  torrent {
    files(input: $input) {
      items {
        index
        path
        size
        fileType
      }
    }
  }
}`

// 100% Single-Pass generic GraphQL Response Envelope
type gqlEnvelope[T any] struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Data T `json:"data"`
}

func queryGraphQLGeneric[T any](ctx context.Context, query string, variables map[string]interface{}, dest *T) error {
	body := map[string]interface{}{"query": query, "variables": variables}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", config.BitmagnetGQLEndpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var envelope gqlEnvelope[T]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}

	if len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("graphql errors: %s", strings.Join(msgs, ", "))
	}

	*dest = envelope.Data
	return nil
}

type TorrentItem struct {
	InfoHash        string                 `json:"infoHash"`
	Title           string                 `json:"title"`
	Seeders         int                    `json:"seeders"`
	Leechers        int                    `json:"leechers"`
	PublishedAt     string                 `json:"publishedAt"`
	VideoResolution string                 `json:"videoResolution"`
	Languages       []struct{ ID string }  `json:"languages"`
	Torrent         struct {
		Name         string `json:"name"`
		Size         int64  `json:"size"`
		FilesStatus  string `json:"filesStatus"`
		FilesCount   int    `json:"filesCount"`
		HasFilesInfo bool   `json:"hasFilesInfo"`
	} `json:"torrent"`
}

type TorrentFile struct {
	Index    int    `json:"index"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	FileType string `json:"fileType"`
}

func SearchTorrents(ctx context.Context, searchString, contentType string, limit int) ([]TorrentItem, error) {
	// We omit "orderBy" entirely from the query variables. 
	// When "queryString" is provided, Bitmagnet natively defaults to sorting by "relevance" descending.
	// Omitting it entirely avoids the complex GORM subquery pagination bug that generates the invalid "_order_1" column reference.
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"queryString": searchString,
			"limit":       limit,
			"facets": map[string]interface{}{
				"contentType": map[string]interface{}{"filter": []string{contentType}},
				"torrentFileType": map[string]interface{}{
					"filter": []string{"video"}, // Enforce server-side video filtering only
				},
			},
		},
	}

	type searchData struct {
		TorrentContent struct {
			Search struct {
				Items []TorrentItem `json:"items"`
			} `json:"search"`
		} `json:"torrentContent"`
	}

	var data searchData
	err := queryGraphQLGeneric(ctx, torrentContentSearchQuery, variables, &data)
	if err != nil {
		utils.Logger.Error("bitmagnet search failed", "error", err)
		return nil, err
	}

	return data.TorrentContent.Search.Items, nil
}

func GetTorrentFiles(ctx context.Context, infoHash string) ([]TorrentFile, error) {
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"infoHashes": []string{infoHash},
			"limit":      1000,
		},
	}

	type filesData struct {
		Torrent struct {
			Files struct {
				Items []TorrentFile `json:"items"`
			} `json:"files"`
		} `json:"torrent"`
	}

	var data filesData
	err := queryGraphQLGeneric(ctx, torrentFilesQuery, variables, &data)
	if err != nil {
		return nil, err
	}

	return data.Torrent.Files.Items, nil
}
