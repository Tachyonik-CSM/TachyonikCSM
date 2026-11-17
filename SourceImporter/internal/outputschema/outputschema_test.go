// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the record schemas against AssetManager's real document: every field
// the importer declares as its own is in the create body, the routine's Go
// result types and the record schemas name the same fields, the rendering
// says what a routine needs, and Check names the record and field that do
// not fit.

package outputschema

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"tachyonik/lib/apicatalogue"
	"tachyonik/lib/apicontract"
	"tachyonik/sourceimporter/internal/jsruntime"
)

// assetManagerDocument is AssetManager/spec/openapi.yaml, parsed; the test is
// skipped when the module is built without its siblings.
func assetManagerDocument(t *testing.T) (*apicatalogue.Document, string) {
	t.Helper()
	path := apicontract.DocumentPath("AssetManager")
	if path == "" {
		t.Skip("AssetManager/spec/openapi.yaml not found: the module is built without its siblings")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := apicatalogue.Parse(data)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return doc, string(data)
}

func schemas(t *testing.T) *Schemas {
	t.Helper()
	doc, _ := assetManagerDocument(t)
	s, err := FromDocument(doc)
	if err != nil {
		t.Fatalf("the record schemas cannot be read from AssetManager's document: %v", err)
	}
	return s
}

// Every field the importer declares as its own is in the body it sends:
// a field renamed in AssetManager fails here, not in production.
func TestTheImporterSetFieldsAreInTheCreateBodies(t *testing.T) {
	doc, _ := assetManagerDocument(t)
	for _, k := range kinds {
		body, err := createBody(doc, k)
		if err != nil {
			t.Fatal(err)
		}
		if len(ImporterSet[k.name]) == 0 {
			t.Errorf("%s: no importer-set fields declared", k.name)
		}
		for field, why := range ImporterSet[k.name] {
			if _, ok := body.Properties[field]; !ok {
				t.Errorf("%s %s has no %s, which the importer sets (%s)", k.method, k.path, field, why)
			}
			if strings.TrimSpace(why) == "" {
				t.Errorf("%s.%s: no reason given why the importer sets it", k.name, field)
			}
		}
	}
}

func TestAnImporterSetFieldMissingFromTheBodyIsAnError(t *testing.T) {
	doc, _ := assetManagerDocument(t)
	op := doc.Operation("detections.create")
	if op == nil || op.RequestBody == nil {
		t.Fatal("detections.create has no body")
	}
	delete(op.RequestBody.Properties, "sourceLabel")
	if _, err := FromDocument(doc); err == nil || !strings.Contains(err.Error(), "sourceLabel") {
		t.Errorf("FromDocument = %v, want an error naming sourceLabel", err)
	}
}

// The routine's Go result types and the record schemas name the same
// fields: a record field the Go type lacked would be dropped silently on
// the way to AssetManager, and a Go field the schema lacks is sent unread.
func TestTheResultTypesMatchTheRecordSchemas(t *testing.T) {
	s := schemas(t)
	for kind, v := range map[string]interface{}{
		Assets:          jsruntime.AssetResult{},
		Vulnerabilities: jsruntime.VulnerabilityResult{},
		Detections:      jsruntime.DetectionResult{},
		ScanCoverage:    jsruntime.ScanCoverageResult{},
	} {
		goFields := jsonFields(reflect.TypeOf(v))
		var schemaFields []string
		for name := range s.Record(kind).Properties {
			schemaFields = append(schemaFields, name)
		}
		sort.Strings(schemaFields)
		if !reflect.DeepEqual(goFields, schemaFields) {
			t.Errorf("%s: the Go type has %v, the record schema %v", kind, goFields, schemaFields)
		}
	}
	// And the return object has no further array a routine could fill.
	if got := jsonFieldsOfResult(); !reflect.DeepEqual(got, Kinds()) {
		t.Errorf("ImportResult has %v, the kinds are %v", got, Kinds())
	}
}

func jsonFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// jsonFieldsOfResult are the record arrays ImportResult converts, by the
// names extractResult reads them under.
func jsonFieldsOfResult() []string {
	var out []string
	t := reflect.TypeOf(jsruntime.ImportResult{})
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Type.Kind() == reflect.Slice {
			n := t.Field(i).Name
			out = append(out, strings.ToLower(n[:1])+n[1:])
		}
	}
	return out
}

func TestTheRenderingDescribesEachRecord(t *testing.T) {
	text := schemas(t).Render()
	for _, want := range []string{
		"assets: [ <asset record>, ... ],",
		"scanCoverage: [ <scan coverage record>, ... ]",
		"- name: string, required",
		`- type: string (one of "Host"), required`,
		"- severity: integer, required",
		"- port: string, required",
		"- lastSeen: string, optional",
		"- target: string, required",
		"- scanCoverageIntervalDays: integer, required",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the rendering lacks %q:\n%s", want, text)
		}
	}
	for _, never := range []string{"sourceId", "sourceLabel", "userId", "- source:"} {
		if strings.Contains(text, never) {
			t.Errorf("the rendering offers %q, which the importer sets or AssetManager deprecates:\n%s", never, text)
		}
	}
}

func TestCheckNamesTheRecordAndField(t *testing.T) {
	s := schemas(t)
	got := s.Check(map[string][]interface{}{
		Assets: {
			map[string]interface{}{"name": "192.168.1.1", "type": "Host", "lastSeen": "2025-07-01T00:00:00Z"},
			map[string]interface{}{"name": "192.168.1.2", "type": "server"},
			map[string]interface{}{"type": "Host"},
		},
		Vulnerabilities: {
			map[string]interface{}{"name": "SQLi", "host": "192.168.1.1", "port": "443/tcp", "severity": 7.5},
			map[string]interface{}{"name": "SQLi", "host": "192.168.1.1", "port": "443/tcp", "severity": int64(75)},
		},
		Detections: {"not a record"},
	})
	if len(got) != 3 {
		t.Fatalf("Check = %v, want violations of three kinds", got)
	}
	if got[0].Kind != Assets || got[0].Count != 2 ||
		!strings.HasPrefix(got[0].Examples[0], "assets[1]: type:") || got[0].Examples[1] != "assets[2]: name is missing" {
		t.Errorf("assets: %+v", got[0])
	}
	if got[1].Kind != Vulnerabilities || got[1].Count != 1 || !strings.HasPrefix(got[1].Examples[0], "vulnerabilities[0]: severity:") {
		t.Errorf("vulnerabilities: %+v", got[1])
	}
	if got[2].Kind != Detections || !strings.Contains(got[2].Examples[0], "is not object") {
		t.Errorf("detections: %+v", got[2])
	}
}

func TestCheckGivesAFewExamplesAndCountsTheRest(t *testing.T) {
	var bad []interface{}
	for i := 0; i < 10; i++ {
		bad = append(bad, map[string]interface{}{"name": "x"})
	}
	got := schemas(t).Check(map[string][]interface{}{Assets: bad})
	if len(got) != 1 || got[0].Count != 10 || len(got[0].Examples) != MaxExamples {
		t.Fatalf("Check = %+v", got)
	}
	if s := got[0].String(); !strings.HasSuffix(s, "; and 7 more") {
		t.Errorf("String = %q", s)
	}
}

func TestTheHolderKeepsTheLastReadableDocument(t *testing.T) {
	_, text := assetManagerDocument(t)
	var h Holder
	if h.Current() != nil {
		t.Fatal("an empty holder has schemas")
	}
	if err := h.Load(text); err != nil {
		t.Fatal(err)
	}
	before := h.Current()
	if err := h.Load("openapi: [unreadable"); err == nil {
		t.Error("an unreadable document was loaded")
	}
	if h.Current() != before {
		t.Error("an unreadable document took away the schemas loaded before")
	}
}
