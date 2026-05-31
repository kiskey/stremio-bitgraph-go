
package metadata

import (
	"context"
	"encoding/json"
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
	Name   string
	Year   string
	Source string
}

func fetchTmdb(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	url := fmt.Sprintf("https://api.themoviedb.org/3/find/%s?api_key=%s&external_source=imdb_id", imdbID, config.TmdbAPIKey)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := tmdbClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tmdb status %d", resp.StatusCode)
	}
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	var results []interface{}
	if typ == "series" {
		results, _ = data["tv_results"].([]interface{})
	} else {
		results, _ = data["movie_results"].([]interface{})
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("not found")
	}
	item := results[0].(map[string]interface{})
	name := ""
	if typ == "series" {
		name, _ = item["name"].(string)
	} else {
		name, _ = item["title"].(string)
	}
	var year string
	if typ == "series" {
		if fad, ok := item["first_air_date"].(string); ok && len(fad) >= 4 {
			year = fad[:4]
		}
	} else {
		if rd, ok := item["release_date"].(string); ok && len(rd) >= 4 {
			year = rd[:4]
		}
	}
	return &MetaResult{Name: name, Year: year, Source: "TMDB"}, nil
}

func fetchCinemeta(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	url := fmt.Sprintf("https://v3-cinemeta.strem.io/meta/%s/%s.json", typ, imdbID)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := cinemetaClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cinemeta status %d", resp.StatusCode)
	}
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	meta, ok := data["meta"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	name, _ := meta["name"].(string)
	yearStr := ""
	if y, ok := meta["year"].(string); ok {
		yearStr = y
	} else if ri, ok := meta["releaseInfo"].(string); ok {
		yearStr = ri
	}
	re := regexp.MustCompile(`\d{4}`)
	match := re.FindString(yearStr)
	return &MetaResult{Name: name, Year: match, Source: "Cinemeta"}, nil
}

func fetchOmdb(ctx context.Context, imdbID string) (*MetaResult, error) {
	if omdbClient == nil {
		return nil, fmt.Errorf("omdb not configured")
	}
	url := fmt.Sprintf("http://www.omdbapi.com/?apikey=%s&i=%s", config.OmdbAPIKey, imdbID)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := omdbClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if respStr, ok := data["Response"].(string); ok && respStr == "False" {
		return nil, fmt.Errorf("omdb not found")
	}
	name, _ := data["Title"].(string)
	year, _ := data["Year"].(string)
	if year != "" {
		year = strings.Split(year, "–")[0]
	}
	return &MetaResult{Name: name, Year: year, Source: "OMDb"}, nil
}

func fetchTrakt(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	if traktClient == nil {
		return nil, fmt.Errorf("trakt not configured")
	}
	searchType := "show"
	if typ != "series" {
		searchType = "movie"
	}
	url := fmt.Sprintf("https://api.trakt.tv/search/imdb/%s?type=%s", imdbID, searchType)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", config.TraktClientID)
	resp, err := traktClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("trakt status %d", resp.StatusCode)
	}
	var data []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("not found")
	}
	item := data[0][searchType].(map[string]interface{})
	name, _ := item["title"].(string)
	var year string
	if y, ok := item["year"].(float64); ok {
		year = fmt.Sprintf("%.0f", y)
	}
	return &MetaResult{Name: name, Year: year, Source: "Trakt"}, nil
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

	go func() {
		res, err := fetchTmdb(raceCtx, imdbID, typ)
		if err != nil {
			utils.Logger.Warn("TMDB failed", "error", err)
			tmdbChan <- nil
			return
		}
		tmdbChan <- res
	}()

	go func() {
		res, err := fetchCinemeta(raceCtx, imdbID, typ)
		if err != nil {
			utils.Logger.Warn("Cinemeta failed", "error", err)
			cinemetaChan <- nil
			return
		}
		cinemetaChan <- res
	}()

	var tmdbRes, cinemetaRes *MetaResult

	select {
	case tmdbRes = <-tmdbChan:
		if tmdbRes != nil {
			cancel() // Abort Cinemeta connection immediately
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
			cancel() // Abort TMDB connection immediately
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
			g2.Go(func() error {
				omdbRes, _ = fetchOmdb(ctx2, imdbID)
				return nil
			})
		}
		if traktClient != nil {
			g2.Go(func() error {
				traktRes, _ = fetchTrakt(ctx2, imdbID, typ)
				return nil
			})
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
