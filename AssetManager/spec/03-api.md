# AssetManager — 3. API

Status: **Accepted** · Area code: `API`

How other parts of the platform talk to AssetManager: over HTTP, and over the
WebSocket on which it announces its changes. The endpoints, parameters, bodies
and status codes are in **[openapi.yaml](openapi.yaml)**. This document gives
the rules all endpoints share, and the WebSocket messages, which OpenAPI does
not describe. What the endpoints do to the records is in
[2. Data model and behaviour](02-data-model-and-behaviour.md). Who may call
them is in [4. Security and tenancy](04-security-and-tenancy.md).

## 1. The endpoints in brief

| Path | GET | POST | PATCH | DELETE |
|---|---|---|---|---|
| `/api/assets` | list | create, or report again | | by id |
| `/api/assets/{id}` | one | | type | |
| `/api/assets/by-source` | | | keep without the source | delete |
| `/api/assets/stale` | | | | after a re-import (service) |
| `/api/assets/scan-coverage` | | apply a table | | |
| `/api/assets/stats` | statistics | | | |
| `/api/vulnerabilities`, `/api/detections` | list | create | | by id |
| `…/by-source` | | | keep without the source | delete |
| `…/stale` | | | | after a re-import (service) |
| `/api/internal/…` | user ids, usage (AM-DATA-45) | score recalculation | move a user's records | delete a user's records |
| `/api/mcp` | | the MCP server (AM-API-15) | | |
| `/health`, `/ready`, `/metrics` | see [5. Operation](05-operation.md) | | | |

## 2. Requests and answers

**AM-API-01** Request and answer bodies MUST be JSON, field names in
lowerCamelCase.
*Reason: one convention for the WebUI and every service client.*

**AM-API-02** An error MUST be answered as `{"error": "<message>"}`, with the
status code saying what kind of error it is:
- 400: the request cannot be read, or is invalid;
- 401: not authenticated;
- 403: not allowed, a limit was reached, or the account is deactivated;
- 404: no such record in the visible set;
- 413: the body is too large;
- 500: the request failed;
- 503: a service that must be asked could not be.

*Reason: a client must be able to show the message and react to the kind
without parsing text. This holds for the refusals of the authentication, the
body limit and the record limits too, which were plain text until
2026-11-18.*

**AM-API-03** Creating MUST answer 201 with the record as stored. Reporting an
existing asset again MUST answer 200 with the asset.
*Reason: an importer counts what was new. The stored record shows the
canonical type and the derived values.*

**AM-API-04** A list MUST answer every matching record of the visible set,
unless the caller asks for fewer with `limit` (AM-API-16), with their number
and, where a limit exists, the limit in effect (0 meaning none).
*Reason: the WebUI shows "n of limit" without a second request.* There is no
paging. At the sizes seen so far (hundreds to a few thousand records per
user) none is needed. This is to be revisited when a user's lists reach tens
of thousands.

**AM-API-05** A list MAY be narrowed by `?filter=`, a JSON filter of rules
joined by AND or OR, on the columns listed in openapi.yaml. A filter with a
rule that cannot be read — an unknown column, an operator the column does not
take, or a value it cannot read — MUST be refused with 400, naming the rule
and why: `Filter rule 2: "severity" needs a number, not "high"`.
*Reason: a rule left out lists more than was asked for, with nothing to say
so, and is mistaken for the answer. A column does not disappear without a
release in which both work (AM-API-08), so a saved filter is not broken by
surprise (decided 2026-11-13).*

**AM-API-16** The lists of assets, vulnerabilities and detections MAY be
ordered and cut by three optional query parameters, applied after the filter:
- `sort`, a field of the answer from the list's own set, documented as an enum
  in openapi.yaml — assets: severity, score, name, type, createdAt,
  modifiedAt, lastSeen, lastScanned; vulnerabilities: severity, name, host,
  createdAt, modifiedAt, lastSeen; detections: name, host, createdAt,
  modifiedAt, lastSeen. Times MUST sort as the times they stand for, text
  ignoring case; records with equal values stay newest first.
- `order`, `asc` or `desc`; without it text fields sort ascending and the
  others descending (highest, newest first).
- `limit`, a whole number from 1 to 500: at most that many records are
  answered, while `total` MUST still count every matching record.

Without any of them a list MUST answer exactly as before. An unknown field,
an order other than `asc` or `desc`, `order` without `sort`, or a limit that
is not a whole number from 1 to 500 MUST be refused with 400, naming what is
allowed (`sort must be one of severity, name, …, not "risk"`). The field's
SQL comes from the list's own set only; the parameter's value MUST NOT be
written into a query.
*Reason: the chat's AI tools are generated from these operations (PA-MCP-01)
and can only ask for what an operation offers. "The 25 most severe" must be a
question the list answers, rather than a few thousand records sent to the AI
to sort and cut. `total` is how a caller learns that the list was cut. A bad
value is refused, not ignored, for the reason of AM-API-05: an answer to
another question is mistaken for the answer (decided 2026-11-17).*

**AM-API-06** The by-source and stale endpoints MUST be called with the source
reference `?source=filename (ID: N)`, and MUST refuse with 400 a reference
without `(ID: N)`.
*Reason: records are matched by the id (AM-DATA-12). A name alone would match
nothing, and a deletion that silently does nothing is easily mistaken for
one that worked.*

**AM-API-07** Times MUST be answered in RFC 3339. A time in a request MUST be
read in RFC 3339, or without a zone. An unreadable time MUST be ignored with
a warning:
- an unreadable `lastSeen` becomes the time of the request;
- an unreadable coverage time is left out.

The record MUST NOT be refused because of it.
*Reason: imports read times out of files of every kind. Losing the host over
one bad date costs more than the date.*

**AM-API-08** A field MUST NOT be removed or renamed without a release in
which both the old and the new form work, the old one logged as deprecated.
*Reason: the services are updated one after another, not at once. An example
is `source` on finding create requests (AM-DATA-16).*

**AM-API-13** Every deletion and unlinking MUST answer alike, for assets,
vulnerabilities and detections, by id, by source, unlinking a source and
stale:

```json
{"deleted": 2, "deletedIds": [17, 18],
 "updated": 1, "updatedIds": [21],
 "skipped": 0, "skippedIds": []}
```

- `deleted`: the records that are gone.
- `updated`: the records that remain but changed: they lost the source, or an
  asset was recomputed because another source still reports it.
- `skipped`: records named by id that were not deleted: one a source still
  reports (AM-DATA-27), or one outside the caller's own records (AM-SEC-10,
  counted as skipped per AM-SEC-11).

Every pair MUST be present, 0 and `[]` when it does not apply, and each count
MUST be the length of its list.
*Reason: a client updates its lists from the ids, without reading them
again. Answers that differed by kind and endpoint let a client do that after
some deletions and not after others: the WebUI removed every selected asset,
including the skipped ones (decided 2026-11-13).* The fields of the earlier
answers — `message`, and `ids` on the finding by-source answers — are still
sent for one release (AM-API-08) and are removed in the next.

## 3. The WebSocket

**AM-API-09** Every change to a record MUST be announced on `/ws` as
`{"type": "<TYPE>", "payload": {…}}`:

| Type | Payload | Sent when |
|---|---|---|
| `ASSET_CREATED` | the asset | an asset is created, by a request or a coverage table |
| `ASSET_UPDATED` | `{"id"}` | an asset is reported again, its type changes, its derived values or coverage change, or it loses a source |
| `ASSET_DELETED` | `{"id"}` | an asset is deleted |
| `VULNERABILITY_CREATED` | the vulnerability | a vulnerability is created |
| `VULNERABILITY_UPDATED` | `{"id"}` | a vulnerability loses its source |
| `VULNERABILITY_DELETED` | `{"id"}` | a vulnerability is deleted |
| `DETECTION_CREATED` | the detection | a detection is created |
| `DETECTION_UPDATED` | `{"id"}` | a detection loses its source |
| `DETECTION_DELETED` | `{"id"}` | a detection is deleted |

*Reason: AM-PUR-09. A created record is sent whole, because a list can insert
it as it is. A change sends only the id, and the client reads the record
again, so it never shows a half-updated one.* Who receives which message is
in AM-SEC-18 and AM-SEC-19.

**AM-API-10** A client MAY send `{"type": "PING"}`, which MUST be answered
with `{"type": "PONG", "payload": null}`. A message from a client larger than
512 bytes MUST close the connection.
*Reason: a keep-alive is the only thing a client has to say. Anything larger
is not one.*

**AM-API-11** Announcements are a notification, not a log. A client MUST NOT
rely on receiving every one. After reconnecting, it MUST read the records
again.
*Reason: messages sent while a client was disconnected are not kept.*

## 4. The contract document

**AM-API-12** `openapi.yaml` MUST describe every endpoint as implemented. A
change to an endpoint MUST update it in the same commit.
*Reason: the WebUI, SourceImporter, ResourceManager, ActionGenerator,
ActionExecutor and SystemManager are all written against this API. A
contract that lags behind the code misleads all of them at once.*

**AM-API-14** `openapi.yaml` is AssetManager's document in the platform API
catalogue, and MUST follow the catalogue's rules
([PA](../../docs/specs/01-platform-api-catalogue.md)):
- every `operationId` is `<namespace>.<verb>`; AssetManager's namespaces are
  `assets`, `vulnerabilities`, `detections` and, for the service's own
  operation, `assetManager` (PA-CAT-03);
- the operations routines may use are marked `x-bridge: read` — `assets.list`,
  `assets.get`, `assets.stats`, `vulnerabilities.list`, `detections.list` —
  or `x-bridge: write` — `assets.create`, `assets.update`,
  `assets.deleteByIds` (PA-CAT-04);
- the bridge names the user by `X-User-ID`, which answers with the user's
  visible set (AM-PUR-10), and never by the query `userId`, which narrows it
  to that one user (`x-bridge-bound`, PA-CAT-05, PA-BR-07);
- each read operation lists the WebSocket messages that invalidate it
  (`x-invalidated-by`, PA-CAT-07), and answers every mock scenario in a named
  example (PA-CAT-08).

A test MUST fail when a route is served but not documented, or documented but
not served (PA-CAT-02), and when a bridge operation's answer, called through
the API bridge, does not match its schema.
*Reason: routines read AssetManager through this document and nothing else.
It must be the code's own description, checked by the code's tests (decided
2026-11-16).*

**AM-API-15** AssetManager MUST serve its MCP server at `POST /api/mcp`:
MCP's JSON-RPC, one JSON object per request, built at startup from
`openapi.yaml`, with one tool per operation marked `x-mcp` and no other
(PA-MCP-01, PA-MCP-04). Who may call it is AM-SEC-23. The endpoint itself is
documented in `openapi.yaml` and is never marked `x-bridge` or `x-mcp`.
*Reason: one MCP server per manager keeps each server's tools, their
documentation and their authorisation with the module that owns the data
(decided 2026-11-16).*

## 5. Known deviations

None known.

## 6. Open questions

None open.
