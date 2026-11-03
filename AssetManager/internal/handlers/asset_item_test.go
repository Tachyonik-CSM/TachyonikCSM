// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests GET and PATCH /api/assets/{id}: a user reads and retypes their own
// asset (an unknown type refused), another user's id is "not found", and fields that are computed or
// that link the asset's findings cannot be set.

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"tachyonik/assetmanager/internal/config"
	"tachyonik/assetmanager/internal/database"
	ws "tachyonik/assetmanager/internal/websocket"
)

func assetFixture(t *testing.T) (http.Handler, int64) {
	t.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Stored directly, as an asset of a type from before the types were a
	// fixed set: retyping it to Host is how such a row is put right.
	res, err := db.Exec(`INSERT INTO assets (user_id, name, type) VALUES (52, '192.168.178.10', 'Device')`)
	if err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	assetID, _ := res.LastInsertId()
	hub := ws.NewHub()
	go hub.Run()
	h := &Handler{db: db, hub: hub, config: &config.Config{}}
	r := mux.NewRouter()
	r.HandleFunc("/api/assets/{id:[0-9]+}", h.GetAsset).Methods("GET")
	r.HandleFunc("/api/assets/{id:[0-9]+}", h.UpdateAsset).Methods("PATCH")
	return r, assetID
}

func asUser(t *testing.T, r http.Handler, method, path string, userID int64, body string) (int, database.Asset, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userIDKey, userID))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var a database.Asset
	_ = json.Unmarshal(w.Body.Bytes(), &a)
	return w.Code, a, w.Body.String()
}

func TestAUserReadsAndRetypesTheirAsset(t *testing.T) {
	r, id := assetFixture(t)
	path := "/api/assets/" + strconv.FormatInt(id, 10)

	if code, a, _ := asUser(t, r, "GET", path, 52, ""); code != http.StatusOK || a.Name != "192.168.178.10" {
		t.Fatalf("GET: %d %+v", code, a)
	}
	if code, _, msg := asUser(t, r, "PATCH", path, 52, `{"type":"Domain"}`); code != http.StatusBadRequest {
		t.Errorf("PATCH to an unknown type: %d %s, want 400", code, msg)
	}
	code, a, _ := asUser(t, r, "PATCH", path, 52, `{"type":"host"}`)
	if code != http.StatusOK || a.Type != "Host" {
		t.Errorf("PATCH: %d %+v, want the type stored as \"Host\"", code, a)
	}
}

func TestAnotherUsersAssetIsNotFound(t *testing.T) {
	r, id := assetFixture(t)
	path := "/api/assets/" + strconv.FormatInt(id, 10)
	if code, _, _ := asUser(t, r, "GET", path, 7, ""); code != http.StatusNotFound {
		t.Errorf("GET by another user: %d, want 404", code)
	}
	if code, _, _ := asUser(t, r, "PATCH", path, 7, `{"type":"Host"}`); code != http.StatusNotFound {
		t.Errorf("PATCH by another user: %d, want 404", code)
	}
	if code, a, _ := asUser(t, r, "GET", path, 52, ""); code != http.StatusOK || a.Type != "Device" {
		t.Errorf("the asset changed: %+v", a)
	}
}

func TestComputedAndLinkingFieldsCannotBeSet(t *testing.T) {
	r, id := assetFixture(t)
	path := "/api/assets/" + strconv.FormatInt(id, 10)
	for _, body := range []string{`{"severity":90}`, `{"score":10}`, `{"name":"other"}`, `{"type":"Host","severity":90}`, `{}`} {
		if code, _, msg := asUser(t, r, "PATCH", path, 52, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", body, code, msg)
		}
	}
}
