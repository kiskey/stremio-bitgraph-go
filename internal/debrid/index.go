
package debrid

import (
	"context"
	"fmt"

	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/utils"
)

type disabledProvider struct{}

func (d *disabledProvider) IsEnabled() bool { return false }
func (d *disabledProvider) AddMagnet(ctx context.Context, magnet string) (*AddResult, error) {
	return nil, fmt.Errorf("debrid not configured")
}
func (d *disabledProvider) GetTorrentInfo(ctx context.Context, id string) (*TorrentInfo, error) {
	return nil, fmt.Errorf("debrid not configured")
}
func (d *disabledProvider) SelectFiles(ctx context.Context, id string, fileIDs []string) error {
	return fmt.Errorf("debrid not configured")
}
func (d *disabledProvider) UnrestrictLink(ctx context.Context, link string) (*UnrestrictResult, error) {
	return nil, fmt.Errorf("debrid not configured")
}
func (d *disabledProvider) DeleteTorrent(ctx context.Context, id string) error {
	return fmt.Errorf("debrid not configured")
}
func (d *disabledProvider) GetTorrents(ctx context.Context) ([]Torrent, error) {
	return nil, fmt.Errorf("debrid not configured")
}
func (d *disabledProvider) CheckCached(ctx context.Context, hashes []string) (map[string]CacheStatus, error) {
	result := make(map[string]CacheStatus)
	for _, h := range hashes {
		result[h] = CacheStatus{Cached: false}
	}
	return result, nil
}
func (d *disabledProvider) AddAndSelect(ctx context.Context, magnet string) (*TorrentInfo, error) {
	return nil, fmt.Errorf("debrid not configured")
}
func (d *disabledProvider) GetCachedFileInfo(ctx context.Context, hash, fileName string) (*FileInfo, error) {
	return nil, fmt.Errorf("debrid not configured")
}

var instance Provider

func LoadProvider() Provider {
	if instance != nil {
		return instance
	}
	if config.DebridService == "" {
		utils.Logger.Info("no debrid service configured – P2P only")
		instance = &disabledProvider{}
		return instance
	}
	switch config.DebridService {
	case "realdebrid":
		if !config.RealDebridEnabled {
			utils.Logger.Warn("real-debrid API key missing, P2P only")
			instance = &disabledProvider{}
			return instance
		}
		utils.Logger.Info("using real-debrid provider")
		instance = NewRealDebrid()
	case "torbox":
		if !config.TorboxEnabled {
			utils.Logger.Warn("torbox API key missing, P2P only")
			instance = &disabledProvider{}
			return instance
		}
		utils.Logger.Info("using torbox provider")
		instance = NewTorbox(nil)
	default:
		utils.Logger.Warn("unknown debrid service", "service", config.DebridService)
		instance = &disabledProvider{}
	}
	return instance
}
