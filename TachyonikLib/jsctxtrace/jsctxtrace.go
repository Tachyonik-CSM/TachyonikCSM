// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jsctxtrace records which properties JavaScript reads from a context
// object — the `ctx` a generated rule routine is handed.
//
// Its purpose is to explain a routine rather than to change what it does. A
// rule validated against mock contexts can be wrong in two ways that look the
// same from its return value alone: it reads a field that does not exist (a
// typo, or a field the model invented), or it is correct but no mock scenario
// happens to satisfy it. Knowing which paths it read tells the two apart, and
// names the offending field instead of leaving someone to guess.
//
// Wrap exposes a Go value to goja as live objects that record every read by
// path — "questionnaires.nis2Impact.level", with "[]" for any array element, as
// in "organisation.itProviders[].types". Reads of properties that are not there
// are recorded too, and Missing reports the paths that were never found present
// anywhere during the run (a field present in some array elements and absent in
// others is not missing).
//
// Behaviour stays close to a plain vm.ToValue of the same data, because a
// routine traced here must decide what it would decide untraced: array methods,
// Object.keys, JSON.stringify and `in` all work, writes land in a private copy
// (a rule may sort ctx.assets without altering the caller's data), and each
// child is wrapped once, so ctx.a === ctx.a holds.
//
// Used by ActionGenerator's mock-context validation and by ActionExecutor's dry
// run, so both report reads the same way.
package jsctxtrace

import (
	"reflect"
	"sort"
	"strings"

	"github.com/dop251/goja"
)

// Trace collects the reads made through the values it wrapped.
type Trace struct {
	read      map[string]bool
	present   map[string]bool
	candidate map[string]bool
}

// New returns an empty trace.
func New() *Trace {
	return &Trace{read: map[string]bool{}, present: map[string]bool{}, candidate: map[string]bool{}}
}

// Wrap exposes value to vm, recording reads into this trace. Maps with string
// keys become objects, slices and arrays become arrays, nil becomes null, and
// anything else is converted as vm.ToValue would.
func (t *Trace) Wrap(vm *goja.Runtime, value interface{}) goja.Value {
	return t.wrap(vm, value, "")
}

// Reset starts a new recording without replacing the wrapped values: whatever a
// script wrote to them stays, and so does the identity of every wrapped child.
// Use it to attribute reads to one function at a time while the functions share
// one context — as the rules of a routine share one ctx in production.
func (t *Trace) Reset() {
	t.read = map[string]bool{}
	t.present = map[string]bool{}
	t.candidate = map[string]bool{}
}

// Read returns every path read, sorted — including the objects passed through
// on the way, so "a" and "a.b" both appear for a read of ctx.a.b.
func (t *Trace) Read() []string { return sortedKeys(t.read) }

// Leaves reduces a sorted path list to the paths no other path extends: the
// values actually tested, without the objects walked through to reach them.
// "a", "a.b" and "a.b.c" become "a.b.c"; "list" and "list[].x" become "list[].x".
func Leaves(paths []string) []string {
	out := []string{}
	for i, p := range paths {
		extended := false
		for _, q := range paths[i+1:] {
			if strings.HasPrefix(q, p+".") || strings.HasPrefix(q, p+"[]") {
				extended = true
				break
			}
		}
		if !extended {
			out = append(out, p)
		}
	}
	return out
}

// Missing returns the paths that were read but never found present, sorted.
func (t *Trace) Missing() []string {
	out := []string{}
	for p := range t.candidate {
		if !t.present[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// Properties the engine looks up on its own — string conversion, JSON, the
// prototype chain. They are not something a rule chose to read, and answering
// nil lets the lookup fall through to Object.prototype as it normally would.
var internal = map[string]bool{
	"toJSON": true, "toString": true, "valueOf": true, "toLocaleString": true,
	"constructor": true, "hasOwnProperty": true, "isPrototypeOf": true,
	"propertyIsEnumerable": true, "__proto__": true,
}

func (t *Trace) wrap(vm *goja.Runtime, value interface{}, path string) goja.Value {
	if value == nil {
		return goja.Null()
	}
	if v, ok := value.(goja.Value); ok {
		return v
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return vm.ToValue(value)
		}
		if rv.IsNil() {
			return goja.Null()
		}
		fields := make(map[string]interface{}, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			fields[iter.Key().String()] = iter.Value().Interface()
		}
		return vm.NewDynamicObject(&object{trace: t, vm: vm, path: path, source: fields,
			cache: map[string]goja.Value{}, deleted: map[string]bool{}})
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return goja.Null()
		}
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return vm.ToValue(value) // bytes are not a list of records
		}
		items := make([]interface{}, rv.Len())
		for i := range items {
			items[i] = rv.Index(i).Interface()
		}
		return vm.NewDynamicArray(&array{trace: t, vm: vm, path: path + "[]", source: items})
	default:
		return vm.ToValue(value)
	}
}

// object is a traced map.
type object struct {
	trace   *Trace
	vm      *goja.Runtime
	path    string
	source  map[string]interface{}
	cache   map[string]goja.Value // wrapped children, and anything the script set
	deleted map[string]bool
}

func (o *object) exists(key string) bool {
	if o.deleted[key] {
		return false
	}
	if _, ok := o.cache[key]; ok {
		return true
	}
	_, ok := o.source[key]
	return ok
}

func (o *object) note(key string) bool {
	p := join(o.path, key)
	o.trace.read[p] = true
	if o.exists(key) {
		o.trace.present[p] = true
		return true
	}
	o.trace.candidate[p] = true
	return false
}

func (o *object) Get(key string) goja.Value {
	if internal[key] || strings.HasPrefix(key, "@@") {
		if v, ok := o.cache[key]; ok {
			return v
		}
		return nil
	}
	if !o.note(key) {
		return nil
	}
	if v, ok := o.cache[key]; ok {
		return v
	}
	v := o.trace.wrap(o.vm, o.source[key], join(o.path, key))
	o.cache[key] = v
	return v
}

func (o *object) Set(key string, val goja.Value) bool {
	delete(o.deleted, key)
	o.cache[key] = val
	return true
}

func (o *object) Has(key string) bool {
	if internal[key] {
		return false
	}
	return o.note(key)
}

func (o *object) Delete(key string) bool {
	delete(o.cache, key)
	o.deleted[key] = true
	return true
}

func (o *object) Keys() []string {
	keys := make([]string, 0, len(o.source)+len(o.cache))
	seen := map[string]bool{}
	for k := range o.source {
		if !o.deleted[k] && !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	for k := range o.cache {
		if !o.deleted[k] && !seen[k] && !internal[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	sort.Strings(keys)
	return keys
}

// array is a traced slice. Elements are wrapped on first access and kept, and a
// write replaces the kept element, so sort() and friends work on a private copy.
type array struct {
	trace  *Trace
	vm     *goja.Runtime
	path   string
	source []interface{}
	items  []goja.Value // nil until first touched
}

func (a *array) materialise() {
	if a.items != nil {
		return
	}
	a.items = make([]goja.Value, len(a.source))
}

func (a *array) Len() int {
	a.trace.read[a.path] = true
	a.trace.present[a.path] = true
	if a.items != nil {
		return len(a.items)
	}
	return len(a.source)
}

func (a *array) Get(idx int) goja.Value {
	a.trace.read[a.path] = true
	a.trace.present[a.path] = true
	a.materialise()
	if idx < 0 || idx >= len(a.items) {
		return nil
	}
	if a.items[idx] == nil {
		if idx < len(a.source) {
			a.items[idx] = a.trace.wrap(a.vm, a.source[idx], a.path)
		} else {
			a.items[idx] = goja.Undefined()
		}
	}
	return a.items[idx]
}

func (a *array) Set(idx int, val goja.Value) bool {
	a.materialise()
	if idx < 0 {
		return false
	}
	for idx >= len(a.items) {
		a.items = append(a.items, goja.Undefined())
	}
	// Make sure every original element is wrapped before a reorder moves them.
	for i := range a.items {
		if a.items[i] == nil && i < len(a.source) {
			a.items[i] = a.trace.wrap(a.vm, a.source[i], a.path)
		}
	}
	a.items[idx] = val
	return true
}

func (a *array) SetLen(n int) bool {
	a.materialise()
	if n < 0 {
		return false
	}
	for i := range a.items {
		if a.items[i] == nil && i < len(a.source) {
			a.items[i] = a.trace.wrap(a.vm, a.source[i], a.path)
		}
	}
	if n <= len(a.items) {
		a.items = a.items[:n]
		return true
	}
	for len(a.items) < n {
		a.items = append(a.items, goja.Undefined())
	}
	return true
}
