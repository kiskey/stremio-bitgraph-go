
package matcher

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/user/stremio-bitgraph-go/internal/bitmagnet"
	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/parser"
	"github.com/user/stremio-bitgraph-go/internal/utils"
	"github.com/xrash/smetrics"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

type Stream struct {
	InfoHash    string
	FileIndex   int
	TorrentName string
	Seeders     int
	Language    string
	Quality     string
	Size        int64
	IsCached    bool
}

// isBlockedArchive checks if a torrent name is a compressed archive that Stremio cannot play
func isBlockedArchive(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".rar") ||
		strings.HasSuffix(lower, ".zip") ||
		strings.HasSuffix(lower, ".7z") ||
		strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".tgz") ||
		strings.HasSuffix(lower, ".gz")
}

func containsNonASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

func stripLeadingArticles(s string) string {
	s = strings.TrimSpace(s)
	articles := []string{"the ", "a ", "an ", "le ", "la ", "les ", "l'"}
	for _, art := range articles {
		if strings.HasPrefix(s, art) {
			return strings.TrimPrefix(s, art)
		}
	}
	return s
}

func getTitleSimilarity(tmdbTitle, torrentName string) float64 {
	if tmdbTitle == "" {
		return 0
	}
	parsed := parser.RobustParseInfo(torrentName, 0)
	if parsed.Title == "" {
		return 0
	}
	
	cleanTmdb := strings.Trim(strings.ToLower(tmdbTitle), " .-_[]()/\\")
	cleanParsed := strings.Trim(strings.ToLower(parsed.Title), " .-_[]()/\\")

	jw := smetrics.JaroWinkler(cleanTmdb, cleanParsed, 0.7, 4)
	if jw >= config.SimilarityThreshold {
		return jw
	}
	
	// Fallback Normalization: strip grammatical leading articles to match titles like "The Dark Knight" with "Dark Knight"
	cleanTmdbNoArt := stripLeadingArticles(cleanTmdb)
	cleanParsedNoArt := stripLeadingArticles(cleanParsed)
	if cleanTmdbNoArt != cleanTmdb || cleanParsedNoArt != cleanParsed {
		jwClean := smetrics.JaroWinkler(cleanTmdbNoArt, cleanParsedNoArt, 0.7, 4)
		if jwClean >= config.SimilarityThreshold {
			return jwClean
		}
	}
	
	return jw
}

func getBestLanguage(torrentLanguages []struct{ ID string }, preferredLanguages []string) string {
	if len(torrentLanguages) == 0 {
		return "en"
	}
	if len(preferredLanguages) > 0 {
		for _, pref := range preferredLanguages {
			for _, l := range torrentLanguages {
				if l.ID == pref {
					return pref
				}
			}
		}
	}
	return torrentLanguages[0].ID
}

func findFileInTorrentInfo(torrentInfo map[string]interface{}, season, episode int) bool {
	filename := ""
	if fn, ok := torrentInfo["filename"].(string); ok {
		filename = fn
	}
	parsed := parser.RobustParseInfo(filename, 0)
	fallbackSeason := parsed.Season

	filesRaw, ok := torrentInfo["files"].([]interface{})
	if !ok {
		return false
	}
	for _, f := range filesRaw {
		fileMap, ok := f.(map[string]interface{})
		if !ok {
			continue
		}
		path, _ := fileMap["path"].(string)
		fileInfo := parser.ParseFilePath(path, fallbackSeason)
		if fileInfo.Season == season && fileInfo.Episode == episode {
			return true
		}
	}
	return false
}

func fetchTorrentFilesConcurrent(ctx context.Context, torrents []bitmagnet.TorrentItem) map[string][]bitmagnet.TorrentFile {
	var mu sync.Mutex
	result := make(map[string][]bitmagnet.TorrentFile)

	g, ctx := errgroup.WithContext(ctx)
	// Bounded concurrent workers for local Bitmagnet I/O
	sem := semaphore.NewWeighted(6)

	for _, t := range torrents {
		if t.Torrent.FilesStatus != "multi" || !t.Torrent.HasFilesInfo {
			continue
		}
		t := t
		g.Go(func() error {
			if err := sem.Acquire(ctx, 1); err != nil {
				return err
			}
			defer sem.Release(1)

			files, err := bitmagnet.GetTorrentFiles(ctx, t.InfoHash)
			if err != nil {
				return nil
			}
			mu.Lock()
			result[t.InfoHash] = files
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return result
}

func FindBestSeriesStreams(ctx context.Context, tmdbShow *bitmagnet.TorrentItem, season, episode int, newTorrents []bitmagnet.TorrentItem, cachedRows []map[string]interface{}, preferredLanguages []string) (streams []Stream, cachedStreams []Stream) {
	for _, torrent := range cachedRows {
		if findFileInTorrentInfo(torrent, season, episode) {
			infoHash, _ := torrent["infohash"].(string)
			lang, _ := torrent["language"].(string)
			quality, _ := torrent["quality"].(string)
			seeders := 0
			if s, ok := torrent["seeders"].(int32); ok {
				seeders = int(s)
			}
			var size int64
			if tinfo, ok := torrent["torrent_info_json"].(map[string]interface{}); ok {
				if bytes, ok := tinfo["bytes"].(float64); ok {
					size = int64(bytes)
				}
				if fn, ok := tinfo["filename"].(string); ok {
					cachedStreams = append(cachedStreams, Stream{
						InfoHash:    infoHash,
						TorrentName: fn,
						Seeders:     seeders,
						Language:    lang,
						Quality:     quality,
						Size:        size,
						IsCached:    true,
					})
				}
			}
		}
	}

	cachedHashes := make(map[string]bool)
	for _, t := range cachedRows {
		if h, ok := t["infohash"].(string); ok {
			cachedHashes[h] = true
		}
	}

	var multiFileTorrents []bitmagnet.TorrentItem
	for _, torrent := range newTorrents {
		if cachedHashes[torrent.InfoHash] {
			continue
		}
		if torrent.Torrent.FilesStatus == "multi" && torrent.Torrent.HasFilesInfo {
			multiFileTorrents = append(multiFileTorrents, torrent)
		}
	}

	filesMap := fetchTorrentFilesConcurrent(ctx, multiFileTorrents)

	// Pre-generate case-insensitive matching substrings for SIMD acceleration
	epStr := fmt.Sprintf("e%02d", episode)     // e.g. "e03"
	epStrShort := fmt.Sprintf("e%d", episode)   // e.g. "e3"
	epStrX := fmt.Sprintf("x%02d", episode)     // e.g. "1x03"
	epStrXShort := fmt.Sprintf("x%d", episode)  // e.g. "1x3"
	epNumStr := fmt.Sprintf("%02d", episode)    // e.g. "03"
	epSingleStr := fmt.Sprintf("%d", episode)   // e.g. "3"

	var mu sync.Mutex
	g, gCtx := errgroup.WithContext(ctx)
	
	// Bound total concurrent workers to physical CPU cores
	g.SetLimit(runtime.NumCPU())

	for _, torrent := range newTorrents {
		if cachedHashes[torrent.InfoHash] {
			continue
		}
		torrentData := torrent.Torrent
		if torrentData.Name == "" {
			continue
		}

		// Filter out compressed archives immediately to prevent downstream debrid resolution overhead
		if isBlockedArchive(torrentData.Name) {
			utils.Logger.Warn("filtering out series torrent: matches compressed archive pattern", "name", torrentData.Name, "hash", torrent.InfoHash)
			continue
		}

		t := torrent
		td := torrentData

		g.Go(func() error {
			select {
			case <-gCtx.Done():
				return gCtx.Err()
			default:
			}

			sim := getTitleSimilarity(tmdbShow.Title, td.Name)
			utils.Logger.Debug("evaluating series torrent", "name", td.Name, "similarity", fmt.Sprintf("%.2f", sim))
			if sim < config.SimilarityThreshold {
				return nil
			}

			bestLang := getBestLanguage(t.Languages, preferredLanguages)
			parsed := parser.RobustParseInfo(td.Name, 0)
			if bestLang == "en" && parsed.Language != "en" && parsed.Language != "" {
				bestLang = parsed.Language
			}
			quality := utils.GetQuality(t.VideoResolution)
			if (quality == "sd" || quality == "") && parsed.Quality != "sd" && parsed.Quality != "" {
				quality = parsed.Quality
			}

			if parsed.Season == season && parsed.Episode == episode {
				mu.Lock()
				streams = append(streams, Stream{
					InfoHash:    t.InfoHash,
					FileIndex:   0,
					TorrentName: td.Name,
					Seeders:     t.Seeders,
					Language:    bestLang,
					Quality:     quality,
					Size:        td.Size,
					IsCached:    false,
				})
				mu.Unlock()
				return nil
			}

			if parsed.Season != 0 && parsed.Episode != 0 {
				return nil
			}

			if parsed.Season == season {
				if td.FilesStatus == "single" {
					mu.Lock()
					streams = append(streams, Stream{
						InfoHash:    t.InfoHash,
						FileIndex:   0,
						TorrentName: td.Name,
						Seeders:     t.Seeders,
						Language:    bestLang,
						Quality:     quality,
						Size:        td.Size,
						IsCached:    false,
					})
					mu.Unlock()
				} else if td.FilesStatus == "multi" {
					files, ok := filesMap[t.InfoHash]
					if !ok || len(files) == 0 {
						return nil
					}
					var videoFiles []bitmagnet.TorrentFile
					for _, f := range files {
						if f.FileType == "video" {
							videoFiles = append(videoFiles, f)
						}
					}
					if len(videoFiles) == 0 {
						return nil
					}
					for _, vf := range videoFiles {
						lowerPath := strings.ToLower(vf.Path)

						// VECTORIZED SIMD PRUNING:
						// Bypass expensive regex VM if Assembly-level search confirms no episode markers
						hasEpisode := strings.Contains(lowerPath, epStr) ||
							strings.Contains(lowerPath, epStrShort) ||
							strings.Contains(lowerPath, epStrX) ||
							strings.Contains(lowerPath, epStrXShort) ||
							strings.Contains(lowerPath, "/"+epNumStr) ||
							strings.Contains(lowerPath, " "+epNumStr) ||
							strings.Contains(lowerPath, "-"+epNumStr) ||
							strings.Contains(lowerPath, "_"+epNumStr) ||
							strings.Contains(lowerPath, "."+epNumStr)

						// Critical Single-Digit Fallback: prevents false-pruning of S013, S01-3, S01_3, S01.3
						if episode < 10 {
							hasEpisode = hasEpisode ||
								strings.Contains(lowerPath, "/"+epSingleStr) ||
								strings.Contains(lowerPath, " "+epSingleStr) ||
								strings.Contains(lowerPath, "-"+epSingleStr) ||
								strings.Contains(lowerPath, "_"+epSingleStr) ||
								strings.Contains(lowerPath, "."+epSingleStr)
						}

						if !hasEpisode {
							continue
						}

						fileInfo := parser.ParseFilePath(vf.Path, parsed.Season)
						if fileInfo.Season == season && fileInfo.Episode == episode {
							mu.Lock()
							streams = append(streams, Stream{
								InfoHash:    t.InfoHash,
								FileIndex:   vf.Index,
								TorrentName: td.Name,
								Seeders:     t.Seeders,
								Language:    bestLang,
								Quality:     quality,
								Size:        td.Size,
								IsCached:    false,
							})
							mu.Unlock()
							break
						}
					}
				}
			}
			return nil
		})
	}

	_ = g.Wait()
	return streams, cachedStreams
}

func FindBestMovieStreams(ctx context.Context, tmdbMovie *bitmagnet.TorrentItem, tmdbYear string, newTorrents []bitmagnet.TorrentItem, cachedRows []map[string]interface{}, preferredLanguages []string) (streams []Stream, cachedStreams []Stream) {
	for _, torrent := range cachedRows {
		infoHash, _ := torrent["infohash"].(string)
		lang, _ := torrent["language"].(string)
		quality, _ := torrent["quality"].(string)
		seeders := 0
		if s, ok := torrent["seeders"].(int32); ok {
			seeders = int(s)
		}
		var size int64
		if tinfo, ok := torrent["torrent_info_json"].(map[string]interface{}); ok {
			if bytes, ok := tinfo["bytes"].(float64); ok {
				size = int64(bytes)
			}
			if fn, ok := tinfo["filename"].(string); ok {
				cachedStreams = append(cachedStreams, Stream{
					InfoHash:    infoHash,
					TorrentName: fn,
					Seeders:     seeders,
					Language:    lang,
					Quality:     quality,
					Size:        size,
					IsCached:    true,
				})
			}
		}
	}

	cachedHashes := make(map[string]bool)
	for _, t := range cachedRows {
		if h, ok := t["infohash"].(string); ok {
			cachedHashes[h] = true
		}
	}

	var multiFileTorrents []bitmagnet.TorrentItem
	for _, torrent := range newTorrents {
		if cachedHashes[torrent.InfoHash] {
			continue
		}
		if torrent.Torrent.FilesStatus == "multi" && torrent.Torrent.HasFilesInfo {
			multiFileTorrents = append(multiFileTorrents, torrent)
		}
	}

	filesMap := fetchTorrentFilesConcurrent(ctx, multiFileTorrents)

	var mu sync.Mutex
	g, gCtx := errgroup.WithContext(ctx)
	
	// Bound concurrency to hardware limits
	g.SetLimit(runtime.NumCPU())

	for _, torrent := range newTorrents {
		if cachedHashes[torrent.InfoHash] {
			continue
		}
		torrentData := torrent.Torrent
		if torrentData.Name == "" {
			continue
		}

		// Filter out compressed archives immediately to prevent downstream debrid resolution overhead
		if isBlockedArchive(torrentData.Name) {
			utils.Logger.Warn("filtering out movie torrent: matches compressed archive pattern", "name", torrentData.Name, "hash", torrent.InfoHash)
			continue
		}

		t := torrent
		td := torrentData

		g.Go(func() error {
			select {
			case <-gCtx.Done():
				return gCtx.Err()
			default:
			}

			sim := getTitleSimilarity(tmdbMovie.Title, td.Name)
			utils.Logger.Debug("evaluating movie torrent", "name", td.Name, "similarity", fmt.Sprintf("%.2f", sim))
			if sim < config.SimilarityThreshold {
				return nil
			}
			
			parsed := parser.RobustParseInfo(td.Name, 0)
			yearMatch := true
			if parsed.Year != 0 && tmdbYear != "" {
				y, err := strconv.Atoi(tmdbYear)
				if err == nil {
					if parsed.Year < y-1 || parsed.Year > y+1 {
						yearMatch = false
					}
				}
			}
			if !yearMatch {
				return nil
			}

			// New Filter: If multi-file and has files list, ensure at least one video file exists
			if td.FilesStatus == "multi" {
				files, ok := filesMap[t.InfoHash]
				if ok && len(files) > 0 {
					hasVideo := false
					for _, f := range files {
						lowerPath := strings.ToLower(f.Path)
						if f.FileType == "video" || 
							strings.HasSuffix(lowerPath, ".mkv") ||
							strings.HasSuffix(lowerPath, ".mp4") ||
							strings.HasSuffix(lowerPath, ".avi") ||
							strings.HasSuffix(lowerPath, ".mov") ||
							strings.HasSuffix(strings.ToLower(f.Path), ".wmv") ||
							strings.HasSuffix(strings.ToLower(f.Path), ".flv") ||
							strings.HasSuffix(strings.ToLower(f.Path), ".webm") {
							hasVideo = true
							break
						}
					}
					if !hasVideo {
						// Filter out completely (prevents unplayable .rar / .exe files from being processed)
						utils.Logger.Warn("filtering out movie torrent: contains no playable video files", "name", td.Name, "hash", t.InfoHash)
						return nil
					}
				}
			}

			bestLang := getBestLanguage(t.Languages, preferredLanguages)
			if bestLang == "en" && parsed.Language != "en" && parsed.Language != "" {
				bestLang = parsed.Language
			}
			quality := utils.GetQuality(t.VideoResolution)
			if (quality == "sd" || quality == "") && parsed.Quality != "sd" && parsed.Quality != "" {
				quality = parsed.Quality
			}

			mu.Lock()
			streams = append(streams, Stream{
				InfoHash:    t.InfoHash,
				TorrentName: td.Name,
				Seeders:     t.Seeders,
				Language:    bestLang,
				Quality:     quality,
				Size:        td.Size,
				IsCached:    false,
			})
			mu.Unlock()
			return nil
		})
	}

	_ = g.Wait()
	return streams, cachedStreams
}

func SortAndFilterStreams(streams, cachedStreams []Stream, preferredLanguages []string) []Stream {
	all := append(cachedStreams, streams...)

	if config.StrictLanguageFilter && len(preferredLanguages) > 0 {
		prefSet := make(map[string]bool)
		for _, l := range preferredLanguages {
			prefSet[l] = true
		}
		var filtered []Stream
		for _, s := range all {
			if prefSet[s.Language] {
				filtered = append(filtered, s)
			}
		}
		all = filtered
		utils.Logger.Debug("strict language filter applied", "kept", len(all))
	}

	langIndex := make(map[string]int)
	for i, l := range preferredLanguages {
		langIndex[l] = i
	}
	getLangPriority := func(lang string) int {
		if i, ok := langIndex[lang]; ok {
			return i
		}
		return 9999
	}

	// Use sort.SliceStable to guarantee 100% deterministic tie-breaks
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.IsCached && !b.IsCached {
			return true
		}
		if !a.IsCached && b.IsCached {
			return false
		}
		la, lb := getLangPriority(a.Language), getLangPriority(b.Language)
		if la != lb {
			return la < lb
		}
		qa, qb := utils.QualityOrder[a.Quality], utils.QualityOrder[b.Quality]
		if qa != qb {
			return qa < qb
		}
		return a.Seeders > b.Seeders
	})

	var final []Stream
	counts := make(map[string]int)
	for _, s := range all {
		key := fmt.Sprintf("%s_%s", s.Language, s.Quality)
		counts[key]++
		if counts[key] <= config.StreamLimitPerQuality {
			final = append(final, s)
		}
	}
	return final
}

type ProcessingLock struct {
	State       string // PROCESSING, COMPLETED, FAILED
	Data        map[string]interface{}
	DownloadURL string
	Error       error
	Promise     chan struct{}
	Once        sync.Once
}

var ProcessingLocks sync.Map
