package addon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/user/stremio-bitgraph-go/internal/bitmagnet"
	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/db"
	"github.com/user/stremio-bitgraph-go/internal/debrid"
	"github.com/user/stremio-bitgraph-go/internal/matcher"
	"github.com/user/stremio-bitgraph-go/internal/metadata"
	"github.com/user/stremio-bitgraph-go/internal/parser"
	"github.com/user/stremio-bitgraph-go/internal/utils"
)

type Manifest struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Resources   []string `json:"resources"`
	Types       []string `json:"types"`
	IDPrefixes  []string `json:"idPrefixes"`
	Catalogs    []string `json:"catalogs"`
}

type StreamResponse struct {
	Streams []Stream `json:"streams"`
}

type Stream struct {
	Name          string            `json:"name"`
	Title         string            `json:"title"`
	URL           string            `json:"url,omitempty"`
	InfoHash      string            `json:"infoHash,omitempty"`
	FileIdx       int               `json:"fileIdx,omitempty"`
	BehaviorHints map[string]interface{} `json:"behaviorHints,omitempty"`
}

func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/manifest.json", manifestHandler)
	r.Get("/stream/{type}/{id}.json", streamHandler)
	return r
}

func manifestHandler(w http.ResponseWriter, r *http.Request) {
	m := Manifest{
		ID:          config.AddonID,
		Version:     config.AddonVersion,
		Name:        config.AddonName,
		Description: "Streams Movies & TV Shows from Bitmagnet via Real-Debrid.",
		Resources:   []string{"stream"},
		Types:       []string{"movie", "series"},
		IDPrefixes:  []string{"tt"},
		Catalogs:    []string{},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	typ := chi.URLParam(r, "type")
	id := chi.URLParam(r, "id")

	utils.Logger.Info("stream request", "type", typ, "id", id)

	var imdbID, seasonStr, episodeStr string
	if typ == "series" {
		parts := strings.Split(id, ":")
		if len(parts) >= 3 {
			imdbID, seasonStr, episodeStr = parts[0], parts[1], parts[2]
		}
	} else {
		imdbID = id
	}

	meta, err := metadata.GetMetaDetails(ctx, imdbID, typ)
	if err != nil || meta == nil {
		json.NewEncoder(w).Encode(StreamResponse{Streams: []Stream{}})
		return
	}

	contentType := "movie"
	if typ == "series" {
		contentType = "tv_show"
	}

	torrents, err := bitmagnet.SearchTorrents(ctx, meta.Name, contentType, 100)
	if err != nil {
		json.NewEncoder(w).Encode(StreamResponse{Streams: []Stream{}})
		return
	}
	if len(torrents) == 0 {
		json.NewEncoder(w).Encode(StreamResponse{Streams: []Stream{}})
		return
	}

	var cachedRows []map[string]interface{}
	if debrid.LoadProvider().IsEnabled() && config.DebridProvider != "" {
		rows, _ := db.Pool.Query(ctx,
			"SELECT * FROM torrents WHERE tmdb_id = $1 AND content_type = $2 AND torrent_info_json IS NOT NULL AND provider = $3",
			imdbID, typ, config.DebridProvider)
		defer rows.Close()
		for rows.Next() {
			var id int
			var infohash, tmdbID, ct, provider, language, quality string
			var torrentInfoJSON []byte
			var seeders int32
			var addedAt, lastUsedAt interface{}
			rows.Scan(&id, &infohash, &tmdbID, &ct, &provider, &torrentInfoJSON, &language, &quality, &seeders, &addedAt, &lastUsedAt)
			var tinfo map[string]interface{}
			json.Unmarshal(torrentInfoJSON, &tinfo)
			cachedRows = append(cachedRows, map[string]interface{}{
				"infohash":            infohash,
				"language":            language,
				"quality":             quality,
				"seeders":             seeders,
				"torrent_info_json":   tinfo,
			})
		}
	}

	var resultStreams, cachedStreams []matcher.Stream
	if typ == "series" {
		season, _ := strconv.Atoi(seasonStr)
		episode, _ := strconv.Atoi(episodeStr)
		sVal := season
		var sPadded string
		if sVal < 10 {
			sPadded = fmt.Sprintf("S0%d", sVal)
		} else {
			sPadded = fmt.Sprintf("S%d", sVal)
		}
		refinedQuery := fmt.Sprintf("%s %s", meta.Name, sPadded)
		refinedTorrents, _ := bitmagnet.SearchTorrents(ctx, refinedQuery, "tv_show", 50)
		refinedResult, _ := matcher.FindBestSeriesStreams(ctx, &bitmagnet.TorrentItem{Title: meta.Name}, season, episode, refinedTorrents, cachedRows, config.PreferredLanguages)
		resultStreams = refinedResult
		cachedStreams = cachedStreams // populated inside FindBestSeriesStreams

		if len(refinedTorrents) < 10 || len(resultStreams) == 0 {
			broadTorrents, _ := bitmagnet.SearchTorrents(ctx, meta.Name, "tv_show", 100)
			broadResult, broadCached := matcher.FindBestSeriesStreams(ctx, &bitmagnet.TorrentItem{Title: meta.Name}, season, episode, broadTorrents, cachedRows, config.PreferredLanguages)
			existing := make(map[string]bool)
			for _, s := range resultStreams {
				existing[s.InfoHash] = true
			}
			for _, s := range broadResult {
				if !existing[s.InfoHash] {
					resultStreams = append(resultStreams, s)
					existing[s.InfoHash] = true
				}
			}
			existingCached := make(map[string]bool)
			for _, s := range cachedStreams {
				existingCached[s.InfoHash] = true
			}
			for _, s := range broadCached {
				if !existingCached[s.InfoHash] {
					cachedStreams = append(cachedStreams, s)
					existingCached[s.InfoHash] = true
				}
			}
		}
	} else {
		movieResult, movieCached := matcher.FindBestMovieStreams(&bitmagnet.TorrentItem{Title: meta.Name}, meta.Year, torrents, cachedRows, config.PreferredLanguages)
		resultStreams = movieResult
		cachedStreams = movieCached
	}

	sorted := matcher.SortAndFilterStreams(resultStreams, cachedStreams, config.PreferredLanguages)
	utils.Logger.Info("total sorted streams", "count", len(sorted))

	provider := debrid.LoadProvider()
	if provider.IsEnabled() {
		if _, ok := provider.(interface{ CheckCached(context.Context, []string) (map[string]debrid.CacheStatus, error) }); ok {
			hashes := make([]string, len(sorted))
			for i, s := range sorted {
				hashes[i] = s.InfoHash
			}
			if len(hashes) > 0 {
				cacheStatus, err := provider.CheckCached(ctx, hashes)
				if err != nil {
					utils.Logger.Warn("checkCached failed", "error", err)
				} else {
					for i := range sorted {
						cs := cacheStatus[sorted[i].InfoHash]
						sorted[i].IsCached = cs.Cached
						if cs.Cached && cs.TorrentID != "" {
							files := make([]map[string]interface{}, len(cs.Files))
							for j, f := range cs.Files {
								files[j] = map[string]interface{}{
									"id":       f.ID,
									"path":     f.Name,
									"bytes":    f.Size,
									"selected": 1,
								}
							}
							resolved := map[string]interface{}{
								"id":       cs.TorrentID,
								"filename": cs.Name,
								"files":    files,
								"status":   "downloaded",
							}
							// Store in memory cache
							_ = resolved
						}
					}
				}
			}
		}
	}

	// Fix missing fileIndex for P2P movies
	if !provider.IsEnabled() {
		torrentMap := make(map[string]bitmagnet.TorrentItem)
		for _, t := range torrents {
			torrentMap[t.InfoHash] = t
		}
		for i := range sorted {
			if sorted[i].FileIndex == 0 {
				if t, ok := torrentMap[sorted[i].InfoHash]; ok && t.Torrent.FilesStatus == "multi" {
					files, err := bitmagnet.GetTorrentFiles(ctx, sorted[i].InfoHash)
					if err == nil && len(files) > 0 {
						var videoFiles []bitmagnet.TorrentFile
						for _, f := range files {
							if f.FileType == "video" {
								videoFiles = append(videoFiles, f)
							}
						}
						if len(videoFiles) > 0 {
							largest := videoFiles[0]
							for _, vf := range videoFiles[1:] {
								if vf.Size > largest.Size {
									largest = vf
								}
							}
							sorted[i].FileIndex = largest.Index
						} else {
							sorted[i].FileIndex = 0
						}
					} else {
						sorted[i].FileIndex = 0
					}
				} else {
					sorted[i].FileIndex = 0
				}
			}
		}
	}

	var streams []Stream
	if provider.IsEnabled() {
		for _, s := range sorted {
			prefix := "⌛"
			if s.IsCached {
				prefix = "⚡"
			}
			url := fmt.Sprintf("%s/%s/stream/%s/%s/%s", config.AppHost, config.AddonID, typ, id, s.InfoHash)
			streams = append(streams, Stream{
				Name:  fmt.Sprintf("[%s RD] %s | %s", prefix, strings.ToUpper(s.Language), strings.ToUpper(s.Quality)),
				Title: fmt.Sprintf("%s\n💾 %s | 👤 %d seeders", s.TorrentName, utils.FormatSize(s.Size), s.Seeders),
				URL:   url,
			})
		}
	} else {
		for _, s := range sorted {
			bh := map[string]interface{}{"notWebReady": true}
			if typ == "series" {
				bh["bingeGroup"] = imdbID
			}
			streams = append(streams, Stream{
				Name:          fmt.Sprintf("[Bitgraph P2P] %s | %s", strings.ToUpper(s.Language), strings.ToUpper(s.Quality)),
				Title:         fmt.Sprintf("%s\n💾 %s | 👤 %d seeders", s.TorrentName, utils.FormatSize(s.Size), s.Seeders),
				InfoHash:      s.InfoHash,
				FileIdx:       s.FileIndex,
				BehaviorHints: bh,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(StreamResponse{Streams: streams})
}
