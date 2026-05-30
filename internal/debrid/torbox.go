package debrid

import (
	"encoding/json"

	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/utils"
)

type torboxProvider struct {
	client        *http.Client
	cache         CacheStore
	selections    map[string]map[int]bool
	recentAdds    map[string]time.Time
	addTimestamps []time.Time
	mu            sync.Mutex
}

type CacheStore interface {
	Get(ctx context.Context, hash string) (map[string]interface{}, error)
	Set(ctx context.Context, hash string, data map[string]interface{}) error
	Update(ctx context.Context, hash string, updates map[string]interface{}) error
	GetByProviderID(ctx context.Context, id string) (map[string]interface{}, error)
}

func NewTorbox(cache CacheStore) Provider {
	return &torboxProvider{
		client:        &http.Client{Timeout: 15 * time.Second},
		cache:         cache,
		selections:    make(map[string]map[int]bool),
		recentAdds:    make(map[string]time.Time),
		addTimestamps: []time.Time{},
	}
}

func (t *torboxProvider) IsEnabled() bool {
	return config.TorboxEnabled
}

func (t *torboxProvider) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	url := "https://api.torbox.app/v1/api" + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+config.TorboxAPIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return t.client.Do(req)
}

func extractInfoHash(magnet string) string {
	idx := strings.Index(magnet, "btih:")
	if idx == -1 {
		return ""
	}
	start := idx + 5
	end := start + 40
	if end > len(magnet) {
		end = len(magnet)
	}
	return strings.ToLower(magnet[start:end])
}

func (t *torboxProvider) checkRateLimit() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-60 * time.Second)
	var kept []time.Time
	for _, ts := range t.addTimestamps {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	t.addTimestamps = kept
	if len(kept) >= 8 {
		return false
	}
	t.addTimestamps = append(t.addTimestamps, now)
	return true
}

func (t *torboxProvider) cleanupStaleActiveTorrents(ctx context.Context, maxActive int) error {
	torrents, err := t.GetTorrents(ctx)
	if err != nil {
		return err
	}
	activeCount := 0
	var stale []Torrent
	for _, tr := range torrents {
		if tr.Status == "downloading" {
			activeCount++
		}
		if tr.Status == "error" {
			stale = append(stale, tr)
		}
	}

	for _, s := range stale {
		_ = t.DeleteTorrent(ctx, s.ID)
	}

	if activeCount >= maxActive {
		for i := len(torrents) - 1; i >= 0; i-- {
			if torrents[i].Status == "downloading" {
				_ = t.DeleteTorrent(ctx, torrents[i].ID)
				activeCount--
				if activeCount < maxActive {
					break
				}
			}
		}
	}
	return nil
}

func (t *torboxProvider) AddMagnet(ctx context.Context, magnet string) (*AddResult, error) {
	hash := extractInfoHash(magnet)
	if hash == "" {
		return nil, fmt.Errorf("invalid magnet link")
	}

	t.mu.Lock()
	if ts, ok := t.recentAdds[hash]; ok && time.Since(ts) < 30*time.Second {
		t.mu.Unlock()
		if t.cache != nil {
			if cached, _ := t.cache.Get(ctx, hash); cached != nil {
				if pid, ok := cached["provider_torrent_id"].(string); ok && pid != "" {
					return &AddResult{ID: pid, Hash: hash, Cached: true}, nil
				}
			}
		}
	} else {
		t.recentAdds[hash] = time.Now()
		t.mu.Unlock()
	}

	if !t.checkRateLimit() {
		return nil, fmt.Errorf("torbox addMagnet rate limit exceeded")
	}

	if config.TorboxMaxActiveTorrents > 0 {
		if err := t.cleanupStaleActiveTorrents(ctx, config.TorboxMaxActiveTorrents); err != nil {
			utils.Logger.Warn("torbox cleanup failed", "error", err)
		}
	}

	if t.cache != nil {
		cached, _ := t.cache.Get(ctx, hash)
		if cached != nil {
			if pid, ok := cached["provider_torrent_id"].(string); ok && pid != "" {
				info, err := t.GetTorrentInfo(ctx, pid)
				if err == nil && info != nil {
					return &AddResult{ID: pid, Hash: hash, Cached: true}, nil
				}
			}
		}
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.WriteField("magnet", magnet)
	writer.Close()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := t.do(ctx, "POST", "/torrents/createtorrent", &body, writer.FormDataContentType())
		if err != nil {
			lastErr = err
			break
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("createtorrent status %d", resp.StatusCode)
			break
		}
		var data map[string]interface{}
		if err := jsonDecode(resp, &data); err != nil {
			lastErr = err
			break
		}
		payload, _ := data["data"].(map[string]interface{})
		if payload == nil {
			payload = data
		}
		torrentID, _ := payload["torrent_id"].(string)
		if torrentID == "" {
			if idf, ok := payload["id"].(float64); ok {
				torrentID = fmt.Sprintf("%.0f", idf)
			}
		}
		hashRet, _ := payload["hash"].(string)
		name, _ := payload["name"].(string)
		isCached := false
		if detail, ok := data["detail"].(string); ok {
			isCached = strings.Contains(strings.ToLower(detail), "cached torrent")
		}
		if torrentID != "" && hashRet != "" {
			if t.cache != nil {
				t.cache.Set(ctx, hash, map[string]interface{}{
					"provider_torrent_id": torrentID,
					"status":              "active",
					"extra":               map[string]interface{}{},
				})
			}
			return &AddResult{ID: torrentID, Hash: hashRet, Name: name, Cached: isCached}, nil
		}
		lastErr = fmt.Errorf("addMagnet response missing id/hash")
		break
	}
	return nil, lastErr
}

func (t *torboxProvider) GetTorrentInfo(ctx context.Context, id string) (*TorrentInfo, error) {
	resp, err := t.do(ctx, "GET", "/torrents/mylist?id="+id, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, ErrResourceNotFound
	}
	var data map[string]interface{}
	if err := jsonDecode(resp, &data); err != nil {
		return nil, err
	}
	list, _ := data["data"].([]interface{})
	if list == nil {
		if d, ok := data["data"].(map[string]interface{}); ok {
			list = []interface{}{d}
		} else {
			list = []interface{}{data}
		}
	}
	if len(list) == 0 {
		return nil, ErrResourceNotFound
	}
	item := list[0].(map[string]interface{})
	return mapTBInfo(id, item, t.selections), nil
}

func mapTBInfo(id string, item map[string]interface{}, selections map[string]map[int]bool) *TorrentInfo {
	info := &TorrentInfo{ID: id}
	info.Filename, _ = item["name"].(string)
	rawStatus, _ := item["download_state"].(string)
	if rawStatus == "" {
		rawStatus, _ = item["status"].(string)
	}
	info.Status = mapTBStatus(rawStatus)
	selectedSet := selections[id]
	if selectedSet == nil {
		selectedSet = make(map[int]bool)
	}
	if filesRaw, ok := item["files"].([]interface{}); ok {
		for _, f := range filesRaw {
			fm, _ := f.(map[string]interface{})
			fidf, _ := fm["id"].(float64)
			fid := int(fidf)
			name, _ := fm["name"].(string)
			size, _ := fm["size"].(float64)
			sel := 1
			if len(selectedSet) > 0 && !selectedSet[fid] {
				sel = 0
			}
			info.Files = append(info.Files, FileInfo{ID: fid, Path: name, Bytes: int64(size), Selected: sel})
		}
	}
	if info.Status == "downloaded" {
		for _, f := range info.Files {
			info.Links = append(info.Links, fmt.Sprintf("tb:%s:%d", id, f.ID))
		}
	} else {
		for range info.Files {
			info.Links = append(info.Links, "")
		}
	}
	return info
}

func mapTBStatus(s string) string {
	switch strings.ToLower(s) {
	case "completed", "cached", "uploading", "seeding", "active", "downloaded":
		return "downloaded"
	case "downloading", "metadl", "checkingresumedata", "stalled", "stalled (no seeds)", "queued":
		return "downloading"
	case "error", "failed", "missingfiles", "expired":
		return "error"
	default:
		return "downloading"
	}
}

func (t *torboxProvider) SelectFiles(ctx context.Context, id string, fileIDs []string) error {
	resp, err := t.do(ctx, "GET", "/torrents/mylist?id="+id, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var data map[string]interface{}
	if err := jsonDecode(resp, &data); err != nil {
		return err
	}
	list, _ := data["data"].([]interface{})
	if len(list) == 0 {
		return fmt.Errorf("torrent not found")
	}
	item := list[0].(map[string]interface{})
	filesRaw, _ := item["files"].([]interface{})
	set := make(map[int]bool)
	if len(fileIDs) == 1 && fileIDs[0] == "all" {
		for _, f := range filesRaw {
			fm, _ := f.(map[string]interface{})
			fidf, _ := fm["id"].(float64)
			set[int(fidf)] = true
		}
	} else {
		for _, fid := range fileIDs {
			var id int
			fmt.Sscanf(fid, "%d", &id)
			set[id] = true
		}
	}
	t.mu.Lock()
	t.selections[id] = set
	t.mu.Unlock()
	return nil
}

func (t *torboxProvider) UnrestrictLink(ctx context.Context, link string) (*UnrestrictResult, error) {
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return &UnrestrictResult{Download: link}, nil
	}
	if strings.HasPrefix(link, "tb:") {
		parts := strings.Split(link, ":")
		if len(parts) == 3 {
			url, err := t.GetDownloadLinkForFile(ctx, parts[1], parts[2])
			if err != nil {
				return nil, err
			}
			return &UnrestrictResult{Download: url}, nil
		}
	}
	return nil, fmt.Errorf("invalid torbox link format")
}

func (t *torboxProvider) DeleteTorrent(ctx context.Context, id string) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.WriteField("id", id)
	writer.WriteField("action", "delete")
	writer.Close()
	resp, err := t.do(ctx, "POST", "/torrents/controltorrent", &body, writer.FormDataContentType())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if t.cache != nil {
		row, _ := t.cache.GetByProviderID(ctx, id)
		if row != nil {
			if hash, ok := row["hash"].(string); ok {
				t.cache.Update(ctx, hash, map[string]interface{}{"status": "deleted"})
			}
		}
	}
	return nil
}

func (t *torboxProvider) GetTorrents(ctx context.Context) ([]Torrent, error) {
	resp, err := t.do(ctx, "GET", "/torrents/mylist", nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data map[string]interface{}
	if err := jsonDecode(resp, &data); err != nil {
		return nil, err
	}
	list, _ := data["data"].([]interface{})
	if list == nil {
		return nil, nil
	}
	var torrents []Torrent
	for _, item := range list {
		m, _ := item.(map[string]interface{})
		id, _ := m["id"].(string)
		hash, _ := m["hash"].(string)
		name, _ := m["name"].(string)
		status, _ := m["status"].(string)
		if status == "" {
			status, _ = m["download_state"].(string)
		}
		torrents = append(torrents, Torrent{ID: id, Hash: hash, Name: name, Status: status})
	}
	return torrents, nil
}

func (t *torboxProvider) CheckCached(ctx context.Context, hashes []string) (map[string]CacheStatus, error) {
	if len(hashes) == 0 {
		return map[string]CacheStatus{}, nil
	}
	bodyMap := map[string]interface{}{"hashes": hashes}
	b, _ := jsonMarshal(bodyMap)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.torbox.app/v1/api/torrents/checkcached?format=object&list_files=true", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+config.TorboxAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data map[string]interface{}
	if err := jsonDecode(resp, &data); err != nil {
		return nil, err
	}
	payload, _ := data["data"].(map[string]interface{})
	if payload == nil {
		payload = data
	}
	result := make(map[string]CacheStatus)
	for _, h := range hashes {
		val := payload[h]
		if vm, ok := val.(map[string]interface{}); ok && vm != nil {
			cs := CacheStatus{Cached: true}
			if idf, ok := vm["id"].(float64); ok {
				cs.TorrentID = fmt.Sprintf("%.0f", idf)
			} else if ids, ok := vm["id"].(string); ok {
				cs.TorrentID = ids
			}
			cs.Name, _ = vm["name"].(string)
			if sz, ok := vm["size"].(float64); ok {
				cs.Size = int64(sz)
			}
			if filesRaw, ok := vm["files"].([]interface{}); ok {
				for _, f := range filesRaw {
					fm, _ := f.(map[string]interface{})
					fidf, _ := fm["id"].(float64)
					fname, _ := fm["name"].(string)
					fsize, _ := fm["size"].(float64)
					cs.Files = append(cs.Files, CacheFile{ID: int(fidf), Name: fname, Size: int64(fsize)})
				}
			}
			result[h] = cs
		} else {
			result[h] = CacheStatus{Cached: false}
		}
	}
	return result, nil
}

func (t *torboxProvider) GetDownloadLinkForFile(ctx context.Context, torrentID, fileID string) (string, error) {
	url := fmt.Sprintf("https://api.torbox.app/v1/api/torrents/requestdl?token=%s&torrent_id=%s&file_id=%s&redirect=false", config.TorboxAPIKey, torrentID, fileID)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+config.TorboxAPIKey)
	resp, err := t.client.Do(req)
	if err != nil {
		utils.Logger.Error("torbox GetDownloadLinkForFile failed", "error", err)
		return "", err
	}
	defer resp.Body.Close()
	var data map[string]interface{}
	if err := jsonDecode(resp, &data); err != nil {
		return "", err
	}
	payload, _ := data["data"].(map[string]interface{})
	if payload == nil {
		payload = data
	}
	if u, ok := payload["url"].(string); ok {
		return u, nil
	}
	return "", fmt.Errorf("no download url")
}

func (t *torboxProvider) GetCachedFileInfo(ctx context.Context, hash, fileName string) (*FileInfo, error) {
	cacheResult, err := t.CheckCached(ctx, []string{hash})
	if err != nil {
		return nil, err
	}
	info := cacheResult[hash]
	if !info.Cached || len(info.Files) == 0 {
		return nil, nil
	}
	for _, f := range info.Files {
		if strings.HasSuffix(f.Name, fileName) || f.Name == fileName {
			return &FileInfo{
				ID:    f.ID,
				Path:  f.Name,
				Bytes: f.Size,
			}, nil
		}
	}
	return nil, nil
}

func (t *torboxProvider) AddAndSelect(ctx context.Context, magnet string) (*TorrentInfo, error) {
	addRes, err := t.AddMagnet(ctx, magnet)
	if err != nil {
		return nil, err
	}
	if addRes.ID != "" {
		if err := t.SelectFiles(ctx, addRes.ID, []string{"all"}); err != nil {
			return nil, err
		}
		return t.GetTorrentInfo(ctx, addRes.ID)
	}
	return nil, fmt.Errorf("addAndSelect failed")
}

func jsonDecode(resp *http.Response, v interface{}) error {
	return json.NewDecoder(resp.Body).Decode(v)
}

func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
