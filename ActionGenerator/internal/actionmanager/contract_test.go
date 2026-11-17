// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the calls this client makes against ActionManager's API document
// (TachyonikLib apicontract): every path exists, and every field the client
// sends or decodes is documented.

package actionmanager

import (
	"testing"

	"tachyonik/lib/apicontract"
)

// The bodies and answers the client builds inline, named so the check can
// walk them.
type (
	hiddenOptionsRequest struct {
		ActionID      int64   `json:"actionId"`
		HiddenOptions []int64 `json:"hiddenOptions"`
	}
	actionsAnswer struct {
		Actions []Action `json:"actions"`
		Total   int      `json:"total"`
	}
	deleteByRuleRequest struct {
		UserID       int64 `json:"userId"`
		ActionRuleID int64 `json:"actionRuleId"`
	}
	deleteByRuleSharedRequest struct {
		ActionRuleID int64 `json:"actionRuleId"`
		KeepUserID   int64 `json:"keepUserId"`
	}
)

var calls = []apicontract.Call{
	{Method: "POST", Path: "/api/internal/actions/hidden-options", Request: hiddenOptionsRequest{}},
	{Method: "GET", Path: "/api/actions", Query: []string{"userId"}, Answer: actionsAnswer{}},
	{Method: "POST", Path: "/api/actions", Request: CreateActionRequest{}, Answer: Action{}},
	{Method: "POST", Path: "/api/internal/actions/delete-by-rule", Request: deleteByRuleRequest{}, Answer: deleteResponse{}},
	{Method: "POST", Path: "/api/internal/actions/delete-by-rule-shared", Request: deleteByRuleSharedRequest{}, Answer: deleteResponse{}},
}

func TestTheClientMatchesActionManagersDocument(t *testing.T) {
	apicontract.Test(t, "ActionManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}
