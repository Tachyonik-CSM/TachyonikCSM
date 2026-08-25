<!--
TachyonikProxy
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# install-tachyonikproxy.sh

Version: 1.6.2

Cross-platform installer for TachyonikProxy on Linux and macOS.

Visit https://tachyonik.com to learn how to use the product.
 
## Usage

```sh
curl -fsSL https://tachyonik.com/install-tachyonikproxy.sh | bash
```

## What it does

1. **Detects OS** (Linux/macOS) and **architecture** (amd64/arm64) via `uname`.
2. **Checks for sudo privileges**. If unavailable, offers a user-space install (see below).
3. **Chooses install method** automatically:
   - Debian/Ubuntu: downloads `.deb`, installs with `dpkg -i` (auto-fixes deps).
   - RHEL/Fedora: downloads `.rpm`, installs with `dnf`/`yum`.
   - Other Linux / macOS: downloads `.tar.gz`, manually places binary, config, and service files.
4. **Downloads** from `https://tachyonik.com/download/proxy/tachyonikproxy-latest-{os}-{arch}.{ext}`.
5. **Sets up the service**: systemd on Linux, LaunchDaemon on macOS.
6. **Creates system user** `tachyonikproxy` on Linux (tar.gz path; native packages handle this via postinstall).
7. **Installs uninstaller** at `/usr/local/bin/uninstall-tachyonikproxy`.
8. **Sets up updates — differently per install method.**
   - **tar.gz route** (macOS, and Linux where no package manager is detected): bootstraps the auto-update layout (`/opt/tachyonik/proxy/<version>/` with `current` symlink) and enables the auto-update timer (`tachyonikproxy-update.timer` on Linux, `com.tachyonik.tachyonikproxy-update` LaunchDaemon on macOS), so signed updates are applied automatically with rollback on health failure.
   - **.deb / .rpm route**: neither. Those installs belong to dpkg/rpm — the built-in updater would write over files they track, leaving the package database describing a version that is no longer on disk, and the next package upgrade would undo it. The package ships a marker at `/usr/share/tachyonik/tachyonikproxy/package-managed`; `tachyonikproxy self-update` sees it, explains the situation and exits without changing anything. **Re-running this installer is the upgrade path** — it fetches the current `…-latest-…` package.
9. **Prints next steps** covering both enrollment modes (online and reverse) — or, on an upgrade where the proxy is already enrolled, prints a service-restart hint instead and asks whether the operator wants to re-enroll anyway (default: no). The success banner also reports the version that was placed on disk: `Installed version: X.Y.Z` on a fresh install, or `Upgraded from A.B.C to X.Y.Z` on an upgrade (`unknown` in place of the prior version when the existing binary refused to report it).

## Upgrades

Re-running the script on a host that already has `tachyonikproxy` is treated as an upgrade:

- The binary is replaced via the same install path that produced the original install (`.deb` / `.rpm` / `.tar.gz`).
- The active configuration at `/etc/tachyonik/tachyonikproxy/config.yaml` (system) or `~/.config/tachyonik/tachyonikproxy/config.yaml` (user-space) is **preserved** — every install path uses an `[ ! -f config.yaml ]` guard before copying the default template. The `certs/` subdirectory written by `enroll` is never touched.
- If the script detects an existing enrollment (`<config-dir>/certs/ca.crt` is present), the post-install message:
  - confirms the enrollment is preserved and re-enrollment is **not** required,
  - shows the service-restart command for the platform (a binary swap on a running service does not auto-restart),
  - asks `Re-enroll anyway? [y/N]` and, on yes, prints the enrollment commands without running them.

## Enrollment modes

Once installed, the proxy supports two enrollment paths:

- **Online enrollment** (`tachyonikproxy enroll "<url>"`) — the proxy POSTs to Tachyonik to fetch its TLS material. Requires outbound HTTPS from the proxy host at enrollment time.
- **Reverse enrollment** (`tachyonikproxy enroll --listen`) — for strict-egress networks. The proxy listens and prints a 6-digit pairing code; the admin enters that code in the Tachyonik WebUI, and Tachyonik dials the proxy to complete enrollment. No proxy-originated traffic is required at any point.

Pick one — the installer does not run either; it just puts the binary in place.

## User-space install

When the current user does not have sudo privileges, the script offers two options:

1. Abort and re-run as a user with sudo permissions (recommended for production).
2. Install in user space — no root required.

User-space install paths:

| Item | Path |
|------|------|
| Binary | `~/.local/bin/tachyonikproxy` |
| Config | `~/.config/tachyonik/tachyonikproxy/config.yaml` |
| Logs | `~/.local/share/tachyonik/logs/` |
| Uninstaller | `~/.local/bin/uninstall-tachyonikproxy` |

The config location is part of the binary's auto-detect search order
(see the "Configuration file resolution" section in README.md), so a
plain `tachyonikproxy` finds it without `TACHYONIKPROXY_CONFIG`. The
shipped config template's log path is rewritten to the user log
directory at install time.

User-space limitations:

- No systemd/launchd service is installed — the proxy must be started manually.
- The proxy will not survive reboots unless configured via crontab (`@reboot`) or a user systemd unit.
- Native packages (`.deb`/`.rpm`) are not used; always installs from `.tar.gz`.

## Uninstalling

System-wide install:

```sh
sudo uninstall-tachyonikproxy
```

User-space install:

```sh
~/.local/bin/uninstall-tachyonikproxy
```

The uninstaller:

- Stops and disables the service (systemd or launchd) — system-wide only.
- Removes the package via `dpkg --purge` or `rpm -e` if installed via native package, otherwise removes files manually.
- Removes the auto-update version store and update state (`/opt/tachyonik/proxy` + `/var/lib/tachyonik/proxy` system-wide, `~/.local/share/tachyonik/proxy` user-space) — program data, removed without prompting.
- Asks whether to remove config, certificates, and logs (preserved by default).
- Removes the `tachyonikproxy` system user on Linux — system-wide only.
- Removes the shared `tachyonik` namespace directories (`/etc/tachyonik`, `/opt/tachyonik`, `/var/lib/tachyonik` system-wide; `~/.config/tachyonik`, `~/.local/share/tachyonik` user-space) **only if empty** — data kept by a "no" answer or by other Tachyonik products is never touched.
- Removes itself.

## Requirements

- `curl` or `wget`
- Root privileges for system-wide install (the script uses `sudo` when not running as root)
- No special privileges needed for user-space install

## Download URL convention

The script expects "latest" symlinks on the download server:

```
https://tachyonik.com/download/proxy/tachyonikproxy-latest-linux-amd64.deb
https://tachyonik.com/download/proxy/tachyonikproxy-latest-linux-arm64.deb
https://tachyonik.com/download/proxy/tachyonikproxy-latest-linux-amd64.rpm
https://tachyonik.com/download/proxy/tachyonikproxy-latest-linux-arm64.rpm
https://tachyonik.com/download/proxy/tachyonikproxy-latest-linux-amd64.tar.gz
https://tachyonik.com/download/proxy/tachyonikproxy-latest-linux-arm64.tar.gz
https://tachyonik.com/download/proxy/tachyonikproxy-latest-darwin-amd64.tar.gz
https://tachyonik.com/download/proxy/tachyonikproxy-latest-darwin-arm64.tar.gz
```

If a download fails, the installer prints the exact URL it requested and the
HTTP status (e.g. `Download failed: HTTP 404. URL: …`), so a missing artifact
for a given os/arch is immediately diagnosable. All eight files above must be
published for every supported platform to install — a 404 means the build for
that os/arch was not uploaded to the download server.
