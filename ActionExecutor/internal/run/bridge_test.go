// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins how a routine runs (PA-BR-09): on the read-write API bridge, for the
// run's user AND role, with its tool runs named in the run's audit entry, and
// what it creates owned by the run's user whatever owner it names
// (PA-BR-03); and how it is validated against the mock answers (PA-VAL-02).

package run

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"tachyonik/actionexecutor/internal/config"
	"tachyonik/actionexecutor/internal/jsruntime"
	libbridge "tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
	"tachyonik/lib/apiholder"
)

func toolDocument() string {
	var ex strings.Builder
	for _, sc := range apicatalogue.Scenarios {
		ex.WriteString("                " + sc + ": { value: { tools: [ { id: 40 } ] } }\n")
	}
	return `openapi: 3.1.0
info: { title: Tools, version: "1" }
paths:
  /api/tools:
    get:
      operationId: toolRecords.list
      x-bridge: read
      x-invalidated-by: [TOOL_CREATED]
      parameters:
        - { name: X-User-ID, in: header, x-bridge-bound: user, schema: { type: integer } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { type: object, properties: { tools: { type: array, items: { type: object, properties: { id: { type: integer } } } } } }
              examples:
` + ex.String() + `
  /api/tools/execute:
    post:
      operationId: toolCalls.execute
      x-bridge: write
      parameters:
        - { name: X-User-ID, in: header, x-bridge-bound: user, schema: { type: integer } }
        - { name: X-User-Role, in: header, x-bridge-bound: role, schema: { type: string } }
      requestBody:
        content:
          application/json:
            schema: { type: object, properties: { toolId: { type: integer } } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  toolName: { type: string }
                  proxyName: { type: string }
                  content: { type: string }
                  isError: { type: boolean }
  /api/tool-notes:
    post:
      operationId: toolNotes.create
      x-bridge: write
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                userId: { type: integer, x-bridge-bound: user }
                text: { type: string }
      responses:
        "201":
          description: created
          content:
            application/json:
              schema: { type: object, properties: { id: { type: integer } } }
`
}

type messages struct {
	mu   sync.Mutex
	text []string
}

func (m *messages) CreateAuditEvent(userID int64, level, module, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.text = append(m.text, message)
	return nil
}

func catalogueFor(t *testing.T, url string) *apiholder.Holder {
	t.Helper()
	h := apiholder.New(map[string]apiholder.Endpoint{"toolmanager": {BaseURL: url, ServiceKey: "k"}}, libbridge.ReadWrite, libbridge.Options{})
	if err := h.Load("fp", []apiholder.Document{{Module: "toolmanager", Document: toolDocument()}}); err != nil {
		t.Fatal(err)
	}
	return h
}

const toolRoutine = `var routineContract = 2;
var executors = [{ name: "scan", ruleId: 4, run: function (api) {
  var tools = api.toolRecords.list().tools;
  var r = api.toolCalls.execute({body: {toolId: tools[0].id}});
  return { status: r.isError ? "error" : "success", message: "ran " + r.toolName };
} }];`

func TestABridgeRoutineRunsForTheUserAndRole(t *testing.T) {
	var mu sync.Mutex
	var who, role string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tools/execute" {
			mu.Lock()
			who, role = r.Header.Get("X-User-ID"), r.Header.Get("X-User-Role")
			mu.Unlock()
			w.Write([]byte(`{"toolName":"Nmap","proxyName":"office","content":"done","isError":false}`))
			return
		}
		w.Write([]byte(`{"tools":[{"id":40}]}`))
	}))
	t.Cleanup(srv.Close)

	audit := &messages{}
	cfg := &config.Config{}
	cfg.Execution.TimeoutSeconds = 5
	r := New(cfg, nil, audit)
	r.SetCatalogue(catalogueFor(t, srv.URL))

	rt := jsruntime.New()
	if err := rt.LoadFromString(toolRoutine); err != nil {
		t.Fatalf("load: %v", err)
	}
	result, err := r.Execute(4, rt, AuthContext{UserID: 7, UserRole: "admin"}, "Execution rule \"Scan\" (id 4) run manually")
	if err != nil || result.Status != "success" || result.Message != "ran Nmap" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if who != "7" || role != "admin" {
		t.Errorf("the tool ran for user %q role %q, want 7 and admin", who, role)
	}
	audit.mu.Lock()
	defer audit.mu.Unlock()
	if len(audit.text) != 1 || !strings.Contains(audit.text[0], "Nmap") {
		t.Errorf("audit = %v, want the tool named", audit.text)
	}
}

func TestWhatARoutineCreatesBelongsToTheRunsUser(t *testing.T) {
	var mu sync.Mutex
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.Execution.TimeoutSeconds = 5
	r := New(cfg, nil, nil)
	r.SetCatalogue(catalogueFor(t, srv.URL))
	rt := jsruntime.New()
	if err := rt.LoadFromString(`var routineContract = 2;
var executors = [{ name: "note", ruleId: 4, run: function (api) {
  api.toolNotes.create({body: {userId: 99, text: "hi"}});
  return { status: "success" };
} }];`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(4, rt, AuthContext{UserID: 7}, "s"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(body, `"userId":7`) || strings.Contains(body, "99") {
		t.Errorf("created with body %s, want userId 7", body)
	}
}

func TestBridgeRoutineValidation(t *testing.T) {
	b, _ := catalogueFor(t, "http://127.0.0.1:1").Bridge()
	rt := jsruntime.New()
	if err := rt.LoadFromString(toolRoutine); err != nil {
		t.Fatal(err)
	}
	v := rt.ValidateBridge(b, 5*time.Second)
	if len(v.Errors) != 0 || strings.Join(v.Operations, ",") != "toolCalls.execute,toolRecords.list" {
		t.Errorf("validation = %+v", v)
	}

	bad := jsruntime.New()
	if err := bad.LoadFromString(strings.Replace(toolRoutine, "tools[0].id", "tools[0].identifier", 1)); err != nil {
		t.Fatal(err)
	}
	v = bad.ValidateBridge(b, 5*time.Second)
	if len(v.Errors) != 1 || !strings.Contains(v.Errors[0], "reads tools[].identifier") {
		t.Errorf("errors = %v", v.Errors)
	}
	if v := bad.ValidateBridge(nil, time.Second); len(v.Errors) != 1 {
		t.Errorf("without a catalogue: %v", v.Errors)
	}
}
