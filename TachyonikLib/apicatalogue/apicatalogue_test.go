// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins how an API document is read and judged: $ref and allOf resolution,
// examples that look like JSON, the catalogue's rules (Check), schema
// validation, filtering, documented paths, refused keywords, and that the
// scenario list matches docs/specs/mock-scenarios.md.

package apicatalogue

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// sample builds a small document with one read bridge operation, whose
// examples are given for every scenario by exampleFor.
func sample(t *testing.T, extra string) *Document {
	t.Helper()
	var ex strings.Builder
	for _, sc := range Scenarios {
		ex.WriteString("                " + sc + ":\n                  value: { items: [ { id: 1, name: a, seen: 2025-05-01T00:00:00Z, gone: null } ], total: 1 }\n")
	}
	src := `openapi: 3.1.0
info: { title: Test, version: "1.0" }
paths:
  /api/things:
    get:
      operationId: things.list
      x-bridge: read
      x-invalidated-by: [THING_CREATED]
      parameters:
        - $ref: "#/components/parameters/user"
        - name: filter
          in: query
          content:
            application/json:
              schema: { type: object }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                required: [items]
                properties:
                  items: { type: array, items: { $ref: "#/components/schemas/Thing" } }
                  total: { type: integer }
              examples:
` + ex.String() + extra + `
components:
  parameters:
    user: { name: userId, in: query, x-bridge-bound: user, schema: { type: integer } }
  schemas:
    Thing:
      allOf:
        - type: object
          properties:
            id: { type: integer }
            name: { type: string }
        - properties:
            seen: { type: string, format: date-time }
            gone: { type: [string, "null"] }
`
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func TestParseResolvesRefsAndAllOf(t *testing.T) {
	doc := sample(t, "")
	op := doc.Operation("things.list")
	if op == nil {
		t.Fatal("things.list not found")
	}
	if op.Method != "GET" || op.Path != "/api/things" || op.Bridge != ExposeRead || op.Namespace() != "things" {
		t.Errorf("operation = %+v", op)
	}
	if len(op.Parameters) != 2 || op.Parameters[0].Bound != BoundUser || !op.Parameters[1].JSON {
		t.Errorf("parameters = %+v %+v", op.Parameters[0], op.Parameters[1])
	}
	thing := op.Response.Properties["items"].Items
	for _, f := range []string{"id", "name", "seen", "gone"} {
		if thing.Properties[f] == nil {
			t.Errorf("allOf lost field %s", f)
		}
	}
	if !thing.Properties["gone"].Nullable() {
		t.Error("gone should be nullable")
	}
}

// YAML reads an unquoted time as a timestamp and a number as int; an example
// must look like the JSON a manager would send.
func TestExamplesLookLikeJSON(t *testing.T) {
	op := sample(t, "").Operation("things.list")
	ex := op.Examples[Scenarios[0]].(map[string]interface{})
	item := ex["items"].([]interface{})[0].(map[string]interface{})
	if item["seen"] != "2025-05-01T00:00:00Z" {
		t.Errorf("seen = %#v, want RFC 3339 text", item["seen"])
	}
	if item["id"] != float64(1) {
		t.Errorf("id = %#v, want float64", item["id"])
	}
}

// PA-CAT-03 to -08 as far as a document alone shows them.
func TestCheck(t *testing.T) {
	if errs := sample(t, "").Check(); len(errs) != 0 {
		t.Fatalf("a fit document: %v", errs)
	}
	bad := sample(t, `                not-a-scenario:
                  value: { items: [] }
`)
	if errs := bad.Check(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "not a mock scenario") {
		t.Errorf("unknown example: %v", errs)
	}

	doc := sample(t, "")
	op := doc.Operations[0]
	op.ID = "ListThings"
	op.InvalidatedBy = nil
	delete(op.Examples, Scenarios[1])
	op.Examples[Scenarios[2]] = map[string]interface{}{"items": "no"}
	want := []string{"lowerCamelCase", "x-invalidated-by", "no example", "is not array"}
	errs := doc.Check()
	if len(errs) != len(want) {
		t.Fatalf("errors = %v", errs)
	}
	for i, w := range want {
		if !strings.Contains(errs[i].Error(), w) {
			t.Errorf("error %d = %v, want %q", i, errs[i], w)
		}
	}

	op.ID, op.Method, op.Bridge = "things.list", "POST", ExposeRead
	if errs := doc.Check(); !strings.Contains(errs[0].Error(), "GET only") {
		t.Errorf("read on POST: %v", errs)
	}
}

func TestValidOperationID(t *testing.T) {
	for id, want := range map[string]bool{
		"assets.list": true, "assets.deleteByIds": true, "assetManager.health": true,
		"ListAssets": false, "assets": false, "assets.list.more": false, "Assets.list": false, "assets.List": false,
	} {
		if got := ValidOperationID(id); got != want {
			t.Errorf("%s: %v, want %v", id, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	s := sample(t, "").Operation("things.list").Response
	ok := map[string]interface{}{"items": []interface{}{map[string]interface{}{"id": float64(2), "gone": nil, "extra": true}}}
	if err := s.Validate(ok); err != nil {
		t.Errorf("valid answer: %v", err)
	}
	for _, c := range []struct {
		v    interface{}
		want string
	}{
		{map[string]interface{}{}, "items is missing"},
		{map[string]interface{}{"items": []interface{}{map[string]interface{}{"id": 1.5}}}, "items[0].id: a number is not integer"},
		{map[string]interface{}{"items": []interface{}{map[string]interface{}{"name": nil}}}, "items[0].name: null is not string"},
	} {
		err := s.Validate(c.v)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%v) = %v, want %q", c.v, err, c.want)
		}
	}
}

// PA-BR-04: only documented fields reach a routine.
func TestFilter(t *testing.T) {
	s := sample(t, "").Operation("things.list").Response
	in := map[string]interface{}{
		"items":  []interface{}{map[string]interface{}{"id": float64(1), "secret": "x"}},
		"total":  float64(1),
		"apiKey": "k",
	}
	want := map[string]interface{}{
		"items": []interface{}{map[string]interface{}{"id": float64(1)}},
		"total": float64(1),
	}
	if got := s.Filter(in); !reflect.DeepEqual(got, want) {
		t.Errorf("Filter = %v, want %v", got, want)
	}
}

func TestHasPath(t *testing.T) {
	s := sample(t, "").Operation("things.list").Response
	for path, want := range map[string]bool{
		"items": true, "items[].name": true, "items.length": true, "total": true,
		"items[].nmae": false, "count": false, "items[].id[]": false,
	} {
		if got := s.HasPath(path); got != want {
			t.Errorf("HasPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestUnsupportedKeywordsAreRefused(t *testing.T) {
	_, err := Parse([]byte(`openapi: 3.1.0
info: { title: T, version: "1" }
paths:
  /x:
    get:
      operationId: x.get
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { oneOf: [ { type: string }, { type: integer } ] }
`))
	if err == nil || !strings.Contains(err.Error(), "oneOf") {
		t.Errorf("oneOf: %v", err)
	}
	if _, err := Parse([]byte("openapi: 3.0.3\n")); err == nil {
		t.Error("3.0 must be refused")
	}
}

// PA-VAL-01: the scenario names here are the ones the companion list names.
func TestScenariosMatchTheSpecification(t *testing.T) {
	data, err := os.ReadFile("../../docs/specs/mock-scenarios.md")
	if err != nil {
		t.Fatalf("read the list: %v", err)
	}
	var names []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\|").FindAllStringSubmatch(string(data), -1) {
		names = append(names, m[1])
	}
	if !reflect.DeepEqual(names, Scenarios) {
		t.Errorf("docs/specs/mock-scenarios.md names %v, the code %v", names, Scenarios)
	}
}

// PA-CAT-11: a field marked x-bridge-hidden is documented — it validates —
// but routines do not get it and may not read it.
func TestHiddenFields(t *testing.T) {
	d, err := Parse([]byte(`openapi: 3.1.0
info: { title: T, version: "1" }
paths:
  /api/tools:
    get:
      operationId: tools.list
      x-bridge: read
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  name: { type: string }
                  hostKey: { type: string, x-bridge-hidden: true }
`))
	if err != nil {
		t.Fatal(err)
	}
	s := d.Operation("tools.list").Response
	v := map[string]interface{}{"name": "n", "hostKey": "ssh-ed25519 AAAA"}
	if err := s.Validate(v); err != nil {
		t.Errorf("a hidden field is still documented: %v", err)
	}
	if got := s.Filter(v); !reflect.DeepEqual(got, map[string]interface{}{"name": "n"}) {
		t.Errorf("Filter = %v", got)
	}
	if s.HasPath("hostKey") || !s.HasPath("name") {
		t.Error("a hidden field must not be readable by routines")
	}
}
