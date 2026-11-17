// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Reads every manager's API document from this repository and fails when its
// MCP server could not offer what it marks (PA-MCP-01, -03, -06): apimcp.New
// refuses the document, the tools differ from the operations marked `x-mcp`,
// or a marked operation is one a signed-in user cannot call — an internal,
// service-key-only route, or the MCP endpoint itself. Each manager's own
// mcp_test checks the same against its real router (Check).

package apimcp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"tachyonik/lib/apicatalogue"
)

var repositoryManagers = []string{"AssetManager", "SystemManager", "ResourceManager", "ActionManager", "AIManager", "ToolManager"}

func TestEveryManagersMCPServerOffersWhatItMarks(t *testing.T) {
	marked := 0
	for _, m := range repositoryManagers {
		data, err := os.ReadFile(filepath.Join("..", "..", m, "spec", "openapi.yaml"))
		if err != nil {
			t.Errorf("%s: %v", m, err)
			continue
		}
		doc, err := apicatalogue.Parse(data)
		if err != nil {
			t.Errorf("%s: %v", m, err)
			continue
		}
		srv, err := New(strings.ToLower(m), "test", doc, nil)
		if err != nil {
			t.Errorf("%s: its MCP server cannot be built: %v", m, err)
			continue
		}
		var got []string
		for _, tl := range srv.Tools() {
			got = append(got, tl.Name)
		}
		sort.Strings(got)
		want := Marked(doc)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: the MCP tools are [%s]; the document marks [%s]", m, strings.Join(got, ", "), strings.Join(want, ", "))
		}
		for _, op := range doc.Operations {
			if op.MCP == "" {
				continue
			}
			marked++
			if strings.HasPrefix(op.Path, "/api/internal/") || op.Path == "/api/mcp" {
				t.Errorf("%s: %s (%s %s) is marked x-mcp but is not for a signed-in user", m, op.ID, op.Method, op.Path)
			}
			if strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s: %s is marked x-mcp without a summary, which is its tool's description", m, op.ID)
			}
		}
	}
	if marked == 0 {
		t.Error("no document marks an operation for MCP")
	}
}
