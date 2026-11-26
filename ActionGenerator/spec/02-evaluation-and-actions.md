# ActionGenerator — 2. Evaluation and actions

Status: **Accepted** · Area code: `EVAL`

When ActionGenerator evaluates the rules, for whom, and what it then does
to the actions in ActionManager. What a routine reads (the API bridge) and
what it returns is in [4. Interfaces](04-interfaces.md). How a routine comes
to exist is in [3. Routine generation and validation](03-generation-and-validation.md).

## 1. When rules are evaluated

**AG-EVAL-01** ActionGenerator MUST evaluate every rule against current data
when one of these happens, and only then:
- the daemon starts;
- a message arrives that may change what an active routine reads
  (AG-IF-24), or a user is created or deleted (AG-IF-18);
- the connection to one of those services is (re)established;
- a rule is created, changed or deleted, or a feed import reloads the rules;
- an evaluation of one rule for one user is requested (AG-EVAL-13).

There MUST NOT be a periodic evaluation.
*Reason: an action appears and goes only when something re-runs the rules, so
staleness is fixed by watching the data the routines read. A timer
would hide a missing trigger instead of exposing it. Re-evaluating on
reconnect covers the changes made while the connection was down.*

**AG-EVAL-02** A burst of change messages MUST lead to one evaluation, started
2 seconds after the last message of the burst.
*Reason: an import produces hundreds of messages. Evaluating every user once
per message would load every service for nothing.*

**AG-EVAL-03** Full evaluations MUST NOT run at the same time, whatever asked
for them: change messages, a rule change, a catalogue change, a reload of the
rules or the start. An evaluation request (AG-EVAL-13) is not a full
evaluation and is not held back by one.
*Reason: a full pass over every rule and user can take longer than the
debounce. Two passes at once both find an action missing and both try to
create it. An evaluation request is one rule for one user with a person
waiting for it; ActionManager refuses a second open action of a rule for an
owner, which covers its one overlap with a pass.*

**AG-EVAL-04** A change that arrives while an evaluation runs MUST lead to
another evaluation after it.
*Reason: the running pass may already have read the data the change touched.
Without another pass, the change is not reflected until something unrelated
happens, since there is no periodic evaluation (AG-EVAL-01).*

**AG-EVAL-05** Rule change messages MUST be debounced per rule, 5 seconds
after the last one. An evaluation request (AG-EVAL-13) MUST NOT be debounced.
*Reason: saving a rule several times in a row must lead to one reload. An
evaluation request is a person's deliberate act; collapsing two would drop
one they asked for.*

## 2. Whom rules are evaluated for

**AG-EVAL-06** ActionGenerator MUST evaluate against every user SystemManager
lists for internal services, which excludes holder accounts of parked group
data. In a SaaS installation each user is evaluated separately, the routines
reading that user's data (PA-BR-03), and their actions are created for them.
*Reason: in SaaS every user's data is private, so is the advice. Holder
accounts are not people (SystemManager's group parking).* Deactivated
accounts are not evaluated (AG-EVAL-25).

**AG-EVAL-07** In a shared workspace (an appliance whose primary owner
SystemManager reports), ActionGenerator MUST evaluate every rule once, for
the primary owner, and create the actions for the primary owner. The
managers answer the bridge's calls for the primary owner with the data of the
whole workspace (PA-BR-07); ActionGenerator MUST NOT combine users' data
itself.
*Reason: every registered user sees the same data, so one action set per user
would show up as duplicates in the shared Actions view. What a workspace user
sees is the managers' to decide; a second definition here — the combined
context of earlier versions — drifts from theirs.*

**AG-EVAL-08** When the workspace mode cannot be read, or an appliance reports
no primary owner, ActionGenerator MUST evaluate per user (AG-EVAL-06) and say
so in the log.
*Reason: per-user evaluation never hides an action from a user; the shared
mode with a wrong owner could. The duplicates it may leave on an appliance are
consolidated by the next shared pass (AG-EVAL-12).*

**AG-EVAL-25** ActionGenerator MUST NOT evaluate a deactivated account: no
rule is evaluated for it, and none of its actions is created or withdrawn.
Its actions MUST stay as they were until the account is reactivated; the
first evaluation after that evaluates it again. An evaluation request for it
(AG-EVAL-13) MUST be refused. A shared workspace is evaluated for its primary
owner as before (AG-EVAL-07), even when that account is deactivated, and an
account for which SystemManager does not say counts as active.
*Reason: nobody can log in to a deactivated account to read the advice, so
evaluating it costs work for no one. Withdrawing its actions meanwhile would
erase what the user left as they were, and they would come back to a
different list. A reactivated account is brought up to date by the next pass
(decided 2026-11-18). The actions of a shared workspace are the workspace's,
not the owner's own, and skipping a deactivated owner would stop the advice
for everyone in it.*

## 3. Creating actions

**AG-EVAL-09** An action ActionGenerator raised MUST be identified by its rule
and its owner, not by its title. For a rule that matches:
- if the owner has an open action of the rule (`New`, `Acknowledged` or
  `InProgress`), ActionGenerator MUST update it in place: title, type,
  description, priority and the evaluated hidden options (AG-EVAL-16). Its
  status, assignment and creation date MUST stay as they are;
- otherwise it MUST create the action the routine returns, with the rule's id
  and the hidden options, unless a `Done` action holds the rule back
  (AG-EVAL-26).

A rule raises at most one action per owner: when its evaluation returns
several, the first MUST be used and the others logged. An action without a
rule, or of another rule, MUST NOT be taken for the rule's, whatever its title.
*Reason: a title that carries a number ("3 hosts not seen…") changes with the
situation, and the action must follow it rather than be raised anew beside
the old one. The text describes the situation as it is now, also while the
user works on it; what they decided — its status and who does it — stays
theirs. ActionManager refuses a second open action of a rule for an owner, so
identifying by title made a changed title fail to be raised at all (decided
2026-11-18).*

**AG-EVAL-10** An action a routine returns that does not fit the action
vocabulary (AG-IF-12) MUST NOT be created. Over-long text MUST be shortened to
its limit instead (AG-IF-13).
*Reason: a value outside the vocabulary is the rule being wrong, and the UI
cannot show or filter it. A long text is the data being long — an asset with
a long name — and dropping the action for it would lose advice the user needs.*

**AG-EVAL-28** For each owner, a pass MUST evaluate the rules from the highest
action priority to the lowest, rules of equal priority by ascending id. The
priority is the rule's (`actionPriority` in AIManager), not the one its routine
returns.
*Reason: the number of actions a user may hold is limited, and ActionManager
refuses the ones beyond it. What it refuses must be the least important
advice, not whichever rule a random order put last. The rule's priority is
known before anything is evaluated, and the routines return it as it is
(decided 2026-11-26).*

## 4. Withdrawing actions

**AG-EVAL-11** When a rule that is not on-demand evaluates to no action for a
user, ActionGenerator MUST withdraw that rule's actions of that user whose
status is `New`, and mark that rule's `Done` actions of that user as cleared
(AG-EVAL-26). Actions in any other status MUST be kept as they are. A rule
whose outcome is unknown has not evaluated to no action (AG-EVAL-22).
*Reason: the action was raised because the condition held, and it holds no
longer. An action the user has acknowledged, started or finished is theirs
(AG-PUR-11).*

**AG-EVAL-12** In a shared workspace, a rule that produces no action MUST have
its `New` actions withdrawn, and its `Done` actions cleared, for every owner.
A rule that does produce one MUST
have its `New` actions of every owner but the primary one withdrawn.
*Reason: the first is AG-EVAL-11 for the workspace. The second consolidates
the per-user copies left by earlier per-user evaluation, and by evaluation
requests before they were evaluated for the primary owner (AG-EVAL-13).*

**AG-EVAL-26** A `Done` action of a rule MUST hold the rule back for its owner:
while it is not cleared, a matching rule MUST NOT create a new action. It is
cleared when the rule evaluates to no action for that owner (AG-EVAL-11, -12);
from then on the next match MUST create a new action. An action marked `Done`
again is not cleared until the rule stops matching once more. A forced
evaluation request (AG-EVAL-13) MUST create the action even while a `Done`
action holds the rule back.
*Reason: a `Done` action that came back while the rule still matches would
reappear seconds after the user marked it, before the data shows what they
did. Once the situation has ended and comes back, it is a new occurrence and
deserves new advice. A forced request is the user asking for the action
(decided 2026-11-18).*

**AG-EVAL-27** When an owner has several open actions of one rule — left by
data from before ActionManager refused them — the newest MUST be the one
updated, and the other `New` ones MUST be withdrawn. Those in `Acknowledged`
or `InProgress` MUST be kept.
*Reason: one rule, one action (AG-EVAL-09). The copies the user has taken on
are theirs.*

## 5. Evaluation requests and on-demand rules

**AG-EVAL-13** When AIManager announces a request to evaluate one rule for one
user, ActionGenerator MUST evaluate that rule for that user,
create its actions for that user, and withdraw as in AG-EVAL-11 when it
produces none. A request marked *forced* MUST skip the rule's condition and
create the action as if it had matched, even while a `Done` action holds the
rule back (AG-EVAL-26). In a shared workspace (AG-EVAL-07) the request MUST be
evaluated for the primary owner and its action placed under them, withdrawn
and consolidated as in AG-EVAL-12; when the workspace mode cannot be read, it
is evaluated for the requester (AG-EVAL-08). A deactivated requester is
refused in every mode (AG-EVAL-25).
*Reason: the WebUI asks for this when the Action Guide opens an action, so the
option set reflects the situation now, and when a user picks an action option
of type `Action`, which asks for a rule's action by name. A request stands in
for the pass, so it gives the pass's answer: in a shared workspace the
workspace's data and one action for everyone, not a copy for the requester
that the next pass withdraws (decided 2026-11-18).*

**AG-EVAL-14** The actions of an on-demand rule MUST NOT be withdrawn by an
evaluation that finds no match.
*Reason: such a rule has no condition, so every unforced evaluation finds no
match. Withdrawing would delete the action a user had just asked for, seconds
after asking. It stays until it is acted on or deleted.*

**AG-EVAL-15** Whether a rule is on-demand MUST be taken from its trigger
prompt being empty (blank counts as empty), and from nothing else.
*Reason: nothing in the routine can tell the two kinds apart: an on-demand
rule's `check()` is a plain `return false` by design (AG-GEN-11).*

## 6. Option conditions

**AG-EVAL-16** For each action it raises or refreshes, ActionGenerator MUST run
the routine's condition for every option that has one, with the same bridge
pass (AG-IF-09), and record the options whose condition is false as hidden. An option
without a condition MUST be shown.
*Reason: exclusions are stored rather than inclusions, so an option added to
the rule later is offered instead of being invisible for missing a list drawn
up before it existed.*

**AG-EVAL-17** An option whose condition throws or overruns its budget MUST be
shown, and the failure logged.
*Reason: hiding a step the user may need because a generated line was wrong is
the worse failure.*

**AG-EVAL-18** The hidden options MUST be sent in ascending order, and
ActionManager MUST NOT be asked to change them when they are unchanged.
*Reason: an unchanged answer must compare equal, so the action is not
rewritten and re-announced to every open WebUI on every evaluation.* The
second half is ActionManager's: it skips an unchanged write.

## 7. Failures during an evaluation

**AG-EVAL-19** *(withdrawn 2026-11-17)* — a service that could not be reached
while the rule context was built cost only its fields, which were filled with
empty values. There is no context any more: a bridge call that fails throws
(PA-BR-06), and the rule's outcome is unknown (AG-EVAL-22).

**AG-EVAL-20** A rule whose `check()` or `createAction()` throws or overruns
its budget MUST NOT create an action (nor withdraw one, AG-EVAL-22), and MUST
NOT stop the evaluation of the other rules and users. The failure MUST be
logged with the rule's name.
*Reason: one broken routine must cost its own advice, not everyone's.*

**AG-EVAL-21** An action that cannot be created, or whose existence cannot be
checked, MUST be logged and skipped; the evaluation MUST go on with the next
one.
*Reason: the next evaluation tries again. Stopping the pass would also stop
the withdrawals still due in it.*

**AG-EVAL-22** A rule whose `check()` or `createAction()` throws — a bridge
call failed, or the routine is faulty — has an unknown outcome:
ActionGenerator MUST NOT create an action for it, and MUST NOT withdraw its
actions, and MUST log it with the rule and the user. The same holds while the
API catalogue is not loaded, when no rule can be decided.
*Reason: only a rule that evaluated to false has stopped matching (PA-RT-03).
This settles EVAL question 1.*

**AG-EVAL-23** A routine MUST be evaluated with one bridge pass per
evaluation pass, for the user evaluated — in a shared workspace for the
primary owner, whose answers cover the workspace (PA-BR-07).
*Reason: one pass answers a repeated call once; the managers already know
what a workspace user sees.*

**AG-EVAL-24** An active routine that does not declare
`var routineContract = 2;` — a context routine (AG-IF-22) — MUST NOT be
evaluated: wherever the rules are loaded (startup, a rule change, a feed
import, a catalogue change) it MUST be held back as a routine whose operation
the catalogue lost is (AG-GEN-20), with a warning naming the rule and the
routine and saying to generate the rule anew. Its rule's actions MUST be
neither created nor withdrawn, as for an unknown outcome (AG-EVAL-22).
*Reason: the context it was written for no longer exists (PA-MIG-02). Its
actions were raised on data that has not been seen since; withdrawing them
would delete advice nobody has found to be stale.*

## 8. Known deviations

No deviations are known.

## 9. Open questions

None is open.
