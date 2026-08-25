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

# Clean up the auto-update machinery that packages up to 1.1.0 installed.
#
# This install is owned by dpkg/rpm, so built-in auto-update does not apply —
# see /usr/share/tachyonik/tachyonikproxy/package-managed. Earlier packages
# enabled a timer and bootstrapped a second copy of the binary under /opt,
# which the service never executed. Both are removed here; all of it is
# best-effort, since a package install must not fail over cleanup.
if [ -d /run/systemd/system ]; then
    # The unit files are gone with this upgrade, but the enable-symlink was
    # created by our own postinstall, so dpkg does not remove it.
    systemctl disable --now tachyonikproxy-update.timer 2>/dev/null || true
    rm -f /etc/systemd/system/timers.target.wants/tachyonikproxy-update.timer
    rm -f /etc/systemd/system/tachyonikproxy-update.timer
    rm -f /etc/systemd/system/tachyonikproxy-update.service
fi

# The old bootstrap left /usr/local/bin/tachyonikproxy pointing into
# /opt/tachyonik/proxy. That path usually precedes /usr/bin, so leaving it
# behind means a shell `tachyonikproxy --version` can disagree with the binary
# the service is actually running. Remove it only when it is a symlink into
# that tree — never a real file an operator put there.
if [ -L /usr/local/bin/tachyonikproxy ]; then
    _target="$(readlink -f /usr/local/bin/tachyonikproxy 2>/dev/null || true)"
    case "$_target" in
        /opt/tachyonik/proxy/*) rm -f /usr/local/bin/tachyonikproxy ;;
    esac
fi

# /opt/tachyonik/proxy is deliberately left in place: it is inert once nothing
# points at it, and a package script deleting a tree outside its own file list
# is how someone's machine gets damaged. Say it is there instead.
if [ -d /opt/tachyonik/proxy ] && [ -n "$(ls -A /opt/tachyonik/proxy 2>/dev/null)" ]; then
    echo "Note: /opt/tachyonik/proxy holds binaries from the old auto-update layout."
    echo "      Nothing uses them now; the directory can be removed."
fi

# Enable and start systemd units (Linux)
if [ -d /run/systemd/system ]; then
    systemctl daemon-reload
    systemctl enable tachyonikproxy.service

    # try-restart, not start.
    #
    # On an upgrade the service is still running the old binary at this point,
    # and try-restart swaps it for the new one — without it, the upgrade left
    # the proxy stopped and someone had to notice and start it by hand.
    #
    # On a fresh install the unit is inactive and try-restart does nothing,
    # which is the behaviour we want: a fresh install is not enrolled yet, and
    # an unenrolled proxy exits fatally by design (requireOutboundTLS /
    # requireInboundTLS). Starting it here would drop the unit into a
    # Restart=on-failure loop every 5 seconds until somebody enrolled it.
    #
    # It also respects a deliberately stopped service: an operator who stopped
    # the proxy does not get it restarted by an unrelated package upgrade.
    if systemctl is-active --quiet tachyonikproxy.service; then
        # Not allowed to fail the transaction: `set -e` is on, and a daemon
        # that does not come up must not leave dpkg with a half-configured
        # package. systemd's Restart=on-failure keeps trying either way, so say
        # so and let the operator look at the log.
        if systemctl try-restart tachyonikproxy.service; then
            echo "Service restarted on the new version."
        else
            echo "Warning: tachyonikproxy did not restart cleanly."
            echo "         Check: systemctl status tachyonikproxy"
        fi
    else
        echo "Service enabled; it will start on boot."
        echo "Not started now — enroll first if you have not already:"
        echo "    sudo tachyonikproxy enroll <enrollment-url>"
        echo "then: sudo systemctl start tachyonikproxy"
    fi
    echo "Upgrades come from your package manager; built-in auto-update is off."
fi

# Load launchd plist (macOS)
if [ -f /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist ]; then
    launchctl load /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist 2>/dev/null || true
    echo "LaunchDaemon loaded. Service will start automatically."
fi
