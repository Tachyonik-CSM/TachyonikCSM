<!--
SourceImporter
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# SourceImporter

SourceImporter is a daemon that monitors the ResourceManager database for analyzed sources and imports them to create assets in the AssetManager database.

## Table of Contents

- [Overview](#overview)
- [Building](#building)
- [Versioning](#versioning)
- [Configuration](#configuration)
- [Operation](#operation)
- [Import Process](#import-process)
- [Supported File Types](#supported-file-types)
- [Logging](#logging)

## Overview

SourceImporter automatically processes sources with status "Analysed" from the ResourceManager database. It:

1. Monitors the ResourceManager database for changes
2. Processes sources with status "Analysed"
3. Parses supported file types (e.g., OpenVAS reports)
4. Extracts asset information (e.g., IP addresses)
5. Creates assets in the AssetManager database
6. Updates source status to "Imported" or "Import failed"

### Content handling (PDF)

Import routines receive the source's analyzable text as `ctx.fileContent` and
its detected media type as `ctx.mimeType`. Text formats (XML/JSON/CSV/plain
text) are passed through as-is with the existing `ctx.xml` / `ctx.json` /
`ctx.csv` bindings; **PDFs are converted to plain text** first, so a routine can
string-match a PDF's content the same way it would a text file (and branch on
`ctx.mimeType === "application/pdf"`). Encrypted / scanned / malformed PDFs yield
empty text and are skipped gracefully.

## Building

### Prerequisites

- Go 1.24 or later
- A checkout of the full TachyonikCSM repository — this module resolves
  `tachyonik/lib` through `replace tachyonik/lib => ../TachyonikLib`, so it
  cannot be built from a standalone copy of this directory.

### Build Command

```bash
cd TachyonikCSM/SourceImporter
make build          # embeds the version — see Versioning below
```

This creates the `tachyonik-sourceimporter` binary in the current directory.

A plain `go build -o tachyonik-sourceimporter ./cmd/daemon` also works, but the
binary then reports `0.0.0-dev` because nothing injected a version.

### Dependencies

Dependencies are managed via `go.mod`:
- `github.com/antchfx/xmlquery` - XML parsing for import routines
- `github.com/dop251/goja` - JavaScript runtime the routines execute in
- `github.com/gorilla/websocket` - Import-rule watcher connection to AIManager
- `gopkg.in/yaml.v3` - YAML configuration

## Versioning

SourceImporter carries **its own version**, resolved from its own namespaced git
tag. It is deliberately independent of the TachyonikCSM application version
(which lives in `WebUI/package.json`) and of every other module's.

```bash
git tag sourceimporter/1.0.1     # cut a release
make version                     # what this tree would build as
./tachyonik-sourceimporter version
```

The version is derived with `git describe --tags --match 'sourceimporter/*'`,
stripped to a numeric `x.y.z`, and injected at build time:

```
-ldflags "-X tachyonik/sourceimporter/internal/version.Version=<version>"
```

`--match` is what keeps it independent — every other tag in the monorepo is
ignored. Off-tag or dirty builds get a descriptive suffix (`1.0.1-3-gabc123`,
`1.0.1-dirty`); a tree with no matching tag falls back to `0.0.0-dev`.

Three build paths inject it, and all three must, or a build silently ships the
fallback:

| Path | How |
|---|---|
| `make build` in this directory | derives it from git |
| `make go-build` at the repo root | `SOURCEIMPORTER_VERSION`, derived from git |
| The container image | `VERSION` build arg, passed by `compose.yaml` — the build stage has no git |

### Two different "importer versions"

This build version identifies the running binary and **decides nothing**.

What decides whether a source is imported again is the **import rule's**
version: `aiimporter.GetImporterVersion` composes `AI-<type>-v<rule version>`,
that string is stamped into ResourceManager's `sources.importer_version`, and
`internal/importer` compares it against the rule currently loaded. Those
versions arrive from AIManager with the rules and move when a rule moves.

Keeping them separate is deliberate: tying re-import to the build version would
re-import every source on each release. It also means this tag can be bumped
freely — unlike SourceAnalyser's, where the version *is* the data driving
re-analysis.

## Configuration

### Configuration File

Default configuration file: `./config.yaml`

See `config.yaml.example` for a copy-ready file with every option and its
default. In outline:

```yaml
rscmanager:                            # sources are read from here
  url: http://localhost:8080
  internal_service_key: ""

assetmanager:                          # assets are written here
  url: http://localhost:8081
  internal_service_key: ""

systemmanager:                         # logs and the liveness heartbeat
  url: http://localhost:8083
  internal_service_key: ""

ai_manager:                            # AI provider + system prompt, read at runtime
  url: http://localhost:8085
  internal_service_key: ""

importer:
  poll_interval: 5                     # seconds

heartbeat:
  interval_seconds: 10

log:
  file_path: /home/tachyon/log/tachyonik-sourceimporter.log
  to_console: false
  to_file: true
  level: INFO  # One of DEBUG, INFO, WARN, ERROR
```

The four `internal_service_key` values are the only ones without a usable
default: the daemon cannot authenticate to the other services without them.

### Environment Variables

Environment variables override configuration file values:

| Variable | Description | Default |
|----------|-------------|---------|
| `SOURCEIMPORTER_CONFIG` | Path to config file | `./config.yaml` |
| `RESOURCEMANAGER_URL` | ResourceManager base URL | `http://localhost:8080` |
| `RESOURCEMANAGER_INTERNAL_SERVICE_KEY` | ResourceManager service key | — |
| `ASSETMANAGER_URL` | AssetManager base URL | `http://localhost:8081` |
| `ASSETMANAGER_INTERNAL_SERVICE_KEY` | AssetManager service key | — |
| `SYSTEMMANAGER_URL` | SystemManager base URL | `http://localhost:8083` |
| `SYSTEMMANAGER_INTERNAL_SERVICE_KEY` | SystemManager service key | — |
| `SOURCEIMPORTER_AI_MANAGER_URL` | AIManager base URL | `http://localhost:8085` |
| `AIMANAGER_INTERNAL_SERVICE_KEY` | AIManager service key | — |
| `SOURCEIMPORTER_POLL_INTERVAL` | Seconds between polls for new sources | `5` |
| `SOURCEIMPORTER_HEARTBEAT_INTERVAL_SECONDS` | Liveness heartbeat interval | `10` |
| `SOURCEIMPORTER_LOG_FILE` | Log file path | `./sourceimporter.log` |
| `SOURCEIMPORTER_LOG_TO_CONSOLE` | Log to console (`true`/`1` or `false`/`0`) | `true` |
| `SOURCEIMPORTER_LOG_TO_FILE` | Log to file (`true`/`1` or `false`/`0`) | `true` |
| `SOURCEIMPORTER_LOG_LEVEL` | Log level (DEBUG, INFO, WARN, ERROR) | `INFO` |

`RESOURCEMANAGER_DB_PATH` and `ASSETMANAGER_DB_PATH` no longer exist. They are
left over in some older configs, from before the daemon reached both services
over HTTP; setting them has no effect and they are simply ignored.

### Configuration Priority

1. Environment variables (highest priority)
2. Configuration file
3. Default values (lowest priority)

## Operation

### Starting the Daemon

```bash
./tachyonik-sourceimporter
```

The daemon will:
1. Load configuration from `config.yaml` and environment variables
2. Initialize logging
3. Connect to both ResourceManager and AssetManager databases
4. Process existing sources with status "Analysed"
5. Wait for database changes and process new sources

### Stopping the Daemon

Send `SIGINT` (Ctrl+C) or `SIGTERM` to gracefully shutdown:
- Current processing completes
- Database connections are closed
- Log files are flushed and closed

### Running as a Background Service

```bash
# Start in background
nohup ./tachyonik-sourceimporter > /dev/null 2>&1 &

# Save PID for later
echo $! > sourceimporter.pid

# Stop the daemon
kill $(cat sourceimporter.pid)
```

### Running with Custom Configuration

```bash
# Using custom config file
SOURCEIMPORTER_CONFIG=/path/to/custom-config.yaml ./tachyonik-sourceimporter

# Using environment variables
SOURCEIMPORTER_LOG_LEVEL=DEBUG ./tachyonik-sourceimporter
```

## Import Process

### Status Flow

```
Analysed → Importing → Imported (success)
                    └→ Import failed (error)
```

### Processing Steps

1. **Query**: Fetch all sources with status "Analysed" from ResourceManager database
2. **Filter**: Check if source type is supported. Transient routine-test sources
   (those with a `testRoutineId`) are **not imported**: those for another module
   (e.g. a SourceAnalyser test) are skipped entirely, and the daemon's own import
   tests (`testModule` = "SourceImporter") take the dry-run path below instead.
3. **Update Status**: Set status to "Importing"
4. **Get File**: Get the file from ResourceManager
5. **Parse**: Parse the file based on source type
6. **Extract**: Extract asset information from parsed data
7. **Create Assets**: Insert assets into AssetManager database
   - Duplicate assets are skipped
8. **Update Status**: Set status to "Imported" or "Import failed"

### Single-routine import test (dry-run)

The WebUI's import-routine test panel creates a transient "test source" tagged
with a `testRoutineId` and `testModule` = "SourceImporter" (status "Analysed").
The daemon runs **only** that routine — fetched fresh from AIManager, bypassing
source-type rule matching — against the real file (including PDF text
extraction), counts the assets/vulnerabilities/detections it *would* produce, and
writes that summary as JSON into the source's import notes with status "Tested"
(or an error message with status "Test failed"). It **never writes to
AssetManager** and emits no audit events, so testing a routine has no side
effects. The WebUI reads the summary and deletes the test source.

### Routine execution limits

Import routines are AI-generated JavaScript run over files somebody uploaded, so
the daemon bounds them rather than trusting them. Two limits apply, and both
surface as an ordinary "Import failed" — no operator action is needed to recover:

- **30 seconds per routine.** goja cannot be preempted and this daemon polls on a
  single goroutine, so a routine that never returns would silently stop every
  import until the process was restarted. The budget covers loading the routine,
  the call itself, and reading the result back (a property getter is routine code
  too). A routine that hits it fails that one source and the daemon moves on.
- **16 MiB per source file.** About three times the largest upload tier
  ResourceManager grants (5 MB registered, 3 MB anonymous), so it rejects nothing
  legitimate and leaves room for a per-user plan to raise its own limit. It is
  sized against what *parsing* a file costs rather than the file itself: building
  a DOM amplifies — measurably ~48x for XML and ~20x for JSON — and that happens
  in Go before any routine runs, so the 30 s budget does not cover it. The read
  cap is the only thing between an uploaded file and the heap it becomes.

Each execution also gets a **fresh JavaScript VM**. Nothing a routine leaves in a
global survives to the next file, which matters because one rule's routine runs
over every user's uploads of that type.

## Supported File Types

### OpenVAS Report

**Source Type**: `OpenVAS Report`

**File Format**: XML

**Extracted Assets**:
- **Type**: `host`
- **Name**: IP address
- **Source**: XML path `report/report/host/ip`

**Example XML Structure**:
```xml
<report>
  <report>
    <host>
      <ip>192.168.222.131</ip>
      ...
    </host>
  </report>
</report>
```

**Asset Creation**:
- Each unique IP address becomes a separate asset
- Duplicate IPs are deduplicated automatically
- Asset name: IP address (e.g., "192.168.222.131")
- Asset type: "host"
- Source reference: "{filename} (ID: {sourceID})"

**Example**:
```
Name: 192.168.222.131
Type: host
Source: openvas-sample-report-1.xml (ID: 15)
```

### Adding New File Types

To support additional file types:

1. Add the type to `isSupported()` in `internal/importer/importer.go`:
   ```go
   supportedTypes := []string{"OpenVAS Report", "New Type"}
   ```

2. Add a case to the switch in `processSource()`:
   ```go
   case "New Type":
       importErr = i.importNewType(source)
   ```

3. Implement the import function:
   ```go
   func (i *Importer) importNewType(source *database.Source) error {
       // 1. Find file
       // 2. Parse file
       // 3. Extract assets
       // 4. Create assets in database
   }
   ```

## Logging

### Log Levels

The daemon supports four log levels:

1. **DEBUG**: Detailed information (unsupported types, detailed processing steps)
2. **INFO**: General informational messages (processing started, assets created)
3. **WARN**: Warning messages (failed to create individual assets)
4. **ERROR**: Error messages (import failures, database errors)

### Log Configuration

Configure logging via `config.yaml` or environment variables:

```yaml
log:
  file_path: /home/tachyon/log/tachyonik-sourceimporter.log
  to_console: false
  to_file: true
  level: INFO
```

### Log Output Examples

```
2026/01/07 11:23:45 cmd/daemon/main.go:62: [INFO] Starting Tachyonik SourceImporter daemon...
2026/01/07 11:23:45 cmd/daemon/main.go:78: [INFO] Database connections established
2026/01/07 11:23:45 cmd/daemon/main.go:85: [INFO] Processing existing sources...
2026/01/07 11:23:45 internal/importer/importer.go:36: [INFO] Processing 1 sources for import...
2026/01/07 11:23:45 internal/importer/importer.go:56: [INFO] Starting import for source 15 (OpenVAS Report)
2026/01/07 11:23:45 internal/importer/openvas.go:55: [INFO] Extracted 1 unique IP addresses from OpenVAS report
2026/01/07 11:23:45 internal/importer/importer.go:117: [INFO] Created asset: 192.168.222.131 (type: host)
2026/01/07 11:23:45 internal/importer/importer.go:121: [INFO] Imported 1 hosts from OpenVAS report
2026/01/07 11:23:45 internal/importer/importer.go:79: [INFO] Import successful for source 15
2026/01/07 11:23:50 cmd/daemon/main.go:115: [INFO] Database change detected, processing sources...
```

### Monitoring Logs

```bash
# Follow log file in real-time
tail -f /home/tachyon/log/tachyonik-sourceimporter.log

# Search for errors
grep ERROR /home/tachyon/log/tachyonik-sourceimporter.log

# Count successful imports
grep "Import successful" /home/tachyon/log/tachyonik-sourceimporter.log | wc -l
```

## Error Handling

### Duplicate Assets

If an asset already exists (same name, type, and source), it is skipped:

```
2026/01/07 11:25:00 internal/importer/importer.go:114: [WARN] Failed to create asset for host 192.168.1.1: asset already exists
```

This is normal behavior and prevents duplicate assets.

### Import Failures

When import fails, the source status is set to "Import failed":

```
2026/01/07 11:25:00 internal/importer/importer.go:72: [ERROR] Import failed for source 20: failed to parse XML: ...
```

To retry:
1. Manually update the source status back to "Analysed" in ResourceManager
2. The daemon will automatically reprocess it

## Database Schema

### AssetManager Database (Write)

**Table: assets**

| Column | Type | Description |
|--------|------|-------------|
| `id` | INTEGER PRIMARY KEY | Unique identifier |
| `name` | TEXT | Asset name (e.g., IP address) |
| `type` | TEXT | Asset type (e.g., "host") |
| `source` | TEXT | Reference to source |
| `created_at` | DATETIME | Creation timestamp |
| `modified_at` | DATETIME | Modification timestamp |

## Troubleshooting

### Daemon not processing sources

1. Check that sources have reached status "Analysed" — the daemon polls
   ResourceManager over HTTP for them:
   ```bash
   curl -H "X-Internal-Service-Key: $KEY" http://localhost:8080/api/sources
   ```

2. Check log file for errors:
   ```bash
   tail -f /home/tachyon/log/tachyonik-sourceimporter.log
   ```

### Assets not appearing in AssetManager

1. Verify `assetmanager.url` and its `internal_service_key` are correct — the
   daemon writes assets over HTTP, never to the database directly
2. Check the log for `Created asset` lines, or ask AssetManager:
   ```bash
   curl -H "X-Internal-Service-Key: $KEY" http://localhost:8081/api/assets
   ```

3. Ensure the AssetManager service is running and reachable

## Performance

### Processing Speed

- Each source is processed sequentially
- Asset creation checks for duplicates before inserting

### Resource Usage

- Memory: Minimal in normal use — one file at a time, capped at 16 MiB on the
  way in. Parsing amplifies (see [Routine execution
  limits](#routine-execution-limits)), so the Docker deployment caps the
  container at **512 MiB** (`mem_limit` in `deployment/docker/compose.yaml`) —
  roughly twice the realistic peak, and low enough that a deliberately
  pathological file kills this container instead of exhausting host memory and
  letting the kernel choose an OOM victim elsewhere in the stack.

  A restart after such a kill does not retry the file: a source's status is set
  to "Importing" before its file is fetched, and the poll loop only picks up
  "Analysed".
- CPU: Low (event-driven, mostly idle)

## Architecture

- **Rule Watcher**: WebSocket connection to AIManager for import-rule changes
- **Debouncing**: 500ms timer with mutex to prevent concurrent processing
- **Service APIs**: Reads sources from ResourceManager and writes assets to
  AssetManager, both over HTTP with the internal service key — the daemon opens
  no database of its own
- **Status Management**: Atomic status updates for source tracking
- **Logging**: Custom logger with configurable levels and multi-writer support
- **XML Parsing**: Encoding/xml for OpenVAS report parsing
- **Routine Sandbox**: One goja VM per execution, discarded afterwards, under a
  30s interrupt budget — see [Routine execution limits](#routine-execution-limits)
