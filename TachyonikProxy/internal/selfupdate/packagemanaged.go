// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import (
	"os"
	"strings"
)

// Self-update is for the standalone (.tar.gz) install only.
//
// A .deb or .rpm install belongs to dpkg/rpm: they record every file they own,
// so a self-updater writing over one of them leaves the package database
// claiming a version that is no longer on disk, and the next `apt upgrade`
// silently reverts it. Two things owning one path is the problem — not merely
// a matter of taste.
//
// It also did not work. The packages install the binary at /usr/bin (see
// nfpm.yaml) and both units execute that path, while the updater only ever
// writes VersionRoot/<version> and VersionRoot/current (apply.go). A packaged
// install therefore downloaded, verified, unpacked and reported success while
// the service carried on running the old binary — and repeated it on every
// timer tick, because the version it compares against is compiled into the
// binary that is still running.

// PackageManagedMarker is written by the .deb / .rpm packages and by nothing
// else. Being package-owned is the whole point: dpkg and rpm create it on
// install and remove it on uninstall, so its presence is true by construction
// rather than inferred from where the binary happens to sit. A tarball install
// never has it.
//
// It lives beside config.yaml.default, which the packages already own.
const PackageManagedMarker = "/usr/share/tachyonik/tachyonikproxy/package-managed"

// PackageManaged reports whether this install belongs to a system package
// manager, along with the marker's own text to show the operator.
//
// The message comes from the marker rather than from here so the package that
// created the install is the thing that says how to upgrade it — a .deb should
// name apt, an .rpm dnf, without this code having to guess which.
func PackageManaged() (bool, string) {
	data, err := os.ReadFile(PackageManagedMarker)
	if err != nil {
		// Absent is the normal case for a tarball install. Any other error
		// (permissions, I/O) is treated the same way: refusing to update
		// because a file could not be read would be worse than updating.
		return false, ""
	}
	msg := strings.TrimSpace(string(data))
	if msg == "" {
		msg = "This installation is managed by your system package manager."
	}
	return true, msg
}
