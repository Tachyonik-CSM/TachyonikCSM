<!--
ActionExecutor
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# ActionExecutor

ActionExecutor carries out **execution rules**: the "do it for me" half of an
action. Where ActionGenerator tells a user what to do next, an execution rule
does it — retrieves the host assets a scanner knows, adjusts the organisation's
host count, runs a security tool.

As with action rules, what an execution rule does is not hard-coded. An
administrator describes it in plain language in AIManager; an AI turns the
description into a small JavaScript routine; ActionExecutor runs that routine
for a user, with that user's data and permissions.

## Table of Contents

- [Overview](#overview)
- [Building](#building)
- [Configuration](#configuration)
- [Operation](#operation)
- [Manual Runs](#manual-runs)
- [Automatic Execution](#automatic-execution)
- [Routines](#routines)
- [Audit Trail](#audit-trail)
- [Architecture](#architecture)

## Overview

A routine runs in one of two ways:

1. **Manually** — the user presses *Execute* in the Automation section, or picks
   an Execution option in the Action Guide. The request reaches ActionExecutor
   through AIManager (see [Manual Runs](#manual-runs)).
2. **Automatically** — a new action is created whose only option runs an
   execution rule the user has set to *Automatic*. ActionExecutor runs it at
   once and deletes the action (see [Automatic Execution](#automatic-execution)).

Either way the result (`status`, `message`, `changes`) is reported back to
AIManager, where the WebUI shows it as the rule's last execution, and an entry
is written to the user's audit trail.

ActionExecutor is an event-driven daemon like ActionGenerator, SourceAnalyser
and SourceImporter: it serves no HTTP and receives all its work over WebSocket
watchers. Its liveness is the heartbeat it sends to SystemManager.

**The AI is needed only to generate a routine.** Running an already-generated
routine is plain JavaScript, so executions keep working with no AI configured;
only generation is refused. Which AI generates is decided at generation time —
the AI set on the rule, otherwise the module's current default — by asking
AIManager then rather than relying on what was loaded at startup
(`tachyonik/lib/aipick`). A generated routine is saved to AIManager; an operator
activates it there.

## Building

### Prerequisites

- Go 1.24 or later
- A checkout of the full TachyonikCSM repository — this module resolves
  `tachyonik/lib` through `replace tachyonik/lib => ../TachyonikLib`, so it
  cannot be built from a standalone copy of this directory.

### Build Command

```bash
cd TachyonikCSM/ActionExecutor
go build -o tachyonik-actionexecutor ./cmd/daemon
```

From the repository root, `make go-build` builds every service binary into
`/tmp/tachyonikcsm-build/` (this one as `actionexecutor`), and `make lib-check`
builds, vets and tests this module together with TachyonikLib and every other
consumer.

### Dependencies

| Dependency | Purpose |
| --- | --- |
| `github.com/dop251/goja` | JavaScript (ES5.1) runtime for the routines |
| `github.com/gorilla/websocket` | Watching AIManager and ActionManager for changes |
| `gopkg.in/yaml.v3` | `config.yaml` |
| `tachyonik/lib` | Logger, heartbeat, AI clients, AI choice, WebSocket watchers, REST client, SystemManager client |

## Configuration

Copy `config.yaml.example` to `config.yaml` and set the keys and secrets. Every
option, its default and its environment variable is listed there.

Priority, highest first: environment variables, `config.yaml`, built-in
defaults. `ACTIONEXECUTOR_CONFIG` overrides the file location (default
`./config.yaml`).

What matters most:

| Option | Why |
| --- | --- |
| `<service>.internal_service_key` | One per backend; each must match that service's key |
| `ai_manager.internal_service_key` | Also for AIManager's service-only routes: run requests, tool capabilities and tool rules |
| `execution.timeout_seconds` | How long one run may take (default 280 s) — see [Time limit](#time-limit) |

`config.yaml` holds secrets and is git-ignored; never commit it.

## Operation

```bash
./tachyonik-actionexecutor          # start the daemon
./tachyonik-actionexecutor help     # usage; needs no configuration
```

At startup the daemon loads the module settings and every execution rule with
its active routine from AIManager and starts its watchers, then runs whatever
run requests are waiting. It stops on SIGINT or SIGTERM. A heartbeat is sent to
SystemManager every `heartbeat.interval_seconds`.

Logs go to the console and/or `log.file_path`, at `log.level`.

## Manual Runs

A user's *Execute* is a request, handed over by AIManager:

1. The WebUI asks AIManager: `POST /api/execution-rules/{id}/run`, with the
   user's login token. AIManager checks the user and the rule (it must have an
   active routine), **stores** the request with the user and their role, and
   answers at once with the request's id.
2. AIManager announces it over its WebSocket (`EXECUTION_REQUESTED`, to the
   services only). ActionExecutor **claims** it — only one claim succeeds, so a
   request runs once — and runs the routine with the service key on behalf of
   that user and role, as an automatic run does.
3. ActionExecutor reports the outcome to the request. AIManager passes it to the
   user who asked (`EXECUTION_REQUEST_FINISHED`), and the WebUI's dialog can
   also read it back (`GET /api/execution-requests/{id}`). As for every run, the
   result is also the rule's last execution, and an audit entry is written.

A request made while ActionExecutor is down waits: on every (re)connect it
fetches the pending requests and runs them. One nobody picks up within 10
minutes expires instead of running long after anyone expects it, and a run
whose result never arrives is marked failed after an hour.

The outcome has the shape the Execute dialog shows:

```json
{
  "success": true,
  "status": "success",
  "message": "Retrieved 23 host assets from 192.168.178.162 …",
  "changes": []
}
```

A routine that reports `status: "error"` gives `success: false` with its
message in `error`; a rule that is not loaded, has no active routine, or whose
run failed gives `success: false` and the reason in `error`.

## Automatic Execution

There is no schedule. ActionExecutor watches ActionManager for new actions, and
runs an execution rule on its own only when **all** of these hold for a new
action:

1. It was raised by an action rule.
2. That action rule has **exactly one** action option, and it is of type
   *Execution*. All options linked to the rule count, including those its
   visibility conditions hide for this user.
3. The user has set the execution rule that option names to *Automatic*
   (Automation section; stored in SystemManager).

The routine then runs with the service key on the user's behalf, and the action
is deleted when the run succeeds, so the user never sees a task they had chosen
to have handled for them. When it fails, the action stays for the user to
handle.

The WebUI's Action Guide offers the *Automatic* checkbox only where these
conditions can hold, for the same reason: elsewhere it would have no effect.

## Routines

A routine is ES5.1 JavaScript defining what to do, as `run(api)`. It is
compiled once when loaded and runs in a **fresh VM for every run**, so nothing it
keeps in a global can reach the next run, another user's included; runs of one
rule also no longer wait for each other. Recursion is capped (2048 calls), and
loading a routine is bounded too (5 s for its top-level code).
**Everything is read and written through `api`**, including who the run is
for (`api.user.get()`): it returns current data, can be filtered, costs a
request only when a routine needs it, and throws on failure.

The `api` object (`internal/apibridge`) proxies calls to the backends on the
user's behalf, with the user's permissions:

| Namespace | Calls | Service |
| --- | --- | --- |
| `api.user` | `get` — the user the run is for | SystemManager |
| `api.organisation` | `get`, `update` | SystemManager |
| `api.assets` | `list`, `get`, `create`, `update` (the type only), `delete` | AssetManager |
| `api.vulnerabilities`, `api.detections` | `list` | AssetManager |
| `api.actions` | `list`, `create`, `update`, `delete` | ActionManager |
| `api.sources` | `list`, `create`, `delete` | ResourceManager |
| `api.tools` | `list`, `get` | ResourceManager, enriched from AIManager |
| `api.tools.execute(toolId, arguments, ruleId?)` | run a security tool | ToolManager |

Every call uses the service key with the run's user id and role
(`X-User-ID`/`X-User-Role`), so the backend applies that user's permissions — for
a manual run the user and role AIManager checked when they asked.

`api.tools.execute` returns `{content, isError, storedFiles, errors, warnings}`.
Output files are stored as sources by ToolManager. A file identical to a source
already on record is not stored again; it is reported in `warnings`, not
`errors`, and that source's last-seen date is updated.

Every call blocks and throws a JavaScript error on a non-2xx answer, so a
routine wraps calls in `try/catch` where it can recover.

### Time limit

A real run — manual or automatic — may take `execution.timeout_seconds`
(default 280 s). When the limit passes, the routine is interrupted and the run
fails with `routine exceeded its time limit of 4m40s`, reported like any other
failure (last execution, audit trail, WebUI). The next run of the rule starts
cleanly.

The interrupt lands between JavaScript statements. A backend call already under
way finishes first: `api.tools.execute` may wait up to the limit (ToolManager
allows a tool 10 minutes), every other `api.*` call at most 30 s.

The routine lists' Test tabs run routines through AIManager's dry run
(`POST /api/dry-run`, package `dryrun`), against a mock of this module's `api`
that records every call and changes nothing. A method added to the bridge
belongs in that mock too; `scripts/check-dryrun-mock.sh` (part of
`make lib-check`) fails when one is missing.

### Which routine runs

Exactly the one AIManager marks as the rule's active routine. If it cannot be
loaded — not *passed*, not found, or not valid JavaScript — the rule has **no**
routine loaded and a run is refused (`424`), rather than the previously loaded
routine going on in its place. A deleted rule is unloaded at once.

## Audit Trail

Each run writes one entry to the user's audit trail in SystemManager:

| Run | Level | Text |
| --- | --- | --- |
| Manual, succeeded | Info | `Execution rule "…" (id 4) run manually (status success)` |
| Manual, failed | Warning | `Execution rule "…" (id 4) run manually: failed (<reason>)` |
| Automatic, succeeded | Info | `Action "…" (id 42) executed automatically by execution rule 4 (status success)` |
| Automatic, failed | Warning | `Action "…" (id 42) executed automatically by execution rule 4: failed (<reason>)` |

A run fails when the routine throws or when it reports `status: "error"`; the
reason is quoted, capped at 500 characters (`internal/runaudit`).

## Architecture

| Package | Role |
| --- | --- |
| `cmd/daemon` | Startup and wiring |
| `internal/config` | Configuration loading |
| `internal/aimanager` | AIManager client: rules, routines, module settings, results |
| `internal/aiexecutor` | Holds the rules and their loaded routines; generates routines |
| `internal/codegen` | The generation request to the AI and its checks |
| `internal/jsruntime` | Runs a routine in goja, under its time limit |
| `internal/apibridge` | The `api` object a routine calls, and the one place a request to another service is authenticated |
| `internal/run` | One run of a routine for one user: `api` object, time limit, result to AIManager, audit entry — the same for manual and automatic runs |
| `internal/manualrun` | Users' run requests from AIManager: claim, run, report |
| `internal/autoexecute` | Automatic execution of new actions |
| `internal/actionwatcher` | Watches ActionManager for new actions (on `tachyonik/lib/wswatcher`) |
| `internal/executionrulewatcher` | Watches AIManager for rule changes and feed imports, debounced per rule, and for run requests (on `tachyonik/lib/wswatcher`) |
| `internal/runaudit` | Wording of the audit-trail entries |

**Events.** A changed rule is reloaded — that rule only; when a generation was requested for it
(*Generate* in the WebUI), a routine is generated and saved, and the request is
cleared with the reason if it failed. A feed import reloads every rule and the
module settings. A change to the module's AI settings re-resolves the AI. A run
request is carried out at once, and on every (re)connect the waiting ones are.
