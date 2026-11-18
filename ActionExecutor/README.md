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
- [Versioning](#versioning)
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
(`tachyonik/lib/aipick`). A generated routine is saved to AIManager. It becomes
the rule's active routine right away when the request asked for it (the
Execution Rule dialog's "Make new routine the active routine", checked by
default) and the routine passed its checks; otherwise an operator activates it
there. A routine that failed its checks never replaces the active one.

## Building

### Prerequisites

- Go 1.24 or later
- A checkout of the full TachyonikCSM repository — this module resolves
  `tachyonik/lib` through `replace tachyonik/lib => ../TachyonikLib`, so it
  cannot be built from a standalone copy of this directory.

### Build Command

```bash
cd TachyonikCSM/ActionExecutor
make build          # embeds the version — see Versioning below
```

This creates the `tachyonik-actionexecutor` binary in the current directory;
`make test`, `make vet` and `make clean` do what they say.

A plain `go build -o tachyonik-actionexecutor ./cmd/daemon` also works, but the
binary then reports `0.0.0-dev` because nothing injected a version.

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

## Versioning

ActionExecutor carries **its own version**, resolved from its own namespaced
git tag. It is deliberately independent of the TachyonikCSM application version
(which lives in `WebUI/package.json`) and of every other module's.

```bash
git tag actionexecutor/1.0.0      # cut a release
make version                      # what this tree would build as
./tachyonik-actionexecutor version
```

The version is derived with `git describe --tags --match 'actionexecutor/*'`,
stripped to a numeric `x.y.z`, and injected at build time:

```
-ldflags "-X tachyonik/actionexecutor/internal/version.Version=<version>"
```

`--match` is what keeps it independent — every other tag in the monorepo is
ignored. Off-tag or dirty builds get a descriptive suffix (`1.0.0-3-gabc123`,
`1.0.0-dirty`); a tree with no matching tag falls back to `0.0.0-dev`.

Three build paths inject it, and all three must, or a build silently ships the
fallback:

| Path | How |
|---|---|
| `make build` in this directory | derives it from git |
| `make go-build` at the repo root | `ACTIONEXECUTOR_VERSION`, derived from git |
| The container image | `VERSION` build arg, passed by `compose.yaml` — the build stage has no git |

The value is **identity only**: it answers "which build is running". Nothing
compares it and nothing is stamped with it — not the results reported to
AIManager, not the audit entries. The routines are versioned separately:
AIManager stores each generated routine with its own version, the model that
wrote it and its checksum.

## Configuration

**An unusable configuration stops the service.** A configuration file that
exists but cannot be read or parsed, or that contains a key ActionExecutor does not
know (a misspelling, which would otherwise leave its setting at the default
without a word), stops it at startup with `ActionExecutor cannot start: <reason>`
on standard error and exit code 1 — visible in `journalctl` and `docker
logs`. Without a configuration file it runs on the defaults and the
environment, and logs that no file was found.

Copy `config.yaml.example` to `config.yaml` and set the keys and secrets. Every
option, its default and its environment variable is listed there.

Priority, highest first: environment variables, `config.yaml`, built-in
defaults. `ACTIONEXECUTOR_CONFIG` overrides the file location (default
`./config.yaml`).

A number, true/false or log-level value ActionExecutor cannot use stops it at
startup with exit code 1, whether it comes from an environment variable or from
the file, and the error lists every such setting with what it takes. The AI
timeout, the run time limit and the heartbeat interval take a whole number of
seconds above 0; `ACTIONEXECUTOR_LOG_TO_CONSOLE` and `ACTIONEXECUTOR_LOG_TO_FILE`
take `true`, `false`, `1` or `0`, in any case; the log level takes DEBUG, INFO,
WARN or ERROR, in any case. An empty variable counts as unset; in the file a
`0` counts as an absent key. Before 2026-11-18 such values were dropped without
a word — `ACTIONEXECUTOR_LOG_TO_FILE=yes` even switched the file log off — so
check a deployment's variables when upgrading.

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
./tachyonik-actionexecutor version  # build version; needs no configuration
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
  "message": "Retrieved 25 asset records (23 distinct hosts) from 192.168.178.162 …",
  "changes": []
}
```

A routine that reports `status: "error"` gives `success: false` with its
message in `error`; a rule that is not loaded, has no active routine, whose
routine was held back (see [Routines](#routines)), or whose run failed gives
`success: false` and the reason in `error`.

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
to have handled for them. When it fails, or the rule's routine was held back,
the action stays for the user to handle.

The WebUI's Action Guide offers the *Automatic* checkbox only where these
conditions can hold, for the same reason: elsewhere it would have no effect.

## Routines

A routine is ES5.1 JavaScript defining what to do, as `run(api)`. It is
compiled once when loaded and runs in a **fresh VM for every run**, so nothing it
keeps in a global can reach the next run, another user's included; runs of one
rule also no longer wait for each other. Recursion is capped (2048 calls), and
loading a routine is bounded too (5 s for its top-level code).

A routine begins with `var routineContract = 2;` and runs on the **platform
API bridge** in its read-write profile
([docs/specs/01-platform-api-catalogue.md](../docs/specs/01-platform-api-catalogue.md)).
**Everything is read and written through `api`**: `run(api)` calls
`api.<namespace>.<verb>({…, body})` with the operations the managers document
and register, read and write, for example `api.assets.stats()` or
`api.toolCalls.execute({body: {toolId: 40}})`. A call returns the answer the
operation documents, and throws a JavaScript error naming the operation and
the reason when it fails, so a routine wraps calls in `try/catch` where it can
recover.

- **Catalogue.** At startup ActionExecutor reads the platform API catalogue
  from AIManager and builds the read-write bridge from it. It does so again
  on `API_CATALOGUE_UPDATED`.
  Each time it logs a warning for a manager that has not registered its
  document (usually: `aimanager.url` is not set in that manager's
  configuration) and for one it has no URL for; the latter's operations are
  left out (PA-RT-09). Reading a name `api` does not have throws an error
  naming it and what exists, e.g. "api.assets does not exist in the platform
  API: its namespaces are: …" (PA-BR-11). Until the catalogue has been read,
  no routine runs; a run asked for fails saying so.
- **Identity.** Every call is made with the service key for the run's user
  and role — for a manual run the user and role AIManager checked when they
  asked. The bridge sets `X-User-ID`, `X-User-Role` and the owner fields of
  what a routine creates itself; a routine can't name anyone.
- **Generation.** The system prompt must contain `{{API_CATALOGUE}}`, which is
  replaced by the catalogue's read and write operations. A prompt without it
  predates the platform API: the AI is not asked, the generation fails with
  that reason, and the log says to replace the prompt with
  `prompts/actionexecutor.txt` (an operator applies it in AIManager, AI →
  Modules).
- **Validation.** A generated routine fails when it does not begin with
  `var routineContract = 2;`. Otherwise it runs once per mock scenario against
  the managers' mock answers; writes change nothing there. It fails when it
  calls an operation it may not, passes an argument the operation doesn't
  have, or reads a field the answer doesn't have. It is stored with the
  operations it called and the catalogue fingerprint.
- **Hold-back.** An active routine whose operations the catalogue no longer
  has is held back, and the log says so.

### Routines that predate the platform API

A routine without `var routineContract = 2;` was written for an `api` object
this version no longer has, and **never runs**. When it is loaded — at
startup, on a rule change, a feed import or a catalogue change — it is held
back and the log says
`execution rule 4: routine 240 predates the platform API, which this version no longer runs — generate the rule anew`.
A run asked of the rule — manual, from a run request, or automatic — fails
with the same message: it is the request's error, the rule's last execution
(status `error`) and a Warning in the audit trail; an action that would have
been handled automatically stays for the user. Generating the rule anew, and
making the new routine the active one, ends it.

### Time limit

A real run — manual or automatic — may take `execution.timeout_seconds`
(default 280 s). When the limit passes, the routine is interrupted and the run
fails with `routine exceeded its time limit of 4m40s`, reported like any other
failure (last execution, audit trail, WebUI). The next run of the rule starts
cleanly.

The interrupt lands between JavaScript statements. A bridge call under way is
cancelled when the limit passes; one call may take up to the limit, as a tool
run does (ToolManager allows a tool 10 minutes).

The routine lists' Test tabs run routines through AIManager's dry run
(`POST /api/dry-run`), which changes nothing.

### Which routine runs

Exactly the one AIManager marks as the rule's active routine. If it cannot be
loaded — not *passed*, not found, not valid JavaScript, predating the platform
API, or calling operations the catalogue no longer has — the rule has **no**
routine loaded and a run is refused with the reason, rather than the
previously loaded routine going on in its place. A deleted rule is unloaded at
once.

## Audit Trail

Each run writes one entry to the user's audit trail in SystemManager (module
`ActionExecutor`): what ran, how it ended, and which tools it started where.

| Run | Level | Text |
| --- | --- | --- |
| Manual, from an action | Info | `Action "…" (id 812) executed manually by execution rule "…" (id 4): success — <message>. Tools: gmp-get-results @ office (ok)` |
| Manual, on the rule | Info | `Execution rule "…" (id 4) run manually: success — <message>` |
| Automatic | Info | `Action "…" (id 42) executed automatically by execution rule 4: success — <message>. Tools: …` |
| Any, failed | Warning | `<subject>: failed (<reason>). Tools: …` |
| Manual, could not start | Warning | `<subject>: failed (execution rule 4 (…) has no active routine)`, or `Execution rule 4 run manually: failed (execution rule 4 not found)` |
| Any, routine held back | Warning | `<subject>: failed (execution rule 4: routine 240 predates the platform API, …)` |

- **Status and message.** A success gives the routine's status and, when it
  reported one, its message. A run fails when the routine throws or reports
  `status: "error"`; the reason is quoted. Reason and message are one line,
  capped at 500 characters (`internal/runaudit`).
- **Action.** A manual run started with Execute in an action's dialog names
  the action. Its id and title come with the run request from AIManager,
  because the action is deleted the moment the run is requested.
- **Tools.** Every `api.toolCalls.execute` call of the run is listed, in
  order, as `<tool> @ <proxy> (ok)` or `(failed: <reason, at most 120
  characters>)`: failed when the tool reported an error or its output could
  not be stored. A call that failed or was refused is listed as `tool <id>`.
  At most 10 are listed, the rest counted (`and 3 more`). The names come from
  ToolManager's answer (`toolName`, `proxyName`), read from the bridge's
  record of the run's calls (`runaudit.ToolCallsOf`).
- **Tools started by hand** (Tools → Execute) are audited by ToolManager.

## Architecture

| Package | Role |
| --- | --- |
| `cmd/daemon` | Startup and wiring |
| `internal/config` | Configuration loading |
| `internal/aimanager` | AIManager client: rules, routines, module settings, results |
| `internal/aiexecutor` | Holds the rules and their loaded routines; generates routines |
| `internal/codegen` | The generation request to the AI and its checks |
| `internal/jsruntime` | Runs a routine in goja, under its time limit |
| `internal/run` | One run of a routine for one user: the platform API bridge as `api`, time limit, result to AIManager, audit entry — the same for manual and automatic runs |
| `internal/servicecall` | An automatic run's own two requests (automation setting, deleting the action), with the service key on the user's behalf |
| `internal/manualrun` | Users' run requests from AIManager: claim, run, report |
| `internal/autoexecute` | Automatic execution of new actions |
| `internal/actionwatcher` | Watches ActionManager for new actions (on `tachyonik/lib/wswatcher`) |
| `internal/executionrulewatcher` | Watches AIManager for rule changes and feed imports, debounced per rule, and for run requests (on `tachyonik/lib/wswatcher`) |
| `internal/runaudit` | Wording of the audit-trail entries, and the tool runs read from the bridge's calls |
| `internal/version` | The build version, injected at build time |

**Events.** A changed rule is reloaded — that rule only; when a generation was requested for it
(*Generate* in the Execution Rule dialog), a routine is generated and saved, and the request is
cleared with the reason if it failed — and, when the request asked for it, with the new routine
as the rule's active one. A feed import reloads every rule and the
module settings. A change to the module's AI settings re-resolves the AI. A run
request is carried out at once, and on every (re)connect the waiting ones are.
