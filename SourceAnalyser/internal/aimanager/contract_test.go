// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the calls this client makes against AIManager's API document
// (TachyonikLib apicontract): every path exists, and every field the client
// sends or decodes is documented.

package aimanager

import (
	"testing"

	"tachyonik/lib/apicontract"
)

var calls = []apicontract.Call{
	{Method: "GET", Path: "/api/internal/analysis-rules", Answer: AnalysisRulesResponse{}},
	{Method: "PATCH", Path: "/api/internal/analysis-rules/%d", Request: map[string]any{}},
	{Method: "POST", Path: "/api/internal/routines", Request: CreateRoutineRequest{}, Answer: Routine{}},
	{Method: "GET", Path: "/api/internal/routines/%d", Answer: Routine{}},
	{Method: "GET", Path: "/api/internal/module-ai-settings/{module}", Answer: ModuleAISetting{}},
	{Method: "GET", Path: "/api/internal/ais/%d", Answer: AIEntry{}},
	{Method: "GET", Path: "/api/internal/ais/by-name/{name}", Answer: AIEntry{}},
}

func TestTheClientMatchesAIManagersDocument(t *testing.T) {
	apicontract.Test(t, "AIManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}
