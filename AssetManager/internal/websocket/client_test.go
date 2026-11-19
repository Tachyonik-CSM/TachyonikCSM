// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the read limit of a client connection: a PING, the largest thing a
// client legitimately sends, is still answered, and a message over
// maxMessageSize ends the connection instead of being read in full. Also the
// scoping of a connection, and its end with its token (AM-SEC-19, -24).

package websocket

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialTestClient(t *testing.T) *websocket.Conn {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		c := NewClient(hub, conn, 7, []int64{7}, time.Time{})
		hub.Register(c)
		go c.WritePump()
		go c.ReadPump()
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestReadLimit(t *testing.T) {
	conn := dialTestClient(t)

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"PING"}`)); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, reply, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no answer to PING: %v", err)
	}
	if !strings.Contains(string(reply), `"PONG"`) {
		t.Fatalf("answer to PING: %s", reply)
	}

	big := `{"type":"PING","payload":"` + strings.Repeat("x", maxMessageSize) + `"}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(big)); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, msg, err := conn.ReadMessage(); err == nil {
		t.Fatalf("connection still open after a %d-byte message; got %s", len(big), msg)
	} else if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		t.Fatal("connection neither answered nor closed after an oversized message")
	}
}

// AM-SEC-19: a user's connection receives what its workspace owns — its own
// records and its colleagues' — and nothing of another tenant's, nor a
// message whose owner is not known. A service receives everything.
func TestCanSeeIsScopedToTheWorkspace(t *testing.T) {
	user := NewClient(nil, nil, 7, []int64{7, 8}, time.Time{})
	service := NewInternalClient(nil, nil)
	for _, c := range []struct {
		owners        []int64
		user, service bool
	}{
		{[]int64{7}, true, true},
		{[]int64{8}, true, true},
		{[]int64{9}, false, true},
		{[]int64{9, 8}, true, true},
		{nil, false, true},
	} {
		if got := user.canSee(c.owners); got != c.user {
			t.Errorf("user connection, owners %v: %v, want %v", c.owners, got, c.user)
		}
		if got := service.canSee(c.owners); got != c.service {
			t.Errorf("service connection, owners %v: %v, want %v", c.owners, got, c.service)
		}
	}
}

// AM-SEC-24: a user's connection ends with its token, with close code 1008.
func TestAConnectionEndsWithItsToken(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		c := NewClient(hub, conn, 7, []int64{7}, time.Now().Add(200*time.Millisecond))
		hub.Register(c)
		go c.WritePump()
		go c.ReadPump()
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("connection not closed at the token's expiry: %v", err)
	}
	if ce.Code != websocket.ClosePolicyViolation || ce.Text != ReasonTokenExpired {
		t.Fatalf("closed with %d %q, want %d %q", ce.Code, ce.Text, websocket.ClosePolicyViolation, ReasonTokenExpired)
	}
}
