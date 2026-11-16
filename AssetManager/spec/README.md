# AssetManager specifications

These documents say what AssetManager must do and must not do. They describe
intended behaviour, not implementation: a statement here holds whatever the
code looks like, and code that contradicts one is a bug in the code or a
change the specification has to record first.

The [module README](../README.md) is the operator's and developer's guide —
building, configuring, running. Where the two overlap, the specification is
authoritative.

| # | Specification | Answers | Status |
|---|---|---|---|
| 1 | [Purpose and scope](01-purpose-and-scope.md) | What is AssetManager for, and what is it not for? | Draft |
| 2 | [Data model and behaviour](02-data-model-and-behaviour.md) | What must always be true of the data? | Draft |
| 3 | [API](03-api.md) ([`openapi.yaml`](openapi.yaml), WebSocket messages) | How do others talk to it? | Draft |
| 4 | [Security and tenancy](04-security-and-tenancy.md) | Who may do what? | Draft |
| 5 | [Operation](05-operation.md) | What does an operator rely on? | Draft |

Topics that hold for every TachyonikCSM service — sessions and
authentication, tenancy, the score model, migration and versioning policy —
belong to the cross-cutting specifications under [`docs/specs/`](../../docs/specs/README.md) and
are referred to rather than repeated here.

## Conventions

- **Requirements are numbered** `AM-<AREA>-<NN>`, e.g. `AM-PUR-03`. A number is
  never reused: a requirement that is dropped stays listed as *withdrawn*, so a
  reference to it elsewhere does not silently point at something else.
- **Keywords** follow [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119):
  MUST / MUST NOT is a requirement, SHOULD / SHOULD NOT a strong default with
  stated exceptions, MAY an allowed option.
- **Each requirement carries its reason** in a line below it. A rule whose
  reason is forgotten is the first to be "simplified" away.
- **Tests cite the requirement they pin** in their comment (`// AM-DATA-07`),
  so it is visible which requirements are tested.
- **Open questions** are listed at the end of a specification until decided.
  A decided question becomes a requirement, or is recorded as a non-goal, and
  is then removed from the list. Questions keep their number for good, so a
  reference such as "PUR question 5" stays valid. The list notes which
  numbers were decided and where they went.
- **Known deviations** are code that contradicts a requirement. They are
  listed in the specification until the code is fixed. The requirement
  stands; the code is what changes.
- A change that alters specified behaviour updates the specification **in the
  same commit**.
