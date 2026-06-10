# Embedded auto-update public keys

Every PEM file in this directory is compiled into the proxy binary via
`go:embed` and used to verify the Ed25519 signature on update manifests
fetched from `auto_update.manifest_url`.

The current files are:

- `tachyonik-prod-2026.pem` — the **production** public key. The matching
  private key is held offline (see RELEASING.md §0.1) and is used to sign
  release manifests; it must never live on a build host, a CI runner, or this
  repo.

## Replacing the key

When generating a new production key (initial setup or a from-scratch reissue):

1. Generate the production keypair on an offline / HSM-backed host:
   ```
   openssl genpkey -algorithm ed25519 -out tachyonik_priv.pem
   openssl pkey -in tachyonik_priv.pem -pubout -out tachyonik_prod.pem
   ```
2. Move `tachyonik_priv.pem` to its long-term offline storage. It must
   never live on a build host, a CI runner, or this repo.
3. Place the production public PEM in this directory (replacing any prior
   key you are retiring), and rebuild.

## Rotation

To rotate, add a second file alongside the existing one (e.g.
`tachyonik-prod-2027.pem`) and rebuild. The proxy accepts a signature from
**any** key it has embedded, so signing the rotation manifest with both keys
during the overlap period lets the fleet pick up the new key without a
window where verification fails.

After all proxies have been updated to the build that includes the new key,
remove the old file in a subsequent release.
