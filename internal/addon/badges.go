package addon

import (
	"regexp"
	"strings"
)

type BadgeFilter struct {
	ID        string
	GroupID   string
	Name      string
	Positive  *regexp.Regexp
	Negatives []*regexp.Regexp
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
	{"v-seadex", "gv", "SeaDex", `(?i)\b(?:seadex|best[\s._-]?release|alt[\s._-]?(?:best[\s._-]?release)?)\b|ᴀʟᴛ ʀᴇʟᴇᴀsᴇ|ʙᴇsᴛ ʀᴇʟᴇᴀsᴇ`, nil},
	{"v-hdr10p", "gv", "HDR10+", `(?i)\bhdr[\s._-]?10[\s._-]?(?:\+|plus|p)(?:\b|[^a-z0-9]|$)`, []string{`(?i)\b(?:dv|dovi|dolby[\s._-]?vision)\b`}},
	{"v-hdr10", "gv", "HDR10", `(?i)\bhdr[\s._-]?10\b`, []string{`(?i)\b(?:dv|dovi|dolby[\s._-]?vision)\b`, `(?i)\bhdr[\s._-]?10[\s._-]?(?:\+|plus|p)(?:\b|[^a-z0-9]|$)`}},
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
	{"ch-71", "gc", "7.1", `(?i)(?:^|[^0-9])[7-8][. ][01](?:[^0-9]|$)`, nil},
	{"ch-51", "gc", "5.1", `(?i)(?:^|[^0-9])5[. ][01](?:[^0-9]|$)\b`, []string{`(?i)(?:^|[^0-9])[7-8][. ][01](?:[^0-9]|$)`}},

	// Streaming
	{"s-nflx", "gs", "NETFLIX", `(?i)\b(?:nflx|netflix|nf)\b`, nil}, // Updated to fully support .NF. abbreviations
	{"s-amzn", "gs", "PRIME VIDEO", `(?i)\b(?:amzn|amazon|prime[\s._-]?video)\b`, nil},
	{"s-atvp", "gs", "APPLE TV+", `(?i)\b(?:atvp|apple[\s._-]?tv\+?|appletv)\b`, nil},
	{"s-dsnp", "gs", "DISNEY+", `(?i)\b(?:dsnp|dsny|disney\+?|disney[\s._-]?plus)\b`, nil},
	{"s-hmax", "gs", "HBO MAX", `(?i)(?:\b(?:hmax|hbomax|hbo[\s._-]?max)\b|(?:^|[\s._-])max(?:[\s._-]|$))`, nil},
	{"s-hulu", "gs", "HULU", `(?i)\bhulu\b`, nil},
	{"s-pcok", "gs", "PEACOCK", `(?i)\b(?:pcok|peacock)\b`, nil},
	{"s-pamp", "gs", "PARAMOUNT+", `(?i)\b(?:pmtp|pamp|paramount\+?|paramount[\s._-]?plus)\b`, nil},
	{"s-croll", "gs", "CRUNCHYROLL", `(?i)\b(?:crunchyroll|crunch)\b`, nil},

	// Encoder
	{"s-h265", "ge", "H265 HEVC", `(?i)\b(?:x265|h[._-]?265|hevc)\b`, nil},
	{"s-h264", "ge", "H264 AVC", `(?i)\b(?:x264|h[._-]?264|avc)\b`, nil},
}

var CompiledFilters []BadgeFilter

func init() {
	CompiledFilters = make([]BadgeFilter, len(filtersDef))
	for i, f := range filtersDef {
		var negatives []*regexp.Regexp
		for _, negPat := range f.Negatives {
			negatives = append(negatives, regexp.MustCompile(negPat))
		}

		CompiledFilters[i] = BadgeFilter{
			ID:        f.ID,
			GroupID:   f.GroupID,
			Name:      f.Name,
			Positive:  regexp.MustCompile(f.Positive),
			Negatives: negatives,
		}
	}
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
