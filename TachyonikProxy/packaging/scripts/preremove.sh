#!/bin/sh
#
# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later
set -e

# Stop systemd units (Linux). Stop the timer first so it can't kick off a
# self-update mid-uninstall.
if command -v systemctl >/dev/null 2>&1; then
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
