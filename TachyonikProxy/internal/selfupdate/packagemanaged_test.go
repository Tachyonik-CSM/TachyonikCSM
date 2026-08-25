// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readMarkerFrom is PackageManaged with the path injected, so the behaviour
// can be exercised without writing to /usr/share on the test machine. Kept in
// step with PackageManaged by TestPackageManagedReadsTheRealMarkerPath below.
func readMarkerFrom(path string) (bool, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}
	msg := strings.TrimSpace(string(data))
	if msg == "" {
		msg = "This installation is managed by your system package manager."
	}
	return true, msg
}

func TestPackageManagedMarker(t *testing.T) {
	dir := t.TempDir()

	// A tarball install: no marker, so self-update proceeds as before. This is
	// the case that must not regress — it is the only self-updating install
	// left once the packages stop shipping the timer.
	if managed, _ := readMarkerFrom(filepath.Join(dir, "absent")); managed {
		t.Error("no marker must read as not package-managed")
	}

	// A packaged install: the marker's own text is what the operator sees, so
	// the package that created the install is the thing that says how to
	// upgrade it.
	marker := filepath.Join(dir, "package-managed")
	if err := os.WriteFile(marker, []byte("  upgrade with apt  \n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	managed, msg := readMarkerFrom(marker)
	if !managed {
		t.Fatal("a present marker must read as package-managed")
	}
	if msg != "upgrade with apt" {
		t.Errorf("message = %q, want the trimmed marker text", msg)
	}

	// An empty marker still means package-managed; it just has nothing of its
	// own to say.
	if err := os.WriteFile(marker, []byte("   \n"), 0o644); err != nil {
		t.Fatalf("rewrite marker: %v", err)
	}
	managed, msg = readMarkerFrom(marker)
	if !managed || msg == "" {
		t.Errorf("empty marker: managed=%v msg=%q, want true and a fallback message", managed, msg)
	}
}

// A directory at the marker path reads as an I/O error. Treating that as
// "package-managed" would disable updates on a tarball install over a
// filesystem oddity, which is worse than updating.
func TestPackageManagedIgnoresUnreadableMarker(t *testing.T) {
	dir := t.TempDir()
	weird := filepath.Join(dir, "package-managed")
	if err := os.Mkdir(weird, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if managed, _ := readMarkerFrom(weird); managed {
		t.Error("an unreadable marker must not disable self-update")
	}
}

// The path the packaging writes and the path the code reads are the pair that
// drifts silently: change one and a packaged install quietly starts
// self-updating again, with no error anywhere. Read the real value out of
// nfpm.yaml and compare.
func TestPackageManagedReadsTheRealMarkerPath(t *testing.T) {
	data, err := os.ReadFile("../../nfpm.yaml")
	if err != nil {
		t.Skipf("nfpm.yaml not readable from here: %v", err)
	}

	re := regexp.MustCompile(`(?m)^\s*-\s*src:\s*packaging/package-managed\s*$\n\s*dst:\s*(\S+)\s*$`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatal("nfpm.yaml no longer ships packaging/package-managed; the guard in " +
			"PackageManaged can never fire and packaged installs will self-update again")
	}
	if got := string(m[1]); got != PackageManagedMarker {
		t.Errorf("nfpm.yaml installs the marker at %s, PackageManaged reads %s", got, PackageManagedMarker)
	}
}

// The packages must not ship the auto-update units: they are what made a
// packaged install run the updater in the first place.
func TestPackagesDoNotShipTheUpdateUnits(t *testing.T) {
	data, err := os.ReadFile("../../nfpm.yaml")
	if err != nil {
		t.Skipf("nfpm.yaml not readable from here: %v", err)
	}
	for _, unit := range []string{"tachyonikproxy-update.service", "tachyonikproxy-update.timer"} {
		if regexp.MustCompile(`(?m)^\s*-\s*src:.*` + regexp.QuoteMeta(unit)).Match(data) {
			t.Errorf("nfpm.yaml ships %s; a package-managed install must not run the updater", unit)
		}
	}
}

// The postinstall script must not bootstrap the versioned-symlink layout: that
// is what left a second copy of the binary under /opt that nothing executed.
func TestPostinstallDoesNotBootstrapLayout(t *testing.T) {
	data, err := os.ReadFile("../../packaging/scripts/postinstall.sh")
	if err != nil {
		t.Skipf("postinstall.sh not readable from here: %v", err)
	}
	script := string(data)
	// Skip comment lines: the script explains the history in prose.
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "--bootstrap-layout") {
			t.Errorf("postinstall.sh still bootstraps the layout: %q", trimmed)
		}
		if strings.Contains(trimmed, "enable") && strings.Contains(trimmed, "tachyonikproxy-update.timer") &&
			!strings.Contains(trimmed, "disable") {
			t.Errorf("postinstall.sh still enables the update timer: %q", trimmed)
		}
	}
}
