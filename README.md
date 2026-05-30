# Stremio Bitgraph Go

Go port of stremio-bitgraph (bitgraph2.0 branch).

## Features
- Stremio addon server (port 7000)
- Debrid resolution API (port 7001)
- Real-Debrid and TorBox support
- PostgreSQL caching
- Sonarr/Radarr-based title parsing via `releasetitleparser`

## Quick Start

```bash
# Copy env
cp .env.sample .env
# Edit .env with your keys

# Run
export $(cat .env | xargs)
go run ./cmd/server
```

## Docker

```bash
docker build -t stremio-bitgraph-go .
docker run -p 7000:7000 -p 7001:7001 --env-file .env stremio-bitgraph-go
```

## Architecture

```
cmd/server/          # Entry point
internal/
  config/            # Environment config
  db/                # PostgreSQL pool & migrations
  utils/             # Logger, helpers
  parser/            # Title parsing (releasetitleparser wrapper)
  metadata/          # TMDB, Cinemeta, OMDb, Trakt
  bitmagnet/         # GraphQL client
  matcher/           # Torrent matching & sorting
  debrid/            # Provider interface + RD + TorBox
  addon/             # Stremio addon HTTP handlers
  api/               # Debrid resolution HTTP handlers
migrations/          # SQL schema
```
