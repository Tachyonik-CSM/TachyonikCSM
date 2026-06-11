#!/usr/bin/env bash
# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# update-latest-symlinks.sh — repoint the `…latest…` download symlinks at a
# specific TachyonikProxy release, or (with no argument) at the highest version
# present. Run it IN the proxy download directory.
#
# Usage:
#   update-latest-symlinks.sh [options] [VERSION]
#
#   VERSION    e.g. 1.0.0 — link to this version. Omit to pick the highest
#              version found in the current directory.
#
# Options:
#   -n, --dry-run   show what would change; make no changes
#   -h, --help      show this help
#
# It maintains relative symlinks for every package family:
#   tachyonikproxy-latest-{linux,darwin}-{amd64,arm64}.tar.gz
#   tachyonikproxy-latest-windows-amd64.{zip,msi}
#   tachyonikproxy-latest.{aarch64,x86_64}.rpm
#   tachyonikproxy_latest_{amd64,arm64}.deb
#
# A family with no artifact for the chosen version is skipped (its existing
# symlink, if any, is left untouched).

set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

dryrun=0
version=""

while [ $# -gt 0 ]; do
  case "$1" in
    -n|--dry-run) dryrun=1; shift ;;
    -h|--help)
      awk 'NR>=6 && /^#/ {sub(/^# ?/,""); print; next} NR>=6 {exit}' "$0"
      exit 0 ;;
    -*) die "unknown option: $1 (try --help)" ;;
    *)  [ -z "$version" ] || die "unexpected extra argument: $1"; version="$1"; shift ;;
  esac
done

# latest-link | versioned template (%V = version; * is a glob, e.g. the rpm
# release number, which the 'latest' name drops).
ENTRIES=(
  "tachyonikproxy-latest-linux-amd64.tar.gz|tachyonikproxy-%V-linux-amd64.tar.gz"
  "tachyonikproxy-latest-linux-arm64.tar.gz|tachyonikproxy-%V-linux-arm64.tar.gz"
  "tachyonikproxy-latest-darwin-amd64.tar.gz|tachyonikproxy-%V-darwin-amd64.tar.gz"
  "tachyonikproxy-latest-darwin-arm64.tar.gz|tachyonikproxy-%V-darwin-arm64.tar.gz"
  "tachyonikproxy-latest-windows-amd64.zip|tachyonikproxy-%V-windows-amd64.zip"
  "tachyonikproxy-latest-windows-amd64.msi|tachyonikproxy-%V-windows-amd64.msi"
  "tachyonikproxy-latest.aarch64.rpm|tachyonikproxy-%V-*.aarch64.rpm"
  "tachyonikproxy-latest.x86_64.rpm|tachyonikproxy-%V-*.x86_64.rpm"
  "tachyonikproxy_latest_amd64.deb|tachyonikproxy_%V_amd64.deb"
  "tachyonikproxy_latest_arm64.deb|tachyonikproxy_%V_arm64.deb"
)

# Resolve a (possibly globbed) template to a single existing *regular* file.
# When several match (e.g. multiple rpm releases) the highest by version-sort
# wins. Prints the filename, or returns 1 if none.
resolve_one() {
  local pat="$1" f good=()
  shopt -s nullglob
  for f in $pat; do
    [ -f "$f" ] && [ ! -L "$f" ] && good+=("$f")
  done
  shopt -u nullglob
  [ "${#good[@]}" -gt 0 ] || return 1
  printf '%s\n' "${good[@]}" | sort -V | tail -n1
}

# ── Determine the target version ────────────────────────────────────────────
if [ -z "$version" ]; then
  shopt -s nullglob
  pkgs=( tachyonikproxy-*.tar.gz tachyonikproxy-*.zip tachyonikproxy-*.rpm tachyonikproxy_*.deb )
  shopt -u nullglob
  version="$(printf '%s\n' "${pkgs[@]}" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | sort -uV | tail -n1 || true)"
  [ -n "$version" ] || die "no TachyonikProxy package files in $(pwd) — run this in the download directory"
  echo "highest version found: $version"
else
  case "$version" in
    ""|*[!0-9.]*|.*|*.|*..*) die "invalid version '$version' (expected numeric dotted-decimal, e.g. 1.0.0)" ;;
  esac
fi

if [ "$dryrun" = 1 ]; then
  echo "Updating 'latest' symlinks -> $version   (dry-run, no changes)"
else
  echo "Updating 'latest' symlinks -> $version"
fi

# ── Update each family ──────────────────────────────────────────────────────
linked=0
skipped=0
for entry in "${ENTRIES[@]}"; do
  link="${entry%%|*}"
  pat="${entry#*|}"; pat="${pat//%V/$version}"
  if target="$(resolve_one "$pat")"; then
    if [ "$dryrun" = 1 ]; then
      echo "  would link  $link -> $target"
    else
      ln -sfn "$target" "$link"          # relative target, atomic replace
      echo "  linked      $link -> $target"
    fi
    linked=$((linked + 1))
  else
    # A family with no artifact for this version is normal (you may not ship
    # every format, e.g. .msi needs a Windows build host). Just note it.
    echo "  skip:       $link (no $version artifact)"
    skipped=$((skipped + 1))
  fi
done

[ "$linked" -gt 0 ] || die "no artifacts found for version $version — nothing to link"

if [ "$skipped" -gt 0 ]; then
  echo "done: $linked link(s) for $version, $skipped family/families skipped (missing)."
else
  echo "done: $linked link(s) for $version."
fi
