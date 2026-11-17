// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the call this package makes to SystemManager against SystemManager's
// API document (TachyonikLib apicontract): the path exists, and every field
// it decodes is documented.

package autoexecute

import (
	"testing"

	"tachyonik/lib/apicontract"
)

var systemManagerCalls = []apicontract.Call{
	{Method: "GET", Path: "/api/user-execution-rule-settings", Answer: userExecutionRuleSettings{}},
}

func TestTheSystemManagerCallsMatchItsDocument(t *testing.T) {
	apicontract.Test(t, "SystemManager", systemManagerCalls)
}
