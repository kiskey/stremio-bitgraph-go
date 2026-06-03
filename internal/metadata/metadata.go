package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/utils"
	"golang.org/x/sync/errgroup"
)

var (
	tmdbClient     = utils.NewOptimizedClient(8 * time.Second)
	cinemetaClient = utils.NewOptimizedClient(8 * time.Second)
	omdbClient     *http.Client
	traktClient    *http.Client
	metaCache      = utils.NewTTLCache(12 * time.Hour)
	yearRegexp     = regexp.MustCompile(`\d{4}`)
)

func init() {
	if config.OmdbAPIKey != "" {
		omdbClient = utils.NewOptimizedClient(8 * time.Second)
	}
	if config.TraktClientID != "" {
		traktClient = utils.NewOptimizedClient(8 * time.Second)
	}
}

type MetaResult struct {
	Name      string
	Year      string
	Source    string
	AltTitles []string
}

type tmdbFindResponse struct {
	MovieResults []struct {
		Title         string `json:"title"`
		ReleaseDate   string `json:"release_date"`
		OriginalTitle string `json:"original_title"`
	} `json:"movie_results"`
	TvResults []struct {
		Name         string `json:"name"`
		FirstAirDate string `json:"first_air_date"`
		OriginalName string `json:"original_name"`
	} `json:"tv_results"`
}

type cinemetaMetaResponse struct {
	Meta struct {
		Name          string   `json:"name"`
		Year          string   `json:"year"`
		ReleaseInfo   string   `json:"releaseInfo"`
		OriginalTitle string   `json:"original_title"`
		Aka           []string `json:"aka"`
	} `json:"meta"`
}

// executeWithRetry wraps API calls to survive transient network timeouts or deadline misses
func executeWithRetry(ctx context.Context, fn func() (*MetaResult, error)) (*MetaResult, error) {
	res, err := fn()
	if err != nil && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timeout")) {
		// Attempt a single fast retry with a fresh context to avoid parent deadline exhaustion
		retryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return fn()
	}
	return res, err
}

func fetchTmdb(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	return executeWithRetry(ctx, func() (*MetaResult, error) {
		url := fmt.Sprintf("https://api.themoviedb.org/3/find/%s?api_key=%s&external_source=imdb_id", imdbID, config.TmdbAPIKey)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := tmdbClient.Do(req)
		if err != nil { return nil, err }
		defer resp.Body.Close()
		if resp.StatusCode != 200 { return nil, fmt.Errorf("tmdb status %d", resp.StatusCode) }

		var data tmdbFindResponse
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil { return nil, err }

		name, year := "", ""
		var altTitles []string
		if typ == "series" {
			if len(data.TvResults) == 0 { return nil, fmt.Errorf("not found") }
			item := data.TvResults[0]
			name = item.Name
			if len(item.FirstAirDate) >= 4 { year = item.FirstAirDate[:4] }
			if item.OriginalName != "" && item.OriginalName != name { altTitles = append(altTitles, item.OriginalName) }
		} else {
			if len(data.MovieResults) == 0 { return nil, fmt.Errorf("not found") }
			item := data.MovieResults[0]
			name = item.Title
			if len(item.ReleaseDate) >= 4 { year = item.ReleaseDate[:4] }
			if item.OriginalTitle != "" && item.OriginalTitle != name { altTitles = append(altTitles, item.OriginalTitle) }
		}
		return &MetaResult{Name: name, Year: year, Source: "TMDB", AltTitles: altTitles}, nil
	})
}

func fetchCinemeta(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	return executeWithRetry(ctx, func() (*MetaResult, error) {
		url := fmt.Sprintf("https://v3-cinemeta.strem.io/meta/%s/%s.json", typ, imdbID)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := cinemetaClient.Do(req)
		if err != nil { return nil, err }
		defer resp.Body.Close()
		if resp.StatusCode != 200 { return nil, fmt.Errorf("cinemeta status %d", resp.StatusCode) }

		var data cinemetaMetaResponse
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil { return nil, err }
		if data.Meta.Name == "" { return nil, fmt.Errorf("not found") }

		yearStr := data.Meta.Year
		if yearStr == "" { yearStr = data.Meta.ReleaseInfo }
		match := yearRegexp.FindString(yearStr)

		var altTitles []string
		if data.Meta.OriginalTitle != "" && data.Meta.OriginalTitle != data.Meta.Name {
			altTitles = append(altTitles, data.Meta.OriginalTitle)
		}
		for _, aka := range data.Meta.Aka {
			if aka != "" && aka != data.Meta.Name {
				altTitles = append(altTitles, aka)
			}
		}
		return &MetaResult{Name: data.Meta.Name, Year: match, Source: "Cinemeta", AltTitles: altTitles}, nil
	})
}

func fetchOmdb(ctx context.Context, imdbID string) (*MetaResult, error) {
	if omdbClient == nil { return nil, fmt.Errorf("omdb not configured") }
	return executeWithRetry(ctx, func() (*MetaResult, error) {
		url := fmt.Sprintf("http://www.omdbapi.com/?apikey=%s&i=%s", config.OmdbAPIKey, imdbID)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := omdbClient.Do(req)
		if err != nil { return nil, err }
		defer resp.Body.Close()
		var data struct {
			Response string `json:"Response"`
			Title    string `json:"Title"`
			Year     string `json:"Year"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil { return nil, err }
		if data.Response == "False" { return nil, fmt.Errorf("omdb not found") }
		year := data.Year
		if year != "" { year = strings.Split(year, "–")[0] }
		return &MetaResult{Name: data.Title, Year: year, Source: "OMDb", AltTitles: nil}, nil
	})
}

func fetchTrakt(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	if traktClient == nil { return nil, fmt.Errorf("trakt not configured") }
	return executeWithRetry(ctx, func() (*MetaResult, error) {
		searchType := "show"
		if typ != "series" { searchType = "movie" }
		url := fmt.Sprintf("https://api.trakt.tv/search/imdb/%s?type=%s", imdbID, searchType)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("trakt-api-version", "2")
		req.Header.Set("trakt-api-key", config.TraktClientID)
		resp, err := traktClient.Do(req)
		if err != nil { return nil, err }
		defer resp.Body.Close()
		if resp.StatusCode != 200 { return nil, fmt.Errorf("trakt status %d", resp.StatusCode) }
		var data []struct {
			Movie map[string]interface{} `json:"movie"`
			Show  map[string]interface{} `json:"show"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil { return nil, err }
		if len(data) == 0 { return nil, fmt.Errorf("not found") }
		item := data[0].Movie
		if typ == "series" { item = data[0].Show }
		name, _ := item["title"].(string)
		var year string
		if y, ok := item["year"].(float64); ok { year = fmt.Sprintf("%.0f", y) }
		return &MetaResult{Name: name, Year: year, Source: "Trakt", AltTitles: nil}, nil
	})
}

func GetMetaDetails(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	cacheKey := imdbID + "_" + typ
	if v, ok := metaCache.Get(cacheKey); ok {
		return v.(*MetaResult), nil
	}

	utils.Logger.Info("resolving metadata", "imdb", imdbID, "type", typ)

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	tmdbChan := make(chan *MetaResult, 1)
	cinemetaChan := make(chan *MetaResult, 1)

	go func() { tmdbChan <- func() *MetaResult { r, _ := fetchTmdb(raceCtx, imdbID, typ); return r }() }()
	go func() { cinemetaChan <- func() *MetaResult { r, _ := fetchCinemeta(raceCtx, imdbID, typ); return r }() }()

	var tmdbRes, cinemetaRes *MetaResult

	select {
	case tmdbRes = <-tmdbChan:
		if tmdbRes != nil {
			cancel()
			utils.Logger.Debug("resolved via TMDB (early-exit)", "name", tmdbRes.Name)
			metaCache.Set(cacheKey, tmdbRes)
			return tmdbRes, nil
		}
		cinemetaRes = <-cinemetaChan
		if cinemetaRes != nil {
			metaCache.Set(cacheKey, cinemetaRes)
			return cinemetaRes, nil
		}
	case cinemetaRes = <-cinemetaChan:
		if cinemetaRes != nil {
			cancel()
			utils.Logger.Info("resolved via Cinemeta (early-exit)", "name", cinemetaRes.Name)
			metaCache.Set(cacheKey, cinemetaRes)
			return cinemetaRes, nil
		}
		tmdbRes = <-tmdbChan
		if tmdbRes != nil {
			metaCache.Set(cacheKey, tmdbRes)
			return tmdbRes, nil
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if omdbClient != nil || traktClient != nil {
		utils.Logger.Info("attempting tier 2 metadata")
		g2, ctx2 := errgroup.WithContext(ctx)
		var omdbRes, traktRes *MetaResult
		if omdbClient != nil {
			g2.Go(func() error { omdbRes, _ = fetchOmdb(ctx2, imdbID); return nil })
		}
		if traktClient != nil {
			g2.Go(func() error { traktRes, _ = fetchTrakt(ctx2, imdbID, typ); return nil })
		}
		_ = g2.Wait()
		if omdbRes != nil {
			metaCache.Set(cacheKey, omdbRes)
			return omdbRes, nil
		}
		if traktRes != nil {
			metaCache.Set(cacheKey, traktRes)
			return traktRes, nil
		}
	}

	utils.Logger.Error("failed to resolve metadata", "imdb", imdbID)
	return nil, fmt.Errorf("all metadata providers failed")
}
