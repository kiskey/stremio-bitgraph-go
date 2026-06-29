package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

var (
	Port                int
	APIPort             int
	AppHost             string
	LogLevel            string
	AddonID             string
	AddonName           string
	AddonVersion        string
	RealDebridAPIKey    string
	TmdbAPIKey          string
	BitmagnetGQLEndpoint string
	DatabaseURL         string
	OmdbAPIKey          string
	TraktClientID       string
	PreferredLanguages  []string
	SimilarityThreshold float64
	StrictLanguageFilter bool
	StreamLimitPerQuality int
	DebridService       string
	TorboxAPIKey        string
	TorboxEnabled       bool
	TorboxMaxActiveTorrents int
	RealDebridEnabled   bool
	DebridProvider      string
	DebridCacheTable    string
)

func init() {
	Port, _ = strconv.Atoi(os.Getenv("PORT"))
	if Port == 0 {
		Port = 7000
	}
	APIPort = Port + 1
	AppHost = os.Getenv("APP_HOST")
	if AppHost == "" {
		AppHost = fmt.Sprintf("http://127.0.0.1:%d", Port)
	}
	LogLevel = os.Getenv("LOG_LEVEL")
	if LogLevel == "" {
		LogLevel = "info"
	}

	AddonID = "org.stremio.go.bitmagnet"
	AddonName = "GoMagnet"
	AddonVersion = "7.3.6" // Graded positive curation engine for CAM/TS/HQ-HDRip

	RealDebridAPIKey = os.Getenv("REALDEBRID_API_KEY")
	TmdbAPIKey = os.Getenv("TMDB_API_KEY")
	BitmagnetGQLEndpoint = os.Getenv("BITMAGNET_GRAPHQL_ENDPOINT")
	DatabaseURL = os.Getenv("DATABASE_URL")
	OmdbAPIKey = os.Getenv("OMDB_API_KEY")
	TraktClientID = os.Getenv("TRAKT_CLIENT_ID")

	langs := os.Getenv("PREFERRED_LANGUAGES")
	if langs != "" {
		for _, l := range strings.Split(langs, ",") {
			PreferredLanguages = append(PreferredLanguages, strings.TrimSpace(l))
		}
	}

	SimilarityThreshold, _ = strconv.ParseFloat(os.Getenv("SIMILARITY_THRESHOLD"), 64)
	if SimilarityThreshold == 0 {
		SimilarityThreshold = 0.75
	}
	StrictLanguageFilter = os.Getenv("STRICT_LANGUAGE_FILTER") == "true"
	StreamLimitPerQuality, _ = strconv.Atoi(os.Getenv("STREAM_LIMIT_PER_QUALITY"))
	if StreamLimitPerQuality == 0 {
		StreamLimitPerQuality = 2
	}

	DebridService = strings.ToLower(os.Getenv("DEBRID_SERVICE"))
	TorboxAPIKey = os.Getenv("TORBOX_API_KEY")
	TorboxEnabled = TorboxAPIKey != ""
	TorboxMaxActiveTorrents, _ = strconv.Atoi(os.Getenv("TORBOX_MAX_ACTIVE_TORRENTS"))
	RealDebridEnabled = RealDebridAPIKey != ""
	DebridCacheTable = os.Getenv("DEBRID_CACHE_TABLE")
	if DebridCacheTable == "" {
		DebridCacheTable = "debrid_cache"
	}

	if DebridService == "" {
		if RealDebridEnabled {
			DebridService = "realdebrid"
		} else if TorboxEnabled {
			DebridService = "torbox"
		}
	}
	DebridProvider = DebridService

	var missing []string
	if TmdbAPIKey == "" {
		missing = append(missing, "TMDB_API_KEY")
	}
	if BitmagnetGQLEndpoint == "" {
		missing = append(missing, "BITMAGNET_GRAPHQL_ENDPOINT")
	}
	if len(missing) > 0 {
		panic(fmt.Sprintf("Missing critical environment variables: %s", strings.Join(missing, ", ")))
	}
}
