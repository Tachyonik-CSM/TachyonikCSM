// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package packaging holds no code. It exists so `go test ./...` carries a few
// assertions about the .deb / .rpm maintainer scripts, which are shell and
// therefore invisible to the compiler.
//
// The behavioural check lives in scripts/check-package-lifecycle.sh, which
// drives real dpkg and rpm transactions. These tests are the cheap half: they
// catch a guard being deleted without needing docker, so they can run anywhere.
package packaging
