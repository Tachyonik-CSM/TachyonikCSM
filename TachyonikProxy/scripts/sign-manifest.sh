#!/usr/bin/env bash
# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# sign-manifest.sh — produce the detached Ed25519 signature for an auto-update
# manifest. Run this OFFLINE on the host that holds the private signing key.
#
# The signature is the raw 64-byte Ed25519 signature over manifest.json's exact
# bytes (no JSON wrapping, no canonicalisation) — the proxy verifies it against
# the embedded public key before it even parses the manifest. See RELEASING.md.
#
# Requires OpenSSL 3.x (the `pkeyutl -rawin` flag, needed for Ed25519).
#
# Usage:
#   scripts/sign-manifest.sh [options] [MANIFEST]
#
#   MANIFEST            Path to the manifest (default: manifest.json)
#
# Options:
#   -k, --key  FILE     Ed25519 private key PEM
#                       (or set env TACHYONIKPROXY_SIGNING_KEY)
#   -p, --pub  FILE     Ed25519 public key PEM — if given, the signature is
#                       verified after signing (strongly recommended)
#   -o, --out  FILE     Signature output path (default: <MANIFEST>.sig)
#   -h, --help          Show this help
#
# Examples:
#   scripts/sign-manifest.sh -k tachyonik_priv.pem -p tachyonik_prod.pem
#   TACHYONIKPROXY_SIGNING_KEY=~/keys/priv.pem scripts/sign-manifest.sh dist/manifest.json

set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
note() { printf '%s\n' "$*" >&2; }

manifest=""
key="${TACHYONIKPROXY_SIGNING_KEY:-}"
pub=""
out=""

while [ $# -gt 0 ]; do
  case "$1" in
    -k|--key) key="${2:-}"; shift 2 ;;
    -p|--pub) pub="${2:-}"; shift 2 ;;
    -o|--out) out="${2:-}"; shift 2 ;;
    -h|--help)
      # Print the contiguous comment block from the description line onward,
      # stripping the leading "# ".
      awk 'NR>=6 && /^#/ {sub(/^# ?/,""); print; next} NR>=6 {exit}' "$0"
      exit 0 ;;
    -*) die "unknown option: $1 (try --help)" ;;
    *)
      [ -z "$manifest" ] || die "unexpected extra argument: $1"
      manifest="$1"; shift ;;
  esac
done

manifest="${manifest:-manifest.json}"
out="${out:-${manifest}.sig}"

# ── Preconditions ───────────────────────────────────────────────────────────
command -v openssl >/dev/null 2>&1 || die "openssl not found on PATH"
[ -f "$manifest" ] || die "manifest not found: $manifest"
[ -s "$manifest" ] || die "manifest is empty: $manifest"
[ -n "$key" ] || die "no private key given (use -k/--key or TACHYONIKPROXY_SIGNING_KEY)"
[ -f "$key" ] || die "private key not found: $key"

# Ed25519 raw signing needs OpenSSL 3.x. LibreSSL / OpenSSL 1.x lack `-rawin`.
ossl_ver="$(openssl version 2>/dev/null || true)"
case "$ossl_ver" in
  "OpenSSL 3."*|"OpenSSL 4."*) : ;;
  *) die "Ed25519 raw signing needs OpenSSL 3.x — found: ${ossl_ver:-unknown}.
       Sign on a host with OpenSSL 3.x, or use a Go-based signer instead." ;;
esac

# ── Pre-flight manifest validation ──────────────────────────────────────────
# Structural checks need jq; the latestVersion format check does NOT — it is
# the most important guard (a 'v' prefix or '-rc1' suffix silently breaks
# self-update's parseVersion), so it always runs via a jq-free fallback.
if command -v jq >/dev/null 2>&1; then
  jq empty "$manifest" 2>/dev/null || die "manifest is not valid JSON: $manifest"

  schema="$(jq -r '.schemaVersion // empty' "$manifest")"
  channel="$(jq -r '.channel // empty' "$manifest")"
  latest="$(jq -r '.latestVersion // empty' "$manifest")"
  published="$(jq -r '.publishedAt // empty' "$manifest")"
  nartifacts="$(jq -r '.artifacts | length' "$manifest" 2>/dev/null || echo 0)"

  [ "$schema" = "1" ] || die "schemaVersion must be 1 (found: '${schema:-missing}')"
  [ -n "$channel" ] || die "manifest missing 'channel'"
  [ -n "$published" ] || die "manifest missing 'publishedAt'"
  [ "${nartifacts:-0}" -ge 1 ] || die "manifest has no 'artifacts'"

  note "pre-flight: schemaVersion=$schema channel=$channel latestVersion=${latest:-?} artifacts=$nartifacts publishedAt=$published"
else
  # Best-effort extraction of just latestVersion without jq.
  latest="$(grep -oE '"latestVersion"[[:space:]]*:[[:space:]]*"[^"]*"' "$manifest" \
            | head -n1 | sed -E 's/.*:[[:space:]]*"([^"]*)".*/\1/')"
  note "note: jq not found — reduced pre-flight (latestVersion format check only)"
fi

# latestVersion must be purely numeric dotted-decimal — matches the proxy's
# parseVersion. Always enforced, with or without jq.
[ -n "$latest" ] || die "could not determine 'latestVersion' in $manifest"
case "$latest" in
  *[!0-9.]*|.*|*.|*..*) die "latestVersion '$latest' must be numeric dotted-decimal (e.g. 1.4.0), got '$latest'" ;;
esac

# ── Sign ────────────────────────────────────────────────────────────────────
openssl pkeyutl -sign -inkey "$key" -rawin -in "$manifest" -out "$out" \
  || die "signing failed (is '$key' an Ed25519 private key?)"

# Ed25519 signatures are exactly 64 bytes; anything else means trouble.
sig_len="$(wc -c < "$out" | tr -d '[:space:]')"
[ "$sig_len" = "64" ] || die "unexpected signature length ${sig_len} (expected 64) — wrote $out"

# ── Verify (if a public key was provided) ────────────────────────────────────
if [ -n "$pub" ]; then
  [ -f "$pub" ] || die "public key not found: $pub"
  openssl pkeyutl -verify -pubin -inkey "$pub" -rawin -in "$manifest" -sigfile "$out" >/dev/null \
    || die "signature did NOT verify against $pub — do not publish"
  note "verified: signature checks out against $pub"
else
  note "note: no public key given (-p/--pub) — signature was NOT verified."
  note "      Strongly recommended: re-run with -p <embedded public key PEM>."
fi

printf 'signed: %s -> %s (64 bytes)\n' "$manifest" "$out"
note "next: upload '$manifest' and '$out' (plus the artifacts) to the manifest_url directory."
