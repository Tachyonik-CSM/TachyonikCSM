# SourceAnalyser — 3. Routine generation and validation

Status: **Accepted** · Area code: `GEN`

How an analysis rule becomes a routine that may run: when SourceAnalyser
generates one, which AI it uses and what it gives it, how the result is
checked, and how it is stored and put to work. What a routine must look like
is in [4. Interfaces](04-interfaces.md#2-the-routine). What protects the
daemon from the code is in [5. Security and containment](05-security-and-containment.md).

## 1. When a routine is generated

**SA-GEN-01** SourceAnalyser MUST generate a routine for a rule when AIManager
announces a change to a rule that carries a pending generation request.
*Reason: a generation costs an AI call and replaces nothing on its own
(SA-GEN-14). It is an administrator's decision, made in the rule dialog — not
a side effect of saving a rule. As AG-GEN-01.*

**SA-GEN-02** *(withdrawn 2026-11-26, GEN question 1)* — at startup, a
routine was generated for every rule without one and used at once.
SourceAnalyser now generates only when asked (SA-GEN-01), as ActionGenerator
does (AG-GEN-01). A feed brings rules with their routines; a rule without one
is shown so in the rule dialog, and a request generates it. The startup
generation used a routine the administrator had not been asked about, and
called the AI for a rule at every restart while its generation kept failing.

**SA-GEN-03** Once a generation request has been handled, whatever its
outcome, SourceAnalyser MUST clear it in AIManager, with the reason when it
failed. When SourceAnalyser first loads its rules, a request made before it
started that is still standing MUST be cleared with the reason that
SourceAnalyser was restarted before it could handle it. A request made after
the start MUST NOT be cleared this way.
*Reason: a request left standing would be generated again on every change to
the rule, in a loop, and the rule dialog would show "generating" for good. The
reason belongs in the rule dialog, not only in this module's log. A request
made before the start was cut off by a stop — an AI call is not waited for
(SA-OPS-09) — or made while the daemon was not running, and no announcement
will come for it any more. The first load is later than the start when
AIManager could not be reached then (SA-OPS-08). As AG-GEN-02.*

**SA-GEN-04** A rule without a rule prompt MUST be generated from the rule
alone.
*Reason: the rule prompt is optional advice. Refusing without it made the
Generate button appear to do nothing. As AG-GEN-03.*

## 2. The AI and what it is given

**SA-GEN-05** The AI MUST be chosen when the generation starts, by asking
AIManager: the AI set on the rule if there is one, otherwise the module's
current default; and the module's current system prompt. Only when AIManager
cannot be asked MAY the AI and system prompt loaded last be used.
*Reason: a missed change notification must not leave the module generating
with an AI that is no longer configured. Implemented in TachyonikLib's
`aipick`, shared with the other generating modules. As AG-GEN-04.*

**SA-GEN-06** Every log line about a generation, and every generation error,
MUST name the AI it used and why that one: set on the rule, module default,
or loaded last.
*Reason: a refused request must say which account refused it. As AG-GEN-05.*

**SA-GEN-07** Generation MUST be refused, with a message saying where to set
it (Settings → SourceAnalyser → System Prompt), when the system prompt is
empty.
*Reason: the system prompt documents the routine contract and the rule
context. Code generated without it would be written against nothing. As
AG-GEN-06.*

**SA-GEN-08** The AI MUST be given the system prompt and the rule's id, type
and rule prompt. Nothing else — in particular no source and no content.
*Reason: this is all a routine needs to be written. No user data goes to the
AI (SA-SEC-06).*

**SA-GEN-09** The answer MUST be cleaned before it is checked: a Markdown code
fence around it removed, typographic quotation marks replaced by ASCII ones.
A header naming the rule's id and type MUST be put in front.
*Reason: models wrap code in fences and reach for smart quotes; both are
syntax errors to the engine. The header tells a reader of the stored routine
what it is for. The cleaning is TachyonikLib's `aicode`. As AG-GEN-09.*

## 3. Validation

A generated routine is checked in this order. The first check that fails ends
the generation, and the routine is stored as `failed` (SA-GEN-13).

**SA-GEN-10** The routine MUST load and have the shape of SA-IF-01, and every
rule of it MUST declare the analysis rule's id as `ruleId` and no other. Each
rule declaring another id MUST be named with the id it declares (decided
2026-11-26, GEN question 2).
*Reason: a routine that does not load recognises nothing, wherever it runs. A
model that invents or renumbers rules would otherwise have its output stored
against the wrong rule, as AG-GEN-10 prevents for ActionGenerator.*

**SA-GEN-11** Every rule of the routine MUST be run against every mock context
(SA-IF-06), and the routine MUST fail when
- for any of them, it fails to load or a rule's `analyze()` throws or overruns
  its budget, named with the rule and the mock context;
- a rule recognises every mock context, the unsupported file included: it
  returns a result with a `sourceType` for each (SA-IF-04), named with the rule
  (decided 2026-11-26, GEN question 2).
*Reason: a routine that throws on a file it does not recognise would be
logged as an error on every upload of every other format. A rule that
recognises every file would give its type to every source after its rule in
order (SA-ANA-10), as AG-GEN-11 refuses a `check()` that is true in every
scenario. A routine's `status` is not checked: it is ignored (SA-IF-04,
SA-IF-05).*

**SA-GEN-12** Each mock context MUST be run in a fresh JavaScript engine, as
the analysis of a source is (SA-SEC-10).
*Reason: a global set in one context would otherwise colour the next, and the
validation would be judging that.*

## 4. Storing and activating

**SA-GEN-13** Every generated routine MUST be stored in AIManager, passed or
failed, with: the code, the rule's id, the module (`SourceAnalyser`), a
version from the time of generation, the model, the SHA-256 of the code, the
time, the status, and a log holding the validation errors.
*Reason: a failed routine must be inspectable in the rule dialog. As
AG-GEN-14.*

**SA-GEN-14** A routine MUST become the rule's active routine only when it
passed and the request asked for it, or when an administrator picks it. A
failed routine MUST NOT replace the active one. A passed routine that was not
asked to become active MUST be stored and MUST NOT run until it is picked;
the active one stays in use.
*Reason: an administrator may want to test a new routine (SA-ANA-15) before it
decides every user's sources. A failed one would replace working recognition
with none.* SourceAnalyser makes a passed routine active by naming it when it
clears the request (SA-GEN-03); AIManager then announces the rule change, and
the routine is loaded from there (SA-GEN-16).

**SA-GEN-15** SourceAnalyser MUST run a rule's active routine only if AIManager
holds it with status `passed` and a checksum, and the code matches that
checksum. Otherwise the rule MUST run no routine, and this MUST be logged.
*Reason: SA-PUR-05. The checksum is depth, not a boundary — whoever can write
the routine row has the database — but a checksum that is recorded and never
read is decoration. Every routine is stored with one, by generation and by a
feed import. As AG-GEN-16.*

**SA-GEN-16** The rules and their active routines MUST be loaded at startup,
the changed rule reloaded on every rule change, and all of them reloaded
after a feed import and after every reconnect to AIManager, together with the
module's AI setting. A deleted rule MUST stop running when its deletion is
announced.
*Reason: a running daemon must pick up new and changed rules without a
restart. A feed import can replace any number of rules and routines in one
go, and a connection that was down has missed the changes announced
meanwhile.*

## 5. Known deviations

No deviations are known.

## 6. Open questions

None open.
