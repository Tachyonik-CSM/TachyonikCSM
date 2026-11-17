// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the heartbeat this package sends against SystemManager's API
// document (apicontract): the path exists, and every field it sends is
// documented.

package heartbeat

import (
	"testing"

	"tachyonik/lib/apicontract"
)

var calls = []apicontract.Call{
	{Method: "POST", Path: "/api/internal/service-heartbeats", Request: payload{}},
}

func TestTheHeartbeatMatchesSystemManagersDocument(t *testing.T) {
	apicontract.Test(t, "SystemManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}
