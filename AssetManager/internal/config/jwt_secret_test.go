// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins which JWT secrets are reported at startup: the public placeholder and
// one shorter than 32 characters are; a long secret of one's own is not.

package config

import (
	"strings"
	"testing"
)

func TestJWTSecretProblem(t *testing.T) {
	for _, c := range []struct {
		secret string
		want   string // part of the report, or "" for none
	}{
		{PlaceholderJWTSecret, "placeholder"},
		{"too-short", "shorter than 32"},
		{strings.Repeat("x", 31), "shorter than 32"},
		{strings.Repeat("x", 32), ""},
		{"a-long-random-secret-of-our-own-0123456789", ""},
	} {
		cfg := &Config{}
		cfg.Auth.JWTSecret = c.secret
		got := cfg.JWTSecretProblem()
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("secret of %d characters: %q, want a report containing %q", len(c.secret), got, c.want)
		}
	}
}
