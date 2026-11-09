// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux || darwin

// The unprivileged ICMP socket of Linux and macOS, which Go's net package does
// not open by itself.

package hostscan

import (
	"net"
	"net/netip"
	"os"
	"syscall"
)

func openDgramICMP() (*icmpConn, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_ICMP)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "icmp")
	defer f.Close() // FilePacketConn holds its own duplicate
	c, err := net.FilePacketConn(f)
	if err != nil {
		return nil, err
	}
	return &icmpConn{
		PacketConn: c,
		target:     func(a netip.Addr) net.Addr { return &net.UDPAddr{IP: a.AsSlice()} },
		dgram:      true,
	}, nil
}
