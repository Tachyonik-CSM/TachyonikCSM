#!/usr/bin/env bash
# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# check-latest-urls.sh — assert that every `…latest…` file
# install-tachyonikproxy.sh downloads is a link update-latest-symlinks.sh
# actually creates.
#
# These two files describe the same set of names from opposite ends: one
# publishes the links on the download server, the other fetches them on the
# customer's machine. Nothing connected them, and they drifted — the installer
# asked for `tachyonikproxy-latest-linux-amd64.deb` and
# `…-latest-linux-amd64.rpm` while the server published
# `tachyonikproxy_latest_amd64.deb` and `tachyonikproxy-latest.x86_64.rpm`.
# Both package routes 404'd; only the tar.gz names happened to match.
#
# The templates are read out of the installer and expanded here, rather than
# restated: a check that compares a hand-written list against itself passes
# whatever the installer says.
#
# Run from anywhere:  scripts/check-latest-urls.sh

set -euo pipefail

cd "$(dirname "$0")/.."

installer="install-tachyonikproxy.sh"
linker="scripts/update-latest-symlinks.sh"

[ -f "$installer" ] || { echo "not found: $installer" >&2; exit 1; }
[ -f "$linker" ]    || { echo "not found: $linker" >&2; exit 1; }

# Link names the server publishes — the left-hand side of the ENTRIES table.
published="$(sed -n 's/^[[:space:]]*"\([^|]*\)|.*"$/\1/p' "$linker")"
[ -n "$published" ] || { echo "could not read ENTRIES from $linker" >&2; exit 1; }

# Filename templates the installer downloads. Only `_url=` lines: the comments
# deliberately mention the old broken names.
# Anchored on the whole assignment rather than matched loosely: the rpm URL
# embeds quotes of its own — _url="${DOWNLOAD_BASE}/…$(rpm_arch "$_arch").rpm" —
# so a [^"]* match tears the template in half and then silently checks the
# fragment.
templates="$(sed -n 's|^[[:space:]]*_url="${DOWNLOAD_BASE}/\(.*\)"[[:space:]]*$|\1|p' "$installer" \
  | sort -u)"
[ -n "$templates" ] || { echo "no latest-URLs found in $installer" >&2; exit 1; }

# Platforms the installer supports, per detect_os / detect_arch.
oses="linux darwin"
arches="amd64 arm64"

rpm_arch_for() {
  case "$1" in
    amd64) echo x86_64 ;;
    arm64) echo aarch64 ;;
    *)     echo "unknown-arch-$1" ;;
  esac
}

fail=0
checked=0

while IFS= read -r template; do
  [ -n "$template" ] || continue

  # Filtering the template list on "latest" would let a renamed URL vanish from
  # the set instead of failing — the check would pass while testing less. Every
  # download is examined; one that isn't a latest-link needs a human to say
  # whether that is intended.
  case "$template" in
    *latest*) : ;;
    *)
      echo "UNCHECKED: $installer downloads '$template', which is not a latest-link" >&2
      fail=1
      continue
      ;;
  esac

  for os in $oses; do
    for arch in $arches; do
      name="$template"
      name="${name//\$\{_os\}/$os}"
      name="${name//\$\{_arch\}/$arch}"
      name="${name//\$(rpm_arch \"\$_arch\")/$(rpm_arch_for "$arch")}"

      # Anything still holding shell syntax means the installer grew a
      # substitution this script does not know how to expand — which would
      # otherwise make the check silently skip that URL.
      case "$name" in
        *'$'*)
          echo "UNEXPANDED: '$template' contains a substitution this check cannot expand" >&2
          fail=1
          continue
          ;;
      esac

      # A template without $_os expands identically for every os; only count
      # and report it once.
      case "$template" in
        *'${_os}'*) : ;;
        *) [ "$os" = "linux" ] || continue ;;
      esac

      checked=$((checked + 1))
      if ! printf '%s\n' "$published" | grep -qxF "$name"; then
        echo "MISSING: the installer downloads '$name', which $linker never creates" >&2
        fail=1
      fi
    done
  done
done <<EOF
$templates
EOF

if [ "$fail" -ne 0 ]; then
  echo "latest-link check FAILED" >&2
  exit 1
fi

echo "latest-link check passed: $checked installer download name(s), all published"
