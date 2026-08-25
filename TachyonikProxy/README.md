<!--
TachyonikProxy
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# TachyonikProxy

TachyonikProxy is a standalone, cross-platform MCP (Model Context Protocol) server that runs inside a customer network and exposes locally installed security tools (Nmap, Nikto, sqlmap, …) and upstream MCP servers to the Tachyonik platform. It is delivered as a self-contained binary plus deployment packages (`.deb`, `.rpm`, `.pkg`, `.msi`, `.tar.gz`, `.zip`) and connects to the platform's `ToolManager` over mutual TLS — either by accepting inbound connections (outbound mode) or by dialing out (inbound / reverse-connect mode).

```
┌───────────────────────┐         mTLS         ┌────────────────────────┐
│   Customer Network    │◄────────────────────►│   Tachyonik Platform   │
│                       │                      │                        │
│   TachyonikProxy      │   JSON-RPC / SSE     │   ToolManager          │
│   ├── nmap            │   JSON-RPC / WS      │   ├── ChatAI           │
│   └── nikto           │                      │   └── WebUI            │
│                       │                      │                        │
└───────────────────────┘                      └────────────────────────┘
```

## Table of Contents

- [Building](#building)
- [Deployment Packages](#deployment-packages)
- [Configuration](#configuration)
- [Operation](#operation)
- [Subcommands](#subcommands)
- [Tool Detection](#tool-detection)
- [Local Network Scan](#local-network-scan)
- [Tool Execution](#tool-execution)
- [Communication and Data Workflows](#communication-and-data-workflows)
- [MCP Protocol](#mcp-protocol)
- [Logging](#logging)
- [Enrollment](#enrollment)
- [Auto-update](#auto-update)
- [Integration](#integration)
- [Troubleshooting](#troubleshooting)
- [Architecture](#architecture)

## Building

### Prerequisites

- Go 1.24 or later
- GNU Make
- For deployment packages:
  - `nfpm` (`go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.1`) — `.deb` and `.rpm`
  - `pkgbuild` (Xcode Command Line Tools) — macOS `.pkg`
  - `go-msi` plus WiX (Windows host only) — `.msi`
  - `tar`, `zip` — portable archives

### Native build

```bash
cd TachyonikProxy
make build
```

This produces `dist/bin/tachyonikproxy` for the host platform. The version string is resolved from this module's **namespaced** git tag `tachyonikproxy/X.Y.Z` (e.g. `tachyonikproxy/1.0.0` → `1.0.0`), so it is independent of TachyonikCSM and other modules in the monorepo; with no matching tag it falls back to `0.0.0-dev`. It's embedded via `-ldflags '-X main.version=...'`. Override it deterministically with `make build VERSION=1.0.0`.

> **Packaging requires a clean release version.** The `package-*` targets refuse to run unless `VERSION` resolves to a numeric `x.y.z` — a dirty tree (`1.0.0-dirty`), an off-tag build (`1.0.0-3-gabc123`), or the `0.0.0-dev` fallback are all rejected, so a mislabeled artifact can't be published (the proxy's self-update also requires a purely numeric version). Tag the release commit `git tag tachyonikproxy/1.0.0` or pass `VERSION=1.0.0`.

### Cross-compile all platforms

```bash
make build-all
```

Produces statically linked binaries (`CGO_ENABLED=0`) for:

- `linux/amd64`, `linux/arm64`
- `darwin/amd64`, `darwin/arm64`
- `windows/amd64`

### Dependencies

Dependencies are managed via `go.mod`:

- `github.com/gorilla/websocket` — WebSocket transport for inbound mode
- `github.com/dop251/goja` — embedded JavaScript engine for tool-detection routines
- `gopkg.in/yaml.v3` — YAML configuration

## Deployment Packages

TachyonikProxy is released as a single self-contained binary plus a set of OS-native installation packages. All packaging targets read the cross-compiled, unversioned intermediate binaries from `dist/bin/` (their version is embedded via ldflags) and write the versioned packages into `dist/`.

| Target            | Output                                           | Tooling required        |
|-------------------|--------------------------------------------------|-------------------------|
| `package-deb`     | `tachyonikproxy-<ver>-<arch>.deb` (amd64, arm64) | `nfpm`                  |
| `package-rpm`     | `tachyonikproxy-<ver>-<arch>.rpm` (amd64, arm64) | `nfpm`                  |
| `package-macos`   | `tachyonikproxy-<ver>-darwin-<arch>.pkg`         | `pkgbuild`              |
| `package-windows` | `tachyonikproxy-<ver>-windows-amd64.msi`         | `go-msi` + WiX (on Win) |
| `package-archives`| `.tar.gz` (Linux, macOS) and `.zip` (Windows)    | `tar`, `zip`            |
| `package-all`     | All of the above                                 | All of the above        |

### Layout installed by native packages

| Item             | Linux (`.deb` / `.rpm`)                                   | macOS (`.pkg`)                                            | Windows (`.msi`)                                |
|------------------|-----------------------------------------------------------|-----------------------------------------------------------|-------------------------------------------------|
| Binary           | `/usr/bin/tachyonikproxy`                                 | `/usr/local/bin/tachyonikproxy`                           | `C:\Program Files\Tachyonik\TachyonikProxy\`    |
| Default config   | `/usr/share/tachyonik/tachyonikproxy/config.yaml.default` | `/etc/tachyonik/tachyonikproxy/config.yaml.default`       | bundled with installer                          |
| Active config    | `/etc/tachyonik/tachyonikproxy/config.yaml`               | `/etc/tachyonik/tachyonikproxy/config.yaml`               | `C:\ProgramData\Tachyonik\TachyonikProxy\`      |
| Service unit     | `/lib/systemd/system/tachyonikproxy.service`              | `/Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist` | Windows Service (`TachyonikTachyonikProxy`) |
| Logs             | `/var/log/tachyonik/`                                     | `/var/log/tachyonik/`                                     | per-config                                      |

`postinstall` scripts create the `tachyonikproxy` system user (Linux), set up the service unit, and install the default config if no `config.yaml` is already present.

### Install script

`install-tachyonikproxy.sh` is the unattended one-liner installer for Linux and macOS. It detects OS and architecture, downloads the matching `.deb`, `.rpm`, or `.tar.gz` from `https://tachyonik.com/download/proxy/`, installs the service unit, and offers a user-space install path when sudo is unavailable. See `INSTALL-SCRIPT-README.md` for details — keep that file in sync with the script (see CLAUDE.md).

## Configuration

### Configuration file resolution

The binary locates its config file by searching, in order:

1. `TACHYONIKPROXY_CONFIG` environment variable — explicit always wins.
2. `./config.yaml` in the current working directory, if it exists (backward compatibility with CWD-based setups).
3. `${XDG_CONFIG_HOME:-~/.config}/tachyonik/tachyonikproxy/config.yaml`, if it exists — the canonical user-space location created by `install-tachyonikproxy.sh`.
4. `/etc/tachyonik/tachyonikproxy/config.yaml`, if it exists — the system location used by the `.deb`/`.rpm` packages.

When no config file exists anywhere, the first of these becomes the write target for `tachyonikproxy enroll`: the system path when running as root, the user XDG path otherwise. So on a user-space install, both enrollment and a plain `tachyonikproxy` work from any directory without `TACHYONIKPROXY_CONFIG`.

**Relative paths inside the config** (TLS certificates, reverse-connect cert/key, `log.file_path`) resolve against the **config file's directory**, not the process working directory. A config plus its `certs/` subdirectory can therefore be moved as a unit — e.g. from a legacy CWD setup into the canonical location:

```bash
mv config.yaml certs/ ~/.config/tachyonik/tachyonikproxy/
tachyonikproxy   # now works from anywhere
```

### Configuration File

```yaml
server:
  port: 9100
  host: "0.0.0.0"

# Mutual TLS authentication — written by `enroll`, do not edit by hand
# tls:
#   ca_cert: "./certs/ca.crt"
#   server_cert: "./certs/server.crt"
#   server_key: "./certs/server.key"
#   allowed_clients:
#     - "toolmanager"

proxy:
  name: "kali-proxy"
  description: "Kali Linux security tools"

log:
  file_path: "./tachyonikproxy.log"
  to_console: true
  to_file: true
  level: INFO  # One of DEBUG, INFO, WARN, ERROR

# Inbound (reverse-connect) mode is set automatically by `enroll --listen`
# connection_mode: "outbound"  # or "inbound"
# reverse_connect:
#   toolmanager_url: "wss://toolmanager.example.com/proxy/connect"
#   client_cert: "./certs/client.crt"
#   client_key:  "./certs/client.key"

tools:
  - name: "nmap_scan"
    description: "Run an Nmap network scan"
    command: "nmap"
    args_schema: { ... }              # JSON schema for tool arguments
    arg_template: "{{.scan_type}} {{if .ports}}-p {{.ports}}{{end}} {{.target}}"
    timeout: 300                      # seconds
    allowed_exit_codes: [0, 1]
    allowed_chars: "a-zA-Z0-9.:/\\-_, "
    max_output_bytes: 5242880
    max_cpu_seconds: 300
    output_files:
      - arg_flag: "-oX"
        extension: ".xml"
        mime_type: "application/xml"

mcp_servers: []                       # Upstream MCP servers to proxy

allow_remote_config: true             # Allow ToolManager to push tools/mcp_servers
```

### Environment Variables

Environment variables override configuration file values:

| Variable                  | Description                                      | Default            |
|---------------------------|--------------------------------------------------|--------------------|
| `TACHYONIKPROXY_CONFIG`   | Path to the config file                          | auto-detect (see above) |
| `TACHYONIKPROXY_PORT`     | Listening port (outbound) or health port (inbound) | `9100`           |
| `TACHYONIKPROXY_HOST`     | Listening host                                   | `0.0.0.0`          |
| `TACHYONIKPROXY_LOG_LEVEL`| Log level (DEBUG, INFO, WARN, ERROR)             | `INFO`             |

### Configuration Priority

1. Environment variables (highest priority)
2. Configuration file
3. Default values (lowest priority)

## Operation

### Starting the Server

```bash
./tachyonikproxy
```

On startup the proxy:

1. Locates and loads its config file (see "Configuration file resolution") and applies environment overrides.
2. Initializes logging (file and/or console per config).
3. Builds the local tool registry from `tools:` and registers any upstream `mcp_servers:`.
4. Initializes the MCP JSON-RPC server.
5. Branches on `connection_mode`:
   - `outbound` (default) — starts an HTTPS+mTLS server with the MCP SSE+HTTP transport on `host:port`. **TLS is mandatory**: the proxy refuses to start unless `tls.ca_cert`, `tls.server_cert`, and `tls.server_key` are all set and readable. Enroll the proxy first (`tachyonikproxy enroll …`).
   - `inbound` — starts a `reverseconnect.Dialer` that opens a persistent WebSocket to `reverse_connect.toolmanager_url`, sends a `register` message with the proxy's name, and serves JSON-RPC requests over that single connection. A localhost-only `/health` endpoint is bound on the configured port for local diagnostics. **TLS is equally mandatory here**: `tls.ca_cert`, `reverse_connect.client_cert` and `reverse_connect.client_key` must be set and readable, checked before the dialer starts so unreadable material is diagnosed once instead of retried indefinitely.
6. Waits for `SIGINT` / `SIGTERM` to shut down gracefully (10 second timeout).

### Stopping the Server

Send `SIGINT` (Ctrl+C) or `SIGTERM`:

- Outbound mode: HTTP server is told to stop accepting connections; outstanding requests get up to 10 s to complete.
- Inbound mode: the dialer is signalled, the WebSocket is closed, and the reconnect loop exits.

### Running with Custom Configuration

```bash
# Custom config path
TACHYONIKPROXY_CONFIG=/path/to/config.yaml ./tachyonikproxy

# Verbose logging
TACHYONIKPROXY_LOG_LEVEL=DEBUG ./tachyonikproxy
```

### Running as a Service

- **Linux (systemd)**: `sudo systemctl start tachyonikproxy`
- **macOS (launchd)**: `sudo launchctl load /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist`
- **Windows**: `net start TachyonikTachyonikProxy`

## Subcommands

The same binary handles operational subcommands. Flags may appear before or after the subcommand.

| Command                         | Purpose                                                                 |
|---------------------------------|-------------------------------------------------------------------------|
| *(none)*                        | Start the proxy server                                                  |
| `enroll <enrollment-url>`       | Online enrollment — POST to Tachyonik to fetch TLS material             |
| `enroll --listen [--san <names>]` | Reverse enrollment — wait for Tachyonik to dial in and complete the handshake |
| `reset-enrollment [--force]`    | Delete on-disk certs and clear `tls.*`, `reverse_connect.*`, `connection_mode`, `proxy.name` |
| `scan [--json]`                 | Run the local tool-detection scanner and print results                  |
| `netscan [--json]`              | Sweep the local network over HTTPS and list what answered               |
| `self-update`                   | Check + apply an auto-update; rolls back automatically on health failure |
| `self-update --dry-run`         | Check the auto-update manifest and report what an apply would do        |
| `self-update --status`          | Print the local `update-state.json` summary                              |
| `self-update --bootstrap-layout`| Migrate this install to the versioned-symlink layout (one-shot)         |
| `self-update --reset-rollback-record` | Clear the sticky list of rolled-back versions                     |
| `version`                       | Print version and exit                                                  |
| `help` / `--help` / `-h`        | Show usage                                                              |

Common enrollment flags: `--config <path>`, `--cert-dir <path>`, `--port <port>`, `--insecure` (online only, skip TLS verification), `--force` (skip the "already enrolled" prompt).

## Tool Detection

Tool detection is **driven from ToolManager**, not from a hard-coded list inside the proxy. Detection rules are JavaScript routines that ship from the platform and execute inside an embedded `goja` interpreter on the proxy host.

### How it works

1. **ToolManager** sends a `tools/scan` JSON-RPC request to the proxy with a list of `RoutineInput { name, code, version, sha256 }` entries.
2. **Integrity check**: for each routine the proxy computes `SHA-256(code)` and rejects any routine whose SHA does not match the declared digest. This prevents tampering on the wire and makes routine versions verifiable.
3. **Sandboxed execution**: each routine runs in a fresh `goja` VM with these host helpers exposed:
   - `which(name)` — wraps `exec.LookPath`; returns the absolute path of `name` on `$PATH`, or `""`.
   - `exec(cmd, args)` — runs a command and returns `{ output, exitCode }` (combined stdout/stderr; `exitCode = -1` if the command was not found).
   - `httpGet(url, options)` — a single bounded HTTPS/HTTP GET returning `{ status, body, error }`. Options: `headers`, `timeout` (ms, capped at 30 s), `skipTLS`. Redirects are not followed.
   - `netscan` — a read-only view of the periodic local-network sweep. See [Local Network Scan](#local-network-scan).
4. The routine declares a global `rules` array; each rule has `name`, `description`, and a `detect()` function. The proxy iterates `rules`, calls `detect()`, and collects:
   - `null` / `undefined` → not detected.
   - object `{ version, path, description, host? }` → one detection. If `host` is omitted the proxy fills it with its own primary IPv4 address.
   - **array** of those objects → multiple detections from a single rule (e.g. an HTTP probe that finds the same tool on several endpoints). One `ToolResult` is emitted per array element.
   - thrown exception → reported as an error in the result.
5. The proxy returns one `ToolResult` per detection — detected, not-detected, or errored — so ToolManager can show negative results and surface SHA-256 / JS-execution failures. Each detected result carries `host` and `toolOverviewId`: the latter is the catalogue identity copied from the originating AIManager `scan_rule.tool_overview_id` and is what ResourceManager stores as `tools.tool_id`. The `scan` CLI subcommand uses the same scanner with no input routines for offline diagnostics.

### Inspecting the sweep from the command line

```bash
tachyonikproxy netscan          # banner table
tachyonikproxy netscan --json   # full records, including response bodies
```

```
Network 192.168.178.0/24 · 3 responded · 22.4s

HOST            PORT   STATUS  TLS        SERVER  TITLE
192.168.178.1   443    200     trusted    nginx   FRITZ!Box 7590
192.168.178.42  443    200     untrusted  gsad    OPENVAS SCAN
192.168.178.77  443    200     untrusted  gsad    OPENVAS SCAN
```

This performs its **own** one-shot sweep rather than reading a running daemon's
snapshot — that snapshot lives in the daemon's memory and is deliberately never
written to disk, so a second process has nothing to read. The practical
consequences: the command works with no daemon running (useful for checking a
network before deployment), it costs a full sweep (~24 s at the defaults), and
`netscan.enabled: false` does not suppress it, since running the command is an
explicit request rather than background behaviour.

Progress goes to stderr and the table to stdout, so `netscan --json` pipes
cleanly. A range the proxy may not sweep is reported on stderr with exit
status 1.

### Local `scan` subcommand

```bash
tachyonikproxy scan          # human-readable
tachyonikproxy scan --json   # machine-readable, suitable for piping
```

The CLI form is mainly a debugging aid: with no routines supplied, it shows what the proxy would return if ToolManager pushed an empty routine set — i.e. an empty list. The intended source of truth for detection is the routine library managed by ToolManager.

## Local Network Scan

Some tools are recognised not by a binary on this host but by a web interface on
a neighbouring one. Probing for those from inside a detection routine does not
work: a `/24` is 254 addresses, each `exec("curl", …)` is synchronous, and a
routine has a 30-second budget. The sweep would also be repeated by every rule
that needed it.

So the proxy does the probing itself, on a schedule, and routines match against
the collected responses.

### What it does

Once at startup and then every `interval_minutes`, the proxy issues an HTTPS
`GET /` to every address in its local network on each configured port,
concurrently, and caches what came back. Certificate validation is deliberately
not required — appliances routinely ship self-signed or expired certificates,
and the goal is to find them. Whether the certificate *would* have validated is
recorded per host as `tlsTrusted`.

The sweep runs in its own goroutine. It never blocks the MCP server, and
routines only ever read the last completed snapshot.

### The range is always private

The network is derived from this host's primary IPv4 as a `/24`, or set
explicitly with `netscan.network`. Either way it must fall inside `10/8`,
`172.16/12`, `192.168/16` or `127/8`, and be no wider than `/22`. A proxy
holding a public address logs that netscan is disabled and carries on — it will
not sweep its neighbours.

This is why a detection rule does **not** need to check that it is on a private
network before scanning: the platform guarantees it.

### The `netscan` JS API

```js
netscan.info()                     // { network, ready, scanning, lastScan, durationMs, hostCount }
netscan.hosts()                    // every address that responded
netscan.find(substring)            // hosts whose body contains substring
netscan.findMatching(regexString)  // hosts whose body matches the pattern
netscan.get(ip[, port])            // one host record, or null
```

Each host record:

```js
{ ip: "192.168.178.42", port: 443,
  url:      "https://192.168.178.42:443/",       // what was probed
  finalUrl: "https://192.168.178.42:443/login",  // where it ended up
  status: 200, body: "…", title: "OPENVAS SCAN",
  headers: { "server": "gsad", "location": "…" },
  tlsTrusted: false,
  certSubject: "CN=gsad", certIssuer: "CN=Greenbone",
  certDnsNames: ["openvas.local"], certNotAfter: "2027-04-01T00:00:00Z",
  error: "" }
```

`title` is the `<title>` text, extracted for you — it is the marker most web
interfaces are identified by.

**Redirects are followed, but only back to the same address.** An appliance that
answers `/` with a `302` to `/login`, or redirects `443` to a product port such
as `9392`, is followed and the real page is what gets stored; `finalUrl` shows
where it landed. A redirect that leaves the probed address is *not* followed —
that would reach a host the sweep never selected and attribute its page to this
record — so the `3xx` and its `location` header are stored instead, which is
still a usable signal. Chains are bounded at 3 hops.

**When the body is useless, use the certificate.** A service behind an
authentication wall, or one whose root serves an empty shell that fills itself
in via JavaScript, still presents a certificate. `certSubject`, `certIssuer` and
`certDnsNames` frequently name the product outright and are often the only thing
worth matching on for such a host.

**Always check `netscan.info().ready` first.** It is `false` until the first
sweep completes, and an empty host list then means "not looked yet", not "not
present" — a routine that skips this check reports every network tool absent
for the first minute after the proxy starts.

### Example

Detecting an OpenVAS/Greenbone web interface anywhere on the local network:

```js
var rules = [{
  name: "OPENVAS SCAN",
  description: "OpenVAS/Greenbone web interface on the local network",
  detect: function () {
    if (!netscan.info().ready) return null;

    var hits = netscan.find("<title>OPENVAS SCAN</title>");
    if (hits.length === 0) return null;

    var out = [];
    for (var i = 0; i < hits.length; i++) {
      out.push({
        name: "OPENVAS SCAN",
        host: hits[i].ip,
        version: "unknown",
        description: "Detected via HTTPS at " + hits[i].url
      });
    }
    return out;
  }
}];
```

Returning an array produces one `ToolResult` per discovered host, each keeping
the address it was found at.

### Configuration

| Setting | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Sweep at startup and on each interval |
| `interval_minutes` | `60` | Time between sweeps |
| `network` | `""` | Explicit CIDR; empty derives the `/24` from the primary IPv4 |
| `ports` | `[443]` | Ports probed per address; each one multiplies the sweep |
| `concurrency` | `32` | Simultaneous probes — the main tuning knob |
| `timeout_seconds` | `3` | Per-probe timeout |
| `max_body_bytes` | `65536` | Response bytes cached per host |
| `max_scan_duration_minutes` | `10` | Hard stop for one sweep |

`concurrency` and `timeout_seconds` set the wall-clock cost: a `/24` where
nothing answers takes roughly `254 / concurrency × timeout_seconds` — about 24
seconds at the defaults. Raising concurrency shortens that but uses more file
descriptors and makes the sweep more conspicuous to network monitoring; 8 is a
reasonable conservative value.

### Operational notes

- **This is active scanning.** Every address in the range is connected to on
  each cycle. On a monitored network that can raise IDS alerts — set
  `enabled: false` if that is unwanted.
- **Cached bodies live in memory only.** They are bounded, replaced on each
  sweep, never written to disk, and never sent to ToolManager wholesale; only
  what a routine returns leaves the proxy.
- Routines supplied by ToolManager can read those cached bodies. That is the
  point of the feature, but it does mean a routine sees page content from hosts
  on the local network.

## Tool Execution

For tools listed under `tools:` in `config.yaml`, the proxy is itself the executor. When a `tools/call` request arrives:

1. The named tool is looked up in the local registry.
2. Arguments are validated against `args_schema` (basic JSON-schema check, including `enum` membership) and the optional `allowed_chars` allowlist (regex character class). Any character outside the allowlist rejects the call. A flag-injection guard additionally rejects any value whose whitespace-separated fields begin with `-` (except `enum`-constrained fields), so an argument cannot smuggle extra command-line flags.
3. The shell-free argv is rendered from `arg_template` (Go `text/template`) using the supplied arguments.
4. If `output_files` are configured, the proxy creates a temp file per output, substitutes its path into the template's `arg_flag`, and reads the file back after execution.
5. The command is executed with `os/exec` under context-bound limits:
   - `timeout` (seconds, default 120). The tool runs in its own process group; on timeout the whole group is killed.
   - `max_output_bytes` (default 10 MiB; combined stdout+stderr, bounded as it streams)
   - `max_cpu_seconds`, `max_memory_mb` (POSIX `setrlimit` where supported)
   - `max_processes`, `max_file_size_mb` (optional `RLIMIT_NPROC` / `RLIMIT_FSIZE`, Linux-only; `0` = unlimited)
   - `allowed_exit_codes` — non-listed exit codes flag the result as `isError`.

   The command also runs with a sanitized environment (a small allowlist; `PATH` always present), so executed tools never inherit the proxy's secrets.
6. Output files are returned as base64 `resource` content blocks alongside the textual stdout in the MCP `tools/call` response.

For **upstream MCP servers** listed under `mcp_servers:`, the proxy advertises their tools alongside its own and forwards `tools/call` requests transparently.

## Communication and Data Workflows

TachyonikProxy supports two mutually exclusive connection modes. The mode is decided at enrollment time and recorded in `config.yaml` as `connection_mode`.

### Outbound mode (default)

```
┌──────────────┐         HTTPS + mTLS (server-side)        ┌──────────────┐
│ ToolManager  │ ─── GET /mcp/sse ───────────────────────► │              │
│              │ ◄── event: endpoint /mcp/message?...      │ TachyonikProxy │
│              │ ─── POST /mcp/message {jsonrpc} ────────► │              │
│              │ ◄── event: message {jsonrpc result} ───── │              │
└──────────────┘                                            └──────────────┘
```

- The proxy binds an HTTPS listener on `server.host:server.port` (default `0.0.0.0:9100`).
- mTLS is mandatory: `tls.ca_cert`, `tls.server_cert`, and `tls.server_key` must all be configured. The proxy will not start in outbound mode without them. Client certificates are accepted only if their CN/DNS matches `tls.allowed_clients`, which must be **non-empty** — the proxy refuses to start with an empty `allowed_clients` rather than accepting any CA-signed certificate.
- The MCP transport is **SSE+HTTP**:
  - `GET /mcp/sse` — Server-Sent Events stream (server → client). The first event is `event: endpoint` carrying the per-session URL the client must POST requests to.
  - `POST /mcp/message?sessionId=…` — JSON-RPC requests (client → server). Responses are pushed back over the SSE channel of that session.
- `GET /health` returns `{"status":"ok","mode":"outbound"}` for liveness probes.
- Suitable when ToolManager can reach the proxy host.

### Inbound mode (reverse-connect)

```
┌──────────────┐                                            ┌──────────────┐
│              │ ◄── WSS (mTLS, client-side) ────────────── │              │
│              │     {type:"register", proxyName}           │              │
│ ToolManager  │ ─── {jsonrpc request} ──────────────────► │ TachyonikProxy │
│              │ ◄── {jsonrpc response} ────────────────── │              │
│              │     ping every 30 s                        │              │
└──────────────┘                                            └──────────────┘
```

- The proxy dials `reverse_connect.toolmanager_url` (a `wss://` URL) using its client certificate and the platform CA. Handshake timeout is 30 s.
- After the WebSocket is up, the proxy sends a `register` message announcing its `proxyName`. ToolManager attaches the connection to the named proxy.
- All MCP traffic is multiplexed over this single WebSocket as JSON-RPC frames. The proxy reads requests, dispatches them to the local MCP server, and writes responses back.
- The dialer pings every 30 s and reconnects with exponential backoff (5 s → 60 s) on any failure, indefinitely until `SIGINT`/`SIGTERM`.
- A localhost-only `/health` endpoint is exposed for local diagnostics (`{"status":"ok","mode":"inbound"}`); no external listener is opened.
- Suitable for strict-egress networks where inbound connections to the proxy host are not permitted.

### Data flow during a tool call

The same data flow applies in both modes; only the transport changes.

1. ChatAI decides to invoke a tool and asks ToolManager.
2. ToolManager sends `tools/call { name, arguments }` to the target proxy.
3. The proxy validates arguments, renders the argv, executes the local command (or forwards to an upstream MCP server), enforces resource limits, and reads any declared output files.
4. The proxy returns a `ToolCallResult` with stdout as a `text` content block and each output file as a base64 `resource` content block.
5. ToolManager streams the result back to ChatAI for inclusion in the conversation.

## MCP Protocol

The MCP server speaks JSON-RPC 2.0 and supports the following methods. Unknown methods return `-32601 Method not found`.

| Method                        | Direction          | Purpose                                                         |
|-------------------------------|--------------------|-----------------------------------------------------------------|
| `initialize`                  | client → server    | Capability handshake; returns proxy name and protocol version `2024-11-05` |
| `notifications/initialized`   | client → server    | Notification (no response)                                      |
| `tools/list`                  | client → server    | Lists local tools and (eventually) tools proxied from upstream MCP servers |
| `tools/call`                  | client → server    | Executes a tool; returns text + optional `resource` content     |
| `tools/scan`                  | client → server    | Runs supplied JS detection routines; returns detected tools     |
| `config/get`                  | client → server    | Returns the proxy's current `tools`, `mcpServers`, and `allowRemoteConfig` flag |
| `config/update`               | client → server    | Replaces `tools` / `mcpServers` if `allow_remote_config: true`; persists `config.yaml` |
| `ping`                        | client → server    | Liveness check; returns `{}`                                    |

Transports:

- **Outbound mode**: SSE+HTTP at `/mcp/sse` (stream) and `/mcp/message` (request).
- **Inbound mode**: each WebSocket frame is a single JSON-RPC request or response.

## Logging

### Log Levels

1. **DEBUG**: Per-request method names, transport-level chatter
2. **INFO**: Startup, enrollment, ToolManager registration, tool scan results
3. **WARN**: Reconnect attempts, missing TLS, recoverable failures
4. **ERROR**: Tool execution failures, marshalling errors
5. **FATAL**: Unrecoverable startup or server failures (process exits)

### Log Configuration

```yaml
log:
  file_path: "/var/log/tachyonik/tachyonikproxy.log"
  to_console: false
  to_file: true
  level: INFO
```

### Changing Log Level at Runtime

```bash
TACHYONIKPROXY_LOG_LEVEL=DEBUG ./tachyonikproxy
TACHYONIKPROXY_LOG_LEVEL=ERROR ./tachyonikproxy
```

## Enrollment

Enrollment is the one-time process that issues TLS material to the proxy and registers it with ToolManager. There are two flows; both end with `tls.*` (and, in inbound mode, `reverse_connect.*`) populated in `config.yaml` and certificates written under `<config-dir>/certs/`.

### Privileges

A system installation (`.deb` / `.rpm`, or the installer's system path) must be enrolled with `sudo` — `/etc/tachyonik/tachyonikproxy` is not writable otherwise. Both flows check this before contacting Tachyonik, so an unprivileged attempt fails immediately with a hint and **does not consume the one-time enrollment token**.

The service itself runs as the unprivileged `tachyonikproxy` account, not as root. Enrollment therefore hands the cert directory, the certificates, and `config.yaml` to whichever account owns the config directory, and verifies the result before reporting success — the summary names it:

```
  Certificates written to: /etc/tachyonik/tachyonikproxy/certs
  Readable by service:     tachyonikproxy
```

A user-space install enrolls without `sudo` and owns everything already; nothing is changed there.

> Proxies enrolled with a version before 1.1.2 may have root-owned material under `/etc/tachyonik/tachyonikproxy`, which the service cannot read (it fails at startup with `permission denied` on `client.crt` or `server.crt`, even though `ls` shows the file world-readable — the 0700 cert *directory* is what blocks it). Repair with `sudo chown -R tachyonikproxy: /etc/tachyonik/tachyonikproxy` and restart; the certificates themselves stay valid, so no re-enrollment is needed.

### Online enrollment

```bash
tachyonikproxy enroll "https://tachyonik.example.com/enroll?token=<one-time-token>"
```

The proxy POSTs `{ token, hostname, port, ipAddresses }` to `/api/proxy-enroll` on the supplied host. ResourceManager (via ToolManager) returns the CA cert, the proxy's signed server cert + key (for outbound mode) or its client cert + key (for inbound mode), and the configured `connectionMode` and `toolManagerEndpoint`. The proxy persists everything atomically. Use `--insecure` only against self-signed development hosts.

### Reverse enrollment (`--listen`)

```bash
tachyonikproxy enroll --listen [--san dns1,dns2,1.2.3.4]
```

The proxy generates an ephemeral self-signed bootstrap certificate, prints a 6-digit pairing code, and listens on the configured port for a short window (10 minutes). The admin enters the pairing code in the Tachyonik WebUI; ToolManager dials the proxy, the proxy returns a CSR for its long-term key, and ToolManager replies with the signed cert chain. Used in strict-egress networks where the proxy cannot reach Tachyonik directly at enrollment time.

### Resetting

```bash
tachyonikproxy reset-enrollment           # interactive, requires typing "DELETE"
tachyonikproxy reset-enrollment --force
```

Removes all on-disk cert material and clears the relevant config sections so the proxy can be re-enrolled.

## Auto-update

The proxy can pull signed updates from Tachyonik's release server and apply them unattended, with automatic rollback if the new version fails its post-install health check.

> **Status:** the trust chain, the apply path with automatic rollback, **and** the periodic timer that drives the check unattended ship now. With a default install the proxy will check for signed updates roughly daily and apply them with automatic rollback on health failure. Fleet-wide telemetry (visibility of update outcomes from the Tachyonik WebUI) is not part of this release; local visibility is via `tachyonikproxy self-update --status` and journald.

> **Applies to the standalone (`.tar.gz`) install only.** A `.deb` or `.rpm` install belongs to your package manager: the built-in updater would write over files dpkg/rpm track, leaving the package database describing a version that is no longer on disk, and the next package upgrade would undo it. Those packages therefore ship no update timer, and `tachyonikproxy self-update` detects the marker at `/usr/share/tachyonik/tachyonikproxy/package-managed`, explains the situation and exits without changing anything. Upgrade them by re-running `install-tachyonikproxy.sh`, or by installing the current `.deb`/`.rpm` with your package manager.

### Trust chain

The auto-update mechanism is **not** secured by TLS to `tachyonik.com` alone — TLS is necessary but not sufficient. The proxy verifies a detached Ed25519 signature on every manifest using a public key compiled into the binary. A compromised CDN cannot substitute a malicious manifest because it cannot produce a valid signature without the offline-held signing key.

Order of validation on every check:

1. HTTPS GET of `auto_update.manifest_url` (rejected if not `https://`).
2. HTTPS GET of `<manifest_url>.sig`.
3. Ed25519 signature verified against any of the embedded public keys (multi-key support is for rotation: during a rotation window manifests are signed with both old and new keys; either one verifies).
4. JSON manifest parsed; `schemaVersion` must match.
5. Channel must equal `auto_update.channel`.
6. Manifest version must be **strictly greater** than the running binary's version (downgrade refused).
7. Manifest `publishedAt` must be **strictly newer** than the timestamp of the manifest that produced the currently installed version (replay refused).
8. The artifact entry for the running platform's `<goos>/<goarch>` must exist and have a non-empty SHA-256.

A failure at any step ends the check; nothing is downloaded or applied.

### Manifest format

```json
{
  "schemaVersion": 1,
  "channel": "stable",
  "latestVersion": "1.4.0",
  "publishedAt": "2026-05-04T10:00:00Z",
  "artifacts": {
    "linux/amd64":  { "url": "https://tachyonik.com/download/proxy/tachyonikproxy-1.4.0-linux-amd64.tar.gz",  "sha256": "…" },
    "linux/arm64":  { "url": "https://tachyonik.com/download/proxy/tachyonikproxy-1.4.0-linux-arm64.tar.gz",  "sha256": "…" },
    "darwin/amd64": { "url": "https://tachyonik.com/download/proxy/tachyonikproxy-1.4.0-darwin-amd64.tar.gz", "sha256": "…" },
    "darwin/arm64": { "url": "https://tachyonik.com/download/proxy/tachyonikproxy-1.4.0-darwin-arm64.tar.gz", "sha256": "…" }
  }
}
```

The companion `manifest.json.sig` is the raw 64-byte Ed25519 signature over `manifest.json`'s exact bytes. No JSON wrapping; no canonicalisation — sign the bytes the CDN serves.

### Configuration

```yaml
auto_update:
  enabled: true                                                       # default: true
  channel: stable                                                     # default: stable
  check_interval_hours: 24                                            # advisory; the timer is currently fixed at 24h
  health_window_seconds: 30                                           # rollback window after restart
  keep_versions: 3                                                    # cap on retained version dirs (always keeps active+previous+rolled-back)
  manifest_url: https://tachyonik.com/download/proxy/manifest.json
```

Set `enabled: false` to opt out entirely. Operators can also disable the systemd timer via `systemctl disable --now tachyonikproxy-update.timer`.

### Schedule

The package install (or `install-tachyonikproxy.sh`) deploys and enables the auto-update unit:

- **Linux**: `systemd` timer `tachyonikproxy-update.timer` running `tachyonikproxy-update.service` as a `oneshot`.
  - First fire: `OnBootSec=10min` after boot (so a fresh install picks up pending releases promptly).
  - Recurring: `OnUnitActiveSec=24h`.
  - Jitter: `RandomizedDelaySec=1h` so a fleet that booted together doesn't hit the manifest URL within seconds of each other.
  - `Persistent=true` so a system that was off when a tick was due fires on next boot.
  - The service runs as root with `Nice=10` and `IOSchedulingClass=idle`.
- **macOS**: LaunchDaemon `com.tachyonik.tachyonikproxy-update` with `StartInterval=86400` and `RunAtLoad=true`. Logs to `/var/log/tachyonik/tachyonikproxy-update.log`.
- **User-space install**: no managed timer. The operator either runs `tachyonikproxy self-update` manually or wires up their own cron / user systemd unit.
- **Windows**: not supported in this release.

The `auto_update.check_interval_hours` config knob is reserved for future use; the timer currently fires on a fixed 24h cadence baked into the unit. A configurable cadence requires templating the unit at install time and is not in this release.

To disable temporarily without uninstalling:

```bash
sudo systemctl disable --now tachyonikproxy-update.timer
```

To re-enable:

```bash
sudo systemctl enable --now tachyonikproxy-update.timer
```

A self-update fires regardless of whether the proxy is currently enrolled. If TLS material is missing, the apply path is skipped (with a clear log line) — so an unenrolled proxy does not pollute the rolled-back list.

Concurrent invocations are serialised via an exclusive `flock` on `<state-dir>/update.lock`; the second invocation exits immediately with a friendly message rather than racing.

### Local update visibility

```bash
tachyonikproxy self-update --status
```

Prints the contents of `update-state.json` in human-readable form: active version, previous, last check, last error, current phase, and the sticky rolled-back list. Useful when diagnosing why a fleet member is on an unexpected version.

The proxy daemon also logs a single `Auto-update: …` INFO line at startup summarising the most recent activity, captured by journald.

> **Fleet-wide visibility (not in this release).** Reporting update outcomes back to ToolManager so an admin can see the rollout state of an entire fleet from the WebUI is a separate workstream that touches ToolManager (DB schema, MCP notification), the WebUI (display), and the proxy (telemetry channel). Until that lands, fleet status is via SSH + `self-update --status` or via journald aggregation.

### On-disk layout

Auto-apply uses a versioned-symlink layout so a swap is a single atomic `rename(2)` and rollback is the same operation in reverse:

```
/opt/tachyonik/proxy/                         (system install)
├── 1.3.0/tachyonikproxy
├── 1.4.0/tachyonikproxy
└── current → 1.4.0                           (atomically flipped)

/usr/local/bin/tachyonikproxy → /opt/tachyonik/proxy/current/tachyonikproxy
/var/lib/tachyonik/proxy/update-state.json    (state machine + sticky rolled-back list)
```

User-space installs use the same shape under `~/.local/share/tachyonik/proxy/` with `~/.local/bin/tachyonikproxy` as the CLI symlink and the state file alongside.

Existing installs whose binary lives directly at `/usr/bin/tachyonikproxy` need a one-time migration:

```bash
sudo tachyonikproxy self-update --bootstrap-layout
```

`--bootstrap-layout` is idempotent — running it on an already-migrated install reports the layout state and exits without changes. The currently running daemon is unaffected because Linux keeps its open inode mapped; the next service restart picks up the new layout.

#### Pruning old version directories

Each successful apply leaves an additional directory under `<VersionRoot>`. Without bounds, that grows unboundedly (~20 MB per release). At the start of every `self-update` invocation that takes the apply/check path, the updater prunes obsolete directories:

- **Always kept**: the version pointed at by `current`, `state.Active`, `state.Previous`, and every entry in `state.RolledBack[]`. These are operationally required (Active is running, Previous is the rollback target) or part of the forensics record (rolled-back versions).
- **Cap**: on top of the always-keep set, the most-recently-modified `auto_update.keep_versions` directories are kept (default `3`, so steady state with no rollbacks is exactly 3 directories: active + previous + one older).
- **Defence in depth**: only directories that contain a `tachyonikproxy` binary are eligible for removal — anything else an operator put under `<VersionRoot>` is left alone.
- **Staging cleanup**: `<VersionRoot>/.staging/<v>/` directories left behind by crashed apply runs are also swept (the file lock guarantees no concurrent apply is in flight when prune runs).

To reclaim the disk used by previously rolled-back versions, an operator runs `tachyonikproxy self-update --reset-rollback-record` (which empties `state.RolledBack[]`); the next prune cycle removes the corresponding directories. Set `auto_update.keep_versions: 0` in `config.yaml` to keep only the safety set.

### Apply flow

When `tachyonikproxy self-update` is invoked (no flag), the subcommand runs the full state machine:

```
checking  →  download  →  stage  →  apply (swap symlink + restart)  →  verify (health window)
                                                  │
                                                  └─ unhealthy ─→  roll back (swap symlink + restart + verify)
                                                                                     │
                                                                                     ├─ healthy → mark <new> rolled-back
                                                                                     └─ still bad → state=degraded, exit
```

Every transition is persisted to `update-state.json` *before* the action, so a crash mid-flight is detectable on the next run.

**Health is intentionally a low bar.** The probe confirms the new binary came up and bound its listener — TLS handshake on the configured port for outbound mode, HTTP `GET /health` on `127.0.0.1:<port>` for inbound mode. Anything richer (real client `initialize` round-trip, ToolManager-side reachability) requires material the updater doesn't have, and would risk flapping on transient client issues.

### Rollback semantics

- Rollback is **automatic** when the post-restart health probe fails to succeed within `auto_update.health_window_seconds` (default 30 s).
- A version that was rolled back is recorded in `update-state.json::rolledBack[]` and **never auto-applied again**. The fleet stays on the previous good version until a new release moves the manifest forward.
- An operator can clear the sticky list with `tachyonikproxy self-update --reset-rollback-record` if they know better.
- If both the new version and the rolled-back-to previous version fail health, the state file is moved to `phase: degraded`. The subcommand exits non-zero with a clear error; manual intervention required.

### Subcommand

```bash
sudo tachyonikproxy self-update                       # check + apply + verify (+ auto-rollback on failure)
sudo tachyonikproxy self-update --dry-run             # check only, no changes
sudo tachyonikproxy self-update --status              # print the local update history from update-state.json
sudo tachyonikproxy self-update --bootstrap-layout    # one-time layout migration
sudo tachyonikproxy self-update --reset-rollback-record   # clear the sticky rolled-back list
```

Exit codes:
- `0` — success (or no update available).
- `1` — health failure that resulted in a successful rollback. The proxy is back on the previous version; the new version is on the sticky list.
- `2` — operational failure (signature mismatch, network error, missing layout, irrecoverable health failure). The on-disk state file holds the details.

### Public key rotation

Public keys live in `internal/selfupdate/pubkeys/` and are embedded via `go:embed`. To rotate, ship a release that embeds **both** the old and new key, then sign the next manifest with both during the overlap window. Once every proxy has been updated to that release, a subsequent release can drop the old key. See `internal/selfupdate/pubkeys/README.md` for the full procedure.

### Threat model summary

- **CDN compromise** — mitigated by the signature; CDN cannot re-sign.
- **MITM** — mitigated by TLS plus signature; signature is the load-bearing wall.
- **Downgrade attack** — refused at step 6.
- **Replay of old signed manifest** — refused at step 7.
- **Hostile ToolManager pushing an update** — impossible. The MCP channel cannot trigger an update; only the timer + signed manifest can.
- **Signing-key compromise** — mitigated by multi-key embedding and an offline rotation runbook.

## Integration

### ToolManager

ToolManager is the only platform component that talks directly to TachyonikProxy. It:

- Calls `tools/list`, `tools/call`, `tools/scan`, `config/get`, `config/update` over MCP.
- In outbound mode, connects via the SSE+HTTP transport and authenticates with its client certificate.
- In inbound mode, accepts the proxy's WebSocket and routes JSON-RPC traffic by `proxyName`.
- Owns the tool-detection routine library — the SHA-256-pinned bodies it sends to `tools/scan`.

### ResourceManager

Used during the enrollment handshake only. It:

- Issues the proxy's TLS material (CA cert, server cert + key, or client cert + key).
- Owns the `proxy-enroll` HTTP endpoint that consumes the one-time token.
- The proxy never talks to ResourceManager again after enrollment completes.

### ChatAI

ChatAI never talks to the proxy directly. It surfaces tools to the LLM and submits `tools/call` requests through ToolManager.

### Upstream MCP servers

Servers listed under `mcp_servers:` are advertised alongside the proxy's local tools. The proxy fetches their tool lists on first request and forwards `tools/call` to them transparently.

## Troubleshooting

### Proxy won't start

1. Check the listening port is free (outbound mode):
   ```bash
   lsof -i :9100
   ```
2. Verify the config file is readable and parses:
   ```bash
   tachyonikproxy --help && tachyonikproxy version
   ```
3. Tail the log:
   ```bash
   tail -f /var/log/tachyonik/tachyonikproxy.log
   ```

### Outbound mode refuses to start with "TLS not configured" / "TLS configuration is incomplete"

The outbound listener is HTTPS-only — the proxy does not start a plain-HTTP listener under any circumstance. Two distinct fatal messages tell you which case you're in:

- **`TLS not configured — proxy is not enrolled`** — all three of `tls.ca_cert` / `tls.server_cert` / `tls.server_key` are empty. Run `tachyonikproxy enroll <enrollment-url>` (online) or `tachyonikproxy enroll --listen` (reverse).
- **`TLS configuration is incomplete (missing: …)`** — some fields are set, others are not. The config has been edited or partially overwritten. Run `tachyonikproxy reset-enrollment` and re-enroll.
- **`TLS file <field>=<path> is not readable`** — the config references a cert path that does not exist. Run `tachyonikproxy reset-enrollment` and re-enroll.
- **`TLS file <field>=<path> cannot be read by <user>`** — the material exists but belongs to another account, typically root-owned leftovers from an enrollment run under `sudo` before 1.1.2. Do **not** re-enroll; the certificates are fine. Run the `chown -R` command the message prints and restart the service. The same check now runs for reverse-connect (inbound) mode, which previously logged `failed to load client certificate: … permission denied` on a retry loop with no startup diagnosis at all.

### TLS / mTLS handshake fails (outbound mode)

1. Confirm ToolManager's client certificate CN/DNS is listed in `tls.allowed_clients`.
2. Re-run enrollment if the cert has expired.

### `self-update` reports rollback

The new version came up but failed its health probe within the window, so the updater swapped the symlink back and restarted on the previous version. The new version is recorded in `update-state.json::rolledBack[]` and will not be auto-applied again.

To diagnose:

1. Tail the proxy log around the apply timestamp for the failing version.
2. Check `update-state.json::lastError` — captures the probe's last error before the window expired.
3. If the issue is fixed in a later release, the next manifest publication will move the fleet forward.
4. If you intentionally want to retry the same version, run `tachyonikproxy self-update --reset-rollback-record` and re-run.

### Auto-update isn't running on schedule

```bash
systemctl list-timers tachyonikproxy-update.timer       # next-fire time
systemctl status tachyonikproxy-update.timer            # is it enabled and active?
journalctl -u tachyonikproxy-update.service -n 50       # last invocation's log
```

If the timer is disabled, re-enable with `sudo systemctl enable --now tachyonikproxy-update.timer`. If `auto_update.enabled: false` is set in `config.yaml`, the timer will fire but the subcommand exits immediately with "Auto-update is disabled".

To inspect the local outcome history without waiting for the next tick:

```bash
sudo tachyonikproxy self-update --status
```

### `self-update` says "this install is not yet on the versioned-symlink layout"

The proxy was installed via a release that pre-dates the auto-update layout. Run once as root:

```bash
sudo tachyonikproxy self-update --bootstrap-layout
```

The migration is idempotent and does not require a service restart — the running daemon keeps its open inode and only the *next* service restart will switch to the new layout. After bootstrap, `self-update` works normally.

### Reverse-connect keeps reconnecting (inbound mode)

1. Check `reverse_connect.toolmanager_url` is reachable: `curl -v <https equivalent>`.
2. If the log says `missing TLS material … proxy is not enrolled`, run `tachyonikproxy enroll --listen` (or online enrollment) to populate the certs and `reverse_connect.*` fields.
3. If the log says `failed to load client certificate: … permission denied`, the cert material belongs to the wrong account — see the `cannot be read by` entry above. Since 1.1.2 this is caught at startup with the repair command instead of looping.
4. Otherwise look for `Reverse-connect error:` lines in the log — backoff doubles up to 60 s between attempts.

### Tool scan returns empty

1. Verify ToolManager is sending routines (`tools/scan` with a non-empty `routines` array).
2. Run `tachyonikproxy scan` locally — if no routines are bundled on the host, this is expected.
3. SHA-256 mismatches show up as per-routine errors; check the routine library version on ToolManager.

### Tool call rejected with "disallowed characters"

The argument violates the tool's `allowed_chars` allowlist. Either widen the allowlist for that tool in `config.yaml` or sanitize the input upstream — the allowlist is intentional defense-in-depth, do not disable it without reason.

### Remote config update rejected

`config/update` returns an error unless `allow_remote_config: true` is set in `config.yaml`. This is intentional — operators must opt in to allowing ToolManager to push tool definitions.

## Architecture

- **MCP server**: JSON-RPC 2.0 dispatcher over two interchangeable transports (SSE+HTTP for outbound, WebSocket for inbound). Implemented in `internal/mcpserver/`.
- **Reverse-connect dialer**: long-lived WebSocket client with exponential-backoff reconnect, `register` handshake, and 30-second pings. Implemented in `internal/reverseconnect/`.
- **Tool registry**: in-memory map of local `ToolConfig` plus stubs for upstream MCP servers; reload-safe under a `sync.RWMutex`. Implemented in `internal/tools/registry.go`.
- **Tool executor**: `os/exec` with templated argv, JSON-schema arg validation, regex character allowlist, output-file capture, and POSIX rlimit-based CPU/memory caps. Implemented in `internal/tools/executor.go`.
- **Tool scanner**: `goja`-sandboxed JS runner with `which()` and `exec()` host helpers and SHA-256 routine integrity checks. Implemented in `internal/toolscan/scanner.go`.
- **Enrollment**: online flow plus listen-mode bootstrap (ephemeral cert, pairing code, CSR exchange). Implemented in `internal/enroll/`.
- **Auto-update**: Ed25519-signed manifest fetch with embedded public keys, SHA-256 artifact verification, version monotonicity, `publishedAt` replay protection, versioned-symlink layout with atomic-rename swap, post-restart health probe, automatic rollback on health failure, sticky rolled-back list, exclusive `flock`-based serialisation, and a periodic systemd timer / launchd job that drives the check unattended (24h cadence, 1h jitter). Implemented in `internal/selfupdate/`. Fleet-wide telemetry to ToolManager is a separate workstream.
- **TLS utilities**: server- and client-side mTLS config loaders with TLS 1.3 minimum. Implemented in `internal/tlsutil/`.
- **Configuration**: YAML with environment-variable overrides; persisted on remote `config/update`. Implemented in `internal/config/`.
- **Service integration**: `cmd/server/service_default.go` (POSIX) and `cmd/server/service_windows.go` (Windows Service Control Manager) provide the platform-specific main entry point.
- **Packaging**: `Makefile` + `nfpm.yaml` + `packaging/` produce native packages (`.deb`, `.rpm`, `.pkg`, `.msi`) and portable archives (`.tar.gz`, `.zip`) from cross-compiled binaries in `dist/`.
