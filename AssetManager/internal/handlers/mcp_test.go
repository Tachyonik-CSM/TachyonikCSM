// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the MCP server at POST /api/mcp (docs/specs/01-platform-api-catalogue.md
// §8): a signed-in user is answered; the internal service key, no credentials
// or a forged token are refused (PA-MCP-05, -06); and a tool call answers
// what the API answers the caller, with the caller's own credentials.

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"tachyonik/assetmanager/internal/config"
	"tachyonik/assetmanager/internal/systemmanager"
	"tachyonik/assetmanager/spec"
	"tachyonik/lib/apicatalogue"
	"tachyonik/lib/apimcp"
)

// mcpFixture is a router with two signed-in users and a user GET whose answer
// differs between them: userAMark is in the first user's answer only.
type mcpFixture struct {
	router                http.Handler
	userA, userB, userAID string
	serviceKey            string
	operation, path       string
	userAMark             string
}

// newMCPFixture: user 7 owns one asset (scopedHandler), user 9 none; each is
// alone in their workspace.
func newMCPFixture(t *testing.T) mcpFixture {
	t.Helper()
	h := scopedHandler(t)
	h.config = &config.Config{}
	h.config.Auth.JWTSecret = testSecret
	h.config.Auth.InternalServiceKey = "svc-key"
	h.accounts = newAccountChecker(&fakeUsers{users: map[int64]*systemmanager.User{
		7: {ID: 7, Active: boolPtr(true)},
		9: {ID: 9, Active: boolPtr(true)},
	}})
	return mcpFixture{
		router: h.SetupRoutes(),
		userA:  token(t, jwt.SigningMethodHS256, session(7)), userAID: "7",
		userB:      token(t, jwt.SigningMethodHS256, session(9)),
		serviceKey: "svc-key",
		operation:  "assets.list", path: "/api/assets", userAMark: "10.0.0.7",
	}
}

// mcpCall sends one JSON-RPC request to /api/mcp on h.
func mcpCall(t *testing.T, h http.Handler, method, params string, header map[string]string) (int, map[string]interface{}) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"`
	if params != "" {
		body += `,"params":` + params
	}
	req := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(body+"}"))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func mcpDocument(t *testing.T) *apicatalogue.Document {
	t.Helper()
	doc, err := apicatalogue.Parse(spec.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func mcpSession(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// PA-MCP-04: a signed-in user is answered, with the tools the document marks.
func TestMCPAnswersASignedInUser(t *testing.T) {
	f := newMCPFixture(t)

	code, out := mcpCall(t, f.router, "initialize", `{"protocolVersion":"2025-06-18"}`, mcpSession(f.userA))
	if code != http.StatusOK || out["result"] == nil {
		t.Fatalf("initialize = %d %v", code, out)
	}
	if name := out["result"].(map[string]interface{})["serverInfo"].(map[string]interface{})["name"]; name != spec.Module {
		t.Errorf("serverInfo.name = %v, want %s", name, spec.Module)
	}

	code, out = mcpCall(t, f.router, "tools/list", "", mcpSession(f.userA))
	if code != http.StatusOK || out["result"] == nil {
		t.Fatalf("tools/list = %d %v", code, out)
	}
	want := 0
	for _, op := range mcpDocument(t).Operations {
		if op.MCP != "" {
			want++
		}
	}
	tools, isList := out["result"].(map[string]interface{})["tools"].([]interface{})
	if !isList {
		t.Errorf("tools/list answers tools = %v, want a list", out["result"])
	}
	if len(tools) != want {
		t.Errorf("tools/list offers %d tools, the document marks %d", len(tools), want)
	}
}

// PA-MCP-03: the server on the real router offers exactly the operations the
// document marks x-mcp, and a signed-in user reaches each of them — none is an
// internal or service-key route, or one the user is refused.
func TestMCPOffersExactlyTheMarkedOperations(t *testing.T) {
	f := newMCPFixture(t)
	doc := mcpDocument(t)
	if len(apimcp.Marked(doc)) == 0 {
		t.Fatal("the document marks no operation for MCP")
	}
	for _, problem := range apimcp.Check(f.router, http.Header{"Authorization": {"Bearer " + f.userA}}, doc) {
		t.Error(problem)
	}
}

// PA-MCP-05, -06: the service key is not a credential for MCP, with or
// without a session beside it or a user it names; nor is nothing.
func TestMCPRefusesServicesAndStrangers(t *testing.T) {
	f := newMCPFixture(t)
	for name, c := range map[string]struct {
		header map[string]string
		want   int
	}{
		"the service key":                {map[string]string{"X-Internal-Service-Key": f.serviceKey}, http.StatusForbidden},
		"the service key and a session":  {map[string]string{"X-Internal-Service-Key": f.serviceKey, "Authorization": "Bearer " + f.userA}, http.StatusForbidden},
		"the service key acting for one": {map[string]string{"X-Internal-Service-Key": f.serviceKey, "X-User-ID": f.userAID}, http.StatusForbidden},
		"no credentials":                 {map[string]string{}, http.StatusUnauthorized},
		"a forged token":                 {map[string]string{"Authorization": "Bearer not.a.token"}, http.StatusUnauthorized},
	} {
		code, out := mcpCall(t, f.router, "tools/list", "", c.header)
		if code != c.want {
			t.Errorf("%s: %d %v, want %d", name, code, out, c.want)
		}
		if out["result"] != nil {
			t.Errorf("%s: answered %v", name, out["result"])
		}
	}
}

// PA-MCP-06: with one user GET marked x-mcp in a copy of the document, a tool
// call answers what the GET answers the caller — each user their own.
func TestMCPToolCallsWithTheCallersCredentials(t *testing.T) {
	f := newMCPFixture(t)
	doc := mcpDocument(t)
	var op *apicatalogue.Operation
	for _, o := range doc.Operations {
		if o.ID == f.operation {
			o.MCP = apicatalogue.ExposeRead
			op = o
		}
	}
	if op == nil || op.Method != "GET" {
		t.Fatalf("no GET operation %s in the document", f.operation)
	}
	srv, err := mcpServerFor(doc, f.router)
	if err != nil {
		t.Fatal(err)
	}

	answers := map[string]string{}
	for _, user := range []string{f.userA, f.userB} {
		req := httptest.NewRequest("GET", f.path, nil)
		req.Header.Set("Authorization", "Bearer "+user)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", f.path, rec.Code, rec.Body)
		}
		var v interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if op.Response != nil {
			v = op.Response.Filter(v)
		}
		want, _ := json.Marshal(v)

		_, out := mcpCall(t, srv, "tools/call", `{"name":"`+f.operation+`","arguments":{}}`, mcpSession(user))
		result, _ := out["result"].(map[string]interface{})
		if result == nil || result["isError"] != false {
			t.Fatalf("tools/call = %v", out)
		}
		got := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
		if got != string(want) {
			t.Errorf("tools/call = %s, GET %s answers %s", got, f.path, want)
		}
		answers[user] = got
	}
	if answers[f.userA] == answers[f.userB] {
		t.Errorf("both users got %s; the caller's credentials were not used", answers[f.userA])
	}
	if !strings.Contains(answers[f.userA], f.userAMark) {
		t.Errorf("the first user's answer lacks %q: %s", f.userAMark, answers[f.userA])
	}
}
