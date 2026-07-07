package parser

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	rtp "github.com/ovrlord-app/releasetitleparser"
)

type ParseResult struct {
	Title    string
	Season   int
	Episode  int
	Year     int
	Language string
	Quality  string
	IsPack   bool
}

type CandidateFile struct {
	ID   int
	Path string
	Size int64
}

type BadgeFilter struct {
	ID        string
	GroupID   string
	Name      string
	Positive  *regexp.Regexp
	Negatives []*regexp.Regexp
}

var languageToISO = map[rtp.Language]string{
	rtp.LanguageEnglish:       "en",
	rtp.LanguageSpanish:       "es",
	rtp.LanguageGerman:        "de",
	rtp.LanguageFrench:        "fr",
	rtp.LanguageItalian:       "it",
//	rtp.LanguageUniversal:     "en", // Map universal to english as a safe fallback
	rtp.LanguageRussian:       "ru",
	rtp.LanguageJapanese:      "ja",
	rtp.LanguageChinese:       "zh",
	rtp.LanguageKorean:        "ko",
	rtp.LanguagePortuguese:    "pt",
	rtp.LanguagePortugueseBR:  "pt-BR",
	rtp.LanguageDutch:         "nl",
	rtp.LanguageDanish:        "da",
	rtp.LanguageNorwegian:     "no",
	rtp.LanguageSwedish:       "sv",
	rtp.LanguageFinnish:       "fi",
	rtp.LanguagePolish:        "pl",
	rtp.LanguageCzech:         "cs",
	rtp.LanguageSlovak:        "sk",
	rtp.LanguageHungarian:     "hu",
	rtp.LanguageRomanian:      "ro",
	rtp.LanguageBulgarian:     "bg",
	rtp.LanguageUkrainian:     "uk",
	rtp.LanguageGreek:         "el",
	rtp.LanguageTurkish:       "tr",
	rtp.LanguageArabic:        "ar",
	rtp.LanguageHindi:         "hi",
	rtp.LanguageThai:          "th",
	rtp.LanguageVietnamese:    "vi",
	rtp.LanguageHebrew:        "he",
	rtp.LanguagePersian:       "fa",
	rtp.LanguageBengali:       "bn",
	rtp.LanguageLatvian:       "lv",
	rtp.LanguageLithuanian:    "lt",
	rtp.LanguageSpanishLatino: "es-MX",
	rtp.LanguageTamil:         "ta",
	rtp.LanguageTelugu:        "te",
	rtp.LanguageMalayalam:     "ml",
	rtp.LanguageKannada:       "kn",
	rtp.LanguageAlbanian:      "sq",
	rtp.LanguageAfrikaans:     "af",
	rtp.LanguageMarathi:       "mr",
	rtp.LanguageTagalog:       "tl",
	rtp.LanguageIcelandic:     "is",
	rtp.LanguageFlemish:       "nl-BE",
	rtp.LanguageUrdu:          "ur",
	rtp.LanguageMongolian:     "mn",
	rtp.LanguageGeorgian:      "ka",
	rtp.LanguageRomansh:       "rm",
	rtp.LanguageOriginal:      "original",
	rtp.LanguageCatalan:       "ca",
	rtp.LanguageAzerbaijani:   "az",
	rtp.LanguageUzbek:         "uz",
}

// Collapses spaces and symbols between SXX and EP(XX) to force standard SXXEXX grouping
var epPatternRegex = regexp.MustCompile(`(?i)(S\d+)?[\s\-_]*\bEP[\s\-_]*[\(\[]?\s*(\d+)\s*[\)\]]?\b`)
var urlRegex = regexp.MustCompile(`\b(https?://\S+|www\.\S+\.\w+|[\w.-]+@[\w.-]+)\b`)
var bracketRegex = regexp.MustCompile(`\[.*?[^\w\s-].*?\]`)

var rangeRegex = regexp.MustCompile(`(?i)\b(?:e|ep|episode)?\s*(\d+)\s*(?:-|to)\s*(?:e|ep|episode)?\s*(\d+)\b`)
var seasonFolderRegex = regexp.MustCompile(`(?i)\b(?:s|season|series)\s*0*(\d+)\b`)

// Pre-compiled regexes for RobustParseInfo safety fallback
var sxeRegex = regexp.MustCompile(`(?i)\bS(\d+)\s*E(\d+)\b`)

// Restrict crossRegex to 1-2 digit seasons and 2-4 digit episodes to prevent Year-Codec / Resolution-Codec collisions
var crossRegex = regexp.MustCompile(`(?i)\b([0-9]{1,2})\s*x\s*([0-9]{2,4})\b`)

// Refined seasonRangeRegex to optionally support redundant second season prefixes (e.g. S01-S21, Season 1 to Season 2)
var seasonRangeRegex = regexp.MustCompile(`(?i)\b(?:s|season|seasons)\s*0*(\d+)\s*(?:-|to|~)\s*(?:s|season|seasons)?\s*0*(\d+)\b`)

// Regional patterns for TamilMV/TamilBlasters indexer formats
var regionalRangeRegex = regexp.MustCompile(`(?i)\b(?:season|s|series)\s*(\d+)\s*(?:ep|episode|e)?\s*[\(\[]?\s*(\d+)\s*(?:-|to)\s*(\d+)\s*[\)\]]?\b`)
var regionalSingleRegex = regexp.MustCompile(`(?i)\b(?:season|s|series)\s*(\d+)\s*(?:ep|episode|e)\s*[\(\[]?\s*(\d+)\s*[\)\]]?\b`)

// Precompiled multi-episode structures for advanced range processing (Sonarr/Radarr compatible)
var conjoinedRegex = regexp.MustCompile(`(?i)\b(?:e|ep|episode)(\d+)(?:e|ep|episode)(\d+)\b`)
var compactRegex = regexp.MustCompile(`(?i)\bE(\d{2})(\d{2})\b`)

// Radarr/Sonarr Website Domain Prefix Stripper - Upgraded to safely exclude movie titles matching [a-z0-9-]+\.[a-z]{2,6} by limiting allowed TLD extensions
var websitePrefixRegex = regexp.MustCompile(`(?i)(?:^|[\s_.-]*)(?:(?:www\d*\.)[a-z0-9-]+\.[a-z]{2,6}\b|\[\s*(?:www\d*\.)?[a-z0-9-]+\.[a-z]{2,6}\s*\]|[a-z0-9-]+\.(?:com|net|org|co|info|yt|tf|re|pm|club|xyz|site|online|me|tv|cc|ws|to|biz|us|uk|ca|in|app|link|io|ag|am|cat|best|release|pe|wf|cx|gd|la|mu|ms|nu|se|tc|vc|vg)\b)[\s_.-]*`)

// Match common decimal channel audio configurations (e.g. 5.1, 7.1, 2.0) to prevent TV show misclassifications
var audioChannelsRegex = regexp.MustCompile(`(?i)\b([1-9])\.([0-9])\b`)

// Match standalone resolution numbers without trailing 'p' (e.g. 1080, 720, 2160) to prevent S10E80 parsing splits
var resolutionNoPRegex = regexp.MustCompile(`\b(2160|1080|720|480|360)\b`)

// Conjoined metadata regexes with strict lower-bounds of 3 characters to prevent short-word collisions (e.g. Scratch1080p, Scratchx264, S01WEBRip)
var conjoinedQualityRegex = regexp.MustCompile(`(?i)\b([a-z]{3,})(2160p|1080p|720p|480p|360p|4k|uhd)\b`)
var conjoinedCodecRegex = regexp.MustCompile(`(?i)\b([a-z]{3,})(x264|x265|h264|h265|hevc|avc)\b`)
var conjoinedSourceRegex = regexp.MustCompile(`(?i)\b([a-z]{3,})(dlrip|webrip|webdl|bluray|hdtv|bdrip|brrip)\b`)
var conjoinedSeasonRegex = regexp.MustCompile(`(?i)\b(S\d+)(webrip|webdl|bluray|hdtv|bdrip|brrip|x264|x265|h264|h265|2160p|1080p|720p|4k|uhd)\b`)

// Conjoined audio regexes with strict lower-bounds of 3 characters
var conjoinedAudioDigitsRegex = regexp.MustCompile(`(?i)\b(\d+)(dts|ac3|aac|mp3)\b`)
var conjoinedAudioAlphaRegex = regexp.MustCompile(`(?i)\b([a-z]{3,})(dts|ac3|aac|mp3)\b`)

// Conjoined alphanumeric splitters to separate squashed numbers and letters natively (e.g. 2007mp4 -> 2007 mp4, 300FLAiTE -> 300 FLAiTE)
var conjoinedDigitToLetters = regexp.MustCompile(`(?i)\b(\d+)([a-z]{2,})\b`)
var conjoinedLettersToDigits = regexp.MustCompile(`(?i)\b([a-z]{2,})(\d+)\b`)
var conjoinedDigitToCodec = regexp.MustCompile(`(?i)\b(\d+)(x264|x265|h264|h265|hevc|avc|webrip|webdl|bluray|hdtv|bdrip|brrip|2160p|1080p|720p|4k|uhd)\b`)

// Unified metadata boundary pattern to slice titles cleanly at the earliest occurrence of any noise/season tags (Including standard audio codecs)
var boundaryRegex = regexp.MustCompile(`(?i)\b(?:S\d+E\d+|S\d+|\d+x\d+|Season\s*\d+|Seasons\s*\d+|2160p|1080p|720p|480p|360p|4k|uhd|bluray|hdtv|web[-_.]?dl|webrip|hdr|sdr|h264|h265|x264|x265|hevc|ddp|dd\+|eac3|truehd|atmos|ac3|dts|aac|mp3|flac|19\d{2}|20\d{2})\b`)

// Fail-safe positive curation engines regex matches based on TRaSH Guides and Servarr pipelines
// Expanded to handle complex variations of PreDVD and deceptive HQRip naming conventions
var (
	wpRegex  = regexp.MustCompile(`(?i)\b(?:workprint|wp)\b`)
	camRegex = regexp.MustCompile(`(?i)\b(?:cam|camrip|hdcam|cam-?rip)\b`)
	tsRegex  = regexp.MustCompile(`(?i)\b(?:ts|hdts|telesync|tele-?sync|ppvrip|pdvdrip)\b`)
	tcRegex  = regexp.MustCompile(`(?i)\b(?:tc|hdtc|telecine|tele-?cine)\b`)
	scrRegex = regexp.MustCompile(`(?i)\b(?:scr|screener|dvdscr|bdscr|dvd-?scr|ddc|dvdscreener|pre[-_ ]?dvd(?:rip)?|predvd(?:rip)?)\b`)
	r5Regex  = regexp.MustCompile(`(?i)\b(?:r5|r6|r5line|r5.line|line.audio|ac3md|ac3ld|line.dub|hq[-_ ]?rip|hq[-_ ]?hdrip|hqrip)\b`)
)

func DetectLowQuality(title string) string {
	lower := strings.ToLower(title)
	if camRegex.MatchString(lower) {
		return "cam"
	}
	if tsRegex.MatchString(lower) {
		return "ts"
	}
	if tcRegex.MatchString(lower) {
		return "tc"
	}
	if scrRegex.MatchString(lower) {
		return "scr"
	}
	if wpRegex.MatchString(lower) {
		return "wp"
	}
	if r5Regex.MatchString(lower) {
		return "regional"
	}
	return ""
}

// Low-Allocation pre-defined filters deconstructed from Perl badges.json to RE2 standard.
// Matches exactly all 39 filters defined in badges.json with extended support for NF and AMZN.
var filtersDef = []struct {
	ID        string
	GroupID   string
	Name      string
	Positive  string
	Negatives []string
}{
	// Quality
	{"q-r", "gq", "Remux", `(?i)\bremux\b`, nil},
	{"q-b", "gq", "BluRay", `(?i)\b(?:blu[-_. ]?ray|b[rd][-_. ]?rip)\b`, []string{`(?i)\bremux\b`}},
	{"q-w", "gq", "WEB-DL", `(?i)\bweb[-_. ]?dl\b`, nil},
	{"src-webrip", "gq", "WEBRip", `(?i)\bweb[-_. ]?rip\b`, nil},
	{"src-hdtv", "gq", "HDTV", `(?i)\bhdtv\b`, nil},
	{"src-hdrip", "gq", "HDRip", `(?i)\bhd[-_. ]?rip\b`, nil},
	{"src-dvdrip", "gq", "DVDRip", `(?i)\bdvd[-_. ]?rip\b`, nil},

	// Resolution
	{"r-4k", "gr", "4K", `(?i)\b2160[pi]?\b|\b4k\b|\buhd\b`, []string{`(?i)\b1080[pi]?\b|\b720[pi]?\b`}},
	{"r-1080", "gr", "1080p", `(?i)\b1080[pi]?\b`, nil},
	{"r-720", "gr", "720p", `(?i)\b720[pi]?\b`, nil},

	// Visual
	{"v-seadex", "gv", "SeaDex", `(?i)\b(?:seadex|best[\s._-]?release|alt[\s._-]?release)\b|ᴀʟᴛ ʀᴇʟᴇᴀsᴇ|ʙᴇsᴛ ʀᴇʟᴇᴀsᴇ`, nil},
	{"v-hdr10p", "gv", "HDR10+", `(?i)\bhdr[\s._-]?10[\s._-]?(?:\+|plus|p)(?:\b|[^a-z0-9]|$)\b`, []string{`(?i)\b(?:dv|dovi|dolby[\s._-]?vision)\b`}},
	{"v-hdr10", "gv", "HDR10", `(?i)\bhdr[\s._-]?10\b`, []string{`(?i)\b(?:dv|dovi|dolby[\s._-]?vision)\b`, `(?i)\bhdr[\s._-]?10[\s._-]?(?:\+|plus|p)(?:\b|[^a-z0-9]|$)\b`}},
	{"v-hdr", "gv", "HDR", `(?i)\bhdr\b`, []string{`(?i)\b(?:dv|dovi|dolby[\s._-]?vision)\b`, `(?i)\bhdr[\s._-]?10\b`}},
	{"v-sdr", "gv", "SDR", `(?i)\bsdr\b`, []string{`(?i)\b(?:hdr|hdr10|hdr10\+|dv|dovi|dolby[\s._-]?vision)\b`}},
	{"v-imax-e", "gv", "IMAX Enhanced", `(?i)\bimax[\s._-]?enhanced\b`, nil},
	{"v-imax", "gv", "IMAX", `(?i)\bimax\b`, []string{`(?i)\benhanced\b`}},
	{"a-dv", "gv", "DV", `(?i)\b(?:dv|dovi|dolby[\s._-]?vision)\b`, nil},

	// Audio
	{"a-dtsx", "ga", "DTS:X", `(?i)\bdts[-_.: ]?x\b`, nil},
	{"a-dtsma", "ga", "DTS-HD MA", `(?i)\bdts[-_. ]?(?:hd[-_. ]?)?ma\b`, []string{`(?i)\bdts[-_.: ]?x\b`}},
	{"a-dtshd", "ga", "DTS-HD", `(?i)\bdts[-_. ]?hd\b`, []string{`(?i)\bdts[-_. ]?(?:hd[-_. ]?)?ma\b`, `(?i)\bdts[-_.: ]?x\b`}},
	{"a-dts", "ga", "DTS", `(?i)\bdts\b`, []string{`(?i)\bdts[-_. ]?(?:hd|ma|xll|x)\b`}},
	{"a-at", "ga", "Atmos", `(?i)\batmos\b`, nil},
	{"a-th", "ga", "TrueHD", `(?i)\btrue[\s._-]?hd\b`, nil},
	{"a-dp", "ga", "DD+", `(?i)\b(?:ddp|dd\+|eac-?3|e-?ac-?3)\b`, []string{`(?i)\btrue[\s._-]?hd\b`}},
	{"a-dd", "ga", "DD", `(?i)\b(?:dd[25][. ][01]|ac-?3)\b`, []string{`(?i)\b(?:ddp|dd\+|eac-?3|e-?ac-?3)\b`, `(?i)\batmos\b`, `(?i)\btrue[\s._-]?hd\b`}},

	// Channels
	{"ch-71", "gc", "7.1", `(?i)(?:^|[^0-9])[7-8][. ][01](?:[^0-9]|$)\b`, nil},
	{"ch-51", "gc", "5.1", `(?i)(?:^|[^0-9])5[. ][01](?:[^0-9]|$)\b`, []string{`(?i)(?:^|[^0-9])[7-8][. ][01](?:[^0-9]|$)\b`}},

	// Streaming
	{"s-nflx", "gs", "NETFLIX", `(?i)\b(?:nflx|netflix|nf)\b`, nil},
	{"s-amzn", "gs", "PRIME VIDEO", `(?i)\b(?:amzn|amazon|prime[\s._-]?video)\b`, nil},
	{"s-atvp", "gs", "APPLE TV+", `(?i)\b(?:atvp|apple[\s._-]?tv\+?|appletv)\b`, nil},
	{"s-dsnp", "gs", "DISNEY+", `(?i)\b(?:dsnp|dsny|disney\+?|disney[\s._-]?plus)\b`, nil},
	{"s-hmax", "gs", "HBO MAX", `(?i)(?:\b(?:hmax|hbomax|hbo[\s._-]?max)\b|(?:^|[\s._-])max(?:[\s._-]|$))`, nil},
	{"s-hulu", "gs", "HULU", `(?i)\bhulu\b`, nil},
	{"s-pcok", "gs", "PEACOCK", `(?i)\b(?:pcok|peacock)\b`, nil},
	{"s-pamp", "gs", "PARAMOUNT+", `(?i)\b(?:pmtp|pamp|paramount\+?|paramount[\s._-]?plus)\b`, nil},
	{"s-croll", "gs", "CRUNCHYROLL", `(?i)\b(?:croll|crunchy|crunchyroll)\b`, nil},
}

func init() {
	for _, f := range filtersDef {
		var posNeg BadgeFilter
		posNeg.ID = f.ID
		posNeg.GroupID = f.GroupID
		posNeg.Name = f.Name
		posNeg.Positive = regexp.MustCompile(f.Positive)
		for _, n := range f.Negatives {
			posNeg.Negatives = append(posNeg.Negatives, regexp.MustCompile(n))
		}
		CompiledFilters = append(CompiledFilters, posNeg)
	}
}

// Thread-safe Parse Cache to eliminate duplicate parsing latency
type parseCacheEntry struct {
	result    *ParseResult
	expiresAt time.Time
}

var (
	parseCache   = make(map[string]parseCacheEntry)
	parseCacheMu sync.RWMutex
)

var CompiledFilters []BadgeFilter

// ParsePackOrRange checks if a torrent name is a complete pack or contains an episode range
func ParsePackOrRange(name string, targetE int) (isPack bool, startE int, endE int, hasRange bool) {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "complete") || strings.Contains(lower, "pack") || strings.Contains(lower, "bundle") {
		return true, 0, 0, false
	}

	if MatchRange(name, targetE) {
		if match := rangeRegex.FindStringSubmatch(name); len(match) >= 3 {
			start, _ := strconv.Atoi(match[1])
			end, _ := strconv.Atoi(match[2])
			return false, start, end, true
		}
		if match := regionalRangeRegex.FindStringSubmatch(name); len(match) >= 4 {
			start, _ := strconv.Atoi(match[2])
			end, _ := strconv.Atoi(match[3])
			return false, start, end, true
		}
		if match := conjoinedRegex.FindStringSubmatch(name); len(match) >= 3 {
			start, _ := strconv.Atoi(match[1])
			end, _ := strconv.Atoi(match[2])
			return false, start, end, true
		}
		if match := compactRegex.FindStringSubmatch(name); len(match) >= 3 {
			start, _ := strconv.Atoi(match[1])
			end, _ := strconv.Atoi(match[2])
			return false, start, end, true
		}
	}
	return false, 0, 0, false
}

// MatchRange checks if a string contains any episode range or multi-episode format covering the target episode.
// This is fully optimized and inspired by advanced Sonarr/Radarr parsing patterns.
func MatchRange(path string, targetEpisode int) bool {
	// Extract base filename to prevent parent folder names from polluting range analysis
	fileName := path
	if idx := strings.LastIndexAny(path, "/\\"); idx != -1 {
		fileName = path[idx+1:]
	}

	// 1. Standard Range Pattern: ep01-05, E01-E05, episodes 1 to 5, etc.
	matches := rangeRegex.FindAllStringSubmatchIndex(fileName, -1)
	for _, match := range matches {
		if len(match) >= 6 {
			startNumStart := match[2]
			startNumEnd := match[3]
			endNumStart := match[4]
			endNumEnd := match[5]

			// Skip matches that are part of decimal numbers (e.g. 13.00-14.00)
			if startNumStart > 0 && isDecimalDot(fileName, startNumStart-1) {
				continue
			}
			if endNumEnd < len(fileName) && isDecimalDot(fileName, endNumEnd) {
				continue
			}

			start, err1 := strconv.Atoi(fileName[startNumStart:startNumEnd])
			end, err2 := strconv.Atoi(fileName[endNumStart:endNumEnd])
			if err1 == nil && err2 == nil {
				if start <= end && targetEpisode >= start && targetEpisode <= end {
					return true
				}
			}
		}
	}

	// 2. Regional Range Pattern: Season 01 EP(01-08), Season 01 Ep 01 to 08
	if match := regionalRangeRegex.FindStringSubmatch(fileName); len(match) >= 4 {
		start, err1 := strconv.Atoi(match[2])
		end, err2 := strconv.Atoi(match[3])
		if err1 == nil && err2 == nil {
			if start <= end && targetEpisode >= start && targetEpisode <= end {
				return true
			}
		}
	}

	// 3. Conjoined Range Pattern: S01E01E03, ep01ep03, E01E02, ep1ep5
	if match := conjoinedRegex.FindStringSubmatch(fileName); len(match) >= 3 {
		start, err1 := strconv.Atoi(match[1])
		end, err2 := strconv.Atoi(match[2])
		if err1 == nil && err2 == nil {
			if start <= end && targetEpisode >= start && targetEpisode <= end {
				return true
			}
		}
	}

	// 4. Compact Double Episode Pattern: S01E0102, S01E0304
	if match := compactRegex.FindStringSubmatch(fileName); len(match) >= 3 {
		start, err1 := strconv.Atoi(match[1])
		end, err2 := strconv.Atoi(match[2])
		if err1 == nil && err2 == nil {
			if start <= end && targetEpisode >= start && targetEpisode <= end {
				return true
			}
		}
	}

	return false
}

// FormatBadges scans the source filename exactly once and extracts matched tags.
// Results are grouped in priority layout: Resolution -> Quality -> Visual -> Audio -> Channels -> Encoder -> Streaming
func FormatBadges(title string) string {
	var res, qual, vis, aud, ch, enc, str string

	for i := range CompiledFilters {
		f := &CompiledFilters[i]
		if f.Positive.MatchString(title) {
			// Perform lookahead-simulating logical negation assertions
			excluded := false
			for _, neg := range f.Negatives {
				if neg.MatchString(title) {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}

			switch f.GroupID {
			case "gr":
				if res == "" {
					res = f.Name
				}
			case "gq":
				if qual == "" {
					qual = f.Name
				}
			case "gv":
				if vis == "" {
					vis = f.Name
				}
			case "ga":
				if aud == "" {
					aud = f.Name
				}
			case "gc":
				if ch == "" {
					ch = f.Name
				}
			case "ge":
				if enc == "" {
					enc = f.Name
				}
			case "gs":
				if str == "" {
					str = f.Name
				}
			}
		}
	}

	// Dynamic slice building with pre-allocated hints to prevent heap allocation resizing
	parts := make([]string, 0, 7)
	if res != "" {
		parts = append(parts, "["+res+"]")
	}
	if qual != "" {
		parts = append(parts, "["+qual+"]")
	}
	if vis != "" {
		parts = append(parts, "["+vis+"]")
	}
	if aud != "" {
		parts = append(parts, "["+aud+"]")
	}
	if ch != "" {
		parts = append(parts, "["+ch+"]")
	}
	if enc != "" {
		parts = append(parts, "["+enc+"]")
	}
	if str != "" {
		parts = append(parts, "["+str+"]")
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func normalizeEpisodePatterns(s string) string {
	return epPatternRegex.ReplaceAllString(s, "${1}E${2}")
}

func getISO(lang rtp.Language) string {
	if iso, ok := languageToISO[lang]; ok {
		return iso
	}
	return "en"
}

func getQuality(res int) string {
	switch res {
	case 2160:
		return "4k"
	case 1080:
		return "1080p"
	case 720:
		return "720p"
	case 480:
		return "480p"
	case 360:
		return "360p"
	default:
		return "sd"
	}
}

func SanitizeName(name string) string {
	// Strip Radarr/Sonarr-style Website Prefix Domains (e.g. www.1TamilMV.yt - or [TamilBlasters.gripe] ) before parsing
	s := websitePrefixRegex.ReplaceAllString(name, "")

	// Normalize standalone resolutions to include 'p' (e.g., 1080 -> 1080p) to prevent TV parser misclassifying them as S10E80
	s = resolutionNoPRegex.ReplaceAllString(s, "${1}p")

	// Replace audio channels like 5.1, 7.1, 2.0 with 5ch, 7ch, 2ch to prevent dot replacement from tokenizing them as series season/episode numbers (e.g. 5 1)
	s = audioChannelsRegex.ReplaceAllString(s, "${1}ch")

	// Insert spaces before conjoined technical keywords to prevent unified word tokenization failures (e.g. Scratch1080p -> Scratch 1080p)
	s = conjoinedQualityRegex.ReplaceAllString(s, "$1 $2")
	s = conjoinedCodecRegex.ReplaceAllString(s, "$1 $2")
	s = conjoinedSourceRegex.ReplaceAllString(s, "$1 $2")
	s = conjoinedSeasonRegex.ReplaceAllString(s, "$1 $2")

	// Insert spaces before conjoined audio tokens (e.g. 2009DTS -> 2009 DTS, AC3HELLYWOOD -> AC3 HELLYWOOD)
	s = conjoinedAudioDigitsRegex.ReplaceAllString(s, "$1 $2")
	s = conjoinedAudioAlphaRegex.ReplaceAllString(s, "$1 $2")

	// Insert spaces between conjoined digits and letters (e.g. 2007mp4 -> 2007 mp4, 300FLAiTE -> 300 FLAiTE)
	s = conjoinedDigitToLetters.ReplaceAllString(s, "$1 $2")
	s = conjoinedLettersToDigits.ReplaceAllString(s, "$1 $2")
	s = conjoinedDigitToCodec.ReplaceAllString(s, "$1 $2")

	// Replace dot and underscore delimiters with standard spaces to prevent conjoined word parsing errors
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.ReplaceAll(s, "_", " ")

	// 1. Replace non-breaking spaces (\u00a0, \u200b) to standard spaces
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\u200b", " ")

	// 2. Normalize episode patterns (e.g. S02 EP(15) -> S02E15)
	s = normalizeEpisodePatterns(s)

	// 3. Remove non-ASCII scripts (Chinese, Cyrillic, Japanese, etc.)
	hasNonASCII := false
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			hasNonASCII = true
			break
		}
	}
	if hasNonASCII {
		var b strings.Builder
		b.Grow(len(s))
		for _, r := range s {
			if r > unicode.MaxASCII {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(r)
		}
		s = b.String()
	}

	// 4. Remove residual URLs/domains (e.g. www.BTHDTV.com)
	s = urlRegex.ReplaceAllString(s, " ")

	// 5. Remove residual empty/garbage brackets
	s = bracketRegex.ReplaceAllString(s, " ")

	s = strings.Join(strings.Fields(s), " ")
	
	// 6. Trim leftover leading/trailing punctuation
	s = strings.TrimLeft(s, " .-_[]()/\\")
	s = strings.TrimRight(s, " .-_[]()/\\")
	return s
}

// Precompiled regex matching leading season/episode patterns (e.g., S01E01, [S01E01], 2x03, [2x03])
var leadingEpPatternRe = regexp.MustCompile(`(?i)^[\s\-_.]*\[?(S\d+E\d+|\b\d+x\d+\b)\]?[\s\-_.]*`)

// ShiftLeadingEpisodePattern transposes any leading episode pattern to the end of the string
// so that releasetitleparser can cleanly extract the series title.
func ShiftLeadingEpisodePattern(s string) string {
	if match := leadingEpPatternRe.FindStringSubmatch(s); len(match) > 1 {
		matchedStr := match[0]
		epPattern := match[1]

		// Strip the pattern from the front
		stripped := s[len(matchedStr):]
		stripped = strings.TrimSpace(stripped)

		// Append the episode pattern to the end
		return fmt.Sprintf("%s %s", stripped, epPattern)
	}
	return s
}

var dateEpisodeRegex = regexp.MustCompile(`(?i)\b(\d{4})[\.\-_ ](\d{2})[\.\-_ ](\d{2})\b`)

func RobustParseInfo(title string, fallbackSeason int) *ParseResult {
	parseCacheMu.RLock()
	entry, ok := parseCache[title]
	parseCacheMu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.result
	}

	preprocessedTitle := ShiftLeadingEpisodePattern(title)
	clean := SanitizeName(preprocessedTitle)

	var result *ParseResult
	if m := dateEpisodeRegex.FindStringSubmatch(clean); len(m) == 4 {
		idx := strings.Index(clean, m[0])
		titleStr := strings.Trim(strings.TrimSpace(clean[:idx]), " .-_")

		quality := "sd"
		lowerClean := strings.ToLower(clean)
		if strings.Contains(lowerClean, "2160") || strings.Contains(lowerClean, "4k") {
			quality = "4k"
		} else if strings.Contains(lowerClean, "1080") {
			quality = "1080p"
		} else if strings.Contains(lowerClean, "720") {
			quality = "720p"
		} else if strings.Contains(lowerClean, "480") {
			quality = "480p"
		} else if strings.Contains(lowerClean, "360") {
			quality = "360p"
		}

		result = &ParseResult{
			Title:    titleStr,
			Season:   fallbackSeason,
			Episode:  0,
			Year:     0,
			Language: "en",
			Quality:  quality,
		}
	} else {
		info := rtp.ParseSeriesTitle(clean)
		if info != nil && (info.SeasonNumber != 0 || len(info.EpisodeNumbers) > 0) {
			lang := "en"
			if len(info.Languages) > 0 {
				lang = getISO(info.Languages[0])
			}
			episode := 0
			if len(info.EpisodeNumbers) > 0 {
				episode = info.EpisodeNumbers[0]
			}
			result = &ParseResult{
				Title:    info.SeriesTitle,
				Season:   info.SeasonNumber,
				Episode:  episode,
				Year:     info.SeriesTitleInfo.Year,
				Language: lang,
				Quality:  getQuality(info.Quality.Quality.Resolution),
				IsPack:   IsPack(info),
			}
		} else {
			// High-performance unified POSIX fallback parsing engine
			// This guarantees that we extract season/episode and pack details consistently
			var parsedTitle string
			var season, episode int
			var isPack bool
			found := false

			// 1. Try to find standard SXXEXX pattern
			if match := sxeRegex.FindStringSubmatchIndex(clean); len(match) >= 6 {
				season, _ = strconv.Atoi(clean[match[2]:match[3]])
				episode, _ = strconv.Atoi(clean[match[4]:match[5]])
				parsedTitle = strings.TrimSpace(clean[:match[0]])
				found = true
			} else if match := regionalRangeRegex.FindStringSubmatchIndex(clean); len(match) >= 8 {
				// 2. Try to find regional season and episode range (e.g. Season 1 EP(01-08))
				season, _ = strconv.Atoi(clean[match[2]:match[3]])
				episode, _ = strconv.Atoi(clean[match[4]:match[5]]) // Take first episode of the range
				parsedTitle = strings.TrimSpace(clean[:match[0]])
				found = true
			} else if match := regionalSingleRegex.FindStringSubmatchIndex(clean); len(match) >= 6 {
				// 3. Try to find regional season and single episode (e.g. Season 1 EP 02)
				season, _ = strconv.Atoi(clean[match[2]:match[3]])
				episode, _ = strconv.Atoi(clean[match[4]:match[5]])
				parsedTitle = strings.TrimSpace(clean[:match[0]])
				found = true
			} else if match := crossRegex.FindStringSubmatchIndex(clean); len(match) >= 6 {
				season, _ = strconv.Atoi(clean[match[2]:match[3]])
				episode, _ = strconv.Atoi(clean[match[4]:match[5]])
				parsedTitle = strings.TrimSpace(clean[:match[0]])
				found = true
			} else if match := seasonRangeRegex.FindStringSubmatchIndex(clean); len(match) >= 6 {
				// 5. Try to find season range packs (S01-S03)
				season, _ = strconv.Atoi(clean[match[2]:match[3]]) // Take start season as default
				isPack = true
				parsedTitle = strings.TrimSpace(clean[:match[0]])
				found = true
			} else if match := seasonFolderRegex.FindStringSubmatchIndex(clean); len(match) >= 4 {
				// 6. Try to find single season folder packs (S03 or Season 3)
				season, _ = strconv.Atoi(clean[match[2]:match[3]])
				isPack = true
				parsedTitle = strings.TrimSpace(clean[:match[0]])
				found = true
			}

			// 7. Clean up title slicing fallback using general boundary slicing if found
			if found && parsedTitle != "" {
				// Clean trailing symbols / delimiters from sliced title
				parsedTitle = strings.Trim(parsedTitle, " .-_[]()/\\")
				result = &ParseResult{
					Title:    parsedTitle,
					Season:   season,
					Episode:  episode,
					Language: "en",
					Quality:  "sd",
					IsPack:   isPack,
				}
			} else {
				// Slicing Fallback: Scan the entire string and slice at the earliest boundary that is NOT at index 0 (which is the title itself)
				// This prevents numeric titles (like "2012" or "300") from blocking metadata checks (such as "2009" or "2007") later in the file name.
				var bestLoc []int
				for _, loc := range boundaryRegex.FindAllStringIndex(clean, -1) {
					if loc != nil && loc[0] > 0 {
						if bestLoc == nil || loc[0] < bestLoc[0] {
							bestLoc = loc
						}
					}
				}

				if bestLoc != nil {
					slicedTitle := strings.Trim(clean[:bestLoc[0]], " .-_[]()/\\")
					if slicedTitle != "" {
						result = &ParseResult{
							Title:    slicedTitle,
							Season:   fallbackSeason,
							Episode:  0,
							Language: "en",
							Quality:  "sd",
						}
					}
				} else {
					// Absolute raw fallback
					result = &ParseResult{
						Title:    clean,
						Season:   fallbackSeason,
						Episode:  0,
						Language: "en",
						Quality:  "sd",
					}
				}
			}
		}
	}

	// Positive Curation Hook: Match the title against early theatrical leak indicators
	// to dynamically override the quality output parameter before committing to memory cache
	if lowQual := DetectLowQuality(title); lowQual != "" {
		result.Quality = lowQual
	}

	parseCacheMu.Lock()
	parseCache[title] = parseCacheEntry{
		result:    result,
		expiresAt: time.Now().Add(24 * time.Hour),
	}
	// Bound capacity to prevent memory leaks (Max 10000 entries)
	if len(parseCache) > 10000 {
		for k := range parseCache {
			delete(parseCache, k)
			break
		}
	}
	parseCacheMu.Unlock()

	return result
}

func ParseFilePath(path string, fallbackSeason int) *ParseResult {
	// Extract the base filename to prevent parent folder names (e.g., S01 EP (01-08)) from polluting parsing
	fileName := path
	if idx := strings.LastIndexAny(path, "/\\"); idx != -1 {
		fileName = path[idx+1:]
	}

	cleanPath := normalizeEpisodePatterns(fileName)
	info := rtp.ParseSeriesPath(cleanPath)
	if info != nil && (info.SeasonNumber != 0 || len(info.EpisodeNumbers) > 0) {
		episode := 0
		if len(info.EpisodeNumbers) > 0 {
			episode = info.EpisodeNumbers[0]
		}
		season := info.SeasonNumber
		if season == 0 {
			season = fallbackSeason
		}
		return &ParseResult{
			Title:   info.SeriesTitle,
			Season:  season,
			Episode: episode,
		}
	}
	return &ParseResult{
		Season:  fallbackSeason,
		Episode: 0,
	}
}

func IsPack(info *rtp.ParsedEpisodeInfo) bool {
	return info != nil && (info.FullSeason || info.IsPartialSeason || info.IsMultiSeason)
}

func isExtraOrSpecial(path string) bool {
	p := strings.ToLower(path)
	return strings.Contains(p, "special") ||
		strings.Contains(p, "bonus") ||
		strings.Contains(p, "trailer") ||
		strings.Contains(p, "featurette") ||
		strings.Contains(p, "recap") ||
		strings.Contains(p, "sample") ||
		strings.Contains(p, "extra") ||
		strings.Contains(p, "behind the scenes") ||
		strings.Contains(p, "interview")
}

func isExtraOrSpecialRelaxed(path string) bool {
	p := strings.ToLower(path)
	return strings.Contains(p, "bonus") ||
		strings.Contains(p, "trailer") ||
		strings.Contains(p, "featurette") ||
		strings.Contains(p, "recap") ||
		strings.Contains(p, "sample") ||
		strings.Contains(p, "behind the scenes") ||
		strings.Contains(p, "interview")
}

func isDecimalDot(s string, i int) bool {
	if i <= 0 || i >= len(s)-1 {
		return false
	}
	if s[i] != '.' {
		return false
	}
	left := s[i-1]
	right := s[i+1]
	return left >= '0' && left <= '9' && right >= '0' && right <= '9'
}

func FindBestSeriesFileLongRunning(candidates []CandidateFile, targetSeason, targetEpisode, fallbackSeason int, airDate string, isAnimation bool) (CandidateFile, bool) {
	var bestCandidate CandidateFile
	var found bool
	var maxWeight int64 = -1

	// Dynamically select target filters depending on requested season context
	checkExtra := isExtraOrSpecial
	if targetSeason == 0 {
		checkExtra = isExtraOrSpecialRelaxed
	}

	parts := strings.Split(airDate, "-")
	var permutations []string
	if len(parts) == 3 {
		y, m, d := parts[0], parts[1], parts[2]
		permutations = []string{
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
	}

	// 1. Direct, Date, and Range-based Scanning with Size-weighting
	for _, c := range candidates {
		if checkExtra(c.Path) {
			continue
		}

		matched := false
		lowerPath := strings.ToLower(c.Path)

		// Check absolute air date match first for daily/long-running shows
		if len(parts) == 3 {
			for _, perm := range permutations {
				if strings.Contains(lowerPath, perm) {
					matched = true
					break
				}
			}
		}

		if !matched {
			cleanPath := normalizeEpisodePatterns(c.Path)
			info := ParseFilePath(cleanPath, fallbackSeason)

			// Check standard parsing match
			if info.Season == targetSeason && info.Episode == targetEpisode {
				matched = true
			}

			// Absolute Episode Bypass for Anime: If the show is animated and the file matches the absolute episode number exactly,
			// we can bypass the strict season match, provided the file does not reside in an explicitly different season folder.
			if !matched && isAnimation && info.Episode == targetEpisode {
				// Ensure it doesn't belong to a different season folder
				matches := seasonFolderRegex.FindAllStringSubmatch(c.Path, -1)
				isDifferentSeason := false
				for _, match := range matches {
					if len(match) >= 2 {
						sNum, err := strconv.Atoi(match[1])
						if err == nil && sNum != targetSeason {
							isDifferentSeason = true
							break
						}
					}
				}
				if !isDifferentSeason {
					matched = true
				}
			}

			// Strict Standalone Numeric Check Fallback:
			// If standard parsing failed or returned episode = 0, but isAnimation is true,
			// check if the filename contains the targetEpisode as a standalone numeric token (e.g. "752.mp4" or "0752.mkv").
			if !matched && isAnimation && (info.Episode == 0 || info.Episode == targetEpisode) {
				if ExtractNumericEpisode(c.Path, targetEpisode) {
					matches := seasonFolderRegex.FindAllStringSubmatch(c.Path, -1)
					isDifferentSeason := false
					for _, match := range matches {
						if len(match) >= 2 {
							sNum, err := strconv.Atoi(match[1])
							if err == nil && sNum != targetSeason {
								isDifferentSeason = true
								break
							}
						}
					}
					if !isDifferentSeason {
						matched = true
					}
				}
			}

			// Check multi-episode parsed array by releasetitleparser (if available)
			parsedInfo := ParseFilePath(c.Path, fallbackSeason)
			if parsedInfo.Season == targetSeason && parsedInfo.Episode == targetEpisode {
				matched = true
			}

			if !matched && isAnimation && parsedInfo.Episode == targetEpisode {
				matches := seasonFolderRegex.FindAllStringSubmatch(c.Path, -1)
				isDifferentSeason := false
				for _, match := range matches {
					if len(match) >= 2 {
						sNum, err := strconv.Atoi(match[1])
						if err == nil && sNum != targetSeason {
							isDifferentSeason = true
							break
						}
					}
				}
				if !isDifferentSeason {
					matched = true
				}
			}

			if !matched && isAnimation && parsedInfo.Episode == 0 {
				if ExtractNumericEpisode(c.Path, targetEpisode) {
					matches := seasonFolderRegex.FindAllStringSubmatch(c.Path, -1)
					isDifferentSeason := false
					for _, match := range matches {
						if len(match) >= 2 {
							sNum, err := strconv.Atoi(match[1])
							if err == nil && sNum != targetSeason {
								isDifferentSeason = true
								break
							}
						}
					}
					if !isDifferentSeason {
						matched = true
					}
				}
			}

			// Check Range Regex (e.g. S01E21-22)
			if !matched && info.Season == targetSeason && MatchRange(c.Path, targetEpisode) {
				matched = true
			}
		}

		if matched {
			// Size-weighting check to prioritize actual episodes over samples/trailers
			if c.Size > maxWeight {
				bestCandidate = c
				maxWeight = c.Size
				found = true
			}
		}
	}

	if found {
		return bestCandidate, true
	}

	// 2. Index-Based Sequential Match Fallback (For absolute numbering in folder packs)
	var seasonMatches []CandidateFile
	for _, c := range candidates {
		if checkExtra(c.Path) {
			continue
		}

		// Ensure it doesn't belong to a different season folder
		matches := seasonFolderRegex.FindAllStringSubmatch(c.Path, -1)
		isDifferentSeason := false
		for _, match := range matches {
			if len(match) >= 2 {
				sNum, err := strconv.Atoi(match[1])
				if err == nil && sNum != targetSeason {
					isDifferentSeason = true
					break
				}
			}
		}
		if isDifferentSeason {
			continue
		}

		seasonMatches = append(seasonMatches, c)
	}

	if len(seasonMatches) > 0 {
		// Sort alphabetically by path to reconstruct original sequence
		sort.Slice(seasonMatches, func(i, j int) bool {
			return strings.Compare(strings.ToLower(seasonMatches[i].Path), strings.ToLower(seasonMatches[j].Path)) < 0
		})

		if targetEpisode > 0 && targetEpisode <= len(seasonMatches) {
			candidate := seasonMatches[targetEpisode-1]

			candParsed := ParseFilePath(candidate.Path, fallbackSeason)
			if candParsed.Episode != 0 && candParsed.Episode != targetEpisode {
				if !MatchRange(candidate.Path, targetEpisode) {
					return CandidateFile{}, false
				}
			}
			return candidate, true
		}
	}

	return CandidateFile{}, false
}

func FindBestSeriesFile(candidates []CandidateFile, targetSeason, targetEpisode, fallbackSeason int, isAnimation bool) (CandidateFile, bool) {
	return FindBestSeriesFileLongRunning(candidates, targetSeason, targetEpisode, fallbackSeason, "", isAnimation)
}

// HasExcludingRange checks if the torrent name explicitly declares a range that excludes our requested episode
func HasExcludingRange(name string, targetEpisode int) bool {
	if targetEpisode == 0 {
		return false
	}
	
	// Scan for standard range formats, e.g. 579-628, EP 579-628, E579-E628, episodes 579 to 628
	matches := rangeRegex.FindAllStringSubmatchIndex(name, -1)
	for _, match := range matches {
		if len(match) >= 6 {
			startNumStart := match[2]
			startNumEnd := match[3]
			endNumStart := match[4]
			endNumEnd := match[5]

			if startNumStart > 0 && isDecimalDot(name, startNumStart-1) {
				continue
			}
			if endNumEnd < len(name) && isDecimalDot(name, endNumEnd) {
				continue
			}

			start, err1 := strconv.Atoi(name[startNumStart:startNumEnd])
			end, err2 := strconv.Atoi(name[endNumStart:endNumEnd])
			if err1 == nil && err2 == nil {
				if start <= end {
					if targetEpisode < start || targetEpisode > end {
						return true
					}
				}
			}
		}
	}
	return false
}

// ExtractNumericEpisode attempts to find a standalone integer in the filename
// that matches the targetEpisode exactly, bypassing complex parser libraries for clean numeric streams.
func ExtractNumericEpisode(path string, targetEpisode int) bool {
	if targetEpisode == 0 {
		return false
	}
	
	// Get the base filename
	fileName := path
	if idx := strings.LastIndexAny(path, "/\\"); idx != -1 {
		fileName = path[idx+1:]
	}
	
	// Remove file extension
	if dotIdx := strings.LastIndex(fileName, "."); dotIdx != -1 {
		fileName = fileName[:dotIdx]
	}
	
	// Clean non-alphanumeric characters but preserve digit grouping
	cleaned := SanitizeName(fileName)
	
	// Split into space-separated fields and find if any token exactly matches the targetEpisode (as string)
	targetStr := strconv.Itoa(targetEpisode)
	targetStrPadded := fmt.Sprintf("%02d", targetEpisode)
	targetStrPadded3 := fmt.Sprintf("%03d", targetEpisode)
	targetStrPadded4 := fmt.Sprintf("%04d", targetEpisode)
	
	for _, token := range strings.Fields(cleaned) {
		if token == targetStr || token == targetStrPadded || token == targetStrPadded3 || token == targetStrPadded4 {
			return true
		}
	}
	return false
}
