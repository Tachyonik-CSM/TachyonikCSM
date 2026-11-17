// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jsruntime executes the AI-generated action routines in an embedded
// JavaScript engine (goja).
//
// A routine is an array of rules, each a check(api) that decides whether the
// rule applies, a createAction(api) that describes the action to raise, and
// optionally one condition per action option. `api` is the platform API
// bridge (TachyonikLib apibridge): what a rule may read is what the managers'
// API documents offer. A routine declares that contract with a top-level
// `var routineContract = 2;` (PA-RT-01); a routine without it — a context
// routine, which was handed a hand-built ctx object — is no longer run.
// Evaluating and validating against the bridge is in bridge.go.
//
// Everything a routine does happens under a guard, because the code was written
// by a model from a prompt and goja cannot be preempted. Every call into
// JavaScript goes through runGuarded, which interrupts the engine once the
// configured budget is spent, and the call stack is capped so runaway recursion
// fails fast instead of taking the process's memory with it.
//
// What a routine returns is checked too. validateAction rejects an action whose
// type, status, assignment or priority falls outside the vocabulary
// ActionManager accepts, and truncates the free-text fields to their limits, so
// a routine cannot widen the platform's enums by inventing a value.
package jsruntime

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"tachyonik/actiongenerator/internal/actionmanager"
	"tachyonik/lib/jsmap"
	"tachyonik/lib/logger"
)

// jsRule represents a loaded JS rule with callable functions
type jsRule struct {
	name     string
	ruleID   int64
	checkFn  goja.Callable
	actionFn goja.Callable
	// optionFns decides, per action option id, whether that option applies to
	// the user the rule just matched. An option with no entry here always
	// applies — which is every option of a rule whose links carry no condition,
	// and every option at all until a routine is regenerated against a prompt
	// that has one.
	optionFns map[int64]goja.Callable
}

// JSRuleExecutor manages and executes JavaScript-based rules
type JSRuleExecutor struct {
	mu sync.RWMutex
	// code is the loaded routine. Every evaluation instantiates it in a VM of
	// its own (instance), so nothing one user's evaluation leaves in a global
	// can reach the next user's — the routine used to live in one VM shared by
	// every user. rules is the instantiation made when it was loaded, kept for
	// what the rules are (names, ids, option ids), not for running them.
	code        string
	rules       []jsRule
	execTimeout time.Duration
	// onDemand marks a routine whose rule has no trigger of its own, so that
	// validation does not reject the `return false` the system prompt tells the
	// model to write for it. See SetOnDemand.
	onDemand bool
	// contract is 2 for a bridge routine (it reads through the platform API
	// bridge), 1 for a context routine, which is no longer run. See bridge.go.
	contract int
	// operations are the bridge operations a bridge routine was recorded as
	// calling when it was generated (PA-RT-04); what its triggers are derived
	// from (PA-RT-07).
	operations []string
}

// New creates a new JSRuleExecutor. execTimeout bounds a single JS execution:
// loading a routine, one check(), one createAction(). A non-positive value
// disables the guard, which is only ever wanted in a test that wants to observe
// an unbounded run.
func New(execTimeout time.Duration) *JSRuleExecutor {
	return &JSRuleExecutor{execTimeout: execTimeout}
}

// runGuarded runs fn, interrupting vm if it exceeds timeout. goja has no
// preemption, so an infinite loop or catastrophic regex in routine code would
// otherwise hang the caller forever — and the code here is written by an AI
// from a prompt, which is exactly the code least worth trusting to terminate.
// Runtime.Interrupt is the sanctioned way to abort from another goroutine. Note
// it aborts JS, not Go: a loop inside a Go callee is not interruptible and
// needs fixing at the source.
//
// The mutex orders the timer's Interrupt against our ClearInterrupt.
// time.Timer.Stop does not wait for a timer that is already firing, so without
// it an Interrupt could land after ClearInterrupt and leave the flag set — and
// the next execution on this VM would abort instantly with "budget exceeded", a
// slow rule silently poisoning its successor.
//
// The recover is load-bearing. goja reports an interrupt (and a JS throw) by
// panicking out of APIs that have no error return — Value.String, Value.Export,
// ToInteger — and only RunString and Callable.Call install a recover of their
// own. Every JS-touching operation runs through here, so catching it here is
// what keeps a routine that overruns its budget from taking the daemon down
// instead of just being rejected.
//
// Lifted from SourceAnalyser, SourceImporter and ActionExecutor, which have
// carried this for longer; ActionGenerator had no guard at all.
func runGuarded(vm *goja.Runtime, timeout time.Duration, fn func() (goja.Value, error)) (v goja.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			v = nil
			err = fmt.Errorf("JS execution aborted: %v", r)
		}
	}()

	if timeout <= 0 {
		return fn()
	}

	var mu sync.Mutex
	finished := false

	t := time.AfterFunc(timeout, func() {
		mu.Lock()
		defer mu.Unlock()
		if !finished {
			vm.Interrupt("JS execution budget exceeded")
		}
	})

	// Deferred so it also runs when fn panics: otherwise the timer stays armed
	// and the interrupt flag survives into the next execution on this VM.
	defer func() {
		mu.Lock()
		finished = true
		mu.Unlock()
		t.Stop()
		vm.ClearInterrupt()
	}()

	return fn()
}

// LoadFromString parses JavaScript code and extracts the rules array
func (e *JSRuleExecutor) LoadFromString(code string) error {
	vm, loaded, err := e.instantiate(code)
	if err != nil {
		return err
	}
	contract := routineContract(vm)

	// Atomically swap rules
	e.mu.Lock()
	e.code = code
	e.rules = loaded
	e.contract = contract
	e.mu.Unlock()

	logger.Infof("Loaded %d JS rules", len(loaded))
	return nil
}

// instantiate runs a routine in a fresh VM and extracts its rules.
func (e *JSRuleExecutor) instantiate(code string) (*goja.Runtime, []jsRule, error) {
	vm := goja.New()
	// Bound recursion. The execution budget stops a routine that loops; it does
	// not stop one that recurses, which exhausts memory rather than time and
	// can do it faster than the timer fires. goja reports the overflow as a
	// JS exception, so an offending rule is rejected like any other bad one.
	vm.SetMaxCallStackSize(maxCallStackSize)

	var loaded []jsRule
	// One budget covers the top-level evaluation AND the extraction below.
	// Extraction is not inert bookkeeping: Export, indexed and named property
	// reads, String() and ToInteger() all invoke getters, toString and valueOf
	// that the routine itself defines, so guarding only RunString would leave
	// those hooks unbounded.
	_, err := runGuarded(vm, e.execTimeout, func() (goja.Value, error) {
		var lerr error
		loaded, lerr = loadRules(vm, code)
		return nil, lerr
	})
	if err != nil {
		return nil, nil, err
	}
	return vm, loaded, nil
}

// instance is a fresh instantiation of the loaded routine, for one evaluation.
// Caller holds e.mu (read).
func (e *JSRuleExecutor) instance() (*goja.Runtime, []jsRule, error) {
	return e.instantiate(e.code)
}

// loadOptionFns extracts the rule's optional `options` member: a map from
// action option id to a function deciding whether that option applies.
//
// Keys are strings because they are JavaScript object keys; a key that is not
// an option id is a fault in the generated routine rather than something to
// shrug off, since the effect would be an option silently never shown.
func loadOptionFns(vm *goja.Runtime, rObj *goja.Object, ruleName string) (map[int64]goja.Callable, error) {
	optionsVal := rObj.Get("options")
	if optionsVal == nil || goja.IsUndefined(optionsVal) || goja.IsNull(optionsVal) {
		return nil, nil
	}
	// Export first: ToObject wraps a primitive in a Number/String object rather
	// than failing, so a routine with `options: 42` would otherwise load with
	// an empty, silently ignored condition set. An array exports to a slice and
	// is rejected here for the same reason — the keys have to be option ids.
	if _, ok := optionsVal.Export().(map[string]interface{}); !ok {
		return nil, fmt.Errorf("rule '%s' has an 'options' member that is not an object of option conditions", ruleName)
	}
	optionsObj := optionsVal.ToObject(vm)
	if optionsObj == nil {
		return nil, fmt.Errorf("rule '%s' has an 'options' member that is not an object", ruleName)
	}

	fns := make(map[int64]goja.Callable)
	for _, key := range optionsObj.Keys() {
		optionID, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("rule '%s' options key %q is not an action option id", ruleName, key)
		}
		fn, ok := goja.AssertFunction(optionsObj.Get(key))
		if !ok {
			return nil, fmt.Errorf("rule '%s' options[%s] is not a function", ruleName, key)
		}
		fns[optionID] = fn
	}
	return fns, nil
}

// loadRules evaluates the routine and extracts its rules array. Always called
// inside runGuarded — every step here can run routine-defined code.
func loadRules(vm *goja.Runtime, code string) ([]jsRule, error) {
	// Execute the code
	_, err := vm.RunString(code)
	if err != nil {
		return nil, fmt.Errorf("failed to execute JS code: %w", err)
	}

	// Extract the rules array
	rulesVal := vm.Get("rules")
	if rulesVal == nil || goja.IsUndefined(rulesVal) || goja.IsNull(rulesVal) {
		return nil, fmt.Errorf("JS code does not define a 'rules' variable")
	}

	rulesObj := rulesVal.Export()
	rulesSlice, ok := rulesObj.([]interface{})
	if !ok {
		return nil, fmt.Errorf("'rules' is not an array")
	}

	var loaded []jsRule
	for i, item := range rulesSlice {
		ruleObj, ok := item.(*goja.Object)
		if !ok {
			// Try getting it from the VM directly
			ruleObj = vm.ToValue(item).ToObject(vm)
		}
		if ruleObj == nil {
			return nil, fmt.Errorf("rule[%d] is not an object", i)
		}

		// Actually, we need the goja objects. Let's use the VM's array directly.
		arrObj := rulesVal.ToObject(vm)
		ruleVal := arrObj.Get(fmt.Sprintf("%d", i))
		if ruleVal == nil {
			return nil, fmt.Errorf("rule[%d] is nil", i)
		}

		rObj := ruleVal.ToObject(vm)

		// Extract name
		nameVal := rObj.Get("name")
		if nameVal == nil || goja.IsUndefined(nameVal) {
			return nil, fmt.Errorf("rule[%d] missing 'name'", i)
		}
		name := nameVal.String()

		// Extract ruleId
		var ruleID int64
		ruleIDVal := rObj.Get("ruleId")
		if ruleIDVal != nil && !goja.IsUndefined(ruleIDVal) {
			ruleID = ruleIDVal.ToInteger()
		}

		// Extract check function
		checkVal := rObj.Get("check")
		if checkVal == nil || goja.IsUndefined(checkVal) {
			return nil, fmt.Errorf("rule[%d] (%s) missing 'check' function", i, name)
		}
		checkFn, ok := goja.AssertFunction(checkVal)
		if !ok {
			return nil, fmt.Errorf("rule[%d] (%s) 'check' is not a function", i, name)
		}

		// Extract createAction function
		actionVal := rObj.Get("createAction")
		if actionVal == nil || goja.IsUndefined(actionVal) {
			return nil, fmt.Errorf("rule[%d] (%s) missing 'createAction' function", i, name)
		}
		actionFn, ok := goja.AssertFunction(actionVal)
		if !ok {
			return nil, fmt.Errorf("rule[%d] (%s) 'createAction' is not a function", i, name)
		}

		// Optional per-option conditions, keyed by action option id. Absent is
		// the normal case: a routine generated before option conditions
		// existed has no such member, and everything it offers applies.
		optionFns, err := loadOptionFns(vm, rObj, name)
		if err != nil {
			return nil, err
		}

		loaded = append(loaded, jsRule{
			name:      name,
			ruleID:    ruleID,
			checkFn:   checkFn,
			actionFn:  actionFn,
			optionFns: optionFns,
		})
	}

	return loaded, nil
}

// raise runs a rule's createAction and its option conditions against the
// argument check() was given — the routine's api — and turns
// the result into a checked action request (AG-IF-11 to -14).
func (e *JSRuleExecutor) raise(vm *goja.Runtime, rule jsRule, arg goja.Value) (*actionmanager.CreateActionRequest, error) {
	// Call createAction function, and read its result, inside one budget:
	// Export runs whatever getters the returned object defines.
	var actionObj interface{}
	_, err := runGuarded(vm, e.execTimeout, func() (goja.Value, error) {
		actionResult, aerr := rule.actionFn(goja.Undefined(), arg)
		if aerr != nil {
			return nil, aerr
		}
		actionObj = actionResult.Export()
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("createAction error: %w", err)
	}

	actionMap, ok := actionObj.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("createAction did not return an object")
	}

	logger.Debugf("JS rule '%s' createAction returned: %+v", rule.name, actionMap)

	action := actionmanager.CreateActionRequest{
		Title:       jsmap.String(actionMap, "title"),
		Type:        jsmap.String(actionMap, "type"),
		Description: jsmap.String(actionMap, "description"),
		Status:      jsmap.String(actionMap, "status"),
		Priority:    jsmap.Int(actionMap, "priority"),
		AssignedTo:  jsmap.String(actionMap, "assignedTo"),
		IssuedBy:    jsmap.String(actionMap, "issuedBy"),
		Trigger:     jsmap.String(actionMap, "trigger"),
	}

	// Which of the rule's options do not apply to this user. Evaluated
	// here because this is where the api the rule read is at hand; an option whose
	// condition throws is left visible, since hiding a step the user needs
	// is worse than offering one they do not.
	if hidden := e.hiddenOptions(vm, rule, arg); hidden != nil {
		action.HiddenOptions = &hidden
	}

	if action.Status == "" {
		action.Status = "New"
	}
	if action.IssuedBy == "" {
		action.IssuedBy = "ActionGenerator"
	}
	if action.AssignedTo == "" {
		action.AssignedTo = "User"
	}

	// What comes back is whatever the model wrote, reaching a database and
	// then every screen that lists actions. Nothing downstream constrains
	// it: ActionManager checks that title, type and status are non-empty
	// and nothing about what they contain.
	if err := validateAction(&action, rule.name); err != nil {
		return nil, fmt.Errorf("produced an invalid action, skipping it: %w", err)
	}
	return &action, nil
}

// ScenarioDecision is what one rule's check() returned in one mock scenario.
type ScenarioDecision struct {
	Label string
	// "true", "false", or "error" when check() threw.
	Result string
}

// RuleValidation is the validation record for one rule.
type RuleValidation struct {
	Name      string
	Decisions []ScenarioDecision
	// What check(), createAction() and the option conditions read in any
	// scenario, as "operation:path".
	FieldsRead []string
	// What the API documents do not allow: a call to an operation the routine
	// may not make, an argument the operation does not have, or a field the
	// answer does not have — a typo or an invention (PA-VAL-02).
	Missing []string
}

// hiddenOptions runs the rule's per-option conditions and returns the ids of
// the options that do not apply.
//
// Returns nil when the routine declares no conditions at all, which the caller
// sends as "nothing hidden" — distinct from an empty slice only in intent, but
// worth keeping apart in the logs.
//
// Each condition gets its own execution budget. A condition that throws or
// overruns leaves its option visible: the alternative is hiding a step the user
// may need because a generated line of JavaScript was wrong.
func (e *JSRuleExecutor) hiddenOptions(vm *goja.Runtime, rule jsRule, api goja.Value) []int64 {
	if len(rule.optionFns) == 0 {
		return nil
	}
	hidden := []int64{}
	for optionID, fn := range rule.optionFns {
		result, err := runGuarded(vm, e.execTimeout, func() (goja.Value, error) {
			return fn(goja.Undefined(), api)
		})
		if err != nil {
			logger.Errorf("JS rule '%s' option %d condition error, leaving the option visible: %v",
				rule.name, optionID, err)
			continue
		}
		if !result.ToBoolean() {
			hidden = append(hidden, optionID)
		}
	}
	// Deterministic, so an unchanged answer compares equal and the action is
	// not rewritten (and re-broadcast) on every evaluation.
	sort.Slice(hidden, func(i, j int) bool { return hidden[i] < hidden[j] })
	return hidden
}

// SetOnDemand marks this executor's rule as one that fires only when asked for
// by name — a rule with no trigger prompt.
//
// It changes one judgement. A check() that returns false in every mock scenario
// while reading nothing is normally an error: the rule could never fire, which
// for a rule that is supposed to have a condition is a fault worth refusing. For
// an on-demand rule that IS the condition — the system prompt has the model
// write a plain `return false`, and the rule produces an action only when a
// forced evaluation asks it to. Without this, such a routine cannot be
// generated at all.
func (e *JSRuleExecutor) SetOnDemand(onDemand bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onDemand = onDemand
}

// ValidationReport is the outcome of ValidateBridge.
type ValidationReport struct {
	// Why the routine must not be stored as passed. Empty means it passes.
	Errors []error
	// Things worth knowing that do not fail the routine.
	Notes []string
	// Per rule, in load order: what check() decided in each scenario and what
	// it read. Written to the routine log so a result explains itself.
	Rules []RuleValidation
	// Operations are the bridge operations the routine called in any
	// scenario, sorted; stored with the routine (PA-RT-04).
	Operations []string
}

// Log renders the report for the routine log.
func (r ValidationReport) Log() string {
	var b strings.Builder
	for i, err := range r.Errors {
		fmt.Fprintf(&b, "Validation error %d: %v\n", i+1, err)
	}
	for _, note := range r.Notes {
		fmt.Fprintf(&b, "Note: %s\n", note)
	}
	if len(r.Rules) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Validation record — check() in each mock scenario, against the API's mock answers:\n")
		for _, rule := range r.Rules {
			fmt.Fprintf(&b, "  rule '%s'\n", rule.Name)
			for i, d := range rule.Decisions {
				fmt.Fprintf(&b, "    %d %-66s %s\n", i+1, d.Label, d.Result)
			}
			if len(rule.FieldsRead) > 0 {
				fmt.Fprintf(&b, "    fields read: %s\n", strings.Join(rule.FieldsRead, ", "))
			} else {
				b.WriteString("    fields read: none\n")
			}
			if len(rule.Missing) > 0 {
				fmt.Fprintf(&b, "    not allowed by the API documents: %s\n", strings.Join(rule.Missing, "; "))
			}
		}
	}
	if len(r.Operations) > 0 {
		fmt.Fprintf(&b, "\nAPI operations called: %s\n", strings.Join(r.Operations, ", "))
	}
	return b.String()
}

func allSame(results []bool) bool {
	for _, r := range results[1:] {
		if r != results[0] {
			return false
		}
	}
	return true
}

func setToSortedBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RuleIDs returns the ruleId of every loaded rule.
func (e *JSRuleExecutor) RuleIDs() []int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ids := make([]int64, len(e.rules))
	for i, r := range e.rules {
		ids[i] = r.ruleID
	}
	return ids
}

// RuleCount returns the number of loaded rules
func (e *JSRuleExecutor) RuleCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.rules)
}

// The vocabularies an action's fields are drawn from.
//
// Taken from where each is actually defined rather than invented here: the
// action types are the options in the WebUI's Action Rule dialog, and the two
// role fields are the sets documented on ActionManager's Action model and in
// the WebUI's ActionAssignedTo/ActionIssuedBy types. A value outside them is a
// broken rule — the UI has no way to render it and no filter selects it.
var (
	validActionTypes  = map[string]bool{"Fix": true, "Info": true, "Support": true}
	validActionStatus = map[string]bool{"New": true, "Done": true}
	validAssignedTo   = map[string]bool{"User": true, "Support": true, "ActionExecutor": true}
	validIssuedBy     = map[string]bool{"User": true, "Support": true, "ActionGenerator": true}
)

// maxCallStackSize bounds recursion in routine code. Deep enough that no
// reasonable rule reaches it — these are flat condition checks, not algorithms
// — and shallow enough that runaway recursion is an error rather than an
// out-of-memory kill.
const maxCallStackSize = 2048

// Bounds on the free-text an action carries.
//
// Generous on purpose: these exist to stop unbounded growth reaching the
// database and the UI, not to enforce a house style. A routine builds these
// strings by interpolating what it read — an asset name, an organisation — so the
// length is a property of the data as much as of the rule.
const (
	MaxActionTitleLen       = 200
	MaxActionDescriptionLen = 4000
	MaxActionTriggerLen     = 500

	MinActionPriority = 0
	MaxActionPriority = 100
)

// validateAction checks what a routine returned before it becomes a real
// action, and normalises what can be normalised.
//
// Two different policies, deliberately:
//
//   - Over-length text is TRUNCATED. The length comes from the data the rule
//     interpolated, so discarding an otherwise-correct action because an asset
//     had a long name would lose something the user needs over a cosmetic
//     problem.
//   - A field outside its vocabulary, or a priority outside its range, is an
//     ERROR and the action is dropped. Those are the rule being wrong rather
//     than the data being awkward, and a value the UI cannot render is worse
//     than no action at all.
func validateAction(a *actionmanager.CreateActionRequest, ruleName string) error {
	if strings.TrimSpace(a.Title) == "" {
		return fmt.Errorf("title is empty")
	}
	if !validActionTypes[a.Type] {
		return fmt.Errorf("type %q is not one of Fix, Info, Support", a.Type)
	}
	if !validActionStatus[a.Status] {
		return fmt.Errorf("status %q is not one of New, Done", a.Status)
	}
	if !validAssignedTo[a.AssignedTo] {
		return fmt.Errorf("assignedTo %q is not one of User, Support, ActionExecutor", a.AssignedTo)
	}
	if !validIssuedBy[a.IssuedBy] {
		return fmt.Errorf("issuedBy %q is not one of User, Support, ActionGenerator", a.IssuedBy)
	}
	if a.Priority < MinActionPriority || a.Priority > MaxActionPriority {
		return fmt.Errorf("priority %d is outside %d-%d", a.Priority, MinActionPriority, MaxActionPriority)
	}

	a.Title = truncate(a.Title, MaxActionTitleLen, ruleName, "title")
	a.Description = truncate(a.Description, MaxActionDescriptionLen, ruleName, "description")
	a.Trigger = truncate(a.Trigger, MaxActionTriggerLen, ruleName, "trigger")
	return nil
}

// truncate shortens s to max runes, saying so in the log.
//
// Counts runes rather than bytes: cutting a UTF-8 string at a byte offset can
// split a character and put an invalid sequence into the database.
func truncate(s string, max int, ruleName, field string) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	logger.Warnf("JS rule '%s': %s was %d characters, truncated to %d", ruleName, field, len(runes), max)
	return string(runes[:max])
}
