// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The aggregate statistics endpoint: a user's assets counted by severity band.
// An internal-service caller may ask on another user's behalf, which is how the
// daemons build a rule context.

package handlers

import (
	"net/http"
	"strconv"
)

// GetAssetStats returns aggregated asset statistics for the current user,
// or for the user specified via ?userId= when called with an internal service key.
func (h *Handler) GetAssetStats(w http.ResponseWriter, r *http.Request) {
	// An explicit ?userId= (internal service call) scopes to that single user.
	// A user request scopes to the caller's visible set — which in Appliance
	// mode is every instance user, so the dashboard matches the shared asset
	// list; in SaaS it is the caller (or their group), matching the list there.
	var userIDs []int64
	if userIDStr := r.URL.Query().Get("userId"); userIDStr != "" {
		// A service's question about one user; never a user's about another.
		if !IsInternalService(r) {
			respondError(w, http.StatusForbidden, "userId may only be given by an internal service")
			return
		}
		id, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Invalid userId parameter")
			return
		}
		userIDs = []int64{id}
	} else {
		if _, ok := GetUserID(r); !ok {
			respondError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		userIDs = h.GetVisibleUserIDs(r)
	}

	stats, err := h.db.GetAssetStatsByUsers(userIDs)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to get asset stats")
		return
	}

	respondJSON(w, http.StatusOK, stats)
}
