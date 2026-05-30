
package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

var Pool *pgxpool.Pool

func InitDB(ctx context.Context) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("unable to create connection pool: %w", err)
	}
	Pool = pool

	migrations := []string{
		`CREATE TABLE IF NOT EXISTS torrents (
			id SERIAL PRIMARY KEY,
			infohash TEXT NOT NULL,
			tmdb_id TEXT NOT NULL,
			content_type TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT 'realdebrid',
			torrent_info_json JSONB,
			language TEXT NOT NULL,
			quality TEXT,
			seeders INTEGER,
			added_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			last_used_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			CONSTRAINT torrents_infohash_tmdb_id_content_type_provider_key
				UNIQUE (infohash, tmdb_id, content_type, provider)
		);`,
		`CREATE TABLE IF NOT EXISTS debrid_cache (
			id SERIAL PRIMARY KEY,
			provider TEXT NOT NULL,
			hash TEXT NOT NULL,
			provider_torrent_id TEXT,
			status TEXT DEFAULT 'active',
			data JSONB DEFAULT '{}',
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(provider, hash)
		);`,
		`DO $$ BEGIN
			-- Handle migrations/fixes for existing stale debrid_cache tables
			IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'debrid_cache') THEN
				-- Rename legacy info_hash/infohash columns if they exist
				IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='debrid_cache' AND column_name='info_hash') THEN
					ALTER TABLE debrid_cache RENAME COLUMN info_hash TO hash;
				ELSIF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='debrid_cache' AND column_name='infohash') THEN
					ALTER TABLE debrid_cache RENAME COLUMN infohash TO hash;
				END IF;

				-- Ensure the hash column exists
				IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='debrid_cache' AND column_name='hash') THEN
					ALTER TABLE debrid_cache ADD COLUMN hash TEXT NOT NULL DEFAULT '';
				END IF;

				-- Ensure the unique constraint on (provider, hash) exists
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'debrid_cache_provider_hash_key') THEN
					BEGIN
						ALTER TABLE debrid_cache ADD CONSTRAINT debrid_cache_provider_hash_key UNIQUE (provider, hash);
					EXCEPTION WHEN duplicate_table OR duplicate_object THEN
						-- Do nothing if already exists under a different system constraint name
					END;
				END IF;
			END IF;
		END $$;`,
		`CREATE INDEX IF NOT EXISTS idx_debrid_cache_hash ON debrid_cache(hash);`,
		`ALTER TABLE torrents ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'realdebrid';`,
		`DO $$ BEGIN
			IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='torrents' AND column_name='rd_torrent_info_json') THEN
				ALTER TABLE torrents RENAME COLUMN rd_torrent_info_json TO torrent_info_json;
			END IF;
		END $$;`,
		`ALTER TABLE torrents DROP CONSTRAINT IF EXISTS unique_torrent_source;`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'torrents_infohash_tmdb_id_content_type_provider_key') THEN
				ALTER TABLE torrents ADD CONSTRAINT torrents_infohash_tmdb_id_content_type_provider_key
					UNIQUE (infohash, tmdb_id, content_type, provider);
			END IF;
		END $$;`,
	}

	for _, q := range migrations {
		if _, err := Pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return nil
}
