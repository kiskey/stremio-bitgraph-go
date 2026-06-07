````markdown
# GoMagnet (Bitgraph Go)

A high-performance, lightweight Go port of `stremio-bitgraph` (the `bitgraph2.0` architecture). This self-hosted Stremio addon bridges your private **Bitmagnet** indexer with premium debrid providers (**Real-Debrid** and **TorBox**) and native **P2P Torrent Streaming** to deliver instant movie and TV show playback.

This release has been migrated to an **embedded SQLite backend**, introducing full Write-Ahead Logging (WAL) concurrency, localized caching speed, a sub-microsecond badge processing engine, and a drastically simplified deployment footprint with zero external database dependencies.

---

## 🚀 Key Features

### 🔌 Unified Single-Port Architecture

All routes are multiplexed under a single, unified port (`PORT=7000` by default). Routes are routed via `go-chi/v5` path-prefix mounting. This design simplifies SSL tunneling and reverse proxying (Nginx, Traefik, Caddy, Cloudflare Tunnels).

### ⚡ Sub-Microsecond Multi-Stage Badge Engine

Unifies all 39 visual, audio, streaming, and quality rules from `badges.json` directly into your compiled Go binary. Deconstructed from heavy Perl backtracking regular expressions to standard RE2-compliant POS/NEG assert structures. It parses full metadata on-the-fly with near-zero latency, zero heap allocations, and zero GC pressure.

### 💾 Zero-Config Embedded SQLite Cache

PostgreSQL database requirements have been fully replaced. SQLite is configured natively for concurrent environments:

- **Write-Ahead Logging (WAL)**: Allows multiple parallel read routines while executing serialized writes without blocking.
- **Auto-Maintenance**: An automated background loop runs daily to prune stale, inactive cache entries based on a configurable inactivity threshold.
- **Atomic Operations**: Safe, ACID-compliant local transactions with auto-healing locks.

---

## 🛠️ Environment Variables Config (`.env`)

Create a `.env` file in the root of your project using the following parameters:

| Variable | Required | Default | Description |
|-----------|-----------|-----------|-------------|
| `PORT` | No | `7000` | The port the unified server will listen on. |
| `APP_HOST` | Yes | `http://127.0.0.1:7000` | The public URL of your instance (used to build debrid redirect/play links). |
| `LOG_LEVEL` | No | `info` | Logging verbosity (`debug`, `info`, `warn`, `error`). |
| `DATABASE_URL` | No | `./bitgraph.db` | Local SQLite database file path. |
| `DATABASE_CLEANUP_DAYS` | No | `30` | Inactivity days before cached torrents are auto-pruned. |
| `BITMAGNET_GRAPHQL_ENDPOINT` | Yes | - | GraphQL API endpoint of your Bitmagnet instance. |
| `TMDB_API_KEY` | Yes | - | TMDB API Key used for primary title lookup. |
| `OMDB_API_KEY` | No | - | Fallback metadata API key (Tier 2 fallback). |
| `TRAKT_CLIENT_ID` | No | - | Fallback metadata client ID (Tier 2 fallback). |
| `DEBRID_SERVICE` | No | - | Debrid provider (`realdebrid`, `torbox`, or empty for P2P-only). |
| `DEBRID_CACHE_TABLE` | No | `debrid_cache` | SQLite database cache table name. |
| `REALDEBRID_API_KEY` | Conditional | - | API key for Real-Debrid (required if service is `realdebrid`). |
| `TORBOX_API_KEY` | Conditional | - | API key for TorBox (required if service is `torbox`). |
| `TORBOX_MAX_ACTIVE_TORRENTS` | No | `0` | Active download limit before oldest torrents are cleaned. |
| `PREFERRED_LANGUAGES` | No | `en` | Comma-separated ISO codes of preferred stream languages. |
| `STRICT_LANGUAGE_FILTER` | No | `false` | If `true`, non-preferred languages will be dropped entirely. |
| `SIMILARITY_THRESHOLD` | No | `0.75` | Minimum matching threshold (0.0 to 1.0) between movie and file name. |
| `STREAM_LIMIT_PER_QUALITY` | No | `2` | Number of returned streams per quality category (4K, 1080p, etc.). |
| `NEGATE_KEYWORDS` | No | - | Comma-separated list of keywords to negate server-side via Bitmagnet FTS. |

---

## 📦 Docker Deployment

Due to the transition to SQLite, GoMagnet no longer requires a PostgreSQL database container. It is now a single-container service.

### Single-Container Compose (`docker-compose.yml`)

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
      - DATABASE_URL=/data/bitgraph.db
      - DATABASE_CLEANUP_DAYS=30
      - BITMAGNET_GRAPHQL_ENDPOINT=http://bitmagnet:3333/graphql
      - TMDB_API_KEY=your_tmdb_key_here
      - DEBRID_SERVICE=realdebrid
      - REALDEBRID_API_KEY=your_rd_api_key_here
      - PREFERRED_LANGUAGES=ta,en
      - STREAM_LIMIT_PER_QUALITY=2
    volumes:
      - ./data:/data
```

---

## 🏔️ Running on Alpine Linux as a Service (OpenRC)

Follow these steps to deploy and manage your compiled static binary as a native daemon on Alpine Linux.

### Step 1: Provision the System User

For security, create a dedicated, unprivileged system user and group to execute the process:

```bash
# Create group and unprivileged user
addgroup -S gomagnet
adduser -S -D -H -h /var/lib/gomagnet -s /sbin/nologin -G gomagnet gomagnet
```

### Step 2: Install your GoMagnet Binary

Download your statically compiled binary from GitHub Releases (or build it locally) and place it in the system binary path:

```bash
# Copy binary
mv gomagnet /usr/bin/gomagnet
chmod 755 /usr/bin/gomagnet
```

### Step 3: Install the OpenRC Service Control Script

Create the init daemon manager:

```bash
nano /etc/init.d/gomagnet
```

Paste the contents of `/etc/init.d/gomagnet` into the file, save, and mark it executable:

```bash
chmod 755 /etc/init.d/gomagnet
```

### Step 4: Configure the Service Environment File

Create the configuration workspace. Any modifications can be copied and pasted directly here using standard `VARIABLE=VALUE` structures (no export prefixes required):

```bash
nano /etc/conf.d/gomagnet
```

Configure your credentials and database storage path:

```text
DATABASE_URL=/var/lib/gomagnet/bitgraph.db
```

### Step 5: Start & Enable on Boot

Run the daemon and register it to start automatically when the system boots:

```bash
# Start the service
rc-service gomagnet start

# Enable service on boot
rc-update add gomagnet default
```

To monitor logs or execution health:

```bash
tail -f /var/log/gomagnet.log
```

---

## 🛠️ CI/CD with GitHub Actions

The provided `.github/workflows/release.yml` automatically compiles stripped, static binaries for `amd64` and `arm64` targets with CGO disabled whenever a new tag is pushed.

### Private Module Authorization

If your workflow incorporates private repositories (such as `github.com/ovrlord-app/releasetitleparser`):

1. Generate a Personal Access Token (PAT) on GitHub with `repo` scope or equivalent fine-grained repository read permissions.
2. Navigate to:

   ```
   Settings → Secrets and variables → Actions
   ```

3. Click:

   ```
   New repository secret
   ```

4. Create a secret named:

   ```
   GH_PAT
   ```

5. Paste your token and save.

### Triggering a Release

Create and push a version tag:

```bash
git tag v2.0.0
git push origin v2.0.0
```

Or create a release directly from the GitHub UI:

1. Open **Releases**.
2. Click **Draft a new release**.
3. Create a new tag such as `v2.0.0`.
4. Publish the release.

The workflow will automatically:

- Build static Linux binaries
- Compile `amd64` and `arm64` targets
- Strip symbols (`-s -w`)
- Upload binaries to the GitHub Release page

Generated artifacts:

```text
gomagnet-linux-amd64
gomagnet-linux-arm64
```
````
