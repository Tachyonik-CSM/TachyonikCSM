// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests how the assets get into the rule context: the fields AssetManager
// reports per asset, last scanned and scan coverage included, with null — not
// a missing key — where a value is not known.

package generator

import (
	"net/http"
	"testing"

	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/systemmanager"
)

func TestAssetsCarryLastScannedAndScanCoverage(t *testing.T) {
	g := generatorWithAssetManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assets":[
			{"id":1,"name":"covered","type":"Host","severity":0,"score":100,"lastSeen":"2026-10-19T00:00:00Z",
			 "lastScanned":"2026-10-19T00:00:00Z","lastScanCoverageVerification":"2026-10-18T00:00:00Z","scanCoverageIntervalDays":7},
			{"id":2,"name":"uncovered","type":"Host","severity":0,"score":0,"lastSeen":"2026-10-01T00:00:00Z",
			 "lastScanned":null,"lastScanCoverageVerification":null,"scanCoverageIntervalDays":null}
		],"total":2}`))
	})
	ctx := jsruntime.RuleContext{}
	g.addAssets(&ctx, &systemmanager.User{ID: 52})

	if len(ctx.Assets) != 2 {
		t.Fatalf("got %d assets, want 2", len(ctx.Assets))
	}
	covered, uncovered := ctx.Assets[0], ctx.Assets[1]
	if covered["scanCoverageIntervalDays"] != 7 ||
		covered["lastScanCoverageVerification"] != "2026-10-18T00:00:00Z" ||
		covered["lastScanned"] != "2026-10-19T00:00:00Z" {
		t.Errorf("covered asset = %v", covered)
	}
	for _, k := range []string{"lastScanned", "lastScanCoverageVerification", "scanCoverageIntervalDays"} {
		v, ok := uncovered[k]
		if !ok {
			t.Errorf("%s is missing; want it present as null", k)
		} else if v != nil {
			t.Errorf("%s = %#v, want untyped nil", k, v)
		}
	}
}
