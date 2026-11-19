# AssetManager — 1. Purpose and scope

Status: **Accepted** · Area code: `PUR`

## 1. Purpose

AssetManager is TachyonikCSM's record of **what an organisation has and what
was found on it**: the assets (for now, hosts), the
vulnerabilities and detections found on those assets, and which source
reported what.

It is the one place the rest of the platform asks "which assets does this
user have, how exposed are they, and how much of the declared estate is
actually known?" — for the WebUI, for the rules that raise actions, for the
routines that act on assets, and for the organisation's scores.

## 2. Terms

Used in this sense throughout the AssetManager specifications. They move to
the shared glossary once it exists.

| Term | Meaning |
|---|---|
| **Asset** | Something an organisation owns and wants covered. Identified per user by its name and type. For now the only type is `Host`: a machine, named by its address. |
| **Source** | Where data came from: an imported file or a tool run, held by ResourceManager and referred to by its id and filename. |
| **Link** | The record that a source reported an asset. An asset can be reported by several sources and keeps one link per source. |
| **Finding** | A vulnerability or a detection on a host. |
| **Vulnerability** | A weakness found on a host, with a severity. |
| **Detection** | Something observed on a host without being a weakness (e.g. a service or product found running), with no severity. |
| **Unmanaged asset** | An asset with no findings at all: nothing says anything about its state. |
| **Scan coverage** | That an asset is part of a scheduled vulnerability scan, and how often that scan runs. |

## 3. Responsibilities

**AM-PUR-01** AssetManager MUST hold each user's assets, vulnerabilities and
detections, and for every asset the sources that reported it.
*Reason: everything else in this list follows from having that record in one
place.*

**AM-PUR-02** AssetManager MUST accept this data only from the import of a
source, by a service (AM-PUR-20).
*Reason: the data arrives from SourceImporter, never from AssetManager's own
observation, and not from users or routines directly: a routine that adds
hosts creates a source.*

**AM-PUR-03** AssetManager MUST keep an asset's derived values — severity,
score, last seen, last scanned — computed from the evidence that still refers
to it, whenever that evidence changes.
*Reason: these values are never entered; a value computed once and left
standing outlives the evidence it came from.*

**AM-PUR-04** AssetManager MUST remove, when a source goes, exactly what that
source contributed — its findings and its links — and an asset only when no
source still reports it.
*Reason: an asset known from two sources must survive the deletion of one;
otherwise re-importing or deleting a file destroys data another file
supports.*

**AM-PUR-05** AssetManager MUST let a re-import of a source remove what the
new run no longer reports, without touching what it reports again.
*Reason: a host that disappeared from a scan must disappear from the record,
and one that is still there must not be deleted and re-created.*

**AM-PUR-06** AssetManager MUST record the scan coverage that an import
states, and only that.
*Reason: coverage is a fact about the scanner's schedule; see AM-PUR-12.*

**AM-PUR-07** AssetManager MUST enforce the limits on how many host assets,
vulnerabilities and detections a user may hold, as SystemManager states
them, and on an appliance the instance-wide licensed host-asset count.
*Reason: the limits belong to the user's plan and the appliance's licence,
and the place that stores the records is the only one that can count them.*

**AM-PUR-08** AssetManager MUST compute the organisation's **Exposure** and
**Coverage** sub-scores from its records and report them to SystemManager
when they may have changed.
*Reason: they are derived from assets and findings, which only AssetManager
holds; SystemManager owns the overall score (see the score model).*

**AM-PUR-09** AssetManager MUST announce every change to its records over its
WebSocket, to the users entitled to see them and to the services that react
to changes.
*Reason: an open WebUI and the action rules must follow an import without
polling.*

**AM-PUR-10** AssetManager MUST serve its records only to the users entitled
to them — the owner, the members of the owner's shared workspace (on an
appliance, which is one workspace: every registered user) — and to
authenticated services.
*Reason: the records describe an organisation's weaknesses. The rules are in
4. Security and tenancy.*

**AM-PUR-11** AssetManager MUST remove or hand over a user's records when
SystemManager asks — when an account is deleted, or an anonymous session
becomes a registered account — and report which users it still holds
records for.
*Reason: SystemManager knows who exists; AssetManager must not keep the data
of someone who does not.*

**AM-PUR-20** The sources MUST rule AssetManager's content. A record MUST be
created only by a service importing a source, and MUST be removed only when
its source is deleted or re-imported (AM-PUR-04, -05) — an asset only once no
remaining source refers to it — or when its owner's account goes (AM-PUR-11).
No one, user or routine, MAY create or delete a record by hand.
*Reason: a record means "a source reports this". One entered or deleted by
hand would say something no source says, and would outlive, or anticipate,
the evidence (decided 2026-11-19).*

## 4. Non-goals

**AM-PUR-12** AssetManager MUST NOT derive scan coverage, an asset's
existence, or a finding from anything it holds — not from findings, last seen
dates or other assets.
*Reason: a guess presented as a recorded fact misleads the user about what is
actually scanned.*

**AM-PUR-13** AssetManager MUST NOT scan, probe or contact the assets it
records, nor talk to TachyonikProxy or any tool.
*Reason: discovery is the job of tools run through ToolManager; AssetManager
only records results.*

**AM-PUR-14** AssetManager MUST NOT parse source files or decide what a source
contains.
*Reason: identifying and importing sources is SourceAnalyser's and
SourceImporter's job; AssetManager receives records already extracted.*

**AM-PUR-15** AssetManager MUST NOT store source files, nor own the list of
sources.
*Reason: sources belong to ResourceManager. AssetManager only refers to them
by id and filename.*

**AM-PUR-16** AssetManager MUST NOT manage user accounts, sessions, groups,
plans or limits.
*Reason: they belong to SystemManager. AssetManager reads the limits and
checks that a session's account is active; it decides neither.*

**AM-PUR-17** AssetManager MUST NOT raise or manage actions, nor evaluate
rules.
*Reason: that is ActionGenerator's and ActionManager's job; AssetManager
supplies the records rules are evaluated against.*

**AM-PUR-18** AssetManager MUST NOT compute the overall organisation score, or
the Capability and Compliance sub-scores.
*Reason: SystemManager combines the sub-scores, ToolManager owns Capability,
and SystemManager computes Compliance.*

**AM-PUR-19** AssetManager MUST NOT use AI.
*Reason: what it computes must be reproducible from its records; the AI
features of the platform live in the modules that generate rules and
routines.*

## 5. Place among the services

### Who calls AssetManager

| Caller | For |
|---|---|
| **WebUI** | Lists and filters a user's assets, vulnerabilities and detections; follows changes over the WebSocket |
| **SourceImporter** | Creates the assets, vulnerabilities and detections an import extracts; applies scan coverage tables; deletes what a re-import left stale |
| **ResourceManager** | Deletes a source's records when the source is deleted |
| **ActionGenerator** | Reads assets and their statistics as the context rules are evaluated against; follows changes over the WebSocket to re-evaluate |
| **ActionExecutor** | Lets execution routines read assets and findings; a routine that adds hosts creates a source in ResourceManager |
| **SystemManager** | Asks for a score recalculation, deletes or transfers a user's records, and asks which users AssetManager holds records for |

### Whom AssetManager calls

| Service | For | Direction |
|---|---|---|
| **SystemManager** | A user's limits and whether the account is active; the members of a shared workspace; the organisation's declared estate | reads |
| | The Exposure and Coverage sub-scores; statistics entries (totals); audit events | sends |
| **ResourceManager** | Whether each source the records refer to still exists, for the sources' sweep (AM-DATA-46) | reads |
| **AIManager** | The appliance's licensed host-asset count (appliance only) | reads |

AssetManager calls no other service, and sends nothing outside the platform.

## 6. Where the details are

| Topic | Specification |
|---|---|
| Identity, links, derived values and their rules, deletion semantics, scan coverage, limits | [2. Data model and behaviour](02-data-model-and-behaviour.md) |
| Endpoints, parameters, status codes, filters, WebSocket messages | [3. API](03-api.md) |
| Users and services, shared workspace and appliance mode, sessions, internal endpoints | [4. Security and tenancy](04-security-and-tenancy.md) |
| Startup, schema and module versions, health, metrics | [5. Operation](05-operation.md) |

## 7. Open questions

None is open.
