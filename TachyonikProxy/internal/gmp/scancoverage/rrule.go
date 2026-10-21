// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Reading a GMP schedule's iCalendar text and how often it runs.
//
// A schedule is an iCalendar VEVENT: a DTSTART, and an RRULE when it repeats.
// What the table wants from it is one number — the longest a host may wait
// between two runs, in whole days rounded up. It is worked out from the
// rule's frequency, interval and weekdays rather than by expanding the
// occurrences, and it errs on the long side where months differ in length.
// Only the parts of RFC 5545 the appliance's own scheduler writes are
// understood; a rule using anything else is refused rather than guessed at.

package scancoverage

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// recurrence is the repeating part of a schedule.
type recurrence struct {
	start    time.Time
	freq     string
	interval int
	weekdays []int // BYDAY without an ordinal, Monday = 0
	ordinal  bool  // a BYDAY with an ordinal ("1MO", "-1FR") or a BYSETPOS
	until    time.Time
	count    int
}

// parseSchedule reads the VEVENT of an iCalendar text. It returns nil, and no
// error, for a schedule that does not repeat.
func parseSchedule(ical string) (*recurrence, error) {
	// Unfold: a line starting with a space or a tab continues the previous one.
	ical = strings.NewReplacer("\r\n ", "", "\r\n\t", "", "\n ", "", "\n\t", "").Replace(ical)
	var dtstart, rrule string
	inEvent := false
	for _, line := range strings.Split(ical, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "BEGIN:VEVENT":
			inEvent = true
		case line == "END:VEVENT":
			inEvent = false
		case inEvent && (strings.HasPrefix(line, "DTSTART:") || strings.HasPrefix(line, "DTSTART;")):
			dtstart = line
		case inEvent && strings.HasPrefix(line, "RRULE:"):
			rrule = strings.TrimPrefix(line, "RRULE:")
		}
	}
	if rrule == "" {
		return nil, nil
	}
	r := &recurrence{interval: 1}
	var calendarParts []string // only understood with a monthly or yearly rule
	if dtstart != "" {
		t, err := parseICalTime(dtstart[strings.LastIndex(dtstart, ":")+1:])
		if err != nil {
			return nil, fmt.Errorf("DTSTART: %w", err)
		}
		r.start = t
	}
	for _, part := range strings.Split(rrule, ";") {
		key, value, _ := strings.Cut(part, "=")
		switch strings.ToUpper(key) {
		case "FREQ":
			r.freq = strings.ToUpper(value)
		case "INTERVAL":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("INTERVAL %q is not a positive number", value)
			}
			r.interval = n
		case "COUNT":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("COUNT %q is not a positive number", value)
			}
			r.count = n
		case "UNTIL":
			t, err := parseICalTime(value)
			if err != nil {
				return nil, fmt.Errorf("UNTIL: %w", err)
			}
			r.until = t
		case "BYDAY":
			for _, d := range strings.Split(value, ",") {
				d = strings.ToUpper(strings.TrimSpace(d))
				if len(d) < 2 {
					return nil, fmt.Errorf("BYDAY %q is not a weekday", d)
				}
				wd, ok := weekdayIndex[d[len(d)-2:]]
				if !ok {
					return nil, fmt.Errorf("BYDAY %q is not a weekday", d)
				}
				if len(d) > 2 {
					r.ordinal = true
				} else {
					r.weekdays = append(r.weekdays, wd)
				}
			}
		case "BYSETPOS":
			r.ordinal = true
		case "BYHOUR", "BYMINUTE", "BYSECOND", "WKST":
			// Choose the time within a day, or how weeks are counted; neither
			// lengthens the wait between days with a run.
		case "BYMONTHDAY", "BYMONTH", "BYYEARDAY", "BYWEEKNO":
			calendarParts = append(calendarParts, strings.ToUpper(key))
		default:
			return nil, fmt.Errorf("the rule part %s is not understood", key)
		}
	}
	switch r.freq {
	case "SECONDLY", "MINUTELY", "HOURLY", "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
	case "":
		return nil, fmt.Errorf("the rule has no FREQ")
	default:
		return nil, fmt.Errorf("FREQ %s is not understood", r.freq)
	}
	if len(calendarParts) > 0 && r.freq != "MONTHLY" && r.freq != "YEARLY" {
		return nil, fmt.Errorf("%s is only understood with a monthly or yearly rule", calendarParts[0])
	}
	return r, nil
}

var weekdayIndex = map[string]int{"MO": 0, "TU": 1, "WE": 2, "TH": 3, "FR": 4, "SA": 5, "SU": 6}

func parseICalTime(v string) (time.Time, error) {
	for _, layout := range []string{"20060102T150405Z", "20060102T150405", "20060102"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not an iCalendar date", v)
}

// longestGap is the longest wait between two runs.
func (r *recurrence) longestGap() (time.Duration, error) {
	const day = 24 * time.Hour
	n := time.Duration(r.interval)
	switch r.freq {
	case "SECONDLY", "MINUTELY":
		return day, nil
	case "HOURLY", "DAILY":
		base := n * time.Hour
		if r.freq == "DAILY" {
			base = n * day
		}
		if len(r.weekdays) == 0 {
			if r.ordinal {
				return 0, fmt.Errorf("a BYDAY with an ordinal is not understood with %s", strings.ToLower(r.freq))
			}
			return base, nil
		}
		if r.interval != 1 {
			// Every second day on weekdays only: the waits depend on where the
			// run lands each week, which this does not work out.
			return 0, fmt.Errorf("an INTERVAL above 1 together with BYDAY is not understood with %s", strings.ToLower(r.freq))
		}
		return maxDur(base, weekdayGap(r.weekdays, 1)), nil
	case "WEEKLY":
		if r.ordinal {
			return 0, fmt.Errorf("a BYDAY with an ordinal is not understood with weekly")
		}
		days := r.weekdays
		if len(days) == 0 {
			days = []int{(int(r.start.Weekday()) + 6) % 7}
		}
		return weekdayGap(days, r.interval), nil
	case "MONTHLY":
		// Months run to 31 days; "the first Monday" can be 35 days from the
		// last.
		gap := n * 31 * day
		if r.ordinal {
			gap += 4 * day
		}
		return gap, nil
	default: // YEARLY
		return n * 366 * day, nil
	}
}

// weekdayGap is the longest wait between the given weekdays, with a run every
// interval-th week.
func weekdayGap(days []int, interval int) time.Duration {
	d := append([]int(nil), days...)
	sort.Ints(d)
	longest := 7*interval - (d[len(d)-1] - d[0])
	for i := 1; i < len(d); i++ {
		if g := d[i] - d[i-1]; g > longest {
			longest = g
		}
	}
	return time.Duration(longest) * 24 * time.Hour
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// ended reports whether the schedule has no run left after now: its UNTIL
// has passed, or its COUNT is used up. For COUNT the last run is estimated
// from the rule's period — exact for the appliance's own schedules, which
// have no gaps of their own.
func (r *recurrence) ended(now time.Time) (bool, error) {
	if !r.until.IsZero() && r.until.Before(now) {
		return true, nil
	}
	if r.count == 0 {
		return false, nil
	}
	if r.count == 1 {
		return true, nil // a single run: not a recurring scan
	}
	if r.start.IsZero() {
		return false, nil
	}
	var period time.Duration
	runs := r.count - 1
	n := time.Duration(r.interval)
	switch r.freq {
	case "SECONDLY":
		period = n * time.Second
	case "MINUTELY":
		period = n * time.Minute
	case "HOURLY":
		period = n * time.Hour
	case "DAILY":
		period = n * 24 * time.Hour
	case "WEEKLY":
		perWeek := len(r.weekdays)
		if perWeek == 0 {
			perWeek = 1
		}
		runs = int(math.Ceil(float64(r.count-1) / float64(perWeek)))
		period = n * 7 * 24 * time.Hour
	case "MONTHLY":
		return !r.start.AddDate(0, runs*r.interval, 0).After(now), nil
	default:
		return !r.start.AddDate(runs*r.interval, 0, 0).After(now), nil
	}
	return !r.start.Add(time.Duration(runs) * period).After(now), nil
}

// intervalDays rounds a wait up to whole days, at least one.
func intervalDays(gap time.Duration) int {
	days := int(math.Ceil(gap.Hours() / 24))
	if days < 1 {
		days = 1
	}
	return days
}
