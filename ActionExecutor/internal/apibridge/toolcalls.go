// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The tool calls of one run, for its audit entry.
//
// api.tools.execute hands its result straight back to the routine, so nothing
// of it would otherwise reach the Go side that writes the entry. A ToolCallLog
// given to BuildRecording collects each call as it happens: the tool and its
// proxy by name, as ToolManager reports them, and whether it succeeded. A call
// ToolManager refused never got as far as naming them, so it is listed by the
// tool record's id.

package apibridge

import (
	"fmt"
	"strings"
	"sync"

	"tachyonik/actionexecutor/internal/runaudit"
)

// ToolCallLog collects the tool calls of one run. Safe for concurrent use; the
// zero value is ready.
type ToolCallLog struct {
	mu    sync.Mutex
	calls []runaudit.ToolCall
}

// Calls returns the calls made so far, in order.
func (l *ToolCallLog) Calls() []runaudit.ToolCall {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]runaudit.ToolCall(nil), l.calls...)
}

func (l *ToolCallLog) add(c runaudit.ToolCall) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.calls = append(l.calls, c)
	l.mu.Unlock()
}

// recordFailed records a call ToolManager did not carry out.
func (l *ToolCallLog) recordFailed(toolID int64, err error) {
	l.add(runaudit.ToolCall{Tool: fmt.Sprintf("tool %d", toolID), Reason: err.Error()})
}

// recordResult records a call from ToolManager's answer: failed when the tool
// reported an error or its output could not be stored.
func (l *ToolCallLog) recordResult(toolID int64, result map[string]interface{}) {
	c := runaudit.ToolCall{OK: true}
	c.Tool, _ = result["toolName"].(string)
	c.Proxy, _ = result["proxyName"].(string)
	if c.Tool == "" {
		c.Tool = fmt.Sprintf("tool %d", toolID)
	}
	if isError, _ := result["isError"].(bool); isError {
		c.OK = false
		c.Reason, _ = result["content"].(string)
	} else if errs, _ := result["errors"].([]interface{}); len(errs) > 0 {
		c.OK = false
		texts := make([]string, 0, len(errs))
		for _, e := range errs {
			texts = append(texts, fmt.Sprint(e))
		}
		c.Reason = strings.Join(texts, "; ")
	}
	l.add(c)
}
