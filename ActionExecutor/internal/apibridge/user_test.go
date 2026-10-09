// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests api.user.get(): who the run is for, from SystemManager, asked with the
// service key on the user's behalf — and throwing when SystemManager does not
// answer.

package apibridge

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dop251/goja"

	"tachyonik/actionexecutor/internal/config"
)

func TestUserGetAsTheRunsUser(t *testing.T) {
	cases := []struct {
		name string
		auth AuthContext
		ok   func(r *http.Request) bool
	}{
		{"any run", AuthContext{UserID: 52},
			func(r *http.Request) bool {
				return r.Header.Get("X-Internal-Service-Key") == "sm-key" && r.Header.Get("X-User-ID") == "52"
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/auth/me" || !c.ok(r) {
					http.Error(w, "wrong request", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":52,"username":"Jan","role":"admin"}`))
			}))
			t.Cleanup(srv.Close)
			cfg := &config.Config{}
			cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey = srv.URL, "sm-key"
			vm := goja.New()
			_ = vm.Set("api", New(cfg).Build(vm, c.auth))
			v, err := vm.RunString(`api.user.get().username`)
			if err != nil || v.String() != "Jan" {
				t.Errorf("got %v, %v", v, err)
			}
		})
	}
}

func TestUserGetThrowsWhenSystemManagerFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.SystemManager.URL = srv.URL
	vm := goja.New()
	_ = vm.Set("api", New(cfg).Build(vm, AuthContext{UserID: 52}))
	if _, err := vm.RunString(`api.user.get()`); err == nil {
		t.Error("a failed lookup did not throw")
	}
}
