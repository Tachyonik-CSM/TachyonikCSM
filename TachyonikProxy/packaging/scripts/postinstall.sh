#!/bin/sh
#
# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later
set -e

# Create system user if it doesn't exist (Linux only)
if command -v useradd >/dev/null 2>&1; then
    if ! id tachyonikproxy >/dev/null 2>&1; then
        useradd --system --no-create-home --shell /usr/sbin/nologin tachyonikproxy
    fi
fi

# Create directories
mkdir -p /etc/tachyonik/tachyonikproxy
mkdir -p /var/log/tachyonik
mkdir -p /var/lib/tachyonik/proxy
mkdir -p /opt/tachyonik/proxy

# Install default config if none exists
if [ ! -f /etc/tachyonik/tachyonikproxy/config.yaml ]; then
    if [ -f /usr/share/tachyonik/tachyonikproxy/config.yaml.default ]; then
        cp /usr/share/tachyonik/tachyonikproxy/config.yaml.default /etc/tachyonik/tachyonikproxy/config.yaml
    fi
fi

# Set ownership
if id tachyonikproxy >/dev/null 2>&1; then
    chown -R tachyonikproxy:tachyonikproxy /etc/tachyonik/tachyonikproxy
    chown -R tachyonikproxy:tachyonikproxy /var/log/tachyonik
fi

# Bootstrap the auto-update versioned-symlink layout. Idempotent — a no-op
# on installs that are already migrated. Failures are warnings, not fatal:
# the package install should not fail just because the auto-update layout
# couldn't be set up. The next manual `self-update --bootstrap-layout` (or
# the operator's own remediation) can finish the job.
if [ -x /usr/bin/tachyonikproxy ]; then
    /usr/bin/tachyonikproxy self-update --bootstrap-layout 2>/dev/null || \
        echo "Warning: failed to bootstrap auto-update layout. Run 'tachyonikproxy self-update --bootstrap-layout' manually."
fi

# Enable and start systemd units (Linux)
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload
    systemctl enable tachyonikproxy.service

    # Auto-update timer: enable + start so the first check fires shortly
    # after boot. The proxy itself need not be running for the timer to
    # tick; the apply path skips when TLS material is missing, so an
    # unenrolled proxy is harmless.
    systemctl enable --now tachyonikproxy-update.timer 2>/dev/null || \
        echo "Warning: failed to enable auto-update timer."

    echo "Service installed. Start with: systemctl start tachyonikproxy"
    echo "Auto-update timer: systemctl list-timers tachyonikproxy-update.timer"
fi

# Load launchd plist (macOS)
if [ -f /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist ]; then
    launchctl load /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist 2>/dev/null || true
    echo "LaunchDaemon loaded. Service will start automatically."
fi
