// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a routine's filter reaches the backend exactly as written. It used
// to be escaped by hand, missing characters such as "%", so a filter holding
// one arrived garbled.

package apibridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dop251/goja"

	"tachyonik/actionexecutor/internal/config"
)

func TestAFilterArrivesIntact(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("filter")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assets":[]}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.AssetManager.URL = srv.URL
	vm := goja.New()
	_ = vm.Set("api", New(cfg).Build(vm, AuthContext{UserID: 52}))
	if _, err := vm.RunString(`api.assets.list({ rules: [{ column: "name", operator: "contains",
		value: "100% & a+b = c?#ä" }], matchAll: true })`); err != nil {
		t.Fatalf("run: %v", err)
	}

	var filter struct {
		Rules []struct{ Value string } `json:"rules"`
	}
	if err := json.Unmarshal([]byte(got), &filter); err != nil || len(filter.Rules) != 1 {
		t.Fatalf("the backend got %q (%v)", got, err)
	}
	if v := filter.Rules[0].Value; v != "100% & a+b = c?#ä" {
		t.Errorf("value arrived as %q", v)
	}
}
