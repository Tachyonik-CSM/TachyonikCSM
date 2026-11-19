// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The re-check of open WebSocket connections (AM-SEC-24). A connection is
// authenticated when it connects; afterwards every minute each connected
// user's account is checked as a request's is (AM-SEC-03), and their
// workspace's members are looked up again. A deactivated or deleted account
// has its connections closed with code 1008; the others take the new visible
// set, so a user who left a workspace stops receiving its announcements and
// one who joined starts. When SystemManager cannot be asked, the connections
// stay open but receive only the user's own records until the next round.
// A connection's token expiry is enforced by the connection itself
// (internal/websocket).

package handlers

import (
	"context"
	"errors"
	"time"

	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

// connectionCheckEvery is how often the open user connections are re-checked.
const connectionCheckEvery = time.Minute

// StartConnectionChecks re-checks the open user connections every minute
// until ctx ends.
func (h *Handler) StartConnectionChecks(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(connectionCheckEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.RecheckConnections(h.VisibleUserIDsForUser)
			}
		}
	}()
}

// RecheckConnections checks every connected user once, however many
// connections they have, and closes or rescopes their connections. members
// gives a user's visible set, and the user alone when SystemManager cannot be
// asked. It returns how many connections it closed.
func (h *Handler) RecheckConnections(members func(userID int64) []int64) int {
	byUser := map[int64][]*ws.Client{}
	for _, c := range h.hub.UserClients() {
		byUser[c.UserID()] = append(byUser[c.UserID()], c)
	}
	closed := 0
	for userID, clients := range byUser {
		var visible []int64
		err := error(nil)
		if h.accounts != nil {
			err = h.accounts.check(userID)
		}
		switch {
		case errors.Is(err, errAccountInactive):
			logger.Warnf("WebSocket: closing %d connection(s) of user %d: the account is deactivated or deleted", len(clients), userID)
			for _, c := range clients {
				c.CloseFor(ws.ReasonAccountDeactivated)
			}
			closed += len(clients)
			continue
		case err != nil:
			// Not verified: keep the connection, but only with the user's own
			// records until SystemManager answers again.
			visible = []int64{userID}
		default:
			visible = members(userID)
		}
		for _, c := range clients {
			c.SetVisible(visible)
		}
	}
	return closed
}
