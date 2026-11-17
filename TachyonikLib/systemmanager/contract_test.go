// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the call this shared client makes against SystemManager's API
// document (apicontract): the path exists, and every field it sends is
// documented.

package systemmanager

import (
	"testing"

	"tachyonik/lib/apicontract"
)

var calls = []apicontract.Call{
	{Method: "POST", Path: "/api/internal/audit-events", Request: auditEventRequest{}},
}

func TestTheClientMatchesSystemManagersDocument(t *testing.T) {
	apicontract.Test(t, "SystemManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}
