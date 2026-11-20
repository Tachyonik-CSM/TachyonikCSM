# SourceAnalyser — 1. Purpose and scope

Status: **Accepted** · Area code: `PUR`

## 1. Purpose

SourceAnalyser finds out **what a source is**: which format an uploaded file,
a URL's content or a tool's result has, so that SourceImporter knows which
import routine reads it.

Which formats exist is not written into SourceAnalyser. An administrator
describes a format in plain language as an **analysis rule**. An AI turns that
description into a small JavaScript **routine**, and SourceAnalyser runs the
routines against each source and writes the format they recognise back to
ResourceManager. Supporting a new format means writing a new rule, not
changing this module.

## 2. Terms

Used in this sense throughout the SourceAnalyser specifications. They move to
the shared glossary once it exists.

| Term | Meaning |
|---|---|
| **Source** | A resource held by ResourceManager: an uploaded file, a URL's content or a tool's result, with its owner, type and status. |
| **Source type** | The format a source was recognised as (`OpenVAS Report`, `Nessus XML`), or `Unsupported` when no rule recognised it, or `Error` when its content could not be fetched. |
| **Analysis rule** | An administrator's description of one format: its type (the name of the format, also the rule's title) and an optional rule prompt. Held by AIManager. |
| **Rule prompt** | Optional advice to the AI on how to write the routine for a rule. |
| **Routine** | The JavaScript generated from a rule. Held by AIManager with its version, model, checksum, status (`passed` or `failed`) and validation log. |
| **Active routine** | The routine of a rule that runs. A rule has at most one. |
| **Rule context** (`ctx`) | What a routine is given about one source: its filename, its content as text, its size and its media type (SA-IF-03). |
| **Content window** | The leading bytes of a source that are downloaded and matched (`max_content_bytes`), or the whole of a PDF up to `max_pdf_bytes`. |
| **Analyser version** | The version of the SourceAnalyser build, stamped on every source it has analysed. |
| **Revisit** | Analysing again the sources left `Unsupported`, because the rules changed. |
| **Test source** | A transient source the WebUI creates to try one routine against a real file. It names the routine (`testRoutineId`) and the module that runs it (`testModule`). |
| **Mock context** | One of SourceAnalyser's fixed rule contexts, against which a newly generated routine is tried before it may be used. |

## 3. Responsibilities

**SA-PUR-01** SourceAnalyser MUST analyse every source that is waiting for
analysis, of every user, and write the source type and status the routines
decide back to ResourceManager.
*Reason: SourceImporter imports only what has been identified. A source that
is never analysed is a file the user uploaded for nothing.*

**SA-PUR-02** SourceAnalyser MUST analyse again the sources it left
`Unsupported` when a newer SourceAnalyser runs, and when the analysis rules
change.
*Reason: a format that is supported later must be recognised in the files
already uploaded, without the user uploading them again.*

**SA-PUR-03** SourceAnalyser MUST run a single routine against a test source
when asked to, and write back its result as for any source.
*Reason: an administrator must be able to try a routine — also one that is
not active yet — against a real file, PDF extraction included, before it
decides the type of every user's sources.*

**SA-PUR-04** SourceAnalyser MUST generate a routine for a rule when an
administrator asks for it, with the AI AIManager assigns, validate the result,
and store it in AIManager with its outcome.
*Reason: turning a description of a format into code is the step that needs
an AI. Everything after it is plain code.*

**SA-PUR-05** SourceAnalyser MUST NOT run a routine that has not passed
validation, or whose stored code differs from the code that was validated.
The one exception is the routine a test source names (SA-ANA-15).
*Reason: routine code is written by a model. The validation and the checksum
are what make it trustworthy enough to decide every user's sources (see 3.
and 5.). A test runs on purpose, against one file, with nothing imported.*

**SA-PUR-06** SourceAnalyser MUST keep analysing sources with the active
routines when no AI is configured or the AI cannot be reached.
*Reason: the AI is needed only to generate a routine. Running one is plain
JavaScript, and an AI outage must not stop uploads from being recognised.*

## 4. Non-goals

**SA-PUR-07** SourceAnalyser MUST NOT serve an HTTP API.
*Reason: it is an event-driven daemon. Work a user asks for synchronously goes
through a manager that has an API (project rule, see CLAUDE.md). A routine
test is asked for through ResourceManager, as a test source.*

**SA-PUR-08** SourceAnalyser MUST NOT store anything itself: no database, no
files other than its log.
*Reason: the sources belong to ResourceManager, the rules and routines to
AIManager. Holding a copy would be a second truth that drifts.*

**SA-PUR-09** SourceAnalyser MUST NOT import a source, nor change anything of
it but its type, its status and the analyser version.
*Reason: reading a source's content into assets and findings is
SourceImporter's job. The file itself, its name and its owner are the user's.*

**SA-PUR-10** SourceAnalyser MUST NOT analyse again a source it has
identified, nor one whose content could not be fetched, unless the source is
put back to `Analysing`.
*Reason: an identified source has been or is being imported. Changing its
type afterwards would leave it imported under a format it no longer has. A
user or ResourceManager re-analyses a source by setting it to `Analysing`.*

**SA-PUR-11** SourceAnalyser MUST NOT decide, outside the routines, what
format a source has: no format, file signature or source type is written in
its code. The media type it detects (SA-ANA-08) is handed to the routines,
not used to decide.
*Reason: formats are authored as rules so that an administrator can add one
without a release.*

**SA-PUR-12** SourceAnalyser MUST NOT manage rules, AI assignments or system
prompts. It reads them, and writes back only generated routines and the
outcome of a generation request.
*Reason: they are authored in the WebUI and held by AIManager.*

**SA-PUR-13** SourceAnalyser MUST NOT send a source's content, or anything
from it, to an AI.
*Reason: generation needs only the rule. A source describes an organisation's
systems and weaknesses, and the AI may be an external provider. See
SA-SEC-06.*

## 5. Place among the services

```
Upload / URL / tool result → ResourceManager → SourceAnalyser → SourceImporter → AssetManager
                                                 (identifies)     (imports)
```

SourceAnalyser is called by nobody: it has no API. It learns of new sources
and rule changes from ResourceManager's and AIManager's WebSockets, goes
through the source list as a safety net, and acts by calling the managers.

### What wakes SourceAnalyser

| Service | How | For |
|---|---|---|
| **ResourceManager** | WebSocket: a source created or changed with status `Analysing`; and the sweep (SA-ANA-19) | the sources to analyse, test sources included |
| **AIManager** | WebSocket: rule created, changed or deleted; a feed imported; the module's AI setting changed | the rules, routines, generation requests, the AI to generate with |

### Whom SourceAnalyser calls

| Service | For | Direction |
|---|---|---|
| **ResourceManager** | the sources of every user, one source, and their content | reads |
| | a source's type, status and analyser version | writes |
| **AIManager** | rules, routines, the module's AI setting and system prompt, AI entries | reads |
| | generated routines; the outcome of a generation request | writes |
| **SystemManager** | audit events about analysed sources; liveness heartbeat | sends |
| **The assigned AI** | the routine for a rule | sends the rule, receives code |

SourceAnalyser contacts nothing else.

## 6. Where the details are

| Topic | Specification |
|---|---|
| Which sources are analysed, content extraction, running the routines, what is written back, re-analysis, test sources, audit events | [2. Analysing sources](02-analysis.md) |
| Generation requests, choosing the AI, what the AI is given, validation, storing and activating routines | [3. Routine generation and validation](03-generation-and-validation.md) |
| The routine contract, the rule context, the result, messages consumed, calls made | [4. Interfaces](04-interfaces.md) |
| Service keys, whose data a routine sees, containment of generated code, what reaches the AI and the log | [5. Security and containment](05-security-and-containment.md) |
| Configuration, startup, shutdown, version, liveness, logging | [6. Operation](06-operation.md) |

## 7. Open questions

None open.
