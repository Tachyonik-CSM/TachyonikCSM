# TachyonikLib

Shared Go library (`module tachyonik/lib`) holding functionality that is used
by one or more Tachyonik service modules. Each service depends on it via a
`replace` directive pointing at this directory:

```
require tachyonik/lib v1.0.0
replace tachyonik/lib => ../TachyonikLib
```

## Packages

| Package | Purpose |
|---------|---------|
| `logger` | Small leveled logger (DEBUG/INFO/WARN/ERROR) writing to console and/or file, with package-level helpers. `Startup(module, version)` writes the line every module logs first: `Starting Tachyonik <Module> <version> (<go version>, <os>/<arch>)`. |
| `drain` | Lets a daemon stop without cutting a unit of work in half (an action rule evaluated for one user; an execution run and the report of its outcome). Units `Enter` a `Gate` and `Leave` it; on SIGINT/SIGTERM the daemon closes its watchers and calls `Stop` — nothing enters any more and a long pass sees `Stopping` — then `Wait(timeout)` lets the running units finish, and names those still running when the time is up, calling their `OnCutOff` so what was cut off is recorded. Used by ActionGenerator and ActionExecutor. |
| `reload` | Loads a daemon's rules again whenever asked — on every (re)connect to AIManager, after a feed import — one load at a time, coalescing requests made meanwhile. A load that fails is tried again every 30 s until one succeeds, so a daemon started before AIManager does not stay without rules. Used by ActionGenerator, ActionExecutor, SourceAnalyser and SourceImporter. |
| `heartbeat` | Sends periodic liveness signals from a daemon (no HTTP server of its own) to SystemManager. Parameterized via `heartbeat.Config{ServiceName, …}`. |
| `aicode` | Tidies the JavaScript a model returns before it is parsed: `Clean` strips a markdown fence and replaces typographic quotation marks. `Checksum(code)` is the SHA-256 every routine is stored with, and `VerifyChecksum(code, recorded)` refuses a routine whose checksum is missing or not its code's — called by every module before it runs a routine and by AIManager before it stores one. `RequestedBefore(requestedAt, start)` tells which generation requests a daemon may clear as left behind by an earlier run: those made before it started. Used by ActionExecutor, ActionGenerator, SourceAnalyser, SourceImporter, ToolManager and AIManager. |
| `aipick` | Decides which AI generates a routine, at the moment it is generated: the rule's own AI, otherwise the module's current default as AIManager reports it now, otherwise (AIManager unreachable) the AI loaded last. A `Choice` describes itself for errors and logs, e.g. `AI "claude opus 5" (id 6, anthropic, model claude-opus-5; module default)`, never with its URL or key. Used by every module that generates code. |
| `aimwatcher` | Maintains one WebSocket connection to AIManager and dispatches analysis-rule changes, bulk feed imports, and module-AI-setting updates to caller-registered handlers, reconnecting automatically. |
| `systemmanager` | Thin outbound client for reporting audit-trail events to SystemManager. Modules needing further endpoints embed this `Client` and add methods locally. |
| `restclient` | The request mechanics every service client repeats: join a path onto the base URL, attach `X-Internal-Service-Key`, send, check the status, decode JSON. Leaves a client holding only its wire types and one line per endpoint. Redirects are never followed — Go strips `Authorization` across hosts but forwards custom headers verbatim, so a 302 would hand the service key to whatever host it names. A wrong status comes back as a typed `*StatusError`, so a caller can react to one code (a 404 becoming "not found") without matching on message text. It carries the reason the upstream gave — the `error` of the managers' `{"error": "…"}` body, or the first line of a plain-text one, cut to 300 characters — so a refusal reads `unexpected status code: 403: Host asset limit reached (25/25)` in a log or an audit entry rather than only the code. An endpoint with more than one shape of success names the others after the wanted status, e.g. `JSON("POST", "/api/assets", req, &out, http.StatusCreated, http.StatusOK)`. |
| `textextract` | Turns a file or a retrieved web page into the text that analysis, import routines and prompts work on. Text formats pass through unchanged; a PDF's text layer is extracted; HTML is rendered as the text a visitor sees (scripts and styling dropped, block elements kept on separate lines) together with its resolved links, a helper that picks out an imprint/contact link, and the images the page offers as its own logo. Those are ranked:
  1. an image or inline `<svg>` in the site's home link or branding block (inline SVGs come back as markup, since they have no URL);
  2. the `apple-touch-icon`;
  3. "logo" images naming the site's domain;
  4. other "logo" images;
  5. the favicon;
  6. the `og:image`.

  Logos inside a references, customers, partners or slider block, or in a wall of three or more logos, come last, and at most three "logo" images are offered, so a wall of customer logos can't push out the site's own icons. Bounded at both ends (see below). Best-effort and never fatal. |
| `httpguard` | Makes an outbound request to a user-influenced address survivable: refuses loopback, unspecified, link-local (cloud metadata), multicast and private/ULA addresses. Three layers — validate the URL up front, re-validate every redirect hop, and check again in the dialer at connect time, which is what defeats DNS rebinding. Used by ChatAI for user-supplied AI endpoints and by SystemManager for the organisation homepage fetch. |
| `providererr` | Turns a failed AI-provider HTTP response into a short reason that is safe to show the user who configured that provider. Echoes no bytes of the body: it lifts at most `message` and `type` out of a document that has to declare a JSON content type and parse as a provider error envelope (`{"error":{"message":…}}` or `{"error":"…"}`), then flattens it to one line and cuts it to 300 runes. Lets a user see "credit balance is too low" instead of "status 400" without turning a user-supplied endpoint into an SSRF read primitive. |
| `proxyrelease` | Tells the latest TachyonikProxy release from the signed auto-update manifest the proxies update from (`manifest.json` + its detached Ed25519 signature `manifest.json.sig`). Believes it only with a valid signature from an embedded release key (`proxyrelease/pubkeys`, the same key the proxy embeds — the proxy's selfupdate tests check the copies agree; rotate both), schema 1, the `stable` channel and a numeric version. Compares versions numerically, as the proxy does; `UpdateAvailable` judges a development build (`1.2.0-2-g8882cc9-dirty`) by the release it was built from. Used by ResourceManager to mark outdated proxies. |
| `jsctxtrace` | Records which properties JavaScript reads from an object — a mock answer the API bridge hands a routine — by path (`questionnaires.nis2Impact.level`, with `[]` for array elements), and which of those do not exist. Behaves like a plain goja conversion otherwise: array methods, `Object.keys`, JSON and `in` work, writes go to a private copy. Used by `apibridge`'s mock passes, so validation and the dry run can tell a misspelt field from a correct rule no scenario satisfies. |
| `apicatalogue` | Reads a manager's API document (OpenAPI 3.1, `<Manager>/spec/openapi.yaml`) for the platform API catalogue ([docs/specs/01-platform-api-catalogue.md](../docs/specs/01-platform-api-catalogue.md)): its operations with their `x-bridge`, `x-bridge-bound`, `x-bridge-hidden`, `x-invalidated-by` and `x-mcp` marks, parameters, answer schemas and the mock answers per scenario. `Check` reports what makes a document unfit for the catalogue; a schema can `Validate` a value, `Filter` it down to the documented fields, and say whether it documents a path a routine read (`HasPath`). Reads the subset of OpenAPI the documents use (local `$ref`, `allOf`, type lists for nullability) and refuses `oneOf`/`anyOf`/`not` rather than misread them. `Scenarios` is the list of mock scenarios; a test keeps it equal to `docs/specs/mock-scenarios.md`. `Modules` lists the managers that register a document. |
| `apibridge` | The API bridge: an `api` object for a goja runtime with one function per bridge operation of the catalogue, `api.<namespace>.<verb>({…parameters, body})`. Built from the documents at run time; profiles *read* (ActionGenerator) and *read-write* (ActionExecutor). The bridge sets what names the user and the user's role (bound parameters, header and body fields), the method, path and host; a pass records write answers, so a run can audit what its writes did; a routine can pass only documented parameters. Answers are checked against their schema and reduced to the documented fields, cached per pass (a write clears the user's cache), and a failure throws a JavaScript `Error` naming the operation. Reading a name `api` or a namespace does not have throws a `TypeError` naming it and what exists, and is a violation of a mock pass, never a call. A mock pass answers from the documents' examples for one scenario and records what a routine called and read, for validation. |
| `apiholder` | A routine-running module's copy of the platform API catalogue and the bridge built from it, in one profile (read-only for ActionGenerator, read-write for ActionExecutor): loaded from AIManager's documents, it answers which of a routine's operations are gone or deprecated, which messages invalidate its reads, and renders the operations — with a write's body, without what the bridge sets — for the AI that writes a routine. `Gaps` names the managers the loaded catalogue leaves out: not registered, or no URL configured in the holding module (whose operations are then not offered). |
| `apicontract` | Checks a hand-written client of a manager against that manager's API document (`<Manager>/spec/openapi.yaml`). A client declares its calls in a test — method, path, query parameters, the Go types it sends and decodes — and `Test` reports a path or parameter the document lacks, a body or answer it does not document, and any JSON field the client's types send or decode that the schema leaves out; `TestDeclared` confirms every API path written in the client's code is declared. On the manager's side, `CheckAnswer` validates a real handler answer against its documented schema. Types with their own `MarshalJSON`, and maps, are checked by what they encode. |
| `apiregister` | Registers a manager's API document with AIManager at startup (`PUT /api/internal/api-catalogue/{module}`), in the background and retried until AIManager takes it; a refusal of the document (400, 409) is logged and not retried. |
| `apimcp` | A manager's MCP server generated from its API document (PA-MCP-01 to -06): one tool per operation marked `x-mcp`, named by its `operationId` and described by its summary, description, parameters and answer fields (without `x-bridge-hidden` ones or what the bridge binds); a call is replayed as the API call through the manager's router with the caller's credentials, and the answer cut to the documented fields. `New` refuses a document whose MCP operation needs a bridge-bound parameter. `Check` is the test every manager runs on its real router (PA-MCP-03): `tools/list` offers exactly the marked operations (`Marked`), and a signed-in user reaches each of them. ToolManager builds the chat's tools from the same code. |
| `listquery` | Reads the optional parameters a manager's list operation offers to order and cut its answer: `sort` (a field from the operation's own list, each with the SQL expression it sorts by), `order` (`asc`/`desc`, default ascending for text, descending for numbers and times) and `limit` (1–`MaxLimit`, 500), plus a bounded whole-number parameter (`Int`) and `Thin`, which cuts a series to at most n evenly spread points keeping the first and the last. A bad value is an `*Error` naming what is allowed (answered with 400), never ignored; a sort value only looks up a field and is never written into SQL. `SortStable`/`Cut` do the same in Go for a list that is not sorted in its query. A list that pages opts into `offset` with `ParseOffset` (refused without `limit`); `LimitSQL` and `Cut` then skip that many rows — AssetManager's asset, vulnerability and detection lists. Used by AssetManager, ActionManager, ResourceManager and SystemManager, so the chat's tools generated from their operations can ask for "the 25 most severe". |
| `settings` | Reads a module's number, true/false and log-level settings from the environment and its parsed config.yaml, and refuses a value it cannot use instead of dropping it: an empty variable is unset; true/false is `true`, `false`, `1` or `0` in any case; a number is whole and within the bounds the setting gives (`Min`, `Max`, `Range`, `ZeroMeansOff`); a log level is DEBUG, INFO, WARN or ERROR in any case. In the file a 0 counts as an absent key. A `Reader` collects every fault, naming the setting, its source and what it takes, and `Err` returns them all, so the module stops at startup with the whole list. Text settings are not read, so no secret appears in an error. |
| `aiclient/claude` | Client for the Anthropic (Claude) Messages API. |
| `aiclient/openai` | Client for OpenAI-compatible APIs (OpenAI, Mistral, Google OpenAI-compat, self-hosted). |
| `aiclient/ollama` | Client for the Ollama API (optional Bearer auth for fronted installs). |

`internal/safehttp` carries the outbound-HTTP precautions the clients share
(redirect policy, response-size bounds, cleartext-credential detection). It is
internal to `tachyonik/lib`: consumers get the behaviour through the clients.

All three `aiclient` provider clients expose the same method —
`Chat(model, systemPrompt, userPrompt) (string, error)` — so a caller can
dispatch on the configured provider and treat the result uniformly. The library
deliberately exports no interface for this: consumers declare a local
one-method interface of that shape, which keeps them from depending on where
the interface lives and keeps their test fakes local.

## Security posture

The library enforces these itself, so a consumer cannot forget them:

- **Redirects are never followed.** Go's `http.Client` strips `Authorization`
  on a cross-host redirect but forwards custom headers verbatim, so a `302`
  would hand `x-api-key` or `X-Internal-Service-Key` to whatever host it names.
  Every client here returns the `3xx` to the caller instead. None of the JSON
  APIs involved legitimately redirect.
- **Response bodies are bounded.** Chat replies are capped at 16 MiB and error
  bodies at 32 KiB, so a hostile or misconfigured endpoint cannot exhaust memory
  through a response. An oversized body is an explicit error, not a truncated
  one — a decoder never sees half a document.
- **PDF extraction is bounded at both ends.** `textextract.MaxInputBytes`
  (64 MiB) rejects an oversized PDF before parsing and `MaxTextBytes` (32 MiB)
  caps what one document may expand to. A PDF's text lives in Flate-compressed
  streams, so a small file can decompress to an arbitrarily large one, and the
  `recover()` around the parser does not help: Go cannot recover from an
  out-of-memory condition. Oversized input yields no text; over-long text is
  truncated, so rules still match against the leading portion.
- **The WebSocket connection is bounded and deadlined.** `aimwatcher` caps a
  frame at 1 MiB (gorilla's default is unlimited) and applies a 90 s read
  deadline, so a half-open connection reconnects instead of parking a goroutine
  in `ReadMessage` and silently missing every rule and settings update.
- **Cleartext credentials are flagged, not blocked.** Configuring a credential
  against a non-TLS URL logs a warning and proceeds, which keeps existing
  loopback and internal-network deployments working.

Not covered here: the Go toolchain itself. `govulncheck` reports reachable
standard-library advisories whenever the builder image trails the supported Go
release line — worth re-running after a toolchain bump.

## Requirements

- Go 1.24 or later.
- Three third-party dependencies, each confined to a single package:
  `github.com/gorilla/websocket` (used by `aimwatcher`),
  `github.com/ledongthuc/pdf` (used by `textextract`) and
  `github.com/dop251/goja` (used by `jsctxtrace`). Every other package uses
  only the standard library. goja is required at the same version ActionGenerator
  and ActionExecutor use, so depending on this library does not move theirs.

## Consumers

All twelve service modules depend on this library:

| Module | Packages used |
|--------|---------------|
| ActionExecutor | `logger`, `settings`, `aicode`, `drain`, `reload`, `heartbeat`, `aimwatcher`, `systemmanager`, `aiclient/*`, `apibridge`, `apiholder`, `apicatalogue` |
| ActionGenerator | `logger`, `settings`, `aicode`, `drain`, `reload`, `heartbeat`, `aimwatcher`, `aiclient/*`, `apibridge`, `apiholder`, `apicatalogue` |
| ActionManager | `logger`, `settings`, `listquery`, `apimcp` — and `apiregister`, with `apicatalogue` and `apibridge` in tests |
| AIManager | `logger`, `settings`, `aicode`, `textextract`, `aiclient/*`, `apicatalogue`, `apimcp`, `apiregister` (its request type), `apibridge` (the dry run of bridge routines) |
| AssetManager | `logger`, `settings`, `systemmanager`, `listquery`, `apimcp`, `apiregister`, `apicatalogue` and `apibridge` (in tests) |
| ChatAI | `logger`, `settings`, `aimwatcher`, `providererr` |
| ResourceManager | `logger`, `settings`, `systemmanager`, `proxyrelease`, `listquery`, `apimcp` — and `apiregister`, with `apicatalogue` and `apibridge` in tests |
| SourceAnalyser | `logger`, `settings`, `aicode`, `reload`, `heartbeat`, `aimwatcher`, `systemmanager`, `restclient`, `textextract`, `aiclient/*` |
| SourceImporter | `logger`, `settings`, `aicode`, `reload`, `heartbeat`, `aimwatcher`, `systemmanager`, `restclient`, `textextract`, `aiclient/*` |
| SystemManager | `logger`, `settings`, `listquery`, `apimcp` — and `apiregister`, with `apicatalogue` and `apibridge` in tests |
| TachyonikProxy | `logger`, `settings`, `aicode` (and `proxyrelease` in a test, to check both copies of the release key agree) |
| ToolManager | `logger`, `settings`, `aicode`, `aimwatcher`, `providererr`, `apimcp` (its own MCP server and the chat's generated tools) — and `apiregister`, with `apicatalogue` and `apibridge` in tests |

## Versioning

There is no version to track here, by design.

Every consumer's `go.mod` carries `require tachyonik/lib v1.0.0`, but that
version is inert. Under a filesystem `replace` Go never resolves it — it only
checks that the string is syntactically `v0` or `v1`. Substituting
`v1.999.42`, a version that exists nowhere, builds exactly the same. The line
is there because `go.mod` demands a version on a `require`, not because
anything reads one.

The unit of versioning is the TachyonikCSM commit. Every consumer compiles
against this working tree, so two services cannot end up on different versions
of this library — which is the problem semantic versioning solves, and it
cannot arise here.

Compatibility is therefore enforced by the compiler rather than described by a
number. From the repository root:

```
make lib-check
```

builds, vets and tests this module and all twelve consumers. Run it after every
change here. A removed or re-typed export fails immediately and names every
call site, which is a stronger guarantee than a major-version bump — and unlike
a version number, it cannot be forgotten.

The contrast with TachyonikProxy is the useful one. The proxy does carry real
versions (`tachyonikproxy/X.Y.Z` tags, `git describe --match`, a
release-version guard) because it ships packaged artifacts to machines that
update independently of this repository. That separation between producer and
consumer is what makes versioning necessary, and this library has none of it.

**Revisit this** when the library gains a consumer outside this repository — a
second repo, a published module, or any consumer that pins a version instead of
path-replacing it. At that point the version stops being decorative, and the
scheme TachyonikProxy uses is the model to copy.

## Build

Because the module is wired into consumers with a local `replace`, building the
consumer with `go build ./...` resolves this library from `../TachyonikLib`. For
container builds the consumer's Dockerfile uses the repository root as the build
context and copies both the service directory and `TachyonikLib/` before
building.
