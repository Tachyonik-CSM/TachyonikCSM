// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the calls this client makes against ResourceManager's API document
// (TachyonikLib apicontract), and that GetSource asks the path ResourceManager
// serves: it once asked /api/src-entries/{id}, which ResourceManager does not
// serve, so every source read as deleted and a user could delete the assets
// of a source that still existed (AM-DATA-26).

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

func TestGetSourceTellsAnExistingSourceFromAMissingOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Service-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/sources/7":
			w.Write([]byte(`{"id":7,"filename":"scan.xml","sourceType":"nmap","status":"Imported","userId":3}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")

	s, err := c.GetSource(7)
	if err != nil || s == nil || s.ID != 7 || s.Filename != "scan.xml" {
		t.Fatalf("GetSource(7) = %+v, %v; want source 7", s, err)
	}
	s, err = c.GetSource(8)
	if err != nil || s != nil {
		t.Fatalf("GetSource(8) = %+v, %v; want nil, nil", s, err)
	}
}
