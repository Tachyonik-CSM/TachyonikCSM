// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that what a routine creates belongs to the user the run is for, even
// when the routine writes another user's id into the request: the call carries
// the service key, which lets a service name any owner.

package apibridge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dop251/goja"

	"tachyonik/actionexecutor/internal/config"
)

func TestCreatedThingsBelongToTheRunsUser(t *testing.T) {
	owners := map[string]interface{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		owners[r.URL.Path] = body["userId"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.AssetManager.URL, cfg.ActionManager.URL = srv.URL, srv.URL
	vm := goja.New()
	_ = vm.Set("api", New(cfg).Build(vm, AuthContext{UserID: 52}))
	if _, err := vm.RunString(`
		api.assets.create({ name: "10.0.0.1", type: "Host", userId: 7 });
		api.actions.create({ title: "Pay this invoice", type: "Info", userId: 7 });
		api.actions.create({ title: "No owner given", type: "Info" });`); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, path := range []string{"/api/assets", "/api/actions"} {
		if owners[path] != float64(52) {
			t.Errorf("%s created for user %v, want the run's user 52", path, owners[path])
		}
	}
}
