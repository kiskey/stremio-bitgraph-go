package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/db"
	"github.com/user/stremio-bitgraph-go/internal/debrid"
	"github.com/user/stremio-bitgraph-go/internal/matcher"
	"github.com/user/stremio-bitgraph-go/internal/parser"
	"github.com/user/stremio-bitgraph-go/internal/utils"
)

func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(corsMiddleware)
	r.Get("/stream/{type}/{id}/{infoHash}", streamResolveHandler)
	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isTerminalError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "magnet_error") ||
		strings.Contains(msg, "virus") ||
		strings.Contains(msg, "rejected") ||
		errors.Is(err, debrid.ErrResourceNotFound)
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "timed out")
}

func streamResolveHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	typ := chi.URLParam(r, "type")
	id := chi.URLParam(r, "id")
	infoHash := chi.URLParam(r, "infoHash")

	idDecoded, err := url.QueryUnescape(id)
	if err != nil {
		idDecoded = id
	}

	parts := strings.Split(idDecoded, ":")
	imdbID := parts[0]
	var season, episode string
	if len(parts) >= 3 {
		season, episode = parts[1], parts[2]
	}

	go func() {
		<-r.Context().Done()
		cancel()
	}()

	provider := debrid.LoadProvider()
	if !provider.IsEnabled() {
		http.Error(w, "Debrid not configured", http.StatusNotFound)
		return
	}

	if cachedURL, _ := debrid.URLCache.Get(ctx, infoHash); cachedURL != nil {
		if urlStr, ok := cachedURL["url"].(string); ok && urlStr != "" {
			http.Redirect(w, r, urlStr, http.StatusFound)
			return
		}
	}

	var torrentInfo *debrid.TorrentInfo
	var lockEntry *matcher.ProcessingLock

	if raw, ok := matcher.ProcessingLocks.Load(infoHash); ok {
		lockEntry = raw.(*matcher.ProcessingLock)
		switch lockEntry.State {
		case "COMPLETED":
			if lockEntry.DownloadURL != "" {
				http.Redirect(w, r, lockEntry.DownloadURL, http.StatusFound)
				return
			}
			if lockEntry.Data != nil {
				info := lockEntry.Data
				if ti, ok := info["id"].(string); ok {
					torrentInfo = &debrid.TorrentInfo{ID: ti}
					if fn, ok := info["filename"].(string); ok {
						torrentInfo.Filename = fn
					}
					if st, ok := info["status"].(string); ok {
						torrentInfo.Status = st
					}
				}
			}
		case "PROCESSING":
			utils.Logger.Warn("request already processing", "hash", infoHash)
			select {
			case <-lockEntry.Promise:
				if raw2, ok := matcher.ProcessingLocks.Load(infoHash); ok {
					le2 := raw2.(*matcher.ProcessingLock)
					if le2.State == "COMPLETED" && le2.DownloadURL != "" {
						http.Redirect(w, r, le2.DownloadURL, http.StatusFound)
						return
					}
				}
			case <-ctx.Done():
				http.Error(w, "Client closed request", 499)
				matcher.ProcessingLocks.Delete(infoHash)
				return
			}
		case "FAILED":
			http.Error(w, "Previous processing failed: "+lockEntry.Error.Error(), http.StatusInternalServerError)
			return
		}
	}

	if torrentInfo == nil {
		if cached, _ := debrid.TorrentInfoCache.Get(ctx, infoHash); cached != nil {
			if data, ok := cached["torrent_info"].(*debrid.TorrentInfo); ok {
				torrentInfo = data
			}
		}
	}

	if torrentInfo == nil && config.DebridProvider != "" {
		row := db.Pool.QueryRow(ctx,
			"SELECT torrent_info_json FROM torrents WHERE infohash = $1 AND tmdb_id = $2 AND content_type = $3 AND provider = $4",
			infoHash, imdbID, typ, config.DebridProvider)
		var jsonData []byte
		err := row.Scan(&jsonData)
		if err == nil && len(jsonData) > 0 {
			var data map[string]interface{}
			if err := json.Unmarshal(jsonData, &data); err == nil {
				if id, ok := data["id"].(string); ok {
					torrentInfo = &debrid.TorrentInfo{ID: id}
					if fn, ok := data["filename"].(string); ok {
						torrentInfo.Filename = fn
					}
					if st, ok := data["status"].(string); ok {
						torrentInfo.Status = st
					}
					if filesRaw, ok := data["files"].([]interface{}); ok {
						for _, f := range filesRaw {
							fm, _ := f.(map[string]interface{})
							fidf, _ := fm["id"].(float64)
							path, _ := fm["path"].(string)
							bytes, _ := fm["bytes"].(float64)
							torrentInfo.Files = append(torrentInfo.Files, debrid.FileInfo{
								ID:    int(fidf),
								Path:  path,
								Bytes: int64(bytes),
							})
						}
					}
					if linksRaw, ok := data["links"].([]interface{}); ok {
						for _, l := range linksRaw {
							if ls, ok := l.(string); ok {
								torrentInfo.Links = append(torrentInfo.Links, ls)
							}
						}
					}
				}
			}
		}
	}

	if torrentInfo == nil {
		utils.Logger.Info("no cache hit, starting debrid process", "hash", infoHash)

		lock := &matcher.ProcessingLock{
			State:   "PROCESSING",
			Promise: make(chan struct{}),
		}
		matcher.ProcessingLocks.Store(infoHash, lock)

		var torrentID string
		var downloadURL string
		var processErr error

		func() {
			defer func() {
				if ctx.Err() == context.Canceled {
					matcher.ProcessingLocks.Delete(infoHash)
					return
				}
				if processErr != nil {
					utils.Logger.Error("debrid process failed", "hash", infoHash, "error", processErr)
					if isTerminalError(processErr) && torrentID != "" {
						utils.Logger.Warn("terminal error, deleting torrent", "id", torrentID)
						provider.DeleteTorrent(context.Background(), torrentID)
					}
					lock.State = "FAILED"
					lock.Error = processErr
					close(lock.Promise)
					go func() {
						time.Sleep(5 * time.Second) // Lock evicted in 5 seconds on fail to allow fast-retry
						matcher.ProcessingLocks.Delete(infoHash)
					}()
				} else {
					lock.State = "COMPLETED"
					lock.DownloadURL = downloadURL
					close(lock.Promise)
					go func() {
						time.Sleep(30 * time.Second)
						matcher.ProcessingLocks.Delete(infoHash)
					}()
				}
			}()

			activeTorrents, err := provider.GetTorrents(ctx)
			if err != nil {
				processErr = err
				return
			}
			var existing *debrid.Torrent
			for _, t := range activeTorrents {
				if strings.EqualFold(t.Hash, infoHash) {
					existing = &t
					break
				}
			}

			if existing != nil {
				if strings.Contains("magnet_error,error,dead,virus", existing.Status) {
					utils.Logger.Warn("existing torrent has bad status, deleting", "id", existing.ID, "status", existing.Status)
					provider.DeleteTorrent(ctx, existing.ID)
				} else {
					utils.Logger.Info("re-using active torrent", "id", existing.ID, "status", existing.Status)
					torrentID = existing.ID
				}
			}

			if torrentID == "" {
				magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s", infoHash)
				addResult, err := provider.AddMagnet(ctx, magnet)
				if err != nil {
					processErr = fmt.Errorf("failed to add magnet: %w", err)
					return
				}
				torrentID = addResult.ID

				if !addResult.Cached {
					utils.Logger.Info("torrent added, waiting for metadata", "id", torrentID)
					ready := false
					for i := 0; i < 10; i++ {
						select {
						case <-ctx.Done():
							processErr = fmt.Errorf("aborted")
							return
						default:
						}
						time.Sleep(2 * time.Second)
						info, err := provider.GetTorrentInfo(ctx, torrentID)
						if err != nil {
							if strings.Contains(err.Error(), "not found") || errors.Is(err, debrid.ErrResourceNotFound) {
								continue
							}
							processErr = err
							return
						}
						if info == nil {
							continue
						}
						if strings.Contains("magnet_error,error,virus", info.Status) {
							processErr = fmt.Errorf("debrid rejected magnet (%s)", info.Status)
							return
						}
						if info.Status == "waiting_files_selection" || info.Status == "downloaded" {
							ready = true
							break
						}
					}
					if !ready {
						processErr = fmt.Errorf("timed out waiting for metadata")
						return
					}
					freshInfo, err := provider.GetTorrentInfo(ctx, torrentID)
					if err != nil {
						processErr = err
						return
					}
					
					if freshInfo != nil && freshInfo.Status == "waiting_files_selection" {
						fileIDs := make([]string, len(freshInfo.Files))
						for i, f := range freshInfo.Files {
							fileIDs[i] = fmt.Sprintf("%d", f.ID)
						}
						provider.SelectFiles(ctx, torrentID, fileIDs)
					}
				} else {
					utils.Logger.Info("torrent is already cached, skipping pre-selection wait")
				}
			}

			readyTorrent, err := pollTorrentUntilReady(ctx, torrentID, provider)
			if err != nil {
				processErr = err
				return
			}

			lang := "en"
			parsed := parser.RobustParseInfo(readyTorrent.Filename, 0)
			if parsed.Language != "" {
				lang = parsed.Language
			}

			debrid.TorrentInfoCache.Set(ctx, infoHash, map[string]interface{}{
				"torrent_info": readyTorrent,
			})

			_, _ = db.Pool.Exec(ctx,
				`INSERT INTO torrents (infohash, tmdb_id, content_type, provider, torrent_info_json, language, quality, seeders)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				 ON CONFLICT (infohash, tmdb_id, content_type, provider)
				 DO UPDATE SET torrent_info_json = EXCLUDED.torrent_info_json, last_used_at = CURRENT_TIMESTAMP`,
				infoHash, imdbID, typ, config.DebridProvider, readyTorrent, lang, utils.GetQuality(readyTorrent.Filename), readyTorrent.Seeders)

			url, err := resolveDownloadURL(ctx, provider, readyTorrent, typ, season, episode)
			if err != nil {
				processErr = err
				return
			}
			downloadURL = url
			debrid.URLCache.Set(ctx, infoHash, map[string]interface{}{"url": downloadURL})
		}()

		if processErr != nil {
			if processErr.Error() == "aborted" {
				http.Error(w, "Client closed request", 499)
				return
			}
			http.Error(w, "Error: "+processErr.Error()+". Please try another stream.", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, downloadURL, http.StatusFound)
		return
	}

	url, err := resolveDownloadURL(ctx, provider, torrentInfo, typ, season, episode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if raw, ok := matcher.ProcessingLocks.Load(infoHash); ok {
		le := raw.(*matcher.ProcessingLock)
		if le.State == "COMPLETED" {
			le.DownloadURL = url
		}
	}
	debrid.URLCache.Set(ctx, infoHash, map[string]interface{}{"url": url})
	http.Redirect(w, r, url, http.StatusFound)
}

func resolveDownloadURL(ctx context.Context, provider debrid.Provider, torrentInfo *debrid.TorrentInfo, typ, season, episode string) (string, error) {
	if typ == "series" {
		parsed := parser.RobustParseInfo(torrentInfo.Filename, 0)
		fallbackSeason := parsed.Season
		var selectedFiles []debrid.FileInfo
		for _, f := range torrentInfo.Files {
			if f.Selected == 1 {
				selectedFiles = append(selectedFiles, f)
			}
		}
		sVal, _ := strconv.Atoi(season)
		eVal, _ := strconv.Atoi(episode)

		var candidates []parser.CandidateFile
		for _, f := range selectedFiles {
			candidates = append(candidates, parser.CandidateFile{
				ID:   f.ID,
				Path: f.Path,
				Size: f.Bytes,
			})
		}

		var targetFile *debrid.FileInfo
		if bestFile, found := parser.FindBestSeriesFile(candidates, sVal, eVal, fallbackSeason); found {
			for _, f := range selectedFiles {
				if f.ID == bestFile.ID {
					targetFile = &f
					break
				}
			}
		}

		// Strict Single-File Fallback: Only play the single file if it does not explicitly
		// declare a conflicting episode number (e.g. do not play Episode 15 for an Episode 2 request).
		if targetFile == nil && len(selectedFiles) == 1 {
			singleFile := selectedFiles[0]
			singleParsed := parser.ParseFilePath(singleFile.Path, fallbackSeason)
			if singleParsed.Episode == 0 || singleParsed.Episode == eVal {
				targetFile = &singleFile
			}
		}

		if targetFile == nil {
			return "", fmt.Errorf("could not find S%sE%s in torrent", season, episode)
		}

		if dlProvider, ok := provider.(interface {
			GetDownloadLinkForFile(context.Context, string, string) (string, error)
		}); ok {
			return dlProvider.GetDownloadLinkForFile(ctx, torrentInfo.ID, fmt.Sprintf("%d", targetFile.ID))
		}

		idx := -1
		for i, f := range selectedFiles {
			if f.ID == targetFile.ID {
				idx = i
				break
			}
		}
		if idx < 0 || idx >= len(torrentInfo.Links) {
			return "", fmt.Errorf("no link available for selected file")
		}
		link := torrentInfo.Links[idx]
		unrestricted, err := provider.UnrestrictLink(ctx, link)
		if err != nil {
			return "", fmt.Errorf("failed to unrestrict link: %w", err)
		}
		return unrestricted.Download, nil
	} else {
		var selectedFiles []debrid.FileInfo
		for _, f := range torrentInfo.Files {
			if f.Selected == 1 {
				selectedFiles = append(selectedFiles, f)
			}
		}
		if len(selectedFiles) == 0 {
			return "", fmt.Errorf("no selected files in movie torrent")
		}
		var fileToPlay *debrid.FileInfo
		var videoFiles []debrid.FileInfo
		for _, f := range selectedFiles {
			if strings.HasSuffix(strings.ToLower(f.Path), ".mkv") ||
				strings.HasSuffix(strings.ToLower(f.Path), ".mp4") ||
				strings.HasSuffix(strings.ToLower(f.Path), ".avi") ||
				strings.HasSuffix(strings.ToLower(f.Path), ".mov") ||
				strings.HasSuffix(strings.ToLower(f.Path), ".wmv") ||
				strings.HasSuffix(strings.ToLower(f.Path), ".flv") ||
				strings.HasSuffix(strings.ToLower(f.Path), ".webm") {
				videoFiles = append(videoFiles, f)
			}
		}
		if len(videoFiles) > 0 {
			largest := &videoFiles[0]
			for i := 1; i < len(videoFiles); i++ {
				if videoFiles[i].Bytes > largest.Bytes {
					largest = &videoFiles[i]
				}
			}
			fileToPlay = largest
			utils.Logger.Info("selected largest video file", "path", fileToPlay.Path, "size", utils.FormatSize(fileToPlay.Bytes))
		} else {
			largest := &selectedFiles[0]
			for i := 1; i < len(selectedFiles); i++ {
				if selectedFiles[i].Bytes > largest.Bytes {
					largest = &selectedFiles[i]
				}
			}
			fileToPlay = largest
			utils.Logger.Warn("no video files found in torrent, falling back to largest file", "path", fileToPlay.Path, "size", utils.FormatSize(fileToPlay.Bytes))
		}

		if dlProvider, ok := provider.(interface {
			GetDownloadLinkForFile(context.Context, string, string) (string, error)
		}); ok {
			return dlProvider.GetDownloadLinkForFile(ctx, torrentInfo.ID, fmt.Sprintf("%d", fileToPlay.ID))
		}

		idx := -1
		for i, f := range selectedFiles {
			if f.ID == fileToPlay.ID {
				idx = i
				break
			}
		}
		if idx < 0 || idx >= len(torrentInfo.Links) {
			return "", fmt.Errorf("no link available for movie file")
		}
		link := torrentInfo.Links[idx]
		unrestricted, err := provider.UnrestrictLink(ctx, link)
		if err != nil {
			return "", fmt.Errorf("failed to unrestrict movie link: %w", err)
		}
		return unrestricted.Download, nil
	}
}

func pollTorrentUntilReady(ctx context.Context, torrentID string, provider debrid.Provider) (*debrid.TorrentInfo, error) {
	maxAttempts := 60
	baseDelay := 500 * time.Millisecond
	readyStatuses := map[string]bool{"downloaded": true, "finished": true}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("aborted")
		default:
		}

		info, err := provider.GetTorrentInfo(ctx, torrentID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "429") || errors.Is(err, debrid.ErrResourceNotFound) {
				select {
				case <-ctx.Done():
					return nil, fmt.Errorf("aborted")
				case <-time.After(baseDelay):
				}
				continue
			}
			return nil, err
		}
		if info == nil {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("aborted")
			case <-time.After(baseDelay):
			}
			continue
		}

		if readyStatuses[info.Status] {
			if len(info.Links) > 0 {
				utils.Logger.Info("torrent ready for streaming", "id", torrentID, "status", info.Status, "filename", info.Filename)
				return info, nil
			}
			utils.Logger.Warn("torrent downloaded but links not yet generated, retrying...", "id", torrentID)
		}

		utils.Logger.Info("torrent polling progress", "id", torrentID, "status", info.Status, "attempt", attempt+1)

		if strings.Contains("magnet_error,error,virus,dead", info.Status) {
			return nil, fmt.Errorf("debrid rejected magnet (%s)", info.Status)
		}

		sleep := baseDelay * time.Duration(1<<attempt)
		if sleep > 3*time.Second {
			sleep = 3 * time.Second
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("aborted")
		case <-time.After(sleep):
		}
	}
	return nil, fmt.Errorf("torrent %s polling timed out", torrentID)
}
