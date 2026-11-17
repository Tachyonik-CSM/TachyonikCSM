// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the registration against AIManager's API document (apicontract):
// the path exists, and every field the request sends is documented.

package apiregister

import (
	"testing"

	"tachyonik/lib/apicontract"
)

var calls = []apicontract.Call{
	{Method: "PUT", Path: "/api/internal/api-catalogue/{module}", Request: Request{}},
}

func TestTheRegistrationMatchesAIManagersDocument(t *testing.T) {
	apicontract.Test(t, "AIManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}
