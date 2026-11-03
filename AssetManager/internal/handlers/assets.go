// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The assets endpoints: listing a user's assets (with a filter), creating them,
// and deleting them by ID, by source or what a re-import left stale, or
// unlinking them from a source. Creation is where the per-user quota and the
// appliance's licensed host-asset cap are enforced, and where an import's
// over-limit rejections originate. Reading and changing one asset is
// asset_item.go.

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tachyonik/assetmanager/internal/database"
	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

// ListResponse represents the list assets response
type ListResponse struct {
	Assets []database.Asset `json:"assets"`
	Total  int              `json:"total"`
	// MaxHostAssets is the effective host-asset ceiling for the UI: the user's
	// per-user quota, tightened to the per-appliance subscription's licensed
	// asset count when one is active (appliance mode). Mirrors what CreateAsset
	// enforces, so the sidebar shows the real limit (e.g. the subscription's 25)
	// rather than the raw per-user quota.
	MaxHostAssets int `json:"maxHostAssets"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error string `json:"error"`
}

// CreateAssetRequest represents the create asset request. The asset is linked
// to the source it came from: sourceId, the ResourceManager source id, and
// sourceLabel, its filename. At least one of them is required.
type CreateAssetRequest struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	SourceID    *int64  `json:"sourceId,omitempty"`    // ResourceManager source id
	SourceLabel string  `json:"sourceLabel,omitempty"` // denormalised filename
	UserID      *int64  `json:"userId,omitempty"`
	LastSeen    *string `json:"lastSeen,omitempty"`
	// Scan coverage as this source reports it — when it was verified that the
	// asset is in a scheduled vulnerability scan, and the scan's shortest
	// interval in whole days (at least 1). Only ever set from here; nothing
	// derives them. Left out, the source's earlier values stay.
	ScanCoverageVerifiedAt   *string `json:"scanCoverageVerifiedAt,omitempty"`
	ScanCoverageIntervalDays *int    `json:"scanCoverageIntervalDays,omitempty"`
}

// ListAssets returns all assets for the authenticated user
func (h *Handler) ListAssets(w http.ResponseWriter, r *http.Request) {
	var userID int64

	// ?userId= names whose assets a SERVICE lists (ActionGenerator and the
	// like). A user never gets to name someone else: it used to be honoured
	// for any caller, so any logged-in user could list another user's assets
	// by passing their id.
	userIDStr := r.URL.Query().Get("userId")
	if userIDStr != "" && !IsInternalService(r) {
		respondError(w, http.StatusForbidden, "userId may only be given by an internal service")
		return
	}
	if userIDStr != "" {
		// Internal service request: parse userId from query parameter
		var err error
		userID, err = strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Invalid userId parameter")
			return
		}
	} else {
		// Regular user request: get userId from JWT context
		var ok bool
		userID, ok = GetUserID(r)
		if !ok {
			respondError(w, http.StatusUnauthorized, "User ID not found in context")
			return
		}
	}

	filter, err := filterParam(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid filter parameter")
		return
	}

	// Use group-aware queries for regular user requests
	visibleUserIDs := h.GetVisibleUserIDs(r)
	if userIDStr != "" {
		// Internal service request: use only the specified user
		visibleUserIDs = []int64{userID}
	}

	assets, err := h.db.GetAssetsByUsersWithFilter(visibleUserIDs, filter)
	if err != nil {
		logger.Errorf("Failed to retrieve assets: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to retrieve assets")
		return
	}

	response := ListResponse{
		Assets:        assets,
		Total:         len(assets),
		MaxHostAssets: h.effectiveHostAssetLimit(userID),
	}

	respondJSON(w, http.StatusOK, response)
}

// effectiveHostAssetLimit is the host-asset ceiling reported to the UI: the
// user's per-user quota, tightened to the per-appliance subscription's licensed
// asset count when one is active (appliance mode). It mirrors the two gates
// CreateAsset enforces, so the sidebar shows the real cap. A zero quota means
// "no per-user cap", so the license (when active) wins; if neither applies the
// result is 0 (unlimited). Best-effort: a lookup failure is ignored.
func (h *Handler) effectiveHostAssetLimit(userID int64) int {
	limit := 0
	if h.systemManagerClient != nil {
		if u, err := h.systemManagerClient.GetUser(userID); err == nil && u != nil {
			limit = u.MaxHostAssets
		}
	}
	if h.aiManagerClient != nil {
		if lic, err := h.aiManagerClient.InstanceLicense(); err == nil && lic.Active && lic.NumAssets > 0 {
			if limit == 0 || lic.NumAssets < limit {
				limit = lic.NumAssets
			}
		}
	}
	return limit
}

// CreateAsset creates a new asset for the authenticated user
func (h *Handler) CreateAsset(w http.ResponseWriter, r *http.Request) {
	logger.Infof("CreateAsset: handler called")

	var req CreateAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Infof("CreateAsset: failed to decode request: %v", err)
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	logger.Infof("CreateAsset: request name=%s type=%s sourceId=%v sourceLabel=%q", req.Name, req.Type, req.SourceID, req.SourceLabel)

	// Determine user ID based on request type
	var userID int64
	// The middleware's verdict, not the header's presence: with no key
	// configured the header is not checked at all, and a user sending one
	// would otherwise choose the owner.
	if IsInternalService(r) {
		// Internal service request - use userId from request body if provided
		if req.UserID != nil {
			userID = *req.UserID
			logger.Infof("CreateAsset: internal request with userID=%d", userID)
		} else {
			userID = 0 // Default to system user if not specified
			logger.Infof("CreateAsset: internal request with default userID=0")
		}
	} else {
		// Regular user request - get user ID from JWT context
		var ok bool
		userID, ok = GetUserID(r)
		if !ok {
			respondError(w, http.StatusUnauthorized, "User ID not found in context")
			return
		}
		logger.Infof("CreateAsset: JWT request with userID=%d", userID)
	}

	// Validate request
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "Asset name is required")
		return
	}
	if req.Type == "" {
		respondError(w, http.StatusBadRequest, "Asset type is required")
		return
	}
	// Normalise the canonical "Host" spelling so the limit check below and
	// the row that gets stored use the same casing. Without this, an
	// importer sending type:"host" would slip past the limit (the gate is
	// case-insensitive but CountHostAssetsByUser is case-sensitive on
	// 'Host'), letting unbounded host rows accumulate.
	if strings.EqualFold(req.Type, "Host") {
		req.Type = "Host"
	}

	var sourceID int64
	sourceLabel := req.SourceLabel
	if req.SourceID != nil && *req.SourceID > 0 {
		sourceID = *req.SourceID
	}
	if sourceID == 0 && sourceLabel == "" {
		respondError(w, http.StatusBadRequest, "Source is required (supply sourceId and sourceLabel)")
		return
	}
	logger.Infof("CreateAsset: validation passed (sourceID=%d, sourceLabel=%q)", sourceID, sourceLabel)

	// Parse lastSeen if provided
	var lastSeen *time.Time
	if req.LastSeen != nil && *req.LastSeen != "" {
		parsed, err := time.Parse(time.RFC3339, *req.LastSeen)
		if err != nil {
			// Try alternative format
			parsed, err = time.Parse("2006-01-02T15:04:05Z", *req.LastSeen)
			if err != nil {
				// Try without timezone
				parsed, err = time.Parse("2006-01-02T15:04:05", *req.LastSeen)
				if err != nil {
					logger.Warnf("Failed to parse lastSeen '%s': %v, using current time", *req.LastSeen, err)
				} else {
					lastSeen = &parsed
				}
			} else {
				lastSeen = &parsed
			}
		} else {
			lastSeen = &parsed
		}
	}

	// Scan coverage: a value that cannot be read is left out, with a warning,
	// rather than failing the whole asset.
	var coverageVerifiedAt *time.Time
	if req.ScanCoverageVerifiedAt != nil && *req.ScanCoverageVerifiedAt != "" {
		if parsed, ok := parseTimestamp(*req.ScanCoverageVerifiedAt); ok {
			coverageVerifiedAt = &parsed
		} else {
			logger.Warnf("CreateAsset: ignoring unreadable scanCoverageVerifiedAt %q for %q", *req.ScanCoverageVerifiedAt, req.Name)
		}
	}
	coverageInterval := req.ScanCoverageIntervalDays
	if coverageInterval != nil && *coverageInterval < 1 {
		logger.Warnf("CreateAsset: ignoring scanCoverageIntervalDays %d for %q (must be at least 1)", *coverageInterval, req.Name)
		coverageInterval = nil
	}

	// Check host asset limit for Host type assets. Type was canonicalised
	// above, so this is now an exact-match gate that pairs with the
	// case-sensitive CountHostAssetsByUser query.
	if req.Type == "Host" && userID != 0 {
		if status, msg := h.hostAssetLimit("CreateAsset", userID, req.Name, sourceLabel, sourceID); status != 0 {
			respondError(w, status, msg)
			return
		}
	}

	// Create asset
	logger.Infof("CreateAsset: creating asset in database")
	result, err := h.db.CreateAsset(req.Name, req.Type, userID, sourceID, sourceLabel, lastSeen)
	if err != nil {
		logger.Errorf("CreateAsset: failed to create asset: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to create asset")
		return
	}
	logger.Infof("CreateAsset: asset id=%d (new=%v, sourceNew=%v)", result.Asset.ID, result.AssetWasNew, result.SourceWasNew)

	if coverageVerifiedAt != nil || coverageInterval != nil {
		if err := h.db.SetAssetScanCoverage(result.Asset.ID, sourceID, sourceLabel, coverageVerifiedAt, coverageInterval); err != nil {
			logger.Errorf("CreateAsset: failed to set scan coverage: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to set asset scan coverage")
			return
		}
		if fresh, err := h.db.GetAssetForUsers(result.Asset.ID, []int64{userID}); err == nil {
			result.Asset = fresh
		} else {
			logger.Errorf("CreateAsset: failed to re-read asset %d: %v", result.Asset.ID, err)
		}
	}

	// Broadcast the right event: AssetCreated if the asset row itself is new,
	// otherwise AssetUpdated so clients refresh their Sources list.
	if result.AssetWasNew {
		// Scoped: the created-asset payload carries full asset details.
		msg, _ := ws.NewMessage(ws.TypeAssetCreated, result.Asset)
		h.hub.BroadcastScoped(msg, []int64{userID})
	} else {
		msg, _ := ws.NewMessage(ws.TypeAssetUpdated, ws.AssetUpdatedPayload{ID: result.Asset.ID})
		h.hub.BroadcastScoped(msg, []int64{userID})
	}

	// Trigger organisation score recalculation
	go h.recalculateOrganisationScore(userID)

	// Timeline stat. Throttled in the client; SystemManager coalesces too.
	if result.AssetWasNew {
		h.emitTotalAssets(userID)
	}

	status := http.StatusOK
	if result.AssetWasNew {
		status = http.StatusCreated
	}
	respondJSON(w, status, result.Asset)
}

// DeleteAssetsBySource deletes all assets associated with a source
func (h *Handler) DeleteAssetsBySource(w http.ResponseWriter, r *http.Request) {
	// The source reference, "filename (ID: N)", as ResourceManager sends it.
	sourceRef := r.URL.Query().Get("source")
	if sourceRef == "" {
		respondError(w, http.StatusBadRequest, "source parameter is required")
		return
	}
	sourceID, _ := database.ParseSourceRef(sourceRef)
	if sourceID == 0 {
		respondError(w, http.StatusBadRequest, "source parameter must include '(ID: N)' suffix")
		return
	}

	// For internal service requests, use the provided user ID or system user
	var userID int64
	if IsInternalService(r) { // the middleware's verdict, not the header's presence
		// Internal service request - delete for all users (pass 0 to indicate no user filter)
		userID = 0
	} else {
		// Regular user request - only delete assets belonging to this user
		var ok bool
		userID, ok = GetUserID(r)
		if !ok {
			respondError(w, http.StatusUnauthorized, "User ID not found in context")
			return
		}
	}

	// Check if request is from internal service
	if !IsInternalService(r) { // the middleware's verdict, not the header's presence
		// Regular user request - validate source doesn't exist in ResourceManager
		source, err := h.rscManagerClient.GetSource(sourceID)
		if err != nil {
			logger.Errorf("Failed to verify source existence: %v", err)
			respondError(w, http.StatusInternalServerError, "Failed to verify source existence")
			return
		}

		if source != nil {
			// Source exists - deny deletion regardless of ownership
			respondError(w, http.StatusForbidden,
				"Cannot delete assets while the source still exists. Please delete the source first.")
			return
		}
	}

	// Capture affected user IDs before deletion (for score recalculation)
	affectedUserIDs, _ := h.db.GetUserIDsByAssetSource(sourceID, userID)

	// Delete assets
	// Those that keep other sources are recomputed from them.
	deletedIDs, refreshedIDs, err := h.db.DeleteAssetsBySource(sourceID, userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to delete assets")
		return
	}

	// Broadcast deletion to WebSocket clients for each deleted asset
	for _, assetID := range deletedIDs {
		msg, _ := ws.NewMessage(ws.TypeAssetDeleted, ws.AssetDeletedPayload{ID: assetID})
		h.hub.Broadcast(msg)
	}
	h.announceAssetsUpdated(refreshedIDs)

	// Trigger organisation score recalculation for affected users
	if len(deletedIDs) > 0 || len(refreshedIDs) > 0 {
		for _, uid := range affectedUserIDs {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalAssets(uid)
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": len(deletedIDs),
		"message": fmt.Sprintf("Deleted %d asset(s)", len(deletedIDs)),
	})
}

// ClearAssetSourcesBySource clears the source field for all assets associated with a source
func (h *Handler) ClearAssetSourcesBySource(w http.ResponseWriter, r *http.Request) {
	// Get source from query parameter
	sourceRef := r.URL.Query().Get("source")
	if sourceRef == "" {
		respondError(w, http.StatusBadRequest, "source parameter is required")
		return
	}
	sourceID, _ := database.ParseSourceRef(sourceRef)
	if sourceID == 0 {
		respondError(w, http.StatusBadRequest, "source parameter must include '(ID: N)' suffix")
		return
	}

	// For internal service requests, use the provided user ID or system user
	var userID int64
	if IsInternalService(r) { // the middleware's verdict, not the header's presence
		// Internal service request - clear for all users (pass 0 to indicate no user filter)
		userID = 0
	} else {
		// Regular user request - only clear assets belonging to this user
		var ok bool
		userID, ok = GetUserID(r)
		if !ok {
			respondError(w, http.StatusUnauthorized, "User ID not found in context")
			return
		}
	}

	// Clear asset sources
	updatedIDs, err := h.db.ClearAssetSourceBySource(sourceID, userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to clear asset sources")
		return
	}

	// Broadcast update to WebSocket clients for each updated asset
	for _, assetID := range updatedIDs {
		msg, _ := ws.NewMessage(ws.TypeAssetUpdated, ws.AssetUpdatedPayload{ID: assetID})
		h.hub.Broadcast(msg)
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"updated": len(updatedIDs),
		"message": fmt.Sprintf("Cleared source for %d asset(s)", len(updatedIDs)),
	})
}

// DeleteStaleAssetsBySource deletes assets associated with a source that were modified before a given timestamp
// This is used during re-import to clean up records that were not updated
func (h *Handler) DeleteStaleAssetsBySource(w http.ResponseWriter, r *http.Request) {
	// This endpoint is only for internal service requests
	if !IsInternalService(r) { // the middleware's verdict, not the header's presence
		respondError(w, http.StatusForbidden, "This endpoint is only available for internal services")
		return
	}

	// Get source from query parameter
	sourceRef := r.URL.Query().Get("source")
	if sourceRef == "" {
		respondError(w, http.StatusBadRequest, "source parameter is required")
		return
	}
	sourceID, _ := database.ParseSourceRef(sourceRef)
	if sourceID == 0 {
		respondError(w, http.StatusBadRequest, "source parameter must include '(ID: N)' suffix")
		return
	}

	// Get modifiedBefore timestamp from query parameter
	modifiedBeforeStr := r.URL.Query().Get("modifiedBefore")
	if modifiedBeforeStr == "" {
		respondError(w, http.StatusBadRequest, "modifiedBefore parameter is required")
		return
	}

	modifiedBefore, err := time.Parse(time.RFC3339, modifiedBeforeStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "modifiedBefore must be in RFC3339 format")
		return
	}

	// Capture affected user IDs before deletion (for score recalculation)
	affectedUserIDs, _ := h.db.GetUserIDsByStaleAssetSource(sourceID, modifiedBefore)

	// Internal service request - delete for all users
	// Those that keep other sources are recomputed from them.
	deletedIDs, refreshedIDs, err := h.db.DeleteStaleAssetsBySource(sourceID, 0, modifiedBefore)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to delete stale assets")
		return
	}

	// Broadcast deletion to WebSocket clients for each deleted asset
	for _, assetID := range deletedIDs {
		msg, _ := ws.NewMessage(ws.TypeAssetDeleted, ws.AssetDeletedPayload{ID: assetID})
		h.hub.Broadcast(msg)
	}
	h.announceAssetsUpdated(refreshedIDs)

	// Trigger organisation score recalculation for affected users
	if len(deletedIDs) > 0 || len(refreshedIDs) > 0 {
		for _, uid := range affectedUserIDs {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalAssets(uid)
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": len(deletedIDs),
		"message": fmt.Sprintf("Deleted %d stale asset(s)", len(deletedIDs)),
	})
}

// DeleteAssetsByIDsRequest represents the request to delete assets by IDs
type DeleteAssetsByIDsRequest struct {
	IDs []int64 `json:"ids"`
}

// DeleteAssetsByIDs deletes assets by their IDs (only if source is NULL or empty)
func (h *Handler) DeleteAssetsByIDs(w http.ResponseWriter, r *http.Request) {
	logger.Infof("DeleteAssetsByIDs: handler called")

	userID, ok := GetUserID(r)
	if !ok {
		logger.Infof("DeleteAssetsByIDs: user ID not found in context")
		respondError(w, http.StatusUnauthorized, "User ID not found in context")
		return
	}
	logger.Infof("DeleteAssetsByIDs: userID=%d", userID)

	var req DeleteAssetsByIDsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Infof("DeleteAssetsByIDs: failed to decode request body: %v", err)
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	logger.Infof("DeleteAssetsByIDs: received IDs=%v", req.IDs)

	if len(req.IDs) == 0 {
		logger.Infof("DeleteAssetsByIDs: no IDs provided")
		respondError(w, http.StatusBadRequest, "No IDs provided")
		return
	}

	// Delete assets (only those with empty source)
	deleted, skipped, err := h.db.DeleteAssetsByIDs(req.IDs, userID)
	if err != nil {
		logger.Errorf("Failed to delete assets: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete assets")
		return
	}

	// Broadcast deletion to WebSocket clients
	for _, id := range deleted {
		// Scoped to the deleting user's tenant so other tenants don't
		// receive its delete events. (SECURITY: CRIT-3)
		msg, _ := ws.NewMessage(ws.TypeAssetDeleted, ws.AssetDeletedPayload{ID: id})
		h.hub.BroadcastScoped(msg, []int64{userID})
	}

	// Trigger organisation score recalculation
	if len(deleted) > 0 {
		go h.recalculateOrganisationScore(userID)
	}

	logger.Infof("Deleted %d assets, skipped %d (source not empty)", len(deleted), len(skipped))

	if len(deleted) > 0 {
		h.emitAudit(userID, "Info", fmt.Sprintf("User removed %d asset(s)", len(deleted)))
		h.emitTotalAssets(userID)
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": len(deleted),
		"skipped": len(skipped),
		"message": fmt.Sprintf("Deleted %d asset(s), skipped %d (source not empty)", len(deleted), len(skipped)),
	})
}

// respondJSON writes a JSON response
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// respondError writes an error response
func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, ErrorResponse{Error: message})
}

// filterParam reads a list's filter from ?filter=, a JSON database.Filter;
// nil when there is none.
func filterParam(r *http.Request) (*database.Filter, error) {
	raw := r.URL.Query().Get("filter")
	if raw == "" {
		return nil, nil
	}
	var f database.Filter
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// parseTimestamp reads a timestamp in the layouts clients send: RFC 3339, or
// without a zone (taken as UTC).
func parseTimestamp(v string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// hostAssetLimit decides whether userID may get one more host asset: 0 when
// it may, otherwise the status and message to refuse with. Both the user's own
// cap and the appliance's licensed count are checked, and a refusal is
// audited. op names the caller in the log.
func (h *Handler) hostAssetLimit(op string, userID int64, name, sourceLabel string, sourceID int64) (int, string) {
	user, err := h.systemManagerClient.GetUser(userID)
	if err != nil {
		logger.Errorf("%s: failed to get user %d for limit check: %v", op, userID, err)
		return http.StatusInternalServerError, "Failed to verify user limits"
	}

	if user != nil {
		// In appliance mode the instance is one shared workspace, so the
		// per-user quota is enforced instance-wide (every user's host assets
		// count against the one budget) — otherwise each user could add up to
		// the full quota. SaaS counts per user.
		var currentCount int
		if h.sharedWorkspace() {
			currentCount, err = h.db.CountHostAssetsAllUsers()
		} else {
			currentCount, err = h.db.CountHostAssetsByUser(userID)
		}
		if err != nil {
			logger.Errorf("%s: failed to count host assets for user %d: %v", op, userID, err)
			return http.StatusInternalServerError, "Failed to verify host asset count"
		}

		// A zero MaxHostAssets means "no per-user cap" (consistent with the
		// action/vulnerability/detection gates), not "block everything". This
		// matters in appliance mode, where the per-appliance license below is
		// the ceiling once a subscription is active.
		if user.MaxHostAssets > 0 && currentCount >= user.MaxHostAssets {
			logger.Warnf("%s: user %d hit host-asset limit (%d/%d), rejecting create of %q", op, userID, currentCount, user.MaxHostAssets, name)
			// Coalesced into one summary audit per (user, source) — see limitaudit.go.
			h.limitAudit.record(assetLimit, userID, currentCount, user.MaxHostAssets, sourceLabel, sourceID)
			return http.StatusForbidden, fmt.Sprintf("Host asset limit reached (%d/%d)", currentCount, user.MaxHostAssets)
		}
	} else {
		logger.Infof("%s: user %d not found, skipping limit check", op, userID)
	}

	// Per-appliance license: an instance-wide cap across ALL users. When the
	// appliance holds an active subscription, its licensed asset count caps
	// the total Host assets regardless of any single user's per-user limit.
	// Best-effort: fail open if the license can't be read, so a transient
	// AIManager blip never blocks asset creation.
	if h.aiManagerClient != nil {
		lic, lerr := h.aiManagerClient.InstanceLicense()
		if lerr != nil {
			logger.Warnf("%s: could not read appliance license, allowing create: %v", op, lerr)
		} else if lic.Active && lic.NumAssets > 0 {
			instanceCount, cerr := h.db.CountHostAssetsAllUsers()
			if cerr != nil {
				logger.Errorf("%s: failed to count instance host assets: %v", op, cerr)
				return http.StatusInternalServerError, "Failed to verify host asset count"
			}
			if instanceCount >= lic.NumAssets {
				logger.Warnf("%s: appliance hit licensed host-asset limit (%d/%d), rejecting create of %q", op, instanceCount, lic.NumAssets, name)
				h.limitAudit.record(instanceAssetLimit, userID, instanceCount, lic.NumAssets, sourceLabel, sourceID)
				return http.StatusForbidden, fmt.Sprintf("Appliance host asset limit reached (%d/%d)", instanceCount, lic.NumAssets)
			}
		}
	}
	return 0, ""
}
