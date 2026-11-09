// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests which targets a scan accepts, how its steps combine into a result, and
// the probes themselves against real sockets on the loopback range: a listener
// that accepts, an address that refuses, and an ICMP echo where the system
// allows one.

package hostscan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestParseTargets(t *testing.T) {
	cases := []struct {
		target      string
		count       int
		first, last string
	}{
		{"192.168.178.5", 1, "192.168.178.5", "192.168.178.5"},
		{"192.168.178.0/24", 254, "192.168.178.1", "192.168.178.254"},
		{"192.168.178.77/24", 254, "192.168.178.1", "192.168.178.254"}, // host bits ignored, as Nmap does
		{"10.0.0.0/31", 2, "10.0.0.0", "10.0.0.1"},
		{"10.0.0.8/32", 1, "10.0.0.8", "10.0.0.8"},
		{"10.0.0.0/30", 2, "10.0.0.1", "10.0.0.2"},
		// What execution rule "Detect host assets" sends: a comma-separated list.
		{"192.168.1.0/24,192.168.2.0/24", 508, "192.168.1.1", "192.168.2.254"},
		{"192.168.1.1, 192.168.1.1 192.168.1.2", 2, "192.168.1.1", "192.168.1.2"},
		{"172.16.0.0/16", 65534, "172.16.0.1", "172.16.255.254"},
		{"127.0.0.1", 1, "127.0.0.1", "127.0.0.1"},
	}
	for _, c := range cases {
		got, err := ParseTargets(c.target)
		if err != nil {
			t.Errorf("%q: %v", c.target, err)
			continue
		}
		if len(got) != c.count || got[0].String() != c.first || got[len(got)-1].String() != c.last {
			t.Errorf("%q: %d addresses %s..%s, want %d %s..%s", c.target, len(got), got[0], got[len(got)-1], c.count, c.first, c.last)
		}
	}
}

func TestParseTargetsRefuses(t *testing.T) {
	cases := []struct{ target, want string }{
		{"", "no target"},
		{" , ", "no target"},
		{"8.8.8.8", "not a private"},
		{"8.8.8.0/24", "not a private"},
		{"192.168.1.1,8.8.8.8", "not a private"},
		{"169.254.1.1", "not a private"},
		{"172.15.255.0/24", "not a private"},
		// A range that starts in a private block and runs out of it.
		{"172.31.0.0/15", "wider than"},
		{"10.0.0.0/8", "wider than"},
		{"127.0.0.0/8", "wider than"},
		{"scanme.example.org", "hostnames"},
		{"fd00::1", "IPv6"},
		{"fd00::/64", "IPv6"},
		{"192.168.1.0/33", "not an IPv4"},
		{"10.0.0.0/16,10.1.0.0/16", "more than"},
	}
	for _, c := range cases {
		_, err := ParseTargets(c.target)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want it to say %q", c.target, err, c.want)
		}
	}
}

func addrs(t *testing.T, target string) []netip.Addr {
	t.Helper()
	a, err := ParseTargets(target)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// fakeDial answers by address: accept, refuse, or silence until cancelled.
func fakeDial(answers map[string]error) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		err, ok := answers[host]
		if !ok {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
}

func noICMP(context.Context, []netip.Addr) (map[netip.Addr]bool, error) {
	return nil, errors.New("not allowed")
}

func TestScanCombinesTheSteps(t *testing.T) {
	icmpHost := netip.MustParseAddr("10.0.0.1")
	var tcpProbedICMPHost atomic.Bool
	dial := fakeDial(map[string]error{"10.0.0.2": nil, "10.0.0.3": syscall.ECONNREFUSED, "10.0.0.5": syscall.EHOSTUNREACH})
	s := scanner{
		ports: []int{443},
		icmp: func(context.Context, []netip.Addr) (map[netip.Addr]bool, error) {
			return map[netip.Addr]bool{icmpHost: true}, nil
		},
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "10.0.0.1:") {
				tcpProbedICMPHost.Store(true)
			}
			ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			return dial(ctx, addr)
		},
		neighbours: func() map[netip.Addr]string {
			return map[netip.Addr]string{
				icmpHost:                         "aa:aa:aa:aa:aa:01",
				netip.MustParseAddr("10.0.0.4"):  "aa:aa:aa:aa:aa:04",
				netip.MustParseAddr("192.0.2.9"): "aa:aa:aa:aa:aa:09", // outside the targets
			}
		},
	}
	res := s.scan(context.Background(), addrs(t, "10.0.0.0/29"))

	if res.Addresses != 6 || !res.Complete || !res.ICMPUsed || len(res.Warnings) != 0 {
		t.Errorf("result = %+v", res)
	}
	got := map[string]string{}
	for _, h := range res.Hosts {
		got[h.IP] = strings.Join(h.DetectedBy, "+") + " " + h.MAC
	}
	want := map[string]string{
		"10.0.0.1": "icmp+neighbour aa:aa:aa:aa:aa:01",
		"10.0.0.2": "tcp ",
		"10.0.0.3": "tcp ",
		"10.0.0.4": "neighbour aa:aa:aa:aa:aa:04",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("hosts = %v, want %v (10.0.0.5 is unreachable, 10.0.0.6 silent)", got, want)
	}
	if res.Hosts[0].IP != "10.0.0.1" || res.Hosts[3].IP != "10.0.0.4" {
		t.Errorf("hosts out of order: %+v", res.Hosts)
	}
	if tcpProbedICMPHost.Load() {
		t.Error("a host that answered ICMP was probed by TCP as well")
	}
}

func TestScanWithoutICMPSaysSo(t *testing.T) {
	s := scanner{ports: []int{443}, icmp: noICMP, dial: fakeDial(map[string]error{"10.0.0.1": nil}), neighbours: func() map[netip.Addr]string { return nil }}
	res := s.scan(context.Background(), addrs(t, "10.0.0.1"))
	if res.ICMPUsed || len(res.Hosts) != 1 {
		t.Errorf("result = %+v", res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "ICMP is not available") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

// A firewall refusing on behalf of absent addresses makes a whole range look
// alive. Nothing can tell for sure, but the result must not pass it off as fact.
func TestScanWarnsWhenARangeLooksTooAlive(t *testing.T) {
	refusing := map[string]error{}
	for _, a := range addrs(t, "10.0.0.0/27") {
		refusing[a.String()] = syscall.ECONNREFUSED
	}
	s := scanner{ports: []int{443}, icmp: noICMP, dial: fakeDial(refusing), neighbours: func() map[netip.Addr]string { return nil }}
	res := s.scan(context.Background(), addrs(t, "10.0.0.0/27"))
	if !anyWarning(res, "refused") {
		t.Errorf("no warning; warnings = %v", res.Warnings)
	}

	// The same share, but accepting: those are real services.
	accepting := map[string]error{}
	for k := range refusing {
		accepting[k] = nil
	}
	s.dial = fakeDial(accepting)
	if res := s.scan(context.Background(), addrs(t, "10.0.0.0/27")); anyWarning(res, "refused") {
		t.Errorf("warned about accepted connections: %v", res.Warnings)
	}

	// A small range that all refuses is not remarkable.
	s.dial = fakeDial(refusing)
	if res := s.scan(context.Background(), addrs(t, "10.0.0.0/29")); anyWarning(res, "refused") {
		t.Errorf("warned on a small range: %v", res.Warnings)
	}
}

func anyWarning(r Result, word string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, word) {
			return true
		}
	}
	return false
}

func TestScanOutOfTimeKeepsWhatItFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	s := scanner{
		ports: []int{443},
		icmp: func(context.Context, []netip.Addr) (map[netip.Addr]bool, error) {
			return map[netip.Addr]bool{netip.MustParseAddr("10.0.0.1"): true}, nil
		},
		dial:       fakeDial(nil), // silent until the deadline
		neighbours: func() map[netip.Addr]string { return nil },
	}
	done := make(chan Result, 1)
	go func() { done <- s.scan(ctx, addrs(t, "10.0.0.0/24")) }()
	select {
	case res := <-done:
		if res.Complete || len(res.Hosts) != 1 || !anyWarning(res, "ran out of time") {
			t.Errorf("result = %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the scan outlived its deadline")
	}
}

// A silent host must cost one timeout, not one per port.
func TestProbeTCPStopsAtFirstAnswer(t *testing.T) {
	var cancelled atomic.Int32
	s := scanner{
		ports: []int{1, 2, 3},
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			if strings.HasSuffix(addr, ":2") {
				return nil, syscall.ECONNREFUSED
			}
			<-ctx.Done()
			cancelled.Add(1)
			return nil, ctx.Err()
		},
	}
	if got := s.probeTCP(context.Background(), netip.MustParseAddr("10.0.0.1")); got != tcpRefused {
		t.Errorf("outcome = %v, want refused", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for cancelled.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if cancelled.Load() != 2 {
		t.Errorf("%d of 2 other probes stopped", cancelled.Load())
	}
}

func TestProbePortOutcomes(t *testing.T) {
	var tries atomic.Int32
	cases := []struct {
		name string
		dial func(context.Context, string) (net.Conn, error)
		want tcpOutcome
	}{
		{"refused", func(context.Context, string) (net.Conn, error) { return nil, syscall.ECONNREFUSED }, tcpRefused},
		{"reset", func(context.Context, string) (net.Conn, error) { return nil, syscall.ECONNRESET }, tcpRefused},
		{"windows refused", func(context.Context, string) (net.Conn, error) { return nil, syscall.Errno(10061) }, tcpRefused},
		{"unreachable", func(context.Context, string) (net.Conn, error) { return nil, syscall.EHOSTUNREACH }, tcpSilent},
		{"timeout", func(context.Context, string) (net.Conn, error) { return nil, context.DeadlineExceeded }, tcpSilent},
		// Out of file descriptors says nothing about the host: it is retried.
		{"no descriptors, then refused", func(context.Context, string) (net.Conn, error) {
			if tries.Add(1) < 3 {
				return nil, syscall.EMFILE
			}
			return nil, syscall.ECONNREFUSED
		}, tcpRefused},
	}
	for _, c := range cases {
		s := scanner{dial: c.dial}
		if got := s.probePort(context.Background(), "10.0.0.1:80"); got != c.want {
			t.Errorf("%s: outcome = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseNeighbours(t *testing.T) {
	const table = `IP address       HW type     Flags       HW address            Mask     Device
192.168.178.1    0x1         0x2         aa:bb:cc:dd:ee:01     *        eth0
192.168.178.2    0x1         0x0         00:00:00:00:00:00     *        eth0
192.168.178.3    0x1         0x6         aa:bb:cc:dd:ee:03     *        eth0
192.168.178.4    0x1         0x2         00:00:00:00:00:00     *        eth0
not-an-address   0x1         0x2         aa:bb:cc:dd:ee:05     *        eth0
`
	got := parseNeighbours(table)
	if len(got) != 2 || got[netip.MustParseAddr("192.168.178.1")] != "aa:bb:cc:dd:ee:01" || got[netip.MustParseAddr("192.168.178.3")] != "aa:bb:cc:dd:ee:03" {
		t.Errorf("neighbours = %v", got)
	}
}

func TestEchoRequestAndReply(t *testing.T) {
	req := echoRequest(0x1234, 7)
	if req[0] != icmpEchoRequest || checksum(req) != 0 {
		t.Errorf("request % x: a correct checksum sums to zero, got %x", req, checksum(req))
	}
	reply := []byte{icmpEchoReply, 0, 0, 0, 0x12, 0x34, 0, 7}
	if !isEchoReply(reply, 0x1234, false) {
		t.Error("a reply with our identifier was not recognised")
	}
	if isEchoReply(reply, 0x9999, false) {
		t.Error("a raw socket accepted another process's reply")
	}
	if !isEchoReply(reply, 0x9999, true) {
		t.Error("an unprivileged socket compared the identifier the kernel owns")
	}
	if isEchoReply([]byte{3, 1, 0, 0, 0, 0, 0, 0}, 0, true) {
		t.Error("destination unreachable counted as a reply")
	}
	withHeader := append(append([]byte{0x45}, make([]byte, 19)...), reply...)
	if !isEchoReply(withHeader, 0x1234, true) {
		t.Error("a reply with a leading IP header (macOS) was not recognised")
	}
}

// Real sockets: 127.0.0.2 accepts on a port, 127.0.0.3 refuses on it.
func TestRealTCPOnLoopback(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot listen on 127.0.0.2: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	d := net.Dialer{Timeout: connectTimeout}
	s := scanner{
		ports: []int{port},
		icmp:  noICMP,
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
		neighbours: func() map[netip.Addr]string { return nil },
	}
	res := s.scan(context.Background(), addrs(t, "127.0.0.2,127.0.0.3"))
	if len(res.Hosts) != 2 || res.Hosts[0].DetectedBy[0] != ViaTCP || res.Hosts[1].DetectedBy[0] != ViaTCP {
		t.Fatalf("port %s: hosts = %+v", strconv.Itoa(port), res.Hosts)
	}
	if got := s.probePort(context.Background(), net.JoinHostPort("127.0.0.2", strconv.Itoa(port))); got != tcpAccepted {
		t.Errorf("listener: outcome = %v, want accepted", got)
	}
	if got := s.probePort(context.Background(), net.JoinHostPort("127.0.0.3", strconv.Itoa(port))); got != tcpRefused {
		t.Errorf("no listener: outcome = %v, want refused", got)
	}
}

// A real echo, where the system allows this process an ICMP socket.
func TestRealICMPOnLoopback(t *testing.T) {
	alive, err := icmpSweep(context.Background(), addrs(t, "127.0.0.1"))
	if err != nil {
		t.Skipf("no ICMP socket here: %v", err)
	}
	if !alive[netip.MustParseAddr("127.0.0.1")] {
		t.Errorf("127.0.0.1 did not answer an echo request: %v", alive)
	}
}
