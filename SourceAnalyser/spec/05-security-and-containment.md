# SourceAnalyser — 5. Security and containment

Status: **Accepted** · Area code: `SEC`

SourceAnalyser holds no data of its own and serves nobody. Its security
questions are therefore three: how it proves itself to the services it calls,
whose data it reads and where that goes, and how it keeps the code it runs —
written by a model — within bounds, against content that anybody may have
uploaded.

## 1. Authentication

**SA-SEC-01** Every call SourceAnalyser makes to a platform service, and every
WebSocket connection it opens, MUST carry that service's internal service key.
*Reason: the internal endpoints serve any user's data to whoever holds the
key. They are refused without it. As AG-SEC-01.*

**SA-SEC-02** The service keys and the AI's API keys MUST NOT be logged, nor be
put into a routine or the context a routine is given.
*Reason: the log is read in support cases and shipped to log collectors; a key
in it is a key handed out. As AG-SEC-02.*

## 2. Whose data goes where

**SA-SEC-03** SourceAnalyser MUST write a source's result to that source only,
and send its audit event to that source's owner only (SA-ANA-18).
*Reason: SourceAnalyser reads every user's sources (SA-ANA-01). A result or an
event on the wrong source would show one user's file to another.*

**SA-SEC-04** A routine MUST be given only the rule context of the source it
analyses (SA-IF-03): no owner, no other source.
*Reason: the format of a file does not depend on whose it is. A routine that
could see the owner could treat users differently, or put one user's data
into what it returns for another.*

**SA-SEC-05** SourceAnalyser MUST NOT send a source's content, or anything from
it, outside the platform.
*Reason: a scan report describes an organisation's systems and weaknesses.*

## 3. What reaches the AI and the log

**SA-SEC-06** What SourceAnalyser sends to an AI MUST be limited to the system
prompt and the rule (SA-GEN-08). No source, no content, no filename and no
user MUST be sent.
*Reason: the AI may be an external provider. A rule is an administrator's text
about a format in general; a source is one organisation's data. As AG-SEC-08.*

**SA-SEC-07** A source's content MUST be logged only at DEBUG, and only its
first 200 bytes.
*Reason: the log at INFO goes to production log collectors. A preview of the
content is what a developer needs to see why a rule did not match; the rest
of the file is not.*

## 4. Containing generated code

Routine code is written by a model from a prompt, and an administrator's
wording can steer it. The content it reads was uploaded by any user, and may
be made to trip it. Both are treated as untrusted on every path.

**SA-SEC-08** Every call into a routine — loading it, reading its `rules`,
each `analyze()`, and reading what it returns — MUST run within the
configured execution budget (SA-OPS-03) and be interrupted when it overruns.
The interruption MUST fail that call only, and MUST NOT stop the daemon.
*Reason: the engine cannot be preempted. A runaway loop or a catastrophic
regular expression — which a crafted file can trigger — would hang the daemon
for good, and every later upload with it. Reading the rules and the returned
object runs getters and `toString` the routine defines, so it needs the
budget too. As AG-SEC-09.*

**SA-SEC-09** The call stack of a routine MUST be bounded, so that runaway
recursion fails as an error.
*Reason: the budget stops a loop; recursion exhausts the stack, faster than
the timer fires, and a Go stack overflow cannot be recovered. As AG-SEC-10.*

**SA-SEC-10** Every analysis of a source, and every mock context of a
validation, MUST run the routine in a fresh JavaScript engine.
*Reason: a global a routine sets while reading one user's file must not reach
the next file, another user's included. As AG-SEC-12.*

**SA-SEC-11** A routine MUST be given nothing but its rule context: no access
to the network, files, processes, or functions of the daemon. Stored routine
code MUST match its recorded checksum before it runs — the active routine
(SA-GEN-15) and a test routine (SA-ANA-15) alike.
*Reason: a routine needs to recognise, not to act; whatever is not handed in
cannot be misused. Code changed in the database after it was validated must
not run.*

**SA-SEC-12** Extracting the text of a PDF MUST be bounded in the input it
parses and the text it produces, and a fault in the parser MUST fail that
source only (SA-ANA-09).
*Reason: the PDF is a file anybody may upload, and the parser is a library
that panics on malformed input.*

## 5. Known deviations

None known.

## 6. Open questions

None open.
