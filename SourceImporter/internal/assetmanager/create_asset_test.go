// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins that an asset AssetManager already knows counts as imported.
//
// AssetManager answers a create with 201 for a new asset and 200 for one that
// existed and has now been linked to the source as well. Accepting 201 alone
// failed every import whose hosts were already on record — all 21 of them, in
// the case that found it.

package assetmanager

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAssetAcceptsNewAndExisting(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"id":7,"name":"192.168.178.1","type":"Host"}`))
		}))
		c := NewClient(srv.URL, "")
		if _, err := c.CreateAsset("192.168.178.1", "Host", 86, "f.xml", 52, ""); err != nil {
			t.Errorf("status %d refused: %v", status, err)
		}
		srv.Close()
	}
}

func TestCreateAssetRefusesAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Host asset limit reached (10/10)"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, "").CreateAsset("192.168.178.1", "Host", 86, "f.xml", 52, ""); err == nil {
		t.Error("a refused create was taken as success")
	}
}
