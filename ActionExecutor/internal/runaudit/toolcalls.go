// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The tool calls of one run, for its audit entry.
//
// A routine runs a security tool with toolCalls.execute on the platform API
// bridge, which hands the answer straight back to the routine. The bridge's
// pass records every call it was asked for, and ToolCallsOf reads the tool
// runs from that record: the tool and its proxy by name, as ToolManager
// answers, and whether it succeeded. A call that failed or was refused never
// got as far as naming them, so it is listed by the tool record's id the
// routine passed.

package runaudit

import (
	"fmt"
	"strings"

	libbridge "tachyonik/lib/apibridge"
)

// ToolExecuteOperation is the bridge operation that runs a security tool.
const ToolExecuteOperation = "toolCalls.execute"

// ToolCallsOf turns the toolCalls.execute calls a run made through the
// platform API bridge into the audit entry's list, in order: what ran, on
// which proxy, and why it failed.
func ToolCallsOf(calls []libbridge.Call) []ToolCall {
	var out []ToolCall
	for _, c := range calls {
		if c.Operation != ToolExecuteOperation {
			continue
		}
		switch {
		case c.Problem != "":
			out = append(out, ToolCall{Tool: requestedTool(c.Args), Reason: c.Problem})
		case c.Err != nil:
			out = append(out, ToolCall{Tool: requestedTool(c.Args), Reason: c.Err.Error()})
		default:
			answer, _ := c.Answer.(map[string]interface{})
			out = append(out, fromAnswer(requestedTool(c.Args), answer))
		}
	}
	return out
}

// requestedTool names the tool a call asked for, by the record id in its
// body, when ToolManager did not name it.
func requestedTool(args map[string]interface{}) string {
	body, _ := args["body"].(map[string]interface{})
	switch id := body["toolId"].(type) {
	case int64, int, float64:
		return fmt.Sprintf("tool %v", id)
	}
	return "a tool"
}

// fromAnswer is a call from ToolManager's answer: failed when the tool
// reported an error or its output could not be stored.
func fromAnswer(fallback string, result map[string]interface{}) ToolCall {
	c := ToolCall{OK: true}
	c.Tool, _ = result["toolName"].(string)
	c.Proxy, _ = result["proxyName"].(string)
	if c.Tool == "" {
		c.Tool = fallback
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
	return c
}
