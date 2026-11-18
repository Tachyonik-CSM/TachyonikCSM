<!--
ActionGenerator
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# ActionGenerator

What ActionGenerator must and must not do is specified in [spec/](spec/README.md), including the API bridge routines read through and the routine contract. This README covers building, configuring and running it; where the two disagree, the specification wins.

ActionGenerator is a daemon that turns the state of a user's installation into
**actions** — the concrete next steps shown in the Actions view.

What counts as a next step is not hard-coded. An administrator describes a
situation in plain language as an **action rule** in AIManager; an AI turns that
description into a small JavaScript routine; ActionGenerator evaluates those
routines for each user — the routines read the user's situation through the
platform's APIs — and creates the actions they return.
Adding a new kind of advice means writing a rule, not changing this code.

## Table of Contents

- [Overview](#overview)
- [Building](#building)
- [Versioning](#versioning)
- [Configuration](#configuration)
- [Operation](#operation)
- [Action Rules](#action-rules)
- [Containment](#containment)
- [Architecture](#architecture)
- [Log Messages](#log-messages)
- [Troubleshooting](#troubleshooting)
- [Integration](#integration)

## Overview

ActionGenerator holds the loaded rule routines in memory and re-evaluates them
whenever the data they reason about changes. For each user it:

1. Starts one pass of the platform API bridge for the user
2. Runs every loaded routine's `check(api)`; the routine reads what it needs —
   assets, the organisation, tools, existing actions — through `api`
3. Calls `createAction(api)` for each rule that matches
4. Creates the resulting action in ActionManager, unless the user already has it
5. Withdraws the outstanding `New` actions of a rule that has stopped matching,
   and leaves them alone for a rule that could not be decided

Step 5 is what makes an action disappear once the user has done what it asked,
rather than lingering as stale advice.

**Which AI generates a routine is decided at generation time.** Before each
generation, the module asks AIManager for its current setting. It uses the AI
set on the rule if there is one, otherwise the module's current default, and
generates with the module's current system prompt. It doesn't rely on the AI it
loaded at startup and the change notification that should follow a switch. A
missed notification therefore can't leave the module on an AI that is no longer
configured. If AIManager can't be asked, the AI loaded last is used. Every
generation error, and the log line before each generation, names the AI, e.g.
`AI "claude opus 5" (id 6, anthropic, model claude-opus-5; module default)`.
The reason is `set on this rule`, `module default`, or that it is the AI loaded
last and may be out of date. The choice is implemented in `tachyonik/lib/aipick`.

The AI is needed only to **generate** a routine from a rule. Evaluating an
already-generated routine is plain JavaScript, so the daemon keeps producing
actions with no AI configured — only code generation is refused.

### SaaS vs. Appliance mode

The daemon reads the instance operating mode from SystemManager (`GET /api/internal/workspace`) and adapts how it generates:

- **SaaS mode** — each user has private data, so rules are evaluated per user and actions are owned by that user (the behavior above).
- **Appliance mode** — the whole instance is one shared workspace: every non-anonymous user sees the same data, so generating one action set per user would surface as duplicates in the shared Actions view. Instead, each rule is evaluated **once**, for a single **primary owner** (the oldest admin, reported as `primaryUserId`), whose API answers the managers give for the whole workspace, and the resulting actions are created under that owner. Any per-user duplicates left by earlier generation are consolidated away under that owner. If the mode can't be determined, the daemon falls back to per-user generation.

## Building

### Prerequisites

- Go 1.24 or later
- A checkout of the full TachyonikCSM repository — this module resolves
  `tachyonik/lib` through `replace tachyonik/lib => ../TachyonikLib`, so it
  cannot be built from a standalone copy of this directory.

### Build Command

```bash
cd TachyonikCSM/ActionGenerator
make build          # embeds the version — see Versioning below
```

This creates the `tachyonik-actiongenerator` binary in the current directory.

A plain `go build -o tachyonik-actiongenerator ./cmd/daemon` also works, but the
binary then reports `0.0.0-dev` because nothing injected a version.

From the repository root, `make go-build` builds every service binary into
`/tmp/tachyonikcsm-build/` (this one as `actiongenerator`), and `make lib-check`
builds, vets and tests this module together with TachyonikLib and every other
consumer.

### Dependencies

Managed via `go.mod`:

| Dependency | Purpose |
|------------|---------|
| `tachyonik/lib` | Shared logger, REST client, heartbeat, AIManager watcher, WebSocket watcher, generated-code cleanup, the platform API catalogue and bridge (local `replace`) |
| `github.com/dop251/goja` | Embedded ES5.1 JavaScript runtime that executes rule routines |
| `github.com/gorilla/websocket` | The manager event connections |
| `gopkg.in/yaml.v3` | YAML configuration |

## Versioning

ActionGenerator carries **its own version**, resolved from its own namespaced
git tag. It is deliberately independent of the TachyonikCSM application version
(which lives in `WebUI/package.json`) and of every other module's: the module is
released when its generation behaviour changes, which is not the same rhythm.

```bash
git tag actiongenerator/1.0.0     # cut a release
make version                      # what this tree would build as
./tachyonik-actiongenerator version
```

The version is derived with `git describe --tags --match 'actiongenerator/*'`,
stripped to a numeric `x.y.z`, and injected at build time:

```
-ldflags "-X tachyonik/actiongenerator/internal/version.Version=<version>"
```

`--match` is what keeps it independent — every other tag in the monorepo is
ignored. Off-tag or dirty builds get a descriptive suffix (`1.0.0-3-gabc123`,
`1.0.0-dirty`); a tree with no matching tag falls back to `0.0.0-dev`. The first line of every start names it — `Starting Tachyonik ActionGenerator <version> (<go version>, <os>/<arch>)` — so a support log shows exactly what ran.

Three build paths inject it, and all three must, or a build silently ships the
fallback:

| Path | How |
|---|---|
| `make build` in this directory | derives it from git |
| `make go-build` at the repo root | `ACTIONGENERATOR_VERSION`, derived from git |
| The container image | `VERSION` build arg, passed by `compose.yaml` — the build stage has no git |

### What this version does not do

Unlike SourceAnalyser's, this value is **identity only**. Nothing compares it,
and nothing is stamped with it: there is no equivalent of
`sources.analyser_version`, so a new release does not cause anything to be
re-generated. It answers "which build is running", and that is all it is wired
to answer — if it is ever given a behavioural meaning, that should be a
deliberate decision rather than a side effect.

The routines themselves are versioned separately. AIManager stores each
generated routine with its own version, the model that wrote it and its
checksum; those move when a rule moves and are unrelated to the module version.

## Configuration

**An unusable configuration stops the service.** A configuration file that
exists but cannot be read or parsed, or that contains a key ActionGenerator does not
know (a misspelling, which would otherwise leave its setting at the default
without a word), stops it at startup with `ActionGenerator cannot start: <reason>`
on standard error and exit code 1 — visible in `journalctl` and `docker
logs`. Without a configuration file it runs on the defaults and the
environment, and logs that no file was found.

### Configuration File

Default configuration file: `./config.yaml`, overridable with
`ACTIONGENERATOR_CONFIG`.

`config.yaml.example` is the annotated reference — every key, its default, its
environment variable and what the block is for. In outline:

```yaml
assetmanager:     # read by routines through the API bridge; change messages
  url: http://localhost:8081
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

systemmanager:    # users, workspace mode, the API bridge; heartbeat out
  url: http://localhost:8083
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

resourcemanager:  # read by routines through the API bridge; change messages
  url: http://localhost:8080
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

actionmanager:    # where actions are created and withdrawn
  url: http://localhost:8082
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

toolmanager:      # capabilities.get, through the API bridge
  url: http://localhost:8086
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

ai_manager:       # rules, routines, AI assignment, system prompt, API catalogue
  url: http://localhost:8085
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

ai:
  timeout_seconds: 300          # HTTP timeout for code-generation requests
  js_exec_timeout_seconds: 5    # budget for one JS execution

heartbeat:
  interval_seconds: 10

log:
  file_path: /home/tachyon/log/tachyonik-actiongenerator.log
  to_console: false             # state both booleans explicitly — see below
  to_file: true
  level: INFO                   # DEBUG, INFO, WARN, ERROR
```

Two traps worth knowing before you edit a live `config.yaml`:

- **`log.to_console` and `log.to_file` must be stated explicitly.** They are
  applied unconditionally rather than only when non-empty, because YAML cannot
  distinguish an absent boolean from `false`. Omitting one disables that sink
  instead of falling back to its default of `true`.
- **`ai.js_exec_timeout_seconds: 0` does not disable the guard.** In the file, a
  `0` is indistinguishable from an absent key, so the default of 5 applies. Use
  `ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS=0` to switch the guard off.

### Environment Variables

Environment variables override configuration file values:

| Variable | Description | Default |
|----------|-------------|---------|
| `ACTIONGENERATOR_CONFIG` | Path to config file | `./config.yaml` |
| `ASSETMANAGER_URL` | AssetManager API URL | `http://localhost:8081` |
| `ASSETMANAGER_INTERNAL_SERVICE_KEY` | AssetManager service key | - |
| `SYSTEMMANAGER_URL` | SystemManager API URL | `http://localhost:8083` |
| `SYSTEMMANAGER_INTERNAL_SERVICE_KEY` | SystemManager service key | - |
| `RESOURCEMANAGER_URL` | ResourceManager API URL | `http://localhost:8080` |
| `RESOURCEMANAGER_INTERNAL_SERVICE_KEY` | ResourceManager service key | - |
| `ACTIONMANAGER_URL` | ActionManager API URL | `http://localhost:8082` |
| `ACTIONMANAGER_INTERNAL_SERVICE_KEY` | ActionManager service key | - |
| `ACTIONGENERATOR_AI_MANAGER_URL` | AIManager API URL | `http://localhost:8085` |
| `TOOLMANAGER_URL` | ToolManager API URL (the API bridge) | `http://localhost:8086` |
| `TOOLMANAGER_INTERNAL_SERVICE_KEY` | ToolManager service key | - |
| `AIMANAGER_INTERNAL_SERVICE_KEY` | AIManager service key | - |
| `ACTIONGENERATOR_AI_TIMEOUT_SECONDS` | HTTP timeout for AI code-generation requests, in whole seconds above 0 | `300` |
| `ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS` | Budget for a single JS execution — loading a routine, one `check()`, one `createAction()`. Routine code is AI-generated and goja cannot be preempted, so without this a rule that never terminates hangs its goroutine for the life of the process. Whole seconds; `0` disables the guard | `5` |
| `ACTIONGENERATOR_HEARTBEAT_INTERVAL_SECONDS` | Liveness heartbeat interval to SystemManager, in whole seconds above 0 | `10` |
| `ACTIONGENERATOR_LOG_FILE` | Log file path | `./actiongenerator.log` |
| `ACTIONGENERATOR_LOG_TO_CONSOLE` | Log to console (`true`, `false`, `1` or `0`, any case) | `true` |
| `ACTIONGENERATOR_LOG_TO_FILE` | Log to file (`true`, `false`, `1` or `0`, any case) | `true` |
| `ACTIONGENERATOR_LOG_LEVEL` | Log level (DEBUG, INFO, WARN, ERROR, any case) | `INFO` |

A value ActionGenerator cannot use stops it at startup with exit code 1 and an
error on standard error listing every such setting with what it takes, whether
it comes from a variable above or from the file (a negative number, an unknown
log level). An empty
variable counts as unset. Before 2026-11-18 such a value was ignored without a
word — `ACTIONGENERATOR_LOG_TO_FILE=yes` even switched the file log off — so
check a deployment's variables when upgrading (spec AG-OPS-15).

### Configuration Priority

1. Environment variables (highest priority)
2. Configuration file
3. Default values (lowest priority)

## Operation

### Starting the Daemon

```bash
./tachyonik-actiongenerator
```

The daemon will:

1. Load configuration from `config.yaml` and environment variables
2. Initialise logging and the API clients
3. Resolve its AI assignment and system prompt from AIManager
4. Load the action rules and their active routines, building one executor per rule.
   If AIManager can't be reached yet, the daemon starts without rules and
   tries again every 30 seconds and on every connect to AIManager, then
   evaluates every rule (spec AG-OPS-08)
5. Evaluate every rule for every user once, on startup
6. Start the watchers (AssetManager, SystemManager, action rules, module AI
   settings) and the SystemManager heartbeat
7. Re-evaluate whenever a watcher reports a change

### Stopping the Daemon

Send `SIGINT` (Ctrl+C) or `SIGTERM` to shut down gracefully (spec AG-OPS-09):

1. The watchers are closed, so no message starts an evaluation any more; an
   evaluation request or pass asked for from now on is refused.
2. The rule and user being evaluated finish — their new actions created and
   their stale ones withdrawn — and the pass stops there; the rest is evaluated
   by the pass at the next start. The wait is at most 20 seconds; a unit still
   running then is named in a warning and cut off. The container is given 30
   seconds (`stop_grace_period` in `deployment/docker/compose.yaml`).
3. A routine generation in progress is not waited for: an AI call takes
   minutes. Its request, and any request made while the daemon was down, is
   cleared at the next start, once the rules are loaded, with that reason,
   which the rule dialog shows — request it again. Only requests made before
   the start are cleared, so one made after it is never mistaken for one.
4. The heartbeat and the log file are closed.

### Running as a Background Service

```bash
# Start in background
nohup ./tachyonik-actiongenerator > /dev/null 2>&1 &

# Save PID for later
echo $! > actiongenerator.pid

# Stop the daemon
kill $(cat actiongenerator.pid)
```

### Running with Custom Configuration

```bash
# Using a custom config file
ACTIONGENERATOR_CONFIG=/etc/tachyonik/actiongenerator.yaml ./tachyonik-actiongenerator

# Using environment variables
ACTIONGENERATOR_LOG_LEVEL=DEBUG \
ACTIONMANAGER_URL=http://actionmanager.internal:8082 \
./tachyonik-actiongenerator
```

## Action Rules

Rules live in AIManager, not in this repository. An administrator writes one in
the WebUI (Settings → Action Rules): a title, a trigger description in plain
language, and the action to raise when it matches. AIManager stores the rule;
ActionGenerator asks the configured AI to write a JavaScript routine for it,
validates the result, and stores it back. The stored routine becomes the rule's
active routine — the one that runs — when an administrator picks it, or when
the generation request asked for it ("Make new routine the active routine" in
the Edit Action Rule dialog): ActionGenerator then names a passed routine the
active one as it clears the request, and reloads the active routines on
AIManager's update. A failed routine never replaces the active one.

A routine declares `var routineContract = 2;` and is an array of rule objects,
each with a `check(api)` that decides whether the rule applies and a
`createAction(api)` that describes the action to raise. `api` is the platform
API bridge (see [What a routine reads](#what-a-routine-reads)).

### When rules are re-evaluated

An action is created when its rule matches and **garbage-collected when it stops
matching** — but only when something re-runs the rules. `ProcessRules` is
triggered by:

| Trigger | Fires on |
| --- | --- |
| Startup | daemon start |
| Manager WebSockets (AssetManager, SystemManager, ResourceManager, ActionManager, AIManager) | a message an API operation an active routine calls lists as `x-invalidated-by` (2s debounce); and on every (re)connect |
| SystemManager WebSocket | `USER_CREATED`, `USER_DELETED`, whatever the routines read — they change whom the rules are evaluated for (2s debounce) |
| Action-rule watcher | a rule created, edited or deleted; an evaluation request; a feed import; an API catalogue change; every reconnect to AIManager, after the rules are loaded again — the connection has missed the changes made while it was down |

There is deliberately **no periodic sweep**: staleness is fixed by watching the
data the routines read, not by re-running on a timer. The triggers follow from
the routines: the message types after which an operation's answer may differ
are part of its manager's API document (`x-invalidated-by`), and ActionGenerator
recomputes the set whenever the rules or the catalogue change. A message no
active routine depends on — `TOOL_ACTIVITY`, say — wakes nothing.

### What a routine reads

A routine reads the platform's data through the **API bridge**
([docs/specs/01-platform-api-catalogue.md](../docs/specs/01-platform-api-catalogue.md)).
Its `check(api)`, `createAction(api)` and option conditions call
`api.<namespace>.<verb>({…})`: for example `api.assets.stats()` or
`api.capabilities.get()`. Each call goes to the manager for the user being
evaluated, and returns the documented fields of the answer, nothing else.
What exists is what the managers' API documents (`*/spec/openapi.yaml`) mark
`x-bridge: read`; there is no field list here to keep in step.

- **Catalogue.** At startup ActionGenerator reads the platform API catalogue
  from AIManager and builds the bridge from it, read-only. It does so again
  on `API_CATALOGUE_UPDATED`. A routine whose operations the catalogue no
  longer offers is held back, and the log says so.
  Each time it logs a warning for a manager that has not registered its
  document (usually: `aimanager.url` is not set in that manager's
  configuration) and for one it has no URL for; the latter's operations are
  left out (PA-RT-09). Reading a name `api` does not have throws an error
  naming it and what exists, e.g. "api.assets does not exist in the platform
  API: its namespaces are: …" (PA-BR-11).
- **Evaluation.**
  - One bridge pass per evaluation pass: the same call is answered once.
  - In a shared workspace, the rule runs once for the primary owner, and the
    managers answer with the workspace's data.
  - A rule whose `check()` or `createAction()` throws, typically because a
    call failed, is left **undecided**: nothing is created and nothing is
    withdrawn. The same holds for every rule while the catalogue is not
    loaded.

### Context routines are no longer run

Earlier versions handed routines a hand-built rule context, `ctx`, instead of
`api`. A routine written for it — one without `var routineContract = 2;` — is
a *context routine*, and this version does not run it. Wherever the rules are
loaded (startup, a rule change, a feed import, a catalogue change) such an
active routine is held back, with a warning:

```
[WARN] action rule 12: routine 345 is a context routine, which this version no longer runs — generate the rule anew
```

The rule is then not evaluated at all: its existing actions are neither
created nor withdrawn, as for an undecided rule. Generating the rule anew,
with the current system prompt, fixes it.

### Generating routines

The module's system prompt (Settings → ActionGenerator → System Prompt) must
contain `{{API_CATALOGUE}}`: at generation it is replaced with the catalogue's
read operations — names, parameters, answer fields and descriptions. A system
prompt without it predates the platform API; generation is then refused
before the AI is asked, with the error that the prompt must be replaced with
`prompts/actiongenerator.txt` (see `scripts/extract-prompts.sh` at the repo
root). Applying it to AIManager is the operator's step.

### Validating generated routines

Before a generated routine is stored as `passed`:

1. It must load and declare `var routineContract = 2;` — otherwise it fails with
   "the routine must begin with var routineContract = 2;" — and declare exactly
   the rule's id.
2. Every rule runs in each of the platform's mock scenarios
   (`docs/specs/mock-scenarios.md`), with the bridge answering from the mock
   answers the managers' API documents give per scenario. What each rule
   called and read is recorded, and it is judged on what it decided **and**
   what it read:

| What the rule did | Outcome |
| --- | --- |
| Called an operation it may not, passed an argument the operation does not have, or read a field the answer does not have | Rejected, naming it: *reads things[].stael* |
| Same answer everywhere, reading nothing that differs between scenarios (`return false`) | Rejected (a note for an on-demand rule) |
| Always false, but reading answers that do differ | Accepted, with a note: no scenario happens to meet its condition |
| Always true | Rejected — it would fire for every user and its action could never be withdrawn |

The routine log records, for passed and failed routines alike, what `check()`
returned in each scenario, which operations and fields it read, and the
operations it called; the routine is stored with those operations and the
catalogue fingerprint.

The routine dialog's Test and Context tabs show the same evidence: they run
the routine through AIManager's dry run (`POST /api/dry-run`) in each mock
scenario, against the same mock answers, and show what each rule decided,
the answers it was given and what it read of them.

### Adding a new rule

Write the rule in the WebUI (Settings → Action Rules) and let the AI generate
its routine; there is no rule table in this code to edit. Generation can be
triggered from the rule dialog, and the daemon picks the result up live — no
restart.

If the generated routine is wrong, the lever is the wording of the rule, or the
module system prompt behind it (Settings → ActionGenerator → System Prompt).

### On-demand rules

A rule with an **empty trigger prompt** has no condition of its own. The system
prompt has the model write its `check()` as a plain `return false`, and the rule
produces an action only when something asks for it by name — an action option of
type `Action` whose `typeContext` is that rule's id, which the WebUI turns into a
*forced* evaluation.

Two things treat such a rule differently, and neither can infer it from the
routine:

- **Its actions are never withdrawn.** The usual rule is that an evaluation
  producing nothing means the condition has stopped holding, so the outstanding
  `New` actions of that rule are deleted. For an on-demand rule that is the
  answer every time, so the sweep would delete the action a user had just asked
  for, within seconds. An action raised this way stays until it is acted on or
  deleted.
- **Validation does not reject it.** A `check()` that returns false everywhere
  while reading nothing is normally an error — a condition that could never fire.
  Here it is the specification, so it becomes a note instead. Without this, an
  on-demand rule could not be regenerated at all.

The marker is the empty trigger prompt itself; nothing else needs setting.

### Conditional action options

A rule offers the user a set of **action options** — "Upload host list",
"Install a tool for host detection". Each option may carry a condition, written
in AIManager as plain language on the link between that rule and that option,
and compiled into the rule's routine along with everything else:

```js
{
  name: "identify-missing-host-assets",
  ruleId: 8,
  check: function(api) { ... },
  createAction: function(api) { ... },
  // optional; an option with no entry here always applies
  options: {
    "5": function(api) { var c = api.capabilities.get();
                         return c.automated.indexOf("Host detection") === -1 &&
                                c.manual.indexOf("Host detection") === -1; }
  }
}
```

The functions return **true when the option applies**, are given the same
`api` — and the same bridge pass — as `check()`, and run at the moment an
action is raised, so the options are decided on the data the condition saw. The options that do *not* apply are recorded on the action
as `hiddenOptions`, and the UI offers the rest.

The exclusions are stored rather than the inclusions, so an option added to a
rule later is offered rather than being invisible for having missed a list drawn
up before it existed.

Three behaviours worth knowing:

- **A condition that throws leaves its option visible.** Hiding a step the user
  needs because a generated line was wrong is the worse failure.
- **The set is refreshed, not frozen.** An action that already exists is not
  re-created on a re-evaluation, so the evaluation updates its option set in
  place; and the user dialog asks for a re-evaluation when it opens, so what it
  offers reflects the situation now rather than when the action was raised.
- **Validation notes, rather than rejects, a condition that answers the same way
  in every mock scenario.** Unlike `check()`, an option condition that no mock
  user happens to satisfy is perfectly ordinary.

This lets one rule replace several that differed only in which options they
offered — the three "Identify missing host assets" variants being the case it
was built for.

## Containment

Routine code is written by a model from a prompt, so it is treated as untrusted
input on both sides.

- **Execution is bounded.** goja cannot be preempted, so every call into
  JavaScript — loading a routine, one `check()`, one `createAction()` — runs
  under `ai.js_exec_timeout_seconds` and is interrupted when it overruns.
  Without it, a rule that never terminates would hang its goroutine for the life
  of the process. The call stack is capped as well, so runaway recursion fails
  fast instead of exhausting memory.
- **Each user's evaluation is isolated.** A routine is instantiated in a fresh VM
  for every evaluation (and for every mock scenario during validation), so a
  global a rule sets while looking at one user's data cannot reach the next
  user's evaluation — or their action's text.
- **Output is validated.** An action whose type, status, assignment or priority
  falls outside the vocabulary ActionManager accepts is rejected rather than
  sent, and the free-text fields are truncated to their limits. A routine cannot
  widen the platform's enums by inventing a value.
- **Stored code is verified.** A routine's checksum is recorded when it is
  generated and checked when it is loaded, so code altered in the database after
  validation is refused rather than run. A routine with no checksum is refused
  too: generation and feed imports always record one.

## Architecture

### Components

- **aigenerator** — owns the rules and their routines: loads them from AIManager,
  keeps one executor per rule, asks the AI for a routine when an administrator
  requests one, and stores the result back with its version, model and checksum.
- **codegen** — prompts the AI provider with the module system prompt and checks
  that what comes back declares exactly the rule ID it was asked for.
- **jsruntime** — the goja runtime: runs the routines through the API bridge
  under the execution guard, and validates both generated routines (against
  the catalogue's mock answers) and the actions they return.
- **generator** — the evaluation engine: one bridge pass per evaluation, runs
  every rule for every user (or once for a shared workspace), creates matching
  actions and withdraws stale ones.
- **Watchers** — WebSocket connections, all authenticated with the internal
  service key:
  - **AssetManager, SystemManager, ResourceManager, ActionManager** — the
    messages the active routines' operations are invalidated by; from
    SystemManager also `USER_CREATED` and `USER_DELETED`.
  - **Action rules** — rules created, edited or deleted; explicit evaluation
    requests from the UI; feed imports; API catalogue changes; and AIManager's
    own messages the routines' operations are invalidated by. Debounced per
    rule, so saving a rule several times in quick succession triggers one
    regeneration.
  - **Module AI settings** — the AI assignment and system prompt, re-resolved
    live when an administrator changes them.
- **API clients** — AIManager (rules, routines, AI settings, the API
  catalogue), SystemManager (users, workspace mode), ActionManager (create,
  check and withdraw actions); everything a routine reads goes through the
  API bridge (TachyonikLib `apibridge`, `apiholder`).

### Processing Flow

```
Manager event → Watcher → 2s debounce → Generator → for each user:
                                                      1. Start a bridge pass
                                                      2. Run every routine's check(api),
                                                         which calls the managers' APIs
                                                      3. createAction(api) where it matched
                                                      4. Validate the action
                                                      5. Create it in ActionManager,
                                                         unless it already exists
                                                      6. Withdraw stale New actions of
                                                         rules that no longer match (not
                                                         of rules left undecided)
```

### Event-Driven Operation

- **Idle state** — the daemon waits on its watchers; there is no polling loop.
- **Triggered state** — when a watcher reports a change:
  1. A 2-second debounce collapses a burst of events (an import produces
     hundreds) into one evaluation
  2. A run in progress is not joined by a second one — a full pass over every
     rule and user can outlast the debounce window
  3. Every user is fetched and checked against every loaded rule
  4. An action is created only if its rule matched and the user does not already
     have it

## Log Messages

### Startup

```
[INFO] Starting Tachyonik ActionGenerator 1.4.0 (go1.24.4, linux/arm64)
[INFO] Configuration loaded:
[INFO]   AIManager URL: http://localhost:8085
[INFO]   JS Exec Timeout: 5s
[INFO] API clients initialized
[INFO] Module AI settings loaded: AI=claude, SystemPrompt=18423 chars
[INFO] Generator initialized
[INFO] Processing rules on startup...
[INFO] AIManager settings watcher started
[INFO] Action rule watcher started
[INFO] AssetManager WebSocket watcher started
[INFO] SystemManager WebSocket watcher started
[INFO] ResourceManager WebSocket watcher started
[INFO] ActionManager WebSocket watcher started
[INFO] ActionGenerator daemon is running. Press Ctrl+C to stop.
```

With no AI assigned, startup continues and says so:

```
[INFO] No AI configured — rule evaluation works, but code generation is disabled
```

### Rule Processing

```
[INFO] Change detected, processing rules...
[INFO] Processing action rules...
[INFO] Checking rules for 5 users
[INFO] Using 12 per-rule executors with 12 total rules
[INFO] Creating action 'Identify missing host assets' for user 3 (jdoe) via JS rule
[INFO] Successfully created action ID 42 for user 3
[INFO] Deleted 1 stale action(s) for user 3 rule 8 (rule no longer matches)
[INFO] Created 1 new actions
```

## Troubleshooting

### Daemon not creating actions

1. Check that the services it depends on are running:
   - ResourceManager (port 8080)
   - AssetManager (port 8081)
   - ActionManager (port 8082)
   - SystemManager (port 8083)
   - AIManager (port 8085)
   - ToolManager (port 8086)

2. Check that the internal service keys match the ones those services expect.

3. Check that rules have an active routine. `No JS rules loaded, skipping rule
   processing` means no rule has one that runs — none has been generated,
   generation failed, or the active routines are context routines (`routine …
   is a context routine, which this version no longer runs`), which need
   generating anew.

4. Check that the API catalogue is loaded (`API catalogue loaded: …`). Until it
   is, no rule can be decided.

5. Raise the log level:

   ```bash
   ACTIONGENERATOR_LOG_LEVEL=DEBUG ./tachyonik-actiongenerator
   ```

### A rule never fires

- Check the routine log in the rule dialog. Validation records what `check()`
  returned in each mock scenario and which operations and fields it read, so a
  routine that never fires explains itself.
- Check that the operation the rule reads is in the catalogue and lists the
  messages that change its answer as `x-invalidated-by` — otherwise nothing
  re-evaluates the rule when that data changes.

### A routine keeps timing out

`JS execution budget exceeded` means a routine overran
`ai.js_exec_timeout_seconds`. Raise it if the routine is legitimately heavy;
regenerate the rule if it is not — an unbounded loop in generated code is a
generation fault, not a configuration one.

### Actions created multiple times

- Before creating an action the daemon checks whether the user already has one
  with the same title. Duplicates point at that check, or at a rule whose title
  varies between evaluations — a title built from a changing number produces a
  new action every time it changes.

### High CPU usage

- Events are debounced by 2 seconds and evaluations never overlap. Sustained
  load usually means a routine is expensive rather than that evaluation is too
  frequent; the JS execution budget bounds a single call, not the total.

## Integration

### Pipeline Position

```
AssetManager ─┐
SystemManager ─┤   (WebSocket events;
ResourceManager┼─   API bridge reads)  ─→ ActionGenerator ─→ ActionManager ─→ Actions view
ActionManager ─┤                               ↑
ToolManager ───┘                           AIManager
                                (rules, routines, AI, API catalogue)
```

### Workflow

1. An administrator writes an action rule in the WebUI; AIManager stores it
2. ActionGenerator asks the AI for a routine, validates it, and stores it back;
   it becomes the rule's active routine when picked, or right away when the
   request asked for that
3. A user's data changes; the managers broadcast it
4. ActionGenerator re-evaluates every rule for every user, the routines
   reading through the API bridge
5. Matching rules create actions in ActionManager; rules that stopped matching
   have their outstanding actions withdrawn
6. Users see the result in their Actions view
