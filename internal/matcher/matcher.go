package matcher

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/user/stremio-bitgraph-go/internal/bitmagnet"
	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/parser"
	"github.com/user/stremio-bitgraph-go/internal/utils"
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

// Low-Entropy Grammatical Stop Words Set for PN-SILEC Filtering
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"of": true, "in": true, "on": true, "at": true, "to": true,
	"for": true, "with": true, "by": true, "from": true, "aka": true,
	"la": true, "le": true, "les": true, "el": true, "un": true, "une": true,
}

// metadataWords are technical tags that should not trigger the single-word guardrail.
// These are common torrent metadata tokens that appear after the actual movie title.
var metadataWords = map[string]bool{
	"1080p": true, "720p": true, "2160p": true, "480p": true, "360p": true,
	"4k": true, "uhd": true, "bluray": true, "bdrip": true, "brrip": true,
	"webdl": true, "webrip": true, "hdrip": true, "dvdrip": true, "pdtv": true,
	"hdtv": true, "cam": true, "camrip": true, "hdcam": true, "ts": true,
	"hdts": true, "tc": true, "predvd": true, "dvdscr": true, "screener": true,
	"scr": true, "hq": true, "v2": true, "v3": true, "hc": true, "clean": true,
	"imax": true, "h264": true, "x264": true, "h265": true, "x265": true,
	"hevc": true, "aac": true, "aac3": true, "dts": true, "dd51": true,
	"truehd": true, "ac3": true, "mp3": true, "xvid": true, "divx": true,
	"av1": true, "vp9": true, "hdr10": true, "hdr": true, "dv": true,
	"dolby": true, "vision": true, "atmos": true, "dts-hd": true, "ma": true,
	"dual": true, "audio": true, "dubbed": true, "dub": true, "multi": true,
	"hindi": true, "tamil": true, "telugu": true, "malayalam": true,
	"kannada": true, "bengali": true, "marathi": true, "punjabi": true,
	"english": true, "spanish": true, "french": true, "italian": true,
	"russian": true, "korean": true, "japanese": true, "chinese": true,
	"51": true, "71": true, "20": true, "10bit": true, "remux": true,
	"3d": true, "sdr": true, "gb": true, "mb": true, "kb": true,
	"web": true, "dl": true, "hd": true,
}

// sequelIndicators are words that strongly suggest a different franchise entry.
var sequelIndicators = map[string]bool{
	"part": true, "chapter": true, "episode": true, "season": true,
	"volume": true, "vol": true, "book": true, "returns": true,
	"rises": true, "begins": true, "forever": true, "legacy": true,
	"fallout": true, "crusade": true, "dynasty": true, "empire": true,
	"revenge": true, "resurrection": true, "reloaded": true,
	"revolutions": true, "origins": true, "awakens": true,
	"last": true, "final": true, "next": true, "new": true,
}

// homoglyphClasses maps standard stylizations/leetspeak lookalikes to represent equivalence classes.
var homoglyphClasses = map[rune][]rune{
	'0': {'0', 'o'},
	'o': {'0', 'o'},
	'1': {'1', 'i', 'l', '!'},
	'i': {'1', 'i', 'l', '!'},
	'l': {'1', 'i', 'l', '!'},
	'3': {'3', 'e'},
	'e': {'3', 'e'},
	'4': {'4', 'a', '@'},
	'a': {'4', 'a', '@'},
	'5': {'5', 's'},
	's': {'5', 's'},
	'7': {'7', 't', 'v', 'l'},
	't': {'7', 't'},
	'v': {'7', 'v'},
	'8': {'8', 'b'},
	'b': {'8', 'b'},
	'9': {'9', 'g'},
	'g': {'9', 'g'},
}

var writtenNumbers = map[string]string{
	"one": "1", "first": "1", "1st": "1",
	"two": "2", "second": "2", "2nd": "2",
	"three": "3", "third": "3", "3rd": "3",
	"four": "4", "fourth": "4", "4th": "4",
	"five": "5", "fifth": "5", "5th": "5",
	"six": "6", "sixth": "6", "6th": "6",
	"seven": "7", "seventh": "7", "7th": "7",
	"eight": "8", "eighth": "8", "8th": "8",
	"nine": "9", "ninth": "9", "9th": "9",
	"ten": "10", "tenth": "10", "10th": "10",
	"eleven": "11", "eleventh": "11", "11th": "11",
	"twelve": "12", "twelfth": "12", "12th": "12",
}

var sequelContexts = map[string]bool{
	"part": true, "vol": true, "volume": true, "chapter": true,
	"episode": true, "season": true, "act": true, "entry": true,
}

var ignoredNumbers = map[string]bool{
	"1080": true, "2160": true, "720": true, "480": true, "360": true,
	"576": true, "264": true, "265": true, "10": true, "8": true,
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

func cleanWord(w string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.ToLower(w))
}

// passTitleGuardrail prevents single-word titles (e.g. "Up", "It") from matching
// unrelated multi-word torrents (e.g. "Upgraded", "Italian"). It allows metadata
// words (codecs, quality tags, languages) to pass through.
func passTitleGuardrail(targetTitle, parsedTitle string) bool {
	cleanTarget := strings.Trim(strings.ToLower(targetTitle), " .-_[]()/\\")
	cleanParsed := strings.Trim(strings.ToLower(parsedTitle), " .-_[]()/\\")

	if cleanTarget == cleanParsed {
		return true
	}

	targetNoArt := stripLeadingArticles(cleanTarget)
	parsedNoArt := stripLeadingArticles(cleanParsed)
	if targetNoArt == parsedNoArt {
		return true
	}

	targetWords := strings.Fields(targetNoArt)
	parsedWords := strings.Fields(parsedNoArt)

	// ── UPGRADE: PN-SILEC Multi-Word Franchise Leakage Guardrail ──
	if len(targetWords) > 1 && len(parsedWords) > len(targetWords) {
		// Verify if the parsed title starts with the target phrase
		startsSame := true
		for i := 0; i < len(targetWords); i++ {
			if cleanWord(parsedWords[i]) != cleanWord(targetWords[i]) {
				startsSame = false
				break
			}
		}

		if startsSame {
			// Extract the extra trailing tokens (P \ T)
			extraWords := parsedWords[len(targetWords):]
			hasSubstantiveProperNoun := false
			for _, w := range extraWords {
				cw := cleanWord(w)
				if cw == "" {
					continue
				}
				// If the extra word is NOT a stop word and NOT a technical metadata word,
				// we flag it as an unauthorizedProperNoun (indicating a different show entity).
				if !stopWords[cw] && !metadataWords[cw] {
					hasSubstantiveProperNoun = true
					break
				}
			}
			if hasSubstantiveProperNoun {
				return false // ❌ REJECTED (Substantive Proper-Noun Detected)
			}
		}
	}

	// ── Standard Single-Word Title Guardrail (Preserved & Fine-Tuned) ──
	if len(targetWords) == 1 {
		singleWord := cleanWord(targetWords[0])
		if len(parsedWords) > 1 {
			firstWord := cleanWord(parsedWords[0])
			if firstWord == singleWord {
				return true
			}

			hasExtraNonMeta := false
			for _, w := range parsedWords {
				cw := cleanWord(w)
				if cw != "" && cw != singleWord && !metadataWords[cw] && !stopWords[cw] {
					hasExtraNonMeta = true
					break
				}
			}
			if hasExtraNonMeta {
				return false // ❌ REJECTED
			}
		}
	}
	return true
}

func getHomoglyphRepresentations(r rune) []rune {
	if classes, ok := homoglyphClasses[r]; ok {
		return classes
	}
	return []rune{r}
}

// OverlapCoefficient computes the overlap coefficient between two strings
// using multi-representation homoglyph character bigrams.
func OverlapCoefficient(s1, s2 string) float64 {
	if s1 == s2 {
		return 1.0
	}

	if len(s1) < 2 || len(s2) < 2 {
		return 0.0
	}

	bg1 := make(map[string]struct{}, len(s1)*2)
	runes1 := []rune(s1)
	for i := 0; i < len(runes1)-1; i++ {
		repsA := getHomoglyphRepresentations(runes1[i])
		repsB := getHomoglyphRepresentations(runes1[i+1])
		for _, charA := range repsA {
			for _, charB := range repsB {
				bg1[string(charA)+string(charB)] = struct{}{}
			}
		}
	}

	bg2 := make(map[string]struct{}, len(s2)*2)
	runes2 := []rune(s2)
	intersection := 0
	for i := 0; i < len(runes2)-1; i++ {
		repsA := getHomoglyphRepresentations(runes2[i])
		repsB := getHomoglyphRepresentations(runes2[i+1])
		for _, charA := range repsA {
			for _, charB := range repsB {
				bigram := string(charA) + string(charB)
				if _, ok := bg2[bigram]; !ok {
					bg2[bigram] = struct{}{}
					if _, exists := bg1[bigram]; exists {
						intersection++
					}
				}
			}
		}
	}

	if len(bg1) == 0 || len(bg2) == 0 {
		return 0.0
	}

	minSize := len(bg1)
	if len(bg2) < minSize {
		minSize = len(bg2)
	}

	return float64(intersection) / float64(minSize)
}

func isRomanSequence(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != 'i' && r != 'v' && r != 'x' && r != 'l' && r != 'c' && r != 'd' && r != 'm' {
			return false
		}
	}
	return true
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func romanToArabic(s string) int {
	romanMap := map[rune]int{
		'i': 1, 'v': 5, 'x': 10, 'l': 50, 'c': 100, 'd': 500, 'm': 1000,
	}
	total := 0
	lastVal := 0
	for i := len(s) - 1; i >= 0; i-- {
		val, ok := romanMap[rune(s[i])]
		if !ok {
			return 0
		}
		if val < lastVal {
			total -= val
		} else {
			total += val
			lastVal = val
		}
	}
	return total
}

func normalizeNumbersInTitle(title string) string {
	titleClean := strings.ReplaceAll(title, ":", " ")
	titleClean = strings.ReplaceAll(titleClean, "-", " ")

	words := strings.Fields(strings.ToLower(titleClean))
	for i, w := range words {
		if numDigit, ok := writtenNumbers[w]; ok {
			words[i] = numDigit
			continue
		}

		if isRomanSequence(w) {
			shouldConvert := false
			if len(w) >= 2 {
				shouldConvert = true
			} else if len(w) == 1 {
				if i > 0 && sequelContexts[words[i-1]] {
					shouldConvert = true
				}
				if i == len(words)-1 {
					shouldConvert = true
				}
			}

			if shouldConvert {
				val := romanToArabic(w)
				if val > 0 {
					words[i] = strconv.Itoa(val)
				}
			}
		}
	}
	return strings.Join(words, " ")
}

func extractNonYearNumbers(s string) []string {
	var nums []string
	var current strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				val := current.String()
				if !ignoredNumbers[val] && !(len(val) == 4 && (strings.HasPrefix(val, "19") || strings.HasPrefix(val, "20"))) {
					nums = append(nums, val)
				}
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		val := current.String()
		if !ignoredNumbers[val] && !(len(val) == 4 && (strings.HasPrefix(val, "19") || strings.HasPrefix(val, "20"))) {
			nums = append(nums, val)
		}
	}
	return nums
}

func hasNumericMismatch(target, parsed string) bool {
	targetNums := extractNonYearNumbers(target)
	parsedNums := extractNonYearNumbers(parsed)

	if len(targetNums) == 0 || len(parsedNums) == 0 {
		return false
	}

	for _, tn := range targetNums {
		tnInt, err1 := strconv.Atoi(tn)
		if err1 != nil {
			continue
		}
		for _, pn := range parsedNums {
			pnInt, err2 := strconv.Atoi(pn)
			if err2 == nil && tnInt == pnInt {
				return false
			}
		}
	}
	return true
}

func sequelGuardrail(targetTitle, parsedTitle string, score float64) float64 {
	cleanTarget := strings.Trim(strings.ToLower(targetTitle), " .-_[]()/\\")
	cleanParsed := strings.Trim(strings.ToLower(parsedTitle), " .-_[]()/\\")

	cleanTarget = normalizeNumbersInTitle(cleanTarget)
	cleanParsed = normalizeNumbersInTitle(cleanParsed)

	targetNoArt := stripLeadingArticles(cleanTarget)
	parsedNoArt := stripLeadingArticles(cleanParsed)

	shorter := len(targetNoArt)
	longer := len(parsedNoArt)
	if shorter > longer {
		shorter, longer = longer, shorter
	}

	if longer == 0 || shorter == 0 {
		return score
	}

	ratio := float64(longer) / float64(shorter)
	if ratio <= 1.3 {
		return score
	}

	if !strings.Contains(targetNoArt, parsedNoArt) && !strings.Contains(parsedNoArt, targetNoArt) {
		return score
	}

	var longerStr, shorterStr string
	if len(targetNoArt) > len(parsedNoArt) {
		longerStr, shorterStr = targetNoArt, parsedNoArt
	} else {
		longerStr, shorterStr = parsedNoArt, targetNoArt
	}

	var extra string
	if strings.HasPrefix(longerStr, shorterStr) {
		extra = strings.TrimSpace(longerStr[len(shorterStr):])
	} else if strings.HasSuffix(longerStr, shorterStr) {
		extra = strings.TrimSpace(longerStr[:len(longerStr)-len(shorterStr)])
	} else {
		return score
	}

	extraWords := strings.Fields(extra)
	for _, w := range extraWords {
		cw := cleanWord(w)
		if isRomanSequence(cw) || isNumber(cw) || sequelIndicators[cw] {
			return score * (float64(shorter) / float64(longer))
		}
	}

	return score
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

	cleanTmdb = normalizeNumbersInTitle(cleanTmdb)
	cleanParsed = normalizeNumbersInTitle(cleanParsed)

	if hasNumericMismatch(cleanTmdb, cleanParsed) {
		return 0.0
	}

	oc := OverlapCoefficient(cleanTmdb, cleanParsed)

	cleanTmdbNoArt := stripLeadingArticles(cleanTmdb)
	cleanParsedNoArt := stripLeadingArticles(cleanParsed)
	if cleanTmdbNoArt != cleanTmdb || cleanParsedNoArt != cleanParsed {
		ocClean := OverlapCoefficient(cleanTmdbNoArt, cleanParsedNoArt)
		if ocClean > oc {
			oc = ocClean
		}
	}

	oc = sequelGuardrail(tmdbTitle, parsed.Title, oc)

	if oc >= config.SimilarityThreshold {
		return oc
	}

	return oc
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

func FindBestSeriesStreams(ctx context.Context, tmdbShow *bitmagnet.TorrentItem, altTitles []string, season, episode int, newTorrents []bitmagnet.TorrentItem, cachedRows []map[string]interface{}, preferredLanguages []string) (streams []Stream, cachedStreams []Stream) {
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

	type jobResult struct {
		streams []Stream
	}
	results := make(chan jobResult, len(newTorrents))

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())

	for _, torrent := range newTorrents {
		if cachedHashes[torrent.InfoHash] {
			continue
		}
		torrentData := torrent.Torrent
		if torrentData.Name == "" {
			continue
		}
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
			for _, alt := range altTitles {
				if s := getTitleSimilarity(alt, td.Name); s > sim {
					sim = s
				}
			}
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

			// ── UPGRADE: PN-SILEC Multi-Word Franchise Leakage Guardrail (Series) ──
			matchedGuardrail := false
			if passTitleGuardrail(tmdbShow.Title, parsed.Title) {
				matchedGuardrail = true
			} else {
				for _, alt := range altTitles {
					if passTitleGuardrail(alt, parsed.Title) {
						matchedGuardrail = true
						break
					}
				}
			}

			if !matchedGuardrail {
				utils.Logger.Debug("filtering out series torrent: failed title guardrail", "target", tmdbShow.Title, "parsed", parsed.Title)
				return nil
			}

			var local []Stream

			if parsed.Season == season {
				if td.FilesStatus == "single" {
					if parsed.Episode != 0 && parsed.Episode != episode {
						return nil
					}

					local = append(local, Stream{
						InfoHash:    t.InfoHash,
						FileIndex:   0,
						TorrentName: td.Name,
						Seeders:     t.Seeders,
						Language:    bestLang,
						Quality:     quality,
						Size:        td.Size,
						IsCached:    false,
					})
				} else if td.FilesStatus == "multi" {
					files, ok := filesMap[t.InfoHash]
					if !ok || len(files) == 0 {
						return nil
					}
					var candidates []parser.CandidateFile
					for _, f := range files {
						if f.FileType == "video" {
							candidates = append(candidates, parser.CandidateFile{
								ID:   f.Index,
								Path: f.Path,
								Size: f.Size,
							})
						}
					}
					if bestFile, found := parser.FindBestSeriesFile(candidates, season, episode, parsed.Season); found {
						local = append(local, Stream{
							InfoHash:    t.InfoHash,
							FileIndex:   bestFile.ID,
							TorrentName: td.Name,
							Seeders:     t.Seeders,
							Language:    bestLang,
							Quality:     quality,
							Size:        td.Size,
							IsCached:    false,
						})
					}
				}
			}

			if len(local) > 0 {
				results <- jobResult{streams: local}
			}
			return nil
		})
	}

	go func() {
		_ = g.Wait()
		close(results)
	}()

	for r := range results {
		streams = append(streams, r.streams...)
	}
	return streams, cachedStreams
}

func FindBestMovieStreams(ctx context.Context, tmdbMovie *bitmagnet.TorrentItem, altTitles []string, tmdbYear string, newTorrents []bitmagnet.TorrentItem, cachedRows []map[string]interface{}, preferredLanguages []string) (streams []Stream, cachedStreams []Stream) {
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

	type jobResult struct {
		streams []Stream
	}
	results := make(chan jobResult, len(newTorrents))

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())

	for _, torrent := range newTorrents {
		if cachedHashes[torrent.InfoHash] {
			continue
		}
		torrentData := torrent.Torrent
		if torrentData.Name == "" {
			continue
		}
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
			for _, alt := range altTitles {
				if s := getTitleSimilarity(alt, td.Name); s > sim {
					sim = s
				}
			}
			utils.Logger.Debug("evaluating movie torrent", "name", td.Name, "similarity", fmt.Sprintf("%.2f", sim))
			if sim < config.SimilarityThreshold {
				return nil
			}

			parsed := parser.RobustParseInfo(td.Name, 0)

			// ── UPGRADE: PN-SILEC Multi-Word Franchise Leakage Guardrail (Movie) ──
			matchedGuardrail := false
			if passTitleGuardrail(tmdbMovie.Title, parsed.Title) {
				matchedGuardrail = true
			} else {
				for _, alt := range altTitles {
					if passTitleGuardrail(alt, parsed.Title) {
						matchedGuardrail = true
						break
					}
				}
			}

			if !matchedGuardrail {
				utils.Logger.Debug("filtering out movie torrent: failed title guardrail", "target", tmdbMovie.Title, "parsed", parsed.Title)
				return nil
			}

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
							strings.HasSuffix(lowerPath, ".wmv") ||
							strings.HasSuffix(lowerPath, ".flv") ||
							strings.HasSuffix(lowerPath, ".webm") {
							hasVideo = true
							break
						}
					}
					if !hasVideo {
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

			results <- jobResult{streams: []Stream{{
				InfoHash:    t.InfoHash,
				TorrentName: td.Name,
				Seeders:     t.Seeders,
				Language:    bestLang,
				Quality:     quality,
				Size:        td.Size,
				IsCached:    false,
			}}}
			return nil
		})
	}

	go func() {
		_ = g.Wait()
		close(results)
	}()

	for r := range results {
		streams = append(streams, r.streams...)
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

	sort.Slice(all, func(i, j int) bool {
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
