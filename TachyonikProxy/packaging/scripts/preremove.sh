#!/bin/sh
#
# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later
set -e

# This script runs on removal *and* on upgrade, and until 1.1.2 it did not tell
# the two apart — so every `apt upgrade` / `dnf upgrade` stopped and disabled a
# perfectly healthy proxy.
#
# On dpkg the damage was "stopped": prerm (stop+disable) runs before the new
# postinst (enable), so the service came out enabled but down. On rpm it was
# worse: the new %post runs *before* the old %preun, so `disable` was the last
# word and the proxy did not come back after a reboot either.
#
# The two packagers signal the difference differently — dpkg passes a word,
# rpm passes a count of the instances that will remain — so match both.
case "${1:-}" in
    remove|purge|0)
        # A genuine removal: stopping and disabling is the whole point.
        ;;
    *)
        # upgrade / failed-upgrade / deconfigure. The new package's postinstall
        # takes it from here; touching the unit now only breaks a running proxy.
        exit 0
        ;;
esac

# Stop systemd units (Linux). The update timer is no longer shipped — a
# package-managed install upgrades through dpkg/rpm — but stop it anyway in
# case one survives from a package built before 1.1.1, so it cannot kick off a
# self-update mid-uninstall.
#
# -d /run/systemd/system rather than `command -v systemctl`: the binary exists
# in containers and chroots where no systemd is running, and the calls fail
# there.
if [ -d /run/systemd/system ]; then
    systemctl stop tachyonikproxy-update.timer 2>/dev/null || true
    systemctl disable tachyonikproxy-update.timer 2>/dev/null || true
    systemctl stop tachyonikproxy.service 2>/dev/null || true
    systemctl disable tachyonikproxy.service 2>/dev/null || true
fi

# Unload launchd plists (macOS)
if [ -f /Library/LaunchDaemons/com.tachyonik.tachyonikproxy-update.plist ]; then
    launchctl unload /Library/LaunchDaemons/com.tachyonik.tachyonikproxy-update.plist 2>/dev/null || true
fi
if [ -f /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist ]; then
    launchctl unload /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist 2>/dev/null || true
fi
