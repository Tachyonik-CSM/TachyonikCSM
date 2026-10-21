// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Reading a target's host list and taking its excluded hosts out of it.
//
// A GMP target names its hosts as one comma-separated string mixing single
// addresses, CIDR networks, address ranges — in full ("10.0.0.1-10.0.0.9") or
// with only the last IPv4 octet ("10.0.0.1-9") — and hostnames. The coverage
// table wants one target per row, in as few forms as possible: an address, a
// CIDR network, a full range, or a hostname.

package scancoverage

import (
	"net/netip"
	"strconv"
	"strings"
)

// hostSpec is one entry of a host list: an address span, or a hostname.
type hostSpec struct {
	lo, hi netip.Addr // the span, for an address, network or range
	// prefix is the network the span was given as, when it was; a span cut
	// by an exclusion is written as a range instead.
	prefix netip.Prefix
	name   string // the hostname, lowercased, when not an address
}

func (h hostSpec) isAddr() bool { return h.lo.IsValid() }

// String writes the spec in the table's forms.
func (h hostSpec) String() string {
	switch {
	case !h.isAddr():
		return h.name
	case h.lo == h.hi:
		return h.lo.String()
	case h.prefix.IsValid() && h.prefix.Addr() == h.lo && lastAddr(h.prefix) == h.hi:
		return h.prefix.String()
	default:
		return h.lo.String() + "-" + h.hi.String()
	}
}

// parseHostList splits a GMP host list. Entries that are neither an address
// form nor a plausible hostname come back in invalid rather than being
// guessed at.
func parseHostList(list string) (specs []hostSpec, invalid []string) {
	for _, raw := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if spec, ok := parseHost(entry); ok {
			specs = append(specs, spec)
		} else {
			invalid = append(invalid, entry)
		}
	}
	return specs, invalid
}

func parseHost(entry string) (hostSpec, bool) {
	if strings.Contains(entry, "/") {
		p, err := netip.ParsePrefix(entry)
		if err != nil {
			return hostSpec{}, false
		}
		p = p.Masked()
		return hostSpec{lo: p.Addr(), hi: lastAddr(p), prefix: p}, true
	}
	if a, err := netip.ParseAddr(entry); err == nil {
		a = a.WithZone("")
		return hostSpec{lo: a, hi: a}, true
	}
	// A dash is a range only when what precedes it is an address — hostnames
	// contain dashes too.
	if i := strings.Index(entry, "-"); i > 0 {
		if lo, err := netip.ParseAddr(strings.TrimSpace(entry[:i])); err == nil {
			rest := strings.TrimSpace(entry[i+1:])
			hi, err := netip.ParseAddr(rest)
			if err != nil && lo.Is4() {
				// The short form: only the last octet of the end.
				if n, nerr := strconv.Atoi(rest); nerr == nil && n >= 0 && n <= 255 {
					b := lo.As4()
					b[3] = byte(n)
					hi, err = netip.AddrFrom4(b), nil
				}
			}
			if err != nil || hi.Is4() != lo.Is4() || hi.Less(lo) {
				return hostSpec{}, false
			}
			return hostSpec{lo: lo, hi: hi}, true
		}
	}
	if isHostname(entry) {
		return hostSpec{name: strings.ToLower(entry)}, true
	}
	return hostSpec{}, false
}

// isHostname accepts the characters a DNS name is made of. Anything else —
// a "|" above all, which would break the table — is refused.
func isHostname(s string) bool {
	if len(s) > 253 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
		default:
			return false
		}
	}
	return true
}

// lastAddr is the highest address of a network.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().As16()
	bits := p.Bits()
	if p.Addr().Is4() {
		bits += 96
	}
	for i := bits; i < 128; i++ {
		b[i/8] |= 1 << (7 - uint(i%8))
	}
	a := netip.AddrFrom16(b)
	if p.Addr().Is4() {
		a = a.Unmap()
	}
	return a
}

// subtract takes the excluded hosts out of the included ones. An address span
// loses every excluded address in it and may fall apart into several; a
// hostname goes when the same name is excluded. Excluded hostnames cannot be
// taken out of an address span — which addresses they stand for is not known
// here — and are left alone.
func subtract(include, exclude []hostSpec) []hostSpec {
	var out []hostSpec
	for _, inc := range include {
		if !inc.isAddr() {
			excluded := false
			for _, ex := range exclude {
				if !ex.isAddr() && ex.name == inc.name {
					excluded = true
					break
				}
			}
			if !excluded {
				out = append(out, inc)
			}
			continue
		}
		pieces := []hostSpec{inc}
		for _, ex := range exclude {
			if !ex.isAddr() || ex.lo.Is4() != inc.lo.Is4() {
				continue
			}
			var next []hostSpec
			for _, p := range pieces {
				next = append(next, cut(p, ex.lo, ex.hi)...)
			}
			pieces = next
		}
		out = append(out, pieces...)
	}
	return out
}

// cut removes lo..hi from the span p.
func cut(p hostSpec, lo, hi netip.Addr) []hostSpec {
	if hi.Less(p.lo) || p.hi.Less(lo) {
		return []hostSpec{p}
	}
	var out []hostSpec
	if p.lo.Less(lo) {
		out = append(out, hostSpec{lo: p.lo, hi: lo.Prev()})
	}
	if hi.Less(p.hi) {
		out = append(out, hostSpec{lo: hi.Next(), hi: p.hi})
	}
	return out
}
