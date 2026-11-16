// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the API bridge against a fake manager: the call is the documented one,
// made for the bound user; only documented arguments are taken and only
// documented fields returned; answers are cached per pass and writes clear
// them; failures throw; a mock pass answers from the examples and reports
// what a routine did that the documentation does not allow (PA-BR, PA-VAL-02).

package apibridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dop251/goja"

	"tachyonik/lib/apicatalogue"
)

func document(t *testing.T) *apicatalogue.Document {
	t.Helper()
	var ex strings.Builder
	for _, sc := range apicatalogue.Scenarios {
		ex.WriteString("                " + sc + ":\n                  value: { things: [ { id: 7, name: mock } ] }\n")
	}
	doc, err := apicatalogue.Parse([]byte(`openapi: 3.1.0
info: { title: Things, version: "1" }
paths:
  /api/things:
    get:
      operationId: things.list
      x-bridge: read
      x-invalidated-by: [THING_CREATED]
      parameters:
        - { name: userId, in: query, x-bridge-bound: user, schema: { type: integer } }
        - { name: filter, in: query, content: { application/json: { schema: { type: object } } } }
        - { name: owner, in: query, x-bridge-bound: omit, schema: { type: integer } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                required: [things]
                properties:
                  things:
                    type: array
                    items:
                      type: object
                      properties:
                        id: { type: integer }
                        name: { type: string }
              examples:
` + ex.String() + `
    post:
      operationId: things.create
      x-bridge: write
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: { type: string }
                userId: { type: integer, x-bridge-bound: user }
      responses:
        "201":
          description: created
          content:
            application/json:
              schema: { type: object, properties: { id: { type: integer } } }
  /api/things/{id}:
    get:
      operationId: things.get
      x-bridge: read
      x-invalidated-by: [THING_CREATED]
      parameters:
        - { name: id, in: path, required: true, schema: { type: integer } }
        - { name: X-User-ID, in: header, x-bridge-bound: user, schema: { type: integer } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { type: object, properties: { id: { type: integer } } }
              examples:
` + strings.ReplaceAll(ex.String(), "{ things: [ { id: 7, name: mock } ] }", "{ id: 7 }") + `
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if errs := doc.Check(); len(errs) > 0 {
		t.Fatalf("Check: %v", errs)
	}
	return doc
}

// manager is a fake manager recording what it was asked.
type manager struct {
	srv      *httptest.Server
	requests atomic.Int32
	last     atomic.Value // *http.Request
	lastBody atomic.Value // string
	status   int
	answer   string
	delay    time.Duration
}

func newManager(t *testing.T) *manager {
	m := &manager{status: 200, answer: `{"things":[{"id":1,"name":"a","secret":"s"}],"apiKey":"k"}`}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requests.Add(1)
		m.last.Store(r)
		b, _ := io.ReadAll(r.Body)
		m.lastBody.Store(string(b))
		if m.delay > 0 {
			time.Sleep(m.delay)
		}
		w.WriteHeader(m.status)
		if r.Method == "POST" {
			io.WriteString(w, `{"id":9}`)
			return
		}
		io.WriteString(w, m.answer)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func bridge(t *testing.T, m *manager, profile Profile) *Bridge {
	t.Helper()
	b, err := New([]Module{{Name: "ThingManager", Document: document(t), BaseURL: m.srv.URL, ServiceKey: "svc"}}, profile, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

// run evaluates script with `api` bound for user 42 and returns its value
// exported, or the thrown error.
func run(t *testing.T, p *Pass, script string) (interface{}, error) {
	t.Helper()
	vm := goja.New()
	_ = vm.Set("api", p.Object(vm, 42))
	v, err := vm.RunString(script)
	if err != nil {
		return nil, err
	}
	return v.Export(), nil
}

// PA-BR-02, -03: the documented call, for the evaluation's user, whatever the
// routine says; the service key attached.
func TestCallIsMadeForTheBoundUser(t *testing.T) {
	m := newManager(t)
	p := bridge(t, m, ReadOnly).NewPass()
	if _, err := run(t, p, `api.things.list({filter: {rules: []}})`); err != nil {
		t.Fatal(err)
	}
	r := m.last.Load().(*http.Request)
	if r.Method != "GET" || r.URL.Path != "/api/things" || r.Header.Get("X-Internal-Service-Key") != "svc" {
		t.Errorf("request = %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("X-Internal-Service-Key"))
	}
	if r.URL.Query().Get("userId") != "42" || r.URL.Query().Get("filter") != `{"rules":[]}` {
		t.Errorf("query = %s", r.URL.RawQuery)
	}

	_, err := run(t, p, `api.things.list({userId: 1})`)
	if err != nil {
		t.Fatal(err)
	}
	// Answered from the cache — and the bound value was never the routine's.
	if q := m.last.Load().(*http.Request).URL.Query().Get("userId"); q != "42" {
		t.Errorf("userId = %s", q)
	}
}

// PA-BR-03 for a user named by header: set by the bridge, not settable by
// the routine.
func TestBoundHeader(t *testing.T) {
	m := newManager(t)
	m.answer = `{"id":5}`
	if _, err := run(t, bridge(t, m, ReadOnly).NewPass(), `api.things.get({id: 5})`); err != nil {
		t.Fatal(err)
	}
	r := m.last.Load().(*http.Request)
	if r.URL.Path != "/api/things/5" || r.Header.Get("X-User-ID") != "42" {
		t.Errorf("request = %s X-User-ID=%q", r.URL.Path, r.Header.Get("X-User-ID"))
	}
	if _, err := run(t, bridge(t, m, ReadOnly).NewPass(), `api.things.get({id: 5, "X-User-ID": 1})`); err == nil {
		t.Error("a routine naming a header must be refused")
	}
}

func TestUnknownArgumentThrows(t *testing.T) {
	m := newManager(t)
	_, err := run(t, bridge(t, m, ReadOnly).NewPass(), `api.things.list({fliter: {}})`)
	if err == nil || !strings.Contains(err.Error(), `no parameter "fliter"`) {
		t.Errorf("err = %v", err)
	}
	if m.requests.Load() != 0 {
		t.Error("a refused call must not be made")
	}
	_, err = run(t, bridge(t, m, ReadOnly).NewPass(), `api.things.list({owner: 5})`)
	if err == nil || !strings.Contains(err.Error(), `no parameter "owner"`) {
		t.Errorf("an omitted parameter must be refused: %v", err)
	}
	_, err = run(t, bridge(t, m, ReadOnly).NewPass(), `api.things.get({})`)
	if err == nil || !strings.Contains(err.Error(), `"id" is required`) {
		t.Errorf("missing path parameter: %v", err)
	}
}

// PA-BR-04: only documented fields reach the routine.
func TestUndocumentedFieldsAreDropped(t *testing.T) {
	m := newManager(t)
	// Field by field: a Go map has no key order, so JSON.stringify of one
	// does not either.
	v, err := run(t, bridge(t, m, ReadOnly).NewPass(),
		`var a = api.things.list(); var t = a.things[0];
		 [Object.keys(a).sort().join("+"), Object.keys(t).sort().join("+"), t.id, t.name].join(",")`)
	if err != nil {
		t.Fatal(err)
	}
	if v != "things,id+name,1,a" {
		t.Errorf("answer = %v", v)
	}
}

// PA-BR-05: one call per user and arguments within a pass; a routine
// writing to its answer does not change what the next one gets.
func TestPassCache(t *testing.T) {
	m := newManager(t)
	b := bridge(t, m, ReadOnly)
	p := b.NewPass()
	if _, err := run(t, p, `var a = api.things.list(); a.things[0].name = "changed"; api.things.list({filter: {x: 1}})`); err != nil {
		t.Fatal(err)
	}
	v, err := run(t, p, `api.things.list().things[0].name`)
	if err != nil {
		t.Fatal(err)
	}
	if v != "a" {
		t.Errorf("cached answer was changed by a routine: %v", v)
	}
	if n := m.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 (two distinct argument sets)", n)
	}
	if _, err := run(t, b.NewPass(), `api.things.list()`); err != nil || m.requests.Load() != 3 {
		t.Errorf("a new pass must call again: %v, %d", err, m.requests.Load())
	}
}

// PA-BR-08: the read profile has no writes; a write is made for the bound
// user and clears the cache.
func TestProfilesAndWrites(t *testing.T) {
	m := newManager(t)
	if _, err := run(t, bridge(t, m, ReadOnly).NewPass(), `api.things.create`); err != nil {
		t.Fatal(err)
	}
	if v, _ := run(t, bridge(t, m, ReadOnly).NewPass(), `typeof api.things.create`); v != "undefined" {
		t.Errorf("read profile offers create: %v", v)
	}

	p := bridge(t, m, ReadWrite).NewPass()
	v, err := run(t, p, `api.things.list(); var c = api.things.create({body: {name: "n", userId: 1}}); api.things.list(); c.id`)
	if err != nil {
		t.Fatal(err)
	}
	if v != int64(9) && v != float64(9) {
		t.Errorf("create answered %v", v)
	}
	if n := m.requests.Load(); n != 3 {
		t.Errorf("requests = %d, want 3: the read after the write must not come from the cache", n)
	}
	var body map[string]interface{}
	// The last request was the second list; check the create's body by
	// repeating it alone.
	if _, err := run(t, bridge(t, m, ReadWrite).NewPass(), `api.things.create({body: {name: "n", userId: 1}})`); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(m.lastBody.Load().(string)), &body)
	if body["userId"] != float64(42) || body["name"] != "n" {
		t.Errorf("body = %v", body)
	}
}

// PA-BR-06: a failure throws, naming the operation.
func TestFailuresThrow(t *testing.T) {
	m := newManager(t)
	m.status, m.answer = 503, `{"error":"limits unavailable"}`
	_, err := run(t, bridge(t, m, ReadOnly).NewPass(), `api.things.list()`)
	if err == nil || !strings.Contains(err.Error(), "api.things.list: ThingManager answered 503: limits unavailable") {
		t.Errorf("err = %v", err)
	}
	m.status, m.answer = 200, `{"things":"no"}`
	_, err = run(t, bridge(t, m, ReadOnly).NewPass(), `api.things.list()`)
	if err == nil || !strings.Contains(err.Error(), "does not match its documentation") {
		t.Errorf("err = %v", err)
	}
	// The routine can handle it.
	v, err := run(t, bridge(t, m, ReadOnly).NewPass(), `try { api.things.list(); "no" } catch (e) { e instanceof Error && e.message.indexOf("api.things.list") === 0 }`)
	if err != nil || v != true {
		t.Errorf("catch: %v %v", v, err)
	}
}

// PA-BR-10: a call ends with the context it is made under.
func TestCallEndsWithItsContext(t *testing.T) {
	m := newManager(t)
	m.delay = 300 * time.Millisecond
	p := bridge(t, m, ReadOnly).NewPass()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	p.SetContext(ctx)
	start := time.Now()
	_, err := run(t, p, `api.things.list()`)
	if err == nil || !strings.Contains(err.Error(), "did not answer in time") {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Errorf("the call outlived its context: %v", time.Since(start))
	}
}

func TestNamespaceBelongsToOneModule(t *testing.T) {
	doc := document(t)
	_, err := New([]Module{{Name: "A", Document: doc}, {Name: "B", Document: doc}}, ReadOnly, Options{})
	if err == nil || !strings.Contains(err.Error(), `namespace "things"`) {
		t.Errorf("err = %v", err)
	}
}

// PA-VAL-02: a mock pass answers from the examples, calls no one, and says
// what the routine did that the documentation does not allow.
func TestMockPass(t *testing.T) {
	m := newManager(t)
	b := bridge(t, m, ReadOnly)
	p := b.NewMockPass(apicatalogue.Scenarios[0])
	v, err := run(t, p, `var t = api.things.list().things[0]; t.nmae; t.id + api.things.get({id: 7}).id`)
	if err != nil {
		t.Fatal(err)
	}
	if v != int64(14) && v != float64(14) {
		t.Errorf("value = %v", v)
	}
	if m.requests.Load() != 0 {
		t.Error("a mock pass called the manager")
	}
	got := p.Violations()
	if len(got) != 1 || got[0] != "things.list: reads things[].nmae, which the answer does not have" {
		t.Errorf("violations = %v", got)
	}
	reads := strings.Join(p.Reads(), " ")
	for _, want := range []string{"things.list:things[].id", "things.get:id"} {
		if !strings.Contains(reads, want) {
			t.Errorf("reads %q lack %s", reads, want)
		}
	}

	p = b.NewMockPass(apicatalogue.Scenarios[0])
	if _, err := run(t, p, `try { api.things.list({nope: 1}) } catch (e) {}`); err != nil {
		t.Fatal(err)
	}
	if got := p.Violations(); len(got) != 1 || !strings.Contains(got[0], `no parameter "nope"`) {
		t.Errorf("a caught bad call must still count: %v", got)
	}
}
