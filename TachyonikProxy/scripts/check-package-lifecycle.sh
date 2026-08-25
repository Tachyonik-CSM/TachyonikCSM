#!/usr/bin/env bash
# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# check-package-lifecycle.sh — run the real maintainer scripts through the real
# packagers and assert what state the service is left in.
#
# Up to 1.1.2 an upgrade stopped the proxy and nobody noticed, because the bug
# is not in either script read on its own: postinstall.sh enables, preremove.sh
# stops and disables, and both look reasonable. It only appears in the order the
# packager runs them — and the two packagers disagree about that order:
#
#   dpkg:  old prerm (stop+disable)  →  new postinst (enable)   → enabled, DOWN
#   rpm:   new %post (enable)        →  old %preun (stop+disable) → DISABLED, DOWN
#
# So this check drives the actual sequence rather than reasoning about it. The
# packages are throwaway probes carrying nothing but packaging/scripts/*.sh, and
# systemctl is replaced by a shim that tracks enabled/active state, since no
# container has a running systemd.
#
# Run from anywhere:  scripts/check-package-lifecycle.sh

set -euo pipefail

cd "$(dirname "$0")/.."

POSTINSTALL="packaging/scripts/postinstall.sh"
PREREMOVE="packaging/scripts/preremove.sh"
UNIT="packaging/systemd/tachyonikproxy.service"

for f in "$POSTINSTALL" "$PREREMOVE" "$UNIT"; do
    [ -f "$f" ] || { echo "not found: $f" >&2; exit 1; }
done

if ! command -v docker >/dev/null 2>&1; then
    echo "SKIP: docker is required to exercise dpkg and rpm" >&2
    exit 0
fi

DEB_IMAGE="${DEB_IMAGE:-debian:13-slim}"
RPM_IMAGE="${RPM_IMAGE:-rockylinux:9}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

cp "$POSTINSTALL" "$workdir/postinstall.sh"
cp "$PREREMOVE" "$workdir/preremove.sh"

# A systemctl good enough to answer the questions the scripts ask. Real systemd
# is not available in a container, and `enable`/`is-active` returning nonsense
# would make the probe agree with anything.
cat > "$workdir/systemctl-shim.sh" <<'SHIM'
#!/bin/sh
# Records every call and maintains just enough state for is-active to be honest.
echo "systemctl $*" >> /tmp/calls.log
[ -f /tmp/enabled ] || echo disabled > /tmp/enabled
[ -f /tmp/active ]  || echo inactive > /tmp/active

_verb="$1"; shift
_now=false
_ours=false
for _a in "$@"; do
    [ "$_a" = "--now" ] && _now=true
    # Only tachyonikproxy.service is tracked. The scripts also act on the
    # legacy tachyonikproxy-update.timer, and a shim that lumped every unit
    # together reported the service as stopped because the *timer* had been
    # disabled --now.
    case "$_a" in tachyonikproxy.service|tachyonikproxy) _ours=true ;; esac
done
$_ours || exit 0

case "$_verb" in
    enable)  echo enabled  > /tmp/enabled; $_now && echo active   > /tmp/active ;;
    disable) echo disabled > /tmp/enabled; $_now && echo inactive > /tmp/active ;;
    start|restart)  echo active   > /tmp/active ;;
    stop)           echo inactive > /tmp/active ;;
    # try-restart restarts only a running unit, and is a no-op otherwise —
    # which is the entire reason postinstall.sh uses it.
    try-restart)    : ;;
    is-active)      [ "$(cat /tmp/active)" = active ] || exit 3 ;;
esac
exit 0
SHIM

# The shared body of both probes: report the final state so the caller can
# assert on it.
cat > "$workdir/report.sh" <<'REPORT'
echo "RESULT enabled=$(cat /tmp/enabled) active=$(cat /tmp/active)"
REPORT

fail=0

# assert_state <label> <output> <expected-enabled> <expected-active>
assert_state() {
    local label="$1" output="$2" want_enabled="$3" want_active="$4"
    local line
    line="$(printf '%s\n' "$output" | grep '^RESULT ' | tail -1 || true)"
    if [ -z "$line" ]; then
        echo "FAIL: $label produced no result — the probe did not run" >&2
        printf '%s\n' "$output" | sed 's/^/    /' >&2
        fail=1
        return
    fi
    local want="RESULT enabled=$want_enabled active=$want_active"
    if [ "$line" = "$want" ]; then
        echo "  ok: $label → $want_enabled, $want_active"
    else
        echo "FAIL: $label" >&2
        echo "    got:  $line" >&2
        echo "    want: $want" >&2
        fail=1
    fi
}

# ---------------------------------------------------------------- dpkg -------

cat > "$workdir/deb-probe.sh" <<'EOF'
set -e
install -m 755 /w/systemctl-shim.sh /usr/bin/systemctl
mkdir -p /run/systemd/system   # stand in for "systemd is running"

build() {
    rm -rf /tmp/p; mkdir -p /tmp/p/DEBIAN /tmp/p/usr/share/tachyonik/tachyonikproxy
    echo "$1" > /tmp/p/usr/share/tachyonik/tachyonikproxy/version
    cat > /tmp/p/DEBIAN/control <<C
Package: tachyonikproxy
Version: $1
Architecture: all
Maintainer: Tachyonik GmbH <noreply@tachyonik.io>
Description: lifecycle probe
C
    cp /w/postinstall.sh /tmp/p/DEBIAN/postinst
    cp /w/preremove.sh   /tmp/p/DEBIAN/prerm
    chmod 755 /tmp/p/DEBIAN/postinst /tmp/p/DEBIAN/prerm
    dpkg-deb -b /tmp/p "/tmp/tachyonikproxy_$1.deb" >/dev/null
}

build 1.1.1
build 1.1.2

: > /tmp/calls.log
dpkg -i /tmp/tachyonikproxy_1.1.1.deb >/dev/null 2>&1
echo "PHASE fresh"
. /w/report.sh

# An enrolled proxy that the operator has started: the state an upgrade must
# preserve.
systemctl start tachyonikproxy.service
dpkg -i /tmp/tachyonikproxy_1.1.2.deb >/dev/null 2>&1
echo "PHASE upgrade"
. /w/report.sh

# A genuine removal must still stop and disable.
dpkg -r tachyonikproxy >/dev/null 2>&1
echo "PHASE remove"
. /w/report.sh
EOF

# ----------------------------------------------------------------- rpm -------

cat > "$workdir/rpm-probe.sh" <<'EOF'
set -e
dnf -q install -y rpm-build >/dev/null 2>&1
install -m 755 /w/systemctl-shim.sh /usr/bin/systemctl
mkdir -p /run/systemd/system

mkdir -p /root/rpmbuild/SPECS
build() {
    {
        cat <<SPEC
Name: tachyonikproxy
Version: $1
Release: 1
Summary: lifecycle probe
License: AGPL-3.0-or-later
%description
probe
%install
mkdir -p %{buildroot}/usr/share/tachyonik/tachyonikproxy
echo $1 > %{buildroot}/usr/share/tachyonik/tachyonikproxy/version
%files
/usr/share/tachyonik/tachyonikproxy/version
%post
SPEC
        # The real scripts, verbatim, exactly as nfpm embeds them.
        cat /w/postinstall.sh
        echo "%preun"
        cat /w/preremove.sh
    } > /root/rpmbuild/SPECS/t.spec
    rpmbuild -bb /root/rpmbuild/SPECS/t.spec >/dev/null 2>&1
}

build 1.1.1
build 1.1.2
_arch="$(uname -m)"

: > /tmp/calls.log
rpm -i "/root/rpmbuild/RPMS/$_arch/tachyonikproxy-1.1.1-1.$_arch.rpm" >/dev/null 2>&1
echo "PHASE fresh"
. /w/report.sh

systemctl start tachyonikproxy.service
rpm -U "/root/rpmbuild/RPMS/$_arch/tachyonikproxy-1.1.2-1.$_arch.rpm" >/dev/null 2>&1
echo "PHASE upgrade"
. /w/report.sh

rpm -e tachyonikproxy >/dev/null 2>&1
echo "PHASE remove"
. /w/report.sh
EOF

run_probe() {
    local image="$1" script="$2"
    docker run --rm -v "$workdir:/w" "$image" sh "/w/$script" 2>&1
}

# phase_output <all-output> <phase>
phase_output() {
    printf '%s\n' "$1" | awk -v p="PHASE $2" '$0 == p {grab=1; next} /^PHASE /{grab=0} grab'
}

# '|' as the separator: the image names carry colons of their own.
for spec in "deb|$DEB_IMAGE|deb-probe.sh" "rpm|$RPM_IMAGE|rpm-probe.sh"; do
    kind="${spec%%|*}"; rest="${spec#*|}"
    image="${rest%%|*}"; script="${rest##*|}"

    echo "$kind ($image):"
    if ! out="$(run_probe "$image" "$script")"; then
        echo "FAIL: $kind probe did not complete" >&2
        printf '%s\n' "$out" | sed 's/^/    /' >&2
        fail=1
        continue
    fi

    # A fresh install enables for boot but must not start: nothing is enrolled
    # yet, and an unenrolled proxy exits fatally into a restart loop.
    assert_state "$kind fresh install" "$(phase_output "$out" fresh)" enabled inactive

    # The regression this file exists for. "enabled" alone is not enough — the
    # dpkg ordering already produced that while leaving the proxy down.
    assert_state "$kind upgrade of a running service" "$(phase_output "$out" upgrade)" enabled active

    # And removal must still do what removal is for.
    assert_state "$kind removal" "$(phase_output "$out" remove)" disabled inactive
done

if [ "$fail" -ne 0 ]; then
    echo "package lifecycle check FAILED" >&2
    exit 1
fi

echo "package lifecycle check passed"
