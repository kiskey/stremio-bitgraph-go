package addon

import (
	"regexp"
	"strings"
)

type BadgeFilter struct {
	ID      string
	GroupID string
	Name    string
	Regex   *regexp.Regexp
}

// Low-Allocation pre-defined static filters mapped directly from badges.json
var filters = []struct {
	ID      string
	GroupID string
	Name    string
	Pattern string
}{
	// Quality
	{"q-r", "gq", "Remux", `(?i)\bremux\b`},
	{"q-b", "gq", "BluRay", `(?i)^(?=.*\b(?:blu[-_. ]?ray|b[rd][-_. ]?rip)\b)(?!.*\bremux\b)`},
	{"q-w", "gq", "WEB-DL", `(?i)\bweb[-_. ]?dl\b`},
	{"src-webrip", "gq", "WEBRip", `(?i)\bweb[-_. ]?rip\b`},
	{"src-hdtv", "gq", "HDTV", `(?i)\bhdtv\b`},
	{"src-hdrip", "gq", "HDRip", `(?i)\bhd[-_. ]?rip\b`},
	{"src-dvdrip", "gq", "DVDRip", `(?i)\bdvd[-_. ]?rip\b`},

	// Resolution
	{"r-4k", "gr", "4K", `(?i)^(?=.*(?:\b2160[pi]?\b|\b4k\b|\buhd\b))(?!.*(?:\b1080[pi]?\b|\b720[pi]?\b))`},
	{"r-1080", "gr", "1080p", `(?i)\b1080[pi]?\b`},
	{"r-720", "gr", "720p", `(?i)\b720[pi]?\b`},

	// Visual
	{"v-seadex", "gv", "SeaDex", `(?i)\b(?:seadex|best[\s._-]?release|alt[\s._-]?(?:best[\s._-]?)?release)\b|ᴀʟᴛ ʀᴇʟᴇᴀsᴇ|ʙᴇsᴛ ʀᴇʟᴇᴀsᴇ`},
	{"v-hdr10p", "gv", "HDR10+", `(?i)^(?!.*\b(?:dv|dovi|dolby[\s._-]?vision)\b)(?=.*\bhdr[\s._-]?10[\s._-]?(?:\+|plus|p)(?:\b|[^a-z0-9]))`},
	{"v-hdr10", "gv", "HDR10", `(?i)^(?!.*\b(?:dv|dovi|dolby[\s._-]?vision)\b)(?=.*\bhdr[\s._-]?10\b)(?!.*\bhdr[\s._-]?10[\s._-]?(?:\+|plus|p)(?:\b|[^a-z0-9]))`},
	{"v-hdr", "gv", "HDR", `(?i)^(?!.*\b(?:dv|dovi|dolby[\s._-]?vision)\b)(?=.*\bhdr\b)(?!.*\bhdr[\s._-]?10)`},
	{"v-sdr", "gv", "SDR", `(?i)^(?!.*\b(?:hdr|hdr10|hdr10\+|dv|dovi|dolby[\s._-]?vision)\b)(?=.*\bsdr\b)`},
	{"v-imax-e", "gv", "IMAX Enhanced", `(?i)\bimax[\s._-]?enhanced\b`},
	{"v-imax", "gv", "IMAX", `(?i)^(?=.*\bimax\b)(?!.*\benhanced\b)`},
	{"a-dv", "gv", "DV", `(?i)\b(?:dv|dovi|dolby[\s._-]?vision)\b`},

	// Audio
	{"a-dtsx", "ga", "DTS:X", `(?i)\bdts[-_.: ]?x\b`},
	{"a-dtsma", "ga", "DTS-HD MA", `(?i)^(?=.*\bdts[-_. ]?(?:hd[-_. ]?)?ma\b)(?!.*\bdts[-_.: ]?x\b)`},
	{"a-dtshd", "ga", "DTS-HD", `(?i)^(?=.*\bdts[-_. ]?hd\b)(?!.*\bdts[-_. ]?(?:hd[-_. ]?)?ma\b)(?!.*\bdts[-_.: ]?x\b)`},
	{"a-dts", "ga", "DTS", `(?i)^(?=.*\bdts\b)(?!.*\bdts[-_. ]?(?:hd|ma|xll|x)\b)`},
	{"a-at", "ga", "Atmos", `(?i)\batmos\b`},
	{"a-th", "ga", "TrueHD", `(?i)\btrue[\s._-]?hd\b`},
	{"a-dp", "ga", "DD+", `(?i)^(?=.*\b(?:ddp|dd\+|eac-?3|e-?ac-?3)\b)(?!.*\btrue[\s._-]?hd\b)`},
	{"a-dd", "ga", "DD", `(?i)^(?=.*\b(?:dd[25][. ][01]|ac-?3)\b)(?!.*\b(?:ddp|dd\+|eac-?3|e-?ac-?3)\b)(?!.*\batmos\b)(?!.*\btrue[\s._-]?hd\b)`},

	// Channels
	{"ch-71", "gc", "7.1", `(?i)(?:^|[^0-9])[7-8][. ][01](?![0-9])`},
	{"ch-51", "gc", "5.1", `(?i)^(?=.*(?:^|[^0-9])5[. ][01](?![0-9]))(?!.*(?:^|[^0-9])[7-8][. ][01](?![0-9]))`},

	// Streaming
	{"s-nflx", "gs", "NETFLIX", `(?i)\b(?:nflx|netflix)\b`},
	{"s-amzn", "gs", "PRIME VIDEO", `(?i)\b(?:amzn|amazon|prime[\s._-]?video)\b`},
	{"s-atvp", "gs", "APPLE TV+", `(?i)\b(?:atvp|apple[\s._-]?tv\+?|appletv)\b`},
	{"s-dsnp", "gs", "DISNEY+", `(?i)\b(?:dsnp|dsny|disney\+?|disney[\s._-]?plus)\b`},
	{"s-hmax", "gs", "HBO MAX", `(?i)(?:\b(?:hmax|hbomax|hbo[\s._-]?max)\b|(?:^|[\s._-])max(?:[\s._-]|$))`},
	{"s-hulu", "gs", "HULU", `(?i)\bhulu\b`},
	{"s-pcok", "gs", "PEACOCK", `(?i)\b(?:pcok|peacock)\b`},
	{"s-pamp", "gs", "PARAMOUNT+", `(?i)\b(?:pmtp|pamp|paramount\+?|paramount[\s._-]?plus)\b`},
	{"s-croll", "gs", "CRUNCHYROLL", `(?i)\b(?:crunchyroll|crunch)\b`},

	// Encoder
	{"s-h265", "ge", "H265 HEVC", `(?i)\b(?:x265|h[._-]?265|hevc)\b`},
	{"s-h264", "ge", "H264 AVC", `(?i)\b(?:x264|h[._-]?264|avc)\b`},
}

var CompiledFilters []BadgeFilter

func init() {
	CompiledFilters = make([]BadgeFilter, len(filters))
	for i, f := range filters {
		CompiledFilters[i] = BadgeFilter{
			ID:      f.ID,
			GroupID: f.GroupID,
			Name:    f.Name,
			Regex:   regexp.MustCompile(f.Pattern),
		}
	}
}

// FormatBadges scans the source filename exactly once and extracts matched tags.
// Results are grouped in priority layout: Resolution -> Quality -> Visual -> Audio -> Channels -> Encoder -> Streaming
func FormatBadges(title string) string {
	var res, qual, vis, aud, ch, enc, str string

	for i := range CompiledFilters {
		f := &CompiledFilters[i]
		if f.Regex.MatchString(title) {
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
