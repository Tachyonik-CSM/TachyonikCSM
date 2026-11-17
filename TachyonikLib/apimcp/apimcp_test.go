// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the generated MCP server: which operations become tools and how they
// are described, that a call is the API call with the caller's credentials,
// that answers are cut to the documented fields, and how failures read.

package apimcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tachyonik/lib/apicatalogue"
)

const doc = `openapi: 3.1.0
info: {title: T, version: "1"}
paths:
  /api/things/{id}:
    get:
      operationId: things.get
      x-mcp: read
      summary: One thing
      description: The thing with that id.
      parameters:
        - {name: id, in: path, required: true, schema: {type: integer}}
        - name: filter
          in: query
          content: {application/json: {schema: {type: object}}}
        - {name: userId, in: query, x-bridge-bound: user, schema: {type: integer}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  name: {type: string, description: Its name.}
                  secret: {type: string, x-bridge-hidden: true}
  /api/things:
    get:
      operationId: things.list
      responses:
        "200": {description: ok}
`

func server(t *testing.T) (*Server, *[]*http.Request) {
	t.Helper()
	d, err := apicatalogue.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var seen []*http.Request
	mux := http.NewServeMux()
	mux.HandleFunc("/api/things/", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		switch {
		case r.Header.Get("Authorization") != "Bearer good":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"not signed in"}`))
		case strings.HasSuffix(r.URL.Path, "/0"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"id must be positive"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"n","secret":"s","extra":1}`))
		}
	})
	s, err := New("things", "1", d, mux)
	if err != nil {
		t.Fatal(err)
	}
	return s, &seen
}

func rpc(t *testing.T, s *Server, token, method string, params interface{}) map[string]interface{} {
	t.Helper()
	b, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	r := httptest.NewRequest("POST", "/api/mcp", bytes.NewReader(b))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %s", method, w.Body.String())
	}
	return out
}

func text(res map[string]interface{}) (string, bool) {
	r := res["result"].(map[string]interface{})
	c := r["content"].([]interface{})[0].(map[string]interface{})
	return c["text"].(string), r["isError"].(bool)
}

func TestToolsAreTheMCPOperations(t *testing.T) {
	s, _ := server(t)
	tools := s.Tools()
	if len(tools) != 1 || tools[0].Name != "things.get" {
		t.Fatalf("tools = %+v", tools)
	}
	tl := tools[0]
	if !strings.Contains(tl.Description, "One thing") || !strings.Contains(tl.Description, "name: string — Its name.") ||
		strings.Contains(tl.Description, "secret") {
		t.Errorf("description:\n%s", tl.Description)
	}
	props := tl.InputSchema["properties"].(map[string]interface{})
	if _, ok := props["userId"]; ok {
		t.Error("a bound parameter is offered")
	}
	if _, ok := props["id"]; !ok || tl.InputSchema["required"].([]string)[0] != "id" {
		t.Errorf("input schema = %v", tl.InputSchema)
	}
	init := rpc(t, s, "good", "initialize", map[string]interface{}{})
	if init["result"].(map[string]interface{})["protocolVersion"] != ProtocolVersion {
		t.Errorf("initialize = %v", init)
	}
}

func TestACallIsTheAPICallWithTheCallersCredentials(t *testing.T) {
	s, seen := server(t)
	got, isErr := text(rpc(t, s, "good", "tools/call", map[string]interface{}{
		"name": "things.get", "arguments": map[string]interface{}{"id": 7, "filter": map[string]interface{}{"a": 1}},
	}))
	if isErr || got != `{"name":"n"}` {
		t.Errorf("answer = %s (error %v): undocumented and hidden fields must be cut", got, isErr)
	}
	r := (*seen)[0]
	if r.URL.Path != "/api/things/7" || r.URL.Query().Get("filter") != `{"a":1}` || r.URL.Query().Has("userId") {
		t.Errorf("request = %s", r.URL)
	}
}

func TestFailuresReadAsTheAPIsAnswer(t *testing.T) {
	s, seen := server(t)
	for _, c := range []struct {
		token string
		args  map[string]interface{}
		want  string
	}{
		{"bad", map[string]interface{}{"id": 7}, "things.get failed (401 Unauthorized): not signed in"},
		{"good", map[string]interface{}{"id": 0}, "Error: things.get refused the arguments: id must be positive"},
		{"good", map[string]interface{}{}, "Error: things.get: id is required"},
		{"good", map[string]interface{}{"id": 1, "userId": 9}, "Error: things.get: it has no argument userId"},
	} {
		got, isErr := text(rpc(t, s, c.token, "tools/call", map[string]interface{}{"name": "things.get", "arguments": c.args}))
		if !isErr || got != c.want {
			t.Errorf("%v: %q (error %v), want %q", c.args, got, isErr, c.want)
		}
	}
	if len(*seen) != 2 {
		t.Errorf("an argument refused here must not reach the API: %d calls", len(*seen))
	}
	if res := rpc(t, s, "good", "tools/call", map[string]interface{}{"name": "things.list"}); res["error"] == nil {
		t.Error("an operation not marked x-mcp must not be callable")
	}
}

func TestABoundRequiredParameterIsRefused(t *testing.T) {
	d, _ := apicatalogue.Parse([]byte(strings.Replace(doc, "{name: userId, in: query, x-bridge-bound: user,", "{name: userId, in: query, required: true, x-bridge-bound: user,", 1)))
	if _, err := New("things", "1", d, http.NewServeMux()); err == nil || !strings.Contains(err.Error(), "only the bridge sets") {
		t.Errorf("err = %v", err)
	}
}

// A document that marks nothing offers an empty list, not null: MCP's
// tools/list answers a list.
func TestNoToolsIsAnEmptyList(t *testing.T) {
	d, _ := apicatalogue.Parse([]byte(strings.Replace(doc, "x-mcp: read", "", 1)))
	s, err := New("things", "1", d, http.NewServeMux())
	if err != nil {
		t.Fatal(err)
	}
	tools, ok := rpc(t, s, "good", "tools/list", nil)["result"].(map[string]interface{})["tools"].([]interface{})
	if !ok || len(tools) != 0 {
		t.Errorf("tools = %v, want []", tools)
	}
}
