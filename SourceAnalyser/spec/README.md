# SourceAnalyser specifications

These documents say what SourceAnalyser must do and must not do. They
describe intended behaviour, not implementation: a statement here holds
whatever the code looks like, and code that contradicts one is a bug in the
code or a change the specification has to record first.

The [module README](../README.md) is the operator's and developer's guide —
building, configuring, running. Where the two overlap, the specification is
authoritative.

| # | Specification | Answers | Status |
|---|---|---|---|
| 1 | [Purpose and scope](01-purpose-and-scope.md) | What is SourceAnalyser for, and what is it not for? | Accepted |
| 2 | [Analysing sources](02-analysis.md) | Which sources are analysed, when, and what is written back? | Accepted |
| 3 | [Routine generation and validation](03-generation-and-validation.md) | How does an analysis rule become a routine that may run? | Draft |
| 4 | [Interfaces](04-interfaces.md) | What do routines read and return, and how does SourceAnalyser talk to the other services? | Accepted |
| 5 | [Security and containment](05-security-and-containment.md) | Whose data reaches whom, and how is generated code kept in bounds? | Accepted |
| 6 | [Operation](06-operation.md) | What does an operator rely on? | Draft |

The specifications were written on 2026-11-20 from the code and the module
README as they stood then. Where the code did something the specification
cannot simply accept, it is recorded as a known deviation or an open question,
not written down as a requirement.

SourceAnalyser has no HTTP API of its own, and must not get one (project
rule, see CLAUDE.md). There is therefore no `openapi.yaml` here: its contract
with the rest of the platform is the routine contract and the context it hands
to routines, the source updates it writes to ResourceManager, and the
WebSocket messages it reacts to, all in [4. Interfaces](04-interfaces.md).

Topics that hold for every TachyonikCSM service — sessions and
authentication, tenancy, migration and versioning policy — belong to the
cross-cutting specifications under [`docs/specs/`](../../docs/specs/README.md)
and are referred to rather than repeated here. Where SourceAnalyser relies on
another module's behaviour, the requirement is named. Where it shares a rule
with ActionGenerator, the [ActionGenerator requirement](../../ActionGenerator/spec/README.md)
is named (for example `AG-GEN-04`), so a change to one is checked against the
other.

## Conventions

- **Requirements are numbered** `SA-<AREA>-<NN>`, e.g. `SA-PUR-03`. A number is
  never reused: a requirement that is dropped stays listed as *withdrawn*, so a
  reference to it elsewhere does not silently point at something else.
- **Keywords** follow [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119):
  MUST / MUST NOT is a requirement, SHOULD / SHOULD NOT a strong default with
  stated exceptions, MAY an allowed option.
- **Each requirement carries its reason** in a line below it. A rule whose
  reason is forgotten is the first to be "simplified" away.
- **Tests cite the requirement they pin** in their comment (`// SA-ANA-07`),
  so it is visible which requirements are tested.
- **Open questions** are listed at the end of a specification until decided.
  A decided question becomes a requirement, or is recorded as a non-goal, and
  is then removed from the list. Questions keep their number for good, so a
  reference such as "ANA question 2" stays valid. The list notes which
  numbers were decided and where they went.
- **Known deviations** are code that contradicts a requirement. They are
  listed in the specification until the code is fixed. The requirement
  stands; the code is what changes.
- A change that alters specified behaviour updates the specification **in the
  same commit**.
