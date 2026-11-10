// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hostscan finds which addresses in a private network are alive. It is
// the scanner behind the built-in tool TachyonikHD (Host Detection), for a
// customer who has no security tool that can detect hosts.
//
// Speed comes before depth, and the proxy normally runs without raw-socket
// rights, so the scan uses only what an unprivileged process has:
//
//  1. ICMP echo, where the system allows it. It is the cheapest probe, and a
//     host that answers needs no other.
//  2. TCP connect to a few common ports, for the rest, stopping at the first
//     answer. A refused connection counts: only a live host refuses.
//  3. The operating system's neighbour table (Linux), read at the end. The
//     probes above made the kernel resolve every address on the proxy's own
//     subnet, so a host that drops all of them but answered ARP shows here,
//     and every host found on that subnet gains its MAC address.
//
// It answers "is something there", nothing more: no hostnames, no services.
//
// Targets are limited to the private ranges the local-network sweep is limited
// to (netscan.IsSweepable), and to MaxAddresses per run, so neither a typo nor
// a request from a chat can turn it on somebody else's network.
package hostscan

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tachyonik/tachyonikproxy/internal/netscan"
)

// How the scan is bounded.
const (
	// MaxAddresses is the most addresses one run may cover: a /16.
	MaxAddresses = 1 << 16
	// connectTimeout is how long a TCP probe waits. A silent host costs this
	// once, whatever the number of ports, since a host's ports are probed
	// together.
	connectTimeout = time.Second
	// maxConnections bounds TCP connections in flight at once.
	maxConnections = 1024
	// fdRetries is how often a probe that found no free file descriptor tries
	// again, and fdRetryDelay how long it waits in between: such a failure says
	// nothing about the host.
	fdRetries    = 20
	fdRetryDelay = 50 * time.Millisecond
	// A scan warns when this share of a range of at least falseAlarmMinimum
	// addresses is known only from refused connections. Real networks are not
	// that full; a firewall answering for absent addresses looks exactly so.
	falseAlarmShare   = 0.9
	falseAlarmMinimum = 16
)

// defaultPorts are probed by TCP: web, SSH, and the Windows services.
var defaultPorts = []int{443, 80, 22, 445, 3389}

// How a host was found.
const (
	ViaICMP      = "icmp"
	ViaTCP       = "tcp"
	ViaNeighbour = "neighbour"
)

// Host is one address found alive.
type Host struct {
	IP string `json:"ip"`
	// DetectedBy says which steps saw it: icmp, tcp, neighbour. A host found
	// by ICMP is not probed by TCP.
	DetectedBy []string `json:"detectedBy"`
	// MAC is its hardware address where the neighbour table has one.
	MAC string `json:"mac,omitempty"`

	addr netip.Addr
}

// Result is what a scan found.
type Result struct {
	// Addresses is how many addresses the targets cover.
	Addresses int
	Hosts     []Host
	// Complete is false when the scan ran out of time: hosts may be missing.
	Complete bool
	// ICMPUsed is false when the system allows this process no ICMP socket.
	ICMPUsed bool
	Warnings []string
	Duration time.Duration
}

// ParseTargets reads a list of addresses and CIDR ranges, separated by commas
// or spaces, into the addresses to probe. The network and broadcast addresses
// of a range are left out. Anything but private IPv4, or more addresses than
// MaxAddresses, is refused with an error that says why.
func ParseTargets(target string) ([]netip.Addr, error) {
	fields := strings.FieldsFunc(target, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	if len(fields) == 0 {
		return nil, errors.New("no target given: name an IP address or a CIDR range")
	}

	seen := make(map[netip.Addr]struct{})
	for _, f := range fields {
		first, last, err := parseRange(f)
		if err != nil {
			return nil, err
		}
		for a := first; ; a = a.Next() {
			seen[a] = struct{}{}
			if len(seen) > MaxAddresses {
				return nil, fmt.Errorf("target covers more than %d addresses, the limit for one scan (a /16)", MaxAddresses)
			}
			if a == last {
				break
			}
		}
	}

	addrs := make([]netip.Addr, 0, len(seen))
	for a := range seen {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i].Less(addrs[j]) })
	return addrs, nil
}

// parseRange reads one address or CIDR range into its first and last address
// to probe.
func parseRange(s string) (first, last netip.Addr, err error) {
	if !strings.Contains(s, "/") {
		a, perr := netip.ParseAddr(s)
		if perr != nil {
			return first, last, fmt.Errorf("target %q is not an IPv4 address or a CIDR range; hostnames are not supported", s)
		}
		a = a.Unmap()
		if err := checkAddr(s, a); err != nil {
			return first, last, err
		}
		return a, a, nil
	}

	p, perr := netip.ParsePrefix(s)
	if perr != nil {
		return first, last, fmt.Errorf("target %q is not an IPv4 address or a CIDR range; hostnames are not supported", s)
	}
	p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked()
	if !p.Addr().Is4() {
		return first, last, fmt.Errorf("target %q is IPv6, which is not supported", s)
	}
	// Refused before it is walked: 10.0.0.0/8 alone is 16 million addresses.
	if p.Bits() < 16 {
		return first, last, fmt.Errorf("target %q is wider than a /16, the limit for one scan", s)
	}
	first = p.Addr()
	base := binary.BigEndian.Uint32(first.AsSlice())
	last = addrFromUint32(base | uint32(1<<(32-p.Bits())-1))
	// Both ends: a range may start in a private block and run out of it.
	if err := checkAddr(s, first); err != nil {
		return first, last, err
	}
	if err := checkAddr(s, last); err != nil {
		return first, last, err
	}
	// A /31 and a /32 have no network or broadcast address to leave out.
	if p.Bits() <= 30 {
		first, last = first.Next(), last.Prev()
	}
	return first, last, nil
}

func checkAddr(target string, a netip.Addr) error {
	if !a.Is4() {
		return fmt.Errorf("target %q is IPv6, which is not supported", target)
	}
	if !netscan.IsSweepable(a.AsSlice()) {
		return fmt.Errorf("target %q is not a private address: only 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 and 127.0.0.0/8 may be scanned", target)
	}
	return nil
}

func addrFromUint32(v uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return netip.AddrFrom4(b)
}

// tcpOutcome is what a TCP probe of one address learned.
type tcpOutcome int

const (
	tcpSilent   tcpOutcome = iota // no answer, or the address is unreachable
	tcpRefused                    // something refused the connection
	tcpAccepted                   // something accepted it
)

// scanner holds the steps, so tests can replace what touches the network.
type scanner struct {
	ports      []int
	icmp       func(ctx context.Context, addrs []netip.Addr) (map[netip.Addr]bool, error)
	dial       func(ctx context.Context, addr string) (net.Conn, error)
	neighbours func() map[netip.Addr]string
}

// Scan probes the addresses until done or until ctx ends, and returns what it
// found so far either way.
func Scan(ctx context.Context, addrs []netip.Addr) Result {
	d := net.Dialer{Timeout: connectTimeout}
	s := scanner{
		ports: defaultPorts,
		icmp:  icmpSweep,
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
		neighbours: readNeighbours,
	}
	return s.scan(ctx, addrs)
}

func (s scanner) scan(ctx context.Context, addrs []netip.Addr) Result {
	started := time.Now()
	res := Result{Addresses: len(addrs)}

	found := make(map[netip.Addr]*Host)
	hostFor := func(a netip.Addr) *Host {
		h, ok := found[a]
		if !ok {
			h = &Host{IP: a.String(), addr: a}
			found[a] = h
		}
		return h
	}

	// 1. ICMP.
	alive, err := s.icmp(ctx, addrs)
	if err != nil {
		res.Warnings = append(res.Warnings, "ICMP is not available to this process ("+err.Error()+"), so only TCP was used: a host that answers ping but has no common port open is missed unless the neighbour table shows it.")
	} else {
		res.ICMPUsed = true
	}
	for a := range alive {
		h := hostFor(a)
		h.DetectedBy = append(h.DetectedBy, ViaICMP)
	}

	// 2. TCP, for the rest.
	var rest []netip.Addr
	for _, a := range addrs {
		if !alive[a] {
			rest = append(rest, a)
		}
	}
	outcomes := s.tcpSweep(ctx, rest)
	refusedOnly := 0
	for a, o := range outcomes {
		if o == tcpSilent {
			continue
		}
		h := hostFor(a)
		h.DetectedBy = append(h.DetectedBy, ViaTCP)
		if o == tcpRefused {
			refusedOnly++
		}
	}
	if len(addrs) >= falseAlarmMinimum && float64(refusedOnly) >= falseAlarmShare*float64(len(addrs)) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d of %d addresses are known only from refused connections. A firewall answering for absent addresses looks exactly like this, so these hosts may not exist.", refusedOnly, len(addrs)))
	}

	// 3. The neighbour table, which the probes above just filled.
	wanted := make(map[netip.Addr]bool, len(addrs))
	for _, a := range addrs {
		wanted[a] = true
	}
	for a, mac := range s.neighbours() {
		if !wanted[a] {
			continue
		}
		h := hostFor(a)
		h.DetectedBy = append(h.DetectedBy, ViaNeighbour)
		h.MAC = mac
	}

	res.Hosts = make([]Host, 0, len(found))
	for _, h := range found {
		res.Hosts = append(res.Hosts, *h)
	}
	sort.Slice(res.Hosts, func(i, j int) bool { return res.Hosts[i].addr.Less(res.Hosts[j].addr) })

	res.Complete = ctx.Err() == nil
	if !res.Complete {
		res.Warnings = append(res.Warnings, "The scan ran out of time before covering every address, so hosts may be missing. Raise the tool rule's timeout or scan a smaller range.")
	}
	res.Duration = time.Since(started)
	return res
}

// tcpSweep probes each address on every port, a limited number of addresses at
// a time. Addresses not reached before ctx ended stay out of the answer.
func (s scanner) tcpSweep(ctx context.Context, addrs []netip.Addr) map[netip.Addr]tcpOutcome {
	out := make(map[netip.Addr]tcpOutcome, len(addrs))
	if len(addrs) == 0 || len(s.ports) == 0 {
		return out
	}

	workers := maxConnections / len(s.ports)
	if workers > len(addrs) {
		workers = len(addrs)
	}
	work := make(chan netip.Addr)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range work {
				o := s.probeTCP(ctx, a)
				mu.Lock()
				out[a] = o
				mu.Unlock()
			}
		}()
	}
feed:
	for _, a := range addrs {
		select {
		case work <- a:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	return out
}

// probeTCP tries every port of one address at once and stops at the first
// answer, so a silent host costs one connectTimeout however many ports there
// are. Stopping there means a host can be reported as refusing although another
// of its ports is open; that only matters to the false-alarm warning, which is
// about a share of the whole range.
func (s scanner) probeTCP(ctx context.Context, a netip.Addr) tcpOutcome {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	answers := make(chan tcpOutcome, len(s.ports))
	for _, port := range s.ports {
		go func(port int) {
			answers <- s.probePort(ctx, net.JoinHostPort(a.String(), strconv.Itoa(port)))
		}(port)
	}
	for range s.ports {
		if o := <-answers; o != tcpSilent {
			return o
		}
	}
	return tcpSilent
}

func (s scanner) probePort(ctx context.Context, addr string) tcpOutcome {
	for try := 0; ; try++ {
		conn, err := s.dial(ctx, addr)
		if err == nil {
			conn.Close()
			return tcpAccepted
		}
		if isRefusal(err) {
			return tcpRefused
		}
		if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
			if try < fdRetries && ctx.Err() == nil {
				time.Sleep(fdRetryDelay)
				continue
			}
		}
		return tcpSilent
	}
}

// isRefusal reports whether a dial failed because something at the address said
// no. Windows reports these under its own error numbers, which the syscall
// constants do not match.
func isRefusal(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		const wsaeConnReset, wsaeConnRefused = 10054, 10061
		return errno == wsaeConnReset || errno == wsaeConnRefused
	}
	return false
}
