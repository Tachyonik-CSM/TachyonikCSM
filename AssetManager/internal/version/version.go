// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package version holds the AssetManager build version.
//
// This is the BUILD version and nothing more: it says which binary is running
// and has no say in what the service stores or how it answers. Nothing
// compares it, and nothing is stamped with it. The database schema has its
// own, separate version — the migration runner's (MaxSupportedSchemaVersion
// in internal/database) — which moves with every migration and is unrelated
// to this one. If a future change gives this value a behavioural meaning, that
// is a decision to make deliberately rather than by wiring it in here.
//
// Resolved from this module's own git tag `assetmanager/X.Y.Z` and injected
// at build time with
//
//	-ldflags "-X tachyonik/assetmanager/internal/version.Version=X.Y.Z"
//
// A var rather than a const because ldflags cannot write a const. There is no
// hardcoded release number: the tag is the single source of truth, and this
// module's version is independent of TachyonikCSM's (WebUI/package.json) and of
// every other module's. The default is what an untagged or un-injected build
// honestly reports.
package version

var Version = "0.0.0-dev"
