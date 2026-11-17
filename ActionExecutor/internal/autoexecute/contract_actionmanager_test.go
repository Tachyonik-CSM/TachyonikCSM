// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the call this package makes to ActionManager (through servicecall)
// against ActionManager's API document (TachyonikLib apicontract): the path
// exists, and the body the handler sends is documented.

package autoexecute

import (
	"testing"

	"tachyonik/lib/apicontract"
)

// deleteActionsRequest is the body deleteAction sends, named so the check
// can walk it.
type deleteActionsRequest struct {
	IDs []int64 `json:"ids"`
}

var actionManagerCalls = []apicontract.Call{
	{Method: "DELETE", Path: "/api/actions", Request: deleteActionsRequest{}},
}

func TestTheActionManagerCallsMatchItsDocument(t *testing.T) {
	apicontract.Test(t, "ActionManager", actionManagerCalls)
}
