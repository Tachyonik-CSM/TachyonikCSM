// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netinfo answers "what are this host's own addresses?" — the question
// the proxy has to answer about itself twice: once at enrollment, when the
// platform records where the proxy lives and what names its certificate must
// cover, and again on every inbound reconnect, when it reports the address the
// Proxies list shows.
//
// It exists so those two answers cannot drift apart. Only interfaces that are
// up are considered: an address on a downed interface is not one anybody can
// reach, and reporting it puts a plausible, wrong IP in front of an operator.
package netinfo

import "net"

// LocalIPs returns every non-loopback address bound to an interface that is
// up, in the order the operating system lists them.
//
// Used for certificate SANs at enrollment, where breadth is the point: a
// missing address means a TLS name mismatch later.
func LocalIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() {
				ips = append(ips, ip.String())
			}
		}
	}
	return ips
}

// PrimaryIPv4 returns the address to report as "this proxy's IP", or "" when
// the host has none.
//
// IPv4 wins over IPv6: it is the family the local-network sweep works in, the
// one an asset in the topology view is matched against, and the one an operator
// recognises on sight. On a multi-homed host "primary" is operator-defined and
// this picks the first the OS offers — a deterministic guess, not a discovery.
// An operator who disagrees pins the sweep with netscan.network; the reported
// address is a diagnostic, and nothing is routed on the strength of it.
func PrimaryIPv4() string {
	var fallback string
	for _, s := range LocalIPs() {
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			return s
		}
		if fallback == "" {
			fallback = s
		}
	}
	return fallback
}
