// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The scan's ICMP step: echo requests to every address, two rounds, and whatever
// answers. Written against the standard library alone, since an echo request is
// eight bytes and a checksum.
//
// It takes the socket it can get: an unprivileged ICMP socket, which Linux grants
// by net.ipv4.ping_group_range and macOS always, or else a raw one, which needs
// root or an administrator. With neither the step reports an error and the scan
// goes on without it.

package hostscan

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

const (
	icmpEchoRequest = 8
	icmpEchoReply   = 0
	// icmpPerSecond paces the requests, so a /16 does not overflow the socket
	// buffer and lose them on this side of the wire.
	icmpPerSecond = 5000
	// icmpRounds asks an address that did not answer once more. The first
	// packet to a silent neighbour is often lost to ARP.
	icmpRounds = 2
	// icmpWait is how long replies are awaited after each round.
	icmpWait = 700 * time.Millisecond
)

// icmpConn is an ICMP socket: what to send to, and whether replies carry an IP
// header.
type icmpConn struct {
	net.PacketConn
	target func(netip.Addr) net.Addr
	// dgram: the kernel owns the identifier on an unprivileged socket, so only
	// a raw socket has one to compare.
	dgram bool
}

func openICMP() (*icmpConn, error) {
	if c, err := openDgramICMP(); err == nil {
		return c, nil
	}
	c, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, errors.New("no unprivileged ICMP socket, and no permission for a raw one")
	}
	return &icmpConn{
		PacketConn: c,
		target:     func(a netip.Addr) net.Addr { return &net.IPAddr{IP: a.AsSlice()} },
	}, nil
}

func icmpSweep(ctx context.Context, addrs []netip.Addr) (map[netip.Addr]bool, error) {
	conn, err := openICMP()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	wanted := make(map[netip.Addr]bool, len(addrs))
	for _, a := range addrs {
		wanted[a] = true
	}
	id := uint16(os.Getpid())

	var mu sync.Mutex
	alive := make(map[netip.Addr]bool)
	// Closed once every address has answered, so nothing waits for no one.
	allAnswered := make(chan struct{})

	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		buf := make([]byte, 1500)
		for {
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				return // closed below, once the rounds are over
			}
			a, ok := peerAddr(peer)
			if !ok || !wanted[a] || !isEchoReply(buf[:n], id, conn.dgram) {
				continue
			}
			mu.Lock()
			first := !alive[a]
			alive[a] = true
			complete := first && len(alive) == len(wanted)
			mu.Unlock()
			if complete {
				close(allAnswered)
			}
		}
	}()

	gap := time.Second / icmpPerSecond
rounds:
	for round := 0; round < icmpRounds && ctx.Err() == nil; round++ {
		next := time.Now()
		for i, a := range addrs {
			if ctx.Err() != nil {
				break
			}
			mu.Lock()
			done := alive[a]
			mu.Unlock()
			if done {
				continue
			}
			// A failed send is an unreachable address, which is an answer of
			// sorts: nothing to report.
			_, _ = conn.WriteTo(echoRequest(id, uint16(i)), conn.target(a))
			next = next.Add(gap)
			if d := time.Until(next); d > 0 {
				time.Sleep(d)
			}
		}
		select {
		case <-time.After(icmpWait):
		case <-allAnswered:
			break rounds
		case <-ctx.Done():
		}
	}

	conn.Close()
	reader.Wait()
	return alive, nil
}

func peerAddr(peer net.Addr) (netip.Addr, bool) {
	var ip net.IP
	switch p := peer.(type) {
	case *net.IPAddr:
		ip = p.IP
	case *net.UDPAddr:
		ip = p.IP
	default:
		return netip.Addr{}, false
	}
	a, ok := netip.AddrFromSlice(ip)
	return a.Unmap(), ok
}

// echoRequest builds an ICMP echo request with no payload.
func echoRequest(id, seq uint16) []byte {
	b := make([]byte, 8)
	b[0] = icmpEchoRequest
	binary.BigEndian.PutUint16(b[4:], id)
	binary.BigEndian.PutUint16(b[6:], seq)
	binary.BigEndian.PutUint16(b[2:], checksum(b))
	return b
}

// checksum is the Internet checksum (RFC 1071).
func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// isEchoReply reads a received packet. A reply with a leading IP header, which
// macOS leaves on an unprivileged socket, is told by the version nibble: an ICMP
// echo reply starts with type 0.
func isEchoReply(b []byte, id uint16, dgram bool) bool {
	if len(b) > 0 && b[0]>>4 == 4 {
		headerLen := int(b[0]&0x0f) * 4
		if len(b) < headerLen {
			return false
		}
		b = b[headerLen:]
	}
	if len(b) < 8 || b[0] != icmpEchoReply {
		return false
	}
	return dgram || binary.BigEndian.Uint16(b[4:]) == id
}
