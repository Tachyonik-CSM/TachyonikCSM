<!--
TachyonikProxy
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Releasing TachyonikProxy

This document describes how to cut a release: build the artifacts, sign the
auto-update manifest, publish to the download space, and handle the
platform-specific upgrade paths.

There are **two upgrade mechanisms**, by platform:

| Platform        | Upgrade path                                   | Trust anchor                          |
|-----------------|------------------------------------------------|---------------------------------------|
| Linux, macOS    | **Self-update** (signed `manifest.json`)       | Embedded Ed25519 public key (PEM)     |
| Windows         | **New `.msi` install** (Windows Installer)     | MSI `UpgradeCode` GUID                |

> Windows is **not** covered by self-update in this release (`ResolveInstallPaths`
> refuses Windows for apply). Windows hosts upgrade by installing a newer `.msi`.

---

## 0. One-time setup (do this once, before the first real release)

### 0.1 Generate the production signing keypair (offline)

The self-update trust chain verifies a detached Ed25519 signature on every
manifest using a **public** key compiled into the binary. The **private** key
signs manifests and must never touch a build host, CI runner, or this repo.

```bash
# On an offline / HSM-backed host:
openssl genpkey -algorithm ed25519 -out tachyonik_priv.pem
openssl pkey -in tachyonik_priv.pem -pubout -out tachyonik_prod.pem
```

- Move `tachyonik_priv.pem` to long-term offline storage (HSM, hardware token,
  air-gapped vault). Losing it means you can no longer publish updates that
  existing proxies will accept; leaking it means an attacker can.
- `tachyonik_prod.pem` is the PKIX `PUBLIC KEY` PEM the binary embeds.

### 0.2 Embed the production public key

The production public key is embedded at
`internal/selfupdate/pubkeys/tachyonik-prod-2026.pem`, and the old discarded-key
placeholder has been removed — so this step is **already done** for the current
key. Confirm what the build will embed:

```bash
ls internal/selfupdate/pubkeys/*.pem
# tachyonik-prod-2026.pem   ← the production public key (matches the §0.1 private key)
```

Every `*.pem` under `internal/selfupdate/pubkeys/` is embedded via `go:embed`,
and the proxy accepts a signature from **any** embedded key (this is what makes
rotation possible — see §6). **Rebuild after changing keys.**

To install or replace the key in a future setup: drop the new public PEM into
this directory, `git rm` any retired key you no longer sign with, and rebuild.
The matching private key must already be in offline storage (see §0.1) — never
commit it.

### 0.3 Windows `UpgradeCode` GUID

The permanent `UpgradeCode` is set in `packaging/windows/wix.json`
(`0a54c21e-fa2a-4bc0-a906-25af9b18717c`), replacing the old placeholder — so
this step is **already done**. The Windows Installer uses this GUID to recognise
that a new `.msi` upgrades the same product.

> **Do not ever change it.** If the `UpgradeCode` changes, Windows stops
> treating new builds as upgrades and installs them side-by-side instead. It is
> fixed for the lifetime of the product. This GUID is unrelated to the signing
> PEM — different platform, different layer.

Only generate a new one if you are deliberately starting a *separate* product
line (`uuidgen`, or PowerShell `[guid]::NewGuid()`).

---

## 1. Versioning scheme

Versions are **purely numeric dotted-decimal**: `MAJOR.MINOR.PATCH`, e.g.
`1.4.0`. This is a hard constraint of the self-update comparator
(`parseVersion` rejects any non-numeric component):

- ✅ `1.4.0`, `0.7.0`, `1.10.2`
- ❌ `v1.4.0` (the `v` prefix fails), `1.4.0-rc1`, `1.4.0-dirty`, `2026.05`

The running binary's version is embedded from `git describe` via ldflags, so:

- **Tag releases without a `v` prefix** (e.g. `git tag 1.4.0`), and
- **Build the release on the exact tag commit** so `git describe` yields a clean
  `1.4.0` (an off-tag build produces `1.4.0-5-gabc1234`, which would break the
  self-update version compare).

The manifest's `latestVersion` must be **strictly greater** than what's deployed
(downgrades are refused), and `publishedAt` must be **strictly newer** than the
previous manifest (replay is refused).

---

## 2. Build prerequisites

- **Go** — build with a **patched toolchain** (currently **≥ go1.24.13**, or the
  latest patch release). Older toolchains ship known standard-library
  vulnerabilities; `govulncheck ./...` should be clean before release.
- **GNU Make**
- Packaging tools (only for the targets you build):
  - `nfpm` → `.deb` / `.rpm` — `go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.1`
  - `pkgbuild` (Xcode CLT, macOS host) → `.pkg`
  - `go-msi` + WiX (Windows host) → `.msi`
  - `tar`, `zip` → portable archives

---

## 3. Build and package

```bash
cd TachyonikProxy

# Cross-compile all platforms (CGO disabled, static)
make build-all

# Produce the artifacts the manifest will reference (the .tar.gz archives):
make package-archives        # linux/macOS .tar.gz + windows .zip

# OS-native installers (run on the relevant host):
make package-deb             # .deb  (amd64, arm64)
make package-rpm             # .rpm  (amd64, arm64)
make package-macos           # .pkg  (macOS host)
make package-windows         # .msi  (Windows host, go-msi + WiX)
# or:  make package-all
```

Outputs land in `dist/`, e.g.:

```
dist/tachyonikproxy-1.4.0-linux-amd64.tar.gz      ← referenced by manifest
dist/tachyonikproxy-1.4.0-linux-arm64.tar.gz      ← referenced by manifest
dist/tachyonikproxy-1.4.0-darwin-amd64.tar.gz     ← referenced by manifest
dist/tachyonikproxy-1.4.0-darwin-arm64.tar.gz     ← referenced by manifest
dist/tachyonikproxy-1.4.0-windows-amd64.zip       ← Windows portable (no self-update)
dist/tachyonikproxy-1.4.0-*.deb / *.rpm / *.pkg / *.msi
```

> The self-update artifact **must** be the `.tar.gz` archive: the updater
> downloads it, gunzips it, and extracts the single regular file whose basename
> is exactly `tachyonikproxy`. The `make package-archives` tarballs already match
> this layout. **Do not** point a manifest artifact at a `.deb`/`.rpm`/`.zip`.

---

## 4. Compute SHA-256 and assemble the manifest

```bash
cd dist
for f in tachyonikproxy-1.4.0-{linux,darwin}-{amd64,arm64}.tar.gz; do
  printf '%s  %s\n' "$(sha256sum "$f" | awk '{print $1}')" "$f"
  # macOS: shasum -a 256
done
```

Create `manifest.json` (see the full example in §8). Only include
`<goos>/<goarch>` entries you actually ship for self-update — i.e. the four
Linux/macOS targets; **omit `windows/amd64`** (Windows uses the MSI).

---

## 5. Sign the manifest

The signature is the **raw 64-byte Ed25519 signature over `manifest.json`'s exact
bytes** — no JSON wrapping, no canonicalisation. Sign the bytes the CDN will
serve, then never re-serialise them.

```bash
# OpenSSL 3.x (the -rawin flag is required for Ed25519):
openssl pkeyutl -sign  -inkey tachyonik_priv.pem -rawin -in manifest.json -out manifest.json.sig

# Sanity-check BEFORE publishing:
test "$(wc -c < manifest.json.sig)" -eq 64 && echo "sig length OK (64 bytes)"
openssl pkeyutl -verify -pubin -inkey tachyonik_prod.pem -rawin \
        -in manifest.json -sigfile manifest.json.sig && echo "signature verifies"
```

If a byte of `manifest.json` changes after signing, re-sign — the proxy verifies
the signature before it even parses the JSON.

---

## 6. Publish to the download space

Upload to the host/path the proxies point at via `auto_update.manifest_url`
(default `https://tachyonik.com/download/proxy/`). The layout the proxy expects:

```
https://tachyonik.com/download/proxy/
├── manifest.json                                   # the signed manifest
├── manifest.json.sig                               # raw 64-byte Ed25519 signature
├── tachyonikproxy-1.4.0-linux-amd64.tar.gz         # artifacts (urls referenced in manifest)
├── tachyonikproxy-1.4.0-linux-arm64.tar.gz
├── tachyonikproxy-1.4.0-darwin-amd64.tar.gz
├── tachyonikproxy-1.4.0-darwin-arm64.tar.gz
├── tachyonikproxy-1.4.0-windows-amd64.msi          # Windows installer (see §9)
└── … (.deb/.rpm/.pkg, install-tachyonikproxy.sh, etc.)
```

- The manifest and `.sig` are fetched from `manifest_url` and `manifest_url + ".sig"`.
- Each artifact `url` must be `https://` (enforced). Hosting artifacts next to the
  manifest is recommended; there is no host-pinning, but keeping them together
  keeps your TLS/trust surface small.
- **There is nothing to place on the proxy hosts.** Each proxy pulls the manifest
  on its timer, verifies + downloads, and installs under `/opt/tachyonik/proxy/<version>/`
  with an atomic `current` symlink swap. The on-host version directories are
  managed automatically (pruned per `auto_update.keep_versions`).

A proxy applies the update on its next timer fire when **all** hold: signature
verifies, `channel` matches, `latestVersion` > installed, `publishedAt` is newer,
and an artifact exists for its platform. It then restarts and runs a health probe
(`health_window_seconds`); on failure it rolls back automatically and records the
bad version so it is not retried.

---

## 7. Where `manifest_url` is configured

`manifest_url` is the `auto_update.manifest_url` key in the proxy's **config
file** (not an environment variable — only `TACHYONIKPROXY_CONFIG/PORT/HOST/LOG_LEVEL`
are env-overridable):

```yaml
auto_update:
  enabled: true
  channel: stable
  manifest_url: https://tachyonik.com/download/proxy/manifest.json
  health_window_seconds: 30
  keep_versions: 3
```

- **Compiled-in default:** `https://tachyonik.com/download/proxy/manifest.json`
  (`internal/config/config.go`). The shipped `config.yaml.default` has no
  `auto_update` block, so this default applies unless overridden.
- **Config file location** (first that exists wins): `TACHYONIKPROXY_CONFIG` →
  `./config.yaml` → `~/.config/tachyonik/tachyonikproxy/config.yaml` →
  `/etc/tachyonik/tachyonikproxy/config.yaml`. Package installs use the `/etc`
  path. To change the URL (e.g. point a fleet at a staging download space), edit
  that file's `auto_update.manifest_url`.

---

## 8. Example `manifest.json`

```json
{
  "schemaVersion": 1,
  "channel": "stable",
  "latestVersion": "1.4.0",
  "publishedAt": "2026-06-10T10:00:00Z",
  "artifacts": {
    "linux/amd64":  { "url": "https://tachyonik.com/download/proxy/tachyonikproxy-1.4.0-linux-amd64.tar.gz",  "sha256": "<hex-sha256>" },
    "linux/arm64":  { "url": "https://tachyonik.com/download/proxy/tachyonikproxy-1.4.0-linux-arm64.tar.gz",  "sha256": "<hex-sha256>" },
    "darwin/amd64": { "url": "https://tachyonik.com/download/proxy/tachyonikproxy-1.4.0-darwin-amd64.tar.gz", "sha256": "<hex-sha256>" },
    "darwin/arm64": { "url": "https://tachyonik.com/download/proxy/tachyonikproxy-1.4.0-darwin-arm64.tar.gz", "sha256": "<hex-sha256>" }
  }
}
```

Field notes:
- `schemaVersion` — must be `1` (the only version this proxy understands).
- `channel` — must equal each proxy's `auto_update.channel` (default `stable`).
- `latestVersion` — numeric dotted-decimal; strictly greater than deployed.
- `publishedAt` — RFC 3339 UTC; strictly newer than the previous manifest.
- `artifacts` — keyed `"<goos>/<goarch>"`; `url` https + hex `sha256`.

---

## 9. Windows handling (MSI)

Windows upgrades do **not** go through the manifest/self-update path. They use
the Windows Installer:

1. **Build:** `make package-windows` on a Windows host (`go-msi` + WiX), driven
   by `packaging/windows/wix.json`. The MSI `ProductVersion` comes from the build
   version — it must **increase** every release for an upgrade to apply.
2. **`UpgradeCode`:** the permanent GUID from §0.3. Same `UpgradeCode` + a higher
   `ProductVersion` → Windows performs a *major upgrade* (removes the old, installs
   the new) automatically when the customer runs the new `.msi`.
3. **Distribute:** publish `tachyonikproxy-<ver>-windows-amd64.msi` to the download
   space. Customers upgrade by downloading and running it (interactively, or
   silently via `msiexec /i tachyonikproxy-<ver>-windows-amd64.msi /qn`).
4. The Windows `.zip` is a portable build, not an installer, and is not part of
   self-update.

Because Windows proxies don't self-update, plan how customers learn about new
Windows builds (release page, notification, MDM/SCCM push, etc.).

---

## 10. Version pinning and channels

There is no single `pin_version` knob. To hold a host/fleet on a version:

- **Disable self-update** on that install — `auto_update.enabled: false` (and/or
  `systemctl disable --now tachyonikproxy-update.timer`) — and upgrade manually.
- **Pin via a dedicated channel/manifest** — point the install at a `channel`
  (and/or `manifest_url`) whose manifest never advertises past the target
  version. Since the proxy only updates when `latestVersion > installed` **and**
  `channel` matches, a manifest capped at `1.4.0` holds that segment on `1.4.0`
  while `stable` advances others. Maintain e.g. `manifest-pinned-1.4.json`
  (`channel: "pinned-1.4"`, `latestVersion: "1.4.0"`) in your download space and
  set those proxies' `auto_update.channel` / `manifest_url` accordingly.

Pinning means "don't advance past X" — you cannot pin *backward* below the
installed version (downgrades are refused).

---

## 11. Release checklist

- [ ] `govulncheck ./...` clean; built with a patched Go toolchain.
- [ ] Production public PEM embedded in `internal/selfupdate/pubkeys/`; placeholder removed.
- [ ] Tag is numeric (`1.4.0`, no `v`); build is on the exact tag commit.
- [ ] `make build-all && make package-archives` (+ native installers as needed).
- [ ] SHA-256 computed for each `.tar.gz`.
- [ ] `manifest.json` assembled (schemaVersion 1, channel, latestVersion > deployed,
      publishedAt newer, 4 Linux/macOS artifacts).
- [ ] `manifest.json.sig` produced **offline**; length is 64 bytes; verifies against the embedded key.
- [ ] Manifest, `.sig`, and `.tar.gz` artifacts uploaded to `manifest_url`'s directory.
- [ ] Windows `.msi` built with the permanent `UpgradeCode` and an increased `ProductVersion`; uploaded.
- [ ] Smoke test: on a staging proxy, `tachyonikproxy self-update --dry-run` reports the new version and verifies; then a real run applies, restarts, and passes the health probe.
- [ ] Verify outcome via `tachyonikproxy self-update --status` and `journalctl -u tachyonikproxy-update.service`.
