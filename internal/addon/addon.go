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

// cleanQueryTitle removes punctuation that breaks PostgreSQL FTS tokenization.
// We do NOT wrap in quotes - unquoted terms use implicit AND which is more forgiving.
func cleanQueryTitle(name string) string {
	s := queryReplacer.Replace(name)
	s = strings.ReplaceAll(s, ":", " ")
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "(", " ")
	s = strings.ReplaceAll(s, ")", " ")
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// buildQueryVariants generates multiple search query strings to maximize recall.
// CRITICAL FIXES:
// 1. NEVER append year to FTS query - year is absent from many torrent names and causes zero results
// 2. NEVER wrap in double quotes - prevents <-> adjacency operator stop-word poisoning
// 3. Use PostgreSQL websearch negation syntax "-" (not "!")
// 4. Generate multiple episode format variants to handle scene naming differences
func buildQueryVariants(metaName string, altTitles []string, season, episode int, contentType string) []string {
	base := cleanQueryTitle(metaName)
	if base == "" {
		return nil
	}

	var variants []string
	sPadded := fmt.Sprintf("S%02d", season)
	ePadded := fmt.Sprintf("E%02d", episode)

	// --- TV Show Variants ---
	if contentType == "tv_show" && season > 0 && episode > 0 {
		// Variant 1: Compact S07E22 (most common scene format)
		variants = append(variants, fmt.Sprintf("%s %s%s", base, sPadded, ePadded))
		// Variant 2: Spaced S07 EP22 (matches 1TamilMV, ETTV, etc.)
		variants = append(variants, fmt.Sprintf("%s %s %s", base, sPadded, ePadded))
		// Variant 3: S07 E22 (alternate spacing)
		variants = append(variants, fmt.Sprintf("%s %s E%02d", base, sPadded, episode))
		// Variant 4: Season 7 Episode 22 (verbose format)
		variants = append(variants, fmt.Sprintf("%s Season %d Episode %d", base, season, episode))
		// Variant 5: Season pack broad query (no episode)
		variants = append(variants, fmt.Sprintf("%s %s", base, sPadded))
		// Variant 6: Title-only broad fallback (catches packs and mislabeled episodes)
		variants = append(variants, base)
	} else {
		// Movie or unknown episode
		variants = append(variants, base)
	}

	// Add alt titles as separate variants (without episode specs for broader matching)
	for _, alt := range altTitles {
		altClean := cleanQueryTitle(alt)
		if altClean != "" && altClean != base {
			variants = append(variants, altClean)
		}
	}

	// Append negation keywords to ALL variants using CORRECT PostgreSQL syntax "-"
	if len(config.NegateKeywords) > 0 {
		var negations []string
		for _, k := range config.NegateKeywords {
			negations = append(negations, fmt.Sprintf("-%s", queryReplacer.Replace(k)))
		}
		negationSuffix := strings.Join(negations, " ")
		for i := range variants {
			variants[i] = variants[i] + " " + negationSuffix
		}
	}

	return variants
}

// DEPRECATED: Kept for backward compatibility during transition.
// Use buildQueryVariants for all new code.
func buildOptimizedQuery(name string, altTitles []string, suffix string) string {
	nameClean := cleanQueryTitle(name)

	query := nameClean
	if suffix != "" {
		suffixClean := queryReplacer.Replace(suffix)
		query = fmt.Sprintf("%s %s", query, suffixClean)
	}

	if len(config.NegateKeywords) > 0 {
		var negations []string
		for _, k := range config.NegateKeywords {
			negations = append(negations, fmt.Sprintf("-%s", queryReplacer.Replace(k)))
		}
		query = fmt.Sprintf("%s %s", query, strings.Join(negations, " "))
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
			// CRITICAL FIX: Do NOT append year to movie query.
			// The matcher handles year filtering post-search.
			// Year in FTS query excludes torrents that don't have year in filename.
			query := buildOptimizedQuery(meta.Name, meta.AltTitles, "")
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
		// --- SERIES SEARCH (COMPLETE REPLACEMENT) ---
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

		// Generate multiple query variants to maximize recall
		queryVariants := buildQueryVariants(meta.Name, meta.AltTitles, season, episode, contentType)
		utils.Logger.Info("generated search variants", "count", len(queryVariants), "variants", queryVariants)

		var allTorrents []bitmagnet.TorrentItem
		seenHashes := make(map[string]bool)
		var mu sync.Mutex

		// Execute all variants with a worker pool (max 3 concurrent to avoid overwhelming Bitmagnet)
		g, gCtx := errgroup.WithContext(ctx)
		sem := semaphore.NewWeighted(3)

		for _, qv := range queryVariants {
			qv := qv // capture range variable
			g.Go(func() error {
				if err := sem.Acquire(gCtx, 1); err != nil {
					return err
				}
				defer sem.Release(1)

				// Search WITHOUT contentType filter to maximize recall across all classified types
				results, err := bitmagnet.SearchTorrents(gCtx, qv, "", 50)
				if err == nil && len(results) > 0 {
					mu.Lock()
					for _, t := range results {
						if !seenHashes[t.InfoHash] {
							allTorrents = append(allTorrents, t)
							seenHashes[t.InfoHash] = true
						}
					}
					mu.Unlock()
				}
				return nil
			})
		}

		_ = g.Wait()
		torrents = allTorrents
		utils.Logger.Info("total unique torrents after variant search", "count", len(torrents))
	}

	var resultStreams, cachedStreams []matcher.Stream
	if typ == "series" {
		// Pass the retrieved primary title & its compiled alternate titles list to the matcher.
		// We use PublishedAt inside TorrentItem as a zero-allocation vector to pass the premiere year.
		resultStreams, cachedStreams = matcher.FindBestSeriesStreams(ctx, &bitmagnet.TorrentItem{Title: meta.Name, PublishedAt: meta.Year}, meta.AltTitles, season, episode, torrents, cachedRows, config.PreferredLanguages)
	} else {
		resultStreams, cachedStreams = matcher.FindBestMovieStreams(ctx, &bitmagnet.TorrentItem{Title: meta.Name}, meta.AltTitles, meta.Year, torrents, cachedRows, config.PreferredLanguages)
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
