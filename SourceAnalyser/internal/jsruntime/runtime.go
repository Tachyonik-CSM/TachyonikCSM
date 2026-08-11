// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jsruntime executes AI-generated JavaScript analysis rules in an
// embedded goja (ES5.1) VM. It loads a `rules` array from JS source, evaluates
// each rule against a source's filename, content, size and media type, and
// returns the first rule that claims a match. It also validates freshly
// generated code against mock contexts before it is trusted.
//
// The routines are machine-generated and unreviewed, so every operation that
// can run them — top-level evaluation, analyze(), and the property reads during
// rule extraction, which dispatch to routine-defined getters and toString — is
// bounded by an execution budget and its overrun reported as an error. The VM
// itself is goja's bare runtime: no require, no filesystem, no network, so a
// routine's reach ends at the values handed to it.
package jsruntime

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"
	"tachyonik/lib/logger"
)

// errResultNotObject distinguishes "the rule returned a non-object" from a
// genuine execution failure, so each keeps its own log message now that both
// surface through the same guarded closure.
var errResultNotObject = errors.New("analyze() did not return an object")

// RuleContext contains all data available for JS rule evaluation. Content is
// the analyzable text of the source (extracted text for PDFs, raw bytes
// otherwise); MimeType is the detected media type (e.g. "application/pdf"),
// letting a rule assert the file's type independently of its text.
type RuleContext struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
	FileSize *int64 `json:"fileSize"`
	MimeType string `json:"mimeType"`
}

// AnalysisResult represents the result of a successful rule match
type AnalysisResult struct {
	SourceType string
	Status     string
	RuleName   string
	RuleID     int64
}

// jsRule represents a loaded JS rule with a callable analyze function
type jsRule struct {
	name      string
	ruleID    int64
	analyzeFn goja.Callable
}

// JSRuleExecutor manages and executes JavaScript-based analysis rules.
//
// mu is a plain Mutex, not an RWMutex: every operation here mutates the shared
// goja.Runtime — ToValue installs values, analyze() can write globals, and the
// execution guard clears the VM's interrupt flag. goja.Runtime is not
// goroutine-safe, so "concurrent readers" is not a state this type has.
type JSRuleExecutor struct {
	mu          sync.Mutex
	vm          *goja.Runtime
	rules       []jsRule
	execTimeout time.Duration
}

// New creates a new JSRuleExecutor. execTimeout bounds a single JS execution
// (top-level load or one analyze() call); when it elapses the VM is interrupted.
// A non-positive execTimeout disables the guard.
func New(execTimeout time.Duration) *JSRuleExecutor {
	return &JSRuleExecutor{execTimeout: execTimeout}
}

// runGuarded runs fn, interrupting vm if it exceeds timeout. goja has no
// preemption, so an infinite loop / catastrophic regex in routine code would
// otherwise hang the caller forever — and because LoadFromString runs while
// AIAnalyser holds its lock, that hang wedges rule loading and analysis for
// good. Runtime.Interrupt is the sanctioned way to abort from another
// goroutine. Note it aborts JS, not Go: a loop inside a Go callee (a
// dependency bug, say) is not interruptible and needs fixing at the source.
//
// The mutex orders the timer's Interrupt against our ClearInterrupt.
// time.Timer.Stop does not wait for a timer that is already firing, so without
// it an Interrupt could land after ClearInterrupt and leave the flag set — and
// the next execution on this VM would abort instantly with "budget exceeded",
// a slow rule silently poisoning its successor.
// The recover is load-bearing. goja reports an interrupt (and a JS throw) by
// panicking out of APIs that have no error return — Value.String, Value.Export,
// ToInteger — and only RunString and Callable.Call install a recover of their
// own. Every JS-touching operation runs through here, so catching it here is
// what keeps a routine that overruns its budget mid-extraction from taking the
// daemon down instead of just being rejected.
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

// LoadFromString parses JavaScript code and extracts the rules array.
//
// Both the top-level evaluation and the extraction run inside one execution
// budget. Extraction is not inert bookkeeping: Export, indexed and named
// property reads, String() and ToInteger() all invoke getters, toString and
// valueOf that the routine itself defines. Guarding only RunString left those
// hooks unbounded, so a rule could loop forever with no way to interrupt it.
func (e *JSRuleExecutor) LoadFromString(code string) error {
	vm := goja.New()

	var loaded []jsRule
	_, err := runGuarded(vm, e.execTimeout, func() (goja.Value, error) {
		if _, err := vm.RunString(code); err != nil {
			return nil, fmt.Errorf("failed to execute JS code: %w", err)
		}
		var err error
		loaded, err = extractRules(vm)
		return nil, err
	})
	if err != nil {
		return err
	}

	// Atomically swap rules
	e.mu.Lock()
	e.vm = vm
	e.rules = loaded
	e.mu.Unlock()

	logger.Infof("Loaded %d JS rules", len(loaded))
	return nil
}

// extractRules reads the `rules` array out of an evaluated VM. Callers MUST run
// it under runGuarded — every read here can dispatch back into routine JS.
func extractRules(vm *goja.Runtime) ([]jsRule, error) {
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
	for i := range rulesSlice {
		// Use the VM's array directly to get goja objects
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

		// Extract analyze function
		analyzeVal := rObj.Get("analyze")
		if analyzeVal == nil || goja.IsUndefined(analyzeVal) {
			return nil, fmt.Errorf("rule[%d] (%s) missing 'analyze' function", i, name)
		}
		analyzeFn, ok := goja.AssertFunction(analyzeVal)
		if !ok {
			return nil, fmt.Errorf("rule[%d] (%s) 'analyze' is not a function", i, name)
		}

		loaded = append(loaded, jsRule{
			name:      name,
			ruleID:    ruleID,
			analyzeFn: analyzeFn,
		})
	}

	return loaded, nil
}

// AnalyzeSource runs all rules against the context and returns the first non-null match
func (e *JSRuleExecutor) AnalyzeSource(ctx RuleContext) (*AnalysisResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.vm == nil || len(e.rules) == 0 {
		return nil, nil
	}

	var fileSize interface{}
	if ctx.FileSize != nil {
		fileSize = *ctx.FileSize
	}

	ctxVal := e.vm.ToValue(map[string]interface{}{
		"filename": ctx.Filename,
		"content":  ctx.Content,
		"fileSize": fileSize,
		"mimeType": ctx.MimeType,
	})

	for _, rule := range e.rules {
		// The call and the Export of its result share one budget: Export
		// dispatches to any getters the returned object defines, so reading the
		// result is as much routine-controlled execution as producing it.
		var resultMap map[string]interface{}
		_, err := runGuarded(e.vm, e.execTimeout, func() (goja.Value, error) {
			result, err := rule.analyzeFn(goja.Undefined(), ctxVal)
			if err != nil {
				return nil, err
			}
			// null/undefined means no match; resultMap stays nil
			if result == nil || goja.IsUndefined(result) || goja.IsNull(result) {
				return nil, nil
			}
			m, ok := result.Export().(map[string]interface{})
			if !ok {
				return nil, errResultNotObject
			}
			resultMap = m
			return nil, nil
		})
		if err != nil {
			if errors.Is(err, errResultNotObject) {
				logger.Errorf("JS rule '%s' analyze did not return an object", rule.name)
			} else {
				logger.Errorf("JS rule '%s' analyze error: %v", rule.name, err)
			}
			continue
		}

		if resultMap == nil {
			logger.Debugf("  rule '%s' (id %d): no match", rule.name, rule.ruleID)
			continue
		}

		sourceType := getStringField(resultMap, "sourceType")
		status := getStringField(resultMap, "status")

		if sourceType == "" {
			logger.Errorf("JS rule '%s' returned empty sourceType", rule.name)
			continue
		}

		logger.Debugf("  rule '%s' (id %d): MATCH type=%s status=%s", rule.name, rule.ruleID, sourceType, status)
		return &AnalysisResult{
			SourceType: sourceType,
			Status:     status,
			RuleName:   rule.name,
			RuleID:     rule.ruleID,
		}, nil
	}

	return nil, nil
}

// ValidateWithMockCtx runs every rule's analyze() against mock source contexts
// to catch runtime errors.
func (e *JSRuleExecutor) ValidateWithMockCtx() []error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.vm == nil || len(e.rules) == 0 {
		return nil
	}

	fileSize := int64(2048)

	mockContexts := []RuleContext{
		// Scenario 1: Nessus XML file
		{
			Filename: "scan_results.nessus",
			Content:  "<?xml version=\"1.0\" ?>\n<NessusClientData_v2>\n<Policy><policyName>test</policyName></Policy>\n<Report name=\"test\">",
			FileSize: &fileSize,
			MimeType: "text/xml; charset=utf-8",
		},
		// Scenario 2: OpenVAS XML file
		{
			Filename: "openvas-report.xml",
			Content:  "<report id=\"87bb1386-8a73-4ae4-b238-b4cdecaceb64\" format_id=\"a994b278-1f62-11e1-96ac-406186ea4fc5\">",
			FileSize: &fileSize,
			MimeType: "text/xml; charset=utf-8",
		},
		// Scenario 3: OpenVAS PDF report (already extracted to text)
		{
			Filename: "openvas-report.pdf",
			Content:  "OpenVAS Vulnerability Report\nThis document reports on the results of an automatic security scan.",
			FileSize: &fileSize,
			MimeType: "application/pdf",
		},
		// Scenario 4: Unsupported file
		{
			Filename: "random.bin",
			Content:  "\x00\x01\x02\x03\x04random binary data that matches nothing",
			FileSize: &fileSize,
			MimeType: "application/octet-stream",
		},
	}

	var errs []error

	for _, mockCtx := range mockContexts {
		var mockFileSize interface{}
		if mockCtx.FileSize != nil {
			mockFileSize = *mockCtx.FileSize
		}

		ctxVal := e.vm.ToValue(map[string]interface{}{
			"filename": mockCtx.Filename,
			"content":  mockCtx.Content,
			"fileSize": mockFileSize,
			"mimeType": mockCtx.MimeType,
		})

		for _, rule := range e.rules {
			_, err := runGuarded(e.vm, e.execTimeout, func() (goja.Value, error) {
				return rule.analyzeFn(goja.Undefined(), ctxVal)
			})
			if err != nil {
				errs = append(errs, fmt.Errorf("rule '%s' analyze() failed with mock ctx (file=%s): %w",
					rule.name, mockCtx.Filename, err))
			}
		}
	}

	return errs
}

func getStringField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
