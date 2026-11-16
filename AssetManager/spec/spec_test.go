// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Holds AssetManager's API document to the platform API catalogue's rules:
// operation names, namespaces, bridge marks and bindings, mock answers
// (docs/specs/01-platform-api-catalogue.md, PA-CAT-03 to -08).

package spec

import (
	"testing"

	"tachyonik/lib/apicatalogue"
)

// PA-CAT-03 to -08: the document is fit for the catalogue — names, marks,
// invalidating messages, and a mock answer for every scenario that fits its
// schema.
func TestDocumentIsFitForTheCatalogue(t *testing.T) {
	doc, err := apicatalogue.Parse(OpenAPI)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, e := range doc.Check() {
		t.Error(e)
	}
}

// PA-CAT-03: AssetManager's namespaces are its own: its records, and
// assetManager for the operation of the service itself.
func TestNamespaces(t *testing.T) {
	doc, err := apicatalogue.Parse(OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	own := map[string]bool{"assets": true, "vulnerabilities": true, "detections": true, "assetManager": true}
	for _, op := range doc.Operations {
		if !own[op.Namespace()] {
			t.Errorf("%s: namespace %q is not AssetManager's", op.ID, op.Namespace())
		}
	}
}

// PA-CAT-05, PA-BR-07: every bridge operation names its user only through a
// bound parameter, and no userId is left for a routine to choose.
func TestBridgeOperationsAreBound(t *testing.T) {
	doc, err := apicatalogue.Parse(OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range doc.BridgeOperations(true) {
		bound := false
		for _, p := range op.Parameters {
			if p.Name == "userId" && p.Bound == "" {
				t.Errorf("%s: userId must be x-bridge-bound — user, or omit where it narrows the answer to that one user", op.ID)
			}
			if p.Bound == apicatalogue.BoundUser {
				bound = true
			}
		}
		if op.RequestBody != nil {
			if f := op.RequestBody.Properties["userId"]; f != nil {
				if f.Bound != apicatalogue.BoundUser {
					t.Errorf("%s: the body's userId must be x-bridge-bound: user", op.ID)
				}
				bound = true
			}
		}
		if !bound && !op.Unscoped {
			t.Errorf("%s: nothing names the user the call is for, and it is not marked x-bridge-unscoped", op.ID)
		}
		if bound && op.Unscoped {
			t.Errorf("%s: marked x-bridge-unscoped, but binds a user", op.ID)
		}
	}
}

func TestOperationalEndpointsAreNotBridged(t *testing.T) {
	doc, err := apicatalogue.Parse(OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range doc.Operations {
		if op.Namespace() == "assetManager" && op.Bridge != "" {
			t.Errorf("%s: the service's own operation is not for routines", op.ID)
		}
	}
}
