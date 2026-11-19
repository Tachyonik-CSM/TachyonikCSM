# AssetManager — 4. Security and tenancy

Status: **Accepted** · Area code: `SEC`

Who may do what with AssetManager's records, how callers prove who they are,
and which records each one sees. The records themselves are described in
[2. Data model and behaviour](02-data-model-and-behaviour.md), and the
endpoints in [3. API](03-api.md).

## 1. Callers

| Caller | Proves itself by | Acts for |
|---|---|---|
| **User** (the WebUI) | A session token issued by SystemManager, as `Authorization: Bearer`. On the WebSocket it is sent as the subprotocol instead. | Themself. They see their shared workspace. |
| **Service** | The internal service key, as `X-Internal-Service-Key` | The user it names, or every user (§4) |
| **Anyone** | Nothing | Only the health and readiness endpoints (see [5. Operation](05-operation.md)) |

## 2. Authentication

**AM-SEC-01** Every request under `/api`, every WebSocket handshake, and
`/metrics` (AM-OPS-17) MUST authenticate as a user or as a service. Otherwise
it MUST be refused with 401.
*Reason: the records describe an organisation's weaknesses (AM-PUR-10).*

**AM-SEC-02** A session token MUST be accepted only if all of these hold:
- it is signed with HS256 under the secret shared with SystemManager;
- it carries an expiry, and has not expired;
- it is a session, not one of SystemManager's narrow tokens (the MFA
  challenge, MFA enrolment, first password).

Any other token MUST be refused with 401.
*Reason: a narrow token is issued before the login is complete. Accepting it
as a session would skip MFA. A token without an expiry could never be
revoked by waiting it out. A fixed algorithm closes the algorithm-confusion
attacks.*

**AM-SEC-03** A user's account MUST be active. AssetManager MUST ask
SystemManager, and MAY reuse an answer for at most 30 seconds.
- A deactivated or deleted account MUST be refused with 403.
- When SystemManager cannot be asked, the request MUST be refused with 503,
  not let through.

*Reason: deactivating an account must end its access everywhere, not only at
SystemManager, and within the same bound as there. Letting a request through
whenever SystemManager is down would turn every outage into a bypass.*

**AM-SEC-04** The internal service key MUST be compared in constant time. It
MUST never be valid while none is configured. With no key configured, a
request presenting one MUST be treated as a user's request.
*Reason: the time a wrong guess takes must say nothing about how close it
was. An empty configured key must not make an empty header a valid one.*

**AM-SEC-05** `/api/internal` MUST accept the internal service key only, never
a session token.
*Reason: these endpoints delete or move whole accounts' data (AM-DATA-37,
AM-DATA-38). Their only legitimate caller is SystemManager.*

**AM-SEC-23** `/api/mcp` (AM-API-15) MUST accept a session token only. A
request carrying `X-Internal-Service-Key` MUST be refused with 403, whether or
not a key is configured and whether or not it also carries a session. Each
tool call MUST be made as the API call it stands for, with the caller's
session, so that every rule of this specification applies to it as to that
call ([PA-MCP-06](../../docs/specs/01-platform-api-catalogue.md)).
*Reason: an MCP caller is an AI agent acting for a signed-in user, with that
user's permissions (PA-MCP-05). The service key would let it act for anyone.*

**AM-SEC-06** AssetManager MUST warn at startup when its token secret is a
known placeholder or shorter than the minimum length. It MUST still start.
*Reason: a deployment on the placeholder secret accepts tokens anyone can
sign. A development setup runs on it, though, so a warning, not a refusal
(decided 2026-11-03).*

**AM-SEC-07** Tokens and keys MUST NOT be carried in URLs, and MUST NOT be
logged.
*Reason: URLs end up in proxy and access logs, and logs are read by more
people than the ones who may hold a session.*

## 3. Which records a user sees and changes

**AM-SEC-08** A user's **visible set** MUST be the user and the members of
their shared workspace, as SystemManager reports them. On an appliance, which
is one workspace, that is every user. When SystemManager cannot answer, the
visible set MUST be the user alone.
*Reason: a workspace is how an organisation shares its estate. Falling back
to the user alone is safe: it hides colleagues' records for a moment, but
never shows a stranger's.*

**AM-SEC-09** Lists, statistics and reading one asset MUST cover exactly the
records whose owner is in the visible set.
*Reason: AM-PUR-10.*

**AM-SEC-10** A user MUST NOT change or remove any record: records are what
their source reports, and go only with it (AM-PUR-20, AM-DATA-06). The
visible set is for reading.
*Reason: deletion cannot be undone, and what a source reports must not
change or disappear behind its back. A member of a workspace could once
change the type of a colleague's asset (decided 2026-11-19, formerly
question 2: no one changes an asset).*

**AM-SEC-11** A record outside the visible set MUST be answered as not found
(404), or counted as skipped in a bulk operation, never as forbidden.
*Reason: a 403 would confirm that the id exists in another tenant.*

**AM-SEC-12** A user MUST NOT create a record; a create from a user MUST be
refused with 403 (AM-PUR-20). An owner named in a request MUST be honoured
only from a service. A user who names an owner in a query MUST be refused
with 403.
*Reason: records come from the import of a source. And a user naming an
owner could place records in, or read from, another tenant's account.*

## 4. What a service may do

**AM-SEC-13** A service names the user it acts for: in the body (`userId`),
or with the `X-User-ID` header; the body wins when it names one. A service
that creates a record — an asset, a vulnerability, a detection, or an asset
from a scan coverage table — MUST name its owner; a request that names no
one, or a user id of 0 or below, MUST be refused with 400. An owner that
SystemManager does not know MUST be refused with 400 too, wherever the limit
check asks SystemManager about the owner (AM-DATA-33, AM-DATA-35).
*Reason: imports and routines act for the user whose source or rule they
process. A record without an existing owner is seen by no one, is held to no
limit, and is deleted by the next orphan sweep (decided 2026-11-19, formerly
PUR question 4).*

**AM-SEC-14** A service acting on a source MUST act on that source's records
of every owner.
*Reason: a source belongs to ResourceManager, which deletes it for whoever
owns it. Scoping to one owner would leave the other owners' records behind.*

**AM-SEC-15** The stale endpoints (AM-DATA-28) and the by-source deletions
(AM-DATA-24) MUST be refused to users with 403.
*Reason: the stale endpoints delete by a time the caller chooses, and only
the importer knows which time is the start of its re-import. A source's
records go when ResourceManager deletes the source (AM-PUR-20).*

## 5. The WebSocket

**AM-SEC-16** A browser MUST authenticate the handshake by offering the
session token as its only subprotocol. The same checks MUST apply as to a
request (AM-SEC-02, AM-SEC-03).
*Reason: browsers cannot set the Authorization header on a WebSocket, and a
query parameter would put the token in the URL (AM-SEC-07).*

**AM-SEC-17** A handshake whose `Origin` is not on the configured allow-list
MUST be refused with 403. A handshake without an `Origin` MUST still
authenticate.
*Reason: a page on another site must not be able to open a socket with the
user's browser. A client without an Origin is not a browser, so authentication
alone guards it.*

**AM-SEC-18** A service MUST authenticate the handshake with the internal
service key. It then receives every announcement, unscoped.
*Reason: ActionGenerator reacts to every user's changes.*

**AM-SEC-19** Every announcement about a record MUST go to the connections of
the users entitled to that record, and to no other user's connection: the
owner's, and those of the members of the owner's shared workspace who are
connected — that is, every connection whose visible set (AM-SEC-08), as
last re-checked (AM-SEC-24), contains the record's owner. This holds for every
message type (AM-API-09), whatever the change — a request, a deletion by
source, a stale deletion, or a recomputation — and whether a user or a
service made it. An announcement whose owner is not known MUST NOT go to any
user's connection. Services receive every announcement (AM-SEC-18).
*Reason: AM-PUR-09, AM-PUR-10. Even an id-only message tells another tenant
that a record exists and when it changed. A workspace's members look at the
same records, so a change one of them makes, or an import made for one of
them, must show in the others' open views too, not only in the owner's
(decided 2026-11-11).*

**AM-SEC-24** A user's connection MUST be closed when its token expires.
Every minute, AssetManager MUST check the account of each connected user as
for a request (AM-SEC-03), once per user however many connections they have:

- a deactivated or deleted account MUST have its connections closed, with
  close code 1008;
- otherwise the user's visible set (AM-SEC-08) MUST be looked up again and
  MUST replace the one of each of their connections;
- when SystemManager cannot be asked, the connections MUST stay open, and
  their visible set MUST be the user alone until the next check.

Services' connections (AM-SEC-18) are not checked.
*Reason: a connection opened before a deactivation, or before its token
expired, would otherwise receive announcements for as long as it stays open,
and one opened before a workspace change would keep the old workspace's.
Asking every minute bounds that to a minute, and asking per user keeps it to
one account check and one member look-up per connected user. An outage of
SystemManager must not cut every open view at once, whose reconnects it would
refuse anyway (AM-SEC-03), but a user who cannot be verified receives no one
else's records. (Decided 2026-11-19, formerly SEC question 1.)*

## 6. Requests and answers

**AM-SEC-20** A request body MUST be limited to 1 MiB, except a scan coverage
table, which is limited to 32 MiB. A larger body MUST be refused with 413, or
cut off at the limit when its length is not declared.
*Reason: one request must not be able to grow the service's memory without
bound. A coverage table lists every target a scanner has.*

**AM-SEC-21** Cross-origin answers MUST name only allow-listed origins, and
MUST carry `Vary: Origin`.
*Reason: a cache must not hand one origin's answer to another.*

**AM-SEC-22** Calls AssetManager makes to other services MUST authenticate
with that service's internal key.
*Reason: they read limits and accounts (SystemManager), sources
(ResourceManager) and the licence (AIManager), which those services give
only to services.*

## 7. Known deviations

None known.

## 8. Open questions

None open.
