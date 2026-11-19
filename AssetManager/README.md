# AssetManager

AssetManager is a REST API server that manages asset records with real-time WebSocket updates.

What AssetManager must and must not do is specified in [spec/](spec/README.md), its HTTP contract in [spec/openapi.yaml](spec/openapi.yaml). This README covers building, configuring and running it; where the two disagree, the specification wins.

## Table of Contents

- [Building](#building)
- [Versioning](#versioning)
- [Configuration](#configuration)
- [Operation](#operation)
- [REST API](#rest-api)
- [WebSocket API](#websocket-api)
- [Logging](#logging)

## Building

### Prerequisites

- Go 1.24 or later
- A checkout of the full TachyonikCSM repository — this module resolves
  `tachyonik/lib` through `replace tachyonik/lib => ../TachyonikLib`, so it
  cannot be built from a standalone copy of this directory.

### Build Command

```bash
cd TachyonikCSM/AssetManager
make build          # embeds the version — see Versioning below
```

This creates the `tachyonik-assetmanager` binary in the current directory.

A plain `go build -o tachyonik-assetmanager ./cmd/server` also works, but the
binary then reports `0.0.0-dev` because nothing injected a version.

From the repository root, `make go-build` builds every service binary into
`/tmp/tachyonikcsm-build/` (this one as `assetmanager`).

### Dependencies

Dependencies are managed via `go.mod`:
- `github.com/gorilla/mux` - HTTP router
- `github.com/gorilla/websocket` - WebSocket support
- `github.com/jmoiron/sqlx` - Database operations
- `github.com/mattn/go-sqlite3` - SQLite driver
- `gopkg.in/yaml.v3` - YAML configuration

## Versioning

AssetManager carries **its own version**, resolved from its own namespaced git
tag. It is deliberately independent of the TachyonikCSM application version
(which lives in `WebUI/package.json`) and of every other module's.

```bash
git tag assetmanager/1.0.0        # cut a release
make version                      # what this tree would build as
./tachyonik-assetmanager version  # also --version or -v
```

The version is derived with `git describe --tags --match 'assetmanager/*'`,
stripped to a numeric `x.y.z`, and injected at build time:

```
-ldflags "-X tachyonik/assetmanager/internal/version.Version=<version>"
```

`--match` is what keeps it independent — every other tag in the monorepo is
ignored. Off-tag or dirty builds get a descriptive suffix (`1.0.0-3-gabc123`,
`1.0.0-dirty`); a tree with no matching tag falls back to `0.0.0-dev`. The first line of every start names it — `Starting Tachyonik AssetManager <version> (<go version>, <os>/<arch>)` — so a support log shows exactly what ran. The
running version is also logged at startup.

Three build paths inject it, and all three must, or a build silently ships the
fallback:

| Path | How |
|---|---|
| `make build` in this directory | derives it from git |
| `make go-build` at the repo root | `ASSETMANAGER_VERSION`, derived from git |
| The container image | `VERSION` build arg, passed by `compose.yaml` — the build stage has no git |

This is the build version only: nothing compares it or stores it. The database
schema has its own version, the migration runner's (see [Database](#database)),
which moves with every migration.

## Configuration

### Configuration File

Default configuration file: `./config.yaml` (override with `ASSETMANAGER_CONFIG`). Priority, highest first: environment variables, `config.yaml`, built-in defaults.

**An unusable configuration stops AssetManager at startup** (AM-OPS-03, AM-OPS-16) instead of running it on defaults: a file that exists but cannot be read or parsed, a setting it does not know (a misspelt key, named with its line), or an instance mode other than `appliance` or `saas` (any case), from the file or `ASSETMANAGER_INSTANCE_MODE`. The reason goes to standard error (`journalctl`, `docker logs`) and the exit code is 1. Without a file, the defaults and the environment apply.

`config.yaml.example` lists every option with its default and environment variable; copy it to `config.yaml` and adjust. A test keeps it complete (`internal/config/example_test.go`). In outline:

```yaml
server:        { port: 8081, host: localhost }
instance:      { mode: appliance }        # appliance or saas; must match SystemManager
database:      { path: /home/tachyon/data/AssetManager.db }
auth:          { jwt_secret: "...", internal_service_key: "..." }
cors:          { allowed_origins: [http://localhost:5173] }
rscmanager:    { url: http://localhost:8080, internal_service_key: "..." }
systemmanager: { url: http://localhost:8083, internal_service_key: "..." }
aimanager:     { url: "", internal_service_key: "..." }   # licence cap and API catalogue; set in both modes
log:           { file_path: /home/tachyon/log/tachyonik-assetmanager.log, to_console: false, to_file: true, level: INFO }
```

`log.to_console` and `log.to_file` must always be stated when a config file exists: they are applied as read, so a missing one becomes `false` and silently switches that log output off instead of falling back to the default `true`.

### Environment Variables

Environment variables override configuration file values:

| Variable | Description | Default |
|----------|-------------|---------|
| `ASSETMANAGER_CONFIG` | Path to config file | `./config.yaml` |
| `ASSETMANAGER_PORT` | Server port (1–65535) | `8081` |
| `ASSETMANAGER_HOST` | Server host | `localhost` |
| `ASSETMANAGER_INSTANCE_MODE` | Operating mode, `appliance` or `saas`; must match SystemManager's | `appliance` |
| `ASSETMANAGER_DB_PATH` | SQLite database path | `/home/tachyon/data/AssetManager.db` |
| `ASSETMANAGER_JWT_SECRET` | Secret the user tokens are checked with; must match SystemManager's. While it is the public placeholder or shorter than 32 characters, a `SECURITY` warning is logged at startup | (placeholder) |
| `ASSETMANAGER_INTERNAL_SERVICE_KEY` | Key other services present to call this one; empty refuses every internal call | (empty) |
| `ASSETMANAGER_LOG_FILE` | Log file path | `./assetmanager.log` |
| `ASSETMANAGER_LOG_TO_CONSOLE` | Log to console: `true`, `false`, `1` or `0`, in any case | `true` |
| `ASSETMANAGER_LOG_TO_FILE` | Log to file: `true`, `false`, `1` or `0`, in any case | `true` |
| `ASSETMANAGER_LOG_LEVEL` | Log level: DEBUG, INFO, WARN or ERROR, in any case | `INFO` |
| `ASSETMANAGER_RESOURCEMANAGER_URL` | ResourceManager base URL, for the source an asset came from | `http://localhost:8080` |
| `ASSETMANAGER_RESOURCEMANAGER_INTERNAL_SERVICE_KEY` | Internal service key for ResourceManager | (empty) |
| `ASSETMANAGER_SYSTEMMANAGER_URL` | SystemManager base URL, for user limits, workspace members, the organisation, scores, statistics and audit events | `http://localhost:8083` |
| `ASSETMANAGER_SYSTEMMANAGER_INTERNAL_SERVICE_KEY` | Internal service key for SystemManager | (empty) |
| `ASSETMANAGER_AIMANAGER_URL` | Local AIManager base URL for the per-appliance license (instance-wide host-asset cap). Set on an appliance; empty on the SaaS to disable the cap. | (empty) |
| `ASSETMANAGER_AIMANAGER_INTERNAL_SERVICE_KEY` | Internal service key for the AIManager license call | (empty) |

A port, true/false or log-level value AssetManager cannot use stops it at startup
with exit code 1 (AM-OPS-19), whether it comes from a variable above or from
the file (`server.port`, `log.level`), and the error lists every such setting with what it
takes. An empty variable counts as unset. Before 2026-11-18 such values were
dropped without a word — `ASSETMANAGER_LOG_TO_FILE=yes` switched the file log
off, and an unknown level fell back to INFO — so check a deployment's
variables when upgrading.

### Per-appliance license (instance-wide host-asset cap)

On an **appliance** deployment, a purchased per-appliance subscription licenses
the appliance for up to *N* host assets. AssetManager enforces this **across all
users** (in addition to each user's `max_host_assets`): when creating a `Host`
asset it reads the licensed cap from the local AIManager (`aimanager.url`, which
owns the pairing token and fetches the license from the SaaS) and rejects the
create with `403` once the instance-wide Host-asset count reaches the cap.
Existing assets are never removed. Enforcement is best-effort and fails **open**
(the create is allowed) if the license cannot be read, so a transient AIManager
outage never blocks asset creation. On the SaaS (`instance mode: saas`)
AIManager reports no appliance license, so no cap applies.

### Platform API catalogue

At startup AssetManager registers its API document, `spec/openapi.yaml`
(embedded in the binary by `spec/spec.go`), with AIManager
at `aimanager.url`, in the background
and retried until AIManager takes it (AM-OPS-18). That is how generated
routines and the WebUI learn what AssetManager offers: see
[docs/specs/01-platform-api-catalogue.md](../docs/specs/01-platform-api-catalogue.md).
Set `aimanager.url` in both instance modes; with it empty the document is not
registered, and the log says so. The document must stay true to the code —
`spec/spec_test.go` and `internal/handlers/api_document_test.go` fail when a
route is undocumented, a documented route is not served, or a bridge
operation answers differently from its schema (AM-API-14).

### Configuration Priority

1. Environment variables (highest priority)
2. Configuration file
3. Default values (lowest priority)

## Operation

### Starting the Server

```bash
./tachyonik-assetmanager
```

The server will:
1. Load configuration from `config.yaml` and environment variables; stop with an error if it cannot be used
2. Initialize logging
3. Connect to the SQLite database (creating it if it doesn't exist)
4. Run database migrations
5. Start the WebSocket hub
6. Start the HTTP server on the configured host and port

### Stopping the Server

Send `SIGINT` (Ctrl+C) or `SIGTERM` to gracefully shutdown:
- Server stops accepting new connections
- Outstanding requests are given 10 seconds to complete
- Database connections are closed
- Log files are flushed and closed

### Running with Custom Configuration

```bash
# Using custom config file
ASSETMANAGER_CONFIG=/path/to/custom-config.yaml ./tachyonik-assetmanager

# Using environment variables
ASSETMANAGER_PORT=8082 ASSETMANAGER_LOG_LEVEL=DEBUG ./tachyonik-assetmanager
```

### Logs

Logs are written to:
- **File**: `/home/tachyon/log/tachyonik-assetmanager.log` (default)
- **Console**: Disabled by default (can be enabled via config)

Log format includes:
- Timestamp
- Log level (DEBUG, INFO, WARN, ERROR)
- Source file and line number
- Message

### Health and metrics

- `GET /health` answers 200 while the process runs; `GET /ready` answers 503
  until startup has finished, then 200. Both are open: the container's health
  check and Compose's start order ask them without a key.
- `GET /metrics` (Prometheus format: requests by method, route template and
  status, durations, requests in flight) answers only an authenticated
  caller: a service with `X-Internal-Service-Key`, or a user with a session,
  an anonymous one included, checked as for the API. Otherwise 401, or 403
  for a deactivated account (AM-OPS-17). The observability stack's Prometheus
  sends the service key from `$TACHYONIKCSM_CONFIG_DIR/prometheus-service-key`
  (see `deployment/observability/prometheus/prometheus.yml`).

## REST API

### Base URL

```
http://localhost:8081/api
```

### MCP server

`POST /api/mcp` is AssetManager's MCP server (MCP's JSON-RPC, one JSON object per
request: `initialize`, `ping`, `tools/list`, `tools/call`), for AI agents such
as the chat acting for a signed-in user
([PA-MCP-04 to -06](../docs/specs/01-platform-api-catalogue.md)). Its tools
are generated at startup from `spec/openapi.yaml`: one per operation marked
`x-mcp`, named by its `operationId`. It takes a user's session (`Authorization: Bearer`) only; a request
carrying the internal service key is refused with 403 (AM-API-15, AM-SEC-23). A tool call is made as
the API call it stands for, through the same router with the caller's
session, so it answers exactly what that call answers the user.

The operations marked `x-mcp: read` — the chat's tools for AssetManager's data — are `assets.stats`, `assets.list`, `vulnerabilities.list` and `detections.list`. A test fails when the server offers anything else, or when a signed-in user cannot call one of them (PA-MCP-03).

### Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/assets` | List all assets for the authenticated user. Each carries `lastSeen`, `lastScanned` (null when never scanned), `lastScanCoverageVerification` and `scanCoverageIntervalDays` (null until an import reports them). Optional `?filter=<json>` with columns `name`, `type`, `severity`, `score`, `lastSeenDays`, `lastScannedDays`, `coverageVerifiedDays` (days ago; never counts as very long ago), `scanCoverageIntervalDays` (an asset without one counts as a very long interval) and `detections` (how many detections name the asset's host, among the listed users'). The statistics' *No Threat* and *Unmanaged* are `severity = 0` with `score = 100` and `score = 0`: an asset scores 100 with findings of severity 0 and 0 with none, so a host whose only findings are vulnerabilities of severity 0 is No Threat (AM-DATA-19). `lastScannedDays`, `coverageVerifiedDays` and `scanCoverageIntervalDays` also take the operators `is set` and `is not set`, without a value: `is not set` selects the never scanned, never verified and unmonitored assets (a date that cannot be read counts as not set), the same ones the older `> 99999` still finds |
| `POST` | `/api/assets` | Create an asset, or report it again, for an import (internal only) |
| `DELETE` | `/api/assets/by-source` | When ResourceManager deletes a source: remove its links and the assets left without one (internal only) |
| `DELETE` | `/api/assets/stale` | After a re-import: remove the source's asset links that no import has touched since the given timestamp (`asset_source.modified_at`), and the assets left without a source (internal only) |
| `POST` | `/api/assets/scan-coverage` | Apply a scan coverage table from one source: `{"userId", "sourceId", "sourceLabel", "entries": [{"target", "scanCoverageIntervalDays", "scanCoverageVerifiedAt"}]}` (internal only; the service names the user and the source id, as for creating an asset). See [Scan coverage tables](#scan-coverage-tables). Answers with `covered` and `created` asset IDs and the counts of `hostnames`, `unmatched`, `invalid` and `refused` entries |
| `GET` | `/api/assets/stats` | Get aggregated asset statistics for the current user: counts by severity band, plus host assets by last seen (`hostsSeen30d` ≤ 30 days, `hostsSeen180d` 31–180 days, `hostsSeenOlder` > 180 days or unreadable) and by last scanned (`hostsScanned2d` ≤ 2 days, `hostsScanned10d` > 2 to 10 days, `hostsScannedOlder` > 10 days, `hostsNeverScanned` none or unreadable) and by scan coverage interval (`hostsMonitored2d` every 1–2 days, `hostsMonitored10d` every 3–10 days, `hostsMonitoredOlder` less often, `hostsUnmonitored` no interval) |
| `GET` | `/api/assets/{id}` | One asset, if it is the caller's (or their group's); `404` otherwise |
| `GET` | `/api/vulnerabilities` | List all vulnerabilities for the authenticated user |
| `POST` | `/api/vulnerabilities` | Create a vulnerability for an import, and report its host asset (internal only) (see [One record per finding](#one-record-per-finding), [Findings and their host asset](#findings-and-their-host-asset), [Findings and their source](#findings-and-their-source)) |
| `DELETE` | `/api/vulnerabilities/by-source` | When ResourceManager deletes a source: delete its vulnerabilities (internal only) |
| `DELETE` | `/api/vulnerabilities/stale` | Delete vulnerabilities not modified since a given timestamp (internal only) |
| `GET` | `/api/detections` | List all detections for the authenticated user |
| `POST` | `/api/detections` | Create a detection for an import, and report its host asset (internal only) (see [One record per finding](#one-record-per-finding), [Findings and their host asset](#findings-and-their-host-asset), [Findings and their source](#findings-and-their-source)) |
| `DELETE` | `/api/detections/by-source` | When ResourceManager deletes a source: delete its detections (internal only) |
| `DELETE` | `/api/detections/stale` | Delete detections not modified since a given timestamp (internal only) |
| `POST` | `/api/internal/organisation/recalculate-score` | Recalculate the Exposure and Coverage sub-scores for a user (internal only). In SaaS they are the owner's; on an appliance they are the shared workspace's, computed over all its users' assets and sent to each user's organisation, as the statistics already count the whole workspace (AM-DATA-41, -42) |
| `DELETE` | `/api/internal/users/{id}/data` | Delete all records of a user, all or nothing: a failure deletes nothing and answers `500` (AM-DATA-37; internal only) |
| `PATCH` | `/api/internal/users/{id}/data/transfer?newOwner=` | Move all records of a user to another, all or nothing. A record the receiver already has is merged into theirs: an asset of the same name and type gives it its links (one link per source), a finding of the same identity is folded in with the newer report's severity and the newest last seen. A failure moves nothing and answers `500` (AM-DATA-38; internal only) |
| `GET` | `/api/internal/users/ids` | Distinct user ids this service holds data for. SystemManager's orphan sweep diffs it against the live users and purges the strays (internal only) |
| `GET` | `/api/internal/users/usage` | How many host assets, vulnerabilities and detections each user holds and the instance in all, counted as the limits count them, whether all users' host assets count against each limit (appliance), and the licensed number while a licence caps them (AM-DATA-45). SystemManager shows them in Admin → Users (internal only) |
| `POST` | `/api/mcp` | The MCP server, for a signed-in user's session only (see *MCP server*) |

#### The sources rule the records

AssetManager holds what the sources report, and nothing else (AM-PUR-20):

- **Only an import creates records:** `POST /api/assets`, `/api/vulnerabilities`,
  `/api/detections` and `/api/assets/scan-coverage` are for services. A user gets
  `403` (`Records are created only by importing a source`), and every create must
  name its source by `sourceId` (`400` otherwise). A routine that adds hosts creates
  a source in ResourceManager instead.
- **Only the source removes them:** a record goes when its source is deleted
  (ResourceManager calls the `by-source` endpoints) or re-imported (the `stale`
  endpoints), and an asset only once no remaining source refers to it. There is no
  deletion by id and no keeping a source's records without it; a user gets `403`
  on `by-source`.
- **The sources' sweep** (AM-DATA-46) catches what a deletion missed: a minute after
  the start and then every hour, AssetManager asks ResourceManager about each source
  id its records refer to and removes the records of a source ResourceManager answers
  with its own `404 {"error": "Source not found"}`, as deleting the source would. Any
  other answer leaves the source alone until the next round. Records that name no
  source id at all (findings without one, links known by a label only, assets without
  a link) are removed by the sweep too. Each round logs what it removed.
- **Deleting an account** still removes all its records (SystemManager's internal
  endpoints).

#### One record per finding

A finding is identified by its owner, its source, its host, its port and its
name (AM-DATA-47). Sending one whose identity is on record **reports it
again** instead of storing a copy, the way an asset is reported again:

- the answer is `200` with the same record (`201` only for a new finding);
- the record takes the new `sourceLabel` and, for a vulnerability, the new
  `severity`; its `lastSeen` only moves forward;
- it is marked as touched by this import, so the stale sweep after a re-import
  keeps it, and its id stays the same across re-imports;
- only a new finding is held to the vulnerability or detection limit;
  reporting a known one again is never refused for a limit, nor while
  SystemManager cannot be asked;
- it is announced as `VULNERABILITY_UPDATED` / `DETECTION_UPDATED`, with the
  whole record, when its severity, label or last seen changed.

So a scanner that reports a check once per virtual hostname of a host
(OpenVAS does) gives one vulnerability per host and port. Findings from
different sources stay separate records. Migration v8 merged the copies
stored before into their oldest record (the highest severity, the newest last
seen) and added a unique index on the identity.

#### Findings and their source

A vulnerability or detection names the source it came from as an asset does: `sourceId`, the ResourceManager source id (required), and `sourceLabel`, its filename. Lists and change messages carry the same two fields (`sourceId` is `null` when only the name is known).

For one release, a create request that has neither still has its older `source` string `"filename (ID: N)"` read instead; the first such request of each kind is logged with a warning, so a deployment can tell whether anything still sends it. The fallback is removed in the next release.

**A filter rule that cannot be read is refused**, not left out: an unknown
column, an operator the column does not take, or a value it cannot read (a
number where one is needed) is answered with `400` and the rule and the
reason, e.g. `{"error": "Filter rule 2: \"severity\" needs a number, not \"high\""}`.
Leaving the rule out used to list more than was asked for (AM-API-05).

**The three lists can be sorted and cut** (AM-API-16): `?sort=<field>` —
assets `severity`, `score`, `name`, `type`, `createdAt`, `modifiedAt`,
`lastSeen`, `lastScanned`; vulnerabilities `severity`, `name`, `host`,
`createdAt`, `modifiedAt`, `lastSeen`; detections `name`, `host`,
`createdAt`, `modifiedAt`, `lastSeen` — with `&order=asc|desc` (default asc
for text, desc for numbers and times), and `?limit=1..500`, which answers at
most that many while `total` still counts every match. All three are
optional; without them a list is answered as before. An unknown field or
order, `order` without `sort`, or a limit outside 1–500 is refused with `400`.

The `by-source` and `stale` endpoints are called with the source reference `?source=filename (ID: N)` and find a source's findings by the id in it — so a renamed file still finds its own. A reference without `(ID: N)` is refused with `400`. Both are for services only.

**Every deletion answers alike** (AM-API-13), whatever the kind (assets,
vulnerabilities, detections) and the way (by source, stale):
`deleted`/`deletedIds` for the records that are gone, `updated`/`updatedIds`
for those that remain but changed (an asset another source still reports,
recomputed), and `skipped`/`skippedIds`, always empty since deletion by id was
removed. Every pair is always
present, `0` and `[]` when it does not apply, so a client can update its
lists from the ids. The earlier fields — `message`, and `ids` on the finding
by-source answers — are still sent for one release and then removed.

#### List Assets

Retrieves all assets for the authenticated user (and their group).

A `?userId=` query parameter, naming whose assets to list, is accepted **only
from an internal service** (a valid `X-Internal-Service-Key`); a user who sends
one gets `403`. The same holds for `GET /api/assets/stats?userId=`. When
creating an asset, a `userId` in the body is likewise used only for a service's
request; a user's asset is always their own. A service must name the owner,
in the body or with `X-User-ID`, and the owner must exist; otherwise the
create is refused with `400` (AM-SEC-13).

**Request:**
```
GET /api/assets
Authorization: Bearer <JWT_TOKEN>
```

**Response:**
```json
{
  "assets": [
    {
      "id": 1,
      "name": "192.168.1.1",
      "type": "Host",
      "sources": [{"sourceId": 15, "sourceLabel": "openvas-report.xml", "lastSeen": "2026-01-07T10:30:00Z"}],
      "sourcesLabel": "openvas-report.xml",
      "userId": 5,
      "createdAt": "2026-01-07T10:30:00Z",
      "modifiedAt": "2026-01-07T10:30:00Z"
    }
  ],
  "total": 1
}
```

**Response Fields:**
- `assets`: Array of asset objects (may be `null` if no assets exist)
- `total`: Total number of assets

**Asset Object:**
- `id`: Unique asset identifier (integer)
- `name`: Asset name/identifier (string)
- `type`: Asset type (string); `Host`, the only type
- `sources`: The sources that reported the asset, each `{sourceId, sourceLabel, lastSeen}` (`sourceId` is null for a source known only by name)
- `sourcesLabel`: The sources' names, comma-separated, for display
- `userId`: User ID who owns this asset (integer)
- `createdAt`: Creation timestamp (ISO 8601)
- `modifiedAt`: Last modification timestamp (ISO 8601)

**Status Codes:**
- `200 OK`: Success
- `401 Unauthorized`: Missing or invalid JWT token
- `500 Internal Server Error`: Database error

**Example:**
```bash
# Regular user request (requires JWT token)
curl -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  http://localhost:8081/api/assets
```

---

#### Create Asset

Creates an asset for an import, or reports it again (internal only: a user is
refused with `403`, see [The sources rule the records](#the-sources-rule-the-records)).

**Request:**
```
POST /api/assets
X-Internal-Service-Key: <KEY>
Content-Type: application/json
```

**Request Body:**
```json
{
  "name": "192.168.1.1",
  "type": "Host",
  "sourceId": 15,
  "sourceLabel": "openvas-report.xml"
}
```

**Request Fields:**
- `name`: Asset name/identifier (required, string)
- `type`: Asset type (required, string). The types are a fixed set, for now only `Host`; a case variant such as `host` is stored as `Host`, any other type is refused with `400`. Assets of another type stored before the set was fixed are kept as they are and reported in a startup warning; they go with their source. An asset's type is set when it is created and never changes: there is no way to change an asset (AM-DATA-06)
- `sourceId`: ResourceManager id of the source the asset came from (required, integer ≥ 1; without it the request is refused with `400`)
- `sourceLabel`: The source's filename (string). (The `"filename (ID: N)"` string once accepted as `source` is no longer read.)
- `userId`: The owner, required unless the `X-User-ID` header names it: naming no owner, or `0`, is refused with `400` (`userId is required: a service must name the owner`), and so is an owner SystemManager does not know (`userId names no user`) — such records would be seen by no one and held to no limit (AM-SEC-13)
- `lastSeen`: When the source saw the asset (optional, ISO 8601)
- `scanCoverageVerifiedAt`: When the source verified that the asset is part of a scheduled vulnerability scan (optional, ISO 8601)
- `scanCoverageIntervalDays`: Shortest interval, in whole days, of a scheduled vulnerability scan that includes the asset (optional, integer ≥ 1)

The two scan coverage values are **only ever set** this way, by an import or another client; AssetManager never computes or derives them from findings, `lastSeen` or anything else. They are kept per source: a later request from the same source replaces that source's values, and leaving a field out keeps its earlier value. The asset's `lastScanCoverageVerification` is the newest verification over its sources and `scanCoverageIntervalDays` the shortest interval; when a source is removed, its values go with it. A value that cannot be read (or an interval below 1) is ignored with a warning, without failing the asset.

**Response:**
```json
{
  "id": 1,
  "name": "192.168.1.1",
  "type": "Host",
  "sources": [{"sourceId": 15, "sourceLabel": "openvas-report.xml", "lastSeen": "2026-01-07T10:30:00Z"}],
  "sourcesLabel": "openvas-report.xml",
  "userId": 5,
  "createdAt": "2026-01-07T10:30:00Z",
  "modifiedAt": "2026-01-07T10:30:00Z"
}
```

**Status Codes:**
- `201 Created`: Asset successfully created
- `200 OK`: The asset already existed and is now linked to this source as well
- `400 Bad Request`: Invalid request body, missing required fields (`sourceId` among them), or no owner named
- `401 Unauthorized`: Missing or invalid internal service key
- `403 Forbidden`: A user's request (records are created only by importing a source), or the host asset limit is reached
- `500 Internal Server Error`: Database error

**Example:**
```bash
# Internal service request (requires internal service key)
curl -X POST \
  -H "X-Internal-Service-Key: YOUR_INTERNAL_SERVICE_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "userId": 5,
    "name": "192.168.1.1",
    "type": "Host",
    "sourceId": 15,
    "sourceLabel": "openvas-report.xml"
  }' \
  http://localhost:8081/api/assets
```

**Notes:**
- Only a service creates records (SourceImporter, for an import); include `userId` and `sourceId` in the request body
- This endpoint announces an `ASSET_CREATED` WebSocket message to the owner's workspace (see [Who receives which message](#who-receives-which-message))

---

#### Delete Assets by Source

When ResourceManager deletes a source: removes its links and the assets left
without one (internal only; a user is refused with `403`).

**Request:**
```
DELETE /api/assets/by-source?source=<SOURCE_REF>
X-Internal-Service-Key: <KEY>
```

**Query Parameters:**
- `source`: Source reference (required, string)

**Response:**
```json
{
  "deleted": 3,
  "deletedIds": [40, 41, 42],
  "updated": 1,
  "updatedIds": [17],
  "skipped": 0,
  "skippedIds": [],
  "message": "Deleted 3 asset(s)"
}
```

**Response Fields** (the same for every deletion and unlinking, see below):
- `deleted`, `deletedIds`: the assets that are gone
- `updated`, `updatedIds`: the assets that remain because another source still reports them, recomputed without this one
- `skipped`, `skippedIds`: always empty here
- `message`: deprecated, sent for one release more

**Status Codes:**
- `200 OK`: Assets successfully deleted
- `400 Bad Request`: Missing `source` query parameter, or one without `(ID: N)`
- `401 Unauthorized`: Missing or invalid internal service key
- `403 Forbidden`: A user's request: records go only with their source
- `500 Internal Server Error`: Database error

**Example:**
```bash
# Internal service request (requires internal service key)
curl -X DELETE \
  -H "X-Internal-Service-Key: YOUR_INTERNAL_SERVICE_KEY" \
  'http://localhost:8081/api/assets/by-source?source=openvas-report.xml%20(ID:%2015)'
```

**Notes:**
- Only a service calls it: ResourceManager, when it deletes the source (the sources' sweep does the same for a source deleted while AssetManager did not answer)
- The source's links of every owner are removed, and the assets left without one deleted
- This endpoint announces an `ASSET_DELETED` WebSocket message for each deleted asset, to that asset's owner's workspace
- URL-encode the `source` parameter in curl commands (spaces become `%20`, colons become `%3A`)

---

#### List Vulnerabilities

Retrieves all vulnerabilities for the authenticated user.

**Request:**
```
GET /api/vulnerabilities
Authorization: Bearer <JWT_TOKEN>
```

**Response:**
```json
{
  "vulnerabilities": [
    {
      "id": 1,
      "name": "CVE-2024-1234",
      "host": "192.168.222.131",
      "port": "80/tcp",
      "severity": 75,
      "userId": 5,
      "createdAt": "2026-01-20T10:30:00Z",
      "modifiedAt": "2026-01-20T10:30:00Z"
    }
  ],
  "total": 1
}
```

**Response Fields:**
- `vulnerabilities`: Array of vulnerability objects (may be `null` if no vulnerabilities exist)
- `total`: Total number of vulnerabilities

**Vulnerability Object:**
- `id`: Unique vulnerability identifier (integer)
- `name`: Short name/identifier for the vulnerability (string)
- `host`: Host IP address or hostname (string)
- `port`: Port specification like "80/tcp" (string)
- `severity`: Severity score 0-100 (integer, divide by 100 to get CVSS score)
- `userId`: User ID who owns this vulnerability (integer)
- `createdAt`: Creation timestamp (ISO 8601)
- `modifiedAt`: Last modification timestamp (ISO 8601)

**Status Codes:**
- `200 OK`: Success
- `401 Unauthorized`: Missing or invalid JWT token
- `500 Internal Server Error`: Database error

**Example:**
```bash
# Regular user request (requires JWT token)
curl -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  http://localhost:8081/api/vulnerabilities
```

**Notes:**
- Vulnerabilities are ordered by severity (descending) then by creation date (descending)
- Severity is stored as integer 0-100, where CVSS = severity / 100

---

#### Findings and their host asset

A finding — a vulnerability or a detection — is evidence of its host, so
every finding has the host asset of its owner (AM-DATA-13). Creating a
finding reports that asset as `POST /api/assets` would: a Host asset named by
the finding's `host` is created when missing, and linked to the finding's
source (or touched, when the link exists). So:

- a routine may return findings without listing their hosts;
- deleting a source, or a re-import, removes an asset only together with the
  last finding on it, as it removes an asset only with its last link;
- a finding on a new host is held to the host asset limit: at the limit it is
  refused with `403` (`Host asset limit reached`), while SystemManager cannot
  be asked with `503`. Findings on hosts that have their asset are not
  affected.

Migration v7 created the host assets that findings stored before this rule
were missing, linked to their sources and without applying the limits.

#### Create Vulnerability

Creates a vulnerability for an import (internal only), and reports its host
asset (see [Findings and their host asset](#findings-and-their-host-asset)).

**Request:**
```
POST /api/vulnerabilities
X-Internal-Service-Key: <KEY>
Content-Type: application/json
```

**Request Body:**
```json
{
  "userId": 5,
  "name": "CVE-2024-1234",
  "host": "192.168.222.131",
  "port": "80/tcp",
  "severity": 75,
  "sourceId": 15,
  "sourceLabel": "openvas-report.xml"
}
```

**Request Fields:**
- `name`: Short name/identifier for the vulnerability (required, string)
- `host`: Host IP address or hostname (required, string)
- `port`: Port specification like "80/tcp" (required, string)
- `severity`: Severity score 0-100 (required, integer, CVSS * 100)
- `sourceId`, `sourceLabel`: The source, as for an asset (`sourceId` required; see [Findings and their source](#findings-and-their-source))
- `userId`: The owner, required unless the `X-User-ID` header names it: naming no owner, or `0`, is refused with `400` (`userId is required: a service must name the owner`), and so is an owner SystemManager does not know (`userId names no user`) — such records would be seen by no one and held to no limit (AM-SEC-13)

**Response:**
```json
{
  "id": 1,
  "name": "CVE-2024-1234",
  "host": "192.168.222.131",
  "port": "80/tcp",
  "severity": 75,
  "userId": 5,
  "createdAt": "2026-01-20T10:30:00Z",
  "modifiedAt": "2026-01-20T10:30:00Z"
}
```

**Status Codes:**
- `201 Created`: A new vulnerability
- `200 OK`: A vulnerability of the same owner, source, host, port and name, reported again (see [One record per finding](#one-record-per-finding))
- `400 Bad Request`: Invalid request body, missing required fields (`sourceId` among them), severity out of range, or no owner named
- `401 Unauthorized`: Missing or invalid internal service key
- `403 Forbidden`: A user's request (records are created only by importing a source), or a limit is reached
- `500 Internal Server Error`: Database error

**Example:**
```bash
# Internal service request (requires internal service key)
curl -X POST \
  -H "X-Internal-Service-Key: YOUR_INTERNAL_SERVICE_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "userId": 5,
    "name": "CVE-2024-1234",
    "host": "192.168.222.131",
    "port": "80/tcp",
    "severity": 75,
    "sourceId": 15,
    "sourceLabel": "openvas-report.xml"
  }' \
  http://localhost:8081/api/vulnerabilities
```

**Notes:**
- Only a service creates records; include `userId` and `sourceId` in the request body
- Severity must be between 0 and 100 (inclusive)
- To convert CVSS score to severity: `severity = CVSS * 100`
- To convert severity to CVSS: `CVSS = severity / 100.0`
- This endpoint announces a `VULNERABILITY_CREATED` WebSocket message to the owner's workspace (see [Who receives which message](#who-receives-which-message))

---

### Authentication and request limits

Every `/api` request authenticates in one of two ways:

- **A user** sends a session token issued by SystemManager as `Authorization: Bearer <token>`. It must be signed with HS256 under the shared `auth.jwt_secret` and carry an expiry. SystemManager's narrow tokens — the MFA challenge issued after the password, the MFA enrolment token and the first-password token — carry a user id as well but are refused (`401`): they are no session.
- **A service** sends the internal service key as `X-Internal-Service-Key` (compared in constant time). `/api/internal` accepts nothing else.

A user's account must still be active: AssetManager asks SystemManager (`GET /api/internal/users/{id}`) and remembers the answer for 30 seconds, so a deactivated or deleted account is refused with `403` within that time, as on SystemManager itself. When SystemManager cannot be asked, the request is refused with `503` rather than let through. The WebSocket handshake applies the same checks.

Request bodies are limited to 1 MiB (32 MiB for `POST /api/assets/scan-coverage`); a larger declared body is refused with `413`.

Creating records is limited by what SystemManager states for the owner: `maxHostAssets`, `maxVulnerabilities` and `maxDetections` (0 is no limit), and on an appliance also by the licensed number of host assets. A new record beyond its limit is refused with `403`. Reporting an existing host asset again creates nothing and is never refused for a limit. When SystemManager cannot be asked for the limits, a new host asset, vulnerability or detection is refused with `503` — a limit that cannot be read cannot be kept — and the caller tries again. When only the licence cannot be read (AIManager), creating goes ahead. See AM-DATA-33 to -35 and AM-DATA-44 in [spec/02-data-model-and-behaviour.md](spec/02-data-model-and-behaviour.md#8-limits).

### CORS

The API supports Cross-Origin Resource Sharing (CORS) for configured origins.

**Allowed Origins** (configurable in `config.yaml` under `cors.allowed_origins`):
- `http://localhost:5173` (Vite default)
- `http://localhost:3000` (React default)

**Allowed Methods:**
- GET, POST, PUT, PATCH, DELETE, OPTIONS

**Allowed Headers:**
- Content-Type, Authorization

Every answer carries `Vary: Origin`, since it depends on the requesting origin.

The browser's `Origin` must be present in `cors.allowed_origins`; this also applies to the WebSocket `/ws` handshake (see below). A request from a disallowed origin is rejected with HTTP 403.

## WebSocket API

### Endpoint

```
ws://localhost:8081/ws
```

### Authentication

The `/ws` endpoint requires authentication, checked as for the API (see [Authentication and request limits](#authentication-and-request-limits)). An unauthenticated handshake, or one with a token that is no session, is rejected with HTTP 401; a deactivated or deleted account, or a handshake whose `Origin` is not in `cors.allowed_origins`, with HTTP 403.

- **Browser clients** authenticate by passing the user's JWT as the WebSocket subprotocol. Browsers cannot set the `Authorization` header on a WebSocket, and the subprotocol keeps the token out of the URL and logs.
- **Internal service-to-service clients** (Go daemons) authenticate by sending the shared internal-service key as the `X-Internal-Service-Key` request header.

### Connection

```javascript
// Browser client: pass the user's JWT as the WebSocket subprotocol
const token = localStorage.getItem('authToken');
const ws = new WebSocket('ws://localhost:8081/ws', [token]);
```

### Message Format

All messages are JSON objects with the following structure:

```json
{
  "type": "MESSAGE_TYPE",
  "payload": { ... }
}
```

### Message Types

#### From Server to Client

Every change to a record is announced. A created record is sent whole; a change or deletion sends only its id, and the client reads the record again. The messages are specified in [spec/03-api.md](spec/03-api.md#3-the-websocket).

| Type | Payload | Sent when |
|---|---|---|
| `ASSET_CREATED` | the asset, as `GET /api/assets/{id}` returns it | an asset is created, by a request or a coverage table |
| `ASSET_UPDATED` | `{"id"}` | an asset is reported again, its derived values or coverage change, or it loses a source |
| `ASSET_DELETED` | `{"id"}` | an asset is deleted |
| `VULNERABILITY_CREATED` | the vulnerability, as listed | a vulnerability is created |
| `VULNERABILITY_UPDATED` | `{"id"}` | a vulnerability loses its source |
| `VULNERABILITY_DELETED` | `{"id"}` | a vulnerability is deleted |
| `DETECTION_CREATED` | the detection, as listed | a detection is created |
| `DETECTION_UPDATED` | `{"id"}` | a detection loses its source |
| `DETECTION_DELETED` | `{"id"}` | a detection is deleted |
| `PONG` | `null` | in answer to a client's `PING` |

```json
{"type": "ASSET_DELETED", "payload": {"id": 1}}
```

#### Who receives which message

Each message about a record goes to the connections of the users entitled to it — the record's owner and the members of the owner's shared workspace who are connected (on an appliance, every user) — and to every service connection, and to no other user's connection. This holds for id-only messages as well, and whether a user or a service made the change, e.g. when a source's records are deleted for all their owners. The workspace a connection sees is the one at connection time. See AM-SEC-19 in [spec/04-security-and-tenancy.md](spec/04-security-and-tenancy.md#5-the-websocket).

#### From Client to Server

**PING**
Keep-alive message from client.

```json
{
  "type": "PING",
  "payload": null
}
```

### Connection Management

- The server automatically removes disconnected clients
- Clients should implement reconnection logic
- Send PING messages periodically to keep the connection alive
- Maximum message size: 512 bytes

### Example Client (JavaScript)

```javascript
const token = localStorage.getItem('authToken');
const ws = new WebSocket('ws://localhost:8081/ws', [token]);

ws.onopen = () => {
  console.log('Connected to AssetManager');

  // Send periodic PING
  setInterval(() => {
    ws.send(JSON.stringify({ type: 'PING', payload: null }));
  }, 30000);
};

ws.onmessage = (event) => {
  const message = JSON.parse(event.data);

  switch (message.type) {
    case 'ASSET_CREATED':
      console.log('New asset:', message.payload);
      break;
    case 'ASSET_UPDATED':
      console.log('Updated asset:', message.payload);
      break;
    case 'ASSET_DELETED':
      console.log('Deleted asset ID:', message.payload.id);
      break;
    case 'VULNERABILITY_CREATED':
      console.log('New vulnerability:', message.payload);
      break;
    case 'VULNERABILITY_UPDATED':
      console.log('Updated vulnerability:', message.payload);
      break;
    case 'VULNERABILITY_DELETED':
      console.log('Deleted vulnerability ID:', message.payload.id);
      break;
    case 'PONG':
      console.log('Server is alive');
      break;
  }
};

ws.onerror = (error) => {
  console.error('WebSocket error:', error);
};

ws.onclose = () => {
  console.log('Disconnected from AssetManager');
  // Implement reconnection logic here
};
```

## Logging

### Log Levels

The server supports four log levels (in order of verbosity):

1. **DEBUG**: Detailed information for debugging (e.g., HTTP requests)
2. **INFO**: General informational messages (e.g., server startup, client connections)
3. **WARN**: Warning messages for potentially harmful situations
4. **ERROR**: Error messages for serious problems

### Log Configuration

Configure logging via `config.yaml` or environment variables:

```yaml
log:
  file_path: /home/tachyon/log/tachyonik-assetmanager.log
  to_console: false  # Log to stdout
  to_file: true      # Log to file
  level: INFO        # Minimum level to log
```

### Log Output Examples

```
2026/01/07 11:41:23 internal/handlers/middleware.go:41: [DEBUG] GET /api/assets 192.168.1.100:54321
2026/01/07 11:41:23 cmd/server/main.go:88: [INFO] WebSocket hub started
2026/01/07 11:41:30 internal/websocket/hub.go:37: [INFO] WebSocket client connected. Total clients: 1
2026/01/07 11:42:15 internal/handlers/handlers.go:62: [ERROR] WebSocket upgrade error: invalid origin
```

### Changing Log Level at Runtime

To change the log level, update the configuration and restart the server:

```bash
# Set to DEBUG for troubleshooting
ASSETMANAGER_LOG_LEVEL=DEBUG ./tachyonik-assetmanager

# Set to ERROR for production (minimal logging)
ASSETMANAGER_LOG_LEVEL=ERROR ./tachyonik-assetmanager
```

## Database

### Schema

The AssetManager uses SQLite with the following schema:

**Table: assets**

| Column | Type | Description |
|--------|------|-------------|
| `id` | INTEGER PRIMARY KEY AUTOINCREMENT | Unique identifier |
| `name` | TEXT NOT NULL | Asset name/identifier |
| `type` | TEXT NOT NULL | Asset type |
| `user_id` | INTEGER NOT NULL | User ID who owns this asset |
| `severity` | INTEGER NOT NULL | Highest severity of the asset's vulnerabilities (0-100) |
| `score` | INTEGER NOT NULL | `100 - severity`; 100 with only zero-severity findings or detections; 0 with no findings at all (unmanaged) |
| `last_seen` | DATETIME NOT NULL | Newest last seen of anything that refers to the asset: any source listing it (`asset_source.last_seen` of a link with `counts_as_seen = 1`), any vulnerability, any detection on its host. Kept as it is when nothing counts — an asset known only from a scan coverage table keeps the table's date |
| `last_scanned` | DATETIME | Host assets only: newest `last_seen` of the host's vulnerabilities and detections; NULL when it has none (added by migration v2) |
| `last_scan_coverage_verification` | DATETIME | Newest scan coverage verification reported by the asset's sources; set by imports only, never derived (migration v3) |
| `scan_coverage_interval_days` | INTEGER | Shortest scan coverage interval reported by the asset's sources, in days; set by imports only, never derived (migration v3) |
| `created_at` | DATETIME NOT NULL | Creation timestamp (default: CURRENT_TIMESTAMP) |
| `modified_at` | DATETIME NOT NULL | Last modification timestamp (default: CURRENT_TIMESTAMP) |

Which sources saw an asset, and when, is kept per source in the `asset_source` table, together with the scan coverage each source reported (`scan_coverage_verified_at`, `scan_coverage_interval_days`). `counts_as_seen` (migration v4, default 1) is 0 for a link made only to record scan coverage: a schedule saying a host would be scanned is not a sighting of it, so such a link does not count toward the asset's `last_seen`. Its `last_seen` is the time the table was applied.

Vulnerabilities and detections hold their source as `source_id` and `source_label` (migration v6, filled from the `"filename (ID: N)"` string the older `source` column held). The `source` column is no longer read or written and is dropped in a later release.

Every vulnerability and detection has the host asset of its owner (AM-DATA-13): migration v7 created the ones findings stored before were missing, linked to each of their sources and dated by the newest finding of that source, without applying the limits. See [Findings and their host asset](#findings-and-their-host-asset).

A vulnerability or detection is unique per owner, source, host, port and name (AM-DATA-47): migration v8 merged the copies stored before into their oldest record and added the unique indexes `idx_vulnerabilities_identity` and `idx_detections_identity`. See [One record per finding](#one-record-per-finding).

Every link also has `modified_at` (migration v5): when an import last touched it, set on every import of the asset whether or not its `last_seen` moved. A re-import drops the links it did not touch by this, not by `last_seen` — that is the date the file says the host was seen, which re-importing the same file leaves where it was. (Judged by `last_seen`, every link of a re-imported file looked stale, and all its assets were deleted while their findings stayed.) When a source lists a host whose link from that source was coverage only, the link becomes a sighting dated by the listing.

#### Scan coverage tables

A scan coverage table (such as the one TachyonikProxy's `gmp-get-scan-coverage` produces) says which targets a scanner scans on a schedule, and how often. SourceImporter sends it as a whole to `POST /api/assets/scan-coverage`, because a network covers whichever host assets fall inside it. Each entry is applied by its target:

- **An address** (`10.0.0.5`): that host asset gets the coverage. When there is none it is created as a `Host` — subject to the host asset limits like any other — with the entry's verification date as its last seen. When SystemManager cannot be asked for the limit, the table is answered with `503` instead of its outcome; what was applied up to then stays, and applying the table again changes nothing more of it.
- **A CIDR network or range** (`10.0.0.0/24`, `10.0.0.1-10.0.0.9`): every existing host asset of the user whose name is an address inside it. No assets are created.
- **A hostname**: skipped, since host assets are named by address only.

An asset covered by several entries gets the shortest interval and the newest verification. The coverage goes on the asset's link to the table's source, as a coverage-only link (`counts_as_seen = 0`) unless that source also listed the asset; the asset's values are then combined over its sources as for any other coverage. None of this moves an asset's `last_seen`.

**Derived values are recomputed from what still refers to the asset.** `severity`, `score`, `last_seen` and `last_scanned` are never entered; `internal/database/asset_refresh.go` computes them by the rules above, and so does every change to an asset's evidence:

- an asset, vulnerability or detection is created (an import),
- a source's vulnerabilities, detections or asset links are deleted (`DELETE …/by-source`, the source being deleted in ResourceManager), also the stale ones a re-import drops (`DELETE …/stale`),
- vulnerabilities or detections are deleted by ID.

The assets recomputed are always their owners' — also on the internal service path, where the request names no user. An asset that keeps other sources therefore moves back to what those say: its last seen to their newest date, its severity and score to the vulnerabilities left. Each asset whose values change is announced with `ASSET_UPDATED`. At startup every asset is recomputed once, which puts right anything an older version left behind.

**Indexes:**
- `idx_assets_created_at`: Index on `created_at DESC`
- `idx_assets_modified_at`: Index on `modified_at DESC`
- `idx_assets_user_id`: Index on `user_id`

**Table: vulnerabilities**

| Column | Type | Description |
|--------|------|-------------|
| `id` | INTEGER PRIMARY KEY AUTOINCREMENT | Unique identifier |
| `name` | TEXT NOT NULL | Short name/identifier for the vulnerability |
| `host` | TEXT NOT NULL | Host IP address or hostname |
| `port` | TEXT NOT NULL | Port specification (e.g., "80/tcp") |
| `severity` | INTEGER NOT NULL | Severity score 0-100 (CVSS * 100) |
| `user_id` | INTEGER NOT NULL | User ID who owns this vulnerability |
| `created_at` | DATETIME NOT NULL | Creation timestamp (default: CURRENT_TIMESTAMP) |
| `modified_at` | DATETIME NOT NULL | Last modification timestamp (default: CURRENT_TIMESTAMP) |

**Constraints:**
- `severity`: CHECK constraint ensuring value is between 0 and 100

**Indexes:**
- `idx_vulnerabilities_user_id`: Index on `user_id`
- `idx_vulnerabilities_host`: Index on `host`
- `idx_vulnerabilities_severity`: Index on `severity DESC`
- `idx_vulnerabilities_created_at`: Index on `created_at DESC`

### Migrations

Database migrations run automatically on server startup. The database file is created if it doesn't exist.

| Version | Change |
|---|---|
| 1 | Baseline schema |
| 2 | `assets.last_scanned` |
| 3 | Scan coverage verification and interval, per source (`asset_source`) and per asset |
| 4 | `asset_source.counts_as_seen` (default 1): whether a link counts toward the asset's last seen |
| 5 | `asset_source.modified_at`: when an import last touched a link (existing links: their `created_at`) |

`MaxSupportedSchemaVersion` in `internal/database/migration_runner.go` must be raised with every new migration — a binary refuses to start on a database whose version is higher, so leaving it behind makes the binary refuse the database it migrated itself. A test pins it to the newest migration.

### Backup

To backup the database:

```bash
# Stop the server first
sqlite3 /home/tachyon/data/AssetManager.db ".backup /path/to/backup.db"
```

## Troubleshooting

### Server won't start

1. Check if the port is already in use:
   ```bash
   lsof -i :8081
   ```

2. Verify database directory exists and is writable:
   ```bash
   ls -ld /home/tachyon/data/
   ```

3. Check log file for errors:
   ```bash
   tail -f /home/tachyon/log/tachyonik-assetmanager.log
   ```

### Cannot connect to WebSocket

1. Verify the server is running and WebSocket endpoint is active
2. Check CORS configuration if connecting from a web application
3. Ensure firewall allows WebSocket connections
4. If the client shows "Disconnected" and the `/ws` handshake returns **401**, the JWT is missing or invalid — browser clients must pass it as the WebSocket subprotocol (`new WebSocket(url, [token])`) and internal clients must send the `X-Internal-Service-Key` header. A **403** means the request's `Origin` is not in `cors.allowed_origins`.

### Database is locked

SQLite can lock if multiple processes try to write simultaneously. Ensure only one AssetManager instance is running.

## Architecture

- **HTTP Server**: gorilla/mux router with middleware for CORS and logging
- **Database**: SQLite with sqlx for structured queries
- **WebSocket**: gorilla/websocket with hub-client pattern for broadcasting
- **Logging**: Custom logger with configurable levels and multi-writer support
- **Configuration**: YAML-based with environment variable overrides
