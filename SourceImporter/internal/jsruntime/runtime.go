// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jsruntime executes the AI-generated import routines in an embedded
// JavaScript engine (goja) and converts what they return into Go values.
//
// A routine is a single importFunction(ctx) handed a context carrying the
// source's text, name, type and media type, plus a parsed view of the content:
// ctx.xml, ctx.json or ctx.csv, whichever the file turned out to be. It returns
// assets, vulnerabilities and detections.
//
// Because both the routine and the file it reads originate outside this daemon —
// one written by a model, the other uploaded by a user — the engine is contained
// rather than trusted. Every execution gets its own runtime, discarded
// afterwards, so nothing carries from one file to the next or from one user to
// another; and every execution runs under an interrupt budget, because goja
// cannot be preempted and this daemon polls on a single goroutine. See
// security_test.go, which pins both properties.
package jsruntime

import (
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"
	"tachyonik/lib/jsmap"
	"tachyonik/lib/logger"
)

// ImportContext contains the data passed to the JS importFunction
type ImportContext struct {
	FileContent string `json:"fileContent"`
	FileName    string `json:"fileName"`
	SourceType  string `json:"sourceType"`
	SourceRef   string `json:"sourceRef"`
	// MimeType is the detected media type of the source (e.g. "application/pdf").
	// For PDFs FileContent holds the extracted text; MimeType lets a routine
	// branch on the original type independently of the text.
	MimeType string `json:"mimeType"`
}

// AssetResult represents an asset extracted by the JS import function
type AssetResult struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	LastSeen string `json:"lastSeen"`
}

// VulnerabilityResult represents a vulnerability extracted by the JS import function
type VulnerabilityResult struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Severity int    `json:"severity"`
	LastSeen string `json:"lastSeen"`
}

// DetectionResult represents a detection extracted by the JS import function
type DetectionResult struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	LastSeen string `json:"lastSeen"`
}

// ImportResult contains all data extracted by the JS import function
type ImportResult struct {
	Assets          []AssetResult
	Vulnerabilities []VulnerabilityResult
	Detections      []DetectionResult
}

// JSImportExecutor manages and executes a JavaScript import function.
//
// It holds the routine's SOURCE, not a live VM. Each execution builds its own
// goja.Runtime and throws it away afterwards.
//
// The alternative — one VM per rule, reused for every file — leaked state
// between runs: a routine that set a global while parsing one user's file saw
// it again while parsing the next user's, so a bug in generated code could
// carry data across tenants. Re-parsing costs one compile of a few kilobytes
// per import, against imports that are seconds apart and dominated by HTTP.
//
// It also removes a concurrency trap. A goja.Runtime is not goroutine-safe, yet
// Execute took a read lock, advertising that concurrent calls were fine; with a
// VM per call that is now true rather than merely unexercised.
type JSImportExecutor struct {
	mu   sync.RWMutex
	code string
	// budget bounds one run. A field rather than a constant read directly so
	// the tests that prove the interrupt works can use a short one — at the
	// real 30s each of them would cost half a minute of wall clock to assert
	// something that is decided in the first millisecond.
	budget time.Duration
}

// ExecutionBudget bounds one routine run — loading its top-level code, or
// executing importFunction against one file. Generous for parsing a few
// megabytes; a routine needing longer than this is looping.
const ExecutionBudget = 30 * time.Second

// runGuarded runs fn, interrupting vm if it exceeds timeout.
//
// goja has no preemption, so an infinite loop or a catastrophic regex in
// routine code would otherwise hang the caller forever — and this daemon polls
// on a single goroutine, so one wedged routine stops every import and every
// re-import, silently, until the process is restarted. The routines are
// AI-generated, which makes that a question of when rather than whether.
// Runtime.Interrupt is the sanctioned way to abort from another goroutine. Note
// it aborts JS, not Go: a loop inside a Go callee is not interruptible and
// needs fixing at the source.
//
// The mutex orders the timer's Interrupt against our ClearInterrupt.
// time.Timer.Stop does not wait for a timer already firing, so without it an
// Interrupt could land after ClearInterrupt and leave the flag set — and the
// next execution on that VM would abort instantly with "budget exceeded", a
// slow routine silently poisoning its successor.
//
// The recover is load-bearing. goja reports an interrupt (and a JS throw) by
// panicking out of APIs that have no error return — Value.String, Value.Export,
// ToObject — and only RunString and Callable.Call install a recover of their
// own. Result extraction calls those on values the routine controls, so
// catching it here is what keeps an overrunning routine from taking the daemon
// down instead of just being rejected.
//
// Ported from SourceAnalyser's jsruntime, where the ordering and the recover
// were reasoned out; the two daemons face the same hazard.
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

// New creates a new JSImportExecutor
func New() *JSImportExecutor {
	return &JSImportExecutor{budget: ExecutionBudget}
}

// executionBudget is the executor's budget, falling back to the default for a
// zero-valued struct.
func (e *JSImportExecutor) executionBudget() time.Duration {
	if e.budget <= 0 {
		return ExecutionBudget
	}
	return e.budget
}

// LoadFromString parses JavaScript code and extracts the importFunction variable
func (e *JSImportExecutor) LoadFromString(code string) error {
	// Compiled once here to reject bad code at load time rather than at the
	// first import; the VM built for the check is discarded.
	if _, _, err := instantiate(code, e.executionBudget()); err != nil {
		return err
	}

	e.mu.Lock()
	e.code = code
	e.mu.Unlock()

	logger.Infof("Loaded JS import function (%d bytes)", len(code))
	return nil
}

// instantiate builds a VM, runs the routine's top-level code and returns its
// importFunction. Both steps are under the execution budget: a routine whose
// module body loops never reaches its function at all.
func instantiate(code string, budget time.Duration) (*goja.Runtime, goja.Callable, error) {
	vm := goja.New()

	if _, err := runGuarded(vm, budget, func() (goja.Value, error) {
		return vm.RunString(code)
	}); err != nil {
		return nil, nil, fmt.Errorf("failed to execute JS code: %w", err)
	}

	fnVal := vm.Get("importFunction")
	if fnVal == nil || goja.IsUndefined(fnVal) || goja.IsNull(fnVal) {
		return nil, nil, fmt.Errorf("JS code does not define an 'importFunction' variable")
	}

	importFn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return nil, nil, fmt.Errorf("'importFunction' is not a function")
	}
	return vm, importFn, nil
}

// Execute calls the importFunction with the given context and returns the result
func (e *JSImportExecutor) Execute(ctx ImportContext) (*ImportResult, error) {
	e.mu.RLock()
	code := e.code
	e.mu.RUnlock()

	if code == "" {
		return nil, fmt.Errorf("no import function loaded")
	}

	// A VM of this file's own, so nothing a routine leaves behind can reach the
	// next file — or the next user's.
	vm, importFn, err := instantiate(code, e.executionBudget())
	if err != nil {
		return nil, err
	}

	ctxVal := buildCtxValue(vm, ctx)

	// Everything from here touches values the routine controls, so it all runs
	// under one budget: extractResult calls Export, ToObject and String, which
	// invoke getters and toString the routine itself may define. Guarding only
	// the call would leave those unbounded.
	result, err := runGuarded(vm, e.executionBudget(), func() (goja.Value, error) {
		return importFn(goja.Undefined(), ctxVal)
	})
	if err != nil {
		return nil, fmt.Errorf("importFunction execution failed: %w", err)
	}

	if result == nil || goja.IsUndefined(result) || goja.IsNull(result) {
		return nil, fmt.Errorf("importFunction returned nil/undefined")
	}

	var out *ImportResult
	if _, err := runGuarded(vm, e.executionBudget(), func() (goja.Value, error) {
		var eerr error
		out, eerr = extractResult(vm, result)
		return nil, eerr
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// buildCtxValue assembles the object exposed to the JavaScript import
// function as `ctx`. Exactly one of `ctx.xml`, `ctx.json`, `ctx.csv` is
// populated per file, chosen by content sniffing in that priority order:
// XML first, then JSON, then CSV as a catch-all. The others are null. The
// routine can still fall back to `ctx.fileContent` for any format that
// does not fit the pre-parsed bindings.
func buildCtxValue(vm *goja.Runtime, ctx ImportContext) goja.Value {
	ctxMap := map[string]interface{}{
		"fileContent": ctx.FileContent,
		"fileName":    ctx.FileName,
		"sourceType":  ctx.SourceType,
		"sourceRef":   ctx.SourceRef,
		"mimeType":    ctx.MimeType,
		"xml":         nil,
		"json":        nil,
		"csv":         nil,
	}

	switch {
	case LooksLikeXML(ctx.FileContent):
		if doc := ParseXMLDoc(ctx.FileContent); doc != nil {
			ctxMap["xml"] = BuildXMLBinding(vm, doc)
		}
	case LooksLikeJSON(ctx.FileContent):
		if parsed := ParseJSON(ctx.FileContent); parsed != nil {
			ctxMap["json"] = BuildJSONBinding(vm, parsed)
		}
	default:
		// Anything else gets a CSV binding. Worst case rows[i] has a single
		// column; routines can inspect ctx.csv.rows[0].length to decide
		// whether the data is tabular.
		if ctx.FileContent != "" {
			ctxMap["csv"] = BuildCSVBinding(vm, ctx.FileContent)
		}
	}

	return vm.ToValue(ctxMap)
}

// extractResult converts the JS return value into an ImportResult
func extractResult(vm *goja.Runtime, val goja.Value) (*ImportResult, error) {
	obj := val.ToObject(vm)
	if obj == nil {
		return nil, fmt.Errorf("importFunction did not return an object")
	}

	result := &ImportResult{}

	// Extract assets
	assetsVal := obj.Get("assets")
	if assetsVal != nil && !goja.IsUndefined(assetsVal) && !goja.IsNull(assetsVal) {
		assetsExported := assetsVal.Export()
		if assetsSlice, ok := assetsExported.([]interface{}); ok {
			for _, item := range assetsSlice {
				if m, ok := item.(map[string]interface{}); ok {
					result.Assets = append(result.Assets, AssetResult{
						Name:     jsmap.String(m, "name"),
						Type:     jsmap.String(m, "type"),
						LastSeen: jsmap.String(m, "lastSeen"),
					})
				}
			}
		}
	}

	// Extract vulnerabilities
	vulnsVal := obj.Get("vulnerabilities")
	if vulnsVal != nil && !goja.IsUndefined(vulnsVal) && !goja.IsNull(vulnsVal) {
		vulnsExported := vulnsVal.Export()
		if vulnsSlice, ok := vulnsExported.([]interface{}); ok {
			for _, item := range vulnsSlice {
				if m, ok := item.(map[string]interface{}); ok {
					result.Vulnerabilities = append(result.Vulnerabilities, VulnerabilityResult{
						Name:     jsmap.String(m, "name"),
						Host:     jsmap.String(m, "host"),
						Port:     jsmap.String(m, "port"),
						Severity: jsmap.Int(m, "severity"),
						LastSeen: jsmap.String(m, "lastSeen"),
					})
				}
			}
		}
	}

	// Extract detections
	detectionsVal := obj.Get("detections")
	if detectionsVal != nil && !goja.IsUndefined(detectionsVal) && !goja.IsNull(detectionsVal) {
		detectionsExported := detectionsVal.Export()
		if detectionsSlice, ok := detectionsExported.([]interface{}); ok {
			for _, item := range detectionsSlice {
				if m, ok := item.(map[string]interface{}); ok {
					result.Detections = append(result.Detections, DetectionResult{
						Name:     jsmap.String(m, "name"),
						Host:     jsmap.String(m, "host"),
						Port:     jsmap.String(m, "port"),
						LastSeen: jsmap.String(m, "lastSeen"),
					})
				}
			}
		}
	}

	return result, nil
}

// ValidateWithMockCtx runs the importFunction against mock contexts to catch runtime errors.
// Tests with: (1) a mock XML file with hosts/vulnerabilities, (2) empty file content.
func (e *JSImportExecutor) ValidateWithMockCtx() []error {
	e.mu.RLock()
	code := e.code
	e.mu.RUnlock()

	if code == "" {
		return nil
	}

	// Each scenario gets its own VM, for the same reason execution does: a
	// routine that stashed state during scenario 1 must not have it in
	// scenario 2, or validation passes on behaviour that will not recur.

	mockContexts := []ImportContext{
		// Scenario 1: Mock XML content with hosts and vulnerabilities
		{
			FileContent: `<?xml version="1.0"?>
<report>
  <results>
    <result>
      <host>192.168.1.1</host>
      <port>443/tcp</port>
      <name>SQL Injection</name>
      <severity>7.5</severity>
      <modification_time>2025-07-01T00:00:00Z</modification_time>
    </result>
    <result>
      <host>192.168.1.1</host>
      <port>80/tcp</port>
      <name>HTTP Server Detection</name>
      <severity>0.0</severity>
      <modification_time>2025-07-01T00:00:00Z</modification_time>
    </result>
    <result>
      <host>192.168.1.2</host>
      <port>22/tcp</port>
      <name>SSH Weak Algorithms</name>
      <severity>4.3</severity>
      <modification_time>2025-07-01T00:00:00Z</modification_time>
    </result>
  </results>
</report>`,
			FileName:   "scan_report.xml",
			SourceType: "Test Report",
			SourceRef:  "test_report.xml (ID: 1)",
		},
		// Scenario 2: Empty file content (should not crash)
		{
			FileContent: "",
			FileName:    "empty.txt",
			SourceType:  "Test Report",
			SourceRef:   "empty.txt (ID: 2)",
		},
	}

	var errs []error

	for i, mockCtx := range mockContexts {
		vm, importFn, err := instantiate(code, e.executionBudget())
		if err != nil {
			errs = append(errs, fmt.Errorf("mock context %d: %w", i+1, err))
			continue
		}
		ctxVal := buildCtxValue(vm, mockCtx)

		result, err := runGuarded(vm, e.executionBudget(), func() (goja.Value, error) {
			return importFn(goja.Undefined(), ctxVal)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("mock context %d: importFunction failed: %w", i+1, err))
			continue
		}

		if result == nil || goja.IsUndefined(result) || goja.IsNull(result) {
			errs = append(errs, fmt.Errorf("mock context %d: importFunction returned nil/undefined", i+1))
			continue
		}

		// Verify the result is an object with expected fields
		obj := result.ToObject(vm)
		if obj == nil {
			errs = append(errs, fmt.Errorf("mock context %d: importFunction did not return an object", i+1))
			continue
		}

		// Check that assets, vulnerabilities, and detections fields exist
		for _, field := range []string{"assets", "vulnerabilities", "detections"} {
			fieldVal := obj.Get(field)
			if fieldVal == nil || goja.IsUndefined(fieldVal) {
				errs = append(errs, fmt.Errorf("mock context %d: result missing '%s' field", i+1, field))
			}
		}
	}

	return errs
}
