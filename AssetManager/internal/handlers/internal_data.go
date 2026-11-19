// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The internal-service endpoints, called by other services rather than by a
// browser: deleting every asset, vulnerability and detection an account owns,
// transferring them from an anonymous session to the registered account it
// became, reporting which users this service still holds data for — the
// sweep SystemManager's purge relies on — and what each holds against their
// limits, for Admin → Users (AM-DATA-45).

package handlers

import (
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"tachyonik/assetmanager/internal/database"
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

	// All or nothing, a record the receiver has already merged into theirs
	// (AM-DATA-38). A failure leaves every record with its owner.
	result, err := h.db.TransferAllUserData(fromUserID, toUserID)
	if err != nil {
		logger.Errorf("Failed to transfer data from user %d to %d: %v", fromUserID, toUserID, err)
		respondError(w, http.StatusInternalServerError, "Failed to transfer user data")
		return
	}
	if err := h.db.RecomputeAfterTransfer(result); err != nil {
		logger.Errorf("Failed to recompute the merged assets of user %d: %v", toUserID, err)
	}

	// Both owners' counts and scores changed.
	for _, uid := range []int64{fromUserID, toUserID} {
		go h.recalculateOrganisationScore(uid)
		h.emitTotalAssets(uid)
		h.emitTotalVulnerabilities(uid)
		h.emitTotalDetections(uid)
	}

	logger.Infof("Transferred %d records from user %d to user %d, %d merged into records user %d already had", result.Moved, fromUserID, toUserID, result.Merged, toUserID)

	respondJSON(w, http.StatusOK, map[string]int64{"transferred": result.Moved + result.Merged})
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
	if ids == nil {
		ids = []int64{} // the document's array, not null, when no one has records
	}
	respondJSON(w, http.StatusOK, map[string][]int64{"userIds": ids})
}

// UsageCounts are the instance's counts in a UsageResponse.
type UsageCounts struct {
	HostAssets      int `json:"hostAssets"`
	Vulnerabilities int `json:"vulnerabilities"`
	Detections      int `json:"detections"`
}

// UsageLicence is the appliance licence the host assets are also checked
// against (AM-DATA-34).
type UsageLicence struct {
	NumAssets int `json:"numAssets"`
}

// UsageResponse is the answer of GET /api/internal/users/usage.
type UsageResponse struct {
	Users    []database.Usage `json:"users"`
	Instance UsageCounts      `json:"instance"`
	// SharedWorkspace is true on an appliance, where the host assets of all
	// users count against each user's host asset limit (AM-DATA-33).
	SharedWorkspace bool `json:"sharedWorkspace"`
	// Licence is present while the appliance holds an active licence with a
	// number of assets.
	Licence *UsageLicence `json:"licence,omitempty"`
}

// UserUsage returns what each user and the instance hold, counted as the
// limits count it (AM-DATA-45; internal service endpoint). SystemManager puts
// it next to the users' limits in Admin → Users.
func (h *Handler) UserUsage(w http.ResponseWriter, r *http.Request) {
	if !IsInternalService(r) {
		respondError(w, http.StatusForbidden, "Internal service access required")
		return
	}
	users, total, err := h.db.UsageByUser()
	if err != nil {
		logger.Errorf("Failed to count the usage per user: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to count the usage")
		return
	}
	resp := UsageResponse{
		Users:           users,
		Instance:        UsageCounts{HostAssets: total.HostAssets, Vulnerabilities: total.Vulnerabilities, Detections: total.Detections},
		SharedWorkspace: h.sharedWorkspace(),
	}
	if h.aiManagerClient != nil {
		// As the limit check does: a licence that cannot be read is no cap.
		if lic, lerr := h.aiManagerClient.InstanceLicense(); lerr != nil {
			logger.Warnf("Usage: could not read the appliance licence: %v", lerr)
		} else if lic.Active && lic.NumAssets > 0 {
			resp.Licence = &UsageLicence{NumAssets: lic.NumAssets}
		}
	}
	respondJSON(w, http.StatusOK, resp)
}
