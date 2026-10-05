// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Revisit: which resources the next import pass should try again after the
// import rules changed.
//
// Two kinds of resource wait on a rule change. One parked in "No import
// routine" may be matched by a rule that now exists. One in "Import failed" may
// succeed with a routine that has since been generated or fixed — but only one
// of the type whose rule changed, since another type's routine is the same as
// before and would fail the same way.
//
// Each request is used once, by the next pass, so a resource that cannot be
// imported is tried again when something changed rather than on every poll.

package importer

import "sync"

// Revisit collects the rule watcher's requests for the next pass. The zero
// value is ready to use, and safe for concurrent use: the watcher's callbacks
// write it while the poll loop reads it.
type Revisit struct {
	mu          sync.Mutex
	parked      bool
	allFailed   bool
	failedTypes map[string]bool
}

// RuleChanged asks the next pass to revisit resources parked without a
// routine, and failed imports of sourceType. An empty sourceType — a rule
// that was deleted, whose type is no longer known — revisits only the parked
// ones.
func (r *Revisit) RuleChanged(sourceType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parked = true
	if sourceType != "" {
		if r.failedTypes == nil {
			r.failedTypes = map[string]bool{}
		}
		r.failedTypes[sourceType] = true
	}
}

// RulesReloaded asks the next pass to revisit resources parked without a
// routine, and every failed import: after a full reload any routine may have
// changed.
func (r *Revisit) RulesReloaded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parked = true
	r.allFailed = true
}

// revisitPlan is one pass's share of the requests.
type revisitPlan struct {
	parked      bool
	allFailed   bool
	failedTypes map[string]bool
}

// retriesFailed reports whether a failed import of sourceType is due a retry.
func (p revisitPlan) retriesFailed(sourceType string) bool {
	return p.allFailed || p.failedTypes[sourceType]
}

// take hands over the pending requests and clears them. A nil Revisit has
// none.
func (r *Revisit) take() revisitPlan {
	if r == nil {
		return revisitPlan{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := revisitPlan{parked: r.parked, allFailed: r.allFailed, failedTypes: r.failedTypes}
	r.parked, r.allFailed, r.failedTypes = false, false, nil
	return p
}
