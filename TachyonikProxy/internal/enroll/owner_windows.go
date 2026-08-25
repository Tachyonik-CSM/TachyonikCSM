// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package enroll

import "strconv"

// Windows has no chown, and the MSI (packaging/windows/wix.json) installs
// files only — it registers no service account, so there is no
// enrolled-by-root / run-as-someone-else split to bridge here. targetOwner
// always reports "nothing to do" and the callers skip the ownership work.

// Owner identifies the account new enrollment material should belong to.
type Owner struct {
	UID  int
	GID  int
	Name string
}

// String renders the owner for the enrollment summary.
func (o Owner) String() string {
	if o.Name != "" {
		return o.Name
	}
	return strconv.Itoa(o.UID)
}

func targetOwner(configDir string) (Owner, bool) { return Owner{}, false }

func chownTo(path string, owner Owner) error { return nil }

func verifyOwned(path string, owner Owner) error { return nil }
