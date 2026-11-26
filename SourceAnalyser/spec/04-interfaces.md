# SourceAnalyser — 4. Interfaces

Status: **Accepted** · Area code: `IF`

What a routine must look like, what it is given and what it returns; the mock
contexts it is validated against; the messages SourceAnalyser reacts to; and
the calls it makes.

## 1. Where the routine contract is written down

The contract below is what the module's system prompt (Settings →
SourceAnalyser → System Prompt, extracted to `prompts/sourceanalyser.txt`)
describes to the AI. A change to it is made here, in the code and in the
system prompt together; applying the prompt is the operator's step.

## 2. The routine

**SA-IF-01** A routine MUST be ES5.1 JavaScript that defines a global `rules`:
an array of objects, each with
- `name`: the rule's name, used in the log;
- `ruleId`: the id of the analysis rule it implements;
- `analyze(ctx)`: the result (SA-IF-04) when the source has the rule's
  format, `null` or `undefined` otherwise.

A routine without `rules`, with a `rules` that is not an array, or with a rule
lacking `name` or `analyze`, or whose `analyze` is not a function, MUST fail
to load, naming what is wrong.
*Reason: the engine (goja) runs ES5.1. A routine of another shape would load
and recognise nothing, without a word.*

**SA-IF-02** A routine MAY define helper functions and variables at top
level. They MUST NOT carry anything from one source to the next (SA-SEC-10).
*Reason: helpers keep the checks readable. Each source gets a fresh engine, so
a global cannot hold state.*

## 3. The rule context

**SA-IF-03** `analyze()` MUST be given one object with:

| Member | Meaning |
|---|---|
| `filename` | the source's name as ResourceManager holds it |
| `content` | the source's text (SA-ANA-08): at most the content window, the extracted text of a PDF, empty for a PDF without readable text |
| `fileSize` | the size ResourceManager holds, in bytes, or `null` when it has none (a URL) |
| `mimeType` | the detected media type, e.g. `application/pdf`, `text/xml; charset=utf-8`, `application/octet-stream` |

Nothing else.
*Reason: this is what recognising a format needs. The owner, the source's id
and every other field stay out, so a routine cannot decide by whose file it
is (SA-SEC-04).*

## 4. The result

**SA-IF-04** `analyze()` returns an object with `sourceType`, the name of the
format. Other members — a `status` included — MUST be ignored. An empty or
missing `sourceType` MUST be treated as no match (SA-ANA-11).
*Reason: the type is what SourceImporter chooses its import routine by; one
without a name cannot be imported. The status is not the routine's to say
(SA-IF-05).*

**SA-IF-05** The `sourceType` MUST be written to ResourceManager as the
routine returned it; the status written with it MUST be `Analysed`, whatever
the routine returned (SA-ANA-14; decided 2026-11-25, IF question 1).
*Reason: the type is free text by design — a new format is a new name, which
the import rules use. The status is not: SourceImporter imports only
`Analysed` sources, and only those get the analyser version, so a routine
returning anything else (`analysed`, `Imported`) left its source unimported
and never revisited, without an error. A recognised source is analysed by
definition, so SourceAnalyser says so itself; routines stored before keep
working.*

## 5. Mock contexts

**SA-IF-06** SourceAnalyser MUST validate a routine against at least these
mock contexts, each with a size of 2048 bytes:

| Context | `filename` | `mimeType` | `content` |
|---|---|---|---|
| Nessus XML | `scan_results.nessus` | `text/xml; charset=utf-8` | the beginning of a `NessusClientData_v2` document |
| OpenVAS XML | `openvas-report.xml` | `text/xml; charset=utf-8` | the beginning of an OpenVAS `<report id=…>` |
| OpenVAS PDF | `openvas-report.pdf` | `application/pdf` | the text extracted from an OpenVAS PDF report |
| Unsupported | `random.bin` | `application/octet-stream` | binary bytes that match nothing |

*Reason: they cover text, PDF and binary content, and a recognised and an
unrecognised file, so a routine that cannot cope with one of them shows it
before it runs (SA-GEN-11).*

## 6. Messages SourceAnalyser reacts to

Over the `/ws` endpoint AIManager and ResourceManager offer internal services,
authenticated with the internal service key (SA-SEC-01). The connections are
TachyonikLib's `aimwatcher` and `wswatcher`.

**SA-IF-07** From **AIManager**:

| Message | Payload | SourceAnalyser |
|---|---|---|
| `ANALYSIS_RULE_CREATED`, `ANALYSIS_RULE_UPDATED` | `{"id"}` | reloads that rule and its active routine, handles a pending generation request (SA-GEN-01), revisits `Unsupported` sources (SA-ANA-04) |
| `ANALYSIS_RULE_DELETED` | `{"id"}` | stops running the rule, revisits `Unsupported` sources |
| `FEED_IMPORTED` | none | reloads all rules, routines and the AI setting, revisits `Unsupported` sources |
| `MODULE_AI_SETTING_UPDATED` for `sourceanalyser` | `{"moduleName"}` | reads the AI and system prompt again |

A rule's messages MUST be debounced per rule (5 seconds), so that a burst of
saves is handled once.
*Reason: the rule dialog saves a rule in several steps; each would otherwise
reload it, and with a pending request generate it more than once.*

**SA-IF-08** A (re)connect to AIManager MUST be handled as a feed import and a
settings change together.
*Reason: SA-GEN-16, SA-OPS-08: the connection may have missed any change.*

**SA-IF-10** From **ResourceManager**:

| Message | Payload | SourceAnalyser |
|---|---|---|
| `SOURCE_CREATED`, `SOURCE_UPDATED` with `status` `Analysing` | the source | queues it (SA-ANA-20) |
| `SOURCE_CREATED`, `SOURCE_UPDATED` with any other status | the source | nothing |

A (re)connect MUST start a sweep (SA-ANA-19).
*Reason: `Analysing` is what a source waiting for SourceAnalyser has, whoever
set it. Its own results come back as `SOURCE_UPDATED` with another status, so
writing a result causes no loop.*

**SA-IF-09** A message of any other type, or one whose payload cannot be read,
MUST be ignored.
*Reason: the managers broadcast many types to all internal listeners; a new
one must not disturb this daemon.*

## 7. Calls SourceAnalyser makes

All with the internal service key of the service called (SA-SEC-01).

| Service | Call | For |
|---|---|---|
| ResourceManager | `GET /api/sources`, naming no user | every user's sources, test sources included: the sweep (SA-ANA-19) |
| | `GET /api/sources/{id}` | a queued source read again (SA-ANA-20) |
| | `/ws` | sources announced as waiting (SA-IF-10) |
| | `GET /api/sources/{id}/file` | the content, read up to the content window (SA-ANA-07) |
| | `PATCH /api/sources/{id}` with `sourceType`, `status` and, when `Analysed`, `analyserVersion` | the result (SA-ANA-14) |
| AIManager | `GET /api/internal/analysis-rules`, `/routines/{id}`, `/module-ai-settings/sourceanalyser`, `/ais/{id}`, `/ais/by-name/{name}` | rules, routines, the AI and system prompt |
| | `POST /api/internal/routines` | store a routine (SA-GEN-13) |
| | `PATCH /api/internal/analysis-rules/{id}` with `clearGenerateRequest`, `generateError` and, to activate, `activeRoutine` | clear a generation request (SA-GEN-03, SA-GEN-14) |
| SystemManager | `POST /api/internal/audit-events` | audit events (SA-ANA-18) |
| | heartbeat (TachyonikLib `heartbeat`) | liveness (SA-OPS-10) |
| The assigned AI | its chat call (TachyonikLib `aiclient`) | a routine (SA-GEN-08) |

## 8. Known deviations

None known.

## 9. Open questions

None open.
