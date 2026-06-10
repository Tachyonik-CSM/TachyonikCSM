# Embedded auto-update public keys

Every PEM file in this directory is compiled into the proxy binary via
`go:embed` and used to verify the Ed25519 signature on update manifests
fetched from `auto_update.manifest_url`.

The current files are:

- `tachyonik-test-2026.pem` — **placeholder** generated 2026-05-04. The
  matching private key was discarded at generation time and is **not** held
  by anyone. This file exists so the trust-chain code has a syntactically
  valid key to compile against; it cannot verify any production manifest.

## Replacing for a release

Before tagging a real release:

1. Generate the production keypair on an offline / HSM-backed host:
   ```
   openssl genpkey -algorithm ed25519 -out tachyonik_priv.pem
   openssl pkey -in tachyonik_priv.pem -pubout -out tachyonik_prod.pem
   ```
2. Move `tachyonik_priv.pem` to its long-term offline storage. It must
   never live on a build host, a CI runner, or this repo.
3. Replace the placeholder in this directory with the production public
   PEM, and rebuild.

## Rotation

To rotate, add a second file alongside the existing one (e.g.
`tachyonik-prod-2027.pem`) and rebuild. The proxy accepts a signature from
**any** key it has embedded, so signing the rotation manifest with both keys
during the overlap period lets the fleet pick up the new key without a
window where verification fails.

After all proxies have been updated to the build that includes the new key,
remove the old file in a subsequent release.
