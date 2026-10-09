// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests api.organisation.update: the routine's patch goes to SystemManager as
// it is, in one PATCH, with nothing read first.
//
// The bridge used to read the organisation and send the merged whole back,
// from when SystemManager's update required every field. It no longer does,
// and the read-merge-write could put back a name or cost someone had just
// changed in the WebUI.

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

func TestOrganisationUpdateSendsOnlyThePatch(t *testing.T) {
	var methods []string
	var sent map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPatch {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &sent)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"PCO","assetsHosts":23}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey = srv.URL, "sm-key"
	vm := goja.New()
	_ = vm.Set("api", New(cfg).Build(vm, AuthContext{UserID: 52}))

	v, err := vm.RunString(`api.organisation.update({ assetsHosts: 23 }).assetsHosts`)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if v.ToInteger() != 23 {
		t.Errorf("returned %v, want the updated organisation", v)
	}
	if len(methods) != 1 || methods[0] != "PATCH /api/users/me/organisation" {
		t.Errorf("requests %v, want a single PATCH", methods)
	}
	if len(sent) != 1 || sent["assetsHosts"] != float64(23) {
		t.Errorf("sent %v, want only {assetsHosts: 23}", sent)
	}
}

func TestOrganisationUpdateNeedsAnObject(t *testing.T) {
	vm := goja.New()
	_ = vm.Set("api", New(&config.Config{}).Build(vm, AuthContext{}))
	if _, err := vm.RunString(`api.organisation.update(5)`); err == nil {
		t.Error("a non-object patch was accepted")
	}
}
