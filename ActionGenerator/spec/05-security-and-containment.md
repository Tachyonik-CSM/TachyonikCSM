# ActionGenerator — 5. Security and containment

Status: **Accepted** · Area code: `SEC`

ActionGenerator holds no data of its own and serves nobody. Its security
questions are therefore three: how it proves itself to the services it calls,
whose data it puts together and where that goes, and how it keeps the code it
runs — written by a model — within bounds.

## 1. Authentication

**AG-SEC-01** Every call ActionGenerator makes to a platform service, and every
WebSocket connection it opens, MUST carry that service's internal service key.
*Reason: the internal endpoints serve any user's data to whoever holds the
key. They are refused without it.*

**AG-SEC-02** The service keys and the AI's API keys MUST NOT be logged, nor be
put into a context, a routine or an action.
*Reason: the log is read in support cases and shipped to log collectors; a key
in it is a key handed out.*

## 2. Whose data goes where

**AG-SEC-03** In a SaaS installation, a user's context MUST hold only that
user's data, and the actions raised from it MUST be created for that user.
*Reason: users' data is private to them. A context mixing two users would let
one user's assets appear in another's advice.*

**AG-SEC-04** A shared context MUST be built only in a shared workspace
(AG-EVAL-07), and MUST NOT include anonymous sessions.
*Reason: only on an appliance do all registered users see the same data. An
anonymous session is not part of the workspace.*

**AG-SEC-05** The context MUST NOT contain secrets: no credential, password,
key or certificate. Of a tool's credential it carries only whether one is
linked, and what kind its tool needs.
*Reason: a routine needs to know that a credential is missing, never what it
is. What the context holds, a routine can put into an action's text.*

**AG-SEC-06** ActionGenerator MUST NOT send a context, or anything from one,
outside the platform.
*Reason: it describes an organisation's weaknesses. The actions it leads to go
to ActionManager, and through it only to those entitled to them.*

**AG-SEC-07** An action's text MUST be treated as plain text by
ActionGenerator: it MUST NOT be interpreted, and nothing MUST be added to it
that a reader would take as markup.
*Reason: the text is written by a routine from data — an asset's name, an
organisation's — and is shown in every list of actions. Rendering it as
text is the WebUI's part.*

## 3. What reaches the AI

**AG-SEC-08** What ActionGenerator sends to an AI MUST be limited to the system
prompt and the rule with its option conditions (AG-GEN-07). No context, no
user, organisation, asset or other record of any user, and no action MUST be
sent.
*Reason: the AI may be an external provider. A rule is an administrator's
text about situations in general; a context is one organisation's data.*

## 4. Containing generated code

Routine code is written by a model from a prompt, and an administrator's
wording can steer it. It is treated as untrusted on every path.

**AG-SEC-09** Every call into a routine — loading it, `check()`,
`createAction()`, each option condition, and reading what they return — MUST
run within the configured execution budget (AG-OPS-03) and be interrupted when
it overruns. The interruption MUST fail that call only, and MUST NOT stop the
daemon.
*Reason: the engine cannot be preempted. A routine that never terminates
would hang its goroutine for the life of the process. Reading a returned
object runs getters the routine defines, so it needs the budget too.*

**AG-SEC-10** The call stack of a routine MUST be bounded, so that runaway
recursion fails as an error.
*Reason: the budget stops a loop; recursion exhausts memory, and can do so
faster than the timer fires.*

**AG-SEC-11** A routine MUST be given nothing but the context: no access to
the network, files, processes, the services, or functions of the daemon.
*Reason: a routine needs to decide, not to act. Whatever is not handed in
cannot be misused.*

**AG-SEC-12** Every evaluation of a routine — for each user, the shared
workspace, and each mock scenario — MUST run in a fresh JavaScript engine.
*Reason: a global a routine sets while looking at one user's data must not
reach the next user's evaluation, or their action's text.*

**AG-SEC-13** What a routine returns MUST be checked before it becomes an
action (AG-IF-12, AG-IF-13), and stored routine code MUST match its recorded
checksum before it runs (AG-GEN-16).
*Reason: the routine's output reaches a database and every screen that lists
actions. Code changed in the database after it was validated must not run.*

## 5. Known deviations

None known.

## 6. Open questions

None open.
