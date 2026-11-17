// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that the tool calls a run made through the platform API bridge are
// listed for its audit entry: by tool and proxy name as ToolManager answers,
// a tool's own error as a failure with its message, and a call that failed or
// was refused by the tool record's id — still listed although the routine saw
// it thrown. Other operations are not tool calls.

package runaudit

import (
	"errors"
	"testing"

	libbridge "tachyonik/lib/apibridge"
)

func TestARunsToolCallsAreListed(t *testing.T) {
	body := func(id int64) map[string]interface{} {
		return map[string]interface{}{"body": map[string]interface{}{"toolId": id}}
	}
	calls := ToolCallsOf([]libbridge.Call{
		{Operation: "toolRecords.list"},
		{Operation: ToolExecuteOperation, Args: body(1), Answer: map[string]interface{}{"toolName": "gmp-get-results", "proxyName": "office", "isError": false}},
		{Operation: ToolExecuteOperation, Args: body(2), Answer: map[string]interface{}{"toolName": "gmp-get-assets", "proxyName": "office", "isError": true, "content": "Authentication failed"}},
		{Operation: ToolExecuteOperation, Args: body(3), Err: errors.New("toolCalls.execute: status 503: Proxy is not connected")},
		{Operation: ToolExecuteOperation, Args: map[string]interface{}{"tool": 4}, Problem: `the operation has no parameter "tool"`},
	})
	if len(calls) != 4 {
		t.Fatalf("listed %d calls, want 4: %+v", len(calls), calls)
	}
	if c := calls[0]; c.Tool != "gmp-get-results" || c.Proxy != "office" || !c.OK {
		t.Errorf("first call %+v", c)
	}
	if c := calls[1]; c.OK || c.Reason != "Authentication failed" {
		t.Errorf("a tool's own error was not a failure with its message: %+v", c)
	}
	if c := calls[2]; c.OK || c.Tool != "tool 3" || c.Reason == "" {
		t.Errorf("a failed call %+v", c)
	}
	if c := calls[3]; c.OK || c.Tool != "a tool" || c.Reason == "" {
		t.Errorf("a refused call %+v", c)
	}
}
