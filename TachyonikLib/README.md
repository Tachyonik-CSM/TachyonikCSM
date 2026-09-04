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
| `logger` | Small leveled logger (DEBUG/INFO/WARN/ERROR) writing to console and/or file, with package-level helpers. |
| `heartbeat` | Sends periodic liveness signals from a daemon (no HTTP server of its own) to SystemManager. Parameterized via `heartbeat.Config{ServiceName, …}`. |
| `aimwatcher` | Maintains one WebSocket connection to AIManager and dispatches analysis-rule changes, bulk feed imports, and module-AI-setting updates to caller-registered handlers, reconnecting automatically. |
| `systemmanager` | Thin outbound client for reporting audit-trail events to SystemManager. Modules needing further endpoints embed this `Client` and add methods locally. |
| `textextract` | Turns a file or a retrieved web page into the text that analysis, import routines and prompts work on. Text formats pass through unchanged; a PDF's text layer is extracted; HTML is rendered as the text a visitor sees (scripts and styling dropped, block elements kept on separate lines) together with its resolved links and a helper that picks out an imprint/contact link. Bounded at both ends (see below). Best-effort and never fatal. |
| `httpguard` | Makes an outbound request to a user-influenced address survivable: refuses loopback, unspecified, link-local (cloud metadata), multicast and private/ULA addresses. Three layers — validate the URL up front, re-validate every redirect hop, and check again in the dialer at connect time, which is what defeats DNS rebinding. Used by ChatAI for user-supplied AI endpoints and by SystemManager for the organisation homepage fetch. |
| `providererr` | Turns a failed AI-provider HTTP response into a short reason that is safe to show the user who configured that provider. Echoes no bytes of the body: it lifts at most `message` and `type` out of a document that has to declare a JSON content type and parse as a provider error envelope (`{"error":{"message":…}}` or `{"error":"…"}`), then flattens it to one line and cuts it to 300 runes. Lets a user see "credit balance is too low" instead of "status 400" without turning a user-supplied endpoint into an SSRF read primitive. |
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
- Two third-party dependencies, each confined to a single package:
  `github.com/gorilla/websocket` (used by `aimwatcher`) and
  `github.com/ledongthuc/pdf` (used by `textextract`). Every other package uses
  only the standard library.

## Consumers

All twelve service modules depend on this library:

| Module | Packages used |
|--------|---------------|
| ActionExecutor | `logger`, `heartbeat`, `aimwatcher`, `systemmanager`, `aiclient/*` |
| ActionGenerator | `logger`, `heartbeat`, `aimwatcher`, `aiclient/*` |
| ActionManager | `logger` |
| AIManager | `logger`, `textextract`, `aiclient/*` |
| AssetManager | `logger`, `systemmanager` |
| ChatAI | `logger`, `aimwatcher`, `providererr` |
| ResourceManager | `logger`, `systemmanager` |
| SourceAnalyser | `logger`, `heartbeat`, `aimwatcher`, `systemmanager`, `textextract`, `aiclient/*` |
| SourceImporter | `logger`, `heartbeat`, `aimwatcher`, `systemmanager`, `textextract`, `aiclient/*` |
| SystemManager | `logger` |
| TachyonikProxy | `logger` |
| ToolManager | `logger`, `aimwatcher`, `providererr` |

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
