# ActionGenerator — 4. Interfaces

Status: **Accepted** · Area code: `IF`

ActionGenerator has no API of its own (AG-PUR-08). Its contracts are:
1. the **API bridge** it hands to routines (§7, and
   [PA: Platform API catalogue](../../docs/specs/01-platform-api-catalogue.md));
2. the **routine** it expects from the AI;
3. the **actions** it sends to ActionManager;
4. the **messages** it reacts to and the **calls** it makes.

The first two are a contract with generated code. What a routine may read is
what the managers' API documents offer, and the AI is given those documents
at generation (PA-RT-05, AG-GEN-18) — so there is no second description of
the data to keep in step.

Earlier versions handed routines a hand-built **rule context** (`ctx`)
instead, described in §1 and §4. A routine written for it — a *context
routine* — is no longer run (AG-IF-22, AG-EVAL-24); the context, its mock
scenarios and the copies of it were removed (PA-MIG-02).

## 1. The rule context (withdrawn)

Withdrawn 2026-11-17 with the context itself (PA-MIG-02, -03). What a routine
reads is now the answer of an API operation, described by its manager's API
document (PA-CAT). The numbers stay listed so that a reference to them does
not point at something else.

- **AG-IF-01** to **AG-IF-05** *(withdrawn)* — the shape of the context: its
  fields, every field always present, empty values, values JavaScript
  compares wrongly, the same meaning in a user's and a shared context.
- **AG-IF-06** *(withdrawn)* — the legacy aliases of `ctx.user`.
- **AG-IF-07** *(withdrawn)* — `ctx.capabilities` describes tools only. The
  same distinction is now ToolManager's `capabilities.get`.

## 2. The routine

**AG-IF-08** A routine MUST be ES5.1 JavaScript that declares
`var routineContract = 2;` (AG-IF-22) and defines a global `rules`: an array
of objects, each with
- `name`: a string;
- `ruleId`: the id of the action rule it implements;
- `check(api)`: true when the rule applies;
- `createAction(api)`: the action to raise (AG-IF-11);
- optionally `options`: an object whose keys are action option ids and whose
  values are functions `(api)` returning true when that option applies.

A routine that does not have this shape MUST fail to load, naming what is
wrong. A key of `options` that is not an option id, a value that is not a
function, or an `options` that is not such an object MUST fail it too.
*Reason: the engine (goja) runs ES5.1. A malformed `options` would otherwise
load with no conditions and silently show every option.*

**AG-IF-09** `check()`, `createAction()` and the option conditions of one rule
MUST be given the same bridge, with the same pass cache (PA-RT-02), and MUST
be expected to read through it only.
*Reason: the action and its options are decided on the data the condition
saw.*

**AG-IF-10** A routine MAY define helper functions and variables at top
level. They MUST NOT carry anything from one evaluation to the next
(AG-SEC-12).
*Reason: helpers keep conditions readable. Each evaluation gets a fresh
engine, so a global cannot hold state.*

## 3. The action

**AG-IF-11** `createAction()` returns an object with `title`, `type`,
`description`, `status`, `priority`, `assignedTo`, `issuedBy` and `trigger`.
Other members MUST be ignored. A missing `status` MUST become `New`, a missing
`assignedTo` `User`, a missing `issuedBy` `ActionGenerator`.
*Reason: the cost of an action lives on its options now; older routines still
return cost members, which must do no harm.*

**AG-IF-12** An action MUST be refused (AG-EVAL-10) when:
- its title is empty or blank;
- `type` is not `Fix`, `Info` or `Support`;
- `status` is not `New` or `Done`;
- `assignedTo` is not `User`, `Support` or `ActionExecutor`;
- `issuedBy` is not `User`, `Support` or `ActionGenerator`;
- `priority` is outside 0–100.

*Reason: these are the values ActionManager's model and the WebUI know. A
routine must not widen the platform's vocabularies by inventing a value.*

**AG-IF-13** The title MUST be shortened to 200 characters, the description to
4000 and the trigger to 500 — counting characters, not bytes — and the
shortening logged.
*Reason: bounds against unbounded growth, generous on purpose. Cutting at a
byte could split a character and store an invalid sequence.*

**AG-IF-14** The request to ActionManager MUST carry, besides the action's
fields, the owner, the rule's id, and the hidden options — or no hidden
options at all when the routine declares no conditions. An update of an open
action (AG-EVAL-09) MUST carry its title, type, description, priority and
hidden options, and no status or assignment.
*Reason: the rule's id is what withdrawal and the Action Guide's options are
found by. "No conditions" leaves every option shown, the behaviour of actions
from before conditions existed.*

## 4. Mock scenarios and the copies of the context (withdrawn)

Withdrawn 2026-11-17 with the context (PA-MIG-02, -03). The mock scenarios are
now the platform's named scenarios (PA-VAL-01, `docs/specs/mock-scenarios.md`),
in which each manager's API document gives the mock answers of its operations
(PA-CAT-08); a routine is validated against those (AG-GEN-11). There are no
copies of a context left to keep in step: the generated
`WebUI/src/generated/actionRuleMockContexts.json` and AIManager's dry-run
defaults went with it.

- **AG-IF-15** *(withdrawn)* — the mock scenarios as complete contexts, and
  what they had to vary.
- **AG-IF-16** *(withdrawn)* — a change to the context made in all its copies
  in one commit.

## 5. Messages ActionGenerator reacts to

Over the `/ws` endpoint each manager offers internal services, authenticated
with the internal service key (AG-SEC-01).

From AssetManager, SystemManager, ResourceManager and ActionManager,
ActionGenerator re-evaluates on the messages an active routine's operations
are invalidated by (AG-IF-24), and on these:

**AG-IF-17** *(withdrawn 2026-11-17)* — AssetManager's asset and
vulnerability messages as fixed triggers. They re-evaluate when an active
routine calls an operation they invalidate (AG-IF-24).

**AG-IF-18** From **SystemManager**: `USER_CREATED`, `USER_DELETED` —
re-evaluate, whatever the routines read.
*Reason: they change whom the rules are evaluated for (AG-EVAL-06), which no
operation's answer announces: a rule about a new user who has nothing yet
must fire for them. A change to a user's data — `USER_UPDATED`,
`ORGANISATION_UPDATED` — is announced through AG-IF-24 when a routine reads
it.*

**AG-IF-19** *(withdrawn 2026-11-17)* — ResourceManager's tool, credential and
proxy messages as fixed triggers, and the explicit exclusion of
`TOOL_ACTIVITY`. They re-evaluate when an active routine calls an operation
they invalidate (AG-IF-24); `TOOL_ACTIVITY` invalidates none.

**AG-IF-20** From **AIManager**:

| Message | Payload | ActionGenerator |
|---|---|---|
| `ACTION_RULE_CREATED`, `ACTION_RULE_UPDATED` | `{"id"}` | reloads the rules and routines, handles a pending generation request (AG-GEN-01), re-evaluates |
| `ACTION_RULE_DELETED` | `{"id"}` | stops running the rule, re-evaluates |
| `ACTION_RULE_EVALUATION_REQUESTED` | `{"ruleId", "userId", "force"}` | queues the evaluation of that rule for that user (AG-EVAL-13) |
| `ACTION_RULES_EVALUATION_REQUESTED` | `{"userId"}` | queues the evaluation of every rule for that user (AG-EVAL-29) |
| `FEED_IMPORTED` | none | reloads all rules, routines and the AI setting, re-evaluates |
| `MODULE_AI_SETTING_UPDATED` for `actiongenerator` | | re-reads the AI and system prompt |
| `API_CATALOGUE_UPDATED` | `{"module", "sha256"}` | reloads the catalogue and the bridge, re-checks the active routines (AG-GEN-20), re-evaluates |
| any other | | re-evaluates when it is a trigger of an active routine (AG-IF-24) |

**AG-IF-21** A message of any other type, or one whose payload cannot be read,
MUST be ignored.
*Reason: managers broadcast many types to all internal listeners; a new one
must not disturb this daemon.*

## 6. Calls ActionGenerator makes

All with the internal service key (AG-SEC-01). Reads are of one user, named by
id, unless stated.

| Service | Call | For |
|---|---|---|
| SystemManager | `GET /api/internal/users`, `/users/{id}`, `/workspace` | users (with `workspaceAdminId`, the admin of a user's SaaS workspace), workspace mode and primary owner |
| AIManager | `GET /api/internal/action-rules`, `/routines/{id}`, `/action-options?actionRuleId=`, `/module-ai-settings/actiongenerator`, `/ais/{id}`, `/ais/by-name/{name}`, the API catalogue | rules and routines, AI, the catalogue the bridge is built from |
| | `POST /api/internal/routines`; `PATCH /api/internal/action-rules/{id}` | store a routine; clear a generation request (AG-GEN-02) |
| ActionManager | `GET /api/actions?userId=` | existing actions, to recognise one already raised (AG-EVAL-09) |
| | `POST /api/actions`; `POST /api/internal/actions/hidden-options`; `POST /api/internal/actions/delete-by-rule`, `…/delete-by-rule-shared` | create; refresh hidden options; withdraw for one user, or for all but one |
| SystemManager | heartbeat (TachyonikLib `heartbeat`) | liveness (AG-OPS-10) |
| every manager of AG-IF-23 | the `read` operations of its API document, through the bridge | what a routine reads, for the user evaluated (PA-BR-02, -03) |

## 7. Bridge routines

**AG-IF-22** A routine MUST declare `var routineContract = 2;` at top level
(PA-RT-01). One that does not is a *context routine*, written for the rule
context of earlier versions: it MUST NOT be run (AG-EVAL-24) nor pass
validation (AG-GEN-10).
*Reason: a context routine reads a `ctx` that no longer exists. Run against
the bridge, it would throw on every evaluation, or — worse — read undefined
and decide on it.*

**AG-IF-23** The `api` object MUST be the read-only API bridge
(TachyonikLib `apibridge`) built from the catalogue AIManager serves, with
each manager reached at the URL and with the key of this module's
configuration: AssetManager, SystemManager, ResourceManager, ActionManager,
AIManager and ToolManager.
*Reason: PA-BR-01, -08. ToolManager is reached only by the routines
(`capabilities.get`).*

**AG-IF-24** ActionGenerator MUST re-evaluate on every message that an
operation an active routine calls lists as `x-invalidated-by`, from
any of those managers — ActionManager's included, which it connects to for
this (PA-RT-07). Its own creating and withdrawing is announced there too; the
pass that follows finds nothing to change.
*Reason: the triggers follow the data the routines read, with no hand-kept
list (decided 2026-11-16).*

## 8. Known deviations

None known.

## 9. Open questions

None open.
