package matcher

import (
	"fmt"
	"strings"
	"sync"

	"github.com/user/stremio-bitgraph-go/internal/bitmagnet"
	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/parser"
	"github.com/user/stremio-bitgraph-go/internal/utils"
	"github.com/xrash/smetrics"
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

func getTitleSimilarity(tmdbTitle, torrentName string) float64 {
	if tmdbTitle == "" {
		return 0
	}
	parsed := parser.RobustParseInfo(torrentName, 0)
	if parsed.Title == "" {
		return 0
	}
	return smetrics.JaroWinkler(strings.ToLower(tmdbTitle), strings.ToLower(parsed.Title), 0.7, 4)
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

	for _, torrent := range newTorrents {
		if cachedHashes[torrent.InfoHash] {
			continue
		}
		torrentData := torrent.Torrent
		if torrentData.Name == "" {
			continue
		}
		sim := getTitleSimilarity(tmdbShow.Title, torrentData.Name)
		utils.Logger.Debug("evaluating series torrent", "name", torrentData.Name, "similarity", fmt.Sprintf("%.2f", sim))
		if sim < config.SimilarityThreshold {
			continue
		}

		bestLang := getBestLanguage(torrent.Languages, preferredLanguages)
		parsed := parser.RobustParseInfo(torrentData.Name, 0)

		if parsed.Season == season && parsed.Episode == episode {
			streams = append(streams, Stream{
				InfoHash:    torrent.InfoHash,
				FileIndex:   0,
				TorrentName: torrentData.Name,
				Seeders:     torrent.Seeders,
				Language:    bestLang,
				Quality:     utils.GetQuality(torrent.VideoResolution),
				Size:        torrentData.Size,
				IsCached:    false,
			})
			continue
		}

		if parsed.Season != 0 && parsed.Episode != 0 {
			continue
		}

		if parsed.Season == season {
			if !torrentData.HasFilesInfo {
				utils.Logger.Warn("rejected: hasFilesInfo=false", "name", torrentData.Name)
				continue
			}
			if torrentData.FilesStatus == "single" {
				streams = append(streams, Stream{
					InfoHash:    torrent.InfoHash,
					FileIndex:   0,
					TorrentName: torrentData.Name,
					Seeders:     torrent.Seeders,
					Language:    bestLang,
					Quality:     utils.GetQuality(torrent.VideoResolution),
					Size:        torrentData.Size,
					IsCached:    false,
				})
			} else if torrentData.FilesStatus == "multi" {
				files, err := bitmagnet.GetTorrentFiles(ctx, torrent.InfoHash)
				if err != nil || len(files) == 0 {
					continue
				}
				var videoFiles []bitmagnet.TorrentFile
				for _, f := range files {
					if f.FileType == "video" {
						videoFiles = append(videoFiles, f)
					}
				}
				if len(videoFiles) == 0 {
					continue
				}
				found := false
				for _, vf := range videoFiles {
					fileInfo := parser.ParseFilePath(vf.Path, parsed.Season)
					if fileInfo.Season == season && fileInfo.Episode == episode {
						streams = append(streams, Stream{
							InfoHash:    torrent.InfoHash,
							FileIndex:   vf.Index,
							TorrentName: torrentData.Name,
							Seeders:     torrent.Seeders,
							Language:    bestLang,
							Quality:     utils.GetQuality(torrent.VideoResolution),
							Size:        torrentData.Size,
							IsCached:    false,
						})
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
		}
	}
	return streams, cachedStreams
}

func FindBestMovieStreams(tmdbMovie *bitmagnet.TorrentItem, newTorrents []bitmagnet.TorrentItem, cachedRows []map[string]interface{}, preferredLanguages []string) (streams []Stream, cachedStreams []Stream) {
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

	for _, torrent := range newTorrents {
		if cachedHashes[torrent.InfoHash] {
			continue
		}
		torrentData := torrent.Torrent
		if torrentData.Name == "" {
			continue
		}
		sim := getTitleSimilarity(tmdbMovie.Title, torrentData.Name)
		if sim < config.SimilarityThreshold {
			continue
		}
		parsed := parser.RobustParseInfo(torrentData.Name, 0)
		yearMatch := true
		if parsed.Year != 0 && tmdbMovie.Torrent.Name != "" {
			// We don't have release_date in TorrentItem; use tmdbMovie.Year if available
			// Actually the original passes movieMetaForMatcher with release_date
			// In our Go version, we'll pass the year separately
		}
		if !yearMatch {
			continue
		}
		bestLang := getBestLanguage(torrent.Languages, preferredLanguages)
		streams = append(streams, Stream{
			InfoHash:    torrent.InfoHash,
			TorrentName: torrentData.Name,
			Seeders:     torrent.Seeders,
			Language:    bestLang,
			Quality:     utils.GetQuality(torrent.VideoResolution),
			Size:        torrentData.Size,
			IsCached:    false,
		})
	}
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

	// sort
	for i := 0; i < len(all)-1; i++ {
		for j := i + 1; j < len(all); j++ {
			a, b := all[i], all[j]
			swap := false
			if a.IsCached && !b.IsCached {
				swap = false
			} else if !a.IsCached && b.IsCached {
				swap = true
			} else {
				la, lb := getLangPriority(a.Language), getLangPriority(b.Language)
				if la != lb {
					swap = la > lb
				} else {
					qa, qb := utils.QualityOrder[a.Quality], utils.QualityOrder[b.Quality]
					if qa != qb {
						swap = qa > qb
					} else {
						swap = a.Seeders < b.Seeders
					}
				}
			}
			if swap {
				all[i], all[j] = all[j], all[i]
			}
		}
	}

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

// ProcessingLock is used by the API server to prevent duplicate debrid calls for the same hash.
type ProcessingLock struct {
	State       string // PROCESSING, COMPLETED, FAILED
	Data        map[string]interface{}
	DownloadURL string
	Error       error
	Promise     chan struct{}
	Once        sync.Once
}

var ProcessingLocks sync.Map
