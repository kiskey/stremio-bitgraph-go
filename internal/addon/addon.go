package addon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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
	"github.com/user/stremio-bitgraph-go/internal/parser"
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

// Compile-time optimized replacement string structure
var queryReplacer = strings.NewReplacer("\\", "", "\"", "")

// Pre-compiled global regular expression to prevent GC pressure and lookahead errors during live stream serving
var rangeRegex = regexp.MustCompile(`(?i)\b(?:e|ep|episode)?\s*(\d+)\s*(?:-|to)\s*(?:e|ep|episode)?\s*(\d+)\b`)

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

func buildOptimizedQuery(name string, altTitles []string, suffix string) string {
	// Clean colons, hyphens, and brackets from the query string to prevent FTS parser confusion (e.g. "From Dusk Till Dawn: The Series" -> "From Dusk Till Dawn The Series")
	nameClean := queryReplacer.Replace(name)
	nameClean = strings.ReplaceAll(nameClean, ":", " ")
	nameClean = strings.ReplaceAll(nameClean, "-", " ")
	nameClean = strings.ReplaceAll(nameClean, "(", " ")
	nameClean = strings.ReplaceAll(nameClean, ")", " ")
	nameClean = strings.Join(strings.Fields(nameClean), " ")

	// Strict enclosing double quotes are removed to ensure standard space-separated unquoted lexemes.
	// This prevents FTS tokenizer and postgres stop-word position errors, maximizing search recall.
	query := nameClean

	if suffix != "" {
		suffixClean := queryReplacer.Replace(suffix)
		query = fmt.Sprintf("%s %s", query, suffixClean)
	}

	return query
}

// checkPackOrRange delegates token scanning strictly to the parsed domain layer (parser.go)
func checkPackOrRange(name string, targetE int) string {
	isPack, start, end, hasRange := parser.ParsePackOrRange(name, targetE)
	if isPack {
		return " (📦 Season Pack)"
	}
	if hasRange {
		return fmt.Sprintf(" (🔢 Batch E%02d-%02d)", start, end)
	}
	return ""
}

func deduplicateTorrents(items []bitmagnet.TorrentItem) []bitmagnet.TorrentItem {
	seen := make(map[string]bool)
	var unique []bitmagnet.TorrentItem
	for _, item := range items {
		if !seen[item.InfoHash] {
			seen[item.InfoHash] = true
			unique = append(unique, item)
		}
	}
	return unique
}

func isVideoFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".mkv") ||
		strings.HasSuffix(lower, ".mp4") ||
		strings.HasSuffix(lower, ".avi") ||
		strings.HasSuffix(lower, ".mov") ||
		strings.HasSuffix(lower, ".wmv") ||
		strings.HasSuffix(lower, ".flv") ||
		strings.HasSuffix(lower, ".webm") ||
		strings.HasSuffix(lower, ".m4v") ||
		strings.HasSuffix(lower, ".ts") ||
		strings.HasSuffix(lower, ".mpg") ||
		strings.HasSuffix(lower, ".mpeg")
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	typ := chi.URLParam(r, "type")
	id := chi.URLParam(r, "id")

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	idDecoded, err := url.QueryUnescape(id)
	if err != nil {
		idDecoded = id
	}

	utils.Logger.Info("stream request", "type", typ, "id", id, "decoded_id", idDecoded)

	var imdbID, seasonStr, episodeStr string
	var season, episode int
	if typ == "series" {
		parts := strings.Split(idDecoded, ":")
		if len(parts) >= 3 {
			imdbID, seasonStr, episodeStr = parts[0], parts[1], parts[2]
			season, _ = strconv.Atoi(seasonStr)
			episode, _ = strconv.Atoi(episodeStr)
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

	if typ == "movie" {
		searchCacheKey := fmt.Sprintf("%s_%s_%s", imdbID, typ, meta.Name)
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			if cachedVal, ok := searchCache.Get(searchCacheKey); ok {
				torrents = cachedVal.([]bitmagnet.TorrentItem)
				return
			}
			query := buildOptimizedQuery(meta.Name, meta.AltTitles, meta.Year)
			torrents, searchErr = bitmagnet.SearchTorrents(ctx, query, contentType, 100)
			if searchErr == nil && len(torrents) > 0 {
				searchCache.Set(searchCacheKey, torrents)
			}
		}()

		go func() {
			defer wg.Done()
			if debrid.LoadProvider().IsEnabled() && config.DebridProvider != "" {
				rows, err := db.Pool.QueryContext(ctx,
					"SELECT infohash, torrent_info_json, language, quality, seeders FROM torrents WHERE tmdb_id = ?1 AND content_type = ?2 AND torrent_info_json IS NOT NULL AND provider = ?3",
					imdbID, typ, config.DebridProvider)
				if err == nil && rows != nil {
					defer rows.Close()
					for rows.Next() {
						var infohash, language, quality string
						var torrentInfoJSON []byte
						var seeders int32
						rows.Scan(&infohash, &torrentInfoJSON, &language, &quality, &seeders)
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
			}
		}()

		wg.Wait()

		if searchErr != nil || len(torrents) == 0 {
			json.NewEncoder(w).Encode(StreamResponse{Streams: []Stream{}})
			return
		}
	} else {
		if debrid.LoadProvider().IsEnabled() && config.DebridProvider != "" {
			rows, err := db.Pool.QueryContext(ctx,
				"SELECT infohash, torrent_info_json, language, quality, seeders FROM torrents WHERE tmdb_id = ?1 AND content_type = ?2 AND torrent_info_json IS NOT NULL AND provider = ?3",
				imdbID, typ, config.DebridProvider)
			if err == nil && rows != nil {
				defer rows.Close()
				for rows.Next() {
					var infohash, language, quality string
					var torrentInfoJSON []byte
					var seeders int32
					rows.Scan(&infohash, &torrentInfoJSON, &language, &quality, &seeders)
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
		}
	}

	var resultStreams, cachedStreams []matcher.Stream
	if typ == "series" {
		sVal := season
		var sPadded string
		if sVal < 10 {
			sPadded = fmt.Sprintf("S0%d", sVal)
		} else {
			sPadded = fmt.Sprintf("S%d", sVal)
		}

		tmdbID := meta.TMDBID
		if tmdbID == "" {
			if id, err := metadata.ResolveTMDBID(ctx, imdbID, typ); err == nil {
				tmdbID = id
			}
		}

		var seasonDetails *metadata.TVSeasonResult
		if tmdbID != "" {
			if sDetails, err := metadata.GetTVSeasonDetails(ctx, tmdbID, season); err == nil {
				seasonDetails = sDetails
			}
		}

		isLongRunning := false
		var airDate string
		if seasonDetails != nil {
			for _, ep := range seasonDetails.Episodes {
				if ep.EpisodeNumber == episode {
					airDate = ep.AirDate
					break
				}
			}
			nameLower := strings.ToLower(meta.Name)
			if strings.Contains(nameLower, "cooku with comali") || 
			   strings.Contains(nameLower, "the daily show") || 
			   strings.Contains(nameLower, "tonight show") || 
			   strings.Contains(nameLower, "jimmy kimmel") || 
			   strings.Contains(nameLower, "late show") || 
			   strings.Contains(nameLower, "saturday night live") ||
			   strings.Contains(nameLower, "jeopardy") ||
			   strings.Contains(nameLower, "wheel of fortune") ||
			   len(seasonDetails.Episodes) > 20 {
				isLongRunning = true
			}
		}

		var refinedTorrents, broadTorrents []bitmagnet.TorrentItem
		var refinedMu, broadMu sync.Mutex
		g, gCtx := errgroup.WithContext(ctx)

		if isLongRunning {
			// Refined: Absolute date and SXXEXX / SXEX combinations
			g.Go(func() error {
				query := buildOptimizedQuery(meta.Name, meta.AltTitles, fmt.Sprintf("%sE%02d", sPadded, episode))
				res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
				if err == nil {
					refinedMu.Lock()
					refinedTorrents = append(refinedTorrents, res...)
					refinedMu.Unlock()
				}
				return err
			})
			g.Go(func() error {
				query := buildOptimizedQuery(meta.Name, meta.AltTitles, fmt.Sprintf("S%dE%d", season, episode))
				res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
				if err == nil {
					refinedMu.Lock()
					refinedTorrents = append(refinedTorrents, res...)
					refinedMu.Unlock()
				}
				return err
			})
			if airDate != "" {
				parts := strings.Split(airDate, "-")
				if len(parts) == 3 {
					y, m, d := parts[0], parts[1], parts[2]
					dateFormats := []string{
						fmt.Sprintf("%s.%s.%s", y, m, d),
						fmt.Sprintf("%s-%s-%s", y, m, d),
						fmt.Sprintf("%s %s %s", y, m, d),
					}
					for _, fmtStr := range dateFormats {
						fmtStr := fmtStr
						g.Go(func() error {
							query := buildOptimizedQuery(meta.Name, meta.AltTitles, fmtStr)
							res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
							if err == nil {
								refinedMu.Lock()
								refinedTorrents = append(refinedTorrents, res...)
								refinedMu.Unlock()
							}
							return err
						})
					}
				}
			}

			// Broad: Season variations
			g.Go(func() error {
				query := buildOptimizedQuery(meta.Name, meta.AltTitles, fmt.Sprintf("Season %d", season))
				res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
				if err == nil {
					broadMu.Lock()
					broadTorrents = append(broadTorrents, res...)
					broadMu.Unlock()
				}
				return err
			})
			g.Go(func() error {
				query := buildOptimizedQuery(meta.Name, meta.AltTitles, sPadded)
				res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
				if err == nil {
					broadMu.Lock()
					broadTorrents = append(broadTorrents, res...)
					broadMu.Unlock()
				}
				return err
			})
			g.Go(func() error {
				query := buildOptimizedQuery(meta.Name, meta.AltTitles, fmt.Sprintf("S%d", season))
				res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
				if err == nil {
					broadMu.Lock()
					broadTorrents = append(broadTorrents, res...)
					broadMu.Unlock()
				}
				return err
			})
		} else {
			// Regular Series: Follows existing year patterns with no-year fallback queries
			// 1. Refined with Year
			g.Go(func() error {
				suffix := fmt.Sprintf("%sE%02d", sPadded, episode)
				if meta.Year != "" {
					suffix = fmt.Sprintf("%s %s", suffix, meta.Year)
				}
				query := buildOptimizedQuery(meta.Name, meta.AltTitles, suffix)
				res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
				if err == nil {
					refinedMu.Lock()
					refinedTorrents = append(refinedTorrents, res...)
					refinedMu.Unlock()
				}
				return err
			})

			// 2. Refined WITHOUT Year (Ensures newer seasons of decade-spanning shows match)
			if meta.Year != "" {
				g.Go(func() error {
					suffix := fmt.Sprintf("%sE%02d", sPadded, episode)
					query := buildOptimizedQuery(meta.Name, meta.AltTitles, suffix)
					res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
					if err == nil {
						refinedMu.Lock()
						refinedTorrents = append(refinedTorrents, res...)
						refinedMu.Unlock()
					}
					return err
				})
			}

			// 3. Broad with Year
			g.Go(func() error {
				suffix := sPadded
				if meta.Year != "" {
					suffix = fmt.Sprintf("%s %s", suffix, meta.Year)
				}
				query := buildOptimizedQuery(meta.Name, meta.AltTitles, suffix)
				res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
				if err == nil {
					broadMu.Lock()
					broadTorrents = append(broadTorrents, res...)
					broadMu.Unlock()
				}
				return err
			})

			// 4. Broad WITHOUT Year (Ensures newer season packs match)
			if meta.Year != "" {
				g.Go(func() error {
					suffix := sPadded
					query := buildOptimizedQuery(meta.Name, meta.AltTitles, suffix)
					res, err := bitmagnet.SearchTorrents(gCtx, query, "tv_show", 100)
					if err == nil {
						broadMu.Lock()
						broadTorrents = append(broadTorrents, res...)
						broadMu.Unlock()
					}
					return err
				})
			}
		}

		_ = g.Wait()

		refinedTorrents = deduplicateTorrents(refinedTorrents)
		broadTorrents = deduplicateTorrents(broadTorrents)

		refinedResult, refinedCached := matcher.FindBestSeriesStreamsLongRunning(ctx, &bitmagnet.TorrentItem{Title: meta.Name, PublishedAt: meta.Year}, meta.AltTitles, season, episode, refinedTorrents, cachedRows, config.PreferredLanguages, isLongRunning, airDate)
		resultStreams = refinedResult
		cachedStreams = refinedCached

		if len(refinedTorrents) < 10 || len(resultStreams) == 0 {
			broadResult, broadCached := matcher.FindBestSeriesStreamsLongRunning(ctx, &bitmagnet.TorrentItem{Title: meta.Name, PublishedAt: meta.Year}, meta.AltTitles, season, episode, broadTorrents, cachedRows, config.PreferredLanguages, isLongRunning, airDate)
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
		movieResult, movieCached := matcher.FindBestMovieStreams(ctx, &bitmagnet.TorrentItem{Title: meta.Name}, meta.AltTitles, meta.Year, torrents, cachedRows, config.PreferredLanguages)
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
						if f.FileType == "video" || isVideoFile(f.Path) {
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
			providerLabel := "RD"
			if config.DebridProvider == "torbox" {
				providerLabel = "TB"
			}

			langFlag := strings.ToUpper(s.Language)
			matchedBadges := parser.FormatBadges(s.TorrentName)

			// Formulate Stream Name (the button text)
			streamName := fmt.Sprintf("[%s %s] %s | %s | %s", prefix, providerLabel, langFlag, strings.ToUpper(s.Quality), utils.FormatSize(s.Size))

			// Formulate Stream Title (the description block) with Option B de-cluttered layout + Sanitized Filename
			var titleBuilder strings.Builder
			if typ == "series" {
				packOrRange := checkPackOrRange(s.TorrentName, episode)
				titleBuilder.WriteString(fmt.Sprintf("📺 %s S%02dE%02d%s\n", meta.Name, season, episode, packOrRange))
			} else {
				if meta.Year != "" {
					titleBuilder.WriteString(fmt.Sprintf("🎬 %s (%s)\n", meta.Name, meta.Year))
				} else {
					titleBuilder.WriteString(fmt.Sprintf("🎬 %s\n", meta.Name))
				}
			}

			// Clean actual filename with website prefixes and subdomains dynamically stripped
			cleanedFileName := parser.SanitizeName(s.TorrentName)
			titleBuilder.WriteString(fmt.Sprintf("📦 %s\n", cleanedFileName))

			if matchedBadges != "" {
				titleBuilder.WriteString(fmt.Sprintf("✨ %s\n", matchedBadges))
			}
			titleBuilder.WriteString(fmt.Sprintf("⚡ Peer Health: 👤 %d seeders", s.Seeders))

			url := fmt.Sprintf("%s/%s/stream/%s/%s/%s", config.AppHost, config.AddonID, typ, id, s.InfoHash)
			streams = append(streams, Stream{
				Name:  streamName,
				Title: titleBuilder.String(),
				URL:   url,
			})
		}
	} else {
		for _, s := range sorted {
			bh := map[string]interface{}{"notWebReady": true}
			if typ == "series" {
				bh["bingeGroup"] = imdbID
			}

			langFlag := strings.ToUpper(s.Language)
			matchedBadges := parser.FormatBadges(s.TorrentName)

			// Formulate Stream Name (the button text)
			streamName := fmt.Sprintf("[🧲 P2P] %s | %s | %s", langFlag, strings.ToUpper(s.Quality), utils.FormatSize(s.Size))

			// Formulate Stream Title (the description block) with Option B de-cluttered layout + Sanitized Filename
			var titleBuilder strings.Builder
			if typ == "series" {
				packOrRange := checkPackOrRange(s.TorrentName, episode)
				titleBuilder.WriteString(fmt.Sprintf("📺 %s S%02dE%02d%s\n", meta.Name, season, episode, packOrRange))
			} else {
				if meta.Year != "" {
					titleBuilder.WriteString(fmt.Sprintf("🎬 %s (%s)\n", meta.Name, meta.Year))
				} else {
					titleBuilder.WriteString(fmt.Sprintf("🎬 %s\n", meta.Name))
				}
			}

			// Clean actual filename with website prefixes and subdomains dynamically stripped
			cleanedFileName := parser.SanitizeName(s.TorrentName)
			titleBuilder.WriteString(fmt.Sprintf("📦 %s\n", cleanedFileName))

			if matchedBadges != "" {
				titleBuilder.WriteString(fmt.Sprintf("✨ %s\n", matchedBadges))
			}
			titleBuilder.WriteString(fmt.Sprintf("⚡ Peer Health: 👤 %d seeders", s.Seeders))

			streams = append(streams, Stream{
				Name:          streamName,
				Title:         titleBuilder.String(),
				InfoHash:      s.InfoHash,
				FileIdx:       s.FileIndex,
				BehaviorHints: bh,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(StreamResponse{Streams: streams})
}
