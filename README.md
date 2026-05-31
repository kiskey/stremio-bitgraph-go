

# Bitgraph Go

A high-performance, lightweight Go port of `stremio-bitgraph` (the `bitgraph2.0` architecture). This self-hosted Stremio addon bridges your private **Bitmagnet** indexer with premium debrid providers (**Real-Debrid** and **TorBox**) and native **P2P Torrent Streaming** to deliver instant movie and TV show playback.

Built with a unified single-port multiplexed design, a type-safe plug-and-play debrid factory, and an advanced metadata resolution engine.

---

## 🚀 Key Features

### 🔌 Unified Single-Port Architecture

Unlike older setups that split routing across two separate HTTP servers on two different ports (e.g. `7000` and `7001`), **Bitgraph Go** unifies the manifest endpoints, stream searches, and redirect APIs under a single port (`PORT=7000` by default).

- Routes are multiplexed via `go-chi/v5` path-prefix mounting.
- Standardized domain configurations require only a single reverse proxy rule, making deployment behind SSL tunnels (Nginx, Traefik, Caddy, Cloudflare) simple.

### 🛡️ Self-Healing Debrid Engine

- **Metadata Validation:** Verifies magnet structures before initiating selections, preventing downstream payload failures.
- **Terminal Error Discrimination:** Distinguishes between slow/timeout torrent downloads and dead magnets. If a magnet is dead (`dead`, `magnet_error`, `virus`), the engine synchronously purges the torrent from your debrid account to keep it clean.
- **Race-Condition Locks:** Combats concurrent identical requests with an in-memory lock table (`sync.Map`) and Go sync channels to prevent duplicate debrid API allocations.
- **Waterfall Metadata Layer:** Employs concurrent `errgroup` patterns to fetch Cinemeta and TMDB (Tier 1) in parallel, falling back to OMDb and Trakt (Tier 2) only if primary lookups fail.

### 🌊 Advanced P2P & Multi-File Indexing

- **Robust File Indexing:** Correctly resolves `fileIdx` (Stremio’s target file index) for TV season packs and multi-file movie releases.
- **Dynamic Fallbacks:** Parses individual file paths inside multi-file season directories using the `releasetitleparser` library. If no explicit episode is declared but only one file is present, the app automatically maps it.
- **Largest File Matching:** For movie releases packaged with extra featurettes, samples, or trailers, it automatically queries Bitmagnet, identifies the largest video file by size, and plays it.
- **Binge-Watching Support:** Injects `bingeGroup` and `notWebReady` behavior hints to enable Stremio's native autoplay mechanism during P2P playbacks.

---

## 📐 System Architecture

```text
┌────────────────────────────────────────────────────────┐
│                    Stremio Client                      │
└─────────────────────────┬──────────────────────────────┘
                          │
      GET /manifest.json (Port 7000)
      GET /stream/series/... (Port 7000)
      GET /org.stremio.realdebrid.bitmagnet/stream/... (Port 7000)
                          │
                          ▼
┌────────────────────────────────────────────────────────┐
│              Unified Bitgraph Go Addon Server          │
│  - addon.go: Serves manifest & searches Bitmagnet      │
│  - api.go: Resolves debrid stream redirection          │
└──────────────┬───────────────────────────┬─────────────┘
               │                           │
      Query GQL│                           │Resolve MD
               ▼                           ▼
┌──────────────────────┐      ┌─────────────────────────┐
│      Bitmagnet       │      │  Waterfall Metadata    │
│                      │      │  - Cinemeta / TMDB     │
└──────────────────────┘      │  - OMDb / Trakt        │
                              └─────────────────────────┘

## 🛠️ Environment Variables Config (`.env`)

Create a `.env` file in the root of your project using the following parameters:

| Variable | Required | Default | Description |
|-----------|-----------|-----------|-------------|
| `PORT` | No | `7000` | The port the unified server will listen on. |
| `APP_HOST` | Yes | `http://127.0.0.1:7000` | The public URL of your instance (used to build playback URLs). |
| `LOG_LEVEL` | No | `info` | Logging verbosity (`debug`, `info`, `warn`, `error`). |
| `DATABASE_URL` | Yes | - | PostgreSQL connection DSN (`postgres://user:pass@host:5432/db`). |
| `BITMAGNET_GRAPHQL_ENDPOINT` | Yes | - | GraphQL API endpoint of your Bitmagnet instance. |
| `TMDB_API_KEY` | Yes | - | TMDB API Key used for primary title lookup. |
| `OMDB_API_KEY` | No | - | Fallback metadata API key. |
| `TRAKT_CLIENT_ID` | No | - | Fallback metadata client ID. |
| `DEBRID_SERVICE` | No | - | Debrid provider to run (`realdebrid`, `torbox`, or empty for P2P-only). |
| `DEBRID_CACHE_TABLE` | No | `debrid_cache` | PostgreSQL database cache table name. |
| `REALDEBRID_API_KEY` | Conditional | - | API key for Real-Debrid (required if service is `realdebrid`). |
| `TORBOX_API_KEY` | Conditional | - | API key for TorBox (required if service is `torbox`). |
| `TORBOX_MAX_ACTIVE_TORRENTS` | No | `0` | Active download limit before oldest torrents are cleaned. |
| `PREFERRED_LANGUAGES` | No | `en` | Comma-separated ISO codes of preferred stream languages. |
| `STRICT_LANGUAGE_FILTER` | No | `false` | If `true`, non-preferred languages will be dropped entirely. |
| `SIMILARITY_THRESHOLD` | No | `0.75` | Minimum matching threshold (0.0 to 1.0) between movie and file name. |
| `STREAM_LIMIT_PER_QUALITY` | No | `2` | Number of returned streams per quality category (4K, 1080p, etc.). |

---

## 🚀 Quick Start (Local Run)

### Prerequisites

- Go 1.25 or higher
- PostgreSQL instance running

```bash
# 1. Clone the repository
git clone https://github.com/your-username/stremio-bitgraph-go.git
cd stremio-bitgraph-go

# 2. Setup your environment variables
cp .env.example .env
nano .env # Configure your API keys, Database, and Bitmagnet GQL endpoint

# 3. Download dependencies
go mod download

# 4. Start the server
go run ./cmd/server/main.go
```

---

## 🐳 Docker Deployment

### Run Natively with Docker

You can compile and package the application into a minimal Alpine container (~20MB):

```bash
# Build the lean production image
docker build -t stremio-bitgraph-go .

# Run the container exposing port 7000
docker run -d \
  --name bitgraph-go \
  -p 7000:7000 \
  --env-file .env \
  stremio-bitgraph-go
```

### Deploying via Docker Compose

For a robust, production-ready environment including the caching database:

```yaml
version: '3.8'

services:
  addon:
    image: ghcr.io/your-username/stremio-bitgraph-go:latest
    container_name: bitgraph-go
    restart: unless-stopped
    ports:
      - "7000:7000"
    environment:
      - PORT=7000
      - APP_HOST=https://yourdomain.duckdns.org
      - LOG_LEVEL=info
      - DATABASE_URL=postgresql://postgres:db_password@postgres:5432/bitgraph?sslmode=disable
      - BITMAGNET_GRAPHQL_ENDPOINT=http://bitmagnet:3333/graphql
      - TMDB_API_KEY=your_tmdb_key_here
      - DEBRID_SERVICE=realdebrid
      - REALDEBRID_API_KEY=your_rd_api_key_here
      - PREFERRED_LANGUAGES=en
      - STREAM_LIMIT_PER_QUALITY=2
    depends_on:
      - postgres

  postgres:
    image: postgres:15-alpine
    container_name: bitgraph-db
    restart: unless-stopped
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: db_password
      POSTGRES_DB: bitgraph
    volumes:
      - pg_data:/var/lib/postgresql/data

volumes:
  pg_data:
```

---

## 📂 Codebase Architecture

```text
cmd/server/          # Entrypoint (Unified server setup on Port 7000)

internal/
  config/            # Environment configurations & environment parser
  db/                # PostgreSQL pgx connection pool and schema migrations
  utils/             # Custom structured slog.Logger & helper functions
  parser/            # Title parsing wrapper around releasetitleparser
  metadata/          # Waterfall metadata resolver (Cinemeta, TMDB, OMDb, Trakt)
  bitmagnet/         # GraphQL client for querying your Bitmagnet instance
  matcher/           # Similarity processing, sorting, and stream filters
  debrid/            # Factory pattern containing Real-Debrid & TorBox providers
  addon/             # Stremio Addon API handlers (/manifest, /stream)
  api/               # Debrid stream resolution and redirect router

migrations/          # Idempotent SQL schemas
```

---

## ⚙️ Developer Customization (Extending the Factory)

This Go port uses Interface Segregation to make it incredibly easy to add a new debrid provider (such as Premiumize or AllDebrid) without modifying any core router files (`api.go`, `addon.go`, or `main.go`).

1. Write your provider file under `internal/debrid/` (e.g. `premiumize.go`).
2. Implement the `debrid.Provider` interface.
3. If your new provider supports a direct file-download API (like TorBox), implement the optional signature:

```go
func (p *premiumizeProvider) GetDownloadLinkForFile(
    ctx context.Context,
    torrentID,
    fileID string,
) (string, error) {
    // Direct single-file retrieval code
}
```

The engine inside `api.go` will automatically detect the presence of this method at runtime and use it.

4. Add your provider initialization case to `LoadProvider()` inside `internal/debrid/index.go`.

---

## 📄 License

