// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the calls this client makes against SystemManager's API document
// (TachyonikLib apicontract): every path exists, and every field the client
// decodes is documented.

package systemmanager

import (
	"testing"

	"tachyonik/lib/apicontract"
)

var calls = []apicontract.Call{
	{Method: "GET", Path: "/api/internal/users", Answer: []User{}},
	{Method: "GET", Path: "/api/internal/users/{id}", Answer: User{}},
	{Method: "GET", Path: "/api/internal/workspace", Answer: Workspace{}},
}

func TestTheClientMatchesSystemManagersDocument(t *testing.T) {
	apicontract.Test(t, "SystemManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}
