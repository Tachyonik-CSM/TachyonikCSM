// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The hd-scan subcommand: TachyonikHD, the built-in host detection, run from a
// shell.
//
// It scans with the same code as the built-in tool (tools.RunHDScan), so the
// same limits apply — private IPv4 only, at most a /16 — and --json prints the
// very document the tool returns, which the platform's analysis and import
// rules recognise when it is uploaded as a source. Like netscan it needs no
// running daemon and no platform connection: it scans, prints, and writes
// nothing to disk.
//
// Without --target it scans the proxy's own network, the one the netscan sweep
// uses by default (netscan.network, or the /24 around the primary address).

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/hostscan"
	"tachyonik/tachyonikproxy/internal/netscan"
	"tachyonik/tachyonikproxy/internal/tools"
)

// hdScanDefaultTimeout matches the built-in tool's default.
const hdScanDefaultTimeout = 300 * time.Second

// hdScanOptions is what the command line asked for. An empty target means the
// proxy's own network.
type hdScanOptions struct {
	target  string
	timeout time.Duration
	json    bool
}

// parseHDScanArgs reads the hd-scan flags from the program's arguments.
func parseHDScanArgs(args []string) (hdScanOptions, error) {
	opts := hdScanOptions{timeout: hdScanDefaultTimeout}
	for _, a := range args {
		if a == "--json" {
			opts.json = true
		}
	}
	if v, found, err := flagValue(args, "target"); err != nil {
		return opts, err
	} else if found {
		opts.target = strings.TrimSpace(v)
	}
	if v, found, err := flagValue(args, "timeout"); err != nil {
		return opts, err
	} else if found {
		secs, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || secs <= 0 {
			return opts, fmt.Errorf("--timeout %q: give a whole number of seconds greater than 0", v)
		}
		opts.timeout = time.Duration(secs) * time.Second
	}
	return opts, nil
}

// ownNetwork is the network the netscan sweep uses by default, as a CIDR.
func ownNetwork(cfg *config.Config) (string, error) {
	scanner, err := netscan.New(netscan.Config{
		Network:       cfg.NetScan.Network,
		NetworkSource: "netscan.network",
	})
	if err != nil {
		return "", err
	}
	if n := scanner.Network(); n != "" {
		return n, nil
	}
	return "", fmt.Errorf("the proxy's own network could not be determined")
}

func runHDScan() {
	opts, err := parseHDScanArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "hd-scan: %v\n", err)
		os.Exit(1)
	}
	if opts.target == "" {
		network, err := ownNetwork(mustLoadConfig(config.Load()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "hd-scan: %v; name the range with --target\n", err)
			os.Exit(1)
		}
		opts.target = network
	}

	// Refused before anything is printed as under way.
	addrs, err := hostscan.ParseTargets(opts.target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hd-scan: %v\n", err)
		os.Exit(1)
	}

	// Ctrl-C ends the scan early; what was found by then is still printed.
	interrupted, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(interrupted, opts.timeout)
	defer cancel()

	// Progress goes to stderr so stdout stays pipeable.
	fmt.Fprintf(os.Stderr, "Scanning %s (%d address(es)) for alive hosts, for at most %s ...\n", opts.target, len(addrs), opts.timeout)
	scan, err := tools.RunHDScan(ctx, opts.target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hd-scan: %v\n", err)
		os.Exit(1)
	}
	if interrupted.Err() != nil {
		fmt.Fprintln(os.Stderr, "Interrupted: showing what was found so far.")
	}
	for _, w := range scan.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	if opts.json {
		doc, err := scan.JSON()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hd-scan: %v\n", err)
			os.Exit(1)
		}
		os.Stdout.Write(append(doc, '\n'))
		return
	}
	printHDScan(os.Stdout, scan)
}

// printHDScan writes the human-readable form: a summary line, then a table.
func printHDScan(w io.Writer, scan tools.HDScan) {
	state := ""
	if !scan.Complete {
		state = " · incomplete"
	}
	fmt.Fprintf(w, "Target %s · %d alive among %d address(es) · %s%s\n\n",
		scan.Target, len(scan.Hosts), scan.AddressesProbed, roundDuration(scan.Duration()), state)
	if len(scan.Hosts) == 0 {
		fmt.Fprintln(w, "No host answered.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tDETECTED BY\tMAC")
	for _, h := range scan.Hosts {
		mac := h.MAC
		if mac == "" {
			mac = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", h.IP, strings.Join(h.DetectedBy, ","), mac)
	}
	tw.Flush()
}

// roundDuration keeps a duration readable: milliseconds below a second, tenths
// of a second above.
func roundDuration(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(100 * time.Millisecond)
}
