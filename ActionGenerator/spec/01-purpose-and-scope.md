# ActionGenerator — 1. Purpose and scope

Status: **Accepted** · Area code: `PUR`

## 1. Purpose

ActionGenerator turns the state of a user's installation into **actions**: the
concrete next steps shown in the Actions view and the Action Guide.

What counts as a next step is not written into ActionGenerator. An
administrator describes a situation in plain language as an **action rule**;
an AI turns that description into a small JavaScript **routine**;
ActionGenerator evaluates the routines for each user — they read the user's
situation through the platform's APIs — creates
the actions they return, and withdraws them again once the situation they
describe has gone. A new kind of advice is a new rule, not a change to this
module.

## 2. Terms

Used in this sense throughout the ActionGenerator specifications. They move to
the shared glossary once it exists.

| Term | Meaning |
|---|---|
| **Action rule** | An administrator's description of a situation and of the action to raise in it: a title, a trigger prompt in plain language, an optional rule prompt, and the action options it offers. Held by AIManager. |
| **Trigger prompt** | The rule's condition in plain language. A rule whose trigger prompt is empty is an *on-demand rule*. |
| **On-demand rule** | A rule with no condition of its own. It raises its action only when asked for by name (a forced evaluation, AG-EVAL-14). |
| **Routine** | The JavaScript generated from a rule. Held by AIManager with its version, model, checksum, status (`passed` or `failed`) and validation log. |
| **Active routine** | The routine of a rule that runs. A rule has at most one. |
| **API bridge** (`api`) | What a routine reads through: one function per read operation the managers' API documents offer, calling the manager for the user evaluated, or for a shared workspace's primary owner. See [4. Interfaces](04-interfaces.md#7-bridge-routines) and [PA: Platform API catalogue](../../docs/specs/01-platform-api-catalogue.md). |
| **Context routine** | A routine written for the rule context (`ctx`) that earlier versions built, recognised by not declaring `var routineContract = 2;`. No longer run (AG-EVAL-24). |
| **Action** | A next step for a user. Held by ActionManager. One raised by a rule carries that rule's id. |
| **Action option** | A way of carrying out an action that a rule offers ("Upload host list", "Run a host scan"). Linked to rules in AIManager; the link may carry a *visibility condition* in plain language. |
| **Hidden options** | The options of a rule that do not apply to the user an action was raised for. Recorded on the action. |
| **Withdrawal** | Deleting the actions a rule raised that the user has not acted on (status `New`), because the rule no longer matches. |
| **Mock scenario** | One of the platform's fixed, named situations (`docs/specs/mock-scenarios.md`), in which every API operation has a mock answer. A newly generated routine is tried in each before it may be used. |
| **Shared workspace** | An appliance installation, where every registered user sees the same data. |

## 3. Responsibilities

**AG-PUR-01** ActionGenerator MUST evaluate the active routine of every action
rule for every user, or once for the shared workspace, and create the actions
the routines return in ActionManager.
*Reason: this is what makes the platform's advice follow the user's actual
situation.*

**AG-PUR-02** ActionGenerator MUST withdraw a rule's untouched actions when
the rule no longer matches.
*Reason: an action whose situation has gone is stale advice. It must
disappear once the user has done what it asked, without the user deleting it.*

**AG-PUR-03** ActionGenerator MUST re-evaluate when the data the routines read
changes, and when a rule or the set of active routines changes.
*Reason: there is no other point at which an action could appear or go; see
AG-EVAL-01.*

**AG-PUR-04** ActionGenerator MUST decide, for each action it raises, which of
the rule's options apply to the user, and keep that decision current while the
action exists.
*Reason: an option such as "install a tool for host detection" is wrong
advice for a user who has one; and the user may install it after the action
was raised.*

**AG-PUR-05** ActionGenerator MUST generate a routine for a rule when an
administrator asks for it, with the AI AIManager assigns, validate the result,
and store it in AIManager with its outcome.
*Reason: turning plain language into a condition is the step that needs an
AI. Everything after it is plain code.*

**AG-PUR-06** ActionGenerator MUST NOT run a routine that has not passed
validation, or whose stored code differs from the code that was validated.
*Reason: routine code is written by a model. The validation and the checksum
are what make it trustworthy enough to run (see 3. and 5.).*

**AG-PUR-07** ActionGenerator MUST keep producing actions from the active
routines when no AI is configured or the AI cannot be reached.
*Reason: the AI is needed only to generate a routine. Evaluating one is plain
JavaScript, and an AI outage must not stop the advice.*

## 4. Non-goals

**AG-PUR-08** ActionGenerator MUST NOT serve an HTTP API.
*Reason: it is an event-driven daemon. Work a user asks for synchronously goes
through a manager that has an API and is announced to the daemon over a
WebSocket (project rule, see CLAUDE.md).*

**AG-PUR-09** ActionGenerator MUST NOT store anything itself: no database, no
files other than its log.
*Reason: the rules and routines belong to AIManager, the actions to
ActionManager. Holding a copy would be a second truth that drifts.*

**AG-PUR-10** ActionGenerator MUST NOT change a user's data — assets, sources,
tools, organisation, settings — nor carry out an action.
*Reason: it advises. Carrying actions out is ActionExecutor's job, and only
on the user's request.*

**AG-PUR-11** ActionGenerator MUST NOT change an action beyond creating it,
updating what its rule's evaluation says about it — title, type, description,
priority and hidden options (AG-EVAL-09) — while it is open, marking it cleared
when it is `Done` (AG-EVAL-26), and withdrawing it while it is `New`.
*Reason: once a user has acknowledged, started or finished an action, its
status and assignment are theirs; the text describes the situation, which
only the rule can tell. ActionManager owns its life cycle.*

**AG-PUR-12** ActionGenerator MUST NOT decide, outside the routines, what
advice a user gets: no rule, condition or action text is written in its code.
*Reason: advice is authored as rules so that an administrator can change it
without a release.*

**AG-PUR-13** ActionGenerator MUST NOT manage rules, action options, AI
assignments or system prompts. It reads them, and writes back only generated
routines and the outcome of a generation request.
*Reason: they are authored in the WebUI and held by AIManager.*

**AG-PUR-14** ActionGenerator MUST NOT send any user's data to an AI.
*Reason: generation needs only the rule. The data a routine later reads stays
inside the platform. See AG-SEC-08.*

## 5. Place among the services

ActionGenerator is called by nobody: it has no API. It learns of work from
WebSocket messages and acts by calling the managers.

### What wakes ActionGenerator

| Service | Messages | For |
|---|---|---|
| **AssetManager**, **SystemManager**, **ResourceManager**, **ActionManager** | the changes the API operations an active routine calls are invalidated by (`x-invalidated-by`) | what the routines read |
| **SystemManager** | a user created or deleted | whom the rules are evaluated for |
| **AIManager** | rule created, changed or deleted; an evaluation requested; a feed imported; the module's AI setting changed; the API catalogue changed; changes its own operations are invalidated by | the rules, routines, generation requests, the AI to generate with, the bridge |

### Whom ActionGenerator calls

| Service | For | Direction |
|---|---|---|
| **SystemManager** | the users; one user; the workspace mode and its primary owner | reads |
| | liveness heartbeat | sends |
| **AIManager** | rules, routines, action options, the module's AI setting and system prompt, AI entries, the API catalogue | reads |
| | generated routines; the outcome of a generation request | writes |
| **ActionManager** | a user's actions | reads |
| | new actions; hidden options; withdrawals | writes |
| **AssetManager**, **SystemManager**, **ResourceManager**, **ActionManager**, **AIManager**, **ToolManager** | what a routine reads, through the API bridge: the `read` operations of their API documents | reads |
| **The assigned AI** | the routine for a rule | sends the rule, receives code |

ActionGenerator contacts nothing else.

## 6. Where the details are

| Topic | Specification |
|---|---|
| Triggers, per-user and shared evaluation, creating, deduplicating and withdrawing actions, option conditions, on-demand rules | [2. Evaluation and actions](02-evaluation-and-actions.md) |
| Generation requests, choosing the AI, what the AI is given, validation, storing and activating routines | [3. Routine generation and validation](03-generation-and-validation.md) |
| The routine contract, the API bridge, the action vocabulary, messages consumed, calls made | [4. Interfaces](04-interfaces.md) |
| Service keys, whose data a routine reads, containment of generated code, what reaches the AI and the log | [5. Security and containment](05-security-and-containment.md) |
| Configuration, startup, shutdown, version, liveness, logging | [6. Operation](06-operation.md) |

## 7. Open questions

None open.
