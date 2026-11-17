// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the calls this client makes against ResourceManager's API document
// (TachyonikLib apicontract): every path exists, and every field the client
// sends or decodes is documented.

package resourcemanager

import (
	"testing"

	"tachyonik/lib/apicontract"
)

var calls = []apicontract.Call{
	{Method: "GET", Path: "/api/sources", Answer: ListSourcesResponse{}},
	{Method: "PATCH", Path: "/api/sources/{id}", Request: UpdateSourceRequest{}},
	{Method: "GET", Path: "/api/sources/{id}/file"},
}

func TestTheClientMatchesResourceManagersDocument(t *testing.T) {
	apicontract.Test(t, "ResourceManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}
