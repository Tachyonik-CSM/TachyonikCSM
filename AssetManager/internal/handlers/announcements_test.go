// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests whom a change is announced to over the WebSocket (AM-SEC-19): the
// connections of the owner's workspace — the owner and the members who are
// connected — and the services, never another tenant's. Deleting by source
// and deleting as stale act for every owner at once, and used to announce
// each record to every connection.

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	ws "tachyonik/assetmanager/internal/websocket"
)

// listener is one connection to the hub, scoped as a user's or a service's.
type listener struct {
	t    *testing.T
	conn *websocket.Conn
}

// listen connects to h's hub as userID seeing visible, or as a service when
// visible is nil, and returns once the hub has registered the connection.
func listen(t *testing.T, h *Handler, userID int64, visible []int64) *listener {
	t.Helper()
	registered := make(chan struct{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		c := ws.NewInternalClient(h.hub, conn)
		if visible != nil {
			c = ws.NewClient(h.hub, conn, userID, visible)
		}
		h.hub.Register(c)
		go c.WritePump()
		go c.ReadPump()
		close(registered)
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	<-registered
	return &listener{t: t, conn: conn}
}

// received is every announcement that arrives until the connection has been
// quiet for a moment, as "TYPE id", sorted.
func (l *listener) received() []string {
	l.t.Helper()
	var got []string
	for {
		l.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, data, err := l.conn.ReadMessage()
		if err != nil {
			break
		}
		var m struct {
			Type    string `json:"type"`
			Payload struct {
				ID int64 `json:"id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &m); err != nil {
			l.t.Fatalf("unreadable message %s: %v", data, err)
		}
		got = append(got, m.Type+" "+itoa(m.Payload.ID))
	}
	sort.Strings(got)
	return got
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// AM-SEC-19: deleting a source's records for every owner announces each
// record to its owner's workspace only, and all of them to the services.
func TestDeletionsBySourceReachOnlyTheOwnersWorkspace(t *testing.T) {
	h := scopedHandler(t) // has user 7's asset 10.0.0.7, without a source
	ownAsset, err := h.db.CreateAsset("10.0.0.1", "Host", 7, 1, "scan.xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	strangerAsset, err := h.db.CreateAsset("10.9.9.9", "Host", 9, 1, "scan.xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	ownVuln, err := h.db.CreateVulnerability("ssh weak", "10.0.0.1", "22", 50, 1, "scan.xml", 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	strangerVuln, err := h.db.CreateVulnerability("ssh weak", "10.9.9.9", "22", 50, 1, "scan.xml", 9, nil)
	if err != nil {
		t.Fatal(err)
	}

	owner := listen(t, h, 7, []int64{7, 8})
	member := listen(t, h, 8, []int64{7, 8})
	stranger := listen(t, h, 9, []int64{9})
	service := listen(t, h, 0, nil)

	source := "?source=" + url.QueryEscape("scan.xml (ID: 1)")
	w := httptest.NewRecorder()
	h.DeleteVulnerabilitiesBySource(w, callAs("DELETE", "/api/vulnerabilities/by-source"+source, "", 0, true))
	if w.Code != http.StatusOK {
		t.Fatalf("deleting the vulnerabilities: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.DeleteAssetsBySource(w, callAs("DELETE", "/api/assets/by-source"+source, "", 0, true))
	if w.Code != http.StatusOK {
		t.Fatalf("deleting the assets: %d %s", w.Code, w.Body)
	}

	ownAssetID, strangerAssetID := ownAsset.Asset.ID, strangerAsset.Asset.ID
	workspace := []string{
		"ASSET_DELETED " + itoa(ownAssetID),
		"VULNERABILITY_DELETED " + itoa(ownVuln.ID),
	}
	// Losing its vulnerability recomputes the asset before it is deleted.
	if got := owner.received(); !same(withoutUpdates(got), workspace) || hasOther(got, ownAssetID) {
		t.Errorf("owner received %v, want %v", got, workspace)
	}
	if got := member.received(); !same(withoutUpdates(got), workspace) || hasOther(got, ownAssetID) {
		t.Errorf("workspace member received %v, want %v", got, workspace)
	}
	strangers := []string{
		"ASSET_DELETED " + itoa(strangerAssetID),
		"VULNERABILITY_DELETED " + itoa(strangerVuln.ID),
	}
	if got := stranger.received(); !same(withoutUpdates(got), strangers) || hasOther(got, strangerAssetID) {
		t.Errorf("the other tenant received %v, want only its own %v", got, strangers)
	}
	all := append(append([]string{}, workspace...), strangers...)
	sort.Strings(all)
	if got := withoutUpdates(service.received()); !same(got, all) {
		t.Errorf("service received %v, want every deletion %v", got, all)
	}
}

// withoutUpdates leaves out the ASSET_UPDATED messages, whose number depends
// on what the recomputation changed.
func withoutUpdates(msgs []string) []string {
	out := []string{}
	for _, m := range msgs {
		if !strings.HasPrefix(m, "ASSET_UPDATED ") {
			out = append(out, m)
		}
	}
	return out
}

// hasOther reports an ASSET_UPDATED about any asset but the one expected.
func hasOther(msgs []string, assetID int64) bool {
	for _, m := range msgs {
		if strings.HasPrefix(m, "ASSET_UPDATED ") && m != "ASSET_UPDATED "+itoa(assetID) {
			return true
		}
	}
	return false
}
