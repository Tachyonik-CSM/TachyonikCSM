// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Bridge routines: execution routines that read and write through the
// platform API bridge, in its read-write profile
// (docs/specs/01-platform-api-catalogue.md, PA-BR-09, -10).
//
// A routine declares itself one with a top-level `var routineContract = 2;`.
// Its executors are objects — name, ruleId, run(api) — and api is the bridge:
// api.<namespace>.<verb>({…, body}) with the answers the managers document.
// A routine without the declaration was written for the hand-written api
// object this module had before; that object is gone, so such a routine is
// refused when it is loaded (ErrPredatesBridge) and never runs.
//
// A bridge routine is also validated before it is stored: it is run once per
// mock scenario against the managers' mock answers, and refused when it calls
// an operation it may not, passes an argument the operation does not have, or
// reads a field the answer does not have (PA-VAL-02). It is not judged on
// what it returns: an executor is a procedure, not a condition.
package jsruntime

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
)

// ContractBridge is the routineContract value of a bridge routine.
const ContractBridge = 2

// ErrPredatesBridge is LoadFromString's answer to a routine that does not
// declare `var routineContract = 2;`: it was written for the api object this
// module no longer has. Callers name the rule and routine.
var ErrPredatesBridge = errors.New("the routine does not begin with var routineContract = 2; it predates the platform API")

func declaresBridge(vm *goja.Runtime) bool {
	v := vm.Get("routineContract")
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return false
	}
	return v.ToInteger() == ContractBridge
}

// SetOperations records the bridge operations the routine calls.
func (e *JSExecutorRuntime) SetOperations(ops []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.operations = append([]string(nil), ops...)
}

// Operations returns the bridge operations the routine calls.
func (e *JSExecutorRuntime) Operations() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.operations...)
}

// BridgeValidation is the outcome of ValidateBridge.
type BridgeValidation struct {
	// Errors are why the routine must not be stored as passed.
	Errors []string
	// Notes do not fail it: what a run returned in each scenario.
	Notes []string
	// Operations are the bridge operations it called, sorted (PA-RT-04).
	Operations []string
}

// Log renders the validation for the routine log.
func (v BridgeValidation) Log() string {
	var b strings.Builder
	for i, e := range v.Errors {
		fmt.Fprintf(&b, "Validation error %d: %s\n", i+1, e)
	}
	if len(v.Notes) > 0 {
		b.WriteString("\nRuns against the API's mock answers:\n")
		for _, n := range v.Notes {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	if len(v.Operations) > 0 {
		fmt.Fprintf(&b, "\nAPI operations called: %s\n", strings.Join(v.Operations, ", "))
	}
	return b.String()
}

// ValidateBridge runs the routine once per mock scenario against the
// bridge's mock answers. b is nil when no catalogue is loaded, which fails
// the routine: it cannot be checked.
func (e *JSExecutorRuntime) ValidateBridge(b *apibridge.Bridge, limit time.Duration) BridgeValidation {
	var v BridgeValidation
	if b == nil {
		v.Errors = append(v.Errors, "the platform API catalogue is not available, so a bridge routine cannot be validated")
		return v
	}
	called := map[string]bool{}
	problems := map[string]bool{}
	for i, sc := range apicatalogue.Scenarios {
		pass := b.NewMockPass(sc)
		result, err := e.Run(func(vm *goja.Runtime) goja.Value {
			return pass.ObjectFor(vm, apibridge.Identity{UserID: 1, Role: "registered"})
		}, limit)
		for _, c := range pass.Calls() {
			called[c.Operation] = true
		}
		for _, p := range pass.Violations() {
			problems[p] = true
		}
		switch {
		case err != nil:
			v.Notes = append(v.Notes, fmt.Sprintf("%d %s: did not run: %v", i+1, sc, err))
		default:
			v.Notes = append(v.Notes, fmt.Sprintf("%d %s: %s — %s", i+1, sc, result.Status, result.Message))
		}
	}
	for p := range problems {
		v.Errors = append(v.Errors, p)
	}
	sort.Strings(v.Errors)
	for op := range called {
		v.Operations = append(v.Operations, op)
	}
	sort.Strings(v.Operations)
	return v
}
