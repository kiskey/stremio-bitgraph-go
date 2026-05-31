
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

func SearchTorrents(ctx context.Context, searchString, contentType string, limit int) ([]TorrentItem, error) {
	cleanQuery := strings.ReplaceAll(searchString, `\`, "")
	cleanQuery = strings.ReplaceAll(cleanQuery, `"`, "")
	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"queryString": cleanQuery,
			"limit":     limit,
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
	var items []TorrentItem
	tc, _ := data["data"].(map[string]interface{})
	if tc == nil {
		return items, nil
	}
	tcs, _ := tc["torrentContent"].(map[string]interface{})
	if tcs == nil {
		return items, nil
	}
	search, _ := tcs["search"].(map[string]interface{})
	if search == nil {
		return items, nil
	}
	rawItems, _ := search["items"].([]interface{})
	for _, ri := range rawItems {
		b, _ := json.Marshal(ri)
		var item TorrentItem
		json.Unmarshal(b, &item)
		items = append(items, item)
	}
	return items, nil
}

type TorrentFile struct {
	Index    int    `json:"index"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	FileType string `json:"fileType"`
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
	var items []TorrentFile
	tc, _ := data["data"].(map[string]interface{})
	if tc == nil {
		return items, nil
	}
	torrent, _ := tc["torrent"].(map[string]interface{})
	if torrent == nil {
		return items, nil
	}
	files, _ := torrent["files"].(map[string]interface{})
	if files == nil {
		return items, nil
	}
	rawItems, _ := files["items"].([]interface{})
	for _, ri := range rawItems {
		b, _ := json.Marshal(ri)
		var item TorrentFile
		json.Unmarshal(b, &item)
		items = append(items, item)
	}
	return items, nil
}
