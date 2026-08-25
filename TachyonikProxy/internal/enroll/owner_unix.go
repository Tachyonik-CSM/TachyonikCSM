// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package enroll

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// Enrollment is run by a human with sudo; the proxy is run by a service
// account. Nothing connected those two facts, so `sudo tachyonikproxy enroll`
// created <config-dir>/certs as root:root 0700 and the service — User=
// tachyonikproxy in packaging/systemd/tachyonikproxy.service — could not even
// traverse into it. The failure surfaced as "permission denied" on a
// world-readable client.crt, which reads like a mode problem and is not one.
//
// The package postinstall does chown -R the config directory, but only at
// install time; a directory created later by root is never revisited.

// Owner identifies the account new enrollment material should belong to.
type Owner struct {
	UID  int
	GID  int
	Name string // resolved login name, for operator-facing output; may be a bare uid
}

// String renders the owner for the enrollment summary.
func (o Owner) String() string {
	if o.Name != "" {
		return o.Name
	}
	return strconv.Itoa(o.UID)
}

// targetOwner reports which account enrollment material should belong to,
// derived from configDir.
//
// The *directory* is the source of truth, never config.yaml: both installers
// chown the directory to the service account (packaging/scripts/postinstall.sh,
// install-tachyonikproxy.sh) and neither the enroller nor the daemon ever
// rewrites it, whereas config.yaml is rewritten by every enrollment and by
// remote config pushes. Deriving from the file would mean that on a machine
// already damaged by a root enrollment — config.yaml now root-owned — a second
// enrollment would faithfully reproduce the damage.
//
// ok is false when there is nothing to do: not running as root (a chown would
// fail anyway, and an unprivileged operator enrolling their own XDG config is
// already the owner), or the directory cannot be stat'd.
func targetOwner(configDir string) (Owner, bool) {
	if os.Geteuid() != 0 {
		return Owner{}, false
	}
	info, err := os.Stat(configDir)
	if err != nil {
		return Owner{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Owner{}, false
	}
	owner := Owner{UID: int(st.Uid), GID: int(st.Gid)}
	if u, err := user.LookupId(strconv.Itoa(owner.UID)); err == nil {
		owner.Name = u.Username
	}
	// Root enrolling a root-owned directory is the macOS LaunchDaemon case and
	// the "no service account" case: correct already, nothing to change.
	if owner.UID == 0 && owner.GID == 0 {
		return owner, false
	}
	return owner, true
}

// chownTo gives path to owner. Called only when targetOwner returned ok, so a
// failure here is a real failure — the file exists, we are root, and the
// account was read off a directory that exists.
func chownTo(path string, owner Owner) error {
	if err := os.Chown(path, owner.UID, owner.GID); err != nil {
		return fmt.Errorf("failed to set owner of %s to %s: %w", path, owner, err)
	}
	return nil
}

// verifyOwned re-reads path and confirms it actually belongs to owner.
//
// This is checked rather than assumed because the consequence of a silently
// half-applied chown is the exact restart loop this code exists to prevent: the
// service crashes on every start with a message that points at the wrong thing.
func verifyOwned(path string, owner Owner) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to verify %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(st.Uid) != owner.UID || int(st.Gid) != owner.GID {
		return fmt.Errorf("%s is owned by %d:%d, expected %s (%d:%d) — "+
			"the service will not be able to read it",
			path, st.Uid, st.Gid, owner, owner.UID, owner.GID)
	}
	return nil
}
