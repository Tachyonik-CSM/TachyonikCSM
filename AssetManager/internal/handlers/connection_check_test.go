// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the re-check of open WebSocket connections (AM-SEC-24): a deactivated
// account's connections are closed, a workspace change rescopes a connection
// in place, and SystemManager being down keeps the connection open with only
// the user's own records.

package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"tachyonik/assetmanager/internal/systemmanager"
	ws "tachyonik/assetmanager/internal/websocket"
)

// waitForUserClients waits until the hub has registered n user connections.
func waitForUserClients(t *testing.T, h *Handler, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(h.hub.UserClients()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("hub has %d user connections, want %d", len(h.hub.UserClients()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// announce sends an asset announcement owned by owner.
func announce(t *testing.T, h *Handler, id, owner int64) {
	t.Helper()
	msg, err := ws.NewMessage(ws.TypeAssetCreated, map[string]int64{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	h.hub.BroadcastScoped(msg, []int64{owner})
}

func membersOf(m map[int64][]int64) func(int64) []int64 {
	return func(id int64) []int64 { return m[id] }
}

func TestADeactivatedAccountsConnectionsAreClosed(t *testing.T) {
	h := scopedHandler(t)
	h.accounts = newAccountChecker(&fakeUsers{users: map[int64]*systemmanager.User{
		7: {ID: 7, Active: boolPtr(false)},
		9: {ID: 9, Active: boolPtr(true)},
	}})
	gone1 := listen(t, h, 7, []int64{7})
	gone2 := listen(t, h, 7, []int64{7})
	stays := listen(t, h, 9, []int64{9})
	waitForUserClients(t, h, 3)

	if closed := h.RecheckConnections(membersOf(map[int64][]int64{9: {9}})); closed != 2 {
		t.Fatalf("closed %d connections, want 2", closed)
	}
	for _, l := range []*listener{gone1, gone2} {
		l.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err := l.conn.ReadMessage()
		var ce *websocket.CloseError
		if !errors.As(err, &ce) || ce.Code != websocket.ClosePolicyViolation || ce.Text != ws.ReasonAccountDeactivated {
			t.Fatalf("deactivated account's connection: %v, want close 1008 %q", err, ws.ReasonAccountDeactivated)
		}
	}
	announce(t, h, 1, 9)
	if got := stays.received(); !same(got, []string{"ASSET_CREATED 1"}) {
		t.Fatalf("active user's connection received %v", got)
	}
}

func TestAWorkspaceChangeRescopesAnOpenConnection(t *testing.T) {
	h := scopedHandler(t)
	h.accounts = newAccountChecker(&fakeUsers{users: map[int64]*systemmanager.User{
		7: {ID: 7, Active: boolPtr(true)},
	}})
	l := listen(t, h, 7, []int64{7, 8}) // in a workspace with 8
	waitForUserClients(t, h, 1)

	// 7 leaves the workspace with 8: 8's records no longer reach it.
	if closed := h.RecheckConnections(membersOf(map[int64][]int64{7: {7}})); closed != 0 {
		t.Fatalf("closed %d connections, want 0", closed)
	}
	announce(t, h, 1, 8)
	announce(t, h, 2, 7)

	// 7 joins a workspace with 9: 9's records reach it, 8's still not. (A
	// connection is read once: a read timeout ends it.)
	h.RecheckConnections(membersOf(map[int64][]int64{7: {7, 9}}))
	announce(t, h, 3, 9)
	announce(t, h, 4, 8)
	if got := l.received(); !same(got, []string{"ASSET_CREATED 2", "ASSET_CREATED 3"}) {
		t.Fatalf("got %v, want the own record and the new colleague's", got)
	}
}

func TestAnUnverifiedAccountKeepsOnlyItsOwnRecords(t *testing.T) {
	h := scopedHandler(t)
	h.accounts = newAccountChecker(&fakeUsers{err: errors.New("SystemManager down")})
	l := listen(t, h, 7, []int64{7, 8})
	waitForUserClients(t, h, 1)

	asked := false
	closed := h.RecheckConnections(func(int64) []int64 { asked = true; return []int64{7, 8} })
	if closed != 0 {
		t.Fatalf("closed %d connections while SystemManager is down, want 0", closed)
	}
	if asked {
		t.Fatal("asked for the members of an account that could not be verified")
	}
	announce(t, h, 1, 8)
	announce(t, h, 2, 7)
	if got := l.received(); !same(got, []string{"ASSET_CREATED 2"}) {
		t.Fatalf("while SystemManager is down: %v, want only the own record", got)
	}
}
