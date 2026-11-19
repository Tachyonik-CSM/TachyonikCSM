// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests GET /api/assets/{id}: a user reads their own asset and another user's
// id is "not found"; and nothing of an asset can be changed — there is no
// PATCH (AM-DATA-06): its name and type are set when an import creates it.

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
	// fixed set: it keeps it (AM-DATA-04).
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

func TestAnotherUsersAssetIsNotFound(t *testing.T) {
	r, id := assetFixture(t)
	path := "/api/assets/" + strconv.FormatInt(id, 10)
	if code, _, _ := asUser(t, r, "GET", path, 7, ""); code != http.StatusNotFound {
		t.Errorf("GET by another user: %d, want 404", code)
	}
	if code, a, _ := asUser(t, r, "GET", path, 52, ""); code != http.StatusOK || a.Type != "Device" {
		t.Errorf("the asset changed: %+v", a)
	}
}

func TestAUserReadsTheirAsset(t *testing.T) {
	r, id := assetFixture(t)
	path := "/api/assets/" + strconv.FormatInt(id, 10)
	if code, a, _ := asUser(t, r, "GET", path, 52, ""); code != http.StatusOK || a.Name != "192.168.178.10" || a.Type != "Device" {
		t.Fatalf("GET: %d %+v", code, a)
	}
}

// AM-DATA-06: the served routes have no way to change an asset.
func TestAnAssetCannotBeChanged(t *testing.T) {
	h := scopedHandler(t)
	srv := httptest.NewServer(h.SetupRoutes())
	t.Cleanup(srv.Close)
	for _, method := range []string{"PATCH", "PUT"} {
		req, _ := http.NewRequest(method, srv.URL+"/api/assets/1", strings.NewReader(`{"type":"Host"}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s /api/assets/1: %d, want it not served (404 or 405)", method, resp.StatusCode)
		}
	}
}
