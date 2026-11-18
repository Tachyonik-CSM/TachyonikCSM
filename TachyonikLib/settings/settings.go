// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package settings reads a module's number, true/false and log-level settings
// from the environment and from its parsed config.yaml, and refuses a value
// the module cannot use instead of dropping it without a word.
//
// A value dropped silently runs a module on a setting nobody chose — and a
// true/false variable read as "anything but true is false" turned
// REGISTRATIONS_DISABLED=yes into open registrations. So a Reader collects
// every fault, naming the setting, where it came from and what it accepts,
// and Err returns them all at once: an operator fixes the lot in one go, and
// the module stops at startup (docs/plans/strict-setting-values.md).
//
// The accepted values are the same in every module:
//   - an empty variable counts as not set;
//   - true/false: true, false, 1 or 0, in any case;
//   - a number: a whole number, spaces around it ignored, within the bounds
//     the setting gives;
//   - a log level: DEBUG, INFO, WARN or ERROR, in any case (set in upper case).
//
// In the file a 0 counts as an absent key — YAML cannot tell them apart — so
// only a value out of bounds is refused there. Text settings are not read
// here, so no secret ever appears in an error.
package settings

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Option bounds a number setting.
type Option func(*bounds)

type bounds struct {
	min, max       int64
	hasMin, hasMax bool
	zeroOff        bool
}

// Min sets the smallest value accepted.
func Min(n int64) Option { return func(b *bounds) { b.min, b.hasMin = n, true } }

// Max sets the largest value accepted.
func Max(n int64) Option { return func(b *bounds) { b.max, b.hasMax = n, true } }

// Range sets both bounds.
func Range(lo, hi int64) Option {
	return func(b *bounds) { b.min, b.hasMin, b.max, b.hasMax = lo, true, hi, true }
}

// ZeroMeansOff also accepts 0 from the environment, below Min: the value
// that switches a guard off. (The file cannot say 0; see the package doc.)
func ZeroMeansOff() Option { return func(b *bounds) { b.zeroOff = true } }

func newBounds(opts []Option) bounds {
	var b bounds
	for _, o := range opts {
		o(&b)
	}
	return b
}

func (b bounds) ok(n int64) bool {
	if n == 0 && b.zeroOff {
		return true
	}
	return (!b.hasMin || n >= b.min) && (!b.hasMax || n <= b.max)
}

func (b bounds) want() string {
	var w string
	switch {
	case b.hasMin && b.hasMax:
		w = fmt.Sprintf("a whole number from %d to %d", b.min, b.max)
	case b.hasMin && b.min == 1:
		w = "a whole number above 0"
	case b.hasMin:
		w = fmt.Sprintf("a whole number of at least %d", b.min)
	case b.hasMax:
		w = fmt.Sprintf("a whole number of at most %d", b.max)
	default:
		w = "a whole number"
	}
	if b.zeroOff {
		w += ", or 0 to switch it off"
	}
	return w
}

// Reader collects the faults of one configuration load.
type Reader struct {
	file string
	errs []error
}

// NewReader starts a load. configPath names the file in errors about its
// values; it may be empty when no file was read.
func NewReader(configPath string) *Reader { return &Reader{file: configPath} }

// Add records a fault a module checks itself (a float, a choice of words),
// so it is reported with the others.
func (r *Reader) Add(err error) {
	if err != nil {
		r.errs = append(r.errs, err)
	}
}

// File names the configuration file in a module's own fault messages.
func (r *Reader) File() string {
	if r.file == "" {
		return "the configuration file"
	}
	return "configuration file " + r.file
}

// Err returns every fault found, one per line, or nil.
func (r *Reader) Err() error { return errors.Join(r.errs...) }

func (r *Reader) envFault(name, value, want string) {
	r.errs = append(r.errs, fmt.Errorf("%s=%q cannot be used: it takes %s", name, value, want))
}

func (r *Reader) fileFault(key string, value any, want string) {
	r.errs = append(r.errs, fmt.Errorf("%s: %s is %v, which cannot be used: it takes %s", r.File(), key, value, want))
}

// Int64 sets dst from the environment variable name, when set.
func (r *Reader) Int64(name string, dst *int64, opts ...Option) {
	v := os.Getenv(name)
	if v == "" {
		return
	}
	b := newBounds(opts)
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || !b.ok(n) {
		r.envFault(name, v, b.want())
		return
	}
	*dst = n
}

// Int is Int64 for an int.
func (r *Reader) Int(name string, dst *int, opts ...Option) {
	n := int64(*dst)
	r.Int64(name, &n, opts...)
	*dst = int(n)
}

// Bool sets dst from the environment variable name, when set.
func (r *Reader) Bool(name string, dst *bool) {
	v := os.Getenv(name)
	if v == "" {
		return
	}
	b, ok := ParseBool(v)
	if !ok {
		r.envFault(name, v, "true, false, 1 or 0")
		return
	}
	*dst = b
}

// Level sets dst from the environment variable name, when set.
func (r *Reader) Level(name string, dst *string) {
	v := os.Getenv(name)
	if v == "" {
		return
	}
	l, ok := ParseLevel(v)
	if !ok {
		r.envFault(name, v, "DEBUG, INFO, WARN or ERROR")
		return
	}
	*dst = l
}

// FileInt64 sets dst from the file's value v of key, unless v is 0 (absent).
func (r *Reader) FileInt64(key string, v int64, dst *int64, opts ...Option) {
	if v == 0 {
		return
	}
	b := newBounds(opts)
	b.zeroOff = false // a 0 never reaches here
	if !b.ok(v) {
		r.fileFault(key, v, b.want())
		return
	}
	*dst = v
}

// FileInt is FileInt64 for an int.
func (r *Reader) FileInt(key string, v int, dst *int, opts ...Option) {
	n := int64(*dst)
	r.FileInt64(key, int64(v), &n, opts...)
	*dst = int(n)
}

// FileLevel sets dst from the file's log level v of key, unless v is empty.
func (r *Reader) FileLevel(key, v string, dst *string) {
	if v == "" {
		return
	}
	l, ok := ParseLevel(v)
	if !ok {
		r.fileFault(key, strconv.Quote(v), "DEBUG, INFO, WARN or ERROR")
		return
	}
	*dst = l
}

// Port sets dst, a listen port kept as text, from the environment variable
// name when set: a whole number from 1 to 65535.
func (r *Reader) Port(name string, dst *string) {
	n := 0
	r.Int(name, &n, Range(1, 65535))
	if n != 0 {
		*dst = strconv.Itoa(n)
	}
}

// FilePort sets dst, a listen port kept as text, from the file's value v of
// key, unless v is 0 (absent).
func (r *Reader) FilePort(key string, v int, dst *string) {
	n := 0
	r.FileInt(key, v, &n, Range(1, 65535))
	if n != 0 {
		*dst = strconv.Itoa(n)
	}
}

// ParseBool reads true, false, 1 or 0, in any case.
func ParseBool(v string) (value, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

// ParseLevel reads a log level in any case and returns it in upper case.
func ParseLevel(v string) (string, bool) {
	switch l := strings.ToUpper(strings.TrimSpace(v)); l {
	case "DEBUG", "INFO", "WARN", "ERROR":
		return l, true
	}
	return "", false
}
