<!--
SourceImporter
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Releasing SourceImporter

SourceImporter is released on its **own** version line, independent of the
TachyonikCSM application version in `WebUI/package.json` and of every other
module in the monorepo.

It is much shorter than [TachyonikProxy's release process](../TachyonikProxy/RELEASING.md)
because it ships as a container image built from this repository, not as
`.deb` / `.rpm` / `.msi` artifacts with signing keys and a self-update manifest.
What the two share is only how the version is decided.

## 1. Versioning scheme

Tags are namespaced: `sourceimporter/X.Y.Z`. The build resolves the version with

```
git describe --tags --match 'sourceimporter/*' --dirty
```

and strips the prefix, so `sourceimporter/1.0.0` becomes `1.0.0`. `--match` is
what keeps this module's line separate — `tachyonikcsm/*`, `tachyonikproxy/*`
and `sourceanalyser/*` tags are invisible to it.

Off-tag builds report a descriptive version (`1.0.0-3-gabc123`), dirty trees
append `-dirty`, and a tree with no matching tag falls back to `0.0.0-dev`.

## 2. This version does not decide what gets imported

Worth being explicit, because SourceImporter has two things called a version and
only one of them is this one.

Whether a source is imported again is decided by the **import rule's** version:
`aiimporter.GetImporterVersion` composes `AI-<type>-v<rule version>`, that
string is stamped into ResourceManager's `sources.importer_version`, and
`internal/importer` compares it against the rule currently loaded. Those
versions arrive from AIManager with the rules and move when a rule moves.

The build version below is pure identity — it says which binary is running.
Bumping it re-imports nothing, and it must stay that way: tying it to the
comparison would re-import every source on each release.

So unlike SourceAnalyser, whose version *is* data, this tag can be bumped freely
whenever a release is cut.

| Change | Bump |
|---|---|
| Breaking change to configuration or behaviour | major |
| New capability, new source handling | minor |
| Fixes, dependency updates, refactoring | patch |

## 3. Cut the release

```bash
# 1. Everything committed, tests green
cd SourceImporter
make test vet

# 2. Tag the commit that IS the release
git tag sourceimporter/1.0.1

# 3. Confirm the tree builds as that version
make version            # → 1.0.1
make build
./tachyonik-sourceimporter version

# 4. Publish the tag
git push origin sourceimporter/1.0.1
```

Tag the commit containing the change, not one before it: the tag is what the
build reads, so a tag placed early names a build that does not contain the work.

## 4. Build the image

From the repository root, so the version reaches the container:

```bash
make images            # SOURCEIMPORTER_VERSION is derived and passed through
```

The build stage has no git and no `.git` in its context, so the version arrives
as a `VERSION` build arg (`compose.yaml`). Building the Dockerfile directly
without that arg produces an image reporting `0.0.0-dev`.

Verify what shipped:

```bash
docker run --rm tachyonikcsm/sourceimporter:latest version
```

## 5. After release

Nothing to do. There is no update manifest, no signing key, and no rollback
record — the image tag is the whole distribution story.
