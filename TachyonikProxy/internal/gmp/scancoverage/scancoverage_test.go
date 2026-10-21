// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the scan coverage table: how often a schedule runs, how a host list is
// split and its exclusions cut out, which tasks are left out, and that a
// target scanned by several tasks keeps the shortest interval.

package scancoverage

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func ical(rrule string) string {
	s := "BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nDTSTART:20261020T160000Z\nDURATION:PT0S\n"
	if rrule != "" {
		s += "RRULE:" + rrule + "\n"
	}
	return s + "UID:x\nEND:VEVENT\nEND:VCALENDAR\n"
}

func TestIntervalOfASchedule(t *testing.T) {
	for rule, want := range map[string]int{
		"FREQ=WEEKLY":                           7,
		"FREQ=WEEKLY;INTERVAL=2":                14,
		"FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR":      3, // Friday to Monday
		"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TH":    11,
		"FREQ=DAILY":                            1,
		"FREQ=DAILY;INTERVAL=3":                 3,
		"FREQ=DAILY;BYDAY=MO,WE,FR":             3,
		"FREQ=HOURLY;INTERVAL=6":                1,
		"FREQ=HOURLY;INTERVAL=36":               2,
		"FREQ=MINUTELY;INTERVAL=30":             1,
		"FREQ=MONTHLY":                          31,
		"FREQ=MONTHLY;BYDAY=1MO":                35,
		"FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=15": 93,
		"FREQ=YEARLY":                           366,
		"INTERVAL=2;FREQ=WEEKLY":                14, // FREQ need not come first
	} {
		r, err := parseSchedule(ical(rule))
		if err != nil || r == nil {
			t.Errorf("%s: %v, %v", rule, r, err)
			continue
		}
		gap, err := r.longestGap()
		if err != nil {
			t.Errorf("%s: %v", rule, err)
			continue
		}
		if got := intervalDays(gap); got != want {
			t.Errorf("%s: %d days, want %d", rule, got, want)
		}
	}
}

func TestAScheduleWithoutARuleDoesNotRepeat(t *testing.T) {
	r, err := parseSchedule(ical(""))
	if err != nil || r != nil {
		t.Errorf("got %v, %v; want no recurrence", r, err)
	}
}

func TestAnEndedScheduleIsRecognised(t *testing.T) {
	for rule, want := range map[string]bool{
		"FREQ=WEEKLY;UNTIL=20261001T000000Z": true,
		"FREQ=WEEKLY;UNTIL=20271001T000000Z": false,
		"FREQ=WEEKLY;COUNT=1":                true, // one run is not a recurring scan
		"FREQ=DAILY;COUNT=2":                 false,
		"FREQ=HOURLY;COUNT=3":                true, // its last run was at 18:00 the day before
		"FREQ=WEEKLY;COUNT=10":               false,
	} {
		r, err := parseSchedule(ical(rule))
		if err != nil {
			t.Fatalf("%s: %v", rule, err)
		}
		if got, _ := r.ended(now); got != want {
			t.Errorf("%s: ended = %v, want %v", rule, got, want)
		}
	}
}

func TestARuleNotUnderstoodIsRefused(t *testing.T) {
	for _, rule := range []string{
		"FREQ=WEEKLY;BYDAY=1MO",
		"FREQ=DAILY;INTERVAL=2;BYDAY=MO,TU",
		"FREQ=DAILY;BYMONTHDAY=1",
		"FREQ=WEEKLY;RSCALE=GREGORIAN",
		"INTERVAL=2",
	} {
		r, err := parseSchedule(ical(rule))
		if err == nil {
			_, err = r.longestGap()
		}
		if err == nil {
			t.Errorf("%s was accepted", rule)
		}
	}
}

func TestAHostListBecomesOneEntryEach(t *testing.T) {
	specs, invalid := parseHostList("192.168.1.5, 10.0.0.0/24,10.1.1.1-20 ,10.2.0.1-10.2.0.9, web-01.example.org, 2001:db8::1, 192.168.2.7/32, bad|name")
	var got []string
	for _, s := range specs {
		got = append(got, s.String())
	}
	want := "192.168.1.5 10.0.0.0/24 10.1.1.1-10.1.1.20 10.2.0.1-10.2.0.9 web-01.example.org 2001:db8::1 192.168.2.7"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v\nwant %s", got, want)
	}
	if len(invalid) != 1 || invalid[0] != "bad|name" {
		t.Errorf("invalid = %v", invalid)
	}
}

func TestExcludedHostsAreCutOut(t *testing.T) {
	inc, _ := parseHostList("10.0.0.0/24, 10.9.9.9, host-a, host-b")
	exc, _ := parseHostList("10.0.0.5, 10.0.0.10-10.0.0.20, 10.0.0.255, host-b, 2001:db8::1")
	var got []string
	for _, s := range subtract(inc, exc) {
		got = append(got, s.String())
	}
	want := "10.0.0.0-10.0.0.4 10.0.0.6-10.0.0.9 10.0.0.21-10.0.0.254 10.9.9.9 host-a"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v\nwant %s", got, want)
	}
}

const targetsDoc = `<get_targets_response status="200" status_text="OK">
<target id="t-lab"><name>Lab</name><hosts>192.168.10.54</hosts><exclude_hosts></exclude_hosts></target>
<target id="t-net"><name>Net</name><hosts>192.168.10.0/24, scanner.lab</hosts><exclude_hosts>192.168.10.1</exclude_hosts></target>
<target id="t-one"><name>One</name><hosts>192.168.10.7</hosts><exclude_hosts/></target>
<target_count>3<filtered>3</filtered><page>3</page></target_count></get_targets_response>`

func task(name, targetID, schedule string) string {
	sched := `<schedule id=""><name></name><trash>0</trash></schedule>`
	if schedule != "" {
		sched = `<schedule id="s-` + name + `"><name>` + name + `</name><trash>0</trash><icalendar>` + schedule + `</icalendar></schedule>`
	}
	return `<task id="` + name + `"><name>` + name + `</name><target id="` + targetID + `"><name>x</name><trash>0</trash></target>` + sched + `</task>`
}

func TestTheTableOfAnAppliance(t *testing.T) {
	tasks := `<get_tasks_response status="200" status_text="OK">` +
		task("Unscheduled", "t-net", "") +
		task("Weekly lab", "t-lab", ical("FREQ=WEEKLY")) +
		task("Monthly net", "t-net", ical("FREQ=MONTHLY")) +
		task("Daily one", "t-one", ical("FREQ=DAILY")) +
		task("Once", "t-one", ical("")) +
		task("Daily lab", "t-lab", ical("FREQ=DAILY;INTERVAL=2")) +
		`<task_count>6<filtered>6</filtered></task_count></get_tasks_response>`
	table, err := Build([]byte(tasks), []byte(targetsDoc), now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := Header + "\n" +
		"192.168.10.0|31\n" +
		"192.168.10.2-192.168.10.255|31\n" +
		"192.168.10.7|1\n" +
		"192.168.10.54|2\n" + // the shorter of the weekly and the two-daily task
		"scanner.lab|31\n"
	if got := string(table.Render()); got != want {
		t.Errorf("table:\n%s\nwant:\n%s", got, want)
	}
	if table.Scheduled != 4 || len(table.Skipped) != 2 {
		t.Errorf("scheduled %d, skipped %+v", table.Scheduled, table.Skipped)
	}
}

func TestTrashedSchedulesAndTargetsAreLeftOut(t *testing.T) {
	tasks := `<get_tasks_response status="200" status_text="OK">` +
		strings.Replace(task("A", "t-lab", ical("FREQ=WEEKLY")), "<trash>0</trash><icalendar>", "<trash>1</trash><icalendar>", 1) +
		strings.Replace(task("B", "t-lab", ical("FREQ=WEEKLY")), `<name>x</name><trash>0</trash>`, `<name>x</name><trash>1</trash>`, 1) +
		task("C", "t-gone", ical("FREQ=WEEKLY")) +
		`</get_tasks_response>`
	table, err := Build([]byte(tasks), []byte(targetsDoc), now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(table.Rows) != 0 || len(table.Skipped) != 3 {
		t.Errorf("rows %+v, skipped %+v", table.Rows, table.Skipped)
	}
	if got := string(table.Render()); got != Header+"\n" {
		t.Errorf("an empty table is %q, want the header alone", got)
	}
}
