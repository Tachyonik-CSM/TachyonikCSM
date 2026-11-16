// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Holds AssetManager's API document (spec/openapi.yaml) to the code: every
// route served is documented and every documented route is served (PA-CAT-02),
// and the bridge operations, called through the platform API bridge as a
// routine calls them, answer what their schemas say — for the user's visible
// set, which on an appliance is the shared workspace (PA-BR-07).

package handlers

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/gorilla/mux"
	"tachyonik/assetmanager/internal/config"
	"tachyonik/assetmanager/internal/systemmanager"
	"tachyonik/assetmanager/spec"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
)

func document(t *testing.T) *apicatalogue.Document {
	t.Helper()
	doc, err := apicatalogue.Parse(spec.OpenAPI)
	if err != nil {
		t.Fatalf("parse spec/openapi.yaml: %v", err)
	}
	return doc
}

var routeParam = regexp.MustCompile(`\{([a-zA-Z]+):[^}]*\}`)

// PA-CAT-02.
func TestEveryRouteIsDocumented(t *testing.T) {
	h := scopedHandler(t)
	router := h.SetupRoutes().(*mux.Router)

	served := map[string]bool{}
	_ = router.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		path, err := route.GetPathTemplate()
		if err != nil {
			return nil
		}
		methods, err := route.GetMethods()
		if err != nil {
			return nil // a prefix, not an endpoint
		}
		path = routeParam.ReplaceAllString(path, "{$1}")
		for _, m := range methods {
			if m != "OPTIONS" {
				served[m+" "+path] = true
			}
		}
		return nil
	})
	documented := map[string]bool{}
	for _, op := range document(t).Operations {
		documented[op.Method+" "+op.Path] = true
	}
	// The WebSocket is described in 03-api.md, not in OpenAPI.
	delete(served, "GET /ws")

	for _, k := range sortedSet(served) {
		if !documented[k] {
			t.Errorf("%s is served but not documented in spec/openapi.yaml", k)
		}
	}
	for _, k := range sortedSet(documented) {
		if !served[k] {
			t.Errorf("%s is documented but not served", k)
		}
	}
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// workspaceSystemManager stands in for SystemManager on an appliance: users 1
// and 2 share one workspace.
func workspaceSystemManager(t *testing.T) *systemmanager.Client {
	t.Helper()
	m := http.NewServeMux()
	m.HandleFunc("/api/internal/users/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/internal/users/")
		w.Write([]byte(`{"id":` + id + `,"active":true}`))
	})
	m.HandleFunc("/api/internal/group-members", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"userIds":[1,2]}`))
	})
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) })
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return systemmanager.NewClient(srv.URL, "")
}

// PA-BR-06, PA-BR-07: the bridge, built from the embedded document, calls the
// real routes; every answer fits its schema (the bridge refuses one that does
// not), and covers the workspace, not only the user named.
func TestBridgeOperationsAnswerAsDocumented(t *testing.T) {
	h := scopedHandler(t)
	h.systemManagerClient = workspaceSystemManager(t)
	h.config = &config.Config{}
	h.config.Auth.InternalServiceKey = "svc-key"
	h.config.Auth.JWTSecret = testSecret
	h.accounts = newAccountChecker(&fakeUsers{users: map[int64]*systemmanager.User{}})

	// User 1's colleague, user 2, imported a host with one finding of each kind.
	if _, err := h.db.CreateAsset("10.0.0.9", "Host", 2, 5, "scan.xml", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.CreateVulnerability("Weak cipher", "10.0.0.9", "443/tcp", 40, 5, "scan.xml", 2, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.CreateDetection("HTTPS Server Detection", "10.0.0.9", "443/tcp", 5, "scan.xml", 2, nil); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.SetupRoutes())
	t.Cleanup(srv.Close)

	b, err := apibridge.New([]apibridge.Module{{Name: spec.Module, Document: document(t), BaseURL: srv.URL, ServiceKey: "svc-key"}},
		apibridge.ReadOnly, apibridge.Options{})
	if err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	_ = vm.Set("api", b.NewPass().Object(vm, 1))
	v, err := vm.RunString(`
		var assets = api.assets.list().assets;
		var mine = assets.filter(function (a) { return a.name === "10.0.0.9"; })[0];
		[
			assets.length,
			api.assets.get({id: mine.id}).name,
			api.assets.stats().total,
			api.vulnerabilities.list().vulnerabilities.length,
			api.detections.list({filter: {rules: [{column: "host", operator: "contains", value: "10.0.0.9"}], matchAll: true}}).detections.length
		].join(",")`)
	if err != nil {
		t.Fatalf("routine: %v", err)
	}
	// Two assets: user 1's own (scopedHandler) is user 7's, so not visible;
	// the workspace is users 1 and 2, and user 2 has one.
	if got := v.String(); got != "1,10.0.0.9,1,1,1" {
		t.Errorf("answers = %s, want 1,10.0.0.9,1,1,1", got)
	}
}
