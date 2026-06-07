-- SQLite Schema Initialization for Stremio Bitgraph Go
-- Tables: torrents, debrid_cache

-- 1. Torrents Metadata Cache Table
CREATE TABLE IF NOT EXISTS torrents (
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
);

-- 2. Debrid Provider State Cache Table
CREATE TABLE IF NOT EXISTS debrid_cache (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    hash TEXT NOT NULL,
    provider_torrent_id TEXT,
    status TEXT DEFAULT 'active',
    data TEXT DEFAULT '{}',
    created_at TEXT DEFAULT (datetime('now')),
    updated_at TEXT DEFAULT (datetime('now')),
    UNIQUE (provider, hash)
);

-- 3. Optimization Indexes for High Concurrency Queries
CREATE INDEX IF NOT EXISTS idx_debrid_cache_hash ON debrid_cache(hash);
CREATE INDEX IF NOT EXISTS idx_torrents_search_lookup ON torrents(tmdb_id, content_type, provider);
