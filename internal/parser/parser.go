package parser

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
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

var languageToISO = map[rtp.Language]string{
	rtp.LanguageEnglish:       "en",
	rtp.LanguageSpanish:       "es",
	rtp.LanguageGerman:        "de",
	rtp.LanguageFrench:        "fr",
	rtp.LanguageItalian:       "it",
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

var rangeRegex = regexp.MustCompile(`(?i)\b(?:e|ep|episode)?\s*(\d+)\s*(?:[\-x]|to)\s*(?:e|ep|episode)?\s*(\d+)\b`)
var seasonFolderRegex = regexp.MustCompile(`(?i)\b(?:s|season|series)\s*0*(\d+)\b`)

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
	s := name

	// 1. Replace non-breaking spaces (\u00a0, \u200b) to standard spaces
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\u200b", " ")

	// 2. Normalize episode patterns (e.g. S02 EP(15) -> S02E15)
	s = normalizeEpisodePatterns(s)

	// 3. Remove non-ASCII scripts (Chinese, Cyrillic, Japanese, etc.)
	var b strings.Builder
	for _, r := range s {
		if r > unicode.MaxASCII {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	s = b.String()

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

func RobustParseInfo(title string, fallbackSeason int) *ParseResult {
	clean := SanitizeName(title)

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
		return &ParseResult{
			Title:    info.SeriesTitle,
			Season:   info.SeasonNumber,
			Episode:  episode,
			Year:     info.SeriesTitleInfo.Year,
			Language: lang,
			Quality:  getQuality(info.Quality.Quality.Resolution),
			IsPack:   IsPack(info),
		}
	}

	movie := rtp.ParseMovieTitle(clean)
	if movie != nil {
		lang := "en"
		if len(movie.Languages) > 0 {
			lang = getISO(movie.Languages[0])
		}
		return &ParseResult{
			Title:    movie.PrimaryMovieTitle(),
			Season:   0,
			Episode:  0,
			Year:     movie.Year,
			Language: lang,
			Quality:  getQuality(movie.Quality.Quality.Resolution),
		}
	}

	return &ParseResult{
		Title:    clean,
		Season:   fallbackSeason,
		Episode:  0,
		Language: "en",
		Quality:  "sd",
	}
}

func ParseFilePath(path string, fallbackSeason int) *ParseResult {
	cleanPath := normalizeEpisodePatterns(path)
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

func matchRange(path string, targetEpisode int) bool {
	matches := rangeRegex.FindAllStringSubmatch(path, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			start, err1 := strconv.Atoi(match[1])
			end, err2 := strconv.Atoi(match[2])
			if err1 == nil && err2 == nil {
				if start <= end && targetEpisode >= start && targetEpisode <= end {
					return true
				}
			}
		}
	}
	return false
}

func FindBestSeriesFile(candidates []CandidateFile, targetSeason, targetEpisode, fallbackSeason int) (CandidateFile, bool) {
	var bestCandidate CandidateFile
	var found bool
	var maxWeight int64 = -1

	// 1. Direct and Range-based Scanning with Size-weighting
	for _, c := range candidates {
		if isExtraOrSpecial(c.Path) {
			continue
		}

		cleanPath := normalizeEpisodePatterns(c.Path)
		info := ParseFilePath(cleanPath, fallbackSeason)

		matched := false
		// Check standard parsing match
		if info.Season == targetSeason && info.Episode == targetEpisode {
			matched = true
		}

		// Check multi-episode parsed array by releasetitleparser (if available)
		parsedInfo := ParseFilePath(c.Path, fallbackSeason)
		if parsedInfo.Season == targetSeason && parsedInfo.Episode == targetEpisode {
			matched = true
		}

		// Check Range Regex (e.g. S01E21-22)
		if !matched && info.Season == targetSeason && matchRange(c.Path, targetEpisode) {
			matched = true
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
		if isExtraOrSpecial(c.Path) {
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
			return seasonMatches[targetEpisode-1], true
		}
	}

	return CandidateFile{}, false
}
