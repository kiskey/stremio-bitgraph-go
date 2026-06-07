package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"github.com/user/stremio-bitgraph-go/internal/utils"
)

// Pool is the global SQLite database handle.
// Kept named Pool to preserve compatibility with all callers.
var Pool *sql.DB

func InitDB(ctx context.Context) error {
	dbPath := os.Getenv("DATABASE_URL")
	if dbPath == "" {
		dbPath = "./bitgraph.db"
	}

	// Clean DSN by removing "file:" prefix if present
	dbPath = strings.TrimPrefix(dbPath, "file:")

	// Ensure target directory exists
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("unable to create database directory: %w", err)
		}
	}

	// Open connection with enterprise pragmas baked into the query string for automatic connection-hook execution
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)", dbPath)
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("unable to open sqlite database: %w", err)
	}

	// Verify database connectivity
	if err := pool.PingContext(ctx); err != nil {
		return fmt.Errorf("unable to ping sqlite database: %w", err)
	}

	// Multi-goroutine thread pool optimization (WAL holds concurrent readers + 1 serialized writer safely)
	pool.SetMaxOpenConns(10)
	pool.SetMaxIdleConns(3)
	pool.SetConnMaxLifetime(1 * time.Hour)
	pool.SetConnMaxIdleTime(15 * time.Minute)

	Pool = pool

	// SQLite Dialect Migrations
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS torrents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			infohash TEXT NOT NULL,
			tmdb_id TEXT NOT NULL,
			content_type TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT 'realdebrid',
			torrent_info_json TEXT,
			language TEXT NOT NULL,
			quality TEXT,
			seeders INTEGER,
			added_at TEXT DEFAULT (datetime('now')),
			last_used_at TEXT DEFAULT (datetime('now')),
			UNIQUE (infohash, tmdb_id, content_type, provider)
		);`,
		`CREATE TABLE IF NOT EXISTS debrid_cache (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL,
			hash TEXT NOT NULL,
			provider_torrent_id TEXT,
			status TEXT DEFAULT 'active',
			data TEXT DEFAULT '{}',
			created_at TEXT DEFAULT (datetime('now')),
			updated_at TEXT DEFAULT (datetime('now')),
			UNIQUE (provider, hash)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_debrid_cache_hash ON debrid_cache(hash);`,
		`CREATE INDEX IF NOT EXISTS idx_torrents_search_lookup ON torrents(tmdb_id, content_type, provider);`,
	}

	for _, q := range migrations {
		if _, err := Pool.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	// Read dynamic cache expiry configuration (defaults to 30 days)
	expiryDays := 30
	if envDays := os.Getenv("DATABASE_CLEANUP_DAYS"); envDays != "" {
		if val, err := strconv.Atoi(envDays); err == nil && val > 0 {
			expiryDays = val
		}
	}

	// Launch active background maintenance loop (Prunes stale entries after the configured number of days)
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				utils.Logger.Info("running database maintenance cleanup...", "expiry_days", expiryDays)
				query := fmt.Sprintf("DELETE FROM torrents WHERE last_used_at < datetime('now', '-%d days')", expiryDays)
				res, err := Pool.ExecContext(context.Background(), query)
				if err != nil {
					utils.Logger.Error("database cleanup failed", "error", err)
				} else {
					rowsAffected, _ := res.RowsAffected()
					utils.Logger.Info("database maintenance completed successfully", "rows_purged", rowsAffected)
				}
			}
		}
	}()

	return nil
}
