// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux && !darwin

// Other systems have no unprivileged ICMP socket; the scan tries a raw one.

package hostscan

import "errors"

func openDgramICMP() (*icmpConn, error) {
	return nil, errors.New("no unprivileged ICMP socket on this system")
}
