// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests of RequestedBefore: only a request made before the start is left
// behind.
package aicode

import (
	"testing"
	"time"
)

func TestRequestedBefore(t *testing.T) {
	start := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	s := func(v string) *string { return &v }
	cases := []struct {
		name string
		at   *string
		want bool
	}{
		{"no request", nil, false},
		{"before the start", s("2026-11-18T09:59:59Z"), true},
		{"after the start", s("2026-11-18T10:00:01Z"), false},
		{"with nanoseconds and an offset", s("2026-11-18T10:59:59.5+01:00"), true},
		{"unreadable", s("yesterday"), true},
	}
	for _, c := range cases {
		if got := RequestedBefore(c.at, start); got != c.want {
			t.Errorf("%s: RequestedBefore = %v, want %v", c.name, got, c.want)
		}
	}
}
