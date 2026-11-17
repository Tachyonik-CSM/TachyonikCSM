// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks that every API path written in auto-execute's code is among the
// calls declared against SystemManager's and ActionManager's documents
// (contract_systemmanager_test.go, contract_actionmanager_test.go).

package autoexecute

import (
	"testing"

	"tachyonik/lib/apicontract"
)

func TestEveryCallIsDeclared(t *testing.T) {
	apicontract.TestDeclared(t, ".", systemManagerCalls, actionManagerCalls)
}
