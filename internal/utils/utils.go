package utils

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var Logger *slog.Logger

func init() {
	lvl := slog.LevelInfo
	switch strings.ToLower(LogLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	Logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{Key: slog.TimeKey, Value: slog.StringValue(a.Value.Time().Format("2006-01-02 15:04:05"))}
			}
			return a
		},
	}))
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

func SanitizeName(name string) string {
	s := name
	// Remove CJK, Arabic, Cyrillic, Thai, Hangul, Hiragana, Katakana
	re := regexp.MustCompile(`[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}\p{Script=Arabic}\p{Script=Cyrillic}\p{Script=Thai}]+`)
	s = re.ReplaceAllString(s, " ")
	// Remove bracketed content with non-word chars
	s = regexp.MustCompile(`\[.*?[^\w\s-].*?\]`).ReplaceAllString(s, " ")
	// Remove URLs
	s = regexp.MustCompile(`\b(https?://\S+|www\.\S+\.\w+|[\w.-]+@[\w.-]+)\b`).ReplaceAllString(s, " ")
	// Remove dashes at boundaries
	s = regexp.MustCompile(`^\s*[-–—]+\s*|\s*[-–—]+\s*$`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`\s+[-–—]+\s+`).ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

var QualityOrder = map[string]int{
	"4k":    1,
	"2160p": 1,
	"1080p": 2,
	"720p":  3,
	"480p":  4,
	"360p":  5,
	"sd":    6,
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

// LogLevel is imported from config in the real build; here we read directly for utils init
var LogLevel = func() string {
	l := os.Getenv("LOG_LEVEL")
	if l == "" {
		return "info"
	}
	return l
}()
