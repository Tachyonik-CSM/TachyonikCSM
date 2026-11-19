// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the calls this client makes against ResourceManager's API document
// (TachyonikLib apicontract), and that SourceExists asks the path
// ResourceManager serves and reads only ResourceManager's own "Source not
// found" as a missing source. The client once asked /api/src-entries/{id},
// which ResourceManager does not serve, so every source read as deleted. The
// sources' sweep removes the records of a missing source (AM-DATA-46): a
// wrong path, or a proxy's 404, must be an error, never "gone".

package rscmanager

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/lib/apicontract"
)

var calls = []apicontract.Call{
	{Method: "GET", Path: "/api/sources/{id}", Answer: Source{}},
}

func TestTheClientMatchesResourceManagersDocument(t *testing.T) {
	apicontract.Test(t, "ResourceManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}

func TestSourceExistsTellsAMissingSourceFromAWrongPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Service-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/sources/7":
			w.Write([]byte(`{"id":7,"filename":"scan.xml","sourceType":"nmap","status":"Imported","userId":3}`))
		case "/api/sources/8":
			// ResourceManager's own answer (sources.get).
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"Source not found"}`))
		default:
			// The router's, for a path it does not serve.
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")

	if ok, err := c.SourceExists(7); err != nil || !ok {
		t.Errorf("source 7: %v, %v; want true", ok, err)
	}
	if ok, err := c.SourceExists(8); err != nil || ok {
		t.Errorf("source 8: %v, %v; want false, as ResourceManager says so", ok, err)
	}
	if ok, err := c.SourceExists(9); err == nil {
		t.Errorf("a 404 that is not ResourceManager's answer: %v, nil; want an error", ok)
	}
	bad := NewClient(srv.URL, "wrong")
	if _, err := bad.SourceExists(7); err == nil {
		t.Error("a refused key: no error")
	}
}
