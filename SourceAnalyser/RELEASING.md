<!--
SourceAnalyser
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Releasing SourceAnalyser

SourceAnalyser is released on its **own** version line, independent of the
TachyonikCSM application version in `WebUI/package.json` and of every other
module in the monorepo.

It is much shorter than [TachyonikProxy's release process](../TachyonikProxy/RELEASING.md)
because it ships as a container image built from this repository, not as
`.deb` / `.rpm` / `.msi` artifacts with signing keys and a self-update manifest.
What the two share is only how the version is decided.

## 1. Versioning scheme

Tags are namespaced: `sourceanalyser/X.Y.Z`. The build resolves the version with

```
git describe --tags --match 'sourceanalyser/*' --dirty
```

and strips the prefix, so `sourceanalyser/1.1.2` becomes `1.1.2`. `--match` is
what keeps this module's line separate — `tachyonikcsm/*` and
`tachyonikproxy/*` tags are invisible to it.

Off-tag builds report a descriptive version (`1.1.2-3-gabc123`), dirty trees
append `-dirty`, and a tree with no matching tag falls back to `0.0.0-dev`.
None of those are release versions; only a clean checkout of a tag produces a
plain `X.Y.Z`.

## 2. When to bump, and which component

The version is not only an identity — it is written into ResourceManager's
`sources.analyser_version` and drives re-analysis. `NeedsReanalysis` re-runs a
source that is *Analysed* + *Unsupported* when the version that last handled it
is older than the running build.

So the question is not "is this user-visible" but **"could the analyser now
reach a different verdict?"**

| Change | Bump |
|---|---|
| New source type detected, or detection widened | minor — those sources must be revisited |
| Detection bug fixed, changing a verdict | patch — and the affected sources are revisited |
| A break in how analysis results are interpreted | major |
| Logging, config, refactoring with no verdict change | patch, or none |

Under-bumping is the failure that costs something: sources marked *Unsupported*
by the old logic are never looked at again.

## 3. Cut the release

```bash
# 1. Everything committed, tests green
cd SourceAnalyser
make test vet

# 2. Tag the commit that IS the release
git tag sourceanalyser/1.1.2

# 3. Confirm the tree builds as that version
make version            # → 1.1.2
make build
./tachyonik-sourceanalyser version

# 4. Publish the tag
git push origin sourceanalyser/1.1.2
```

Tag the commit containing the change, not one before it: the tag is what the
build reads, so a tag placed early names a build that does not contain the work.

## 4. Build the image

From the repository root, so the version reaches the container:

```bash
make images            # SOURCEANALYSER_VERSION is derived and passed through
```

The build stage has no git and no `.git` in its context, so the version arrives
as a `VERSION` build arg (`compose.yaml`). Building the Dockerfile directly
without that arg produces an image reporting `0.0.0-dev`.

Verify what shipped:

```bash
docker run --rm tachyonikcsm/sourceanalyser:latest version
```

## 5. After release

Nothing to do. There is no update manifest, no signing key, and no rollback
record — the image tag is the whole distribution story. The next release starts
by tagging again.
