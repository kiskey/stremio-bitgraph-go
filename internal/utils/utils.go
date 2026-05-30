package utils

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"
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
	
	// Remove CJK brackets
	re := regexp.MustCompile(`【.*?】`)
	s = re.ReplaceAllString(s, " ")

	// Remove non-ASCII scripts
	re = regexp.MustCompile(`[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}\p{Script=Arabic}\p{Script=Cyrillic}\p{Script=Thai}]+`)
	s = re.ReplaceAllString(s, " ")

	// Remove bracketed content with non-word chars
	re = regexp.MustCompile(`\[.*?[^\w\s-].*?\]`)
	s = re.ReplaceAllString(s, " ")

	// Remove URLs
	re = regexp.MustCompile(`\b(https?://\S+|www\.\S+\.\w+|[\w.-]+@[\w.-]+)\b`)
	s = re.ReplaceAllString(s, " ")

	// Remove dashes at boundaries
	re = regexp.MustCompile(`^\s*[-–—]+\s*|\s*[-–—]+\s*$`)
	s = re.ReplaceAllString(s, " ")
	
	re = regexp.MustCompile(`\s+[-–—]+\s+`)
	s = re.ReplaceAllString(s, " ")

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
