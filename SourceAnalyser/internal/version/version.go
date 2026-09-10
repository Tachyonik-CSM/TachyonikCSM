// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package version holds the SourceAnalyser version constant and the
// version-comparison helpers used to decide when a previously analysed source
// should be re-analysed by a newer build (an "Unsupported" source stamped with
// an older analyser version is a candidate for another pass).
package version

import (
	"strconv"
	"strings"
)

// Version is the current version of SourceAnalyser, resolved from this module's
// own git tag `sourceanalyser/X.Y.Z` and injected at build time with
//
//	-ldflags "-X tachyonik/sourceanalyser/internal/version.Version=X.Y.Z"
//
// A var rather than a const because ldflags cannot write a const. There is no
// hardcoded release number here: the tag is the single source of truth, and
// this module's version is independent of TachyonikCSM's (which lives in
// WebUI/package.json) and of every other module's.
//
// The default is what an untagged or un-injected build honestly reports. It is
// also safe for the analyser_version column this value is stamped into:
// IsOlderThan parses it as 0.0.0, so a development build never re-analyses
// sources left by a real release, while a later release does re-analyse
// anything a development build touched — which is the right way round, since a
// development build's verdict is provisional.
var Version = "0.0.0-dev"

// IsOlderThan compares two semantic version strings
// Returns true if v1 is older than v2
// Returns false if versions are equal or v1 is newer, or if parsing fails
func IsOlderThan(v1, v2 string) bool {
	if v1 == "" || v2 == "" {
		return true // Empty version is considered older
	}

	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	// Compare across the longer of the two, treating a missing component as 0
	// so "1.2" and "1.2.0" compare equal.
	for i := 0; i < max(len(parts1), len(parts2)); i++ {
		var num1, num2 int

		if i < len(parts1) {
			num1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			num2, _ = strconv.Atoi(parts2[i])
		}

		if num1 < num2 {
			return true
		} else if num1 > num2 {
			return false
		}
	}

	return false // Versions are equal
}

// NeedsReanalysis checks if a source with given status, type, and version needs re-analysis
func NeedsReanalysis(status, sourceType, analyserVersion string) bool {
	return status == "Analysed" &&
		sourceType == "Unsupported" &&
		(analyserVersion == "" || IsOlderThan(analyserVersion, Version))
}
