// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apicatalogue reads a manager's API document — its OpenAPI 3.1 file,
// <Manager>/spec/openapi.yaml — and answers what the platform API catalogue
// needs to know about it (docs/specs/01-platform-api-catalogue.md, "PA"):
// which operations it has, which of them the API bridge may call and how,
// which messages invalidate their answers, what their answers look like, and
// what each one answers in each mock scenario.
//
// It reads the subset of OpenAPI the platform's documents use, not OpenAPI in
// general: local $refs, parameters in path and query (a query parameter with
// `content` is JSON-encoded), one JSON request body, the first 2xx JSON
// answer, and schemas built from type (a list for nullability), properties,
// required, items, enum and allOf. That is enough to check a value against a
// schema, to keep only the documented fields of an answer, and to tell
// whether a field a routine read is documented. A feature outside the subset
// in a bridge operation is an error, not something silently ignored.
//
// The managers' tests use it to check their own documents, AIManager to
// accept a registration, and the API bridge to build its functions.
package apicatalogue

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Modules are the modules that register an API document: the managers with
// an HTTP API (PA-CAT-01, PA-NG-03). AIManager accepts a registration from
// these only; a routine-running module warns when one of them is missing
// from the catalogue it loaded.
var Modules = []string{
	"actionmanager",
	"aimanager",
	"assetmanager",
	"resourcemanager",
	"systemmanager",
	"toolmanager",
}

// Scenarios are the mock scenarios every bridge operation answers in, by the
// names its examples carry (PA-VAL-01). The list with the situation each one
// stands for is docs/specs/mock-scenarios.md; a test keeps the two equal.
var Scenarios = []string{
	"anonymous-at-source-limit",
	"admin-empty",
	"registered-nis2-red-readiness-yellow",
	"registered-nis2-orange-readiness-red",
	"registered-nis2-yellow-readiness-green-strategy-expired",
	"registered-nis2-orange-readiness-in-progress",
	"registered-nis2-impact-in-progress-recommended",
}

// Bridge and MCP exposure (PA-CAT-04, PA-MCP-05).
const (
	ExposeRead  = "read"
	ExposeWrite = "write"
)

// BoundUser marks a parameter or body field the bridge sets to the user the
// call is for; BoundOmit one the bridge never sends and a routine may not
// set — another way of naming a user that the bridge must not offer
// (PA-CAT-05).
const (
	BoundUser = "user"
	BoundOmit = "omit"
	// BoundRole marks a parameter the bridge sets to the role of the user
	// the call is for, where a manager decides on it (X-User-Role).
	BoundRole = "role"
)

// Document is one manager's parsed API document.
type Document struct {
	Title      string
	Version    string
	Operations []*Operation
}

// Operation is one method on one path.
type Operation struct {
	ID          string
	Method      string // upper case
	Path        string // as documented, e.g. /api/assets/{id}
	Summary     string
	Description string
	Deprecated  bool
	// Bridge and MCP are "", ExposeRead or ExposeWrite.
	Bridge        string
	MCP           string
	InvalidatedBy []string
	// Unscoped marks a bridge operation whose answer concerns no user — the
	// latest proxy release, a catalogue of tool kinds — so nothing in it is
	// bound to the user (x-bridge-unscoped, PA-CAT-05).
	Unscoped   bool
	Parameters []*Parameter
	// RequestBody is the JSON request body's schema, or nil.
	RequestBody *Schema
	// Response is the schema of the first 2xx JSON answer, or nil.
	Response *Schema
	// Examples are the 2xx answer's named examples, by scenario name.
	Examples map[string]interface{}
	// Statuses are the documented answer codes, e.g. "200", "404".
	Statuses []string
}

// AnswersNotFound reports whether the operation documents a 404: then a
// scenario in which the record does not exist has no example, and the mock
// answers not found (PA-CAT-08).
func (o *Operation) AnswersNotFound() bool { return contains(o.Statuses, "404") }

// Namespace is the part of the operation's id before the dot.
func (o *Operation) Namespace() string {
	ns, _, _ := strings.Cut(o.ID, ".")
	return ns
}

// Parameter is one path or query parameter.
type Parameter struct {
	Name        string
	In          string // "path" or "query"
	Required    bool
	Description string
	Deprecated  bool
	Bound       string // "", BoundUser or BoundOmit
	// JSON is true for a query parameter documented with `content`: its value
	// is sent JSON-encoded.
	JSON   bool
	Schema *Schema
}

// Schema is a resolved JSON Schema in the subset the documents use.
type Schema struct {
	Types       []string // empty: any type
	Properties  map[string]*Schema
	Required    []string
	Items       *Schema
	Enum        []interface{}
	Description string
	Deprecated  bool
	Bound       string // a body field the bridge sets (PA-CAT-05)
	// Hidden marks an answer field routines do not get (x-bridge-hidden,
	// PA-CAT-11): documented, because the manager sends it, but dropped by
	// the bridge, not shown to the AI, and a violation to read.
	Hidden bool
}

// Nullable reports whether the schema allows null.
func (s *Schema) Nullable() bool {
	for _, t := range s.Types {
		if t == "null" {
			return true
		}
	}
	return false
}

var operationIDPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*\.[a-z][a-zA-Z0-9]*$`)

// ValidOperationID reports whether id has the form <namespace>.<verb>, both in
// lowerCamelCase (PA-CAT-03).
func ValidOperationID(id string) bool { return operationIDPattern.MatchString(id) }

var methods = []string{"get", "post", "put", "patch", "delete"}

// Parse reads an API document.
func Parse(data []byte) (*Document, error) {
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("not YAML: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("empty document")
	}
	if v, _ := root["openapi"].(string); !strings.HasPrefix(v, "3.1") {
		return nil, fmt.Errorf("openapi version %q: want 3.1", v)
	}
	r := &resolver{root: root}
	doc := &Document{}
	if info, ok := root["info"].(map[string]interface{}); ok {
		doc.Title, _ = info["title"].(string)
		doc.Version = fmt.Sprint(info["version"])
	}
	paths, _ := root["paths"].(map[string]interface{})
	pathKeys := sortedKeys(paths)
	for _, path := range pathKeys {
		item, ok := r.deref(paths[path]).(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("path %s is not an object", path)
		}
		shared, err := r.parameters(item["parameters"])
		if err != nil {
			return nil, fmt.Errorf("path %s: %w", path, err)
		}
		for _, m := range methods {
			raw, ok := item[m].(map[string]interface{})
			if !ok {
				continue
			}
			op, err := r.operation(strings.ToUpper(m), path, raw, shared)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(m), path, err)
			}
			doc.Operations = append(doc.Operations, op)
		}
	}
	return doc, nil
}

// Operation returns the operation with this id, or nil.
func (d *Document) Operation(id string) *Operation {
	for _, op := range d.Operations {
		if op.ID == id {
			return op
		}
	}
	return nil
}

// BridgeOperations returns the operations exposed to the bridge in a profile:
// read ones only, or read and write ones.
func (d *Document) BridgeOperations(withWrites bool) []*Operation {
	var out []*Operation
	for _, op := range d.Operations {
		if op.Bridge == ExposeRead || (withWrites && op.Bridge == ExposeWrite) {
			out = append(out, op)
		}
	}
	return out
}

// Check returns everything that makes the document unfit for the catalogue:
// the rules of PA-CAT-03 to -08 that can be checked from the document alone.
// Empty when it is fit.
func (d *Document) Check() []error {
	var errs []error
	seen := map[string]bool{}
	for _, op := range d.Operations {
		where := op.Method + " " + op.Path
		switch {
		case op.ID == "":
			errs = append(errs, fmt.Errorf("%s: no operationId", where))
		case !ValidOperationID(op.ID):
			errs = append(errs, fmt.Errorf("%s: operationId %q is not <namespace>.<verb> in lowerCamelCase", where, op.ID))
		case seen[op.ID]:
			errs = append(errs, fmt.Errorf("%s: operationId %q is used twice", where, op.ID))
		}
		seen[op.ID] = true
		errs = append(errs, checkExposure(op, where)...)
	}
	return errs
}

func checkExposure(op *Operation, where string) []error {
	var errs []error
	for _, mark := range []struct{ name, value string }{{"x-bridge", op.Bridge}, {"x-mcp", op.MCP}} {
		switch mark.value {
		case "":
		case ExposeRead:
			if op.Method != "GET" {
				errs = append(errs, fmt.Errorf("%s: %s: read is for GET only", where, mark.name))
			}
		case ExposeWrite:
			if op.Method == "GET" {
				errs = append(errs, fmt.Errorf("%s: %s: write is not for GET", where, mark.name))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: %s: %q is neither read nor write", where, mark.name, mark.value))
		}
	}
	if op.Bridge == "" {
		return errs
	}
	if op.Response == nil {
		errs = append(errs, fmt.Errorf("%s (%s): a bridge operation needs a JSON answer schema", where, op.ID))
	}
	if op.Bridge == ExposeRead {
		if len(op.InvalidatedBy) == 0 {
			errs = append(errs, fmt.Errorf("%s (%s): a read bridge operation needs x-invalidated-by", where, op.ID))
		}
		for _, sc := range Scenarios {
			ex, ok := op.Examples[sc]
			if !ok {
				if !op.AnswersNotFound() {
					errs = append(errs, fmt.Errorf("%s (%s): no example for scenario %q", where, op.ID, sc))
				}
				continue
			}
			if op.Response != nil {
				if err := op.Response.Validate(ex); err != nil {
					errs = append(errs, fmt.Errorf("%s (%s): example %q: %w", where, op.ID, sc, err))
				}
			}
		}
	}
	for name := range op.Examples {
		if !contains(Scenarios, name) {
			errs = append(errs, fmt.Errorf("%s (%s): example %q is not a mock scenario", where, op.ID, name))
		}
	}
	for _, p := range op.Parameters {
		if p.Bound != "" && p.Bound != BoundUser && p.Bound != BoundOmit && p.Bound != BoundRole {
			errs = append(errs, fmt.Errorf("%s (%s): parameter %s: x-bridge-bound %q is not %q, %q or %q", where, op.ID, p.Name, p.Bound, BoundUser, BoundRole, BoundOmit))
		}
	}
	return errs
}

// Validate reports the first way v does not fit the schema, naming the path.
// v is a decoded JSON or YAML value. Fields the schema does not list are
// allowed: the bridge drops them (Filter), it does not refuse the answer.
func (s *Schema) Validate(v interface{}) error { return s.validate(v, "") }

func (s *Schema) validate(v interface{}, path string) error {
	at := path
	if at == "" {
		at = "the value"
	}
	if len(s.Types) > 0 && !typeMatches(s.Types, v) {
		return fmt.Errorf("%s: %s is not %s", at, describe(v), strings.Join(s.Types, " or "))
	}
	if len(s.Enum) > 0 && v != nil {
		ok := false
		for _, e := range s.Enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%s: %v is not one of %v", at, v, s.Enum)
		}
	}
	switch x := v.(type) {
	case map[string]interface{}:
		for _, name := range s.Required {
			if _, ok := x[name]; !ok {
				return fmt.Errorf("%s: %s is missing", at, join(path, name))
			}
		}
		for _, name := range sortedKeys(x) {
			if ps, ok := s.Properties[name]; ok {
				if err := ps.validate(x[name], join(path, name)); err != nil {
					return err
				}
			}
		}
	case []interface{}:
		if s.Items != nil {
			for i, item := range x {
				if err := s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Filter returns v with only the fields the schema documents (PA-BR-04),
// without those marked hidden from routines (PA-CAT-11). A
// schema without properties keeps an object as it is.
func (s *Schema) Filter(v interface{}) interface{} {
	if s == nil {
		return v
	}
	switch x := v.(type) {
	case map[string]interface{}:
		if len(s.Properties) == 0 {
			return x
		}
		out := make(map[string]interface{}, len(s.Properties))
		for name, ps := range s.Properties {
			if ps.Hidden {
				continue
			}
			if fv, ok := x[name]; ok {
				out[name] = ps.Filter(fv)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, item := range x {
			out[i] = s.Items.Filter(item)
		}
		return out
	}
	return v
}

// HasPath reports whether the schema documents a path as a routine reads it:
// fields joined by dots, "[]" for an array's elements (the form
// tachyonik/lib/jsctxtrace records), e.g. "assets[].lastSeen". Array
// properties a routine may read (length) count as documented.
func (s *Schema) HasPath(path string) bool {
	cur := s
	for _, part := range splitPath(path) {
		if cur == nil {
			return false
		}
		if part == "[]" {
			if cur.Items == nil {
				return false
			}
			cur = cur.Items
			continue
		}
		if cur.Items != nil && part == "length" {
			return true
		}
		next, ok := cur.Properties[part]
		if !ok || next.Hidden {
			return false
		}
		cur = next
	}
	return true
}

func splitPath(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, ".") {
		for strings.HasSuffix(seg, "[]") {
			seg = strings.TrimSuffix(seg, "[]")
			if seg != "" {
				out = append(out, seg)
			}
			out = append(out, "[]")
			seg = ""
		}
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func typeMatches(types []string, v interface{}) bool {
	for _, t := range types {
		switch t {
		case "null":
			if v == nil {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "integer":
			switch n := v.(type) {
			case int, int64:
				return true
			case float64:
				if n == float64(int64(n)) {
					return true
				}
			}
		case "number":
			switch v.(type) {
			case int, int64, float64:
				return true
			}
		case "object":
			if _, ok := v.(map[string]interface{}); ok {
				return true
			}
		case "array":
			if _, ok := v.([]interface{}); ok {
				return true
			}
		}
	}
	return false
}

func describe(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case int, int64, float64:
		return "a number"
	case map[string]interface{}:
		return "an object"
	case []interface{}:
		return "an array"
	}
	return fmt.Sprintf("%T", v)
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
