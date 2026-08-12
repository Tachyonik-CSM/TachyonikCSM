// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package toolscan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/dop251/goja"

	"tachyonik/tachyonikproxy/internal/execenv"
	"tachyonik/tachyonikproxy/internal/netscan"
)

// Defaults that bound the resource usage of a single scan routine.
//
// These are *necessary* (not optional defence-in-depth): without them, a
// hostile or buggy ToolManager-provided routine can hang the scanner
// goroutine indefinitely, blocking every subsequent tools/scan call
// because Scan holds the scanner-wide mutex for the full batch.
//
// Vars (not consts) so tests can override without exporting the values.
// Production code never modifies them.
var (
	defaultRoutineTimeout = 30 * time.Second
	defaultExecTimeout    = 10 * time.Second
	defaultExecOutputCap  = 1 << 20 // 1 MiB
)

// ToolResult represents a detected (or undetected) tool.
//
// Host is the IP address (or hostname; whatever the routine produced)
// where the tool lives. For locally-executed tools the routine usually
// omits it and the scanner fills it in with the proxy's primary IPv4
// address. For HTTP/network-detected tools the routine sets it
// explicitly to the discovered host. ResourceManager uses Host as part
// of the per-row identity so two instances of the same tool on
// different hosts produce two rows.
//
// ToolOverviewID is the catalogue identity of the tool, copied from the
// owning RoutineInput. ResourceManager uses it as the authoritative
// tools.tool_id value rather than re-deriving from command-string
// matching. nil only when the routine itself arrived without an
// overview link (a data bug in AIManager's scan_rules row).
type ToolResult struct {
	Name           string `json:"name"`
	Detected       bool   `json:"detected"`
	Version        string `json:"version,omitempty"`
	Path           string `json:"path,omitempty"`
	Description    string `json:"description,omitempty"`
	Host           string `json:"host,omitempty"`
	ToolOverviewID *int64 `json:"toolOverviewId,omitempty"`
	Error          string `json:"error,omitempty"`
}

// RoutineInput represents a tool detection routine received from ToolManager.
//
// ToolOverviewID is the tool_overview.id from the originating AIManager
// scan_rule. The scanner stamps it onto every ToolResult emitted by this
// routine, including each entry in array-shaped detect() returns.
type RoutineInput struct {
	Name           string `json:"name"`
	Code           string `json:"code"`
	Version        string `json:"version"`
	SHA256         string `json:"sha256"`
	ToolOverviewID *int64 `json:"toolOverviewId,omitempty"`
}

// NetScanProvider supplies the latest local-network sweep to routines. It is
// satisfied by *netscan.Scanner; nil means no sweep is configured, in which
// case the netscan JS API reports itself as not ready and routines relying on
// it return "not detected" rather than failing.
type NetScanProvider interface {
	Snapshot() netscan.Snapshot
}

// Scanner executes tool-detection JS routines received from ToolManager
type Scanner struct {
	mu      sync.Mutex
	netScan NetScanProvider
}

// New creates a new Scanner with no local-network data.
func New() *Scanner {
	return &Scanner{}
}

// NewWithNetScan creates a Scanner whose routines can query the local-network
// sweep through the netscan JS global.
func NewWithNetScan(p NetScanProvider) *Scanner {
	return &Scanner{netScan: p}
}

// Scan runs the provided routines and returns results
func (s *Scanner) Scan(routines []RoutineInput) []ToolResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	var results []ToolResult
	for _, routine := range routines {
		// Verify SHA256 integrity
		if routine.SHA256 != "" {
			hash := sha256.Sum256([]byte(routine.Code))
			actual := hex.EncodeToString(hash[:])
			if actual != routine.SHA256 {
				results = append(results, ToolResult{
					Name:  routine.Name,
					Error: fmt.Sprintf("SHA256 mismatch: expected %s, got %s", routine.SHA256, actual),
				})
				continue
			}
		}

		ruleResults := s.executeRuleCode(routine.Code)
		// Stamp the routine's catalogue identity onto every emitted
		// result. A single routine corresponds to a single AIManager
		// scan_rule (and therefore a single tool_overview), so all of
		// its rule outputs share the same ToolOverviewID — including
		// the per-host fan-out from array-shaped detect() returns.
		if routine.ToolOverviewID != nil {
			oid := *routine.ToolOverviewID
			for i := range ruleResults {
				if ruleResults[i].Detected && ruleResults[i].ToolOverviewID == nil {
					ruleResults[i].ToolOverviewID = &oid
				}
			}
		}
		results = append(results, ruleResults...)
	}

	return results
}

// executeRuleCode runs a single JS rule file and returns results for each rule in it
func (s *Scanner) executeRuleCode(code string) []ToolResult {
	vm := goja.New()

	// Register exec() helper — runs a command and returns {output, exitCode}.
	// Bounded by a context deadline (defaultExecTimeout) so a hostile
	// routine cannot wedge the scanner with `exec("dd","if=/dev/zero",...)`,
	// and by an output-size cap so it cannot exhaust memory.
	vm.Set("exec", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			return vm.ToValue(map[string]interface{}{
				"output":   "",
				"exitCode": -1,
			})
		}

		cmdName := call.Arguments[0].String()

		var args []string
		if len(call.Arguments) > 1 {
			argsVal := call.Arguments[1].Export()
			if argsSlice, ok := argsVal.([]interface{}); ok {
				for _, a := range argsSlice {
					args = append(args, fmt.Sprintf("%v", a))
				}
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), defaultExecTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, cmdName, args...)
		// Sanitized environment: a ToolManager-supplied routine can run
		// arbitrary commands here, so never hand them the proxy's full
		// environment (enrollment material, tokens, etc.).
		cmd.Env = execenv.MinimalEnv()

		// Capture stdout+stderr through a single bounded reader. We
		// duplicate stderr to the same buffer because routines typically
		// inspect version strings which various tools print to stderr.
		buf := &boundedBuf{cap: defaultExecOutputCap}
		cmd.Stdout = buf
		cmd.Stderr = buf

		err := cmd.Run()

		exitCode := 0
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return vm.ToValue(map[string]interface{}{
					"output":   buf.String() + "\n... (exec timed out)",
					"exitCode": -1,
				})
			}
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				// Command not found or other error
				return vm.ToValue(map[string]interface{}{
					"output":   buf.String(),
					"exitCode": -1,
				})
			}
		}

		return vm.ToValue(map[string]interface{}{
			"output":   buf.String(),
			"exitCode": exitCode,
		})
	})

	// Register the local-network sweep view and the HTTP helper. Both are
	// pure lookups or bounded single requests — see jsbridge.go.
	s.registerNetScan(vm)
	registerHTTPGet(vm)

	// Register which() helper — returns the path of a command or ""
	vm.Set("which", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			return vm.ToValue("")
		}
		name := call.Arguments[0].String()
		path, err := exec.LookPath(name)
		if err != nil {
			return vm.ToValue("")
		}
		return vm.ToValue(path)
	})

	// Execute the JS code under a hard deadline. goja.Interrupt() raises
	// an exception inside the VM; if RunString is in a tight loop with no
	// host-fn calls, the interrupt still lands at the next interpreter
	// dispatch step. We also call Interrupt again after RunString returns,
	// to release any timer goroutine that may still be racing.
	timer := time.AfterFunc(defaultRoutineTimeout, func() {
		vm.Interrupt(fmt.Sprintf("scan routine timed out after %s", defaultRoutineTimeout))
	})
	defer timer.Stop()

	_, err := vm.RunString(code)
	if err != nil {
		return []ToolResult{{
			Name:  "unknown",
			Error: fmt.Sprintf("JS execution error: %v", err),
		}}
	}

	// Extract rules array
	rulesVal := vm.Get("rules")
	if rulesVal == nil || goja.IsUndefined(rulesVal) || goja.IsNull(rulesVal) {
		return nil
	}

	rulesObj := rulesVal.Export()
	rulesSlice, ok := rulesObj.([]interface{})
	if !ok {
		return nil
	}

	var results []ToolResult
	for i := range rulesSlice {
		arrObj := rulesVal.ToObject(vm)
		ruleVal := arrObj.Get(fmt.Sprintf("%d", i))
		if ruleVal == nil {
			continue
		}

		rObj := ruleVal.ToObject(vm)

		// Get rule name and description
		nameVal := rObj.Get("name")
		name := "unknown"
		if nameVal != nil && !goja.IsUndefined(nameVal) {
			name = nameVal.String()
		}

		descVal := rObj.Get("description")
		desc := ""
		if descVal != nil && !goja.IsUndefined(descVal) {
			desc = descVal.String()
		}

		// Call detect()
		detectVal := rObj.Get("detect")
		if detectVal == nil || goja.IsUndefined(detectVal) {
			results = append(results, ToolResult{
				Name:        name,
				Description: desc,
				Error:       "missing detect() function",
			})
			continue
		}

		detectFn, ok := goja.AssertFunction(detectVal)
		if !ok {
			results = append(results, ToolResult{
				Name:        name,
				Description: desc,
				Error:       "detect is not a function",
			})
			continue
		}

		retVal, err := detectFn(goja.Undefined())
		if err != nil {
			results = append(results, ToolResult{
				Name:        name,
				Description: desc,
				Error:       fmt.Sprintf("detect() error: %v", err),
			})
			continue
		}

		// null/undefined means not detected
		if retVal == nil || goja.IsUndefined(retVal) || goja.IsNull(retVal) {
			results = append(results, ToolResult{
				Name:        name,
				Detected:    false,
				Description: desc,
			})
			continue
		}

		// detect() may return either a single object (one detection on
		// this proxy host, the original shape) or an array of objects
		// (one detection per discovered host — used by network probes
		// that find the same tool on multiple endpoints).
		exported := retVal.Export()
		var rawResults []map[string]interface{}
		switch v := exported.(type) {
		case map[string]interface{}:
			rawResults = []map[string]interface{}{v}
		case []interface{}:
			for _, e := range v {
				if m, ok := e.(map[string]interface{}); ok {
					rawResults = append(rawResults, m)
				}
			}
			if len(rawResults) == 0 {
				results = append(results, ToolResult{
					Name:        name,
					Description: desc,
					Error:       "detect() returned an array with no objects",
				})
				continue
			}
		default:
			results = append(results, ToolResult{
				Name:        name,
				Description: desc,
				Error:       "detect() did not return an object or array of objects",
			})
			continue
		}

		// Default host: the proxy's primary IPv4. Resolved once per
		// rule, only when at least one returned record needs it. If
		// the proxy can't determine a primary IP (no non-loopback
		// interface, network down at scan time), we leave host empty;
		// ResourceManager treats empty as wildcard during the
		// post-deploy migration window.
		var defaultHost string
		var defaultHostResolved bool
		for _, m := range rawResults {
			host := getStr(m, "host")
			if host == "" {
				if !defaultHostResolved {
					defaultHost = primaryIPv4()
					defaultHostResolved = true
				}
				host = defaultHost
			}
			results = append(results, ToolResult{
				Name:        name,
				Detected:    true,
				Version:     getStr(m, "version"),
				Path:        getStr(m, "path"),
				Description: getStr(m, "description"),
				Host:        host,
			})
		}
	}

	return results
}

// primaryIPv4 returns the first non-loopback IPv4 address bound to any
// active interface, as a string. Falls back to "" when nothing usable is
// available — a multi-homed box's "primary" IP is operator-defined, but
// for the host-attribution use case a single deterministic pick is good
// enough. Errors are swallowed: the scanner stays useful even if the
// network stack is misbehaving.
func primaryIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		return ip4.String()
	}
	return ""
}

func getStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// boundedBuf is an io.Writer that caps the total bytes written. Writes
// past the cap are silently dropped — we don't return an error because
// returning io.ErrShortWrite to os/exec causes Cmd.Run to surface it as
// a process-failure condition we don't actually want.
type boundedBuf struct {
	cap int
	buf []byte
}

func (b *boundedBuf) Write(p []byte) (int, error) {
	if len(b.buf) >= b.cap {
		return len(p), nil
	}
	room := b.cap - len(b.buf)
	if room >= len(p) {
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	b.buf = append(b.buf, p[:room]...)
	return len(p), nil
}

func (b *boundedBuf) String() string { return string(b.buf) }

var _ io.Writer = (*boundedBuf)(nil)
