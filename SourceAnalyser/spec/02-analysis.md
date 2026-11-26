# SourceAnalyser — 2. Analysing sources

Status: **Accepted** · Area code: `ANA`

Which sources SourceAnalyser analyses and when, how a source's content
becomes the text a routine matches, how the routines decide, what is written
back, and how a test source is handled. What a routine is given and must
return is in [4. Interfaces](04-interfaces.md#2-the-routine).

## 1. Which sources, and when

**SA-ANA-01** SourceAnalyser MUST analyse a source when ResourceManager
announces it with status `Analysing` (`SOURCE_CREATED`, `SOURCE_UPDATED`,
SA-IF-10), and the sources the sweep finds (SA-ANA-19): those waiting
(SA-ANA-02) and those due for another analysis (SA-ANA-03, SA-ANA-04).
*Reason: a new upload must be looked at when it arrives, not on the next
round; and listing every user's sources every few seconds costs with all
sources, not with new ones (decided 2026-11-20, ANA question 2). The sweep is
the safety net for what no message brings.*

**SA-ANA-02** A source whose status is `Analysing` MUST be analysed.
*Reason: `Analysing` is the status ResourceManager gives a new source, and the
one a user or ResourceManager sets to have a source analysed again
(SA-PUR-10).*

**SA-ANA-03** A source of type `Unsupported` and status `Analysed` MUST be
analysed again when it carries no analyser version, or one older than the
running build's (SA-OPS-12).
*Reason: SA-PUR-02. A newer analyser may recognise what an older one did not.
Only `Unsupported` sources are revisited (SA-PUR-10).*

**SA-ANA-04** After the rules have changed — a rule created, changed or
deleted, a feed imported, or the rules loaded again (SA-GEN-16, SA-OPS-08) —
the next sweep MUST analyse again, once, every source of type `Unsupported`
whose status is `Analysed` or `No import routine`, whatever its analyser
version.
*Reason: a rule that exists now may recognise them. `No import routine` is
where SourceImporter parks an `Unsupported` source, so a source that has
already passed the importer must be revisited too. Once: an ordinary sweep must
not analyse every terminal source again.*

**SA-ANA-05** A source analysed again (SA-ANA-03, SA-ANA-04) MUST first be set
to `Analysing`, keeping its type, before it is analysed. When that update
fails, the source MUST be left out of this sweep.
*Reason: the status tells the user and SourceImporter that the source is
being looked at. A source whose status cannot be written cannot have its
result written either.*

**SA-ANA-19** SourceAnalyser MUST go through the whole source list — a
*sweep*, selecting as in SA-ANA-02 to SA-ANA-04 — at startup, on every
(re)connect to ResourceManager, at once after the rules have changed, and
every `sweep_interval_seconds` (300 by default).
*Reason: a message sent while the connection is down is lost, and a
re-analysis because the analyser advanced (SA-ANA-03) or the rules changed
(SA-ANA-04) is announced by no message about the source. Five minutes bounds
how long a lost message can go unnoticed; the events that lose messages sweep
at once anyway.*

**SA-ANA-20** Sources to analyse MUST be queued, each source once. Before
analysing one, SourceAnalyser MUST read it again from ResourceManager, and
analyse it only while it is still `Analysing`; a source that is gone, or a test
source of another module, MUST be skipped. A source being analysed MUST be
queued again when it is announced again meanwhile. The queue MUST NOT be kept
across a restart.
*Reason: a message and a sweep can bring the same source, and its own
`Analysing` (SA-ANA-05) brings it once more; it must be analysed once. The
message may be older than the source: it may have been deleted or analysed
since. What is left at a stop is still `Analysing`, and the startup sweep
finds it.*

**SA-ANA-06** Sources MUST be analysed one after the other. A source that
fails MUST NOT stop the others.
*Reason: one source at a time bounds the memory to the largest content window
(SA-OPS-16). A broken file is that file's problem.*

## 2. The content

**SA-ANA-07** SourceAnalyser MUST download at most `max_content_bytes` of a
source's content. When those bytes are a PDF that fills the window, it MUST
download the PDF again, up to `max_pdf_bytes`. When that second download
fails, the first window is used.
*Reason: identifying a text format needs its beginning, and a bounded window
bounds memory and time. A PDF's text lives in compressed streams spread over
the file, so its beginning says nothing.*

**SA-ANA-08** The content MUST be turned into the text routines match against:
the extracted text of a PDF, and the bytes unchanged for every other format.
Its media type MUST be detected from the bytes (`application/pdf` for a PDF)
and handed to the routines with it.
*Reason: string matching cannot see into a PDF's streams. The media type lets
a rule tell a PDF report from the same report as text. Extraction is
TachyonikLib's `textextract`.*

**SA-ANA-09** A PDF whose text cannot be read — encrypted, scanned without a
text layer, malformed, or too large to parse — MUST be handed to the routines
with empty text and its media type, and MUST NOT stop the daemon. The text of
one PDF MUST be bounded.
*Reason: an unreadable PDF is a source no rule recognises, not a fault. A
small PDF can decompress to an unbounded text ("PDF bomb"), and running out of
memory cannot be recovered from.*

## 3. Deciding the type

**SA-ANA-10** The active routines MUST be run in the order of their rules'
ids, and within a routine its rules in their order. The first that returns a
result decides the source's type and status.
*Reason: the order must be the same on every run, so the same file gets the
same type. Rules that recognise overlapping formats are resolved by order,
not by chance.*

**SA-ANA-11** A rule of a routine that throws, overruns its budget, returns
something other than an object, or returns an empty `sourceType` MUST be
logged with its name and treated as no match; the next rule MUST be run.
*Reason: one broken routine must not keep the other formats from being
recognised.*

**SA-ANA-12** When no rule matches, the source MUST become type `Unsupported`
with status `Analysed`.
*Reason: `Analysed` says the analysis is done; `Unsupported` says no format
was recognised, which is what SA-ANA-03 and SA-ANA-04 revisit.*

**SA-ANA-13** When ResourceManager answers that a source's file does not exist
(404), the source MUST become type `Error` with status `File Not Found`. Any
other failure to fetch it — a timeout, a 5xx, ResourceManager unreachable or
restarting — MUST leave the source `Analysing`, so the next sweep (SA-ANA-19)
analyses it again.
*Reason: the user must see that the file itself is missing, which no new rule
can fix. A passing failure is not that: written as `File Not Found`, a source
whose file exists would never be analysed again (SA-PUR-10) (decided
2026-11-26, ANA question 1).*

## 4. What is written back

**SA-ANA-14** The result MUST be written to ResourceManager through its API,
never its database: the type, the status, and — when the status is
`Analysed` — the analyser version.
*Reason: ResourceManager announces the change (`SOURCE_UPDATED`), which is how
SourceImporter and the WebUI learn of it. The version is what SA-ANA-03
compares; a source not analysed to the end carries none.*

## 5. Test sources

**SA-ANA-15** A test source for SourceAnalyser — one with a `testRoutineId`
and a `testModule` that is empty or `SourceAnalyser` — MUST be analysed with
only the routine it names, fetched from AIManager at that moment, whether or
not that routine is active or passed. Its code MUST match its checksum
(SA-SEC-11). Its result MUST be written back as for any source; no match
gives `Unsupported` / `Analysed`.
*Reason: SA-PUR-03. The administrator tests a draft routine before
activating it; a routine whose code changed after it was stored is refused
here too.*

**SA-ANA-16** When the test routine cannot be fetched, loaded or run, or its
checksum does not match, the test source MUST become type `Error` with status
`Test failed`, and the reason MUST be logged.
*Reason: the WebUI waits for the source to change. A test that cannot run must
say so instead of leaving it waiting.*

**SA-ANA-17** A test source for another module MUST be left untouched.
*Reason: it is that module's test (SourceImporter's import test). Analysing it
would change the type the other test relies on.*

## 6. Audit events

**SA-ANA-18** SourceAnalyser MUST send SystemManager an audit event for the
source's owner, naming the source by name and id:
- `Info` when a rule recognised it, naming the format;
- `Warning` when its content could not be fetched;
- `Warning` when no rule matched, the first time only: a source that is
  `Unsupported` already and still matches nothing sends none.

A test source MUST NOT send one, nor a source without an owner. A failure to
send MUST be logged and MUST NOT change the analysis.
*Reason: the audit log is where a user sees what became of an upload. A revisit
after every rule change would otherwise repeat the same warning for every
unrecognised file. A test is the administrator's experiment, not the user's
history.*

## 7. Known deviations

None known.

## 8. Open questions

None open.
