// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a call carries the service key and the user it is made for, and
// the two transport guards: a redirect is not followed, so the service key
// never reaches another address, and an oversized answer is refused rather
// than read into memory.

package servicecall

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestACallCarriesTheKeyAndTheUser(t *testing.T) {
	var key, user, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, user, method = r.Header.Get("X-Internal-Service-Key"), r.Header.Get("X-User-ID"), r.Method
		_, _ = w.Write([]byte(`{"n": 3}`))
	}))
	t.Cleanup(srv.Close)

	var out struct{ N int }
	if err := New().Call("DELETE", srv.URL+"/", "/api/actions", "k", 52, map[string]interface{}{"ids": []int64{1}}, &out); err != nil {
		t.Fatal(err)
	}
	if key != "k" || user != "52" || method != "DELETE" || out.N != 3 {
		t.Errorf("key %q user %q method %q out %+v", key, user, method, out)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	var reached bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	t.Cleanup(elsewhere.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	err := New().Call("GET", srv.URL, "/api/x", "k", 1, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("err = %v, want the 302 reported", err)
	}
	if reached {
		t.Error("the redirect was followed")
	}
}

func TestAnOversizedAnswerIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", MaxResponseBytes+1)))
	}))
	t.Cleanup(srv.Close)

	if err := New().Call("GET", srv.URL, "/api/x", "k", 1, nil, nil); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err = %v, want the answer refused", err)
	}
}
