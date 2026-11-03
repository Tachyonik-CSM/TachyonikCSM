// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Recomputing assets after their evidence changed, and telling the WebUI.
//
// Every handler that adds or removes a vulnerability, a detection or a
// source's link to an asset ends here, so what an asset shows — severity,
// score, last seen, last scanned — always follows from what still refers to
// it (see database/asset_refresh.go for the rule). The assets named are always
// their owners', also on the internal service path where the request carries
// no user: refreshing "for user 0" used to recompute nothing.

package handlers

import (
	"tachyonik/assetmanager/internal/database"
	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

// refreshHosts recomputes the assets the refs name and announces those that
// changed.
func (h *Handler) refreshHosts(refs []database.HostRef) {
	changed, err := h.db.RefreshAssetsForHosts(refs)
	if err != nil {
		logger.Errorf("Failed to refresh assets of %d host(s): %v", len(refs), err)
	}
	h.announceAssetsUpdated(changed)
}

// announceAssetsUpdated tells the WebUI that these assets changed, so an open
// list shows the new values without a reload.
func (h *Handler) announceAssetsUpdated(ids []int64) {
	for _, id := range ids {
		msg, _ := ws.NewMessage(ws.TypeAssetUpdated, ws.AssetUpdatedPayload{ID: id})
		h.hub.Broadcast(msg)
	}
}

// usersOf is the distinct users the refs belong to.
func usersOf(refs []database.HostRef) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, r := range refs {
		if !seen[r.UserID] {
			seen[r.UserID] = true
			out = append(out, r.UserID)
		}
	}
	return out
}
