package matcher

import (
	"math"
	"regexp"
	"strings"
	"time"
)

type AnimePriorMeta struct {
	OriginalLanguage   string
	OriginCountries    []string
	IsAnimation        bool
	SeasonEpisodeCount int
}

var (
	// Exhaustive Anime Release Group Regex
	animeGroupRe = regexp.MustCompile(`(?i)\b(?:SubsPlease|Erai[-_]?raws|HorribleSubs|ASW|Judah|Judas|Ember|Vostfr|Yamez|AnimXT|Kawaiika[-_]?Raws|Shokorefa|Fumetsu|Nanatsu|PAS|PnPSubs|SeaDex|Cleo|Anime[-_]?Time|BlueLaguna|KUC[\s._]?NG|op[\s._]?tube|Shin[\s._]?Sekai|CEBRAY|SiGLA|ACEM|Kitsune|DarQ|nyaa|BakedFish|SpaceFish|AnimeRG|RH|NoobSubs)\b`)
	
	// Explicit Anime Audio/Language Tokens
	animeLangRe = regexp.MustCompile(`(?i)\b(?:vostfr|vost|eng[\s._-]?sub|multi[\s._-]?audio|dual[\s._-]?audio|jpn[\s._-]?subs|japanese[\s._-]?sub|\.sub\.|subbed|eng[\s._-]?softsub|vostfr[\s._-]?hd|multi[\s._-]?vf2|castellano|german[\s._-]?sub|ger[\s._-]?sub)\b`)
	
	// Standard Anime CRC32 Hex Hash
	animeCrcHashRe = regexp.MustCompile(`(?i)\[[0-9a-fA-F]{8}\]`)

	// Leading Group Bracket Prefix
	animeBracketPrefixRe = regexp.MustCompile(`(?i)^\s*[\[【][a-zA-Z0-9_.-]+[\]】]`)
	
	// Standard Western Release Group Regex
	westernGroupRe = regexp.MustCompile(`(?i)\b(?:RARBG|NTb|FLUX|CMRG|PHoMo|DLAA|AJP69|KiNGS|GLHF|r00t|TEPES|ROCCaT|EZTV|aXXo|TOMMY|BAE|NOSiViD|BiNGE|SYNCOPY|EDITH|MeGusta|WADU|LoRD|D3G|RBB|PortalGoods|PSA|FWB|FLAME|SAUERKRAUT|higgsboson|ntropic|QxR|Tigole|GalaxyTV|TARDiS)\b`)
	
	// Anime Streaming Platform Indicators
	animeSourceRe = regexp.MustCompile(`(?i)\b(?:CR|Crunchyroll|Bilibili|BILI|iQiyi|MuseAsia|AniOne|FuniRip|CR-Rip|CrunchyRip)\b`)

	// Western Streaming Platform Indicators
	westernSourceRe = regexp.MustCompile(`(?i)\b(?:NF|Netflix|NFLX|AMZN|ATVP|DSNP|HMAX|PCOK|PMTP|HULU|STAN|STANAU|SHO|TUBI|BCORE|AppleTV|Hulu|Amazon)\b`)
	
	// Live-Action Indicators
	liveActionMarkerRe = regexp.MustCompile(`(?i)\b(?:live[\s._-]?action|LA[\s._-]|netflix[\s._-]?series)\b`)
)

func isAnimeRelease(filename string) bool {
	lower := strings.ToLower(filename)
	trimmed := strings.TrimSpace(filename)
	
	if animeGroupRe.MatchString(filename) || animeCrcHashRe.MatchString(filename) || animeLangRe.MatchString(filename) || animeBracketPrefixRe.MatchString(trimmed) {
		return true
	}
	if strings.Contains(lower, "op tube") || strings.Contains(lower, "shin sekai") {
		return true
	}
	return false
}

func isNewerShowDisqualified(fileTs int64, premiereYear int) bool {
	if fileTs == 0 || premiereYear <= 1970 {
		return false
	}
	// Disqualify if posted more than 6 months (15552000s) before premiere year start
	premiereTs := time.Date(premiereYear, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	return fileTs < (premiereTs - 15552000)
}

func ClassifyTargetPrior(meta AnimePriorMeta) float64 {
	// Safeguard 1: If the show is NOT animated, it cannot be anime.
	// Force the score below the threshold of 3.0 so that the Anime Shield is never triggered for live action.
	if !meta.IsAnimation {
		return -10.0 // Strictly live-action, never triggers anime-release expectations
	}

	var score float64 = 0.0
	lang := strings.ToLower(meta.OriginalLanguage)

	switch lang {
	case "ja":
		score += 10.0 // Strengthen Japanese language baseline
	case "en":
		score -= 10.0 // Strongly penalize English language baseline for animation
	case "zh":
		score += 5.0
	case "ko":
		score += 3.0
	}

	// Double-down on origin country to separate Western Animation from Japanese Anime
	isEasternAsia := false
	for _, c := range meta.OriginCountries {
		if c == "JP" {
			score += 10.0
			isEasternAsia = true
		} else if c == "CN" || c == "TW" || c == "KR" {
			score += 5.0
			isEasternAsia = true
		}
	}

	// Safeguard 2: If it is animated, but original language is English and origin is not Eastern Asia,
	// it is structurally a Western cartoon (e.g., South Park, Rick and Morty, The Simpsons).
	if lang == "en" && !isEasternAsia {
		return -5.0
	}

	return score
}

func isOfficialLicensedRetailRelease(filename string) bool {
	// Leverage the pre-defined westernSourceRe platform regex to identify major licensed streaming sources
	if westernSourceRe.MatchString(filename) {
		return true
	}
	// Handle non-rigid digital platform variations frequently found in Usenet indexers or torrent filenames
	lower := strings.ToLower(filename)
	return strings.Contains(lower, "disneyplus") ||
		strings.Contains(lower, "disney+") ||
		strings.Contains(lower, "apple.tv") ||
		strings.Contains(lower, "appletv") ||
		strings.Contains(lower, "primevideo") ||
		strings.Contains(lower, "prime.video") ||
		strings.Contains(lower, "hbomax") ||
		strings.Contains(lower, "crunchyroll") ||
		strings.Contains(lower, "funimation") ||
		strings.Contains(lower, "hidive") ||
		strings.Contains(lower, "bilibili") ||
		strings.Contains(lower, "bili.bili") ||
		strings.Contains(lower, "muse.asia") ||
		strings.Contains(lower, "ani-one") ||
		strings.Contains(lower, "ani.one")
}

func ComputeCandidateScore(filename string) float64 {
	var score float64 = 0.0
	trimmed := strings.TrimSpace(filename)

	if animeCrcHashRe.MatchString(filename) {
		score += 12.0
	}
	if animeBracketPrefixRe.MatchString(trimmed) {
		// Demoted from 5.0 to 1.0 to prevent false-positives on bracketed standard Western cartoon releases (Sonarr/Radarr Parity)
		score += 1.0
	}
	if animeGroupRe.MatchString(filename) {
		score += 6.0
	}
	if westernGroupRe.MatchString(filename) {
		score -= 5.0
	}
	if animeSourceRe.MatchString(filename) {
		score += 5.0
	}
	if westernSourceRe.MatchString(filename) {
		score -= 4.0
	}
	if animeLangRe.MatchString(filename) {
		score += 3.0
	}
	if liveActionMarkerRe.MatchString(filename) {
		score -= 5.0
	}

	return score
}

func EvaluateAnimeShield(filename string, prior AnimePriorMeta) bool {
	targetPrior := ClassifyTargetPrior(prior)
	if math.Abs(targetPrior) < 3.0 {
		return true // Not confident enough to filter
	}

	candScore := ComputeCandidateScore(filename)
	if targetPrior > 3.0 && candScore < -3.0 {
		// Bypass Rejection: If this is an official licensed retail/digital distribution,
		// we expect standard Western-style scene group formatting (such as WEB-DL-NF or AMZN).
		// Therefore, we allow it to pass through cleanly.
		if isOfficialLicensedRetailRelease(filename) {
			return true
		}
		return false // Reject: Expected anime but got western live action
	}
	if targetPrior < -3.0 && candScore > 4.0 {
		return false // Reject: Expected live action but got anime release
	}

	return true
}

func parsePublishedAt(pubStr string) int64 {
	if t, err := time.Parse(time.RFC3339, pubStr); err == nil {
		return t.Unix()
	}
	return 0
}
