<!--
TachyonikProxy
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# TachyonikProxy — Security Concepts

This document describes the security model that TachyonikProxy is built on. It covers the network surfaces the proxy exposes, how the proxy verifies code and configuration it accepts from the Tachyonik platform, how locally executed tools are sandboxed, and how auto-updates establish authenticity. The intended audience is operators evaluating the proxy for production deployment and security reviewers.

The companion runtime documentation lives in [README.md](./README.md). This file focuses on the *why* and on the design decisions; the README covers the *what* and *how*.

## Table of contents

1. [Threat model summary](#1-threat-model-summary)
2. [Communication security](#2-communication-security)
3. [Verification of code and data received from ToolManager](#3-verification-of-code-and-data-received-from-toolmanager)
4. [Tool execution sandboxing](#4-tool-execution-sandboxing)
5. [Configuration trust](#5-configuration-trust)
6. [Enrollment](#6-enrollment)
7. [Auto-update trust chain](#7-auto-update-trust-chain)
8. [Privilege and process model](#8-privilege-and-process-model)
9. [Concurrency and atomicity](#9-concurrency-and-atomicity)
10. [Logging and observability](#10-logging-and-observability)
11. [Out of scope / customer responsibilities](#11-out-of-scope--customer-responsibilities)
12. [Reporting security issues](#12-reporting-security-issues)
13. [Appendix A: CWE mapping](#appendix-a-cwe-mapping)

---

## 1. Threat model summary

TachyonikProxy runs on a customer host and connects a customer's local security tools (Nmap, Nikto, sqlmap, …) to the Tachyonik platform. Two security boundaries are continuously crossed during normal operation:

- **Customer host ↔ Tachyonik platform** — over the public Internet (or a customer VPN). Untrusted intermediate networks.
- **Proxy process ↔ local OS / local tools** — the proxy executes external binaries on behalf of remote callers.

Trust anchors used by the proxy:

| Channel                          | Authenticity anchor                                                                 |
|----------------------------------|-------------------------------------------------------------------------------------|
| MCP control plane (tools, scan) to ToolManager | mTLS — client cert signed by Tachyonik CA, configured at enrollment time |
| Auto-update manifest fetch       | Ed25519 public keys compiled into the binary (`go:embed`)                           |

The mTLS anchor and the auto-update anchor are intentionally **independent**. A compromise of the ToolManager service cannot, by itself, be used to push a malicious binary update — the manifest signing key is held offline and not on any TLS-terminating host. Conversely, a CDN compromise cannot be used to push a malicious tool routine — the proxy will not accept tool definitions from anything other than an mTLS-authenticated ToolManager.

Adversaries assumed in scope:

- **Network attacker** with full read/write of the underlay network (TLS-only mitigations apply).
- **CDN compromise** — an attacker who can substitute bytes at the auto-update download URL but cannot produce valid Ed25519 signatures (auto-update signatures + multi-key rotation apply).
- **Hostile or compromised ToolManager** — limited to the MCP surface; cannot push binary updates and cannot bypass the proxy's local execution sandboxing.
- **Local unprivileged user on the proxy host** — restricted by filesystem permissions, systemd hardening, and the lock file on the auto-update state directory.

Adversaries explicitly **not** assumed in scope:

- A compromise of the Tachyonik manifest signing key. This is mitigated by keeping the key offline and supporting two embedded public keys for rotation, but no further compensating control is implemented at the proxy.
- A compromise of root on the proxy host. The auto-updater needs root to manage the install layout; once root is held, the entire host is the attacker's.

---

## 2. Communication security

### 2.1 Mode 1: outbound (proxy as HTTPS server)

The proxy accepts HTTPS+mTLS connections from ToolManager.

- **TLS 1.3 minimum** is enforced on the listener (`internal/tlsutil`). No SSL/early-TLS support, no protocol downgrade.
- **mTLS is mandatory.** The listener will not start unless `tls.ca_cert`, `tls.server_cert`, and `tls.server_key` are all configured and the files are readable. There is **no fallback to plain HTTP**: at server startup, `requireOutboundTLS()` (`cmd/server/main.go`) refuses to bind a listener with a fatal error message that distinguishes "not enrolled" from "broken config" so the operator sees the right remediation. This is the explicit mitigation for [CWE-319](#appendix-a-cwe-mapping) (cleartext transmission).
- **Allowed-clients allowlist.** Even after a successful TLS handshake, the proxy verifies the client certificate's CN/DNS against `tls.allowed_clients`. A rogue client signed by the same CA but with the wrong identity is rejected. The list must be **non-empty**: the proxy refuses to start in outbound mode with an empty `allowed_clients`, because `RequireAndVerifyClientCert` alone would accept *any* certificate chaining to the shared CA (fail-closed — no silent "accept any CA-signed client").
- **MCP transport.** Server-Sent Events (`/mcp/sse`) for the response stream and POST (`/mcp/message`) for requests. Both endpoints share the mTLS protection.

### 2.2 Mode 2: inbound (proxy as WebSocket client)

For strict-egress networks where ToolManager cannot reach the proxy directly, the proxy dials out and ToolManager terminates the connection on its side.

- **TLS 1.3 minimum** is enforced on the client side (`internal/reverseconnect`).
- **Mutual authentication.** The proxy presents its client certificate; ToolManager's server certificate is verified against the embedded CA cert.
- **WSS only.** The dialer rejects URLs that are not `wss://`.
- **Early validation.** The dialer's first action on each reconnect is a non-empty check on the TLS material so an unenrolled proxy logs a clear `"missing TLS material — proxy is not enrolled"` rather than a low-level filesystem error.
- **Reconnect with exponential backoff.** Capped at 60 s. No information leakage about the cause of the failure other than to the local log file.

### 2.3 Health endpoint

A `/health` endpoint is exposed for liveness probing.

- In **outbound mode** it shares the mTLS-protected listener — health checks must speak mTLS.
- In **inbound mode** there is no external listener; a localhost-only HTTP `/health` is bound on `127.0.0.1:<port>` for local diagnostics. Plain HTTP on loopback is acceptable here because the interface is not network-reachable; this is a deliberate non-downgrade.

### 2.4 Proxy → Tachyonik connections at enrollment time

Enrollment uses standard HTTPS with full chain verification by default. The `--insecure` flag exists for development against self-signed Tachyonik instances and is documented as a development-only override. It is not used by production deployments and is never used for any subsequent connection — once enrollment is complete, all traffic is mTLS-authenticated.

---

## 3. Verification of code and data received from ToolManager

The proxy accepts three classes of input from ToolManager over the MCP control channel: tool-execution requests (`tools/call`), tool-detection routines (`tools/scan`), and configuration updates (`config/update`). Each has its own validation pipeline.

### 3.1 Tool-detection routines (`tools/scan`)

ToolManager pushes JavaScript routines that the proxy executes locally to detect installed security tools. This is the most unusual code path in the system — actual code crosses the trust boundary at runtime.

The trust anchor for this channel is **the mTLS-authenticated session itself**. The proxy trusts ToolManager to curate the routine library; the SHA-256 transmitted alongside each routine is a transport-integrity check, not a separate cryptographic authenticity check. (This is in contrast to auto-update, where the trust anchor is the offline Ed25519 key — see §7.)

Each routine carries a `{name, code, version, sha256}` record. Validation, in order:

1. **SHA-256 of `code` is recomputed** and compared to the declared digest. A mismatch produces a per-routine error result and aborts execution of that routine. This guards against accidental on-the-wire corruption and against any intermediary that could mutate `code` while leaving `sha256` unchanged.
2. **Execution in a sandboxed JavaScript VM.** The proxy uses `goja` (an embedded ECMAScript interpreter; not Node and not V8). The VM is freshly created per routine — there is no shared global state across routines and no surface for routine-A to affect routine-B's execution.
3. **Restricted host helper surface.** The VM has exactly two host helpers exposed:
   - `which(name)` — wraps `exec.LookPath`. Returns the absolute path of `name` on `$PATH`, or the empty string. Read-only.
   - `exec(cmd, args)` — wraps `exec.Command`. Returns `{output, exitCode}`. Bounded by a 10 s context deadline and a 1 MiB combined-stdout-stderr cap, so a hostile routine cannot wedge the scanner with `exec("dd","if=/dev/zero",...)` or exhaust memory.

   Notably absent: `require`, `import`, file I/O, network I/O, environment-variable mutation, process control. A routine cannot read the proxy's TLS keys, write to disk, open sockets, or escape the goja VM via prototype pollution (the host helpers are bound on a fresh VM each call and do not return objects with reachable references back to the Go runtime).
4. **Per-routine execution deadline.** The interpreter runs under a 30 s wall-clock timeout. When the timer fires, `goja.Interrupt()` raises an exception inside the VM, which lands at the next interpreter dispatch step — even from a tight `while(true)` loop. Without this, a hostile or buggy routine would hang the scanner goroutine and (because the scanner-wide mutex is held for the full batch) block every subsequent `tools/scan` call.
5. **No captured output in the response by default.** The `tools/scan` response only carries the `{detected, version, path, description, error}` fields the routine returned via its `detect()` function. Routines cannot smuggle arbitrary bytes back up the wire by stuffing them into command output unless `detect()` deliberately returns them.

The `exec()` helper is the only side-effecting capability the JS sandbox has. A routine can therefore probe arbitrary local commands, which is exactly what tool detection requires (`nmap --version`, `nikto -Version`, etc.). The risk surface is bounded by the trust placed in the routine library curated by ToolManager: a malicious routine could exfiltrate command output via the `description` field, run arbitrary commands as the proxy user, or fingerprint the host. This is acceptable because the same ToolManager already drives `tools/call` (§3.2) which has equivalent power. **The JS sandbox does not raise the trust threshold**; it only prevents the routine from accessing capabilities ToolManager doesn't already have.

This corresponds to [CWE-94](#appendix-a-cwe-mapping) (improper control of generation of code) and [CWE-829](#appendix-a-cwe-mapping) (inclusion of functionality from untrusted control sphere). The mitigation is the combination of mTLS for authenticity, SHA-256 for integrity, and the goja sandbox for capability minimisation.

### 3.2 Tool execution requests (`tools/call`)

ToolManager sends tool-execution requests that the proxy translates into a local subprocess invocation. The translation pipeline is the primary defence against argument injection.

The proxy maintains a registry of *tool definitions* — these are not provided per-call. They come from either the local `config.yaml` or a `config/update` push (§3.3). A `tools/call` arrives with `{name, arguments}`, where `name` must already exist in the registry; the request itself cannot define a new tool.

Per-call validation, in order:

1. **Name lookup.** Unknown tool names return `-32601 Method not found` (technically a `tools/call` returns an `isError: true` content block); no execution occurs.
2. **JSON-schema validation.** Arguments are validated against the tool's `args_schema`. Type mismatches and missing required fields are rejected with no execution. A value constrained by an `enum` must be one of the declared values — so an `enum` field such as nmap's `scan_type` (whose allowed values are themselves flags like `-sV`) cannot be used to smuggle an arbitrary flag.
3. **Allowed-character allowlist.** Each argument is matched against the regex `^[<allowed_chars>]*$` from the tool's config. The allowlist is character-class syntax (`a-zA-Z0-9.:/\\-_, `, etc.). An argument containing any character outside the allowlist is rejected with `argument %q contains disallowed characters`. This mitigates [CWE-77](#appendix-a-cwe-mapping) / [CWE-78](#appendix-a-cwe-mapping) (command injection).
   - **Flag-injection guard.** The allowlist alone is necessary but not sufficient: the shipped allowlists permit spaces and dashes, and because the rendered template is re-tokenised on whitespace (step 4), a value like `10.0.0.1 --script=evil` would otherwise expand into extra argv flags the tool author never intended — argument injection (CWE-88) even though no shell is involved. So every whitespace-separated field of an argument value must additionally **not begin with `-`**. Values constrained by an `enum` (step 2) are exempt because they are author-approved. This runs regardless of whether `allowed_chars` is set.
4. **Template rendering.** The argv is rendered from `arg_template` using Go's `text/template`. The template substitutes argument values into a *list of argv tokens*; there is no shell. The proxy **never** invokes `/bin/sh -c` or any other shell, on any code path. (An earlier release applied resource limits via a `/bin/sh -c "ulimit ... && cmd ..."` wrapper; that path was removed and replaced with `prlimit(2)` against the child PID, see §4.)
5. **Subprocess execution under resource limits.** `os/exec` runs the command directly with the rendered argv, in a **sanitized environment** — a small allowlist of non-secret variables (PATH always present), never the proxy's full `os.Environ()` — so executed tools cannot read enrollment material or other secrets from the environment. The child is placed in its own **process group**, and on timeout the whole group is SIGKILLed, so detached grandchildren cannot survive a cancelled call. Limits applied:
   - `timeout` — context deadline; default 120 s.
   - `max_output_bytes` — combined stdout+stderr; default 10 MiB. Output beyond the cap is discarded as it streams (a bounded buffer), so the cap bounds proxy memory, not merely the returned string.
   - `max_cpu_seconds`, `max_memory_mb` — `prlimit(2)` (`RLIMIT_CPU` / `RLIMIT_AS`) against the child PID after `cmd.Start()` on Linux.
   - `max_processes`, `max_file_size_mb` — optional `prlimit(2)` (`RLIMIT_NPROC` / `RLIMIT_FSIZE`) fork-bomb / disk-fill guards; `0` = unlimited. `RLIMIT_NPROC` is per real UID, so size it with headroom for the daemon itself.
   - macOS / Windows: rlimits are not enforced; a one-time warning is logged at first use.
6. **`output_files` config-shape validation.** `arg_flag` and `extension` (the only two `output_files` fields that are concatenated into argv) are validated against tight regexes (`^-{1,2}[A-Za-z0-9][A-Za-z0-9-]{0,15}$` and `^\.[A-Za-z0-9]{1,16}$`). A `config/update` push that smuggled whitespace, semicolons, or other metacharacters into either field is rejected at use time.
7. **Output-file paths.** Temp files are created with `os.CreateTemp` (entropy via the OS, plus `O_EXCL`), not by hand-rolling a path from `crypto/rand`. This eliminates both the predictable-path-on-rand-failure footgun and the symlink-race window between path generation and the tool opening the file.
6. **Exit-code allowlist.** Tools may legitimately exit non-zero (e.g., nmap's `1` for "no hosts up"). `allowed_exit_codes` lists the codes considered "successful" for surfacing as `IsError: false`.
7. **Output-file capture.** When the tool produces structured output (e.g., nmap's `-oX`), the proxy creates a temp file, substitutes its path into the template, and returns the contents as a base64 `resource` content block alongside stdout. The temp file is created with `os.CreateTemp` (mkstemp semantics) under the proxy's TmpDir.

The combination of #3, #4, and #5 closes the classic argv-injection paths: no shell, every argument is allowlisted, and the command name is fixed in the tool definition (callers cannot supply an arbitrary `cmd`).

### 3.3 Configuration updates (`config/update`)

ToolManager can push tool definitions and upstream MCP server entries via `config/update`. This is the highest-privilege MCP method in the API.

- **Disabled by default.** The proxy refuses `config/update` unless `allow_remote_config: true` is set in `config.yaml`. The default is `false`. An operator must opt in; this is a deliberate friction point.
- **Validation floor.** Even when enabled, a pushed tool is rejected (the whole update fails) if it has an empty `name`, an empty `command`, or an empty `allowed_chars`. A remote push therefore cannot register a tool that disables argument validation.
- **Atomic persistence.** When accepted, the new configuration is written via `config.SaveConfig`, which marshals to YAML and writes the file. The in-memory tool registry is updated under a write lock.
- **No code execution in this step.** The pushed config is a description of how to invoke local tools; it does not contain executable bytes. The validation pipeline of §3.2 still applies on every subsequent `tools/call`.

The `tools` and `mcp_servers` fields are the only config fields that `config/update` can modify. It cannot, for example, change `tls.allowed_clients` or `auto_update.manifest_url` — those would require local file edits.

### 3.4 Other MCP methods

The dispatcher in `internal/mcpserver` handles a closed set of methods: `initialize`, `notifications/initialized`, `tools/list`, `tools/call`, `tools/scan`, `config/get`, `config/update`, `ping`. Anything else returns `-32601 Method not found`. There is no generic "execute arbitrary command" or "read arbitrary file" method.

---

## 4. Tool execution sandboxing

The validation pipeline in §3.2 is the inner defence; this section covers the outer defence against a misbehaving (or hostile-but-trusted) tool binary.

- **Process isolation by OS.** Tools run as the same user the proxy runs as (typically the unprivileged `tachyonikproxy` system user, see §8) — not root. A tool that escapes its expected behaviour is bounded by that user's permissions.
- **No shell.** The argv passed to `os/exec` is a `[]string` slice. The OS exec syscall takes the literal argv, with no shell expansion. A tool argument like `; rm -rf /` is passed *as a single argument string* to the binary; the binary itself decides what to do with it. This shifts the threat from "the proxy's argv parser is wrong" to "the tool's argument parser is wrong" — which is the latter's responsibility, not the proxy's.
- **Resource ceilings.** CPU and memory caps via `prlimit(2)` applied to the child PID after `cmd.Start()` on Linux (no shell wrapper, no parent-process side effects). Output-byte caps applied as the streaming reader sees more bytes; once the cap is hit the process is killed. Timeouts via `context.WithTimeout`. On macOS / Windows the per-tool memory and CPU rlimits are not enforced (no equivalent of `prlimit(2)`); operators on those platforms should rely on launchd / Job Object resource controls or systemd-style limits at the service level. These mitigate [CWE-400](#appendix-a-cwe-mapping) and [CWE-770](#appendix-a-cwe-mapping).
- **No automatic cleanup of long-lived state.** The proxy does not maintain server-side state between calls. Each `tools/call` is independent; a previous call cannot influence the next via shared in-memory state.

---

## 5. Configuration trust

The on-disk configuration is the source of truth for everything that gates trust: TLS material, the manifest URL, embedded-key references, the `allow_remote_config` flag.

- **File location.** System installs use `/etc/tachyonik/tachyonikproxy/config.yaml` (mode `0640`, owned by the `tachyonikproxy` user and group). User-space installs use `~/.config/tachyonik/tachyonikproxy/config.yaml`.
- **Cert files location.** Written by the `enroll` flow into `<config-dir>/certs/`. Files are written via an atomic temp-then-rename pattern.
- **No environment-variable injection of secrets.** The proxy reads `TACHYONIKPROXY_CONFIG`, `TACHYONIKPROXY_PORT`, `TACHYONIKPROXY_HOST`, `TACHYONIKPROXY_LOG_LEVEL`. None of these can override TLS material or the auto-update manifest URL. Secrets only come from the config file.
- **Embedded public keys.** The auto-update Ed25519 keys are embedded in the binary via `go:embed pubkeys/*.pem` and are not configurable at runtime. An attacker who can edit `config.yaml` cannot redirect auto-update to keys they control.

---

## 6. Enrollment

Enrollment is the one-time trust establishment that produces a working mTLS configuration. Two flows are supported.

### 6.1 Online enrollment

```
tachyonikproxy enroll "https://tachyonik.example.com/enroll?token=<one-time-token>"
```

- **One-time tokens.** The token is consumed on first use; replay returns an error.
- **HTTPS required.** The proxy refuses non-HTTPS enrollment URLs unless `--insecure` is supplied (development-only).
- **Existing-enrollment guard.** If `tls.*` is already populated, the proxy prompts for confirmation before re-enrolling. `--force` skips the prompt; `reset-enrollment` is the explicit clear path.
- **Cert material persistence.** Atomic write (temp + fsync + rename) for each cert file. A crash mid-write leaves either the old file or the new file, never a half-written one.

### 6.2 Reverse (`--listen`) enrollment

For strict-egress networks. The proxy listens; ToolManager dials in.

- **Bootstrap certificate.** A self-signed cert is generated in-memory with a 15-minute validity window — no long-lived bootstrap key on disk.
- **Pairing code.** A 6-digit code is printed to the operator's terminal and shown alongside the `helloRequest`. The operator types it into the Tachyonik WebUI; ToolManager then proves possession of the code in its `helloRequest`. The pairing code's hash (not the plaintext) lives in a server-side ticket, with constant-time comparison (`crypto/subtle`).
- **Listen window.** `listenWindow = 10 * time.Minute`. After this, the listener tears down and the proxy must be re-armed.
- **Per-IP rate limiting.** `perIPAttemptLimit = 5` failed attempts within a `perIPCooldown = 60s` window blocks further attempts from that IP for the rest of the listen window.
- **Global cap.** `globalAttemptLimit = 15` failed attempts across all IPs ends the listen window.
- **CSR-based key issuance.** The proxy generates an ephemeral RSA-4096 key, sends a CSR; ToolManager (via ResourceManager) signs it. The signed cert is the only artifact that survives the listen window — the bootstrap key and cert are discarded.

These rate limits and the short windows mitigate brute-force attacks against the pairing code (which is small enough — ~1M space — that defence-in-depth matters here).

### 6.3 Resetting

`tachyonikproxy reset-enrollment` removes all on-disk cert material and clears the relevant config sections. Requires the operator to type `DELETE` to confirm (or `--force` to skip the prompt). After reset, the proxy refuses to start in outbound mode (per §2.1) until re-enrolled.

---

## 7. Auto-update trust chain

This channel is independent of the MCP/mTLS channel by design. A compromise of ToolManager does not enable an attacker to push a malicious binary.

### 7.1 Trust anchor: embedded Ed25519 public keys

- **Multiple keys supported.** Every PEM file under `internal/selfupdate/pubkeys/` is embedded into the binary via `go:embed`. The verifier accepts a signature from any of them. This enables zero-downtime rotation: a release ships with both old and new keys; the next manifest is signed with both; once every proxy has updated, a subsequent release drops the old key.
- **Offline private keys.** Production private keys are held offline (HSM or air-gapped). The build host and CDN never see the private key.
- **Public-key parsing fails closed.** A corrupt PEM aborts startup of the auto-update path; we do not fall back to "no keys, allow anything".

### 7.2 Validation order on every check

Order matters — each step gates the next. A failure at any step ends the check; nothing is downloaded or applied.

1. **HTTPS required.** Manifest URL must be `https://`. Plain HTTP is refused. Mitigates [CWE-319](#appendix-a-cwe-mapping).
2. **Manifest fetched, then signature fetched.** Both with a 1 MiB body cap to bound resource consumption. Mitigates [CWE-770](#appendix-a-cwe-mapping).
3. **Signature verified BEFORE parsing.** `VerifyManifest` operates on raw bytes against any embedded public key. The JSON parser does not see untrusted input. This is a deliberate ordering: a parser-side bug (e.g., a future Go stdlib JSON CVE) cannot be triggered by a forged manifest. Mitigates [CWE-345](#appendix-a-cwe-mapping) and [CWE-502](#appendix-a-cwe-mapping).
4. **Schema-version match.** A manifest with a `schemaVersion` the proxy does not recognise is refused. The proxy does not "best-effort" parse a future format.
5. **Channel match.** Manifest's `channel` must equal `auto_update.channel`. Proxies on different channels do not converge.
6. **Strict version monotonicity.** `manifest.LatestVersion` must be greater than the running proxy's version. Equal-or-lower is refused as a downgrade attack. Mitigates [CWE-346](#appendix-a-cwe-mapping).
7. **`publishedAt` replay protection.** Must be strictly newer than the timestamp of the manifest that produced the currently installed version (read from the local state file). A signed-but-stale manifest is refused.
8. **Per-platform artifact existence.** The artifact entry for `<goos>/<goarch>` must be present and have a non-empty SHA-256.

### 7.3 Artifact verification and extraction

1. **HTTPS-only artifact URL.** Same enforcement as the manifest.
2. **Streamed SHA-256 verification AFTER full write.** The downloaded bytes are written to disk first, then hashed. A streaming-as-we-go check is rejected because torn reads make recovery harder.
3. **Tar extraction with allowlist.** Only files named `tachyonikproxy` are extracted. Path traversal (`..`, absolute paths) is refused with `"rejecting suspicious tar entry"`. Other files in the archive are ignored. Mitigates [CWE-22](#appendix-a-cwe-mapping) and [CWE-409](#appendix-a-cwe-mapping).
4. **Extracted binary mode `0755`.** Not `0777`, not `+s`. Owned by the user that ran the apply (root for system installs).

### 7.4 Atomic apply and automatic rollback

1. **Versioned-symlink layout.** The apply path writes the new binary to `/opt/tachyonik/proxy/<new>/tachyonikproxy`, then atomically swaps `/opt/tachyonik/proxy/current` (`rename(2)` of a symlink onto another symlink).
2. **Health probe with bounded window.** After the service restart, a probe confirms the new binary is responsive (TLS handshake on the configured port for outbound mode; HTTP `/health` on `127.0.0.1` for inbound mode). Window is `auto_update.health_window_seconds` (default 30 s).
3. **Automatic rollback.** A health-probe failure within the window causes the symlink to be flipped back to the previous version and the service restarted. If the rollback also fails health, the state file is moved to `phase: degraded` and the apply path exits non-zero.
4. **Sticky rolled-back list.** A version that was rolled back is recorded in `update-state.json::rolledBack[]` and never auto-applied again, even if the manifest still points at it. Operators can clear the list with `--reset-rollback-record`.

### 7.5 State-machine durability

Every transition is written to `update-state.json` *before* the action it represents (download, stage, swap, restart, verify, rollback). A crash between two transitions leaves the state file at the last-attempted phase, so the next run can detect partial state. The state file is itself written atomically (temp + fsync + rename).

### 7.6 What auto-update can NOT be made to do

- Apply a non-Tachyonik-signed binary.
- Apply a binary published by Tachyonik on a different channel.
- Downgrade.
- Replay an older signed manifest.
- Apply over an unenrolled proxy (the apply path skips on missing TLS material).
- Run two apply paths concurrently (file-lock serialised).

---

## 8. Privilege and process model

### 8.1 Daemon process

- **System install.** Runs as the unprivileged `tachyonikproxy` system user (created by the package's `postinstall.sh`). Binary path `/usr/bin/tachyonikproxy`. The systemd unit applies extensive hardening:
  - `NoNewPrivileges=true` — `setuid`-style privilege gain by an exec'd binary is blocked.
  - `ProtectSystem=strict` — read-only `/`, with explicit exceptions.
  - `ProtectHome=true` — no access to `/home`, `/root`.
  - `PrivateTmp=true` — process-private `/tmp` and `/var/tmp`.
  - `ReadWritePaths=/var/log/tachyonik /etc/tachyonik/tachyonikproxy` — only the directories the daemon legitimately writes.
  - `LimitNOFILE=65536` — bound on file descriptors.
- **macOS install.** `LaunchDaemon` runs the proxy. The OS does not have systemd's hardening primitives; the daemon runs unprivileged via `launchd`'s `User` key (set by the install script).
- **User-space install.** Runs as the operator's account. No system-wide service supervisor; the operator is responsible for restart-on-boot. This mode is intentionally less secure (and less convenient) and is documented as a fallback for hosts where the operator lacks sudo.

### 8.2 Auto-update process

- The auto-update path runs as a one-shot subcommand (`tachyonikproxy self-update`), not as part of the long-running daemon. This isolation means a bug in the updater cannot crash the running proxy.
- Triggered by a systemd timer (`tachyonikproxy-update.timer`) on Linux or a `LaunchDaemon` (`com.tachyonik.tachyonikproxy-update`) on macOS.
- **Runs as root.** The updater needs root to write under `/opt/tachyonik/proxy/`, manage the CLI symlink at `/usr/bin/`, write the state file under `/var/lib/`, and call `systemctl restart`. Tighter capability sets are achievable but unnecessary given the updater is a small one-shot program with no shell, no interactive input, and exactly one outbound network destination (the manifest URL).
- **`Nice=10` and `IOSchedulingClass=idle`.** The updater never preempts the proxy itself for CPU or disk.
- **`PrivateTmp=true`** to keep download/extract artifacts out of the shared `/tmp`.

### 8.3 Filesystem layout

```
/usr/bin/tachyonikproxy                       (binary; symlink after layout bootstrap)
/etc/tachyonik/tachyonikproxy/config.yaml     (mode 0640, root:tachyonikproxy)
/etc/tachyonik/tachyonikproxy/certs/          (mode 0700, owned by daemon user)
/var/log/tachyonik/                           (logs, mode 0755, owned by daemon user)
/var/lib/tachyonik/proxy/update-state.json    (auto-update state, root-owned)
/var/lib/tachyonik/proxy/update.lock          (auto-update flock, root-owned)
/opt/tachyonik/proxy/<version>/tachyonikproxy (auto-update versioned binaries)
/opt/tachyonik/proxy/current → <version>/     (auto-update active symlink)
```

Cert files are written `0600` and the `certs/` directory is `0700` so an unprivileged local user cannot read the proxy's mTLS private key. This addresses [CWE-732](#appendix-a-cwe-mapping).

---

## 9. Concurrency and atomicity

- **Auto-update lock.** A `flock`-based exclusive lock on `<state-dir>/update.lock` prevents two `self-update` invocations from racing. A second invocation exits immediately with a friendly message rather than corrupting state.
- **Atomic file replacements.** State file, config file, cert files: temp + fsync + rename. The standard POSIX guarantee — a crash leaves either the old or the new file, never a half-written one.
- **Atomic symlink swap.** Auto-update apply uses temp-symlink + rename to flip `current`. Single-step on POSIX.
- **Per-routine fresh JS VM.** No shared mutable state across `tools/scan` routine executions.
- **Per-call subprocess isolation.** No shared mutable state across `tools/call` executions in the proxy.

---

## 10. Logging and observability

- **No automatic log shipping.** Logs are local. The proxy does not telemeter execution data, command arguments, or tool outputs to the platform without an explicit operator action (`tools/call` returns are sent over the existing mTLS channel as a direct response to ToolManager's request, not as background telemetry).
- **Log levels.** `DEBUG`, `INFO`, `WARN`, `ERROR`. `DEBUG` includes per-request method names but NOT argument values. Tool argument values are recorded only when explicitly debugging a misbehaving tool, never at `INFO` or higher.
- **Auto-update startup line.** A single `Auto-update: …` INFO line at daemon startup summarises the most recent update outcome — captured by journald, useful for forensics.
- **No secrets in logs.** TLS keys, tokens, and credentials are not logged at any level. The `--insecure` flag for online enrollment logs a `Warning: TLS certificate verification disabled` line so its use is auditable.

---

## 11. Out of scope / customer responsibilities

The proxy's security depends on a healthy host environment. Customers are responsible for:

- **Host hardening.** Patching the OS, configuring host-based firewalls, restricting network egress, monitoring for unexpected processes.
- **Filesystem permissions.** The package installer sets sensible defaults; operators who hand-edit `/etc/tachyonik/tachyonikproxy/` are responsible for not weakening them.
- **Local user controls.** A local attacker with the `tachyonikproxy` user's credentials has all the privileges the proxy has. Treat the daemon user as a privileged service account.
- **Tool binary trust.** The proxy executes locally installed binaries. Customers are responsible for the integrity and provenance of those binaries (`nmap`, `nikto`, etc.).
- **Out-of-band signing-key rotation.** When Tachyonik publishes a new auto-update release that embeds a new signing key, customers should ensure their proxies pick up that release within a reasonable window, otherwise they will be left on the old key when a future release drops it.

The proxy is a defensive security tool. It is **not** in scope to defend against:

- A compromise of root on the proxy host.
- A malicious tool binary deliberately installed on the host.
- A network attacker capable of producing valid Ed25519 signatures (i.e., a signing-key compromise — see §1).
- Side-channel attacks against the tools the proxy invokes (timing, cache, etc.). These are properties of the tools themselves.

---

## 12. Reporting security issues

Please **do not** open public GitHub issues for security reports. Email `security@tachyonik.io` with reproduction steps. PGP details are published at `https://tachyonik.io/security`.

We commit to:

- Acknowledging receipt within two business days.
- Providing an initial assessment within ten business days.
- Coordinating a disclosure window with the reporter before publication.

---

## Appendix A: CWE mapping

The CWE references below are not exhaustive; they call out the weaknesses that this proxy's design directly addresses, and where in the codebase the mitigation lives.

| CWE     | Title                                                                | Mitigation in TachyonikProxy                                                                                                                |
|---------|----------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------|
| **CWE-22**   | Improper Limitation of a Pathname to a Restricted Directory ("Path Traversal") | `internal/selfupdate/download.go: ExtractTarGz` rejects entries with `..` or absolute paths; only files named `tachyonikproxy` are extracted. |
| **CWE-77**   | Command Injection                                                    | `internal/tools/executor.go`: tool arguments validated against `args_schema` and `allowed_chars` allowlist; argv built via `text/template`, never via shell. Resource limits applied via `prlimit(2)` (`internal/tools/rlimit_linux.go`), not a shell wrapper. `output_files.arg_flag` and `extension` validated against tight regexes before being concatenated into argv. |
| **CWE-78**   | OS Command Injection                                                 | Same as CWE-77; `os/exec` invoked with a literal `[]string` argv — no `/bin/sh -c` on any code path. |
| **CWE-94**   | Improper Control of Generation of Code ("Code Injection")            | `tools/scan` JS routines run in a fresh `goja` VM (`internal/toolscan/scanner.go`) with only `which()` and `exec()` host helpers. SHA-256 integrity check on each routine. Per-routine 30 s deadline + per-`exec()` 10 s deadline + 1 MiB output cap to prevent indefinite hangs. |
| **CWE-200**  | Exposure of Sensitive Information to an Unauthorized Actor           | mTLS-only listener with allowed-clients allowlist (§2.1). No automatic telemetry of tool arguments or output (§10). Cert files mode `0600`. |
| **CWE-285**  | Improper Authorization                                               | mTLS authentication on every MCP request; `allowed_clients` allowlist; `config/update` gated by `allow_remote_config`.  |
| **CWE-295**  | Improper Certificate Validation                                      | TLS 1.3 with full chain verification; `--insecure` flag is enrollment-only and clearly documented as development-only. mTLS server verifies client certs against pinned CA. |
| **CWE-300**  | Channel Accessible by Non-Endpoint ("Man-in-the-Middle")             | mTLS on the MCP channel; signed manifests (Ed25519) on the auto-update channel — neither relies on TLS alone. |
| **CWE-319**  | Cleartext Transmission of Sensitive Information                      | Outbound mode refuses to start without TLS material (`requireOutboundTLS` in `cmd/server/main.go`). Inbound mode uses `wss://` only. Manifest URL must be `https://`. No HTTP fallback under any circumstance. |
| **CWE-345**  | Insufficient Verification of Data Authenticity                       | Auto-update: Ed25519 signature on the manifest verified before parsing. SHA-256 verification on the downloaded artifact. Tool-detection routines: SHA-256 integrity check. |
| **CWE-346**  | Origin Validation Error                                              | Auto-update: strict version monotonicity (no downgrade) and `publishedAt` replay protection. |
| **CWE-352**  | Cross-Site Request Forgery                                           | N/A — proxy has no cookie-based session and no browser surface; the only way to talk to the listener is over mTLS with a valid client cert. |
| **CWE-400**  | Uncontrolled Resource Consumption                                    | Tool execution: timeouts, `max_output_bytes`, `prlimit(2)` for memory/CPU on Linux. Manifest fetch: 1 MiB body cap. JS sandbox: per-routine 30 s deadline, per-`exec()` 10 s + 1 MiB cap. Auto-update: serialised via `flock`. |
| **CWE-409**  | Improper Handling of Highly Compressed Data ("Tar Bomb")             | Auto-update tar extraction limits scope to one filename and rejects path-traversal entries; only one file is ever extracted per archive. |
| **CWE-426**  | Untrusted Search Path                                                | Tool definitions specify the command name; `os/exec` is invoked with the result of `LookPath` at registration time, not with a `$PATH`-relative name resolved per-call. The systemd unit sets a clean `PATH`. |
| **CWE-494**  | Download of Code Without Integrity Check                             | Auto-update: signature on manifest, SHA-256 on artifact, version monotonicity, `publishedAt` replay protection — see §7. |
| **CWE-502**  | Deserialization of Untrusted Data                                    | Auto-update manifest signature is verified BEFORE JSON parsing, so a forged manifest never reaches the parser. MCP JSON-RPC parsing happens on data already authenticated by mTLS. |
| **CWE-522**  | Insufficiently Protected Credentials                                 | TLS keys written `0600` in a `0700` directory, owned by the daemon user. Auto-update private key kept offline (Tachyonik responsibility). |
| **CWE-732**  | Incorrect Permission Assignment for Critical Resource                | Cert files `0600`, `certs/` dir `0700` (Chmod-on-open migrates installs from looser previous defaults), config file `0640` (mode preserved across rewrites; defaults to `0640` on first creation), auto-update lock file `0600`. |
| **CWE-770**  | Allocation of Resources Without Limits or Throttling                 | Manifest fetch capped at 1 MiB. Per-IP and global rate limits in `--listen` enrollment. Tool-call resource limits via rlimits. |
| **CWE-798**  | Use of Hard-coded Credentials                                        | No hard-coded secrets. Embedded Ed25519 *public* keys are intentional trust anchors, not credentials, and are public by definition. |
| **CWE-829**  | Inclusion of Functionality from Untrusted Control Sphere             | Tool-detection routines (untrusted JS-on-the-wire) are sandboxed in `goja` with a 2-function host surface. Auto-update binaries are signature-verified. `config/update` is opt-in. |
| **CWE-918**  | Server-Side Request Forgery                                          | The proxy does not accept arbitrary URLs from MCP callers and does not make outbound HTTP calls based on caller-supplied data. The only outbound URLs are the manifest URL (config-pinned) and the enrollment URL (operator-provided once at enrollment). |
| **CWE-1188** | Insecure Default Initialization of Resource                          | `allow_remote_config` defaults to `false`. Outbound mode refuses to run without TLS. Auto-update validates schema/channel/version before applying. |

The list focuses on weaknesses where the proxy's design *materially* reduces the risk. It is not a guarantee of freedom from all CWEs — defence in depth is the goal, not vulnerability-class elimination.
