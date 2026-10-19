// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests how the asset statistics get into the rule context: the severity
// buckets and the host assets by last seen, as AssetManager reports them, and
// the same keys at zero when AssetManager does not answer.

package generator

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/actiongenerator/internal/assetmanager"
	"tachyonik/actiongenerator/internal/jsruntime"
	"tachyonik/actiongenerator/internal/systemmanager"
)

func generatorWithAssetManager(t *testing.T, handler http.HandlerFunc) *Generator {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Generator{assetMgr: assetmanager.NewClient(srv.URL, "am-key")}
}

func TestAssetStatsCarryHostsByLastSeen(t *testing.T) {
	g := generatorWithAssetManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"critical":1,"high":2,"medium":0,"low":0,"noThreat":0,"unmanaged":107,"total":110,
			"hostsSeen30d":3,"hostsSeen180d":68,"hostsSeenOlder":39}`))
	})
	ctx := jsruntime.RuleContext{}
	g.addAssetStats(&ctx, &systemmanager.User{ID: 52})

	for k, want := range map[string]int{"total": 110, "hostsSeen30d": 3, "hostsSeen180d": 68, "hostsSeenOlder": 39} {
		if got := toInt(ctx.AssetStats[k]); got != want {
			t.Errorf("%s = %v, want %d", k, ctx.AssetStats[k], want)
		}
	}
}

func TestAssetStatsAreZeroWhenAssetManagerFails(t *testing.T) {
	g := generatorWithAssetManager(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	ctx := jsruntime.RuleContext{}
	g.addAssetStats(&ctx, &systemmanager.User{ID: 52})

	for _, k := range []string{"total", "hostsSeen30d", "hostsSeen180d", "hostsSeenOlder"} {
		v, ok := ctx.AssetStats[k]
		if !ok || toInt(v) != 0 {
			t.Errorf("%s = %v (present %v), want 0", k, v, ok)
		}
	}
}
