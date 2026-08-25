#!/bin/sh
# install-tachyonikproxy.sh — Cross-platform installer for TachyonikProxy
#
# Usage:
#   curl -fsSL https://tachyonik.com/install-tachyonikproxy.sh | bash
#
# Supports: Linux (amd64, arm64) and macOS (amd64, arm64)
# On Linux, prefers native .deb/.rpm packages when available.
# Falls back to user-space install when sudo is not available.
#
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later

set -eu

# ─── Constants ──────────────────────────────────────────────────────

INSTALLER_VERSION="1.6.3"
DOWNLOAD_BASE="https://tachyonik.com/download/proxy"
BINARY_NAME="tachyonikproxy"

# System-wide paths
SYS_CONFIG_DIR="/etc/tachyonik/tachyonikproxy"
SYS_LOG_DIR="/var/log/tachyonik"
SYSTEMD_UNIT="/lib/systemd/system/tachyonikproxy.service"
LAUNCHD_PLIST="/Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist"
SYS_UNINSTALLER="/usr/local/bin/uninstall-tachyonikproxy"

# User-space paths
USER_BIN_DIR="${HOME}/.local/bin"
USER_CONFIG_DIR="${HOME}/.config/tachyonik/tachyonikproxy"
USER_LOG_DIR="${HOME}/.local/share/tachyonik/logs"
# Auto-update version store (selfupdate.ResolveInstallPaths, user mode):
# per-version binary dirs, the `current` symlink, update-state.json, .staging.
USER_UPDATE_DIR="${HOME}/.local/share/tachyonik/proxy"
USER_UNINSTALLER="${HOME}/.local/bin/uninstall-tachyonikproxy"

# Set by main(): "system" or "user"
INSTALL_MODE=""

# Version of TachyonikProxy detected before this run, if any. Empty when
# no prior install was found; "unknown" when a prior binary exists but
# refused to report its version. Surfaced in upgrade success banners.
PRIOR_VERSION=""

# Version of TachyonikProxy that this run placed on disk. Empty if the
# freshly-installed binary couldn't be queried (in which case the
# version line is omitted from the success banner rather than printing
# a placeholder).
INSTALLED_VERSION=""

# ─── Logging ────────────────────────────────────────────────────────

info()  { printf '\033[1;34m[info]\033[0m  %s\n' "$*"; }
warn()  { printf '\033[1;33m[warn]\033[0m  %s\n' "$*"; }
error() { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

# ─── Helpers ────────────────────────────────────────────────────────

need_cmd() {
    command -v "$1" >/dev/null 2>&1 || error "Required command not found: $1"
}

# Download a URL to a local file. Tries curl, falls back to wget. On any
# failure the URL that was attempted is shown — together with the HTTP status
# when curl is used — so a missing artifact for the current platform surfaces
# as an actionable message instead of a bare "curl: (22) ... 404".
download() {
    _url="$1"
    _dest="$2"
    if command -v curl >/dev/null 2>&1; then
        # Deliberately no -f: let curl finish on a 4xx/5xx so we can report the
        # exact status alongside the URL rather than aborting with curl's terse
        # error and no context about which file was requested.
        _code="$(curl -sSL --retry 3 -o "$_dest" -w '%{http_code}' "$_url" 2>/dev/null)"
        case "$_code" in
            2??) return 0 ;;
        esac
        rm -f "$_dest"
        case "$_code" in
            ''|000)
                error "Download failed: could not reach the server.
        URL: $_url" ;;
            *)
                error "Download failed: HTTP $_code.
        URL: $_url
        The file may not be published for your platform (os/arch). Open the URL
        above to confirm whether it exists." ;;
        esac
    elif command -v wget >/dev/null 2>&1; then
        if ! wget -q -O "$_dest" "$_url"; then
            rm -f "$_dest"
            error "Download failed.
        URL: $_url
        The file may not be published for your platform (os/arch). Open the URL
        above to confirm whether it exists."
        fi
    else
        error "Neither curl nor wget found. Please install one and retry."
    fi
}

# Run a command with sudo if not already root.
maybe_sudo() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    elif command -v sudo >/dev/null 2>&1; then
        sudo "$@"
    else
        error "This installer needs root privileges. Please run as root or install sudo."
    fi
}

# Check whether we can obtain root privileges.
can_elevate() {
    [ "$(id -u)" -eq 0 ] && return 0
    command -v sudo >/dev/null 2>&1 && sudo -n -v >/dev/null 2>&1 && return 0
    return 1
}

# Returns 0 if a proxy enrollment exists at the given config dir. Looks for
# ca.crt under <config-dir>/certs/ — both online enrollment and `enroll --listen`
# write this file via PersistEnrollment, so its presence is a reliable
# enrolled-marker without parsing YAML in shell.
is_proxy_enrolled() {
    _config_dir="$1"
    [ -f "${_config_dir}/certs/ca.crt" ]
}

# Print the bare version token reported by `<binary> version`. The binary
# prints "Tachyonik TachyonikProxy <version>"; we extract the last
# whitespace-separated field. Returns the empty string on any failure
# (binary missing, exec error, version subcommand absent, etc.) so
# callers can branch on `[ -z "..." ]`.
extract_proxy_version() {
    _bin="$1"
    [ -x "$_bin" ] || { command -v "$_bin" >/dev/null 2>&1 || return 0; }
    _out="$("$_bin" version 2>/dev/null || true)"
    [ -n "$_out" ] || return 0
    printf '%s' "$_out" | awk '{print $NF}'
}

# ─── Detection ──────────────────────────────────────────────────────

detect_os() {
    case "$(uname -s)" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        *)       error "Unsupported operating system: $(uname -s). Only Linux and macOS are supported." ;;
    esac
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)   echo "amd64" ;;
        aarch64|arm64)  echo "arm64" ;;
        *)              error "Unsupported architecture: $(uname -m). Only amd64 and arm64 are supported." ;;
    esac
}

# Maps a Go architecture (what detect_arch returns) to the name RPM uses in
# package filenames. Only the two architectures detect_arch accepts can reach
# here, so an unknown value means detect_arch grew a case this did not.
rpm_arch() {
    case "$1" in
        amd64) echo "x86_64" ;;
        arm64) echo "aarch64" ;;
        *)     error "No RPM architecture mapping for '$1'." ;;
    esac
}

# Returns deb, rpm, or tar based on available package managers (Linux only).
detect_package_manager() {
    if command -v dpkg >/dev/null 2>&1 && command -v apt-get >/dev/null 2>&1; then
        echo "deb"
    elif command -v rpm >/dev/null 2>&1 && { command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; }; then
        echo "rpm"
    else
        echo "tar"
    fi
}

# ─── System-wide install methods ───────────────────────────────────

# The published package filenames follow NATIVE packaging conventions, not the
# tarballs' os-arch shape: nfpm emits `tachyonikproxy_<version>_<arch>.deb` and
# `tachyonikproxy-<version>-<release>.<rpmarch>.rpm`, and
# scripts/update-latest-symlinks.sh maintains `latest` links in the same form.
# Asking for `tachyonikproxy-latest-linux-amd64.deb` fetched a name that has
# never existed on the server.

install_native_deb() {
    _arch="$1"
    # Debian convention: name_version_arch, underscore-separated, Go arch names.
    _url="${DOWNLOAD_BASE}/tachyonikproxy_latest_${_arch}.deb"
    _tmp="$(mktemp /tmp/tachyonikproxy-XXXXXX.deb)"

    info "Downloading .deb package for linux/${_arch}..."
    download "$_url" "$_tmp"
    [ -s "$_tmp" ] || error "Download failed or file is empty."

    info "Installing package..."
    maybe_sudo dpkg -i "$_tmp" || {
        warn "dpkg reported issues — attempting to fix dependencies..."
        maybe_sudo apt-get install -f -y
    }
    rm -f "$_tmp"
}

install_native_rpm() {
    _arch="$1"
    # RPM names architectures its own way; detect_arch yields Go's.
    _url="${DOWNLOAD_BASE}/tachyonikproxy-latest.$(rpm_arch "$_arch").rpm"
    _tmp="$(mktemp /tmp/tachyonikproxy-XXXXXX.rpm)"

    info "Downloading .rpm package for linux/${_arch}..."
    download "$_url" "$_tmp"
    [ -s "$_tmp" ] || error "Download failed or file is empty."

    info "Installing package..."
    if command -v dnf >/dev/null 2>&1; then
        maybe_sudo dnf install -y "$_tmp"
    else
        maybe_sudo yum install -y "$_tmp"
    fi
    rm -f "$_tmp"
}

install_from_tar() {
    _os="$1"
    _arch="$2"
    _url="${DOWNLOAD_BASE}/tachyonikproxy-latest-${_os}-${_arch}.tar.gz"
    _tmp="$(mktemp /tmp/tachyonikproxy-XXXXXX.tar.gz)"
    _extract_dir="$(mktemp -d /tmp/tachyonikproxy-extract-XXXXXX)"

    info "Downloading tar.gz archive for ${_os}/${_arch}..."
    download "$_url" "$_tmp"
    [ -s "$_tmp" ] || error "Download failed or file is empty."

    info "Extracting archive..."
    tar -xzf "$_tmp" -C "$_extract_dir"
    rm -f "$_tmp"

    # Find the extracted directory (tachyonikproxy-VERSION/ or just the binary)
    _src_dir="$(find "$_extract_dir" -maxdepth 1 -type d | tail -1)"
    _binary=""
    if [ -f "${_src_dir}/${BINARY_NAME}" ]; then
        _binary="${_src_dir}/${BINARY_NAME}"
    elif [ -f "${_extract_dir}/${BINARY_NAME}" ]; then
        _binary="${_extract_dir}/${BINARY_NAME}"
    else
        # Search recursively
        _binary="$(find "$_extract_dir" -name "$BINARY_NAME" -type f | head -1)"
    fi
    [ -n "$_binary" ] || error "Binary not found in archive."

    _install_dir="/usr/local/bin"

    info "Installing binary to ${_install_dir}/${BINARY_NAME}..."
    maybe_sudo mkdir -p "$_install_dir"
    maybe_sudo cp "$_binary" "${_install_dir}/${BINARY_NAME}"
    maybe_sudo chmod 755 "${_install_dir}/${BINARY_NAME}"

    # Create config directory and install default config
    maybe_sudo mkdir -p "$SYS_CONFIG_DIR"
    maybe_sudo mkdir -p "$SYS_LOG_DIR"
    _default_config=""
    if [ -f "${_src_dir}/config.yaml" ]; then
        _default_config="${_src_dir}/config.yaml"
    elif [ -f "${_src_dir}/config.yaml.default" ]; then
        _default_config="${_src_dir}/config.yaml.default"
    fi
    if [ -n "$_default_config" ] && [ ! -f "${SYS_CONFIG_DIR}/config.yaml" ]; then
        maybe_sudo cp "$_default_config" "${SYS_CONFIG_DIR}/config.yaml"
        info "Default configuration installed at ${SYS_CONFIG_DIR}/config.yaml"
    fi

    # Install service files
    if [ "$_os" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
        if [ -f "${_src_dir}/tachyonikproxy.service" ]; then
            maybe_sudo cp "${_src_dir}/tachyonikproxy.service" "$SYSTEMD_UNIT"
            maybe_sudo systemctl daemon-reload
            maybe_sudo systemctl enable tachyonikproxy.service
            info "systemd service installed and enabled."
        fi

        # Auto-update timer (Phase 3). Tar.gz archive may include the unit
        # files; if not, generate them inline so a tar.gz install gets the
        # same automation as a .deb / .rpm install.
        if [ -f "${_src_dir}/tachyonikproxy-update.service" ]; then
            maybe_sudo cp "${_src_dir}/tachyonikproxy-update.service" "/lib/systemd/system/tachyonikproxy-update.service"
        else
            maybe_sudo tee "/lib/systemd/system/tachyonikproxy-update.service" >/dev/null <<'UPDSVC'
[Unit]
Description=Tachyonik TachyonikProxy auto-update (oneshot)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/tachyonikproxy self-update
Environment=TACHYONIKPROXY_CONFIG=/etc/tachyonik/tachyonikproxy/config.yaml
Nice=10
IOSchedulingClass=idle
User=root
Group=root
NoNewPrivileges=true
ProtectHome=true
PrivateTmp=true
UPDSVC
        fi

        if [ -f "${_src_dir}/tachyonikproxy-update.timer" ]; then
            maybe_sudo cp "${_src_dir}/tachyonikproxy-update.timer" "/lib/systemd/system/tachyonikproxy-update.timer"
        else
            maybe_sudo tee "/lib/systemd/system/tachyonikproxy-update.timer" >/dev/null <<'UPDTIMER'
[Unit]
Description=Periodic auto-update check for TachyonikProxy

[Timer]
OnBootSec=10min
OnUnitActiveSec=24h
RandomizedDelaySec=1h
Persistent=true
Unit=tachyonikproxy-update.service

[Install]
WantedBy=timers.target
UPDTIMER
        fi

        maybe_sudo systemctl daemon-reload
        maybe_sudo mkdir -p /var/lib/tachyonik/proxy /opt/tachyonik/proxy
        # Bootstrap the auto-update layout (idempotent).
        maybe_sudo /usr/local/bin/tachyonikproxy self-update --bootstrap-layout 2>/dev/null || true
        maybe_sudo systemctl enable --now tachyonikproxy-update.timer 2>/dev/null || \
            warn "Failed to enable auto-update timer."

        # Create system user
        if command -v useradd >/dev/null 2>&1; then
            if ! id tachyonikproxy >/dev/null 2>&1; then
                maybe_sudo useradd --system --no-create-home --shell /usr/sbin/nologin tachyonikproxy
            fi
            maybe_sudo chown -R tachyonikproxy:tachyonikproxy "$SYS_CONFIG_DIR"
            maybe_sudo chown -R tachyonikproxy:tachyonikproxy "$SYS_LOG_DIR"
        fi
    elif [ "$_os" = "darwin" ]; then
        # Install launchd plist — generate inline since tar.gz doesn't include it
        maybe_sudo tee "$LAUNCHD_PLIST" >/dev/null <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.tachyonik.tachyonikproxy</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/tachyonikproxy</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>TACHYONIKPROXY_CONFIG</key>
        <string>/etc/tachyonik/tachyonikproxy/config.yaml</string>
    </dict>
    <key>WorkingDirectory</key>
    <string>/etc/tachyonik/tachyonikproxy</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/var/log/tachyonik/tachyonikproxy.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/tachyonik/tachyonikproxy.log</string>
</dict>
</plist>
PLIST
        maybe_sudo launchctl load "$LAUNCHD_PLIST" 2>/dev/null || true
        info "LaunchDaemon installed and loaded."

        # Auto-update LaunchDaemon (Phase 3). Inline because the tar.gz
        # archive doesn't ship plist files.
        _UPD_PLIST="/Library/LaunchDaemons/com.tachyonik.tachyonikproxy-update.plist"
        maybe_sudo tee "$_UPD_PLIST" >/dev/null <<'UPDPLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.tachyonik.tachyonikproxy-update</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/tachyonikproxy</string>
        <string>self-update</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>TACHYONIKPROXY_CONFIG</key>
        <string>/etc/tachyonik/tachyonikproxy/config.yaml</string>
    </dict>
    <key>WorkingDirectory</key>
    <string>/etc/tachyonik/tachyonikproxy</string>
    <key>RunAtLoad</key>
    <true/>
    <key>StartInterval</key>
    <integer>86400</integer>
    <key>LowPriorityIO</key>
    <true/>
    <key>Nice</key>
    <integer>10</integer>
    <key>StandardOutPath</key>
    <string>/var/log/tachyonik/tachyonikproxy-update.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/tachyonik/tachyonikproxy-update.log</string>
</dict>
</plist>
UPDPLIST
        maybe_sudo mkdir -p /var/lib/tachyonik/proxy /opt/tachyonik/proxy
        maybe_sudo /usr/local/bin/tachyonikproxy self-update --bootstrap-layout 2>/dev/null || true
        maybe_sudo launchctl load "$_UPD_PLIST" 2>/dev/null || true
        info "Auto-update LaunchDaemon installed and loaded."
    fi

    rm -rf "$_extract_dir"
}

# ─── User-space install ────────────────────────────────────────────

install_user_space() {
    _os="$1"
    _arch="$2"
    _url="${DOWNLOAD_BASE}/tachyonikproxy-latest-${_os}-${_arch}.tar.gz"
    _tmp="$(mktemp /tmp/tachyonikproxy-XXXXXX.tar.gz)"
    _extract_dir="$(mktemp -d /tmp/tachyonikproxy-extract-XXXXXX)"

    info "Downloading tar.gz archive for ${_os}/${_arch}..."
    download "$_url" "$_tmp"
    [ -s "$_tmp" ] || error "Download failed or file is empty."

    info "Extracting archive..."
    tar -xzf "$_tmp" -C "$_extract_dir"
    rm -f "$_tmp"

    # Find binary
    _src_dir="$(find "$_extract_dir" -maxdepth 1 -type d | tail -1)"
    _binary=""
    if [ -f "${_src_dir}/${BINARY_NAME}" ]; then
        _binary="${_src_dir}/${BINARY_NAME}"
    elif [ -f "${_extract_dir}/${BINARY_NAME}" ]; then
        _binary="${_extract_dir}/${BINARY_NAME}"
    else
        _binary="$(find "$_extract_dir" -name "$BINARY_NAME" -type f | head -1)"
    fi
    [ -n "$_binary" ] || error "Binary not found in archive."

    # Install binary
    mkdir -p "$USER_BIN_DIR"
    cp "$_binary" "${USER_BIN_DIR}/${BINARY_NAME}"
    chmod 755 "${USER_BIN_DIR}/${BINARY_NAME}"
    info "Binary installed to ${USER_BIN_DIR}/${BINARY_NAME}"

    # Install default config
    mkdir -p "$USER_CONFIG_DIR"
    mkdir -p "$USER_LOG_DIR"
    _default_config=""
    if [ -f "${_src_dir}/config.yaml" ]; then
        _default_config="${_src_dir}/config.yaml"
    elif [ -f "${_src_dir}/config.yaml.default" ]; then
        _default_config="${_src_dir}/config.yaml.default"
    fi
    if [ -n "$_default_config" ] && [ ! -f "${USER_CONFIG_DIR}/config.yaml" ]; then
        # Adjust paths in the config for user-space locations. The shipped
        # template may carry either system paths (/var/log, /etc) or the
        # historic CWD-relative log default (./tachyonikproxy.log) — map
        # both onto the canonical user-space locations.
        sed \
            -e "s|/var/log/tachyonik|${USER_LOG_DIR}|g" \
            -e "s|/etc/tachyonik/tachyonikproxy|${USER_CONFIG_DIR}|g" \
            -e "s|\"\./tachyonikproxy\.log\"|\"${USER_LOG_DIR}/tachyonikproxy.log\"|g" \
            -e "s|: \./tachyonikproxy\.log|: ${USER_LOG_DIR}/tachyonikproxy.log|g" \
            "$_default_config" > "${USER_CONFIG_DIR}/config.yaml"
        info "Default configuration installed at ${USER_CONFIG_DIR}/config.yaml"
    fi

    rm -rf "$_extract_dir"
}

# ─── Uninstaller ────────────────────────────────────────────────────

install_uninstaller_system() {
    _os="$1"

    info "Installing uninstall script at ${SYS_UNINSTALLER}..."
    maybe_sudo tee "$SYS_UNINSTALLER" >/dev/null <<'UNINSTALL_EOF'
#!/bin/sh
# uninstall-tachyonikproxy — Remove TachyonikProxy from this system
set -eu

info()  { printf '\033[1;34m[info]\033[0m  %s\n' "$*"; }
warn()  { printf '\033[1;33m[warn]\033[0m  %s\n' "$*"; }
error() { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

maybe_sudo() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    elif command -v sudo >/dev/null 2>&1; then
        sudo "$@"
    else
        error "This script needs root privileges. Please run as root or install sudo."
    fi
}

info "Uninstalling TachyonikProxy..."

# Stop and disable services. Stop the auto-update timer first so it can't
# kick off a self-update mid-uninstall.
if command -v systemctl >/dev/null 2>&1; then
    maybe_sudo systemctl stop tachyonikproxy-update.timer 2>/dev/null || true
    maybe_sudo systemctl disable tachyonikproxy-update.timer 2>/dev/null || true
    maybe_sudo systemctl stop tachyonikproxy.service 2>/dev/null || true
    maybe_sudo systemctl disable tachyonikproxy.service 2>/dev/null || true
    info "systemd service and timer stopped and disabled."
fi
if [ -f /Library/LaunchDaemons/com.tachyonik.tachyonikproxy-update.plist ]; then
    maybe_sudo launchctl unload /Library/LaunchDaemons/com.tachyonik.tachyonikproxy-update.plist 2>/dev/null || true
    maybe_sudo rm -f /Library/LaunchDaemons/com.tachyonik.tachyonikproxy-update.plist
fi
if [ -f /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist ]; then
    maybe_sudo launchctl unload /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist 2>/dev/null || true
    maybe_sudo rm -f /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist
    info "LaunchDaemons unloaded and removed."
fi

# Remove via native package manager if applicable
_removed_via_pkg=false
if command -v dpkg >/dev/null 2>&1 && dpkg -l tachyonikproxy >/dev/null 2>&1; then
    info "Removing .deb package..."
    maybe_sudo dpkg --purge tachyonikproxy
    _removed_via_pkg=true
elif command -v rpm >/dev/null 2>&1 && rpm -q tachyonikproxy >/dev/null 2>&1; then
    info "Removing .rpm package..."
    maybe_sudo rpm -e tachyonikproxy
    _removed_via_pkg=true
fi

if [ "$_removed_via_pkg" = false ]; then
    # Manual removal (tar.gz install)
    maybe_sudo rm -f /usr/local/bin/tachyonikproxy
    maybe_sudo rm -f /usr/bin/tachyonikproxy
    maybe_sudo rm -f /lib/systemd/system/tachyonikproxy.service
    maybe_sudo rm -f /lib/systemd/system/tachyonikproxy-update.service
    maybe_sudo rm -f /lib/systemd/system/tachyonikproxy-update.timer
    if command -v systemctl >/dev/null 2>&1; then
        maybe_sudo systemctl daemon-reload
    fi
    info "Binary and service files removed."
fi

# Auto-update version store + state. Created at runtime by the updater
# (per-version binaries, current symlink, update-state.json), so neither
# dpkg --purge nor rpm -e knows about it — remove it on every path.
# Program data, not user data: no prompt.
if [ -d /opt/tachyonik/proxy ] || [ -d /var/lib/tachyonik/proxy ]; then
    maybe_sudo rm -rf /opt/tachyonik/proxy
    maybe_sudo rm -rf /var/lib/tachyonik/proxy
    info "Auto-update version store removed."
fi

# Ask about config/certs/logs
printf '\n'
printf 'Remove configuration, certificates, and logs? [y/N] '
read -r _answer </dev/tty 2>/dev/null || _answer="n"
case "$_answer" in
    [Yy]*)
        maybe_sudo rm -rf /etc/tachyonik/tachyonikproxy
        maybe_sudo rm -rf /var/log/tachyonik
        info "Configuration, certificates, and logs removed."
        ;;
    *)
        info "Configuration preserved at /etc/tachyonik/tachyonikproxy"
        ;;
esac

# Remove system user (Linux)
if command -v userdel >/dev/null 2>&1 && id tachyonikproxy >/dev/null 2>&1; then
    maybe_sudo userdel tachyonikproxy 2>/dev/null || true
    info "System user 'tachyonikproxy' removed."
fi

# Clean up the shared tachyonik namespace directories ONLY when empty —
# rmdir refuses to touch a non-empty directory, so anything another
# Tachyonik product (or a preserved config) still keeps in there
# survives untouched.
maybe_sudo rmdir /opt/tachyonik 2>/dev/null || true
maybe_sudo rmdir /var/lib/tachyonik 2>/dev/null || true
maybe_sudo rmdir /etc/tachyonik 2>/dev/null || true

# Remove this uninstaller
maybe_sudo rm -f /usr/local/bin/uninstall-tachyonikproxy

info "TachyonikProxy has been uninstalled."
UNINSTALL_EOF
    maybe_sudo chmod 755 "$SYS_UNINSTALLER"
}

install_uninstaller_user() {
    info "Installing uninstall script at ${USER_UNINSTALLER}..."
    cat > "$USER_UNINSTALLER" <<UNINSTALL_USER_EOF
#!/bin/sh
# uninstall-tachyonikproxy — Remove user-space TachyonikProxy installation
set -eu

info()  { printf '\033[1;34m[info]\033[0m  %s\n' "\$*"; }

info "Uninstalling TachyonikProxy (user-space)..."

# The binary may be a symlink into the auto-update version store; remove
# both. The version store (per-version binaries, current symlink,
# update-state.json) is program data, not user data — no prompt.
rm -f "${USER_BIN_DIR}/${BINARY_NAME}"
rm -rf "${USER_UPDATE_DIR}"
info "Binary and auto-update version store removed."

printf '\n'
printf 'Remove configuration, certificates, and logs? [y/N] '
read -r _answer </dev/tty 2>/dev/null || _answer="n"
case "\$_answer" in
    [Yy]*)
        rm -rf "${USER_CONFIG_DIR}"
        rm -rf "${USER_LOG_DIR}"
        info "Configuration, certificates, and logs removed."
        ;;
    *)
        info "Configuration preserved at ${USER_CONFIG_DIR}"
        ;;
esac

# Clean up the shared tachyonik namespace directories ONLY when empty —
# rmdir refuses to touch a non-empty directory, so anything another
# Tachyonik product (or a preserved config/log) still keeps in there
# survives untouched.
rmdir "${HOME}/.config/tachyonik" 2>/dev/null || true
rmdir "${HOME}/.local/share/tachyonik" 2>/dev/null || true

rm -f "${USER_UNINSTALLER}"
info "TachyonikProxy has been uninstalled."
UNINSTALL_USER_EOF
    chmod 755 "$USER_UNINSTALLER"
}

# ─── Success messages ──────────────────────────────────────────────

# Print the "Installed version: X" or "Upgraded from X to Y" line shown
# under each green success banner. Quietly emits nothing if the freshly
# installed binary couldn't be queried — better to drop the line than to
# show a placeholder.
print_version_line() {
    _is_upgrade="$1"
    [ -n "$INSTALLED_VERSION" ] || return 0
    if [ "$_is_upgrade" = "1" ]; then
        _from="${PRIOR_VERSION:-unknown}"
        printf '  Upgraded from %s to %s\n' "$_from" "$INSTALLED_VERSION"
    else
        printf '  Installed version: %s\n' "$INSTALLED_VERSION"
    fi
}

print_success_system() {
    _os="$1"
    printf '\n'
    printf '\033[1;32m  TachyonikProxy installed successfully!\033[0m\n'
    print_version_line 0
    printf '\n'
    printf '  Next steps:\n'
    printf '    1. Enroll with your Tachyonik instance (pick one):\n'
    printf '       a) Online enrollment (proxy calls home):\n'
    printf '          tachyonikproxy enroll "https://your-instance/api/proxy-enroll?token=TOKEN"\n'
    printf '       b) Reverse enrollment (strict-egress networks; Tachyonik dials the proxy):\n'
    printf '          tachyonikproxy enroll --listen\n'
    printf '          Then enter the printed pairing code in the Tachyonik WebUI.\n'
    printf '\n'
    if [ "$_os" = "linux" ]; then
        printf '    2. Start the service:\n'
        printf '       sudo systemctl start tachyonikproxy\n'
        printf '\n'
        # A .deb / .rpm install belongs to dpkg or rpm; built-in auto-update is
        # off there, so say what actually upgrades it rather than pointing at a
        # timer the package no longer ships.
        if [ "${PKG_METHOD:-tar}" = "tar" ]; then
            printf '  Auto-updates: a systemd timer (tachyonikproxy-update.timer) is\n'
            printf '  enabled. The proxy will check for signed updates daily and apply\n'
            printf '  them with automatic rollback on health failure. To disable:\n'
            printf '    sudo systemctl disable --now tachyonikproxy-update.timer\n'
        else
            printf '  Updates: this install is managed by your package manager, so\n'
            printf '  built-in auto-update is off. To upgrade, re-run this installer:\n'
            printf '    curl -fsSL %s/install-tachyonikproxy.sh | sudo sh\n' "$DOWNLOAD_BASE"
        fi
    elif [ "$_os" = "darwin" ]; then
        printf '    2. The service starts automatically via LaunchDaemon.\n'
        printf '       To restart: sudo launchctl unload && sudo launchctl load %s\n' "$LAUNCHD_PLIST"
        printf '\n'
        printf '  Auto-updates: a LaunchDaemon (com.tachyonik.tachyonikproxy-update)\n'
        printf '  is loaded and runs daily.\n'
    fi
    printf '\n'
    printf '    To uninstall later: sudo uninstall-tachyonikproxy\n'
    printf '\n'
}

print_success_upgrade_system() {
    _os="$1"
    printf '\n'
    printf '\033[1;32m  TachyonikProxy upgraded successfully!\033[0m\n'
    print_version_line 1
    printf '\n'
    printf '  This proxy is already enrolled with Tachyonik. The existing\n'
    printf '  configuration and certificates have been preserved, so re-enrollment\n'
    printf '  is NOT required.\n'
    printf '\n'
    if [ "$_os" = "linux" ]; then
        printf '  Restart the service to pick up the new binary:\n'
        printf '    sudo systemctl restart tachyonikproxy\n'
    elif [ "$_os" = "darwin" ]; then
        printf '  Restart the service to pick up the new binary:\n'
        printf '    sudo launchctl unload %s\n' "$LAUNCHD_PLIST"
        printf '    sudo launchctl load %s\n' "$LAUNCHD_PLIST"
    fi
    printf '\n'
    printf '  Re-enroll anyway? [y/N] '
    read -r _answer </dev/tty 2>/dev/null || _answer="n"
    case "$_answer" in
        [Yy]*)
            printf '\n'
            printf '  To re-enroll, pick one:\n'
            printf '    a) Online enrollment (proxy calls home):\n'
            printf '       tachyonikproxy enroll "https://your-instance/api/proxy-enroll?token=TOKEN"\n'
            printf '    b) Reverse enrollment (strict-egress networks; Tachyonik dials the proxy):\n'
            printf '       tachyonikproxy enroll --listen\n'
            printf '       Then enter the printed pairing code in the Tachyonik WebUI.\n'
            printf '\n'
            printf '  Note: re-enrollment overwrites the existing certificates and\n'
            printf '  may require an admin action in the Tachyonik WebUI.\n'
            ;;
        *)
            ;;
    esac
    printf '\n'
    printf '  To uninstall later: sudo uninstall-tachyonikproxy\n'
    printf '\n'
}

print_success_upgrade_user() {
    printf '\n'
    printf '\033[1;32m  TachyonikProxy upgraded successfully (user-space)!\033[0m\n'
    print_version_line 1
    printf '\n'
    printf '  This proxy is already enrolled with Tachyonik. The existing\n'
    printf '  configuration and certificates have been preserved, so re-enrollment\n'
    printf '  is NOT required.\n'
    printf '\n'
    printf '  Restart the proxy to pick up the new binary (kill the running\n'
    printf '  process and re-launch it):\n'
    printf '    %s/%s\n' "$USER_BIN_DIR" "$BINARY_NAME"
    printf '  (the binary finds %s/config.yaml on its own)\n' "$USER_CONFIG_DIR"
    printf '\n'
    printf '  Re-enroll anyway? [y/N] '
    read -r _answer </dev/tty 2>/dev/null || _answer="n"
    case "$_answer" in
        [Yy]*)
            printf '\n'
            printf '  To re-enroll, pick one:\n'
            printf '    a) Online enrollment:\n'
            printf '       %s/%s enroll "https://your-instance/api/proxy-enroll?token=TOKEN"\n' "$USER_BIN_DIR" "$BINARY_NAME"
            printf '    b) Reverse enrollment (strict-egress networks):\n'
            printf '       %s/%s enroll --listen\n' "$USER_BIN_DIR" "$BINARY_NAME"
            printf '       Then enter the printed pairing code in the Tachyonik WebUI.\n'
            printf '\n'
            printf '  Note: re-enrollment overwrites the existing certificates and\n'
            printf '  may require an admin action in the Tachyonik WebUI.\n'
            ;;
        *)
            ;;
    esac
    printf '\n'
    printf '  To uninstall later: %s\n' "$USER_UNINSTALLER"
    printf '\n'
}

print_success_user() {
    printf '\n'
    printf '\033[1;32m  TachyonikProxy installed successfully (user-space)!\033[0m\n'
    print_version_line 0
    printf '\n'

    # Check if ~/.local/bin is in PATH
    case ":${PATH}:" in
        *":${USER_BIN_DIR}:"*) ;;
        *)
            printf '  \033[1;33mNote:\033[0m %s is not in your PATH.\n' "$USER_BIN_DIR"
            printf '  Add it by appending this to your shell profile (~/.bashrc, ~/.zshrc, etc.):\n'
            printf '    export PATH="%s:$PATH"\n' "$USER_BIN_DIR"
            printf '\n'
            ;;
    esac

    printf '  Next steps:\n'
    printf '    1. Enroll with your Tachyonik instance (pick one):\n'
    printf '       a) Online enrollment:\n'
    printf '          %s/%s enroll "https://your-instance/api/proxy-enroll?token=TOKEN"\n' "$USER_BIN_DIR" "$BINARY_NAME"
    printf '       b) Reverse enrollment (strict-egress networks):\n'
    printf '          %s/%s enroll --listen\n' "$USER_BIN_DIR" "$BINARY_NAME"
    printf '          Then enter the printed pairing code in the Tachyonik WebUI.\n'
    printf '\n'
    printf '    2. Start manually:\n'
    printf '       %s/%s\n' "$USER_BIN_DIR" "$BINARY_NAME"
    printf '       (the binary finds %s/config.yaml on its own)\n' "$USER_CONFIG_DIR"
    printf '\n'
    printf '    Note: This is a user-space installation. The service will NOT start\n'
    printf '    automatically on boot. To make it persistent, consider adding it to\n'
    printf '    crontab (@reboot) or a user systemd unit.\n'
    printf '\n'
    printf '    To uninstall later: %s\n' "$USER_UNINSTALLER"
    printf '\n'
}

# ─── Main ───────────────────────────────────────────────────────────

main() {
    info "TachyonikProxy Installer v${INSTALLER_VERSION}"
    printf '\n'

    OS="$(detect_os)"
    ARCH="$(detect_arch)"
    info "Detected platform: ${OS}/${ARCH}"

    # Check for existing install
    WAS_INSTALLED=0
    if command -v tachyonikproxy >/dev/null 2>&1; then
        PRIOR_VERSION="$(extract_proxy_version tachyonikproxy)"
        [ -n "$PRIOR_VERSION" ] || PRIOR_VERSION="unknown"
        warn "TachyonikProxy is already installed (${PRIOR_VERSION}). Upgrading..."
        WAS_INSTALLED=1
    fi

    # Determine install mode: system-wide or user-space
    if [ "$(id -u)" -eq 0 ]; then
        INSTALL_MODE="system"
    elif can_elevate; then
        INSTALL_MODE="system"
    else
        warn "You do not have sudo privileges on this system."
        printf '\n'
        printf '  You have two options:\n'
        printf '\n'
        printf '    1) Re-run this script as a user with sudo permissions\n'
        printf '       (installs as a system service that starts automatically on boot)\n'
        printf '\n'
        printf '    2) Install in your home directory (~/.local/bin/tachyonikproxy)\n'
        printf '       (you will need to start it manually; it will not survive reboots\n'
        printf '        unless you configure it yourself, e.g. via crontab or a user systemd unit)\n'
        printf '\n'
        printf '  Install in user space? [Y/n] '
        read -r _answer </dev/tty 2>/dev/null || _answer="y"
        case "$_answer" in
            [Nn]*)
                info "Aborting. Please re-run as a user with sudo permissions."
                exit 0
                ;;
            *)
                INSTALL_MODE="user"
                ;;
        esac
    fi

    # Detect enrollment state against the config dir that this run will reuse.
    # Done before install so we can branch the success message; the install
    # paths preserve the existing config and certs/ directory.
    WAS_ENROLLED=0
    if [ "$INSTALL_MODE" = "system" ]; then
        if is_proxy_enrolled "$SYS_CONFIG_DIR"; then WAS_ENROLLED=1; fi
    else
        if is_proxy_enrolled "$USER_CONFIG_DIR"; then WAS_ENROLLED=1; fi
    fi

    if [ "$INSTALL_MODE" = "system" ]; then
        info "Install mode: system-wide"

        if [ "$OS" = "linux" ]; then
            PKG_METHOD="$(detect_package_manager)"
            info "Package method: ${PKG_METHOD}"

            case "$PKG_METHOD" in
                deb)  install_native_deb "$ARCH" ;;
                rpm)  install_native_rpm "$ARCH" ;;
                tar)  install_from_tar "$OS" "$ARCH" ;;
            esac
        elif [ "$OS" = "darwin" ]; then
            PKG_METHOD="tar"
            install_from_tar "$OS" "$ARCH"
        fi

        install_uninstaller_system "$OS"
        # Query the freshly-installed binary. Use PATH lookup since the
        # native package and tar paths can both end up on $PATH; this also
        # means a follow-up `tachyonikproxy` call in the user's shell will
        # resolve to the same binary we just queried.
        INSTALLED_VERSION="$(extract_proxy_version tachyonikproxy)"
        if [ "$WAS_INSTALLED" = "1" ] && [ "$WAS_ENROLLED" = "1" ]; then
            print_success_upgrade_system "$OS"
        else
            print_success_system "$OS"
        fi
    else
        info "Install mode: user-space (~/.local/bin)"
        install_user_space "$OS" "$ARCH"
        install_uninstaller_user
        # Query the absolute path: user-space installs aren't necessarily
        # on $PATH (the success banner warns when $USER_BIN_DIR isn't in
        # $PATH), so resolving by name would miss them.
        INSTALLED_VERSION="$(extract_proxy_version "${USER_BIN_DIR}/${BINARY_NAME}")"
        if [ "$WAS_INSTALLED" = "1" ] && [ "$WAS_ENROLLED" = "1" ]; then
            print_success_upgrade_user
        else
            print_success_user
        fi
    fi
}

# Clean up temp files on unexpected exit
TMPFILES=""
cleanup() {
    [ -n "$TMPFILES" ] && rm -f $TMPFILES 2>/dev/null || true
}
trap cleanup EXIT INT TERM

main "$@"
