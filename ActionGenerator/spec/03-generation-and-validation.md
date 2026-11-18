# ActionGenerator — 3. Routine generation and validation

Status: **Accepted** · Area code: `GEN`

How a rule becomes a routine that may run: when ActionGenerator generates
one, which AI it uses and what it gives it, how the result is checked, and how
it is stored and put to work. What a routine must look like is in
[4. Interfaces](04-interfaces.md#2-the-routine). What protects the daemon
from the code is in [5. Security and containment](05-security-and-containment.md).

## 1. When a routine is generated

**AG-GEN-01** ActionGenerator MUST generate a routine for a rule when, and
only when, AIManager announces a change to a rule that carries a pending
generation request.
*Reason: a generation costs an AI call and replaces nothing on its own
(AG-GEN-15). It is an administrator's decision, made in the rule dialog —
not a side effect of saving a rule or of a rule having no routine.*

**AG-GEN-02** Once a generation request has been handled, whatever its
outcome, ActionGenerator MUST clear it in AIManager, with the reason when it
failed. A request still standing at startup MUST be cleared with the reason
that ActionGenerator was restarted before it could handle it.
*Reason: a request left standing would be generated again on every change to
the rule, in a loop. The reason belongs in the rule dialog, not only in this
module's log. A request is handled when its rule's change is announced; one
standing at startup was cut off by a stop (AG-OPS-09) or made while the daemon
was not running, and no announcement will come for it any more.*

**AG-GEN-03** A rule without a rule prompt MUST be generated from the rule
alone.
*Reason: the rule prompt is optional advice on variables. Refusing without it
made the Generate button appear to do nothing.*

## 2. The AI and what it is given

**AG-GEN-04** The AI MUST be chosen when the generation starts, by asking
AIManager: the AI set on the rule if there is one, otherwise the module's
current default; and the module's current system prompt. Only when AIManager
cannot be asked MAY the AI and system prompt loaded last be used.
*Reason: a missed change notification must not leave the module generating
with an AI that is no longer configured. Implemented in TachyonikLib's
`aipick`, shared with the other generating modules.*

**AG-GEN-05** Every log line about a generation, and every generation error,
MUST name the AI it used and why that one: set on the rule, module default,
or loaded last.
*Reason: a refused request must say which account refused it.*

**AG-GEN-06** Generation MUST be refused, with a message saying where to set
it, when the system prompt is empty.
*Reason: the system prompt documents the routine contract and where the API
catalogue goes (AG-GEN-18). Code generated without it would be written
against nothing.*

**AG-GEN-07** The AI MUST be given the system prompt and the rule as AIManager
holds it — its id, title, trigger prompt, rule prompt, action type, message
and priority, with its bookkeeping (assigned AI, active routine, request and
change times) — and for each of the rule's options that carries a visibility
condition for this rule, the option's id, title and that condition. Nothing
about any user.
*Reason: this is all a routine needs to be written. Options without a
condition are left out: a function returning true for them can only be
wrong. No user data goes to the AI (AG-SEC-08).*

**AG-GEN-08** When the rule's options cannot be read, the routine MUST still
be generated, without option conditions, and this MUST be logged.
*Reason: every option is then shown, which is the behaviour of a rule without
conditions — better than no routine.*

**AG-GEN-09** The answer MUST be cleaned before it is checked: a Markdown code
fence around it removed, typographic quotation marks replaced by ASCII ones.
A header naming the rule's id and title MUST be put in front.
*Reason: models wrap code in fences and reach for smart quotes; both are
syntax errors to the engine. The header tells a reader of the stored routine
what it is for. The cleaning is TachyonikLib's `aicode`.*

## 3. Validation

A generated routine is checked in this order. The first check that fails ends
the generation, and the routine is stored as `failed` (AG-GEN-14).

**AG-GEN-10** The routine MUST load (AG-IF-08), MUST declare
`var routineContract = 2;` — failing otherwise with "the routine must begin
with var routineContract = 2;" — and MUST declare the rule's id and no other.
*Reason: a context routine would never be run (AG-EVAL-24), so storing one
as passed would be storing a rule without advice. A model that invents or
renumbers rules would otherwise have its output stored against the wrong
rule.*

**AG-GEN-11** Every rule of the routine MUST be run in every mock scenario
(PA-VAL-01), with the bridge answering from the mock answers of the
catalogue's API documents, and with what it calls and reads recorded
(PA-VAL-02). The routine MUST fail when:
- it fails to load for a scenario;
- `check()`, `createAction()` or an option condition throws or overruns its
  budget in a scenario;
- it calls an operation that is not a bridge operation, passes an argument
  the operation does not have, or reads a field the answer's schema does not
  have — each of which MUST be named;
- `check()` returns true in every scenario;
- `check()` returns false in every scenario while reading nothing, or only
  answers that are the same in every scenario — unless the rule is on-demand
  (AG-EVAL-15).

*Reason: judging by the answers alone cannot tell a broken rule from a
correct one no scenario happens to meet; what it read separates them. A call
or field the documents do not have is a typo or an invention. A rule true
everywhere would fire for every user and could never be withdrawn. One false
everywhere without reading anything that varies has no condition — which is
exactly what an on-demand rule's `return false` is meant to be.*

**AG-GEN-12** These MUST be recorded as notes, and MUST NOT fail the routine:
- `check()` returns false in every scenario but reads answers that differ
  between them;
- an option condition answers the same in every scenario where its rule
  fires;
- an on-demand rule's `check()` returns false everywhere.

*Reason: each is what a correct rule can look like when no mock user is in
the situation it is for. An option no mock user needs is ordinary; failing
the routine for it would make the scenarios a specification of which
conditions may exist.*

**AG-GEN-13** Each scenario MUST be run in a fresh JavaScript engine, as an
evaluation is (AG-SEC-12).
*Reason: a global set in one scenario would otherwise colour the next, and
the judgement of AG-GEN-11 would be judging that.*

## 4. Storing and activating

**AG-GEN-14** Every generated routine MUST be stored in AIManager, passed or
failed, with: the code, the rule's id, a version from the time of generation,
the model, the SHA-256 of the code, the time, the status, and a log. The log
MUST hold the validation errors and notes, for every rule what `check()`
returned in each scenario and which operations and fields it read, and the
operations the routine called.
*Reason: a failed routine must be inspectable in the rule dialog, and a
passed one that never fires must explain itself without being run again.*

**AG-GEN-15** A routine MUST become the rule's active routine only when it
passed and the request asked for it, or when an administrator picks it. A
failed routine MUST NOT replace the active one.
*Reason: an administrator may want to look at a new routine before it runs.
A failed one would replace working advice with none.* ActionGenerator makes a
passed routine active by naming it when it clears the request (AG-GEN-02);
AIManager then announces the rule change, and the routine is loaded from
there (AG-GEN-16).

**AG-GEN-16** ActionGenerator MUST run a rule's active routine only if
AIManager holds it with status `passed` and a checksum, and the code matches
that checksum. Otherwise the rule MUST run no routine, and this MUST be
logged.
*Reason: AG-PUR-06. The checksum is depth, not a boundary — whoever can write
the routine row has the database — but a checksum that is recorded and never
read is decoration. Every routine is stored with one, by generation and by a
feed import, so a routine without one was not written by either. Routines
from before checksums were recorded were kept running at first; none are left
on the installations or in the feed files, so they are refused without a
transition (decided 2026-11-18).*

**AG-GEN-17** The rules and their active routines MUST be loaded at startup,
reloaded on every rule change, and reloaded as a whole after a feed import,
together with the module's AI setting. A deleted rule MUST stop running when
its deletion is announced.
*Reason: a running daemon must pick up new and changed rules without a
restart. A feed import can replace any number of rules and routines in one
go.*

### The platform API

**AG-GEN-18** ActionGenerator MUST replace `{{API_CATALOGUE}}` in the
module's system prompt, at generation, with the read operations of the
current API catalogue — name, parameters, answer fields and their
descriptions — and MUST refuse to generate while no catalogue is loaded.
*Reason: PA-RT-05. The catalogue is the description of the data, kept by its
owners.*

**AG-GEN-19** *(withdrawn 2026-11-17)* — validation of a bridge routine
against the catalogue's mock answers. With the context gone it is the only
validation there is, and is AG-GEN-11.

**AG-GEN-20** A routine MUST be stored with the catalogue fingerprint
and the operations it called during validation (PA-RT-04). An active
routine that calls an operation the catalogue does not offer MUST NOT run,
and MUST be logged naming the operations; one that calls a deprecated
operation MUST run and be logged.
*Reason: PA-RT-06 — a routine must fail visibly when its foundation goes.*

**AG-GEN-21** A system prompt that is not empty but does not contain
`{{API_CATALOGUE}}` predates the platform API: generation MUST be refused
before the AI is asked, logged as an error saying that the system prompt
must be replaced with `prompts/actiongenerator.txt`, and the request cleared
with that reason (AG-GEN-02). Nothing is stored.
*Reason: such a prompt asks for context routines, which would fail
validation (AG-GEN-10) after an AI call was spent on them. Applying the new
prompt is the operator's step.*

## 5. Known deviations

No deviations are known.

## 6. Open questions

None is open.
