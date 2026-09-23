<!--
ActionGenerator
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# ActionGenerator

ActionGenerator is a daemon that turns the state of a user's installation into
**actions** — the concrete next steps shown in the Actions view.

What counts as a next step is not hard-coded. An administrator describes a
situation in plain language as an **action rule** in AIManager; an AI turns that
description into a small JavaScript routine; ActionGenerator evaluates those
routines against each user's context and creates the actions they return.
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

1. Assembles the rule context — organisation, settings, assets and their
   statistics, sources, tools and capabilities, existing actions
2. Runs every loaded routine's `check(ctx)` against it
3. Calls `createAction(ctx)` for each rule that matches
4. Creates the resulting action in ActionManager, unless the user already has it
5. Withdraws the outstanding `New` actions of a rule that has stopped matching

Step 5 is what makes an action disappear once the user has done what it asked,
rather than lingering as stale advice.

The AI is needed only to **generate** a routine from a rule. Evaluating an
already-generated routine is plain JavaScript, so the daemon keeps producing
actions with no AI configured — only code generation is refused.

### SaaS vs. Appliance mode

The daemon reads the instance operating mode from SystemManager (`GET /api/internal/workspace`) and adapts how it generates:

- **SaaS mode** — each user has private data, so rules are evaluated per user and actions are owned by that user (the behavior above).
- **Appliance mode** — the whole instance is one shared workspace: every non-anonymous user sees the same data, so generating one action set per user would surface as duplicates in the shared Actions view. Instead, each rule is evaluated **once** against the aggregate of all users' data, and the resulting actions are created under a single **primary owner** (the oldest admin, reported as `primaryUserId`). Any per-user duplicates left by earlier generation are consolidated away under that owner. If the mode can't be determined, the daemon falls back to per-user generation.

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
| `tachyonik/lib` | Shared logger, REST client, heartbeat, AIManager watcher, WebSocket watcher, generated-code cleanup, JS context tracing (local `replace`) |
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
`1.0.0-dirty`); a tree with no matching tag falls back to `0.0.0-dev`.

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

### Configuration File

Default configuration file: `./config.yaml`, overridable with
`ACTIONGENERATOR_CONFIG`.

`config.yaml.example` is the annotated reference — every key, its default, its
environment variable and what the block is for. In outline:

```yaml
assetmanager:     # assets and statistics for the rule context (read-only)
  url: http://localhost:8081
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

systemmanager:    # users, organisation, settings, workspace mode; heartbeat out
  url: http://localhost:8083
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

resourcemanager:  # sources, tools, source quota (read-only)
  url: http://localhost:8080
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

actionmanager:    # where actions are created and withdrawn
  url: http://localhost:8082
  internal_service_key: "CHANGE-THIS-INTERNAL-SERVICE-KEY"

ai_manager:       # rules, routines, AI assignment and system prompt
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
| `AIMANAGER_INTERNAL_SERVICE_KEY` | AIManager service key | - |
| `ACTIONGENERATOR_AI_TIMEOUT_SECONDS` | HTTP timeout for AI code-generation requests | `300` |
| `ACTIONGENERATOR_JS_EXEC_TIMEOUT_SECONDS` | Budget for a single JS execution — loading a routine, one `check()`, one `createAction()`. Routine code is AI-generated and goja cannot be preempted, so without this a rule that never terminates hangs its goroutine for the life of the process. `0` disables the guard | `5` |
| `ACTIONGENERATOR_HEARTBEAT_INTERVAL_SECONDS` | Liveness heartbeat interval to SystemManager | `10` |
| `ACTIONGENERATOR_LOG_FILE` | Log file path | `./actiongenerator.log` |
| `ACTIONGENERATOR_LOG_TO_CONSOLE` | Log to console (true/false) | `true` |
| `ACTIONGENERATOR_LOG_TO_FILE` | Log to file (true/false) | `true` |
| `ACTIONGENERATOR_LOG_LEVEL` | Log level (DEBUG, INFO, WARN, ERROR) | `INFO` |

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
4. Load the action rules and their active routines, building one executor per rule
5. Evaluate every rule for every user once, on startup
6. Start the watchers (AssetManager, SystemManager, action rules, module AI
   settings) and the SystemManager heartbeat
7. Re-evaluate whenever a watcher reports a change

### Stopping the Daemon

Send `SIGINT` (Ctrl+C) or `SIGTERM` to shut down gracefully:

- The evaluation in progress completes
- The WebSocket watchers and the heartbeat are closed
- The log file is closed

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
validates the result, and stores it back as the rule's active routine.

A routine is an array of rule objects, each with a `check(ctx)` that decides
whether the rule applies and a `createAction(ctx)` that describes the action to
raise.

### When rules are re-evaluated

An action is created when its rule matches and **garbage-collected when it stops
matching** — but only when something re-runs the rules. `ProcessRules` is
triggered by:

| Trigger | Fires on |
| --- | --- |
| Startup | daemon start |
| AssetManager WebSocket | asset / vulnerability changes (2s debounce) |
| SystemManager WebSocket | user, organisation and settings changes (2s debounce) |
| Action-rule watcher | a rule created, edited or deleted; an evaluation request; a feed import |

There is deliberately **no periodic sweep**: staleness is fixed by watching the
data the context is built from, not by re-running on a timer. The consequence is
that a context source with no watcher goes stale silently, so adding a field to
`RuleContext` means asking where its change events come from. Currently
unwatched: sources (ResourceManager), tools and capabilities (ResourceManager +
AIManager), and existing actions (ActionManager) — a rule reading those is
re-evaluated only when one of the triggers above happens to fire.

### Rule context

Every rule is a JavaScript object with `check(ctx)` and `createAction(ctx)`, and `ctx` is
assembled per user by `buildJSRuleContext`. It carries:

| Field | What it is |
| --- | --- |
| `ctx.user` | id, username, email, role, createdAt, maxHostAssets |
| `ctx.organisation` | The organisation record: name, assetsHosts, score, homepageUrl, naceCode, employees, employeesAffiliated, the postal address, companyLegalName, vatNumber, vatNumberValid, itStaffHourlyCost, currency, and `itProviders` |
| `ctx.organisation.itProviders` | The IT service providers the organisation recorded: an array of `{name, homepageUrl, contactEmail, types}`, where `types` is an array of `"cybersecurity"`, `"network_infrastructure"`, `"general_it_support"`. Notes and logos are not included |
| `ctx.questionnaires` | Progress and verdicts of the three organisation questionnaires, computed by SystemManager from the stored answers: `nis2Impact` (progress, complete, level `red`/`orange`/`yellow`/`green`, size, expired, …), `nis2Readiness` (progress, complete, score, level `red`/`yellow`/`green`, gaps, reportingCritical, …) and `cyberStrategy` (progress, complete, missingRequired, expired). Levels are `""` until complete; the strategy's `complete` means all required parts, not progress 100 |
| `ctx.settings` | What the user configured for themselves: `ai.{research,chat,dashboard}` as a state (`personal` / `internal` / `disabled` / `none`), `ai.personalProviderCount`, and `language` |
| `ctx.sources`, `ctx.sourceCount`, `ctx.sourceMax` | Uploaded sources and the user's limit |
| `ctx.assets`, `ctx.assetCount`, `ctx.highestScoreAsset` | Assets, and the highest-scoring one (or `null`) |
| `ctx.assetStats` | Severity buckets from AssetManager's `/api/assets/stats` |
| `ctx.capabilities` | `{automated, manual}` capability names covered by the user's configured **tools** |
| `ctx.actions`, `ctx.actionCount` | Existing actions |

Two distinctions the rules depend on:

- **`ctx.settings` is not `ctx.capabilities`.** Capabilities are what a user's *tools* can do —
  scanning, detection. AI features (Research, Chat, Dashboard) are configuration and live in
  `ctx.settings`. A rule about an unconfigured AI provider that searches `ctx.capabilities` will
  never fire, because no capability is named after an AI feature.
- **`ctx.organisation` is a top-level object.** `ctx.user` also carries `organisationName`,
  `organisationScore` and `organisationHostAssets` as legacy aliases, kept so routines generated
  before `ctx.organisation` existed keep working. New rules use `ctx.organisation`.

Every field is always defined — strings default to `""`, numbers to `0`, booleans to `false`,
arrays to `[]` (`itProviders`, and each provider's `types`), including when the backing service
call fails. `ctx.highestScoreAsset` is the one exception: it is `null` when no asset is scored,
because that is a real state a rule must test for. When the questionnaire summary cannot be fetched
a rule sees every questionnaire as not started. `ctx.questionnaires.nis2Readiness.reportingScore`
is `0` rather than `null` when unanswered — `null < 50` is true in JavaScript — with
`reportingAnswered` to tell the cases apart.

### Validating generated routines

Before a generated routine is stored as `passed`, `ValidateWithMockCtxReport` runs every rule
against six mock scenarios (`jsruntime.MockScenarios`): an anonymous user with nothing configured,
an admin with no data, and a registered user with everything filled in, four times over with
different questionnaire states, so that every Impact and Readiness level appears in some scenario and
not in another.

The context each rule sees is traced (`tachyonik/lib/jsctxtrace`), so validation judges a rule by what
it decided **and** what it read:

| What the rule did | Outcome |
| --- | --- |
| Read a field that exists in no scenario | Rejected, naming the field: *reads ctx.x.levle, which does not exist* |
| Same answer everywhere, reading nothing that differs between scenarios (`return false`) | Rejected |
| Always false, but reading fields that do differ | Accepted, with a note: no scenario happens to meet its condition |
| Always true | Rejected — it would fire for every user and its action could never be withdrawn |

The routine log records, for passed and failed routines alike, what `check()` returned in each
scenario and which fields it read, so a routine that never fires explains itself.

The scenarios are generated into `WebUI/src/generated/actionRuleMockContexts.json`, which the
routine dialog's Test and Context tabs use, and `TestMockContextsFileIsCurrent` fails when that file
is stale. After changing the scenarios or the context shape, regenerate it:

```bash
UPDATE_MOCK_CONTEXTS=1 go test ./internal/jsruntime/ -run TestMockContextsFileIsCurrent
```

One copy of the context shape is still kept by hand: ActionExecutor's dry-run defaults
(`actionContextDefaults`), which fill in fields a dry-run context omits. A field added to
`RuleContext` belongs there too. The AI writes rules from a system prompt stored in AIManager
(Settings → ActionGenerator → System Prompt) that documents this context — a field the prompt does
not mention is a field no generated rule will use, however well it is populated.

### Adding a new rule

Write the rule in the WebUI (Settings → Action Rules) and let the AI generate
its routine; there is no rule table in this code to edit. Generation can be
triggered from the rule dialog, and the daemon picks the result up live — no
restart.

If the generated routine is wrong, the lever is the wording of the rule, or the
module system prompt behind it (Settings → ActionGenerator → System Prompt).

## Containment

Routine code is written by a model from a prompt, so it is treated as untrusted
input on both sides.

- **Execution is bounded.** goja cannot be preempted, so every call into
  JavaScript — loading a routine, one `check()`, one `createAction()` — runs
  under `ai.js_exec_timeout_seconds` and is interrupted when it overruns.
  Without it, a rule that never terminates would hang its goroutine for the life
  of the process. The call stack is capped as well, so runaway recursion fails
  fast instead of exhausting memory.
- **Output is validated.** An action whose type, status, assignment or priority
  falls outside the vocabulary ActionManager accepts is rejected rather than
  sent, and the free-text fields are truncated to their limits. A routine cannot
  widen the platform's enums by inventing a value.
- **Stored code is verified.** A routine's checksum is recorded when it is
  generated and checked when it is loaded, so code altered in the database after
  validation is refused rather than run.

## Architecture

### Components

- **aigenerator** — owns the rules and their routines: loads them from AIManager,
  keeps one executor per rule, asks the AI for a routine when a rule has none,
  and stores the result back with its version, model and checksum.
- **codegen** — prompts the AI provider with the module system prompt and checks
  that what comes back declares exactly the rule ID it was asked for.
- **jsruntime** — the goja runtime: builds the context, runs the routines under
  the execution guard, and validates both generated routines and the actions
  they return.
- **generator** — the evaluation engine: assembles each user's context, runs
  every rule, creates matching actions and withdraws stale ones.
- **Watchers** — four WebSocket connections, all authenticated with the internal
  service key:
  - **AssetManager** — asset and vulnerability changes.
  - **SystemManager** — user, organisation and settings changes (`USER_*`,
    `ORGANISATION_UPDATED`). A rule's context is not only assets, so a change to
    the organisation record or to a user's own settings must wake the daemon too.
  - **Action rules** — rules created, edited or deleted; explicit evaluation
    requests from the UI; feed imports. Debounced per rule, so saving a rule
    several times in quick succession triggers one regeneration.
  - **Module AI settings** — the AI assignment and system prompt, re-resolved
    live when an administrator changes them.
- **API clients** — AIManager (rules, routines, AI settings, tool rules),
  SystemManager (users, organisation, settings, workspace mode), AssetManager
  (assets and statistics), ResourceManager (sources, tools, quota), ActionManager
  (create, check and withdraw actions).

### Processing Flow

```
Manager event → Watcher → 2s debounce → Generator → for each user:
                                                      1. Build ctx (SystemManager,
                                                         AssetManager, ResourceManager,
                                                         AIManager)
                                                      2. Run every routine's check(ctx)
                                                      3. createAction(ctx) where it matched
                                                      4. Validate the action
                                                      5. Create it in ActionManager,
                                                         unless it already exists
                                                      6. Withdraw stale New actions of
                                                         rules that no longer match
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
[INFO] Starting Tachyonik ActionGenerator daemon...
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
[INFO] ActionGenerator daemon is running. Press Ctrl+C to stop.
```

With no AI assigned, startup continues and says so:

```
[INFO] No AI configured — rule evaluation works, but code generation is disabled
```

### Rule Processing

```
[INFO] Asset/Vulnerability change detected, processing rules...
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

2. Check that the internal service keys match the ones those services expect.

3. Check that rules have an active routine. `No JS rules loaded, skipping rule
   processing` means no rule has one — either none has been generated, or
   generation failed.

4. Raise the log level:

   ```bash
   ACTIONGENERATOR_LOG_LEVEL=DEBUG ./tachyonik-actiongenerator
   ```

### A rule never fires

- Check the routine log in the rule dialog. Validation records what `check()`
  returned in each mock scenario and which context fields it read, so a routine
  that never fires explains itself.
- Check that the field the rule reads is one the system prompt documents, and
  that the rule reads `ctx.organisation`, not `ctx.user.organisation`.

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
SystemManager ─┼─ (WebSocket events) ─→ ActionGenerator ─→ ActionManager ─→ Actions view
ResourceManager ┘                            ↑
                                        AIManager
                                   (rules, routines, AI)
```

### Workflow

1. An administrator writes an action rule in the WebUI; AIManager stores it
2. ActionGenerator asks the AI for a routine, validates it, and stores it back
   as the rule's active routine
3. A user's data changes; the managers broadcast it
4. ActionGenerator re-evaluates every rule against every user's context
5. Matching rules create actions in ActionManager; rules that stopped matching
   have their outstanding actions withdrawn
6. Users see the result in their Actions view
