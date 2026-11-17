// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package outputschema describes and checks the records an import routine
// returns — assets, vulnerabilities, detections and scan coverage rows — from
// AssetManager's API document, as SourceImporter loads it from the platform
// API catalogue (docs/plans/platform-api-follow-ups.md, phase 4).
//
// A record kind's schema is the request body of the AssetManager operation
// the importer creates it with, less the fields the importer sets itself
// (ImporterSet) and the deprecated ones; for scan coverage it is the schema of
// one entry of the table's body. AssetManager's document is thereby the one
// description of a record: the system prompt gets it through the
// {{OUTPUT_SCHEMA}} placeholder (Render), and a generated routine's records,
// and those of the single-routine import test, are checked against it
// (Check).
//
// The routine's contract stays importFunction(ctx), a pure parser; only the
// description of what it returns moves here.
package outputschema

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"tachyonik/lib/apicatalogue"
)

// Placeholder is where the system prompt wants the record schemas rendered.
const Placeholder = "{{OUTPUT_SCHEMA}}"

// Module is AssetManager's name in the platform API catalogue.
const Module = "assetmanager"

// The record kinds, by the names of their arrays in the routine's return
// object.
const (
	Assets          = "assets"
	Vulnerabilities = "vulnerabilities"
	Detections      = "detections"
	ScanCoverage    = "scanCoverage"
)

// kind is one record kind and the AssetManager operation it is created with.
type kind struct {
	name   string
	method string
	path   string
	// entries: the record is one element of the body's "entries" array (a
	// scan coverage table goes to AssetManager as a whole).
	entries bool
	// required: the return object must have this array (SourceImporter's own
	// contract, not AssetManager's).
	required bool
	// what one record is, for the rendering.
	what string
}

var kinds = []kind{
	{Assets, "POST", "/api/assets", false, true, "a host the file shows"},
	{Vulnerabilities, "POST", "/api/vulnerabilities", false, true, "a weakness found on a host (severity above 0)"},
	{Detections, "POST", "/api/detections", false, true, "something observed on a host that is no weakness (an informational finding, severity 0)"},
	{ScanCoverage, "POST", "/api/assets/scan-coverage", true, false, "one row of a scan coverage table: a target a scanner scans on a schedule. Only for such a table, and only when the rule asks for it; a row is not an asset, so do not also add the target to assets"},
}

// Kinds are the record kinds in the order of the return object.
func Kinds() []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k.name
	}
	return out
}

// ImporterSet are the fields of each create body that the importer sets
// itself, with why, so a routine neither sees nor returns them. The one
// place they are declared; a test fails when one is not in AssetManager's
// body (a field renamed there).
var ImporterSet = map[string]map[string]string{
	Assets:          findingOrAssetSource,
	Vulnerabilities: findingOrAssetSource,
	Detections:      findingOrAssetSource,
	// Set on the table's body, around the entries a routine returns.
	ScanCoverage: findingOrAssetSource,
}

var findingOrAssetSource = map[string]string{
	"sourceId":    "the ResourceManager source the routine is run on, which the importer knows and the routine is not told",
	"sourceLabel": "the source's filename, as ResourceManager has it",
	"userId":      "the source's owner; a routine parses a file and does not decide whose records they are",
}

// Schemas are the record schemas read from one AssetManager document.
type Schemas struct {
	records map[string]*apicatalogue.Schema
	// version is the document's info.version, for the log.
	version string
}

// FromDocument reads the record schemas from AssetManager's document. An
// operation, body or declared importer-set field it lacks is an error: the
// document no longer describes what this importer sends.
func FromDocument(doc *apicatalogue.Document) (*Schemas, error) {
	s := &Schemas{records: map[string]*apicatalogue.Schema{}, version: doc.Version}
	for _, k := range kinds {
		body, err := createBody(doc, k)
		if err != nil {
			return nil, err
		}
		for field := range ImporterSet[k.name] {
			if _, ok := body.Properties[field]; !ok {
				return nil, fmt.Errorf("%s %s: the body has no %s, which the importer sets", k.method, k.path, field)
			}
		}
		record := body
		if k.entries {
			entries := body.Properties["entries"]
			if entries == nil || entries.Items == nil {
				return nil, fmt.Errorf("%s %s: the body has no entries array", k.method, k.path)
			}
			record = entries.Items
		}
		s.records[k.name] = recordOf(record, k)
	}
	return s, nil
}

// createBody is the request body of the operation a kind is created with.
func createBody(doc *apicatalogue.Document, k kind) (*apicatalogue.Schema, error) {
	for _, op := range doc.Operations {
		if op.Method == k.method && op.Path == k.path {
			if op.RequestBody == nil || len(op.RequestBody.Properties) == 0 {
				return nil, fmt.Errorf("%s %s (%s) documents no request body", k.method, k.path, op.ID)
			}
			return op.RequestBody, nil
		}
	}
	return nil, fmt.Errorf("AssetManager's document has no %s %s", k.method, k.path)
}

// recordOf copies a body schema without the importer's fields and the
// deprecated ones: a deprecated field is on its way out of AssetManager and
// not for new routines (the finding's older "source" string, AM-DATA-16).
// For an entry schema, the importer's fields are on the body around it and
// not in it, so nothing of it is the importer's.
func recordOf(body *apicatalogue.Schema, k kind) *apicatalogue.Schema {
	own := ImporterSet[k.name]
	if k.entries {
		own = nil
	}
	out := *body
	out.Properties = map[string]*apicatalogue.Schema{}
	for name, p := range body.Properties {
		if _, mine := own[name]; mine || p.Deprecated || p.Hidden {
			continue
		}
		out.Properties[name] = p
	}
	out.Required = nil
	for _, r := range body.Required {
		if _, ok := out.Properties[r]; ok {
			out.Required = append(out.Required, r)
		}
	}
	// The body's description speaks of the fields left out (which source
	// to name); a record's meaning is in the rendering's own line.
	out.Description = ""
	return &out
}

// Record is a kind's record schema, nil for an unknown kind.
func (s *Schemas) Record(name string) *apicatalogue.Schema { return s.records[name] }

// Version is the AssetManager document's version.
func (s *Schemas) Version() string { return s.version }

// Render describes the return object and each kind's record for the AI that
// writes a routine — the text {{OUTPUT_SCHEMA}} stands for. Built from
// AssetManager's document alone, with the return object's shape, which is
// SourceImporter's own contract.
func (s *Schemas) Render() string {
	var b strings.Builder
	b.WriteString("RETURN VALUE — importFunction(ctx) MUST always return an object of this shape:\n{\n")
	for i, k := range kinds {
		sep := ","
		if i == len(kinds)-1 {
			sep = ""
		}
		note := "required; [] when the file has none"
		if !k.required {
			note = "optional; leave it out unless the rule asks for it"
		}
		fmt.Fprintf(&b, "  %s: [ <%s record>, ... ]%s   // %s\n", k.name, singular(k.name), sep, note)
	}
	b.WriteString("}\n")
	b.WriteString("Never return null or undefined, and never null for one of the arrays. Each record is a plain object with only the fields below; a field marked optional may be left out — leave it out rather than inventing a value the file does not state. The source, the owner and the import time are added by the importer, not by the routine.\n")
	for _, k := range kinds {
		fmt.Fprintf(&b, "\n%s record (in %s) — %s:\n", capitalise(singular(k.name)), k.name, k.what)
		renderFields(&b, s.records[k.name], "  ")
	}
	return b.String()
}

// renderFields lists a record's fields as the platform API catalogue's
// rendering does a request body's (tachyonik/lib/apiholder): name, type,
// required or optional, meaning.
func renderFields(b *strings.Builder, s *apicatalogue.Schema, indent string) {
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	names := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		names = append(names, n)
	}
	// Required fields first, then alphabetical: what a record cannot be
	// without comes before what it may add.
	sort.Slice(names, func(i, j int) bool {
		if required[names[i]] != required[names[j]] {
			return required[names[i]]
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		p := s.Properties[n]
		req := "optional"
		if required[n] {
			req = "required"
		}
		fmt.Fprintf(b, "%s- %s: %s, %s", indent, n, typeOf(p), req)
		if d := oneLine(p.Description); d != "" {
			fmt.Fprintf(b, " — %s", d)
		}
		b.WriteString("\n")
	}
}

func typeOf(s *apicatalogue.Schema) string {
	t := strings.Join(s.Types, " or ")
	if t == "" {
		t = "any"
	}
	if len(s.Enum) > 0 {
		vals := make([]string, len(s.Enum))
		for i, v := range s.Enum {
			vals[i] = fmt.Sprintf("%q", fmt.Sprint(v))
		}
		t += " (one of " + strings.Join(vals, ", ") + ")"
	}
	return t
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func singular(kind string) string {
	switch kind {
	case Vulnerabilities:
		return "vulnerability"
	case ScanCoverage:
		return "scan coverage"
	}
	return strings.TrimSuffix(kind, "s")
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// MaxExamples is how many violations of one kind are given in full; the
// rest are counted.
const MaxExamples = 3

// Violations are the records of one kind that do not fit its schema.
type Violations struct {
	Kind  string
	Count int
	// Examples are the first MaxExamples, each naming the record and the
	// field, e.g. `assets[2]: type: "server" is not one of [Host]`.
	Examples []string
}

// String is the kind's violations in one line.
func (v Violations) String() string {
	s := fmt.Sprintf("%d of the %s do not fit AssetManager's document: %s", v.Count, v.Kind, strings.Join(v.Examples, "; "))
	if more := v.Count - len(v.Examples); more > 0 {
		s += fmt.Sprintf("; and %d more", more)
	}
	return s
}

// Check checks every record against its kind's schema: types, required
// fields, enums (apicatalogue.Schema.Validate). records are the arrays as
// the routine returned them, by kind (jsruntime.ImportResult.Records). The
// answer has one entry per kind with violations, in the order of Kinds.
func (s *Schemas) Check(records map[string][]interface{}) []Violations {
	var out []Violations
	for _, k := range kinds {
		schema := s.records[k.name]
		v := Violations{Kind: k.name}
		for i, rec := range records[k.name] {
			if err := schema.Validate(rec); err != nil {
				v.Count++
				if len(v.Examples) < MaxExamples {
					v.Examples = append(v.Examples, fmt.Sprintf("%s[%d]: %s", k.name, i, strings.TrimPrefix(err.Error(), "the value: ")))
				}
			}
		}
		if v.Count > 0 {
			out = append(out, v)
		}
	}
	return out
}

// Holder keeps the record schemas of the AssetManager document last loaded
// from the catalogue. Safe for concurrent use; the zero value holds none.
type Holder struct {
	mu      sync.RWMutex
	schemas *Schemas
}

// Load reads AssetManager's document, given as AIManager serves it. On an
// error the schemas loaded before are kept: a document this importer cannot
// read must not take away the one it could.
func (h *Holder) Load(document string) error {
	doc, err := apicatalogue.Parse([]byte(document))
	if err != nil {
		return fmt.Errorf("AssetManager's document cannot be read: %w", err)
	}
	s, err := FromDocument(doc)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.schemas = s
	h.mu.Unlock()
	return nil
}

// Current is the schemas loaded last, nil while none is.
func (h *Holder) Current() *Schemas {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.schemas
}
