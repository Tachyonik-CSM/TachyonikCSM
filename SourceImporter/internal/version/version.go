// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package version holds the SourceImporter build version.
//
// This is the BUILD version and nothing more — it identifies which binary is
// running and has no say in what gets imported or re-imported.
//
// That distinction is worth stating because SourceImporter has a second, quite
// separate notion of "importer version" which does drive behaviour:
// aiimporter.GetImporterVersion composes one per import RULE
// ("AI-<type>-v<rule version>"), that string is stamped into ResourceManager's
// sources.importer_version, and importer.go compares it to decide whether a
// source should be imported again. Those versions come from AIManager with the
// rules, move when a rule moves, and are unrelated to this constant. Wiring the
// build version into that comparison would re-import the whole corpus on every
// release; keeping the two apart is deliberate.
//
// Resolved from this module's own git tag `sourceimporter/X.Y.Z` and injected
// at build time with
//
//	-ldflags "-X tachyonik/sourceimporter/internal/version.Version=X.Y.Z"
//
// A var rather than a const because ldflags cannot write a const. There is no
// hardcoded release number: the tag is the single source of truth, and this
// module's version is independent of TachyonikCSM's (WebUI/package.json) and of
// every other module's. The default is what an untagged or un-injected build
// honestly reports.
package version

var Version = "0.0.0-dev"
