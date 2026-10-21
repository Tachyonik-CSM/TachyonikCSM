// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package scancoverage works out which hosts an OPENVAS SCAN appliance scans
// on a schedule, and how often, as a table of "target|scan-interval".
//
// Unlike the other GMP built-ins this one interprets. The answer is spread
// over three of the appliance's objects — a scan task names a target and a
// schedule, the target the hosts, the schedule the recurrence — and no single
// document holds it. So the proxy reads the tasks and the targets and writes
// one row per target: an address, a CIDR network, a range or a hostname, and
// the longest wait between two scans of it in whole days (at least 1).
//
// Left out are tasks without a schedule, schedules that do not repeat or have
// run their course, and anything in the appliance's trash. A comma list of
// hosts becomes one row per entry, excluded hosts are cut out of the address
// spans, and a target scanned by several tasks keeps the shortest interval.
package scancoverage

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Header is the table's first line, which is what identifies the file.
const Header = "target|scan-interval"

// Row is one target and its scan interval in days.
type Row struct {
	Target       string
	IntervalDays int
}

// Skipped is a scan task that contributes no row, and why.
type Skipped struct {
	Task   string
	Reason string
}

// Table is the scan coverage of one appliance.
type Table struct {
	Rows []Row
	// Scheduled is how many tasks contributed rows.
	Scheduled int
	// Skipped is every task left out, for the log; unscheduled ones included.
	Skipped []Skipped
}

type tasksResponse struct {
	Tasks []struct {
		ID     string `xml:"id,attr"`
		Name   string `xml:"name"`
		Target struct {
			ID    string `xml:"id,attr"`
			Trash string `xml:"trash"`
		} `xml:"target"`
		Schedule struct {
			ID        string `xml:"id,attr"`
			Name      string `xml:"name"`
			Trash     string `xml:"trash"`
			ICalendar string `xml:"icalendar"`
		} `xml:"schedule"`
	} `xml:"task"`
}

type targetsResponse struct {
	Targets []struct {
		ID           string `xml:"id,attr"`
		Name         string `xml:"name"`
		Hosts        string `xml:"hosts"`
		ExcludeHosts string `xml:"exclude_hosts"`
	} `xml:"target"`
}

// Build works out the table from a <get_tasks_response> and a
// <get_targets_response>, as of now.
func Build(tasksDoc, targetsDoc []byte, now time.Time) (*Table, error) {
	var tasks tasksResponse
	if err := xml.Unmarshal(tasksDoc, &tasks); err != nil {
		return nil, fmt.Errorf("the tasks could not be read: %w", err)
	}
	var targets targetsResponse
	if err := xml.Unmarshal(targetsDoc, &targets); err != nil {
		return nil, fmt.Errorf("the targets could not be read: %w", err)
	}
	type hosts struct{ include, exclude []hostSpec }
	byID := map[string]hosts{}
	for _, t := range targets.Targets {
		inc, _ := parseHostList(t.Hosts)
		exc, _ := parseHostList(t.ExcludeHosts)
		byID[t.ID] = hosts{inc, exc}
	}

	table := &Table{}
	shortest := map[string]int{}
	specs := map[string]hostSpec{}
	for _, task := range tasks.Tasks {
		skip := func(reason string) { table.Skipped = append(table.Skipped, Skipped{Task: task.Name, Reason: reason}) }
		switch {
		case task.Schedule.ID == "":
			skip("no schedule")
			continue
		case task.Schedule.Trash == "1":
			skip(fmt.Sprintf("its schedule %q is in the trash", task.Schedule.Name))
			continue
		case task.Target.ID == "":
			skip("no target")
			continue
		case task.Target.Trash == "1":
			skip("its target is in the trash")
			continue
		}
		rec, err := parseSchedule(task.Schedule.ICalendar)
		if err != nil {
			skip(fmt.Sprintf("its schedule %q could not be read: %v", task.Schedule.Name, err))
			continue
		}
		if rec == nil {
			skip(fmt.Sprintf("its schedule %q does not repeat", task.Schedule.Name))
			continue
		}
		if ended, err := rec.ended(now); err != nil || ended {
			skip(fmt.Sprintf("its schedule %q has no run left", task.Schedule.Name))
			continue
		}
		gap, err := rec.longestGap()
		if err != nil {
			skip(fmt.Sprintf("its schedule %q could not be read: %v", task.Schedule.Name, err))
			continue
		}
		h, ok := byID[task.Target.ID]
		if !ok {
			skip("its target is not among the appliance's targets")
			continue
		}
		days := intervalDays(gap)
		table.Scheduled++
		for _, spec := range subtract(h.include, h.exclude) {
			key := spec.String()
			if cur, seen := shortest[key]; !seen || days < cur {
				shortest[key] = days
			}
			specs[key] = spec
		}
	}

	for key, days := range shortest {
		table.Rows = append(table.Rows, Row{Target: key, IntervalDays: days})
	}
	// Addresses in address order, then hostnames alphabetically.
	sort.Slice(table.Rows, func(i, j int) bool {
		a, b := specs[table.Rows[i].Target], specs[table.Rows[j].Target]
		switch {
		case a.isAddr() != b.isAddr():
			return a.isAddr()
		case !a.isAddr():
			return a.name < b.name
		case a.lo != b.lo:
			return a.lo.Less(b.lo)
		default:
			return a.hi.Less(b.hi)
		}
	})
	return table, nil
}

// Render writes the table: the header, then one "target|days" line per row.
func (t *Table) Render() []byte {
	var b bytes.Buffer
	b.WriteString(Header + "\n")
	for _, r := range t.Rows {
		b.WriteString(r.Target + "|" + strconv.Itoa(r.IntervalDays) + "\n")
	}
	return b.Bytes()
}
