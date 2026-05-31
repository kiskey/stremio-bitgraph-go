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

func queryGraphQL(ctx context.Context, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	body := map[string]interface{}{"query": query, "variables": variables}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", config.BitmagnetGQLEndpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if errs, ok := data["errors"].([]interface{}); ok && len(errs) > 0 {
		msgs := []string{}
		for _, e := range errs {
			if em, ok := e.(map[string]interface{}); ok {
				msgs = append(msgs, fmt.Sprintf("%v", em["message"]))
			}
		}
		return nil, fmt.Errorf("graphql errors: %s", strings.Join(msgs, ", "))
	}
	return data, nil
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

type searchResponse struct {
	Data struct {
		TorrentContent struct {
			Search struct {
				Items []TorrentItem `json:"items"`
			} `json:"search"`
		} `json:"torrentContent"`
	} `json:"data"`
}

type filesResponse struct {
	Data struct {
		Torrent struct {
			Files struct {
				Items []TorrentFile `json:"items"`
			} `json:"files"`
		} `json:"torrent"`
	} `json:"data"`
}

type gqlErrorResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func SearchTorrents(ctx context.Context, searchString, contentType string, limit int) ([]TorrentItem, error) {
	cleanQuery := strings.ReplaceAll(searchString, `\`, "")
	cleanQuery = strings.ReplaceAll(cleanQuery, `"`, "")
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"queryString": cleanQuery,
			"limit":       limit,
			"orderBy": []map[string]interface{}{
				{"field": "published_at", "descending": true},
				{"field": "seeders", "descending": true},
			},
			"facets": map[string]interface{}{
				"contentType": map[string]interface{}{"filter": []string{contentType}},
			},
		},
	}
	data, err := queryGraphQL(ctx, torrentContentSearchQuery, variables)
	if err != nil {
		utils.Logger.Error("bitmagnet search failed", "error", err)
		return nil, err
	}

	// Check GraphQL errors first
	var errResp gqlErrorResponse
	if raw, _ := json.Marshal(data); raw != nil {
		_ = json.Unmarshal(raw, &errResp)
	}
	if len(errResp.Errors) > 0 {
		msgs := make([]string, 0, len(errResp.Errors))
		for _, e := range errResp.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("graphql errors: %s", strings.Join(msgs, ", "))
	}

	var resp searchResponse
	if raw, _ := json.Marshal(data); raw != nil {
		if err := json.Unmarshal(raw, &resp); err != nil {
			utils.Logger.Error("bitmagnet search decode failed", "error", err)
			return nil, err
		}
	}
	return resp.Data.TorrentContent.Search.Items, nil
}

func GetTorrentFiles(ctx context.Context, infoHash string) ([]TorrentFile, error) {
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"infoHashes": []string{infoHash},
			"limit":      1000,
		},
	}
	data, err := queryGraphQL(ctx, torrentFilesQuery, variables)
	if err != nil {
		return nil, err
	}

	var errResp gqlErrorResponse
	if raw, _ := json.Marshal(data); raw != nil {
		_ = json.Unmarshal(raw, &errResp)
	}
	if len(errResp.Errors) > 0 {
		msgs := make([]string, 0, len(errResp.Errors))
		for _, e := range errResp.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("graphql errors: %s", strings.Join(msgs, ", "))
	}

	var resp filesResponse
	if raw, _ := json.Marshal(data); raw != nil {
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
	}
	return resp.Data.Torrent.Files.Items, nil
}
