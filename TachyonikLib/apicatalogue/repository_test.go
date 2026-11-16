// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Reads every manager's API document from this repository, as the catalogue
// will hold them together, and fails on what only shows when they meet: a
// namespace two managers both use (PA-CAT-03), which AIManager would refuse
// with 409 only when the second one registers — at a customer's startup
// rather than in a test.

package apicatalogue

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// catalogueManagers are the modules with an API document (PA-CAT-01).
var catalogueManagers = []string{"AssetManager", "SystemManager", "ResourceManager", "ActionManager", "AIManager", "ToolManager"}

func TestTheRepositorysDocumentsFitTogether(t *testing.T) {
	owner := map[string]string{}
	bridged := 0
	for _, m := range catalogueManagers {
		path := filepath.Join("..", "..", m, "spec", "openapi.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", m, err)
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		for _, e := range doc.Check() {
			t.Errorf("%s: %v", m, e)
		}
		spaces := map[string]bool{}
		for _, op := range doc.Operations {
			spaces[op.Namespace()] = true
			if op.Bridge != "" {
				bridged++
			}
		}
		names := make([]string, 0, len(spaces))
		for ns := range spaces {
			names = append(names, ns)
		}
		sort.Strings(names)
		for _, ns := range names {
			if prev, ok := owner[ns]; ok {
				t.Errorf("namespace %q is used by both %s and %s", ns, prev, m)
				continue
			}
			owner[ns] = m
		}
	}
	if bridged == 0 {
		t.Error("no document offers a bridge operation")
	}
}
