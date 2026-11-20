// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that the old poll interval, which every installed configuration file
// carries from the template, is accepted for one release, ignored, and warned
// about, while the sweep interval replaces it (SA-OPS-18,
// docs/plans/sourceanalyser-source-events.md).

package config

import (
	"strings"
	"testing"
)

func TestTheOldPollIntervalIsIgnoredWithAWarning(t *testing.T) {
	withConfig(t, "analyzer:\n  poll_interval: 5\n")
	t.Setenv("SOURCEANALYSER_POLL_INTERVAL", "2")
	cfg := mustLoad(t)
	if cfg.Analyzer.SweepIntervalSeconds != 300 {
		t.Errorf("sweep interval = %d, want the default 300", cfg.Analyzer.SweepIntervalSeconds)
	}
	if len(cfg.Warnings) != 2 {
		t.Fatalf("warnings = %q, want one for the key and one for the variable", cfg.Warnings)
	}
	for i, want := range []string{"analyzer.poll_interval is ignored", "SOURCEANALYSER_POLL_INTERVAL is ignored"} {
		if !strings.Contains(cfg.Warnings[i], want) || !strings.Contains(cfg.Warnings[i], "(now 300 s)") {
			t.Errorf("warning %d = %q, want %q and the interval in effect", i, cfg.Warnings[i], want)
		}
	}
}

func TestTheSweepIntervalIsASetting(t *testing.T) {
	withConfig(t, "analyzer:\n  sweep_interval_seconds: 120\n")
	if got := mustLoad(t); got.Analyzer.SweepIntervalSeconds != 120 || len(got.Warnings) != 0 {
		t.Errorf("sweep interval = %d, warnings %q", got.Analyzer.SweepIntervalSeconds, got.Warnings)
	}
	t.Setenv("SOURCEANALYSER_SWEEP_INTERVAL_SECONDS", "60")
	if got := mustLoad(t).Analyzer.SweepIntervalSeconds; got != 60 {
		t.Errorf("sweep interval from the environment = %d, want 60", got)
	}
}
