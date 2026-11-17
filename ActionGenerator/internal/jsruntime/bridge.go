// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Bridge routines: routines that read the platform's data through the API
// bridge (docs/specs/01-platform-api-catalogue.md). They are the only routines
// this version runs.
//
// A routine declares itself one with a top-level `var routineContract = 2;`
// (PA-RT-01). Each of its rules — name, ruleId, check, createAction, options —
// is given `api`: one function per bridge operation, which calls the manager
// for the user the evaluation is for. A routine without the declaration is a
// context routine, written for the rule context earlier versions built; it is
// refused where it is loaded (see aigenerator) and EvaluateBridge does not run
// it.
//
// Two things decide how an evaluation is judged:
//
//   - A rule whose check() or createAction() throws — above all because a
//     bridge call failed — has an UNKNOWN outcome: it raises nothing, and its
//     actions are not withdrawn either (PA-RT-03).
//   - Validation runs every rule against the mock answers the managers'
//     documents give per scenario, and judges what it called and read against
//     those documents (PA-VAL-02).
package jsruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dop251/goja"
	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
	"tachyonik/lib/logger"
)

// ContractBridge is the routineContract value of a bridge routine.
const ContractBridge = 2

// ErrContextRoutine is the reason a routine without the bridge declaration is
// refused, as validation reports it and as a generation failure reads.
var ErrContextRoutine = fmt.Errorf("the routine must begin with var routineContract = %d;", ContractBridge)

// routineContract reads a routine's declared contract: 2 for a bridge
// routine, 1 for anything else.
func routineContract(vm *goja.Runtime) int {
	v := vm.Get("routineContract")
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return 1
	}
	if v.ToInteger() == ContractBridge {
		return ContractBridge
	}
	return 1
}

// IsBridge reports whether the loaded routine reads through the API bridge.
func (e *JSRuleExecutor) IsBridge() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.contract == ContractBridge
}

// SetOperations records the bridge operations the routine calls, as stored
// with it (PA-RT-04).
func (e *JSRuleExecutor) SetOperations(ops []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.operations = append([]string(nil), ops...)
}

// Operations returns the bridge operations the routine calls.
func (e *JSRuleExecutor) Operations() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.operations...)
}

// bridgeBudget makes the pass's calls end within one execution budget, and
// returns how to undo it.
func (e *JSRuleExecutor) bridgeBudget(pass *apibridge.Pass) func() {
	if e.execTimeout <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.execTimeout)
	pass.SetContext(ctx)
	return func() {
		cancel()
		pass.SetContext(context.Background())
	}
}

// guardBridge runs fn under the execution budget, and makes the bridge calls
// fn makes end with that budget too: goja's interrupt stops JavaScript, not a
// Go HTTP call (PA-BR-10).
func (e *JSRuleExecutor) guardBridge(vm *goja.Runtime, pass *apibridge.Pass, fn func() (goja.Value, error)) (goja.Value, error) {
	if e.execTimeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), e.execTimeout)
		defer cancel()
		pass.SetContext(ctx)
		defer pass.SetContext(context.Background())
	}
	return runGuarded(vm, e.execTimeout, fn)
}

// EvaluateBridge evaluates a bridge routine for one user, with the calls
// answered through pass. unknown is true when a rule could not be decided —
// its check() or createAction() threw, typically on a failed bridge call — in
// which case the caller neither creates nor withdraws for it (PA-RT-03).
// force skips check(), as an evaluation request asking for the action by name
// does (AG-EVAL-13).
func (e *JSRuleExecutor) EvaluateBridge(pass *apibridge.Pass, userID int64, force bool) (actions []actionmanager.CreateActionRequest, unknown bool, err error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.code == "" || len(e.rules) == 0 {
		return nil, false, nil
	}
	if e.contract != ContractBridge {
		return nil, true, ErrContextRoutine
	}
	vm, rules, err := e.instance()
	if err != nil {
		return nil, true, fmt.Errorf("instantiate routine: %w", err)
	}
	api := pass.Object(vm, userID)

	for _, rule := range rules {
		if !force {
			result, cerr := e.guardBridge(vm, pass, func() (goja.Value, error) {
				return rule.checkFn(goja.Undefined(), api)
			})
			if cerr != nil {
				logger.Warnf("JS rule '%s' for user %d could not be decided, leaving its actions as they are: %v", rule.name, userID, cerr)
				unknown = true
				continue
			}
			if !result.ToBoolean() {
				continue
			}
		}
		// raise guards each call into the routine itself; the bridge calls in
		// them end with the budget of one such call.
		restore := e.bridgeBudget(pass)
		action, rerr := e.raise(vm, rule, api)
		restore()
		if rerr != nil {
			logger.Warnf("JS rule '%s' for user %d could not raise its action, leaving its actions as they are: %v", rule.name, userID, rerr)
			unknown = true
			continue
		}
		actions = append(actions, *action)
	}
	return actions, unknown, nil
}

// ValidateBridge validates a bridge routine against the mock answers of the
// catalogue (PA-VAL-02): AG-GEN-11 and -12, judged on what each rule called
// and read, and additionally refusing a call to an operation the routine may
// not make, an argument the operation does not have, or a field the answer
// does not have. The operations the routine called are reported for storing
// with it (PA-RT-04).
func (e *JSRuleExecutor) ValidateBridge(b *apibridge.Bridge) ValidationReport {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var report ValidationReport
	if e.code == "" || len(e.rules) == 0 {
		return report
	}
	if e.contract != ContractBridge {
		report.Errors = append(report.Errors, ErrContextRoutine)
		return report
	}
	if b == nil {
		report.Errors = append(report.Errors, fmt.Errorf("the platform API catalogue is not available, so a bridge routine cannot be validated"))
		return report
	}

	type tally struct {
		decisions     []ScenarioDecision
		results       []bool
		threw         bool
		checkReads    map[string]bool
		allReads      map[string]bool
		violations    map[string]bool
		optionResults map[int64][]bool
	}
	tallies := make([]*tally, len(e.rules))
	for i := range tallies {
		tallies[i] = &tally{checkReads: map[string]bool{}, allReads: map[string]bool{}, violations: map[string]bool{}}
	}
	called := map[string]bool{}
	absorb := func(t *tally, p *apibridge.Pass, intoCheck bool) {
		for _, r := range p.Reads() {
			t.allReads[r] = true
			if intoCheck {
				t.checkReads[r] = true
			}
		}
		for _, v := range p.Violations() {
			t.violations[v] = true
		}
		for _, c := range p.Calls() {
			called[c.Operation] = true
		}
	}

	for si, sc := range apicatalogue.Scenarios {
		vm, rules, err := e.instance()
		if err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("routine failed to load for mock scenario %d (%s): %w", si+1, sc, err))
			continue
		}
		for ri, rule := range rules {
			t := tallies[ri]
			checkPass := b.NewMockPass(sc)
			res, cerr := runGuarded(vm, e.execTimeout, func() (goja.Value, error) {
				return rule.checkFn(goja.Undefined(), checkPass.Object(vm, 1))
			})
			absorb(t, checkPass, true)
			if cerr != nil {
				t.threw = true
				t.decisions = append(t.decisions, ScenarioDecision{Label: sc, Result: "error"})
				report.Errors = append(report.Errors, fmt.Errorf("rule '%s' check() failed in mock scenario %d (%s): %w", rule.name, si+1, sc, cerr))
				continue
			}
			fired := res.ToBoolean()
			t.results = append(t.results, fired)
			t.decisions = append(t.decisions, ScenarioDecision{Label: sc, Result: fmt.Sprintf("%v", fired)})
			if !fired {
				continue
			}
			actionPass := b.NewMockPass(sc)
			api := actionPass.Object(vm, 1)
			if _, aerr := runGuarded(vm, e.execTimeout, func() (goja.Value, error) {
				return rule.actionFn(goja.Undefined(), api)
			}); aerr != nil {
				report.Errors = append(report.Errors, fmt.Errorf("rule '%s' createAction() failed in mock scenario %d (%s): %w", rule.name, si+1, sc, aerr))
			}
			for optionID, fn := range rule.optionFns {
				r, oerr := runGuarded(vm, e.execTimeout, func() (goja.Value, error) {
					return fn(goja.Undefined(), api)
				})
				if oerr != nil {
					report.Errors = append(report.Errors, fmt.Errorf("rule '%s' condition for option %d failed in mock scenario %d (%s): %w",
						rule.name, optionID, si+1, sc, oerr))
					continue
				}
				if t.optionResults == nil {
					t.optionResults = map[int64][]bool{}
				}
				t.optionResults[optionID] = append(t.optionResults[optionID], r.ToBoolean())
			}
			absorb(t, actionPass, false)
		}
	}

	for ri, rule := range e.rules {
		t := tallies[ri]
		rec := RuleValidation{Name: rule.name, Decisions: t.decisions, FieldsRead: setToSortedBool(t.allReads)}
		rec.Missing = setToSortedBool(t.violations)
		report.Rules = append(report.Rules, rec)
		for _, v := range rec.Missing {
			report.Errors = append(report.Errors, fmt.Errorf("rule '%s': %s", rule.name, v))
		}
		if t.threw || len(t.results) < 2 || !allSame(t.results) {
			continue
		}
		varying := bridgeVaryingReads(b, setToSortedBool(t.checkReads))
		if t.results[0] {
			report.Errors = append(report.Errors, fmt.Errorf(
				"rule '%s' check() returned true in every mock scenario: it would fire for every user "+
					"and could never be cleared. Does the condition actually test what the API answers?", rule.name))
			continue
		}
		if len(varying) == 0 {
			if e.onDemand {
				report.Notes = append(report.Notes, fmt.Sprintf(
					"rule '%s' has no trigger of its own, so check() returns false in every mock scenario. "+
						"It produces an action only when something asks for it by name — an action option of "+
						"type \"Action\" pointing at this rule.", rule.name))
				continue
			}
			read := "nothing"
			if len(t.checkReads) > 0 {
				read = "only answers that are the same in every scenario (" + strings.Join(setToSortedBool(t.checkReads), ", ") + ")"
			}
			report.Errors = append(report.Errors, fmt.Errorf(
				"rule '%s' check() returned false in every mock scenario and reads %s: it would never fire. "+
					"Does the condition test what the API answers at all?", rule.name, read))
			continue
		}
		report.Notes = append(report.Notes, fmt.Sprintf(
			"rule '%s' check() returned false in every mock scenario, but it reads answers that differ between them "+
				"(%s), so it is accepted: no scenario happens to meet its condition.",
			rule.name, strings.Join(varying, ", ")))
	}
	for ri, rule := range e.rules {
		t := tallies[ri]
		ids := make([]int64, 0, len(t.optionResults))
		for id := range t.optionResults {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			results := t.optionResults[id]
			if len(results) < 2 || !allSame(results) {
				continue
			}
			state := "shown"
			if !results[0] {
				state = "hidden"
			}
			report.Notes = append(report.Notes, fmt.Sprintf(
				"rule '%s': action option %d is %s in every mock scenario where the rule fires. "+
					"That is legitimate if no mock user is in the situation the option is for.", rule.name, id, state))
		}
	}
	report.Operations = setToSortedBool(called)
	return report
}

// bridgeVaryingReads returns the reads ("operation:path") whose value differs
// between the scenarios' mock answers. A path through an array is compared at
// the array itself, since that is the value the rule's reading depends on.
func bridgeVaryingReads(b *apibridge.Bridge, reads []string) []string {
	var out []string
	for _, r := range reads {
		opID, path, ok := strings.Cut(r, ":")
		if !ok {
			continue
		}
		op := b.Operation(opID)
		if op == nil {
			continue
		}
		base := path
		if i := strings.Index(base, "[]"); i >= 0 {
			base = strings.TrimSuffix(base[:i], ".")
		}
		first, differs := "", false
		for i, sc := range apicatalogue.Scenarios {
			sig := "<absent>"
			if ex, ok := op.Examples[sc]; ok {
				sig = signatureAt(op.Response.Filter(ex), base)
			}
			if i == 0 {
				first = sig
			} else if sig != first {
				differs = true
				break
			}
		}
		if differs {
			out = append(out, r)
		}
	}
	return out
}

func signatureAt(v interface{}, path string) string {
	if path != "" {
		for _, key := range strings.Split(path, ".") {
			m, ok := v.(map[string]interface{})
			if !ok {
				return "<absent>"
			}
			if v, ok = m[key]; !ok {
				return "<absent>"
			}
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}
