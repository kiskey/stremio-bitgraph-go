package matcher

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/user/stremio-bitgraph-go/internal/bitmagnet"
	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/db"
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
	Badges      string // Pre-computed badges to eliminate real-time regex latency
}

// Static Low-Entropy Grammatical Stop Words Set for PN-SILEC Filtering
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"of": true, "in": true, "on": true, "at": true, "to": true,
	"for": true, "with": true, "by": true, "from": true, "aka": true,
	"la": true, "le": true, "les": true, "el": true, "un": true, "une": true,
}

// Technical tags that should not trigger the single-word guardrail.
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
	"english": true, "spanish": true, "french": true, "italic": true,
	"russian": true, "korean": true, "japanese": true, "chinese": true,
	"51": true, "71": true, "20": true, "10bit": true, "remux": true,
	"3d": true, "sdr": true, "gb": true, "mb": true, "kb": true,
	"web": true, "dl": true, "hd": true,
	"complete": true, "repack": true, "proper": true, "vostfr": true,
	"subs":     true, "sub": true, "esub": true, "vof": true, "vff": true,
	"vf":       true, "season": true, "series": true, "episode": true, "pack": true,
	// Live Action & Regional Series Metadata Indicators (Added to prevent guardrail false-positives)
	"live": true, "action": true, "serie": true,
	// Aligned common extensions and formats
	"mkv": true, "mp4": true, "avi": true, "mov": true, "wmv": true, "flv": true, "webm": true,
	"rar": true, "zip": true, "par2": true, "nfo": true, "srt": true,
	// Country/region identifiers & miscellaneous common tags to prevent false negatives
	"us": true, "uk": true, "ca": true, "nz": true, "au": true,
	"fr": true, "de": true, "jp": true, "kr": true, "cn": true,
	"hk": true, "tw": true, "it": true, "es": true, "nl": true,
	"pl": true, "ru": true, "se": true, "no": true, "fi": true,
	"dk": true, "new": true, "full": true, "all": true,
	// Regional language/subtitle abbreviations & subdomain noise markers
	"tam": true, "tel": true, "hin": true, "eng": true, "mal": true, "kan": true,
	"msub": true, "tamilmv": true, "tamilblasters": true, "bolly4u": true, "torrent911": true,
	// Extended regional country, dub, sub, and video format codes
	"cz": true, "sk": true, "hu": true, "ro": true, "bg": true, "ua": true, "tr": true,
	"th": true, "vi": true, "he": true, "fa": true, "soft": true, "hard": true,
	"ntsc": true, "pal": true, "open": true, "matte": true, "unrated": true, "rated": true,
	"subbed": true, "rosubbed": true, "nlsubs": true, "engsub": true,

	// Plural and multilingual metadata additions (Parity Sanitization Matrix)
	"episodes": true, "seasons": true, "eps": true, "vost": true,
	"subbed": true, "dubbed": true, "dual-audio": true, "multi-sub": true,
	"spanish": true, "french": true, "german": true, "italian": true,
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

// Refined seasonRangeRegex to optionally support redundant second season prefixes (e.g. S01-S21, Season 1 to Season 2)
var seasonRangeRegex = regexp.MustCompile(`(?i)\b(?:s|season|seasons)\s*0*(\d+)\s*(?:-|to|~)\s*(?:s|season|seasons)?\s*0*(\d+)\b`)

// Self-Learning Entropy Engine Global State Variables
var (
	entropyOnce      sync.Once
	tokenFrequencies = make(map[string]int)
	tokenFreqMu      sync.RWMutex
)

var abbreviationMap = map[string][]string{
	"dr":  {"doctor"},
	"st":  {"saint"},
	"mr":  {"mister"},
	"mrs": {"missus", "missis"},
	"vs":  {"versus"},
	"wk":  {"week"},
	"ft":  {"feat", "featuring"},
}

// standardizePunctuation normalizes non-standard middle dots, curly quotes, and dashes to standard ASCII representations
func standardizePunctuation(s string) string {
	r := strings.NewReplacer(
		"·", " ",
		"•", " ",
		"’", "'",
		"‘", "'",
		"´", "'",
		"`", "'",
		"“", "\"",
		"”", "\"",
		"—", "-",
		"–", "-",
	)
	return r.Replace(s)
}

func ExpandAbbreviations(title string) string {
	words := strings.Fields(strings.ToLower(title))
	for i, w := range words {
		cleaned := strings.TrimRight(w, ".,;:!?")
		if expansions, ok := abbreviationMap[cleaned]; ok && len(expansions) > 0 {
			words[i] = expansions[0]
		}
	}
	return strings.Join(words, " ")
}

func tokenPositionOverlap(s1, s2 string) float64 {
	t1 := strings.Fields(strings.ToLower(s1))
	t2 := strings.Fields(strings.ToLower(s2))
	if len(t1) == 0 || len(t2) == 0 {
		return 0
	}
	minLen := len(t1)
	if len(t2) < minLen {
		minLen = len(t2)
	}
	matches := 0
	for i := 0; i < minLen; i++ {
		if cleanWord(t1[i]) == cleanWord(t2[i]) {
			matches++
		}
	}
	return float64(matches) / float64(minLen)
}

// InitializeEntropyEngine pre-seeds and scans the database to build self-learning token counts
func InitializeEntropyEngine(ctx context.Context) {
	// 1. Pre-seed with all metadataWords and stopWords to guarantee 100% cold-start safety
	tokenFreqMu.Lock()
	for k := range metadataWords {
		tokenFrequencies[k] = 1000
	}
	for k := range stopWords {
		tokenFrequencies[k] = 1000
	}
	tokenFreqMu.Unlock()

	// 2. Query SQLite torrents table to digest cached filenames with zero circular dependency imports
	rows, err := db.Pool.QueryContext(ctx, "SELECT torrent_info_json FROM torrents WHERE torrent_info_json IS NOT NULL")
	if err != nil {
		utils.Logger.Warn("Entropy Engine: Failed to query torrents table for learning", "error", err)
		return
	}
	defer rows.Close()

	tokenFreqMu.Lock()
	defer tokenFreqMu.Unlock()

	type minimalTorrentInfo struct {
		Filename string `json:"filename"`
	}

	count := 0
	for rows.Next() {
		var jsonBytes []byte
		if err := rows.Scan(&jsonBytes); err != nil {
			continue
		}
		var info minimalTorrentInfo
		if err := json.Unmarshal(jsonBytes, &info); err == nil && info.Filename != "" {
			cleanName := parser.SanitizeName(info.Filename)
			for _, w := range strings.Fields(strings.ToLower(cleanName)) {
				tokenFrequencies[cleanWord(w)]++
			}
			count++
		}
	}
	utils.Logger.Info("Entropy Engine: Successfully digested historical cache", "records", count, "unique_tokens", len(tokenFrequencies))
}

// UpdateEntropyToken registers a newly processed filename dynamically to keep the engine updated
func UpdateEntropyToken(name string) {
	tokenFreqMu.Lock()
	defer tokenFreqMu.Unlock()
	cleanName := parser.SanitizeName(name)
	for _, w := range strings.Fields(strings.ToLower(cleanName)) {
		tokenFrequencies[cleanWord(w)]++
	}
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

// cleanWord converts a string to lowercase and removes non-alphanumeric characters.
// Features an allocation-free fast-path for already cleaned string inputs.
func cleanWord(w string) string {
	hasUpperOrNonAlpha := false
	for i := 0; i < len(w); i++ {
		c := w[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			hasUpperOrNonAlpha = true
			break
		}
	}
	if !hasUpperOrNonAlpha {
		return w
	}

	var buf []byte
	for i := 0; i < len(w); i++ {
		c := w[i]
		if c >= 'A' && c <= 'Z' {
			buf = append(buf, c+32)
		} else if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			buf = append(buf, c)
		}
	}
	return string(buf)
}

// isYearNumber checks if a string is a standard 4-digit release year
func isYearNumber(s string) bool {
	return len(s) == 4 && (strings.HasPrefix(s, "19") || strings.HasPrefix(s, "20"))
}

// isNumber checks if a string consists entirely of digits
func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isTechnicalToken performs an allocation-free dynamic check to identify season, episode,
// and pack-specific serialization tokens, allowing them to safely bypass the guardrail.
func isTechnicalToken(s string) bool {
	if metadataWords[s] || stopWords[s] {
		return true
	}

	if isNumber(s) {
		return true
	}

	if len(s) >= 2 {
		first := s[0]
		if (first == 's' || first == 'e' || first == 'p') && isNumber(s[1:]) {
			return true
		}
		if len(s) >= 3 {
			prefix2 := s[:2]
			if (prefix2 == "se" || prefix2 == "ep") && isNumber(s[2:]) {
				return true
			}
		}
		if len(s) >= 4 {
			if s[:3] == "epi" && isNumber(s[3:]) {
				return true
			}
		}
		if len(s) >= 5 {
			prefix4 := s[:4]
			if (prefix4 == "seas" || prefix4 == "part") && isNumber(s[4:]) {
				return true
			}
		}
		if len(s) >= 7 {
			if s[:6] == "season" && isNumber(s[6:]) {
				return true
			}
		}
		if len(s) >= 8 {
			if s[:7] == "episode" && isNumber(s[7:]) {
				return true
			}
		}
	}
	return false
}

// passTitleGuardrail prevents single-word titles (e.g. "Up", "It") from matching
// unrelated multi-word torrents (e.g. "Upgraded", "Italian"). It allows metadata
// words (codecs, quality tags, languages) to pass through.
func passTitleGuardrail(targetTitle, parsedTitle string, altTitles []string) bool {
	stdTarget := standardizePunctuation(targetTitle)
	stdParsed := standardizePunctuation(parsedTitle)

	cleanTarget := strings.Trim(strings.ToLower(stdTarget), " .-_[]()/\\")
	cleanParsed := strings.Trim(strings.ToLower(stdParsed), " .-_[]()/\\")

	// Temporarily replace hyphens with standard spaces to handle dash-joined titles
	// like "From-Scratch" matching "From Scratch" cleanly.
	cleanTarget = strings.ReplaceAll(cleanTarget, "-", " ")
	cleanParsed = strings.ReplaceAll(cleanParsed, "-", " ")

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

	// ── UPGRADE: Substantive Word Guardrail ──
	// Ensure the parsed title doesn't contain unrelated substantive words.
	// This prevents partial title matches, release group leaks, and unrelated shows.
	targetWordSet := make(map[string]bool)
	for _, w := range targetWords {
		targetWordSet[cleanWord(w)] = true
	}
	for _, alt := range altTitles {
		stdAlt := standardizePunctuation(alt)
		cleanAlt := strings.Trim(strings.ToLower(stdAlt), " .-_[]()/\\")
		cleanAlt = strings.ReplaceAll(cleanAlt, "-", " ")
		altNoArt := stripLeadingArticles(cleanAlt)
		for _, w := range strings.Fields(altNoArt) {
			targetWordSet[cleanWord(w)] = true
		}
	}

	hasUnrelatedSubstantiveWord := false
	for _, w := range parsedWords {
		cw := cleanWord(w)
		if cw == "" {
			continue
		}
		if targetWordSet[cw] {
			continue
		}

		// Self-Learning Entropy Guardrail:
		// Check the dynamic frequency of the word in our database.
		// If the word appears across 3 or more records, it is classified as low-entropy noise (metadata).
		// If it has a frequency of 1 or 2, it is treated as a highly unique substantive word (another show/movie title).
		tokenFreqMu.RLock()
		freq := tokenFrequencies[cw]
		tokenFreqMu.RUnlock()

		if freq >= 3 {
			continue // Low-entropy noise word (Safely Skipped)
		}

		if isTechnicalToken(cw) {
			continue
		}
		hasUnrelatedSubstantiveWord = true
		break
	}

	if hasUnrelatedSubstantiveWord {
		return false // ❌ REJECTED (Unrelated Substantive Word Detected)
	}

	// ── UPGRADE: PN-SILEC Multi-Word Franchise Leakage Guardrail ──
	if len(targetWords) > 1 && len(parsedWords) > len(targetWords) {
		startsSame := true
		for i := 0; i < len(targetWords); i++ {
			if cleanWord(parsedWords[i]) != cleanWord(targetWords[i]) {
				startsSame = false
				break
			}
		}

		if startsSame {
			extraWords := parsedWords[len(targetWords):]
			hasSubstantiveProperNoun := false
			for _, w := range extraWords {
				cw := cleanWord(w)
				if cw == "" {
					continue
				}
				if !isTechnicalToken(cw) {
					hasSubstantiveProperNoun = true
					break
				}
			}
			if hasSubstantiveProperNoun {
				return false
			}
		}
	}

	// ── Standard Single-Word Title Guardrail (Corrected & Safe) ──
	if len(targetWords) == 1 {
		singleWord := cleanWord(targetWords[0])
		if len(parsedWords) > 1 {
			hasExtraNonMeta := false
			for _, w := range parsedWords {
				cw := cleanWord(w)
				if cw != "" && cw != singleWord {
					tokenFreqMu.RLock()
					freq := tokenFrequencies[cw]
					tokenFreqMu.RUnlock()

					if freq >= 3 {
						continue // Low-entropy noise word (Safely Skipped)
					}

					if !isTechnicalToken(cw) {
						hasExtraNonMeta = true
						break
					}
				}
			}
			if hasExtraNonMeta {
				return false // ❌ REJECTED
			}
			return true
		}
	}
	return true
}

var uint64MapPool = sync.Pool{
	New: func() interface{} {
		return make(map[uint64]struct{}, 64)
	},
}

func clearMap(m map[uint64]struct{}) {
	for k := range m {
		delete(m, k)
	}
}

// OverlapCoefficient computes the overlap coefficient between two strings
// using multi-representation homoglyph character bigrams.
// Fully optimized for zero heap allocations, bitwise rune-packing, and zero GC pressure.
func OverlapCoefficient(s1, s2 string) float64 {
	if s1 == s2 {
		return 1.0
	}

	if len(s1) < 2 || len(s2) < 2 {
		return 0.0
	}

	bg1 := uint64MapPool.Get().(map[uint64]struct{})
	bg2 := uint64MapPool.Get().(map[uint64]struct{})
	defer func() {
		clearMap(bg1)
		uint64MapPool.Put(bg1)
		clearMap(bg2)
		uint64MapPool.Put(bg2)
	}()

	var lastRune rune
	hasLast := false
	for _, r := range s1 {
		if !hasLast {
			lastRune = r
			hasLast = true
			continue
		}
		repsA, okA := homoglyphClasses[lastRune]
		repsB, okB := homoglyphClasses[r]
		if !okA && !okB {
			packed := (uint64(lastRune) << 32) | uint64(r)
			bg1[packed] = struct{}{}
		} else if !okA && okB {
			for _, charB := range repsB {
				packed := (uint64(lastRune) << 32) | uint64(charB)
				bg1[packed] = struct{}{}
			}
		} else if okA && !okB {
			for _, charA := range repsA {
				packed := (uint64(charA) << 32) | uint64(r)
				bg1[packed] = struct{}{}
			}
		} else {
			for _, charA := range repsA {
				for _, charB := range repsB {
					packed := (uint64(charA) << 32) | uint64(charB)
					bg1[packed] = struct{}{}
				}
			}
		}
		lastRune = r
	}

	intersection := 0
	hasLast = false
	for _, r := range s2 {
		if !hasLast {
			lastRune = r
			hasLast = true
			continue
		}
		repsA, okA := homoglyphClasses[lastRune]
		repsB, okB := homoglyphClasses[r]
		if !okA && !okB {
			packed := (uint64(lastRune) << 32) | uint64(r)
			if _, ok := bg2[packed]; !ok {
				bg2[packed] = struct{}{}
				if _, exists := bg1[packed]; exists {
					intersection++
				}
			}
		} else if !okA && okB {
			for _, charB := range repsB {
				packed := (uint64(lastRune) << 32) | uint64(charB)
				if _, ok := bg2[packed]; !ok {
					bg2[packed] = struct{}{}
					if _, exists := bg1[packed]; exists {
						intersection++
					}
				}
			}
		} else if okA && !okB {
			for _, charA := range repsA {
				packed := (uint64(charA) << 32) | uint64(r)
				if _, ok := bg2[packed]; !ok {
					bg2[packed] = struct{}{}
					if _, exists := bg1[packed]; exists {
						intersection++
					}
				}
			}
		} else {
			for _, charA := range repsA {
				for _, charB := range repsB {
					packed := (uint64(charA) << 32) | uint64(charB)
					if _, ok := bg2[packed]; !ok {
						bg2[packed] = struct{}{}
						if _, exists := bg1[packed]; exists {
							intersection++
						}
					}
				}
			}
		}
		lastRune = r
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
				if !ignoredNumbers[val] && !isYearNumber(val) {
					nums = append(nums, val)
				}
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		val := current.String()
		if !ignoredNumbers[val] && !isYearNumber(val) {
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

	return sequelGuardrailParsed(cleanTarget, cleanParsed, score)
}

func sequelGuardrailParsed(cleanTarget, cleanParsed string, score float64) float64 {
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
		// Upgrade: Prevent legitimate release years from destroying movie similarity scores
		if isRomanSequence(cw) || (isNumber(cw) && !isYearNumber(cw)) || sequelIndicators[cw] {
			return score * (float64(shorter) / float64(longer))
		}
	}

	return score
}

// ── COMPREHENSIVE SONARR/RADARR PARITY SANITIZATION PIPELINE ──────────────────

var (
	// Matches inner brackets/parentheses safely (e.g. [AnimeRG] or (Episodes 001-837) or 【pseudo】)
	bracketExtractRe = regexp.MustCompile(`[([【（][^()\[\]【】（）]+[)\]】）]`)

	// Slice Boundaries: Slices unbracketed titles at the earliest occurrence of any of these patterns
	sliceBoundaryRe = regexp.MustCompile(`(?i)\b(?:seasons?|s)\s*\d+|\b(?:episodes?|eps?|e|ep)\s*\d+|\bS\d+E\d+|\b\d+x\d+|\b\d+\s*(?:-|to|~)\s*\d+\b|\b(?:2160p|1080p|720p|480p|360p|4k|uhd|bluray|web[-_.]?dl|webrip|hdtv|bdrip|brrip|dvdrip|hdr|sdr|h264|h265|x264|x265|hevc)\b`)
)

// isBracketMetadata evaluates if a bracket's inner content should be treated as metadata.
// It recursively reuses existing codebase validators (CompiledFilters, theatrical leak regexes, Anime Shield markers).
func isBracketMetadata(inner string) bool {
	innerClean := strings.TrimSpace(inner)
	if innerClean == "" {
		return true
	}

	// 1. Check against the compiled badge filters from the badge engine (parser.CompiledFilters)
	for i := range parser.CompiledFilters {
		filter := &parser.CompiledFilters[i]
		if filter.Positive.MatchString(innerClean) {
			return true
		}
	}

	// 2. Check against low-quality theatrical leak matchers (re-used from parser.go)
	if parser.DetectLowQuality(innerClean) != "" {
		return true
	}

	// 3. Check against Anime Shield regex sets to identify animation-specific indicators
	if animeLangRe.MatchString(innerClean) ||
		animeCrcHashRe.MatchString(innerClean) ||
		animeSourceRe.MatchString(innerClean) ||
		westernSourceRe.MatchString(innerClean) ||
		liveActionMarkerRe.MatchString(innerClean) {
		return true
	}

	// 4. Token-based classification pass: if majority of words are technical metadata words/numbers
	words := strings.Fields(strings.ToLower(standardizePunctuation(innerClean)))
	if len(words) == 0 {
		return true
	}

	metadataCount := 0
	for _, w := range words {
		cw := cleanWord(w)
		if cw == "" {
			metadataCount++
			continue
		}
		if isTechnicalToken(cw) || isRomanSequence(cw) || ignoredNumbers[cw] {
			metadataCount++
		}
	}

	// If at least 50% of the words are metadata tokens, classify the whole bracket as metadata
	return float64(metadataCount)/float64(len(words)) >= 0.5
}

// NormalizeTitleSonarrParity implements Sonarr/Radarr-parity title sanitization recursively
func NormalizeTitleSonarrParity(s string) string {
	s = standardizePunctuation(s)
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.ReplaceAll(s, "_", " ")

	// Pass 1: Recursive Bracket / Parenthetical stripping (Runs up to 3 times to safely resolve nested brackets)
	for i := 0; i < 3; i++ {
		prevLen := len(s)
		s = bracketExtractRe.ReplaceAllStringFunc(s, func(match string) string {
			if len(match) < 2 {
				return match
			}
			inner := match[1 : len(match)-1]
			if isBracketMetadata(inner) {
				return " "
			}
			return match
		})
		if len(s) == prevLen {
			break
		}
	}

	// Pass 2: Unbracketed Boundary Slicing
	if loc := sliceBoundaryRe.FindStringIndex(s); loc != nil {
		if loc[0] > 0 { // Ensure we don't slice if the title itself begins with a keyword
			s = s[:loc[0]]
		}
	}

	// Collapse whitespace and return lowercase representation
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func getTitleSimilarityParsed(tmdbTitle, parsedTitle string) float64 {
	if tmdbTitle == "" || parsedTitle == "" {
		return 0
	}

	// Apply Sonarr/Radarr-parity normalization recursively to both incoming titles
	cleanTmdb := NormalizeTitleSonarrParity(tmdbTitle)
	cleanParsed := NormalizeTitleSonarrParity(parsedTitle)

	if cleanTmdb == "" || cleanParsed == "" {
		return 0.0
	}

	if hasNumericMismatch(cleanTmdb, cleanParsed) {
		return 0.0
	}

	oc := OverlapCoefficient(cleanTmdb, cleanParsed)
	posOc := tokenPositionOverlap(cleanTmdb, cleanParsed)

	oc = (oc * 0.7) + (posOc * 0.3)

	cleanTmdbNoArt := stripLeadingArticles(cleanTmdb)
	cleanParsedNoArt := stripLeadingArticles(cleanParsed)
	if cleanTmdbNoArt != cleanTmdb || cleanParsedNoArt != cleanParsed {
		ocClean := OverlapCoefficient(cleanTmdbNoArt, cleanParsedNoArt)
		posOcClean := tokenPositionOverlap(cleanTmdbNoArt, cleanParsedNoArt)
		ocClean = (ocClean * 0.7) + (posOcClean * 0.3)
		if ocClean > oc {
			oc = ocClean
		}
	}

	oc = sequelGuardrailParsed(cleanTmdb, cleanParsed, oc)

	return oc
}

func getTitleSimilarity(tmdbTitle, torrentName string) float64 {
	if tmdbTitle == "" {
		return 0
	}
	parsed := parser.RobustParseInfo(torrentName, 0)
	return getTitleSimilarityParsed(tmdbTitle, parsed.Title)
}

// stripDiacritics maps standard Latin-1 and advanced unicode diacritics to ASCII base characters
func stripDiacritics(s string) string {
	var replacer = strings.NewReplacer(
		"ā", "a", "á", "a", "à", "a", "ä", "a", "â", "a", "ã", "a", "å", "a",
		"ē", "e", "é", "e", "è", "e", "ë", "e", "ê", "e",
		"ī", "i", "í", "i", "ì", "i", "ï", "i", "î", "i",
		"ō", "o", "ó", "o", "ò", "o", "ö", "o", "ô", "o", "õ", "o", "ø", "o",
		"ū", "u", "ú", "u", "ù", "u", "ü", "u", "û", "u",
		"ý", "y", "ÿ", "y",
		"ñ", "n", "ç", "c",
		"Ā", "A", "Á", "A", "À", "A", "Ä", "A", "Â", "A", "Ã", "A", "Å", "A",
		"Ē", "E", "É", "E", "È", "E", "Ë", "E", "Ê", "E",
		"Ī", "I", "Í", "I", "Ï", "I", "Î", "I",
		"Ō", "O", "Ó", "O", "Ò", "O", "Ö", "O", "Ô", "O", "Õ", "O", "Ø", "O",
		"Ū", "U", "Ú", "U", "Ù", "U", "Ü", "U", "Û", "U",
		"Ý", "Y", "Ñ", "N", "Ç", "C",
	)
	return replacer.Replace(s)
}

// injectNormalizedAltTitle adds the un-accented ASCII representation to AltTitles if it differs from the primary name
func injectNormalizedAltTitle(name string, alts []string) []string {
	normalized := stripDiacritics(name)
	if normalized != name {
		isUnique := true
		for _, existing := range alts {
			if existing == normalized {
				isUnique = false
				break
			}
		}
		if isUnique {
			alts = append(alts, normalized)
		}
	}
	return alts
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
		if !t.Torrent.HasFilesInfo && t.Torrent.FilesStatus != "multi" {
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

func FindBestSeriesStreams(ctx context.Context, tmdbShow *bitmagnet.TorrentItem, altTitles []string, season, episode int, newTorrents []bitmagnet.TorrentItem, cachedRows []map[string]interface{}, preferredLanguages []string) (streams []Stream, cachedStreams []Stream) {
	return FindBestSeriesStreamsLongRunning(ctx, tmdbShow, altTitles, season, episode, newTorrents, cachedRows, preferredLanguages, false, "", AnimePriorMeta{})
}

func FindBestSeriesStreamsLongRunning(ctx context.Context, tmdbShow *bitmagnet.TorrentItem, altTitles []string, season, episode int, newTorrents []bitmagnet.TorrentItem, cachedRows []map[string]interface{}, preferredLanguages []string, isLongRunning bool, airDate string, prior AnimePriorMeta) (streams []Stream, cachedStreams []Stream) {
	// Dynamically load the self-learning Entropy Engine exactly once on the first query execution.
	// This eliminates startup circular dependency import cycles and cold-start latencies.
	entropyOnce.Do(func() {
		utils.Logger.Info("Entropy Engine: Initiating self-learning parser scan...")
		InitializeEntropyEngine(context.Background())
	})

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
						Badges:      parser.FormatBadges(fn),
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
		if torrent.Torrent.HasFilesInfo || torrent.Torrent.FilesStatus == "multi" {
			multiFileTorrents = append(multiFileTorrents, torrent)
		}
	}

	filesMap := fetchTorrentFilesConcurrent(ctx, multiFileTorrents)

	utils.Logger.Debug("started series stream parsing", "new_torrents_count", len(newTorrents), "multi_file_count", len(multiFileTorrents), "files_map_resolved", len(filesMap))

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

			// Apply Bayesian LLR Anime Gated Shield
			if !EvaluateAnimeShield(td.Name, prior) {
				utils.Logger.Debug("filtering out series torrent: failed anime shield", "name", td.Name)
				return nil
			}

			// Apply/Execute the Range Boundary Guardrail
			// If the torrent name declares an explicit episode range,
			// and our requested episode is strictly outside this range, reject the torrent immediately.
			if parser.HasExcludingRange(td.Name, episode) {
				utils.Logger.Debug("filtering out series torrent: requested episode is outside declared range", "name", td.Name, "episode", episode)
				return nil
			}

			// Apply Temporal Disqualification Shield
			if t.PublishedAt != "" && tmdbShow.PublishedAt != "" {
				premiereY, err := strconv.Atoi(tmdbShow.PublishedAt)
				if err == nil {
					pubTs := parsePublishedAt(t.PublishedAt)
					if isNewerShowDisqualified(pubTs, premiereY) {
						utils.Logger.Debug("filtering out series torrent: failed temporal disqualification", "name", td.Name, "published", t.PublishedAt, "premiere", tmdbShow.PublishedAt)
						return nil
					}
				}
			}

			parsed := parser.RobustParseInfo(td.Name, 0)

			// Find the title (primary or alternate) that actually matched
			matchingTitle := ""
			bestSim := getTitleSimilarityParsed(tmdbShow.Title, parsed.Title)
			if bestSim >= config.SimilarityThreshold {
				matchingTitle = tmdbShow.Title
			}
			for _, alt := range altTitles {
				if s := getTitleSimilarityParsed(alt, parsed.Title); s > bestSim {
					bestSim = s
					if s >= config.SimilarityThreshold {
						matchingTitle = alt
					}
				}
			}

			if bestSim < config.SimilarityThreshold || matchingTitle == "" {
				utils.Logger.Debug("filtering out series torrent: failed title similarity",
					"name", td.Name,
					"best_similarity", fmt.Sprintf("%.4f", bestSim),
					"threshold", config.SimilarityThreshold,
					"tmdb_title", tmdbShow.Title,
					"alt_titles", altTitles)
				return nil
			}

			// SPEBC: Block older remakes by checking if the torrent year is older than the premiere year
			if !isLongRunning && parsed.Year != 0 && tmdbShow.PublishedAt != "" {
				premiereY, err := strconv.Atoi(tmdbShow.PublishedAt)
				if err == nil {
					if parsed.Year < premiereY-1 {
						utils.Logger.Debug("filtering out series torrent: failed earliest premiere year check", "torrent_year", parsed.Year, "premiere_year", premiereY)
						return nil
					}
				}
			}

			bestLang := getBestLanguage(t.Languages, preferredLanguages)
			if bestLang == "en" && parsed.Language != "en" && parsed.Language != "" {
				bestLang = parsed.Language
			}
			quality := utils.GetQuality(t.VideoResolution)
			if parsed.Quality == "cam" || parsed.Quality == "ts" || parsed.Quality == "tc" || parsed.Quality == "scr" || parsed.Quality == "wp" || parsed.Quality == "regional" {
				quality = parsed.Quality
			} else if (quality == "sd" || quality == "") && parsed.Quality != "sd" && parsed.Quality != "" {
				quality = parsed.Quality
			}

			// ── UPGRADE: PN-SILEC Multi-Word Franchise Leakage Guardrail (Series) ──
			if !passTitleGuardrail(matchingTitle, parsed.Title, altTitles) {
				utils.Logger.Debug("filtering out series torrent: failed title guardrail", "target", matchingTitle, "parsed", parsed.Title)
				return nil
			}

			// CSRC Range Check: Overrides strict season matching if a multi-season range is detected
			isMultiSeasonPack := false
			if matches := seasonRangeRegex.FindStringSubmatch(td.Name); len(matches) >= 3 {
				startS, _ := strconv.Atoi(matches[1])
				endS, _ := strconv.Atoi(matches[2])
				if season >= startS && season <= endS {
					isMultiSeasonPack = true
				}
			}

			var local []Stream

			matchedSeasonEpisodeOrDate := false
			if parsed.Season == season || isMultiSeasonPack {
				matchedSeasonEpisodeOrDate = true
			} else if isLongRunning && airDate != "" {
				parts := strings.Split(airDate, "-")
				if len(parts) == 3 {
					y, m, d := parts[0], parts[1], parts[2]
					permutations := []string{
						fmt.Sprintf("%s.%s.%s", y, m, d),
						fmt.Sprintf("%s-%s-%s", y, m, d),
						fmt.Sprintf("%s %s %s", y, m, d),
						fmt.Sprintf("%s.%s.%s", m, d, y),
						fmt.Sprintf("%s-%s-%s", m, d, y),
						fmt.Sprintf("%s %s %s", m, d, y),
						fmt.Sprintf("%s.%s.%s", d, m, y),
						fmt.Sprintf("%s-%s-%s", d, m, y),
						fmt.Sprintf("%s %s %s", d, m, y),
					}
					lowerName := strings.ToLower(td.Name)
					for _, perm := range permutations {
						if strings.Contains(lowerName, perm) {
							matchedSeasonEpisodeOrDate = true
							break
						}
					}
				}
			}

			if matchedSeasonEpisodeOrDate {
				isSeasonPack := (parsed.Season == season && (parsed.IsPack || parsed.Episode == 0)) || isMultiSeasonPack
				files, ok := filesMap[t.InfoHash]

				if ok && len(files) > 0 {
					var candidates []parser.CandidateFile
					for _, f := range files {
						if f.FileType == "video" || isVideoFile(f.Path) {
							candidates = append(candidates, parser.CandidateFile{
								ID:   f.Index,
								Path: f.Path,
								Size: f.Size,
							})
						}
					}
					if bestFile, found := parser.FindBestSeriesFileLongRunning(candidates, season, episode, parsed.Season, airDate, prior.IsAnimation); found {
						local = append(local, Stream{
							InfoHash:    t.InfoHash,
							FileIndex:   bestFile.ID,
							TorrentName: td.Name,
							Seeders:     t.Seeders,
							Language:    bestLang,
							Quality:     quality,
							Size:        td.Size,
							IsCached:    false,
							Badges:      parser.FormatBadges(td.Name),
						})
					} else {
						utils.Logger.Debug("filtering out series torrent: files found, but none matched requested season/episode", "name", td.Name, "season", season, "episode", episode)
					}
				} else {
					// Fallback: If no files info is populated yet, match against the torrent name directly
					matched := false
					if isSeasonPack {
						matched = true
					}
					if !matched && parsed.Episode == episode {
						matched = true
					}
					if !matched && parser.MatchRange(td.Name, episode) {
						matched = true
					}
					if !matched && isLongRunning && airDate != "" {
						parts := strings.Split(airDate, "-")
						if len(parts) == 3 {
							y, m, d := parts[0], parts[1], parts[2]
							permutations := []string{
								fmt.Sprintf("%s.%s.%s", y, m, d),
								fmt.Sprintf("%s-%s-%s", y, m, d),
								fmt.Sprintf("%s %s %s", y, m, d),
								fmt.Sprintf("%s.%s.%s", m, d, y),
								fmt.Sprintf("%s-%s-%s", m, d, y),
								fmt.Sprintf("%s %s %s", m, d, y),
								fmt.Sprintf("%s.%s.%s", d, m, y),
								fmt.Sprintf("%s-%s-%s", d, m, y),
								fmt.Sprintf("%s %s %s", d, m, y),
							}
							lowerName := strings.ToLower(td.Name)
							for _, perm := range permutations {
								if strings.Contains(lowerName, perm) {
									matched = true
									break
								}
							}
						}
					}

					if matched {
						local = append(local, Stream{
							InfoHash:    t.InfoHash,
							FileIndex:   0,
							TorrentName: td.Name,
							Seeders:     t.Seeders,
							Language:    bestLang,
							Quality:     quality,
							Size:        td.Size,
							IsCached:    false,
							Badges:      parser.FormatBadges(td.Name),
						})
					} else {
						utils.Logger.Debug("filtering out series torrent: fallback failed (no season pack or episode match)",
							"name", td.Name,
							"parsed_season", parsed.Season,
							"parsed_episode", parsed.Episode,
							"requested_season", season,
							"requested_episode", episode,
							"is_season_pack", isSeasonPack)
					}
				}
			} else {
				utils.Logger.Debug("filtering out series torrent: parsed season/episode/date does not match requested",
					"name", td.Name,
					"parsed_season", parsed.Season,
					"parsed_episode", parsed.Episode,
					"requested_season", season,
					"requested_episode", episode)
			}

			if len(local) > 0 {
				// Register matched filename dynamically to update Entropy Engine weights
				UpdateEntropyToken(td.Name)
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

func FindBestMovieStreams(ctx context.Context, tmdbMovie *bitmagnet.TorrentItem, altTitles []string, tmdbYear string, newTorrents []bitmagnet.TorrentItem, cachedRows []map[string]interface{}, preferredLanguages []string, prior AnimePriorMeta) (streams []Stream, cachedStreams []Stream) {
	// Dynamically load the self-learning Entropy Engine exactly once on the first query execution.
	// This eliminates startup circular dependency import cycles and cold-start latencies.
	entropyOnce.Do(func() {
		utils.Logger.Info("Entropy Engine: Initiating self-learning parser scan...")
		InitializeEntropyEngine(context.Background())
	})

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
					Badges:      parser.FormatBadges(fn),
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
		if torrent.Torrent.HasFilesInfo || torrent.Torrent.FilesStatus == "multi" {
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

			// Apply Bayesian LLR Anime Gated Shield
			if !EvaluateAnimeShield(td.Name, prior) {
				utils.Logger.Debug("filtering out movie torrent: failed anime shield", "name", td.Name)
				return nil
			}

			// Apply Temporal Disqualification Shield
			if t.PublishedAt != "" && tmdbYear != "" {
				premiereY, err := strconv.Atoi(tmdbYear)
				if err == nil {
					pubTs := parsePublishedAt(t.PublishedAt)
					if isNewerShowDisqualified(pubTs, premiereY) {
						utils.Logger.Debug("filtering out movie torrent: failed temporal disqualification", "name", td.Name, "published", t.PublishedAt, "premiere", tmdbYear)
						return nil
					}
				}
			}

			parsed := parser.RobustParseInfo(td.Name, 0)

			// Find the title (primary or alternate) that actually matched
			matchingTitle := ""
			bestSim := getTitleSimilarityParsed(tmdbMovie.Title, parsed.Title)
			if bestSim >= config.SimilarityThreshold {
				matchingTitle = tmdbMovie.Title
			}
			for _, alt := range altTitles {
				if s := getTitleSimilarityParsed(alt, parsed.Title); s > bestSim {
					bestSim = s
					if s >= config.SimilarityThreshold {
						matchingTitle = alt
					}
				}
			}

			if bestSim < config.SimilarityThreshold || matchingTitle == "" {
				utils.Logger.Debug("filtering out movie torrent: failed title similarity",
					"name", td.Name,
					"best_similarity", fmt.Sprintf("%.4f", bestSim),
					"threshold", config.SimilarityThreshold,
					"tmdb_title", tmdbMovie.Title)
				return nil
			}

			// Type-Leakage Prevention: TV Series episodes/packs must never match as movie streams
			if parsed.Season != 0 || parsed.Episode != 0 || parsed.IsPack {
				// Guardrail: If Season/Episode match common audio configurations (5.1 -> S05E01 or 7.1 -> S07E01)
				// and the title does NOT explicitly declare typical series patterns, bypass series rejection.
				isAudioFakeSeries := false
				if (parsed.Season == 5 && parsed.Episode == 1) || (parsed.Season == 7 && parsed.Episode == 1) || (parsed.Season == 2 && parsed.Episode == 0) {
					lowerName := strings.ToLower(td.Name)
					hasExplicitSeason := strings.Contains(lowerName, "s05") || strings.Contains(lowerName, "season 5") || strings.Contains(lowerName, "season.5") ||
						strings.Contains(lowerName, "s07") || strings.Contains(lowerName, "season 7") || strings.Contains(lowerName, "season.7") ||
						strings.Contains(lowerName, "s02") || strings.Contains(lowerName, "season 2") || strings.Contains(lowerName, "season.2")
					if !hasExplicitSeason {
						isAudioFakeSeries = true
					}
				}

				// Guardrail 2: If Season parses to >= 90 in movie mode (representing custom encoder presets like S90/S91 Joy Releases),
				// clear TV indicators and bypass series rejection.
				isEncoderFakeSeries := false
				if parsed.Season >= 90 {
					parsed.Season = 0
					parsed.IsPack = false
					isEncoderFakeSeries = true
				}

				// Guardrail 3: If Episode parses to >= 100 in movie mode (representing codec collisions
				// like x264 -> Episode 264 or x265 -> Episode 265), clear TV indicators and bypass series rejection.
				isCodecFakeSeries := false
				if parsed.Episode == 264 || parsed.Episode == 265 || parsed.Episode >= 100 {
					parsed.Season = 0
					parsed.Episode = 0
					parsed.IsPack = false
					isCodecFakeSeries = true
				}

				if !isAudioFakeSeries && !isEncoderFakeSeries && !isCodecFakeSeries {
					utils.Logger.Debug("filtering out movie torrent: contains TV series indicators", "name", td.Name, "season", parsed.Season, "episode", parsed.Episode, "is_pack", parsed.IsPack)
					return nil
				}
			}

			// ── UPGRADE: PN-SILEC Multi-Word Franchise Leakage Guardrail (Movie) ──
			if !passTitleGuardrail(matchingTitle, parsed.Title, altTitles) {
				utils.Logger.Debug("filtering out movie torrent: failed title guardrail", "target", matchingTitle, "parsed", parsed.Title)
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
				utils.Logger.Debug("filtering out movie torrent: failed year match check", "name", td.Name, "parsed_year", parsed.Year, "tmdb_year", tmdbYear)
				return nil
			}

			if td.HasFilesInfo || td.FilesStatus == "multi" {
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
			if parsed.Quality == "cam" || parsed.Quality == "ts" || parsed.Quality == "tc" || parsed.Quality == "scr" || parsed.Quality == "wp" || parsed.Quality == "regional" {
				quality = parsed.Quality
			} else if (quality == "sd" || quality == "") && parsed.Quality != "sd" && parsed.Quality != "" {
				quality = parsed.Quality
			}

			// Register matched filename dynamically to update Entropy Engine weights
			UpdateEntropyToken(td.Name)

			results <- jobResult{streams: []Stream{{
				InfoHash:    t.InfoHash,
				TorrentName: td.Name,
				Seeders:     t.Seeders,
				Language:    bestLang,
				Quality:     quality,
				Size:        td.Size,
				IsCached:    false,
				Badges:      parser.FormatBadges(td.Name),
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
