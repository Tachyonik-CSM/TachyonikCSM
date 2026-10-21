// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a rule about the scan coverage of host assets validates: the mock
// scenarios carry each asset's scan coverage interval, set for some hosts and
// null for others, so such a rule neither reads a missing field nor answers
// the same in every scenario.

package jsruntime

import (
	"testing"
	"time"
)

func TestARuleOnHostsWithoutScanCoverageValidates(t *testing.T) {
	e := New(5 * time.Second)
	if err := e.LoadFromString(`
		function uncovered(assets) {
			var n = 0;
			for (var i = 0; i < assets.length; i++) {
				var a = assets[i];
				if (a.type && a.type.toLowerCase() === "host" && a.scanCoverageIntervalDays === null) n++;
			}
			return n;
		}
		var rules = [{
			name: "hosts-without-scan-coverage", ruleId: 9,
			check: function (ctx) { return uncovered(ctx.assets) > 0; },
			createAction: function (ctx) {
				return {title: "Cover hosts", type: "Fix", status: "New", priority: 90,
					description: uncovered(ctx.assets) + " hosts without scan coverage"};
			}
		}];`); err != nil {
		t.Fatalf("load: %v", err)
	}
	report := e.ValidateWithMockCtxReport()
	if len(report.Errors) > 0 {
		t.Fatalf("validation errors: %v", report.Errors)
	}
}

func TestEveryMockAssetHasTheScanFields(t *testing.T) {
	for _, sc := range MockScenarios() {
		for _, a := range sc.Ctx.Assets {
			for _, k := range []string{"lastScanned", "lastScanCoverageVerification", "scanCoverageIntervalDays"} {
				if _, ok := a[k]; !ok {
					t.Errorf("%s: asset %v has no %s", sc.Label, a["name"], k)
				}
			}
		}
	}
}
