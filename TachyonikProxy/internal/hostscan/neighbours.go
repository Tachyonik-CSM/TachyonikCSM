// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Reads the Linux neighbour table, the scan's last step. Elsewhere the file does
// not exist and the step finds nothing. An entry can outlive its host by a short
// while, so a host that left moments ago may still be listed.

package hostscan

import (
	"net/netip"
	"os"
	"strconv"
	"strings"
)

const (
	neighbourTable = "/proc/net/arp"
	// arpComplete is ATF_COM: the entry holds a resolved hardware address, not
	// a request that went unanswered.
	arpComplete = 0x2
)

func readNeighbours() map[netip.Addr]string {
	data, err := os.ReadFile(neighbourTable)
	if err != nil {
		return nil
	}
	return parseNeighbours(string(data))
}

// parseNeighbours reads the table's lines, after a header line:
//
//	IP address       HW type     Flags       HW address            Mask     Device
//	192.168.178.1    0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0
func parseNeighbours(table string) map[netip.Addr]string {
	out := make(map[netip.Addr]string)
	lines := strings.Split(table, "\n")
	if len(lines) > 0 {
		lines = lines[1:]
	}
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimPrefix(f[2], "0x"), 16, 32)
		if err != nil || flags&arpComplete == 0 || f[3] == "00:00:00:00:00:00" {
			continue
		}
		a, err := netip.ParseAddr(f[0])
		if err != nil {
			continue
		}
		out[a] = f[3]
	}
	return out
}
