// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apicontract checks a hand-written client of a manager against that
// manager's API document (<Manager>/spec/openapi.yaml, the platform API
// catalogue's source, docs/specs/01-platform-api-catalogue.md).
//
// A client declares the calls it makes — method, path, query parameters, and
// the Go types it sends and decodes — in its tests, and Check reports what of
// that the document does not have: a path or method that does not exist, a
// query parameter it does not list, a JSON field it does not document, an
// answer or body it does not document at all. Covered additionally confirms
// that every API path written in the client's code is among its declared
// calls, so the declaration cannot drift from the code.
//
// The same check serves a manager for its own handlers: declared with the
// handlers' answer types, it confirms that the document describes what they
// send.
package apicontract

import (
	"encoding"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"tachyonik/lib/apicatalogue"
)

// Call is one call a client makes.
type Call struct {
	Method string // upper or lower case
	// Path as the client builds it: a template ("/api/sources/{id}") or a
	// format string ("/api/sources/%d"); a query string is ignored.
	Path string
	// Query names the query parameters the client may send.
	Query []string
	// Request is a value of the type the client sends as the JSON body, or
	// nil. A map is not checked field by field.
	Request interface{}
	// Answer is a value of the type the client decodes the answer into, or
	// nil when it does not read the answer.
	Answer interface{}
}

var placeholder = regexp.MustCompile(`\{[^}]*\}|%[-+# 0-9.]*[a-zA-Z]`)

// normalise turns a path template or format string into a comparable form:
// every parameter is "{}", the query string and a trailing slash are dropped.
func normalise(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	p = placeholder.ReplaceAllString(p, "{}")
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// Check returns what of calls the document does not have, sorted.
func Check(doc *apicatalogue.Document, calls []Call) []string {
	byKey := map[string]*apicatalogue.Operation{}
	for _, op := range doc.Operations {
		byKey[op.Method+" "+normalise(op.Path)] = op
	}
	var problems []string
	for _, c := range calls {
		label := strings.ToUpper(c.Method) + " " + c.Path
		op := byKey[strings.ToUpper(c.Method)+" "+normalise(c.Path)]
		if op == nil {
			problems = append(problems, label+": not in the document")
			continue
		}
		label += " (" + op.ID + ")"
		for _, q := range c.Query {
			if !hasQuery(op, q) {
				problems = append(problems, fmt.Sprintf("%s: query parameter %q is not documented", label, q))
			}
		}
		if c.Request != nil {
			if op.RequestBody == nil {
				problems = append(problems, label+": the request body is not documented")
			} else {
				for _, f := range missing(reflect.TypeOf(c.Request), op.RequestBody, "", map[reflect.Type]bool{}) {
					problems = append(problems, fmt.Sprintf("%s: %s is not in the documented request body", label, f))
				}
			}
		}
		if c.Answer != nil {
			if op.Response == nil {
				problems = append(problems, label+": the answer is not documented")
			} else {
				for _, f := range answerMissing(c.Answer, op.Response) {
					problems = append(problems, fmt.Sprintf("%s: %s is not in the documented answer", label, f))
				}
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func hasQuery(op *apicatalogue.Operation, name string) bool {
	for _, p := range op.Parameters {
		if p.In == "query" && p.Name == name {
			return true
		}
	}
	return false
}

// CheckAnswer checks a real answer body of the operation at method and path
// against its documented answer: the value must validate, and every field it
// has must be documented. It is how a manager tests that its handlers answer
// as its document says.
func CheckAnswer(doc *apicatalogue.Document, method, path string, body []byte) []string {
	// A real path: the literal match wins over one through a parameter
	// ("/users/ids" before "/users/{id}").
	var op *apicatalogue.Operation
	best := -1
	for _, o := range doc.Operations {
		if o.Method != strings.ToUpper(method) {
			continue
		}
		if n, ok := matches(o.Path, path); ok && n > best {
			op, best = o, n
		}
	}
	label := strings.ToUpper(method) + " " + path
	if op == nil {
		return []string{label + ": not in the document"}
	}
	label += " (" + op.ID + ")"
	if op.Response == nil {
		return []string{label + ": the answer is not documented"}
	}
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return []string{label + ": the answer is not JSON: " + err.Error()}
	}
	var out []string
	if err := op.Response.Validate(v); err != nil {
		out = append(out, label+": "+err.Error())
	}
	for _, f := range jsonMissing(v, op.Response, "") {
		out = append(out, label+": "+f+" is not in the documented answer")
	}
	sort.Strings(out)
	return out
}

// matches reports whether a real path fits a documented template, and how
// many of its segments matched literally.
func matches(template, path string) (int, bool) {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	ts := strings.Split(strings.Trim(template, "/"), "/")
	ps := strings.Split(strings.Trim(path, "/"), "/")
	if len(ts) != len(ps) {
		return 0, false
	}
	literal := 0
	for i := range ts {
		switch {
		case strings.HasPrefix(ts[i], "{") && strings.HasSuffix(ts[i], "}"):
		case ts[i] == ps[i]:
			literal++
		default:
			return 0, false
		}
	}
	return literal, true
}

// answerMissing is missing for an answer value. A type with its own
// MarshalJSON, and a map, are checked by what they encode: the value is
// marshalled and the JSON walked, so give such a value with its optional
// fields filled in.
func answerMissing(v interface{}, s *apicatalogue.Schema) []string {
	t := reflect.TypeOf(v)
	if t.Implements(marshalerType) || t.Kind() == reflect.Map {
		data, err := json.Marshal(v)
		if err != nil {
			return []string{"(the answer value cannot be marshalled: " + err.Error() + ")"}
		}
		var generic interface{}
		_ = json.Unmarshal(data, &generic)
		return jsonMissing(generic, s, "")
	}
	return missing(t, s, "", map[reflect.Type]bool{})
}

// jsonMissing returns the paths of a decoded JSON value that s does not
// document.
func jsonMissing(v interface{}, s *apicatalogue.Schema, at string) []string {
	if s == nil || freeForm(s) {
		return nil
	}
	switch x := v.(type) {
	case map[string]interface{}:
		var out []string
		for k, sub := range x {
			path := k
			if at != "" {
				path = at + "." + k
			}
			ps, ok := s.Properties[k]
			if !ok {
				out = append(out, path)
				continue
			}
			out = append(out, jsonMissing(sub, ps, path)...)
		}
		return out
	case []interface{}:
		if s.Items == nil {
			return nil
		}
		var out []string
		for _, e := range x {
			out = append(out, jsonMissing(e, s.Items, at+"[]")...)
		}
		return dedupe(out)
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

var (
	marshalerType   = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	timeType        = reflect.TypeOf(time.Time{})
	rawType         = reflect.TypeOf(json.RawMessage{})
	unmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	textType        = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

// freeForm reports whether a schema says nothing about what is below it: no
// properties and no items. Anything a client reads below it is accepted.
func freeForm(s *apicatalogue.Schema) bool {
	return len(s.Properties) == 0 && s.Items == nil
}

// leaf reports whether t is decoded as one value, not walked field by field.
func leaf(t reflect.Type) bool {
	if t == timeType || t == rawType {
		return true
	}
	if t.Implements(unmarshalerType) || reflect.PointerTo(t).Implements(unmarshalerType) ||
		t.Implements(marshalerType) ||
		t.Implements(textType) || reflect.PointerTo(t).Implements(textType) {
		return true
	}
	return false
}

// missing returns the JSON paths of t's fields that s does not document.
func missing(t reflect.Type, s *apicatalogue.Schema, at string, seen map[reflect.Type]bool) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if s == nil || freeForm(s) || leaf(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 { // []byte: one base64 value
			return nil
		}
		if s.Items == nil {
			return nil
		}
		return missing(t.Elem(), s.Items, at+"[]", seen)
	case reflect.Struct:
		if seen[t] {
			return nil
		}
		seen[t] = true
		defer delete(seen, t)
		var out []string
		for _, f := range fieldsOf(t) {
			path := f.name
			if at != "" {
				path = at + "." + f.name
			}
			sub, ok := s.Properties[f.name]
			if !ok {
				out = append(out, path)
				continue
			}
			out = append(out, missing(f.typ, sub, path, seen)...)
		}
		return out
	}
	return nil
}

type jsonField struct {
	name string
	typ  reflect.Type
}

// fieldsOf lists the JSON fields of a struct as encoding/json sees them:
// exported fields under their tag names, embedded structs flattened.
func fieldsOf(t reflect.Type) []jsonField {
	var out []jsonField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				out = append(out, fieldsOf(et)...)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, jsonField{name: name, typ: f.Type})
	}
	return out
}

// Covered returns the API paths written as string literals in the non-test
// Go files of dir that no call in calls covers, sorted. A literal is covered
// when it names a declared call's path, or begins one — a path put together
// by concatenation ("/api/sources/" + id).
func Covered(dir string, calls []Call) ([]string, error) {
	lits, err := literalPaths(dir)
	if err != nil {
		return nil, err
	}
	var declared []string
	for _, c := range calls {
		declared = append(declared, normalise(c.Path))
	}
	var out []string
	for _, l := range lits {
		n := normalise(l)
		ok := false
		for _, d := range declared {
			if d == n || strings.HasPrefix(d, strings.TrimSuffix(n, "/")+"/") || (strings.HasSuffix(l, "/") && strings.HasPrefix(d, l)) {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out, nil
}

func literalPaths(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			return nil, err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			// "/api/…", or a base URL put in front: "%s/api/…".
			if i := strings.Index(v, "/api/"); i == 0 || (i > 0 && strings.HasSuffix(v[:i], "%s")) {
				set[v[i:]] = true
			}
			return true
		})
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	return out, nil
}

// DocumentPath finds <module>/spec/openapi.yaml by walking up from the
// working directory to the repository root; "" when it is not found — a
// module built on its own, without its siblings.
func DocumentPath(module string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		p := filepath.Join(dir, module, "spec", "openapi.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Test checks calls against the document of module (its directory name,
// e.g. "ResourceManager"). Each problem is a test error; the test is skipped
// when the document is not there.
func Test(t testing.TB, module string, calls []Call) {
	t.Helper()
	path := DocumentPath(module)
	if path == "" {
		t.Skipf("%s/spec/openapi.yaml not found: the module is built without its siblings", module)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := apicatalogue.Parse(data)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for _, p := range Check(doc, calls) {
		t.Errorf("%s: %s", module, p)
	}
}

// TestDeclared checks that every API path written in the non-test Go files
// of dir — a client package, usually "." — is among calls: all the calls the
// package makes, of every manager it calls.
func TestDeclared(t testing.TB, dir string, calls ...[]Call) {
	t.Helper()
	var all []Call
	for _, c := range calls {
		all = append(all, c...)
	}
	uncovered, err := Covered(dir, all)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range uncovered {
		t.Errorf("%s is called in the client but not declared as a call", l)
	}
}
