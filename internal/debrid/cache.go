package debrid

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/db"
)

type DBCache struct {
	provider string
	table    string
}

func NewDBCache(provider string) *DBCache {
	return &DBCache{provider: provider, table: config.DebridCacheTable}
}

func (c *DBCache) Get(ctx context.Context, hash string) (map[string]interface{}, error) {
	query := fmt.Sprintf("SELECT * FROM %s WHERE provider = ?1 AND hash = ?2", c.table)
	row := db.Pool.QueryRowContext(ctx, query, c.provider, hash)
	var id int
	var provider, hashStr, providerTorrentID, status string
	var data []byte
	var createdAt, updatedAt interface{}
	err := row.Scan(&id, &provider, &hashStr, &providerTorrentID, &status, &data, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]interface{}{
		"id":                  id,
		"provider":            provider,
		"hash":                hashStr,
		"provider_torrent_id": providerTorrentID,
		"status":              status,
	}
	if len(data) > 0 {
		var extra map[string]interface{}
		json.Unmarshal(data, &extra)
		result["data"] = extra
	}
	return result, nil
}

func (c *DBCache) Set(ctx context.Context, hash string, data map[string]interface{}) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (provider, hash, provider_torrent_id, status, data)
		VALUES (?1, ?2, ?3, ?4, ?5)
		ON CONFLICT (provider, hash)
		DO UPDATE SET provider_torrent_id = EXCLUDED.provider_torrent_id,
			status = EXCLUDED.status,
			data = EXCLUDED.data,
			updated_at = datetime('now')`, c.table)
	pid, _ := data["provider_torrent_id"].(string)
	status, _ := data["status"].(string)
	extra := map[string]interface{}{}
	if e, ok := data["extra"].(map[string]interface{}); ok {
		extra = e
	}
	extraJSON, _ := json.Marshal(extra)
	_, err := db.Pool.ExecContext(ctx, query, c.provider, hash, pid, status, extraJSON)
	return err
}

func (c *DBCache) Update(ctx context.Context, hash string, updates map[string]interface{}) error {
	sets := []string{}
	args := []interface{}{c.provider, hash}
	argIdx := 3
	if pid, ok := updates["provider_torrent_id"]; ok {
		sets = append(sets, fmt.Sprintf("provider_torrent_id = ?%d", argIdx))
		args = append(args, pid)
		argIdx++
	}
	if status, ok := updates["status"]; ok {
		sets = append(sets, fmt.Sprintf("status = ?%d", argIdx))
		args = append(args, status)
		argIdx++
	}
	if extra, ok := updates["extra"]; ok {
		sets = append(sets, fmt.Sprintf("data = ?%d", argIdx))
		b, _ := json.Marshal(extra)
		args = append(args, b)
		argIdx++
	}
	sets = append(sets, "updated_at = datetime('now')")
	if len(sets) == 0 {
		return nil
	}
	query := fmt.Sprintf("UPDATE %s SET %s WHERE provider = ?1 AND hash = ?2", c.table, joinStrings(sets, ", "))
	_, err := db.Pool.ExecContext(ctx, query, args...)
	return err
}

func (c *DBCache) Delete(ctx context.Context, hash string) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE provider = ?1 AND hash = ?2", c.table)
	_, err := db.Pool.ExecContext(ctx, query, c.provider, hash)
	return err
}

func (c *DBCache) GetByProviderID(ctx context.Context, id string) (map[string]interface{}, error) {
	query := fmt.Sprintf("SELECT * FROM %s WHERE provider = ?1 AND provider_torrent_id = ?2", c.table)
	row := db.Pool.QueryRowContext(ctx, query, c.provider, id)
	var rid int
	var provider, hashStr, providerTorrentID, status string
	var data []byte
	var createdAt, updatedAt interface{}
	err := row.Scan(&rid, &provider, &hashStr, &providerTorrentID, &status, &data, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]interface{}{
		"id":                  rid,
		"provider":            provider,
		"hash":                hashStr,
		"provider_torrent_id": providerTorrentID,
		"status":              status,
	}
	if len(data) > 0 {
		var extra map[string]interface{}
		json.Unmarshal(data, &extra)
		result["data"] = extra
	}
	return result, nil
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
