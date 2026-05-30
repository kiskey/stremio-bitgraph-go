CREATE TABLE IF NOT EXISTS torrents (
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
);

CREATE TABLE IF NOT EXISTS debrid_cache (
    id SERIAL PRIMARY KEY,
    provider TEXT NOT NULL,
    hash TEXT NOT NULL,
    provider_torrent_id TEXT,
    status TEXT DEFAULT 'active',
    data JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(provider, hash)
);

CREATE INDEX IF NOT EXISTS idx_debrid_cache_hash ON debrid_cache(hash);
