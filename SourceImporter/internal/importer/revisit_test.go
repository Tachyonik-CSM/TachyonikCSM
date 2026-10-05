// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests which resources a pass tries again after a rule change.
//
// The case behind it: a resource failed to import, a working routine for its
// type arrived later, and the resource stayed "Import failed" for good, because
// only "Analysed" and "No import routine" were ever looked at again.

package importer

import (
	"sort"
	"testing"

	"tachyonik/sourceimporter/internal/rscmanager"
)

var sources = []rscmanager.Source{
	{ID: 1, Status: "Analysed", SourceType: "Nmap XML Report"},
	{ID: 2, Status: "No import routine", SourceType: "Unknown CSV"},
	{ID: 86, Status: "Import failed", SourceType: "OPENVAS GMP Host Assets XML"},
	{ID: 4, Status: "Import failed", SourceType: "Nessus XML Report"},
	{ID: 5, Status: "Imported", SourceType: "OPENVAS GMP Host Assets XML"},
}

func selectedIDs(r *Revisit) []int64 {
	var ids []int64
	for _, s := range selectSources(sources, r.take()) {
		ids = append(ids, s.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAnOrdinaryPassTakesOnlyAnalysed(t *testing.T) {
	if got := selectedIDs(&Revisit{}); !equal(got, []int64{1}) {
		t.Errorf("selected %v, want only the analysed source", got)
	}
}

// The regression: a rule change for a type retries its failed imports, and
// only those — another type's routine has not changed.
func TestARuleChangeRetriesFailedImportsOfItsType(t *testing.T) {
	r := &Revisit{}
	r.RuleChanged("OPENVAS GMP Host Assets XML")
	if got := selectedIDs(r); !equal(got, []int64{1, 2, 86}) {
		t.Errorf("selected %v, want analysed, parked, and the failed GMP import", got)
	}
	// Used once: the next pass is ordinary again.
	if got := selectedIDs(r); !equal(got, []int64{1}) {
		t.Errorf("second pass selected %v, want the request used up", got)
	}
}

func TestADeletedRuleRetriesOnlyParked(t *testing.T) {
	r := &Revisit{}
	r.RuleChanged("")
	if got := selectedIDs(r); !equal(got, []int64{1, 2}) {
		t.Errorf("selected %v, want analysed and parked only", got)
	}
}

func TestAFullReloadRetriesEveryFailedImport(t *testing.T) {
	r := &Revisit{}
	r.RulesReloaded()
	if got := selectedIDs(r); !equal(got, []int64{1, 2, 4, 86}) {
		t.Errorf("selected %v, want every failed import too", got)
	}
}

func TestANilRevisitIsAnOrdinaryPass(t *testing.T) {
	var r *Revisit
	if got := selectedIDs(r); !equal(got, []int64{1}) {
		t.Errorf("selected %v", got)
	}
}
