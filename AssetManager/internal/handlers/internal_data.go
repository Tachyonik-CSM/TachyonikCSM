// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The internal-service endpoints, called by other services rather than by a
// browser: deleting every asset, vulnerability and detection an account owns,
// transferring them from an anonymous session to the registered account it
// became, and reporting which users this service still holds data for — the
// sweep SystemManager's purge relies on.

package handlers

import (
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"tachyonik/lib/logger"
)

// DeleteUserData deletes all assets, vulnerabilities, and detections for a user (internal service endpoint)
func (h *Handler) DeleteUserData(w http.ResponseWriter, r *http.Request) {
	// Internal-only: AuthMiddleware also accepts a user JWT, so without this
	// guard any authenticated tenant could wipe another tenant's data.
	if !IsInternalService(r) {
		respondError(w, http.StatusForbidden, "Internal service access required")
		return
	}
	vars := mux.Vars(r)
	userID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid user ID")
		return
	}

	count, err := h.db.DeleteAllUserData(userID)
	if err != nil {
		logger.Errorf("Failed to delete data for user %d: %v", userID, err)
		respondError(w, http.StatusInternalServerError, "Failed to delete user data")
		return
	}

	logger.Infof("Deleted %d records for user %d", count, userID)

	respondJSON(w, http.StatusOK, map[string]int64{"deleted": count})
}

// TransferUserData transfers all data from one user to another (internal service endpoint)
func (h *Handler) TransferUserData(w http.ResponseWriter, r *http.Request) {
	// Internal-only: AuthMiddleware also accepts a user JWT, so without this
	// guard any authenticated tenant could transfer another tenant's data.
	if !IsInternalService(r) {
		respondError(w, http.StatusForbidden, "Internal service access required")
		return
	}
	vars := mux.Vars(r)
	fromUserID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid user ID")
		return
	}

	newOwnerStr := r.URL.Query().Get("newOwner")
	if newOwnerStr == "" {
		respondError(w, http.StatusBadRequest, "newOwner query parameter is required")
		return
	}

	toUserID, err := strconv.ParseInt(newOwnerStr, 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid newOwner parameter")
		return
	}

	count, err := h.db.TransferAllUserData(fromUserID, toUserID)
	if err != nil {
		logger.Errorf("Failed to transfer data from user %d to %d: %v", fromUserID, toUserID, err)
		respondError(w, http.StatusInternalServerError, "Failed to transfer user data")
		return
	}

	logger.Infof("Transferred %d records from user %d to user %d", count, fromUserID, toUserID)

	respondJSON(w, http.StatusOK, map[string]int64{"transferred": count})
}

// ListUserIDs returns every distinct user id this service still holds data
// for. SystemManager's reconciliation sweep diffs it against the users that
// actually exist and purges the strays — data left behind because a deletion
// happened while this service was unreachable, or before the deletion paths
// cleaned up at all.
func (h *Handler) ListUserIDs(w http.ResponseWriter, r *http.Request) {
	if !IsInternalService(r) {
		respondError(w, http.StatusForbidden, "Internal service access required")
		return
	}
	ids, err := h.db.DistinctUserIDs()
	if err != nil {
		logger.Errorf("Failed to list user ids: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list user ids")
		return
	}
	respondJSON(w, http.StatusOK, map[string][]int64{"userIds": ids})
}
