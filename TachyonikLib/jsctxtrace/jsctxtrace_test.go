// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that tracing records the right paths — including the one it exists
// for, a read of a field that is not there — and that a traced context behaves
// like the untraced one: array methods, Object.keys, JSON, `in`, identity, and
// writes that do not reach the caller's data.

package jsctxtrace

import (
	"reflect"
	"testing"

	"github.com/dop251/goja"
)

func sampleContext() map[string]interface{} {
	return map[string]interface{}{
		"questionnaires": map[string]interface{}{
			"nis2Impact":    map[string]interface{}{"progress": 100, "level": "red"},
			"nis2Readiness": map[string]interface{}{"progress": 40},
		},
		"organisation": map[string]interface{}{
			"itProviders": []interface{}{
				map[string]interface{}{"name": "SecureOps", "types": []interface{}{"cybersecurity"}},
				map[string]interface{}{"name": "NetCo", "types": []interface{}{"network_infrastructure"}},
			},
		},
		"assets": []map[string]interface{}{
			{"name": "b", "severity": 3},
			{"name": "a", "severity": 9, "extra": true},
		},
		"highestScoreAsset": nil,
	}
}

func run(t *testing.T, ctx map[string]interface{}, expr string) (goja.Value, *Trace) {
	t.Helper()
	vm := goja.New()
	tr := New()
	if err := vm.Set("ctx", tr.Wrap(vm, ctx)); err != nil {
		t.Fatalf("set ctx: %v", err)
	}
	v, err := vm.RunString(expr)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return v, tr
}

func TestRecordsTheExactPathsRead(t *testing.T) {
	v, tr := run(t, sampleContext(),
		`ctx.questionnaires.nis2Impact.progress === 100 && ctx.questionnaires.nis2Impact.level !== "green" && ctx.questionnaires.nis2Readiness.progress < 100`)
	if !v.ToBoolean() {
		t.Error("condition evaluated differently under tracing")
	}
	want := []string{
		"questionnaires", "questionnaires.nis2Impact", "questionnaires.nis2Impact.level",
		"questionnaires.nis2Impact.progress", "questionnaires.nis2Readiness", "questionnaires.nis2Readiness.progress",
	}
	if got := tr.Read(); !reflect.DeepEqual(got, want) {
		t.Errorf("read:\n got  %v\n want %v", got, want)
	}
	if m := tr.Missing(); len(m) != 0 {
		t.Errorf("nothing missing expected, got %v", m)
	}
}

// The case this package exists for: a misspelt field is named.
func TestNamesAFieldThatDoesNotExist(t *testing.T) {
	v, tr := run(t, sampleContext(), `ctx.questionnaires.nis2Impact.levle === "red"`)
	if v.ToBoolean() {
		t.Error("a missing field compared equal")
	}
	if got := tr.Missing(); !reflect.DeepEqual(got, []string{"questionnaires.nis2Impact.levle"}) {
		t.Errorf("missing = %v", got)
	}
}

func TestArraysBehaveLikeArrays(t *testing.T) {
	v, tr := run(t, sampleContext(),
		`ctx.organisation.itProviders.some(function (p) { return p.types.indexOf("cybersecurity") >= 0; })
		 && ctx.assets.filter(function (a) { return a.severity >= 8; }).length === 1
		 && Array.isArray(ctx.assets)`)
	if !v.ToBoolean() {
		t.Error("array methods evaluated differently under tracing")
	}
	for _, p := range []string{"organisation.itProviders[]", "organisation.itProviders[].types[]", "assets[].severity"} {
		if !contains(tr.Read(), p) {
			t.Errorf("read does not include %q: %v", p, tr.Read())
		}
	}
}

// A field present on one element and absent on another is not missing: the
// rule is reading something that exists.
func TestAFieldPresentInSomeElementsIsNotMissing(t *testing.T) {
	_, tr := run(t, sampleContext(), `ctx.assets.filter(function (a) { return a.extra; }).length`)
	if contains(tr.Missing(), "assets[].extra") {
		t.Errorf("a field present on one element was reported missing: %v", tr.Missing())
	}
}

// A rule may sort ctx.assets to find the worst one. That must work, and must
// not reorder the data the next scenario or the caller sees.
func TestWritesStayPrivate(t *testing.T) {
	ctx := sampleContext()
	v, _ := run(t, ctx, `ctx.assets.sort(function (x, y) { return y.severity - x.severity; })[0].name
		+ "|" + (function () { ctx.note = "set"; return ctx.note; })()`)
	if v.String() != "a|set" {
		t.Errorf("sort or assignment did not behave: %q", v.String())
	}
	if first := ctx["assets"].([]map[string]interface{})[0]["name"]; first != "b" {
		t.Errorf("sorting changed the caller's data: first asset is now %v", first)
	}
	if _, leaked := ctx["note"]; leaked {
		t.Error("an assignment reached the caller's map")
	}
}

func TestObjectsBehaveLikeObjects(t *testing.T) {
	v, tr := run(t, sampleContext(), `
		(ctx.questionnaires === ctx.questionnaires)
		&& Object.keys(ctx.questionnaires.nis2Impact).length === 2
		&& ("level" in ctx.questionnaires.nis2Impact)
		&& !("nope" in ctx.questionnaires.nis2Impact)
		&& JSON.stringify(ctx.questionnaires.nis2Readiness) === '{"progress":40}'
		&& ctx.highestScoreAsset === null
		&& ("" + ctx.questionnaires).length > 0`)
	if !v.ToBoolean() {
		t.Error("object behaviour differs under tracing")
	}
	// `in` for an absent key is a check, and a read of something missing.
	if !contains(tr.Missing(), "questionnaires.nis2Impact.nope") {
		t.Errorf("`in` on an absent key not recorded: %v", tr.Missing())
	}
	for _, p := range tr.Read() {
		if p == "questionnaires.toJSON" || p == "questionnaires.toString" {
			t.Errorf("an engine-internal lookup was recorded as a read: %s", p)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestLeaves(t *testing.T) {
	got := Leaves([]string{"a", "a.b", "a.b.c", "a.d", "list", "list[]", "list[].x", "z"})
	want := []string{"a.b.c", "a.d", "list[].x", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Leaves = %v, want %v", got, want)
	}
}

// Rules of one routine share a ctx: what one writes, the next sees. Reset must
// split the reads per rule without undoing that.
func TestResetAttributesReadsWithoutLosingWrites(t *testing.T) {
	vm := goja.New()
	tr := New()
	if err := vm.Set("ctx", tr.Wrap(vm, sampleContext())); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(`ctx.shared = ctx.questionnaires.nis2Impact.level;`); err != nil {
		t.Fatal(err)
	}
	tr.Reset()
	v, err := vm.RunString(`ctx.shared + "|" + ctx.questionnaires.nis2Readiness.progress`)
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "red|40" {
		t.Errorf("a write before Reset was lost: %q", v.String())
	}
	if got := Leaves(tr.Read()); !reflect.DeepEqual(got, []string{"questionnaires.nis2Readiness.progress", "shared"}) {
		t.Errorf("reads after Reset = %v", got)
	}
}
