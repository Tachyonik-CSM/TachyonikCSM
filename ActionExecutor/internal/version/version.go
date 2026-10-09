// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package version holds the ActionExecutor build version.
//
// This is the BUILD version and nothing more: it says which binary is running
// and has no say in what is generated or run. Nothing compares it, and nothing
// is stamped with it — not the run results reported to AIManager, not the
// audit entries. If a future change gives this value a behavioural meaning,
// that is a decision to make deliberately rather than by wiring it in here.
//
// A generated routine carries its own, separate version, stored in AIManager
// beside the routine with the model that wrote it and its checksum. That one
// moves when a rule moves and is unrelated to this constant.
//
// Resolved from this module's own git tag `actionexecutor/X.Y.Z` and injected
// at build time with
//
//	-ldflags "-X tachyonik/actionexecutor/internal/version.Version=X.Y.Z"
//
// A var rather than a const because ldflags cannot write a const. There is no
// hardcoded release number: the tag is the single source of truth, and this
// module's version is independent of TachyonikCSM's (WebUI/package.json) and of
// every other module's. The default is what an untagged or un-injected build
// honestly reports.
package version

var Version = "0.0.0-dev"
