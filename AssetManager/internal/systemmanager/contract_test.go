// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the calls this client makes against SystemManager's API document
// (TachyonikLib apicontract): every path exists, and every field the client
// sends or decodes is documented. The audit events go through the shared
// core (TachyonikLib systemmanager), which checks its own call.

package systemmanager

import (
	"testing"

	"tachyonik/lib/apicontract"
)

var calls = []apicontract.Call{
	{Method: "GET", Path: "/api/internal/organisation", Query: []string{"userId"}, Answer: Organisation{}},
	{Method: "PATCH", Path: "/api/internal/organisation/sub-score", Request: subScoreRequest{}},
	{Method: "GET", Path: "/api/internal/users/ids", Answer: userIDs{}},
	{Method: "GET", Path: "/api/internal/users/{id}", Answer: User{}},
	{Method: "GET", Path: "/api/internal/group-members", Query: []string{"userId"}, Answer: userIDs{}},
	{Method: "POST", Path: "/api/internal/stats", Request: statsEntryRequest{}},
}

func TestTheClientMatchesSystemManagersDocument(t *testing.T) {
	apicontract.Test(t, "SystemManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}
