// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jsruntime executes the AI-generated execution routines in an embedded
// JavaScript engine (goja).
//
// A routine is an array of executors, each a run(api). api — the synchronous
// bridge in package apibridge — is the only way it can reach the outside
// world, for reading as for changing anything, so there is a single place
// where a routine's effects can be seen, bounded, or replaced with mocks.
//
// That substitution is what the routine Test tabs use (AIManager's package
// dryrun): a mock api that touches nothing, under an interrupt budget.
//
// A real run is bounded: Run interrupts a routine still going at its time
// limit (execution.timeout_seconds), and the run counts as failed.
package jsruntime

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"

	"tachyonik/lib/jsmap"
	"tachyonik/lib/logger"
)

// ExecResult is the parsed return value of an executor's run() function.
type ExecResult struct {
	Status  string                   `json:"status"`
	Message string                   `json:"message"`
	Changes []map[string]interface{} `json:"changes,omitempty"`
}

// jsExecutor is one executor of a routine, instantiated in one VM.
type jsExecutor struct {
	name   string
	ruleID int64
	runFn  goja.Callable
}

// MaxCallStackSize bounds recursion in routine code. Deep enough that no
// reasonable routine reaches it, shallow enough that runaway recursion is an
// error rather than a crash. The same value as ActionGenerator's.
const MaxCallStackSize = 2048

// errTimeLimit is the value a run is interrupted with when its limit passes.
var errTimeLimit = errors.New("time limit exceeded")

// JSExecutorRuntime holds one routine, compiled, and runs it.
//
// Every run gets a VM of its own, made from the compiled program. The routine
// used to live in one VM shared by every run of its rule, so whatever it kept
// outside run() — a `var cache = {}` at the top, say — survived into the next
// run, another user's included. SourceImporter has worked this way for the same
// reason. It also means runs of one rule no longer wait for each other.
type JSExecutorRuntime struct {
	mu      sync.RWMutex
	program *goja.Program
	ruleIDs []int64
}

// New creates a new JSExecutorRuntime.
func New() *JSExecutorRuntime {
	return &JSExecutorRuntime{}
}

// LoadFromString compiles routine code and checks that it defines a usable
// executors array, by instantiating it once.
func (e *JSExecutorRuntime) LoadFromString(code string) error {
	program, err := goja.Compile("routine", code, false)
	if err != nil {
		return fmt.Errorf("failed to execute JS code: %w", err)
	}
	_, executors, err := instantiate(program, LoadBudget)
	if err != nil {
		return err
	}
	ids := make([]int64, len(executors))
	for i, x := range executors {
		ids[i] = x.ruleID
	}

	e.mu.Lock()
	e.program = program
	e.ruleIDs = ids
	e.mu.Unlock()

	logger.Infof("Loaded %d JS executors", len(executors))
	return nil
}

// newVM is a bare VM with recursion bounded. The time limit stops a routine
// that loops; it does not stop one that recurses, which exhausts the stack
// faster than the timer fires — and a Go stack overflow cannot be recovered, so
// it took the whole daemon down. goja reports the overflow as a JavaScript
// exception instead, and the run fails with it like any other error.
func newVM() *goja.Runtime {
	vm := goja.New()
	vm.SetMaxCallStackSize(MaxCallStackSize)
	return vm
}

// LoadBudget bounds loading a routine: its top-level code runs once to check
// what it defines, and a loop there must not hang the loader.
const LoadBudget = 5 * time.Second

// instantiate runs the program's top-level code in a fresh VM, within budget,
// and extracts its executors.
func instantiate(program *goja.Program, budget time.Duration) (*goja.Runtime, []jsExecutor, error) {
	vm := newVM()
	timer := time.AfterFunc(budget, func() { vm.Interrupt(errTimeLimit) })
	defer timer.Stop()
	if _, err := vm.RunProgram(program); err != nil {
		return nil, nil, fmt.Errorf("failed to execute JS code: %w", err)
	}
	executors, err := extractExecutors(vm)
	return vm, executors, err
}

// extractExecutors reads the executors array a routine defines.
func extractExecutors(vm *goja.Runtime) ([]jsExecutor, error) {
	executorsVal := vm.Get("executors")
	if executorsVal == nil || goja.IsUndefined(executorsVal) || goja.IsNull(executorsVal) {
		return nil, fmt.Errorf("JS code does not define an 'executors' variable")
	}
	executorsSlice, ok := executorsVal.Export().([]interface{})
	if !ok {
		return nil, fmt.Errorf("'executors' is not an array")
	}

	var loaded []jsExecutor
	arrObj := executorsVal.ToObject(vm)
	for i := range executorsSlice {
		execVal := arrObj.Get(fmt.Sprintf("%d", i))
		if execVal == nil {
			return nil, fmt.Errorf("executors[%d] is nil", i)
		}
		eObj := execVal.ToObject(vm)

		nameVal := eObj.Get("name")
		if nameVal == nil || goja.IsUndefined(nameVal) {
			return nil, fmt.Errorf("executors[%d] missing 'name'", i)
		}
		name := nameVal.String()

		var ruleID int64
		if ruleIDVal := eObj.Get("ruleId"); ruleIDVal != nil && !goja.IsUndefined(ruleIDVal) {
			ruleID = ruleIDVal.ToInteger()
		}

		runVal := eObj.Get("run")
		if runVal == nil || goja.IsUndefined(runVal) {
			return nil, fmt.Errorf("executors[%d] (%s) missing 'run' function", i, name)
		}
		runFn, ok := goja.AssertFunction(runVal)
		if !ok {
			return nil, fmt.Errorf("executors[%d] (%s) 'run' is not a function", i, name)
		}
		loaded = append(loaded, jsExecutor{name: name, ruleID: ruleID, runFn: runFn})
	}
	if len(loaded) == 0 {
		return nil, fmt.Errorf("no executors loaded")
	}
	return loaded, nil
}

// Run runs the routine's first (and typically only) executor in a VM of its
// own and returns its parsed result. buildAPI makes the api object for that
// VM (apibridge.Bridge.Build); it is called once the routine is instantiated.
//
// If the JS throws or the return value cannot be parsed, the result is a
// failure saying so; an error means the run could not happen at all.
//
// limit bounds the run, top-level code included: when it passes, the
// JavaScript is interrupted and the result is a failure saying so. The
// interrupt lands between JavaScript statements; a call waiting in Go (an
// api.* request) is bounded by its own HTTP timeout instead. Zero means no
// limit.
func (e *JSExecutorRuntime) Run(buildAPI func(vm *goja.Runtime) goja.Value, limit time.Duration) (*ExecResult, error) {
	e.mu.RLock()
	program := e.program
	e.mu.RUnlock()
	if program == nil {
		return nil, fmt.Errorf("no executors loaded")
	}

	vm := newVM()
	if limit > 0 {
		timer := time.AfterFunc(limit, func() { vm.Interrupt(errTimeLimit) })
		defer timer.Stop()
	}

	failed := func(format string, args ...interface{}) *ExecResult {
		return &ExecResult{Status: "error", Message: fmt.Sprintf(format, args...)}
	}
	interrupted := func(err error) *ExecResult {
		var overflow *goja.StackOverflowError
		if errors.As(err, &overflow) {
			return failed("routine exceeded the maximum call depth of %d (runaway recursion?)", MaxCallStackSize)
		}
		var stopped *goja.InterruptedError
		if errors.As(err, &stopped) {
			return failed("routine exceeded its time limit of %s", limit)
		}
		return nil
	}

	if _, err := vm.RunProgram(program); err != nil {
		if r := interrupted(err); r != nil {
			return r, nil
		}
		return nil, fmt.Errorf("failed to execute JS code: %w", err)
	}
	executors, err := extractExecutors(vm)
	if err != nil {
		return nil, err
	}
	exec := executors[0]
	apiVal := goja.Undefined()
	if buildAPI != nil {
		apiVal = buildAPI(vm)
	}

	var result *ExecResult
	func() {
		defer func() {
			if r := recover(); r != nil {
				// goja panics with a goja.Value when JS throws.
				if v, ok := r.(*goja.Object); ok {
					result = failed("JS uncaught exception: %v", v.Export())
					return
				}
				result = failed("JS uncaught panic: %v", r)
			}
		}()

		ret, err := exec.runFn(goja.Undefined(), apiVal)
		if r := interrupted(err); r != nil {
			result = r
			return
		}
		if err != nil {
			result = failed("executor '%s' run() error: %v", exec.name, err)
			return
		}
		if ret == nil || goja.IsUndefined(ret) || goja.IsNull(ret) {
			result = failed("executor run() returned nothing")
			return
		}
		exported := ret.Export()
		retMap, ok := exported.(map[string]interface{})
		if !ok {
			result = failed("executor run() did not return an object (got %T)", exported)
			return
		}
		result = parseResult(retMap)
	}()

	if result == nil {
		return nil, fmt.Errorf("executor produced no result")
	}
	return result, nil
}

// RuleIDs returns the ruleId of every loaded executor.
func (e *JSExecutorRuntime) RuleIDs() []int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]int64(nil), e.ruleIDs...)
}

func parseResult(m map[string]interface{}) *ExecResult {
	r := &ExecResult{
		Status:  jsmap.String(m, "status"),
		Message: jsmap.String(m, "message"),
	}
	if r.Status == "" {
		r.Status = "success"
	}

	if changesRaw, ok := m["changes"]; ok && changesRaw != nil {
		if changesSlice, ok := changesRaw.([]interface{}); ok {
			for _, c := range changesSlice {
				if cMap, ok := c.(map[string]interface{}); ok {
					r.Changes = append(r.Changes, cMap)
				}
			}
		}
	}
	return r
}
