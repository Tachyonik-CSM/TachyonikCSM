// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the consequences of resolving Version from a git tag rather than
// from a hardcoded constant.
//
// The version is data, not a banner: it is written into ResourceManager's
// sources.analyser_version for every source the daemon analyses, and
// NeedsReanalysis compares a source's stored value against the running build to
// decide whether an "Unsupported" verdict deserves another pass. So the strings
// `git describe` can produce — a clean tag, a build some commits past one, a
// dirty tree, and the 0.0.0-dev fallback of an un-injected build — all have to
// order sensibly against each other, not merely parse.
//
// What these pin is the direction of that ordering: a development build must
// never overrule a release's verdict, while a release must always revisit what
// a development build touched, because a development build's verdict is
// provisional. Getting that backwards would either strand sources as
// permanently Unsupported or re-analyse the whole corpus on every dev run, and
// neither would be visible from the version string itself.

package version

import "testing"

func TestDevFallbackOrdersCorrectlyAgainstReleases(t *testing.T) {
	const dev = "0.0.0-dev"

	// A development build must not re-analyse what a real release settled: its
	// verdict is not an improvement on 1.1.1.
	if IsOlderThan("1.1.1", dev) {
		t.Error("a release version compared older than a dev build")
	}

	// A later release must re-analyse what a development build touched, since
	// that verdict was provisional.
	if !IsOlderThan(dev, "1.1.1") {
		t.Error("a dev build compared newer than or equal to a release")
	}

	// The suffix must not be read as a version component: 0.0.0-dev is 0.0.0,
	// not something larger.
	if IsOlderThan("0.0.1", dev) {
		t.Errorf("%q parsed as newer than 0.0.1", dev)
	}
}

// git describe returns these off a tag; they must still order sensibly, since
// any build that is not a clean checkout of a tag produces one.
func TestDescribeSuffixesOrderSensibly(t *testing.T) {
	// Commits past a tag are not yet the next release, so they must not out-rank
	// the tag they follow.
	if IsOlderThan("1.1.1", "1.1.1-3-gabc123") {
		t.Error("1.1.1 compared older than a build 3 commits past it")
	}
	// A dirty tree is likewise not the release it sits on.
	if !IsOlderThan("1.1.1-dirty", "1.1.1") {
		t.Error("a dirty build compared equal to or newer than the clean tag")
	}
	// And a genuinely later release still wins.
	if !IsOlderThan("1.1.1-3-gabc123", "1.2.0") {
		t.Error("an off-tag 1.1.1 build compared newer than 1.2.0")
	}
}

// NeedsReanalysis is the only consumer that matters; check it end to end
// rather than only its helper.
func TestNeedsReanalysisAcrossBuildKinds(t *testing.T) {
	saved := Version
	t.Cleanup(func() { Version = saved })

	Version = "1.2.0"
	if !NeedsReanalysis("Analysed", "Unsupported", "1.1.1") {
		t.Error("a newer release did not pick up a source left by an older one")
	}
	if !NeedsReanalysis("Analysed", "Unsupported", "0.0.0-dev") {
		t.Error("a release did not pick up a source left by a dev build")
	}
	if NeedsReanalysis("Analysed", "Unsupported", "1.2.0") {
		t.Error("a source analysed by this very build was re-analysed")
	}

	Version = "0.0.0-dev"
	if NeedsReanalysis("Analysed", "Unsupported", "1.1.1") {
		t.Error("a dev build re-analysed a source settled by a release")
	}

	// Only Unsupported sources are ever revisited; a recognised type is done.
	Version = "9.9.9"
	if NeedsReanalysis("Analysed", "Nmap XML Report", "1.0.0") {
		t.Error("a recognised source type was queued for re-analysis")
	}
}
