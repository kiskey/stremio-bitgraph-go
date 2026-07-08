package utils

import (
	"context" // Added to support DialContext signatures
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var Logger *slog.Logger
var LogLevelVar = new(slog.LevelVar)
var LogLevel string

func init() {
	LogLevel = os.Getenv("LOG_LEVEL")
	if LogLevel == "" {
		LogLevel = "info"
	}

	SetLogLevel(LogLevel)

	Logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: LogLevelVar,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{Key: slog.TimeKey, Value: slog.StringValue(a.Value.Time().Format("2006-01-02 15:04:05"))}
			}
			return a
		},
	}))
}

func SetLogLevel(level string) {
	switch strings.ToLower(level) {
	case "debug":
		LogLevelVar.Set(slog.LevelDebug)
	case "warn":
		LogLevelVar.Set(slog.LevelWarn)
	case "error":
		LogLevelVar.Set(slog.LevelError)
	default:
		LogLevelVar.Set(slog.LevelInfo)
	}
}

// NewOptimizedClient creates an HTTP client with high connection reuse, HTTP2, and forced IPv4 loop resolution
func NewOptimizedClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// Force IPv4 (tcp4) to completely bypass IPv6 DNS/handshake hangs on unprivileged LXC/Docker/Wireguard networks
				return (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext(ctx, "tcp4", addr)
			},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}
}

func Sleep(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

func FormatSize(bytes int64) string {
	if bytes == 0 {
		return "N/A"
	}
	gb := float64(bytes) / 1e9
	return fmt.Sprintf("%.2f GB", gb)
}

var epPatternRegex = regexp.MustCompile(`(?i)(S\d+)?[\s\-_]*\bEP[\s\-_]*[\(\[]?\s*(\d+)\s*[\)\]]?\b`)
var urlRegex = regexp.MustCompile(`\b(https?://\S+|www\.\S+\.\w+|[\w.-]+@[\w.-]+)\b`)
var bracketRegex = regexp.MustCompile(`\[.*?[^\w\s-].*?\]`)

// Match common decimal channel audio configurations (e.g. 5.1, 7.1, 2.0) to prevent TV show misclassifications
var audioChannelsRegex = regexp.MustCompile(`(?i)\b([1-9])\.([0-9])\b`)

// Match standalone resolution numbers without trailing 'p' (e.g. 1080, 720, 2160) to prevent S10E80 parsing splits
var resolutionNoPRegex = regexp.MustCompile(`\b(2160|1080|720|480|360)\b`)

func SanitizeName(name string) string {
	s := name

	// Normalize standalone resolutions to include 'p' (e.g., 1080 -> 1080p) to prevent TV parser misclassifying them as S10E80
	s = resolutionNoPRegex.ReplaceAllString(s, "${1}p")

	// Replace audio channels like 5.1, 7.1, 2.0 with 5ch, 7ch, 2ch to prevent dot replacement from tokenizing them as series season/episode numbers (e.g. 5 1)
	s = audioChannelsRegex.ReplaceAllString(s, "${1}ch")

	// 1. Normalize special unicode spaces (e.g. \u00a0, \u200b) to standard spaces
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\u200b", " ")

	// 2. Collapse spacing on custom EP representations
	s = epPatternRegex.ReplaceAllString(s, "${1}E${2}")

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

// Extended Quality Sorting Stack containing positive low-quality demoted priority indices
var QualityOrder = map[string]int{
	"4k":       1,
	"2160p":    1,
	"1080p":    2,
	"720p":     3,
	"480p":     4,
	"360p":     5,
	"sd":       6,
	"scr":      7,  // Screener Quality
	"tc":       8,  // Telecine Quality
	"ts":       9,  // Telesync Quality
	"cam":      10, // Camrip Quality
	"wp":       11, // Workprint Quality
	"regional": 12, // Regional R5/R6 Line Audio Release Quality
}

func GetQuality(resolution string) string {
	if resolution == "" {
		return "sd"
	}
	res := strings.ToLower(resolution)
	if strings.Contains(res, "2160") || strings.Contains(res, "4k") {
		return "4k"
	}
	if strings.Contains(res, "1080") {
		return "1080p"
	}
	if strings.Contains(res, "720") {
		return "720p"
	}
	if strings.Contains(res, "480") {
		return "480p"
	}
	if strings.Contains(res, "360") {
		return "360p"
	}
	return "sd"
}
