// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the accepted values, the faults and their wording: every fault is
// collected and names the setting, its source and what it takes.

package settings

import (
	"strings"
	"testing"
)

func TestEnvironment(t *testing.T) {
	t.Setenv("T_PORT", " 587 ")
	t.Setenv("T_OFF", "0")
	t.Setenv("T_EMPTY", "")
	t.Setenv("T_BOOL", "FALSE")
	t.Setenv("T_LEVEL", "debug")
	r := NewReader("")
	port, off, empty, b, level := 25, 5, 10, true, "INFO"
	r.Int("T_PORT", &port, Range(1, 65535))
	r.Int("T_OFF", &off, Min(1), ZeroMeansOff())
	r.Int("T_EMPTY", &empty, Min(1))
	r.Bool("T_BOOL", &b)
	r.Level("T_LEVEL", &level)
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if port != 587 || off != 0 || empty != 10 || b || level != "DEBUG" {
		t.Errorf("got %d %d %d %v %s", port, off, empty, b, level)
	}

	for _, c := range []struct{ value, want string }{
		{"abc", "a whole number above 0"},
		{"0", "a whole number above 0"},
		{"-3", "a whole number above 0"},
		{"1.5", "a whole number above 0"},
	} {
		t.Setenv("T_N", c.value)
		r := NewReader("")
		n := 7
		r.Int("T_N", &n, Min(1))
		if err := r.Err(); err == nil || !strings.Contains(err.Error(), `T_N="`+c.value+`"`) || !strings.Contains(err.Error(), c.want) || n != 7 {
			t.Errorf("%q: %v, n=%d", c.value, err, n)
		}
	}

	t.Setenv("T_B1", "yes")
	t.Setenv("T_B2", "on")
	t.Setenv("T_L", "verbose")
	t.Setenv("T_P", "70000")
	r = NewReader("")
	var b1, b2 bool
	var l string
	p := 1
	r.Bool("T_B1", &b1)
	r.Bool("T_B2", &b2)
	r.Level("T_L", &l)
	r.Int("T_P", &p, Range(1, 65535))
	err := r.Err()
	if err == nil || strings.Count(err.Error(), "\n") != 3 {
		t.Fatalf("all four faults, one per line: %v", err)
	}
	for _, s := range []string{"T_B1", "true, false, 1 or 0", "T_L", "DEBUG, INFO, WARN or ERROR", "from 1 to 65535"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("missing %q in %v", s, err)
		}
	}
}

func TestFile(t *testing.T) {
	r := NewReader("/etc/x.yaml")
	a, b, c, l := 10, 10, 10, "INFO"
	r.FileInt("a.zero", 0, &a, Min(1))
	r.FileInt("a.ok", 30, &b, Min(1))
	r.FileInt("a.neg", -5, &c, Min(1))
	r.FileLevel("log.level", "warn", &l)
	r.FileLevel("log.bad", "loud", &l)
	err := r.Err()
	if a != 10 || b != 30 || c != 10 || l != "WARN" {
		t.Errorf("got %d %d %d %s", a, b, c, l)
	}
	if err == nil || !strings.Contains(err.Error(), "configuration file /etc/x.yaml: a.neg is -5") || !strings.Contains(err.Error(), `log.bad is "loud"`) {
		t.Errorf("err = %v", err)
	}
}

func TestPort(t *testing.T) {
	t.Setenv("T_PORT_OK", " 8090 ")
	t.Setenv("T_PORT_BAD", "80a")
	r := NewReader("f.yaml")
	a, b, c, d := "8080", "8080", "8080", "8080"
	r.Port("T_PORT_OK", &a)
	r.Port("T_PORT_BAD", &b)
	r.FilePort("server.port", 0, &c)
	r.FilePort("server.port", 70000, &d)
	err := r.Err()
	if a != "8090" || b != "8080" || c != "8080" || d != "8080" || err == nil ||
		!strings.Contains(err.Error(), "T_PORT_BAD") || !strings.Contains(err.Error(), "server.port is 70000") {
		t.Errorf("%s %s %s %s %v", a, b, c, d, err)
	}
}
