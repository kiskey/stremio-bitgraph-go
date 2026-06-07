
### **`README.md`**

```markdown
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

Follow these step-by-step instructions to deploy and manage your compiled static binary natively as a daemon on Alpine Linux.

### Step 1: Provision the System User & Directory
For security, create a dedicated, unprivileged system user and group to execute the process:
```bash
# Create group and unprivileged user
addgroup -S gomagnet
adduser -S -D -H -h /var/lib/gomagnet -s /sbin/nologin -G gomagnet gomagnet
```

### Step 2: Install your GoMagnet Binary
Download your statically compiled binary from GitHub Releases (or build it locally) and place it in the system binary path:
```bash
# Copy binary to system path
mv gomagnet /usr/bin/gomagnet
chmod 755 /usr/bin/gomagnet
```

### Step 3: Install the OpenRC Service Control Script
Create the system init daemon manager:
```bash
nano /etc/init.d/gomagnet
```

Paste the following shell-safe code into the file (incorporating auto-directory verification and comment-escaped config loading):

```bash
#!/sbin/openrc-run

name="gomagnet"
description="High-performance GoMagnet Stremio Addon Server"

# Path to the compiled static Go binary
command="/usr/bin/gomagnet"
command_user="gomagnet:gomagnet"
command_background="yes"

# PID file and log paths
pidfile="/run/${RC_SVCNAME}.pid"
output_log="/var/log/gomagnet.log"
error_log="/var/log/gomagnet.log"

depend() {
	need net
	after firewall
}

start_pre() {
	# Ensure the dedicated database directory exists and is owned by the unprivileged user
	checkpath --directory --owner gomagnet:gomagnet --mode 0755 /var/lib/gomagnet
	
	# Ensure the log file exists with write capabilities for the unprivileged user
	checkpath --file --owner gomagnet:gomagnet --mode 0640 /var/log/gomagnet.log

	# Dynamically load and export plain VARIABLE=VALUE lines from the config file.
	# This runs inside OpenRC's launcher shell context right before starting the daemon,
	# forcing the Go binary to automatically inherit any variables defined in the file.
	if [ -f "/etc/conf.d/gomagnet" ]; then
		while read -r line || [ -n "$line" ]; do
			# Strip leading/trailing whitespaces
			cleaned_line=$(echo "$line" | xargs)
			
			# Skip empty lines
			[ -z "${cleaned_line}" ] && continue
			
			# Skip comments (Safely quoted to prevent ash comment interpretation bugs)
			case "${cleaned_line}" in
				"#"*) continue ;;
			esac
			
			# Safely evaluate and export the line to the environment context
			eval "export ${cleaned_line}"
		done < "/etc/conf.d/gomagnet"
	fi
}
```

Save the file (`Ctrl+O`, `Enter`) and mark it executable:
```bash
chmod 755 /etc/init.d/gomagnet
```

### Step 4: Configure the Service Environment File
Create the configuration workspace file. Because our initialization script is equipped with a dynamic `eval` parser, you can copy-paste settings using standard `VARIABLE=VALUE` lines (with **no `export` prefixes required**):

```bash
nano /etc/conf.d/gomagnet
```

Paste and customize your environment credentials:

```env
# ==============================================================================
#                 GoMagnet Alpine Linux Service Configuration
# ==============================================================================
# Sourced dynamically by /etc/init.d/gomagnet before execution.
# Simply write VARIABLE=VALUE (one per line). No "export" prefix is required!

# ── 1. SERVER CONFIGURATION ──────────────────────────────────────────────────
# The port the unified server will listen on.
# Default: 7000
PORT=7000

# The public URL of your instance (used to build debrid play/redirect links).
# Crucial: Must be reachable by your Stremio client.
APP_HOST=https://sbdgo.mjlan.duckdns.org

# Logging level: debug, info, warn, error
# Set to "debug" to view exact compiled queries and similarity scoring.
LOG_LEVEL=info


# ── 2. DATABASE CONFIGURATION (SQLite) ───────────────────────────────────────
# SQLite database path. Points to the secure directory created by the init script.
DATABASE_URL=/var/lib/gomagnet/bitgraph.db

# Cache Expiry: How many days of inactivity before a cached torrent is auto-pruned.
# Default: 30
DATABASE_CLEANUP_DAYS=30

# Optional custom table name for debrid cache (defaults to "debrid_cache").
DEBRID_CACHE_TABLE=debrid_cache


# ── 3. BITMAGNET INDEXER CONFIGURATION ───────────────────────────────────────
# GraphQL API endpoint of your Bitmagnet instance.
BITMAGNET_GRAPHQL_ENDPOINT=http://bitmagnet:3333/graphql


# ── 4. METADATA PROVIDERS CONFIGURATION ──────────────────────────────────────
# Primary TMDB (TheMovieDB) API Key used for title lookup (Required).
TMDB_API_KEY=your_tmdb_api_key_here

# Fallback OMDb API Key (Optional, Tier 2 metadata fallback).
OMDB_API_KEY=your_omdb_api_key_here

# Fallback Trakt Client ID (Optional, Tier 2 metadata fallback).
TRAKT_CLIENT_ID=your_trakt_client_id_here


# ── 5. DEBRID PROVIDERS CONFIGURATION ────────────────────────────────────────
# Which debrid service to use: realdebrid, torbox, or empty for P2P-only.
# If empty, auto-detected from available API keys.
DEBRID_SERVICE=torbox

# Real-Debrid API Key (Required if DEBRID_SERVICE is "realdebrid").
REALDEBRID_API_KEY=your_rd_api_key_here

# TorBox API Key (Required if DEBRID_SERVICE is "torbox").
TORBOX_API_KEY=your_torbox_api_key_here

# TorBox Max Active Torrents (Optional).
# Maximum active downloading torrents allowed before oldest downloads are pruned.
# Set to 0 for unlimited.
TORBOX_MAX_ACTIVE_TORRENTS=0


# ── 6. STREAM SEARCH & FILTER OPTIONS ────────────────────────────────────────
# Comma-separated ISO codes of preferred stream audio languages.
PREFERRED_LANGUAGES=ta,en

# If set to true, non-preferred languages will be dropped entirely from the results.
STRICT_LANGUAGE_FILTER=false

# Minimum matching threshold (0.0 to 1.0) between movie title and torrent filename.
# Default: 0.75. Lower values are more permissive but risk false positives.
SIMILARITY_THRESHOLD=0.75

# Number of returned streams per quality category (4K, 1080p, 720p, SD).
# Default: 2.
STREAM_LIMIT_PER_QUALITY=2

# Comma-separated list of keywords to negate server-side via Bitmagnet FTS.
# Recommended: 3D,CAM,Screener,TS,TC,zip,rar,iso,exe
NEGATE_KEYWORDS=3D,CAM,Screener,TS,TC,zip,rar,iso,exe
```

Save and exit.

### Step 5: Start and Register the Service on Boot
Restart the daemon caching dependencies and start your GoMagnet service:

```bash
# Add daemon to boot sequence
rc-update add gomagnet default

# Start the service
rc-service gomagnet start
```

### Monitoring the Service
You can track real-time logs, incoming requests, and caching events directly from the log file:
```bash
tail -f /var/log/gomagnet.log
```

---

## 🛠️ CI/CD with GitHub Actions

The provided `.github/workflows/release.yml` automatically compiles stripped, static binaries for `amd64` and `arm64` targets with CGO disabled whenever a new tag is pushed.

### Private Module Authorization
If your workflow incorporates private repositories (such as `github.com/ovrlord-app/releasetitleparser`):
1. Generate a **Personal Access Token (PAT)** on GitHub with `read:packages` or `repo` scopes.
2. In your repository settings, go to **Settings > Secrets and variables > Actions > New repository secret**.
3. Create a secret named **`GH_PAT`** and paste your token.
4. Push a tag (e.g., `git tag v2.0.0 && git push origin v2.0.0`) to trigger the compilation and release automatically.
```
```
---


