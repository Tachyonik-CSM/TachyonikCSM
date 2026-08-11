# SourceAnalyser

SourceAnalyser is a daemon that identifies the format of sources uploaded to
ResourceManager, so SourceImporter knows how to read them.

Classification is not hard-coded. An administrator describes a format in plain
language as an **analysis rule** in AIManager; an AI turns that description into
a small JavaScript routine; SourceAnalyser runs those routines against each
uploaded file. Adding support for a new format means writing a rule, not
changing this code.

## Table of Contents

- [Overview](#overview)
- [Building](#building)
- [Configuration](#configuration)
- [Operation](#operation)
- [Analysis rules and AI](#analysis-rules-and-ai)
- [Analysis Process](#analysis-process)
- [Status Values](#status-values)
- [Log Messages](#log-messages)
- [Troubleshooting](#troubleshooting)
- [Performance](#performance)
- [Architecture](#architecture)
- [Integration](#integration)
- [Example Usage](#example-usage)

## Overview

SourceAnalyser polls ResourceManager for sources awaiting analysis. For each one it:

1. Downloads a bounded window of the file
2. Converts it to matchable text (PDFs are extracted; text formats pass through)
3. Runs the loaded analysis routines against it, in rule order
4. Writes the resulting source type and status back to ResourceManager
5. Emits an audit event to SystemManager

A source is picked up when its status is `Analysing`, or when it qualifies for
re-analysis — see [Re-analysis triggers](#re-analysis-triggers).

## Building

### Prerequisites

- Go 1.24 or later
- A checkout of the full TachyonikCSM repository — this module resolves
  `tachyonik/lib` through `replace tachyonik/lib => ../TachyonikLib`, so it
  cannot be built from a standalone copy of this directory.

### Build Command

```bash
cd TachyonikCSM/SourceAnalyser
go build -o tachyonik-sourceanalyser ./cmd/daemon
```

This creates the `tachyonik-sourceanalyser` binary in the current directory.

From the repository root, `make go-build` builds every service binary, and
`make lib-check` builds, vets and tests this module together with TachyonikLib
and every other consumer.

### Dependencies

Managed via `go.mod`:

| Dependency | Purpose |
|------------|---------|
| `tachyonik/lib` | Shared logger, heartbeat, AIManager watcher, SystemManager client, PDF text extraction (local `replace`) |
| `github.com/dop251/goja` | Embedded ES5.1 JavaScript runtime that executes analysis routines |
| `gopkg.in/yaml.v3` | YAML configuration |

Pulled in indirectly: `github.com/ledongthuc/pdf` (PDF text extraction, via
TachyonikLib) and `github.com/gorilla/websocket` (AIManager watcher).

## Configuration

### Configuration File

Default configuration file: `./config.yaml` (override the path with `SOURCEANALYSER_CONFIG`).
See `config.yaml.example` for a fully commented template.

```yaml
# Sources are retrieved from ResourceManager. No data is sent.
resourcemanager:
  url: http://localhost:8080
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

# Log/heartbeat information is sent to SystemManager. No data is retrieved.
systemmanager:
  url: http://localhost:8083
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"  # falls back to resourcemanager key if empty

# AI configuration (model, name, system prompt) is retrieved from AIManager at runtime.
ai_manager:
  url: http://localhost:8085
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

analyzer:
  poll_interval: 5  # seconds between polls for new sources
  js_exec_timeout_seconds: 5  # max seconds one JS analysis routine may run; <= 0 disables
  max_content_bytes: 65536  # bytes downloaded/matched for a non-PDF source
  max_pdf_bytes: 33554432  # max bytes of a PDF fetched for text extraction (32 MiB)

# Liveness signal sent to SystemManager.
heartbeat:
  interval_seconds: 10

log:
  file_path: /home/tachyon/log/tachyonik-sourceanalyser.log
  to_console: false
  to_file: true
  level: INFO  # One of DEBUG, INFO, WARN, ERROR
```

The AI model, name, and system prompt are not set here — they are resolved from AIManager
at runtime via the `ai_manager` connection.

### Environment Variables

Environment variables override configuration file values:

| Variable | Description | Default |
|----------|-------------|---------|
| `SOURCEANALYSER_CONFIG` | Path to config file | `./config.yaml` |
| `RESOURCEMANAGER_URL` | ResourceManager API URL (legacy: `RESOURCEMANAGER_API_BASE_URL`) | `http://localhost:8080` |
| `RESOURCEMANAGER_INTERNAL_SERVICE_KEY` | Internal service key for ResourceManager | _(empty)_ |
| `SYSTEMMANAGER_URL` | SystemManager API URL | `http://localhost:8083` |
| `SYSTEMMANAGER_INTERNAL_SERVICE_KEY` | Internal service key for SystemManager (falls back to ResourceManager key) | _(empty)_ |
| `SOURCEANALYSER_AI_MANAGER_URL` | AIManager API URL | `http://localhost:8085` |
| `AIMANAGER_INTERNAL_SERVICE_KEY` | Internal service key for AIManager | _(empty)_ |
| `SOURCEANALYSER_POLL_INTERVAL` | Polling interval in seconds | `5` |
| `SOURCEANALYSER_JS_EXEC_TIMEOUT_SECONDS` | Max seconds a single JS analysis routine may run before being interrupted (guards against infinite loops / catastrophic regex in AI/feed-sourced routines); `<= 0` disables | `5` |
| `SOURCEANALYSER_MAX_CONTENT_BYTES` | Bytes downloaded and matched for a non-PDF source (also the type-sniff read) | `65536` |
| `SOURCEANALYSER_MAX_PDF_BYTES` | Max bytes of a PDF fetched for text extraction | `33554432` |
| `SOURCEANALYSER_HEARTBEAT_INTERVAL_SECONDS` | Heartbeat interval in seconds | `10` |
| `SOURCEANALYSER_LOG_FILE` | Log file path | `./sourceanalyser.log` |
| `SOURCEANALYSER_LOG_TO_CONSOLE` | Write logs to console (`true`/`1`) | `true` |
| `SOURCEANALYSER_LOG_TO_FILE` | Write logs to file (`true`/`1`) | `true` |
| `SOURCEANALYSER_LOG_LEVEL` | Log verbosity (DEBUG, INFO, WARN, ERROR) | `INFO` |

### Configuration Priority

1. Environment variables (highest priority)
2. Configuration file
3. Default values (lowest priority)

## Operation

### Commands

```bash
./tachyonik-sourceanalyser         # start the daemon (default)
./tachyonik-sourceanalyser help    # show usage and exit
```

### Starting the Daemon

On start the daemon loads its configuration, connects to AIManager to fetch this
module's AI settings and the analysis rules, loads a JavaScript routine for every
rule that has an active one, opens the AIManager WebSocket for live updates,
starts the SystemManager heartbeat, and begins polling.

### Stopping the Daemon

Send `SIGINT` (Ctrl+C) or `SIGTERM` to shut down gracefully — the source being
processed completes first.

### Running as a Background Service

```bash
# Start in background
nohup ./tachyonik-sourceanalyser > /dev/null 2>&1 &

# Save PID for later
echo $! > sourceanalyser.pid

# Stop the daemon
kill $(cat sourceanalyser.pid)
```

### Running with Custom Configuration

```bash
# Using custom config file
SOURCEANALYSER_CONFIG=/path/to/custom-config.yaml ./tachyonik-sourceanalyser

# Using environment variables
SOURCEANALYSER_POLL_INTERVAL=10 ./tachyonik-sourceanalyser
```

### Adjusting Poll Interval

The poll interval determines how often the daemon checks for new entries:

- **Lower values** (e.g., 1-2 seconds): Faster response time, higher CPU usage
- **Higher values** (e.g., 10-30 seconds): Lower CPU usage, slower response time
- **Default**: 5 seconds (balanced)

```bash
# Check every 2 seconds (faster)
SOURCEANALYSER_POLL_INTERVAL=2 ./tachyonik-sourceanalyser

# Check every 30 seconds (more efficient)
SOURCEANALYSER_POLL_INTERVAL=30 ./tachyonik-sourceanalyser
```

## Analysis rules and AI

### How a format gets recognised

An **analysis rule** in AIManager describes one file format in plain language.
For each rule, SourceAnalyser asks the configured AI to generate a JavaScript
routine implementing it, then validates and stores the routine back in AIManager:

1. **Generate** — the rule is sent to the AI together with the SourceAnalyser
   system prompt (Settings → SourceAnalyser → System Prompt)
2. **Validate syntax** — the generated code is loaded into a goja VM
3. **Validate behaviour** — every rule's `analyze()` is run against a set of mock
   source contexts to catch runtime errors
4. **Store** — the routine is saved in AIManager as `passed` or `failed`, with the
   model, a SHA-256 of the code, and any validation log

Only a routine stored as `passed` and marked *active* by an administrator is ever
loaded for real analysis.

### A routine

A routine defines a `rules` array; each entry has a `name`, a `ruleId`, and an
`analyze(ctx)` function returning `{sourceType, status}` on a match or `null`
otherwise:

```js
var rules = [{
  name: "OpenVAS XML Report",
  ruleId: 1,
  analyze: function (ctx) {
    if (ctx.content.indexOf("<report id=") >= 0) {
      return { sourceType: "OpenVAS Report", status: "Analysed" };
    }
    return null;
  }
}];
```

`ctx` carries `filename`, `content`, `fileSize` and `mimeType`.

### Running without AI

**An AI is required only to generate routines, never to run them.** With no AI
configured, already-generated routines keep loading and classifying sources
normally; only generation is disabled until an AI is assigned. The same applies
when the system prompt is empty — generation is refused with an explicit error
rather than run against an empty prompt.

### Live updates

SourceAnalyser holds one WebSocket connection to AIManager and reacts to:

- **Analysis rule created/updated/deleted** — regenerate or reload that rule's
  routine (debounced), and schedule a revisit of `Unsupported` sources
- **Module AI setting updated** — re-resolve the AI client and system prompt
- **Feed imported** — a bulk import replaces rules and routines wholesale, so the
  full rule set and module settings are reloaded

The connection re-syncs on every (re)connect, so state written while the daemon
was disconnected is picked up rather than missed.

### Containment

Routines are machine-generated and unreviewed, so they run under two constraints:

- **Execution budget** — `js_exec_timeout_seconds` bounds every operation that can
  run routine code (loading, rule extraction, and each `analyze()` call). An
  overrun is reported as an error and the routine is rejected, rather than hanging
  the daemon.
- **Bare VM** — goja is embedded with no `require`, no filesystem and no network,
  so a routine's reach ends at the values handed to it.

## Analysis Process

### Content extraction (PDF)

Before a source is evaluated, its bytes are turned into the text that rules match
against. Text formats (XML, JSON, CSV, plain text) are used as-is; **PDFs are
converted to plain text** (their text otherwise lives in compressed streams and
would be invisible to string matching). The extracted text is exposed to rules
as `ctx.content`, and the detected media type as `ctx.mimeType` (e.g.
`application/pdf`), so a rule can assert the file's type independently of its
text — for example:

```js
if (ctx.mimeType === "application/pdf"
    && ctx.content.indexOf("OpenVAS Vulnerability Report") >= 0
    && ctx.content.indexOf("This document reports on the results of an automatic security scan.") >= 0) {
  return { sourceType: "OpenVAS PDF Report", status: "Analysed" };
}
```

Extraction is best-effort: encrypted, scanned/image-only (no text layer), or
malformed PDFs yield no text and fall through to **Unsupported** — they never
crash the daemon. PDF text extraction is pure-Go (no external tools) and bounded
by `max_pdf_bytes`.

### Processing Steps

1. **List** — fetch all sources from ResourceManager
2. **Select** — keep those with status `Analysing`, plus any qualifying for
   re-analysis (below); selected sources are moved to `Analysing`
3. **Download** — fetch up to `max_content_bytes`. If the sniff shows a PDF that
   filled the window, re-fetch the whole file up to `max_pdf_bytes`
4. **Extract** — convert to matchable text and detect the media type
5. **Classify** — run each loaded routine in rule order; the first match wins
6. **Update** — write `{sourceType, status}` back to ResourceManager and emit an
   audit event to SystemManager

No rule matching leaves the source `Unsupported / Analysed`.

### Re-analysis triggers

A source is re-analysed without being re-uploaded when either applies:

- **The analyser advanced** — a source typed `Unsupported` and stamped with an
  older `analyserVersion` than the running build is a candidate for another pass
- **The rules changed** — after a rule create/update/delete or a feed import,
  sources still typed `Unsupported` are revisited once, since a rule that now
  exists may identify them. This covers sources resting in either terminal
  status: `Analysed`, or `No import routine` (where SourceImporter parks them)

### Status Flow

```
Analysing → OpenVAS Report (Analysed)     [a rule matched]
         └→ Unsupported (Analysed)         [no rule matched]
         └→ Error (File Not Found)         [ResourceManager could not deliver the file]
```

### Single-routine test sources

When a source carries a `testRoutineId` (a transient "test source" created by the
WebUI's routine-test panel), SourceAnalyser classifies it with **only** that
routine — fetched fresh from AIManager — instead of the active-routine set. This
lets a specific, possibly draft (not-yet-active), routine be exercised against a
real file, including full PDF text extraction, and the result (`{sourceType,
status}`) is written back exactly as for a normal source. No audit events are
emitted for test runs, and SourceImporter skips these sources so no data is
imported. The WebUI deletes the test source once it reads the result.

A test source carrying another module's `testModule` (e.g. `SourceImporter`) is
left untouched.

### Adding support for a new format

No code change is required. In the WebUI:

1. Create an analysis rule under AIManager describing the format
2. Let SourceAnalyser generate a routine for it (or request generation explicitly)
3. Review the generated routine and its validation log; test it against a real
   file using the routine-test panel
4. Mark the routine active

The daemon picks the change up over its AIManager WebSocket and revisits sources
previously left `Unsupported`.

## Status Values

- `Analysing` — source is queued for, or currently under, analysis
- `Analysed` — analysis completed (both for recognised types and `Unsupported`)
- `File Not Found` — ResourceManager could not deliver the file
- `Test failed` — a single-routine test could not be run (test sources only)

`sourceType` is whatever the matching rule returns (e.g. `OpenVAS Report`), or
`Unsupported` when nothing matched, or `Error` when the file could not be fetched.

## Log Messages

The shared logger prefixes each line with a level.

### Startup
```
2026/01/07 10:00:00 [INFO] Starting Tachyonik SourceAnalyser daemon...
2026/01/07 10:00:00 [INFO] Version: 1.1.1
2026/01/07 10:00:00 [INFO] Module AI settings loaded: AI=local-ollama, SystemPrompt=1832 chars
2026/01/07 10:00:00 [INFO] Loaded 4 analysis rules (3 with active routines)
2026/01/07 10:00:00 [INFO] AIManager watcher started
```

### Analysis
```
2026/01/07 10:00:05 [INFO] Processing 1 entries...
2026/01/07 10:00:05 [INFO] Rule 'OpenVAS XML Report' matched source 15 (openvas-report.xml): type=OpenVAS Report, status=Analysed
```

### No match
```
2026/01/07 10:00:07 [INFO] No rule matched for source: notes.txt (ID: 16), marking as Unsupported
```

### Errors
```
2026/01/07 10:00:15 [ERROR] Failed to download file for source 25: unexpected status code: 404
2026/01/07 10:00:20 [ERROR] JS rule 'Draft Rule' analyze error: JS execution aborted: JS execution budget exceeded
```

## Troubleshooting

### Daemon not processing sources

1. Check that sources in ResourceManager have status `Analysing`
2. Check the logs for connection errors to ResourceManager or AIManager
3. Verify the poll interval is appropriate

### Everything is marked "Unsupported"

Usually means no routines are loaded. Check the startup line
`Loaded N analysis rules (M with active routines)`:

- **M is 0** — no rule has an *active* routine. Generate one and mark it active.
- **N is 0** — no analysis rules exist in AIManager, or the connection failed.
- Otherwise the loaded routines genuinely do not match the file; test the rule
  against it with the routine-test panel.

Note that an AI is **not** needed for this — missing routines are a generation
problem, but existing ones run regardless.

### Routines are not being generated

- No AI assigned to SourceAnalyser (Settings → SourceAnalyser), or the assigned
  AI is unreachable or uses an unsupported provider
- The system prompt is empty — generation is refused, and the log says so
- Check the routine's stored validation log in AIManager: a `failed` routine
  records why it was rejected

### A routine keeps timing out

`JS execution budget exceeded` means a routine exceeded `js_exec_timeout_seconds`.
Usually a runaway loop or a catastrophic regular expression in generated code —
regenerate the routine, or raise the budget if the rule is legitimately heavy.

### High CPU usage

- Increase the poll interval
- Look for routines doing expensive matching over the full content window

## Performance

- **Poll-based** — checks ResourceManager for work every `poll_interval` seconds
- **Sequential** — processes one source at a time
- **Bounded I/O** — at most `max_content_bytes` per source (64 KiB by default);
  a PDF that fills that window is re-fetched up to `max_pdf_bytes` (32 MiB)
- **Bounded execution** — every routine runs under `js_exec_timeout_seconds`

Memory tracks the largest file being handled plus the goja VMs holding the loaded
routines, so a deployment analysing large PDFs should size against `max_pdf_bytes`
rather than assume a fixed footprint.

### Scaling Considerations

- Adjust the poll interval to the source creation rate
- Keep the rule set tight: every routine runs against every unmatched source
- Consider switching to an event-driven approach (like SourceImporter)

## Architecture

- **Polling** — time-based ticker drives the analysis loop
- **Rule execution** — AI-generated JavaScript in an embedded goja (ES5.1) VM,
  under an execution budget
- **Rule source** — analysis rules and routines live in AIManager; live changes
  arrive over one WebSocket connection
- **Content extraction** — pure-Go PDF text extraction via TachyonikLib
- **Signal handling** — graceful shutdown on SIGINT/SIGTERM
- **State** — sources are read fresh each poll; the loaded rules, their compiled
  routines, and a one-shot "revisit Unsupported" flag are held in memory and
  rebuilt from AIManager on (re)connect

## Integration

### Pipeline Position

```
User Upload → ResourceManager → SourceAnalyser → SourceImporter → AssetManager
                             ↑                ↑
                        (determines         (creates
                         file type)         assets)
```

AIManager supplies the analysis rules and routines; SystemManager receives audit
events and the liveness heartbeat.

### Workflow

1. **ResourceManager** receives an uploaded file, creates a source with status `Analysing`
2. **SourceAnalyser** polls, finds the source, downloads and extracts its text
3. **SourceAnalyser** runs the loaded routines and writes back the source type and status
4. **SourceImporter** detects the `Analysed` source and processes it
5. **SourceImporter** creates assets in AssetManager

## Example Usage

### Analyzing Uploaded Files

1. Upload a file through ResourceManager (status set to "Analysing")
2. SourceAnalyser automatically processes it within the poll interval
3. Check the updated status:
   ```bash
   sqlite3 /home/tachyon/data/ResourceManager.db "SELECT id, filename, source_type, status FROM sources WHERE id=15;"
   ```

### Manual Reanalysis

To reanalyze a source:

```bash
# Reset status to trigger reanalysis
sqlite3 /home/tachyon/data/ResourceManager.db "UPDATE sources SET status='Analysing' WHERE id=15;"

# Daemon will process it on next poll
```
