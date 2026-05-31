
package addon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/user/stremio-bitgraph-go/internal/bitmagnet"
	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/db"
	"github.com/user/stremio-bitgraph-go/internal/debrid"
	"github.com/user/stremio-bitgraph-go/internal/matcher"
	"github.com/user/stremio-bitgraph-go/internal/metadata"
	"github.com/user/stremio-bitgraph-go/internal/utils"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
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
	Name          string                 `json:"name"`
	Title         string                 `json:"title"`
	URL           string                 `json:"url,omitempty"`
	InfoHash      string                 `json:"infoHash,omitempty"`
	FileIdx       int                    `json:"fileIdx,omitempty"`
	BehaviorHints map[string]interface{} `json:"behaviorHints,omitempty"`
}

var searchCache = utils.NewTTLCache(5 * time.Minute)

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(corsMiddleware)
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

	idDecoded, err := url.QueryUnescape(id)
	if err != nil {
		idDecoded = id
	}

	utils.Logger.Info("stream request", "type", typ, "id", id, "decoded_id", idDecoded)

	var imdbID, seasonStr, episodeStr string
	if typ == "series" {
		parts := strings.Split(idDecoded, ":")
		if len(parts) >= 3 {
			imdbID, seasonStr, episodeStr = parts[0], parts[1], parts[2]
		}
	} else {
		imdbID = idDecoded
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

	var torrents []bitmagnet.TorrentItem
	var cachedRows []map[string]interface{}
	var searchErr error

	searchCacheKey := fmt.Sprintf("%s_%s_%s", imdbID, typ, meta.Name)

	var wg sync.WaitGroup
	wg.Add(2)

	// Concurrent Bitmagnet Search + Database Caching check
	go func() {
		defer wg.Done()
		if cachedVal, ok := searchCache.Get(searchCacheKey); ok {
			torrents = cachedVal.([]bitmagnet.TorrentItem)
			return
		}
		torrents, searchErr = bitmagnet.SearchTorrents(ctx, meta.Name, contentType, 100)
		if searchErr == nil && len(torrents) > 0 {
			searchCache.Set(searchCacheKey, torrents)
		}
	}()

	go func() {
		defer wg.Done()
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
					"infohash":          infohash,
					"language":          language,
					"quality":           quality,
					"seeders":           seeders,
					"torrent_info_json": tinfo,
				})
			}
		}
	}()

	wg.Wait()

	if searchErr != nil || len(torrents) == 0 {
		json.NewEncoder(w).Encode(StreamResponse{Streams: []Stream{}})
		return
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

		// Parallel fetch of Refined and Broad query sets
		var refinedTorrents, broadTorrents []bitmagnet.TorrentItem
		g, gCtx := errgroup.WithContext(ctx)

		g.Go(func() error {
			refinedQuery := fmt.Sprintf("%s %s", meta.Name, sPadded)
			var innerErr error
			refinedTorrents, innerErr = bitmagnet.SearchTorrents(gCtx, refinedQuery, "tv_show", 50)
			return innerErr
		})

		g.Go(func() error {
			var innerErr error
			broadTorrents, innerErr = bitmagnet.SearchTorrents(gCtx, meta.Name, "tv_show", 100)
			return innerErr
		})

		_ = g.Wait()

		refinedResult, refinedCached := matcher.FindBestSeriesStreams(ctx, &bitmagnet.TorrentItem{Title: meta.Name}, season, episode, refinedTorrents, cachedRows, config.PreferredLanguages)
		resultStreams = refinedResult
		cachedStreams = refinedCached

		if len(refinedTorrents) < 10 || len(resultStreams) == 0 {
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
		movieResult, movieCached := matcher.FindBestMovieStreams(ctx, &bitmagnet.TorrentItem{Title: meta.Name}, meta.Year, torrents, cachedRows, config.PreferredLanguages)
		resultStreams = movieResult
		cachedStreams = movieCached
	}

	sorted := matcher.SortAndFilterStreams(resultStreams, cachedStreams, config.PreferredLanguages)
	utils.Logger.Info("total sorted streams", "count", len(sorted))

	provider := debrid.LoadProvider()
	if provider.IsEnabled() {
		if cachedProvider, ok := provider.(interface {
			CheckCached(context.Context, []string) (map[string]debrid.CacheStatus, error)
		}); ok {
			hashes := make([]string, len(sorted))
			for i, s := range sorted {
				hashes[i] = s.InfoHash
			}
			if len(hashes) > 0 {
				cacheStatus, err := cachedProvider.CheckCached(ctx, hashes)
				if err != nil {
					utils.Logger.Warn("checkCached failed", "error", err)
				} else {
					for i := range sorted {
						cs := cacheStatus[sorted[i].InfoHash]
						sorted[i].IsCached = cs.Cached
						if cs.Cached && cs.TorrentID != "" {
							torrentInfo := &debrid.TorrentInfo{
								ID:       cs.TorrentID,
								Filename: cs.Name,
								Status:   "downloaded",
							}
							for _, f := range cs.Files {
								torrentInfo.Files = append(torrentInfo.Files, debrid.FileInfo{
									ID:       f.ID,
									Path:     f.Name,
									Bytes:    f.Size,
									Selected: 1,
								})
							}
							debrid.TorrentInfoCache.Set(ctx, sorted[i].InfoHash, map[string]interface{}{
								"torrent_info": torrentInfo,
							})
						}
					}
				}
			}
		}
	}

	// Fix missing fileIndex for P2P movies concurrently
	if !provider.IsEnabled() {
		torrentMap := make(map[string]bitmagnet.TorrentItem)
		var multiHashes []string
		for _, t := range torrents {
			torrentMap[t.InfoHash] = t
			if t.Torrent.FilesStatus == "multi" {
				multiHashes = append(multiHashes, t.InfoHash)
			}
		}

		filesMap := make(map[string][]bitmagnet.TorrentFile)
		var mu sync.Mutex
		g, pctx := errgroup.WithContext(ctx)
		sem := semaphore.NewWeighted(6)

		for _, h := range multiHashes {
			h := h
			g.Go(func() error {
				if err := sem.Acquire(pctx, 1); err != nil {
					return err
				}
				defer sem.Release(1)
				files, err := bitmagnet.GetTorrentFiles(pctx, h)
				if err == nil {
					mu.Lock()
					filesMap[h] = files
					mu.Unlock()
				}
				return nil
			})
		}
		_ = g.Wait()

		for i := range sorted {
			if sorted[i].FileIndex == 0 {
				if t, ok := torrentMap[sorted[i].InfoHash]; ok && t.Torrent.FilesStatus == "multi" {
					files, ok := filesMap[sorted[i].InfoHash]
					if !ok || len(files) == 0 {
						sorted[i].FileIndex = 0
						continue
					}
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
					}
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
