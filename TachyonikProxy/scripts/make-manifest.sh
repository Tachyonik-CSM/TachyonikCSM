#!/usr/bin/env bash
# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# make-manifest.sh — generate the auto-update manifest.json by hashing the
# built release archives. Run this in the BUILD area (where dist/*.tar.gz
# live), right after `make package-archives`. It does NOT contact the download
# server — it hashes the local files and writes the URLs from --base-url.
#
# Pipeline:  make package-archives  →  make-manifest.sh  →  sign-manifest.sh  →  upload
#
# The four self-update platforms (linux/macOS × amd64/arm64) are required;
# Windows is intentionally excluded (it upgrades via .msi, not self-update).
#
# Usage:
#   scripts/make-manifest.sh -v X.Y.Z -u BASE_URL [options]
#
# Options:
#   -v, --version  X.Y.Z   Release version (numeric dotted-decimal). Required.
#   -u, --base-url URL     https:// prefix where the artifacts are hosted,
#                          e.g. https://tachyonik.com/download/proxy/  Required.
#   -c, --channel  NAME    Update channel (default: stable)
#   -d, --dist     DIR     Directory holding the .tar.gz (default: dist)
#   -o, --out      FILE    Output path (default: <dist>/manifest.json)
#       --published-at TS  RFC3339 UTC timestamp (default: now)
#   -h, --help             Show this help
#
# Example:
#   make package-archives VERSION=1.0.0
#   scripts/make-manifest.sh -v 1.0.0 -u https://tachyonik.com/download/proxy/
#   scripts/sign-manifest.sh -k <offline-priv.pem> \
#       -p internal/selfupdate/pubkeys/tachyonik-prod-2026.pem dist/manifest.json

set -euo pipefail

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
note() { printf '%s\n' "$*" >&2; }

version=""
baseurl=""
channel="stable"
dist="dist"
out=""
published=""

while [ $# -gt 0 ]; do
  case "$1" in
    -v|--version)      version="${2:-}"; shift 2 ;;
    -u|--base-url)     baseurl="${2:-}"; shift 2 ;;
    -c|--channel)      channel="${2:-}"; shift 2 ;;
    -d|--dist)         dist="${2:-}"; shift 2 ;;
    -o|--out)          out="${2:-}"; shift 2 ;;
    --published-at)    published="${2:-}"; shift 2 ;;
    -h|--help)
      awk 'NR>=6 && /^#/ {sub(/^# ?/,""); print; next} NR>=6 {exit}' "$0"
      exit 0 ;;
    -*) die "unknown option: $1 (try --help)" ;;
    *)  die "unexpected argument: $1" ;;
  esac
done

# ── Validate inputs ─────────────────────────────────────────────────────────
[ -n "$version" ] || die "missing --version (numeric x.y.z)"
case "$version" in
  *[!0-9.]*|.*|*.|*..*) die "version '$version' must be numeric dotted-decimal (e.g. 1.0.0)" ;;
esac
[ -n "$baseurl" ] || die "missing --base-url (https:// prefix where artifacts are hosted)"
case "$baseurl" in
  https://*) : ;;
  *) die "base URL must be https://, got '$baseurl'" ;;
esac
case "$channel" in
  ""|*[!A-Za-z0-9._-]*) die "channel '$channel' must be non-empty and use only [A-Za-z0-9._-]" ;;
esac
[ -d "$dist" ] || die "dist directory not found: $dist"
out="${out:-$dist/manifest.json}"
baseurl="${baseurl%/}"   # normalise: drop trailing slash, we add one per file

if [ -z "$published" ]; then
  published="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
fi

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "need sha256sum or shasum on PATH"
  fi
}

# ── Hash each required artifact and build the artifacts entries ─────────────
PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
artifacts=""
first=1
note "Hashing artifacts in $dist/ for version $version:"
for p in $PLATFORMS; do
  goos="${p%/*}"; goarch="${p#*/}"
  fn="tachyonikproxy-${version}-${goos}-${goarch}.tar.gz"
  path="$dist/$fn"
  [ -f "$path" ] || die "missing artifact: $path
       build it first:  make package-archives VERSION=$version"
  sha="$(sha256_of "$path")"
  url="$baseurl/$fn"
  entry=$(printf '    "%s": { "url": "%s", "sha256": "%s" }' "$p" "$url" "$sha")
  if [ "$first" = 1 ]; then artifacts="$entry"; first=0; else artifacts="$artifacts,
$entry"; fi
  note "  $p  $sha  ($fn)"
done

# ── Emit manifest.json ──────────────────────────────────────────────────────
cat > "$out" <<EOF
{
  "schemaVersion": 1,
  "channel": "$channel",
  "latestVersion": "$version",
  "publishedAt": "$published",
  "artifacts": {
$artifacts
  }
}
EOF

# Validate it's well-formed JSON if a parser is available (best effort).
if command -v jq >/dev/null 2>&1; then
  jq empty "$out" || die "generated manifest is not valid JSON (this is a bug): $out"
elif command -v python3 >/dev/null 2>&1; then
  python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$out" \
    || die "generated manifest is not valid JSON (this is a bug): $out"
fi

printf 'wrote %s (version %s, channel %s, publishedAt %s)\n' "$out" "$version" "$channel" "$published"
note "next: sign it offline, then upload the manifest + .sig + the four .tar.gz:"
note "  scripts/sign-manifest.sh -k <offline-priv.pem> -p internal/selfupdate/pubkeys/tachyonik-prod-2026.pem $out"
