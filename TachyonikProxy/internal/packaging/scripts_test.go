// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package packaging

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readScript(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("%s not readable from here: %v", path, err)
	}
	return string(data)
}

// stripComments drops comment lines, because both scripts explain this history
// in prose and a naive substring search would match the explanation instead of
// the code.
func stripComments(script string) string {
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// preremove runs on upgrades as well as removals. Without a guard on the
// argument, every `apt upgrade` / `dnf upgrade` stopped and disabled a healthy
// proxy — on rpm the disable was even the last word, so it did not come back
// after a reboot either.
func TestPreremoveOnlyActsOnARealRemoval(t *testing.T) {
	script := stripComments(readScript(t, "../../packaging/scripts/preremove.sh"))

	if !strings.Contains(script, "systemctl stop tachyonikproxy.service") {
		t.Fatal("preremove.sh no longer stops the service on removal")
	}

	// dpkg passes a word ("remove" / "upgrade"), rpm passes the number of
	// instances that will remain (0 on final erase, 1 on upgrade). A guard that
	// handles only one of them leaves the other packager broken.
	guard := regexp.MustCompile(`case\s+"\$\{?1[^"]*"?\s*in`)
	if !guard.MatchString(script) {
		t.Fatal("preremove.sh does not branch on $1; it will stop and disable the " +
			"service during upgrades as well as removals")
	}
	for _, token := range []string{"remove", "0"} {
		if !regexp.MustCompile(`(?m)^\s*[^#\n]*\b` + regexp.QuoteMeta(token) + `\b[^\n]*\)`).MatchString(script) {
			t.Errorf("preremove.sh's guard has no case for %q — one of dpkg/rpm is unhandled", token)
		}
	}
}

// The other half: an upgrade has to put the service back on the new binary.
// `enable` alone was what shipped, and it is exactly the state that looked
// correct while leaving the proxy down.
func TestPostinstallRestartsAnUpgradedService(t *testing.T) {
	script := stripComments(readScript(t, "../../packaging/scripts/postinstall.sh"))

	if !strings.Contains(script, "systemctl enable tachyonikproxy.service") {
		t.Error("postinstall.sh no longer enables the service; it will not start on boot")
	}

	// try-restart, not start: a fresh install is not enrolled, and an unenrolled
	// proxy exits fatally — starting it would produce a Restart=on-failure loop
	// rather than a working service.
	if !strings.Contains(script, "try-restart tachyonikproxy.service") {
		t.Error("postinstall.sh does not try-restart the service; an upgrade will " +
			"leave the proxy stopped on the old binary")
	}
	if regexp.MustCompile(`(?m)systemctl\s+start\s+tachyonikproxy\.service`).MatchString(script) {
		t.Error("postinstall.sh starts the service unconditionally; on a fresh, " +
			"unenrolled install that is a crash loop, not a start")
	}
}

// `command -v systemctl` finds the binary in containers and chroots that have
// no systemd running, where the calls then fail. /run/systemd/system is the
// standard test for "systemd is actually the init system here".
func TestScriptsDetectSystemdByRuntimeDirectory(t *testing.T) {
	for _, path := range []string{
		"../../packaging/scripts/postinstall.sh",
		"../../packaging/scripts/preremove.sh",
	} {
		script := stripComments(readScript(t, path))
		if strings.Contains(script, "command -v systemctl") {
			t.Errorf("%s gates on `command -v systemctl`, which is true in containers "+
				"and chroots with no running systemd", path)
		}
		if !strings.Contains(script, "/run/systemd/system") {
			t.Errorf("%s has no /run/systemd/system check", path)
		}
	}
}

// enable only does anything if the unit says where it wants to be installed.
// Without this section `systemctl enable` fails and nothing starts at boot,
// which no amount of correct scripting would fix.
func TestUnitIsInstallableIntoATarget(t *testing.T) {
	unit := readScript(t, "../../packaging/systemd/tachyonikproxy.service")
	if !regexp.MustCompile(`(?m)^\[Install\]`).MatchString(unit) {
		t.Fatal("the unit has no [Install] section; systemctl enable cannot work")
	}
	if !regexp.MustCompile(`(?m)^WantedBy=\S+`).MatchString(unit) {
		t.Error("the unit's [Install] section has no WantedBy=; it will not start on boot")
	}
}
