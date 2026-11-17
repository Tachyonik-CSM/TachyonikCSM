// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins what Check reports of a client's declared calls against a document,
// and how Covered matches the paths written in a client's code.

package apicontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tachyonik/lib/apicatalogue"
)

const doc = `openapi: 3.1.0
info: {title: T, version: "1"}
paths:
  /api/sources:
    get:
      operationId: sources.list
      parameters:
        - {name: limit, in: query, schema: {type: integer}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  sources:
                    type: array
                    items:
                      type: object
                      properties:
                        id: {type: integer}
                        name: {type: string}
                        createdAt: {type: string}
                        meta: {type: object}
                  maxSources: {type: integer}
    post:
      operationId: sources.create
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
      responses:
        "2XX": {description: Success.}
  /api/sources/{id}:
    delete:
      operationId: sources.delete
      responses:
        "204": {description: gone}
`

type source struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	CreatedAt time.Time      `json:"createdAt"`
	Meta      map[string]any `json:"meta"`
	Meta2     struct {
		Deep string `json:"deep"`
	} `json:"-"`
	internal int //nolint:unused
}

type list struct {
	Entries    []source `json:"entries"`
	Sources    []source `json:"sources"`
	MaxSources int      `json:"maxSources"`
}

type created struct {
	ID int64 `json:"id"`
}

func parsed(t *testing.T) *apicatalogue.Document {
	t.Helper()
	d, err := apicatalogue.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCheck(t *testing.T) {
	got := strings.Join(Check(parsed(t), []Call{
		{Method: "GET", Path: "/api/sources?limit=5", Query: []string{"limit", "offset"}, Answer: list{}},
		{Method: "post", Path: "/api/sources", Request: map[string]any{"x": 1}, Answer: &created{}},
		{Method: "DELETE", Path: "/api/sources/%d"},
		{Method: "PUT", Path: "/api/sources/{id}"},
	}), "\n")
	for _, want := range []string{
		`GET /api/sources?limit=5 (sources.list): query parameter "offset" is not documented`,
		`GET /api/sources?limit=5 (sources.list): entries is not in the documented answer`,
		`POST /api/sources (sources.create): the answer is not documented`,
		`PUT /api/sources/{id}: not in the document`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, not := range []string{"sources[].id", "createdAt", "meta", "deep", "internal", "DELETE"} {
		if strings.Contains(got, not) {
			t.Errorf("%q reported:\n%s", not, got)
		}
	}
}

func TestCheckRequestFields(t *testing.T) {
	type body struct {
		Name  string `json:"name"`
		Owner int64  `json:"owner"`
	}
	got := Check(parsed(t), []Call{{Method: "POST", Path: "/api/sources", Request: body{}}})
	if len(got) != 1 || !strings.Contains(got[0], "owner is not in the documented request body") {
		t.Errorf("got %v", got)
	}
}

func TestCovered(t *testing.T) {
	dir := t.TempDir()
	src := `package c
const a = "/api/sources"
var b = "/api/sources/" + "1"
var c = "/api/other?x=1"
var d = "not a path"
var e = fmt.Sprintf("%s/api/sources/%d", base, 1)
var f = fmt.Sprintf("%s/api/unknown", base)
`
	if err := os.WriteFile(filepath.Join(dir, "c.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "c_test.go"), []byte("package c\nvar z = \"/api/ignored\"\n"), 0o600)
	got, err := Covered(dir, []Call{{Method: "GET", Path: "/api/sources"}, {Method: "DELETE", Path: "/api/sources/{id}"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "/api/other?x=1,/api/unknown" {
		t.Errorf("uncovered = %v", got)
	}
}

type encoded struct{ hidden string }

func (encoded) MarshalJSON() ([]byte, error) {
	return []byte(`{"sources":[{"id":1,"colour":"red"}],"maxSources":3,"extra":true}`), nil
}

// A type with its own MarshalJSON is checked by what it encodes.
func TestCheckAMarshaler(t *testing.T) {
	got := strings.Join(Check(parsed(t), []Call{{Method: "GET", Path: "/api/sources", Answer: encoded{}}}), "\n")
	if !strings.Contains(got, ": extra is not") || !strings.Contains(got, ": sources[].colour is not") || strings.Contains(got, "maxSources") {
		t.Errorf("got:\n%s", got)
	}
}

func TestCheckAnswer(t *testing.T) {
	d := parsed(t)
	if got := CheckAnswer(d, "GET", "/api/sources", []byte(`{"sources":[{"id":1,"name":"a"}],"maxSources":2}`)); len(got) != 0 {
		t.Errorf("a documented answer: %v", got)
	}
	got := strings.Join(CheckAnswer(d, "GET", "/api/sources", []byte(`{"sources":[{"id":"x"}],"total":2}`)), "\n")
	if !strings.Contains(got, "total is not in the documented answer") || !strings.Contains(got, "sources[0].id") {
		t.Errorf("got:\n%s", got)
	}
	if got := CheckAnswer(d, "DELETE", "/api/sources/7", []byte(`{}`)); len(got) != 1 || !strings.Contains(got[0], "(sources.delete)") {
		t.Errorf("a real path through a parameter: %v", got)
	}
	if got := CheckAnswer(d, "POST", "/api/sources", []byte(`{}`)); len(got) != 1 || !strings.Contains(got[0], "not documented") {
		t.Errorf("an undocumented answer: %v", got)
	}
}
