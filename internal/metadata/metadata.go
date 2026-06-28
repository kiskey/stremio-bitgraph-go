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
	imdbToTmdbID   = utils.NewTTLCache(7 * 24 * time.Hour)
)

func init() {
	if config.OmdbAPIKey != "" {
		omdbClient = utils.NewOptimizedClient(8 * time.Second)
	}
	if config.TraktClientID != "" {
		traktClient = utils.NewOptimizedClient(8 * time.Second)
	}
}

type SeasonInfo struct {
	SeasonNumber int `json:"season_number"`
	EpisodeCount int `json:"episode_count"`
}

type MetaResult struct {
	Name      string
	Year      string
	Source    string
	AltTitles []string
	Type      string
	Seasons   []SeasonInfo
}

type EpisodeMeta struct {
	AirDate string `json:"air_date"`
}

// Low-Allocation Structs for TMDB Deserialization
type tmdbFindResponse struct {
	MovieResults []struct {
		Title         string `json:"title"`
		ReleaseDate   string `json:"release_date"`
		OriginalTitle string `json:"original_title"`
		ID            int    `json:"id"`
	} `json:"movie_results"`
	TvResults []struct {
		Name         string `json:"name"`
		FirstAirDate string `json:"first_air_date"`
		OriginalName string `json:"original_name"`
		ID            int    `json:"id"`
	} `json:"tv_results"`
}

// Low-Allocation Structs for Cinemeta Deserialization
type cinemetaMetaResponse struct {
	Meta struct {
		Name          string   `json:"name"`
		Year          string   `json:"year"`
		ReleaseInfo   string   `json:"releaseInfo"`
		OriginalTitle string   `json:"original_title"`
		Aka           []string `json:"aka"`
	} `json:"meta"`
}

// stripDiacritics maps standard Latin-1 and advanced unicode diacritics to ASCII base characters
func stripDiacritics(s string) string {
	var replacer = strings.NewReplacer(
		"ā", "a", "á", "a", "à", "a", "ä", "a", "â", "a", "ã", "a", "å", "a",
		"ē", "e", "é", "e", "è", "e", "ë", "e", "ê", "e",
		"ī", "i", "í", "i", "ì", "i", "ï", "i", "î", "i",
		"ō", "o", "ó", "o", "ò", "o", "ö", "o", "ô", "o", "õ", "o", "ø", "o",
		"ū", "u", "ú", "u", "ù", "u", "ü", "u", "û", "u",
		"ý", "y", "ÿ", "y",
		"ñ", "n", "ç", "c",
		"Ā", "A", "Á", "A", "À", "A", "Ä", "A", "Â", "A", "Ã", "A", "Å", "A",
		"Ē", "E", "É", "E", "È", "E", "Ë", "E", "Ê", "E",
		"Ī", "I", "Í", "I", "Ì", "I", "Ï", "I", "Î", "I",
		"Ō", "O", "Ó", "O", "Ò", "O", "Ö", "O", "Ô", "O", "Õ", "O", "Ø", "O",
		"Ū", "U", "Ú", "U", "Ù", "U", "Ü", "U", "Û", "U",
		"Ý", "Y", "Ñ", "N", "Ç", "C",
	)
	return replacer.Replace(s)
}

// injectNormalizedAltTitle adds the un-accented ASCII representation to AltTitles if it differs from the primary name
func injectNormalizedAltTitle(res *MetaResult) {
	if res == nil {
		return
	}
	normalized := stripDiacritics(res.Name)
	if normalized != res.Name {
		isUnique := true
		for _, existing := range res.AltTitles {
			if existing == normalized {
				isUnique = false
				break
			}
		}
		if isUnique {
			res.AltTitles = append(res.AltTitles, normalized)
		}
	}
}

// executeWithRetry provides resilient, allocation-free execution for external API calls
func executeWithRetry(ctx context.Context, fn func(context.Context) (*MetaResult, error)) (*MetaResult, error) {
	res, err := fn(ctx)
	if err != nil && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "timeout")) {
		// Attempt a single fast retry with a fresh context to avoid parent deadline exhaustion
		retryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return fn(retryCtx)
	}
	return res, err
}

func fetchTmdb(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	return executeWithRetry(ctx, func(reqCtx context.Context) (*MetaResult, error) {
		url := fmt.Sprintf("https://api.themoviedb.org/3/find/%s?api_key=%s&external_source=imdb_id", imdbID, config.TmdbAPIKey)
		req, _ := http.NewRequestWithContext(reqCtx, "GET", url, nil)
		resp, err := tmdbClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("tmdb status %d", resp.StatusCode)
		}

		var data tmdbFindResponse
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, err
		}

		name, year := "", ""
		var altTitles []string
		contentType := "movie"
		var seasons []SeasonInfo

		if len(data.TvResults) > 0 {
			contentType = "series"
			item := data.TvResults[0]
			name = item.Name
			if len(item.FirstAirDate) >= 4 {
				year = item.FirstAirDate[:4]
			}
			if item.OriginalName != "" && item.OriginalName != name {
				altTitles = append(altTitles, item.OriginalName)
			}
			imdbToTmdbID.Set(imdbID, item.ID)

			// Fetch detailed TV show info in the same high-level call to enrich seasons list
			tvURL := fmt.Sprintf("https://api.themoviedb.org/3/tv/%d?api_key=%s", item.ID, config.TmdbAPIKey)
			tvReq, _ := http.NewRequestWithContext(reqCtx, "GET", tvURL, nil)
			tvResp, tvErr := tmdbClient.Do(tvReq)
			if tvErr == nil && tvResp.StatusCode == 200 {
				defer tvResp.Body.Close()
				var tvDetails struct {
					Seasons []struct {
						SeasonNumber int `json:"season_number"`
						EpisodeCount int `json:"episode_count"`
					} `json:"seasons"`
				}
				if err := json.NewDecoder(tvResp.Body).Decode(&tvDetails); err == nil {
					for _, s := range tvDetails.Seasons {
						seasons = append(seasons, SeasonInfo{
							SeasonNumber: s.SeasonNumber,
							EpisodeCount: s.EpisodeCount,
						})
					}
				}
			}
		} else if len(data.MovieResults) > 0 {
			contentType = "movie"
			item := data.MovieResults[0]
			name = item.Title
			if len(item.ReleaseDate) >= 4 {
				year = item.ReleaseDate[:4]
			}
			if item.OriginalTitle != "" && item.OriginalTitle != name {
				altTitles = append(altTitles, item.OriginalTitle)
			}
		} else {
			return nil, fmt.Errorf("not found")
		}

		return &MetaResult{
			Name:      name,
			Year:      year,
			Source:    "TMDB",
			AltTitles: altTitles,
			Type:      contentType,
			Seasons:   seasons,
		}, nil
	})
}

func fetchCinemeta(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	return executeWithRetry(ctx, func(reqCtx context.Context) (*MetaResult, error) {
		url := fmt.Sprintf("https://v3-cinemeta.strem.io/meta/%s/%s.json", typ, imdbID)
		req, _ := http.NewRequestWithContext(reqCtx, "GET", url, nil)
		resp, err := cinemetaClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("cinemeta status %d", resp.StatusCode)
		}

		var data cinemetaMetaResponse
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, err
		}

		meta := data.Meta
		if meta.Name == "" {
			return nil, fmt.Errorf("not found")
		}

		yearStr := meta.Year
		if yearStr == "" {
			yearStr = meta.ReleaseInfo
		}
		match := yearRegexp.FindString(yearStr)

		var altTitles []string
		if meta.OriginalTitle != "" && meta.OriginalTitle != meta.Name {
			altTitles = append(altTitles, meta.OriginalTitle)
		}
		for _, aka := range meta.Aka {
			if aka != "" && aka != meta.Name {
				isUnique := true
				for _, existing := range altTitles {
					if existing == aka {
						isUnique = false
						break
					}
				}
				if isUnique {
					altTitles = append(altTitles, aka)
				}
			}
		}

		contentType := "movie"
		if typ == "series" {
			contentType = "series"
		}

		return &MetaResult{Name: meta.Name, Year: match, Source: "Cinemeta", AltTitles: altTitles, Type: contentType}, nil
	})
}

func fetchOmdb(ctx context.Context, imdbID string) (*MetaResult, error) {
	if omdbClient == nil {
		return nil, fmt.Errorf("omdb not configured")
	}
	return executeWithRetry(ctx, func(reqCtx context.Context) (*MetaResult, error) {
		url := fmt.Sprintf("http://www.omdbapi.com/?apikey=%s&i=%s", config.OmdbAPIKey, imdbID)
		req, _ := http.NewRequestWithContext(reqCtx, "GET", url, nil)
		resp, err := omdbClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		var data struct {
			Response string `json:"Response"`
			Title    string `json:"Title"`
			Year     string `json:"Year"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, err
		}
		if data.Response == "False" {
			return nil, fmt.Errorf("omdb not found")
		}

		year := data.Year
		if year != "" {
			year = strings.Split(year, "–")[0]
		}
		return &MetaResult{Name: data.Title, Year: year, Source: "OMDb", AltTitles: nil, Type: "movie"}, nil
	})
}

func fetchTrakt(ctx context.Context, imdbID, typ string) (*MetaResult, error) {
	if traktClient == nil {
		return nil, fmt.Errorf("trakt not configured")
	}
	return executeWithRetry(ctx, func(reqCtx context.Context) (*MetaResult, error) {
		searchType := "show"
		if typ != "series" {
			searchType = "movie"
		}
		url := fmt.Sprintf("https://api.trakt.tv/search/imdb/%s?type=%s", imdbID, searchType)
		req, _ := http.NewRequestWithContext(reqCtx, "GET", url, nil)
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

		var data []struct {
			Movie map[string]interface{} `json:"movie"`
			Show  map[string]interface{} `json:"show"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("not found")
		}

		item := data[0].Movie
		if typ == "series" {
			item = data[0].Show
		}

		name, _ := item["title"].(string)
		var year string
		if y, ok := item["year"].(float64); ok {
			year = fmt.Sprintf("%.0f", y)
		}
		contentType := "movie"
		if typ == "series" {
			contentType = "series"
		}
		return &MetaResult{Name: name, Year: year, Source: "Trakt", AltTitles: nil, Type: contentType}, nil
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
			injectNormalizedAltTitle(tmdbRes)
			metaCache.Set(cacheKey, tmdbRes)
			return tmdbRes, nil
		}
		cinemetaRes = <-cinemetaChan
		if cinemetaRes != nil {
			injectNormalizedAltTitle(cinemetaRes)
			metaCache.Set(cacheKey, cinemetaRes)
			return cinemetaRes, nil
		}
	case cinemetaRes = <-cinemetaChan:
		if cinemetaRes != nil {
			cancel()
			utils.Logger.Info("resolved via Cinemeta (early-exit)", "name", cinemetaRes.Name)
			injectNormalizedAltTitle(cinemetaRes)
			metaCache.Set(cacheKey, cinemetaRes)
			return cinemetaRes, nil
		}
		tmdbRes = <-tmdbChan
		if tmdbRes != nil {
			injectNormalizedAltTitle(tmdbRes)
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
			injectNormalizedAltTitle(omdbRes)
			metaCache.Set(cacheKey, omdbRes)
			return omdbRes, nil
		}
		if traktRes != nil {
			injectNormalizedAltTitle(traktRes)
			metaCache.Set(cacheKey, traktRes)
			return traktRes, nil
		}
	}

	utils.Logger.Error("failed to resolve metadata", "imdb", imdbID)
	return nil, fmt.Errorf("all metadata providers failed")
}

func GetEpisodeDetails(ctx context.Context, imdbID string, season, episode int) (*EpisodeMeta, error) {
	cacheKey := fmt.Sprintf("ep_%s_%d_%d", imdbID, season, episode)
	if v, ok := metaCache.Get(cacheKey); ok {
		return v.(*EpisodeMeta), nil
	}

	var tvID int
	if cachedID, ok := imdbToTmdbID.Get(imdbID); ok {
		tvID = cachedID.(int)
	} else {
		url := fmt.Sprintf("https://api.themoviedb.org/3/find/%s?api_key=%s&external_source=imdb_id", imdbID, config.TmdbAPIKey)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := tmdbClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			var data tmdbFindResponse
			if err := json.NewDecoder(resp.Body).Decode(&data); err == nil && len(data.TvResults) > 0 {
				tvID = data.TvResults[0].ID
				imdbToTmdbID.Set(imdbID, tvID)
			}
		}
	}

	if tvID == 0 {
		return nil, fmt.Errorf("could not find TMDB TV ID for IMDb %s", imdbID)
	}

	epURL := fmt.Sprintf("https://api.themoviedb.org/3/tv/%d/season/%d/episode/%d?api_key=%s", tvID, season, episode, config.TmdbAPIKey)
	req, _ := http.NewRequestWithContext(ctx, "GET", epURL, nil)
	resp, err := tmdbClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("episode details API status %d", resp.StatusCode)
	}

	var epDetails EpisodeMeta
	if err := json.NewDecoder(resp.Body).Decode(&epDetails); err != nil {
		return nil, err
	}

	metaCache.Set(cacheKey, &epDetails)
	return &epDetails, nil
}
