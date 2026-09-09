// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Flag parsing for subcommands that take a value.
//
// The rest of main.go matches boolean flags by scanning os.Args for an exact
// string, which is enough for --json or --dry-run. A flag carrying a value
// needs both spellings — "--flag value" and "--flag=value" — and has to say so
// when the value is missing rather than silently reading the next subcommand as
// one.
//
// This is deliberately not flag.FlagSet. main() scans every argument for a
// subcommand and tolerates flags on either side of it; a FlagSet expects the
// flags to follow the subcommand and would reject invocations that work today.

// flagValue returns the value given for --name.
//
// found is false when the flag is absent, which is distinct from a flag given
// an empty value — "--network=" is an error, not a request for the default.
func flagValue(args []string, name string) (value string, found bool, err error) {
	prefix := "--" + name
	for i, arg := range args {
		if arg == prefix {
			// "--flag value": the next argument is the value, unless there is
			// none or it starts with "-", which is treated as the next flag
			// rather than a value. That rules out values beginning with a
			// dash, which neither a CIDR nor a port list can — and it is what
			// stops "netscan --network --json" from sweeping a network
			// helpfully named "--json".
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return "", true, fmt.Errorf("%s needs a value", prefix)
			}
			return args[i+1], true, nil
		}
		if strings.HasPrefix(arg, prefix+"=") {
			v := strings.TrimPrefix(arg, prefix+"=")
			if strings.TrimSpace(v) == "" {
				return "", true, fmt.Errorf("%s needs a value", prefix)
			}
			return v, true, nil
		}
	}
	return "", false, nil
}

// parsePorts reads a comma-separated port list: "443,8443,9392".
//
// Duplicates are dropped and the result sorted, so the banner and the sweep
// report the same list in the same order however it was typed. There is no cap
// on how many ports may be given — config.yaml has none either, and a sweep
// that grows too large is stopped by max_scan_duration_minutes rather than by
// an arbitrary limit here.
func parsePorts(list string) ([]int, error) {
	seen := make(map[int]struct{})
	ports := []int{}
	for _, field := range strings.Split(list, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		port, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q: not a number", field)
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid port %d: out of range 1-65535", port)
		}
		if _, dup := seen[port]; dup {
			continue
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("no ports given")
	}
	sort.Ints(ports)
	return ports, nil
}
