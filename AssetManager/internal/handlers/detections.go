// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The detections endpoints: listing what was observed on a user's assets (with
// a filter), creating them, and deleting them by ID, by source or what a
// re-import left stale, or unlinking them from a source.

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tachyonik/assetmanager/internal/database"
	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

// ListDetectionsResponse represents the response for listing detections
type ListDetectionsResponse struct {
	Detections    []database.Detection `json:"detections"`
	Total         int                  `json:"total"`
	MaxDetections int                  `json:"maxDetections"`
}

// CreateDetectionRequest represents the request to create a detection
type CreateDetectionRequest struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port string `json:"port"`
	// The source the finding came from: sourceId, the ResourceManager source
	// id, and sourceLabel, its filename. Both left out for one entered by
	// hand.
	SourceID    *int64 `json:"sourceId,omitempty"`
	SourceLabel string `json:"sourceLabel,omitempty"`
	// Source is the older "filename (ID: N)" form, read only when sourceId
	// and sourceLabel are both missing. Kept for one release, so an importer
	// updated after AssetManager still works; removed in the next.
	Source   string  `json:"source,omitempty"`
	UserID   *int64  `json:"userId,omitempty"`
	LastSeen *string `json:"lastSeen,omitempty"`
}

// ListDetections returns all detections for the authenticated user
func (h *Handler) ListDetections(w http.ResponseWriter, r *http.Request) {
	userID, ok := GetUserID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "User ID not found in context")
		return
	}

	filter, err := filterParam(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid filter parameter")
		return
	}

	visibleUserIDs := h.GetVisibleUserIDs(r)

	detections, err := h.db.GetDetectionsByUsersWithFilter(visibleUserIDs, filter)
	if err != nil {
		logger.Errorf("Failed to retrieve detections: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to retrieve detections")
		return
	}

	// Fetch user's maxDetections limit
	var maxDetections int
	if h.systemManagerClient != nil {
		smUser, smErr := h.systemManagerClient.GetUser(userID)
		if smErr != nil {
			logger.Errorf("Failed to fetch user %d from SystemManager: %v", userID, smErr)
		} else if smUser != nil {
			maxDetections = smUser.MaxDetections
		}
	}

	response := ListDetectionsResponse{
		Detections:    detections,
		Total:         len(detections),
		MaxDetections: maxDetections,
	}

	respondJSON(w, http.StatusOK, response)
}

// CreateDetection creates a new detection for the authenticated user
func (h *Handler) CreateDetection(w http.ResponseWriter, r *http.Request) {
	var req CreateDetectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Determine user ID based on request type
	var userID int64
	if IsInternalService(r) { // the middleware's verdict, not the header's presence
		// Internal service request - use userId from request body if provided
		if req.UserID != nil {
			userID = *req.UserID
		} else {
			userID = 0 // Default to system user if not specified
		}
	} else {
		// Regular user request - get user ID from JWT context
		var ok bool
		userID, ok = GetUserID(r)
		if !ok {
			respondError(w, http.StatusUnauthorized, "User ID not found in context")
			return
		}
	}

	// Validate request
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "Detection name is required")
		return
	}
	if req.Host == "" {
		respondError(w, http.StatusBadRequest, "Host is required")
		return
	}
	if req.Port == "" {
		respondError(w, http.StatusBadRequest, "Port is required")
		return
	}

	sourceID, sourceLabel := findingSource("detection", req.SourceID, req.SourceLabel, req.Source)

	// Enforce detection limit
	if h.systemManagerClient != nil {
		smUser, smErr := h.systemManagerClient.GetUser(userID)
		if smErr != nil {
			logger.Errorf("Failed to fetch user %d from SystemManager for limit check: %v", userID, smErr)
		} else if smUser != nil && smUser.MaxDetections > 0 {
			currentCount, _ := h.db.CountDetectionsByUserWithFilter(userID, nil)
			if currentCount >= smUser.MaxDetections {
				// Coalesced into one summary audit per (user, source) — see limitaudit.go.
				h.limitAudit.record(detectionLimit, userID, currentCount, smUser.MaxDetections, sourceLabel, sourceID)
				http.Error(w, fmt.Sprintf("Detection limit reached (%d/%d)", currentCount, smUser.MaxDetections), http.StatusForbidden)
				return
			}
		}
	}

	// Parse lastSeen if provided
	var lastSeen *time.Time
	if req.LastSeen != nil && *req.LastSeen != "" {
		parsed, err := time.Parse(time.RFC3339, *req.LastSeen)
		if err != nil {
			// Try alternative format (OpenVAS format: 2024-01-15T10:30:00Z or similar)
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

	// Create detection
	detection, err := h.db.CreateDetection(req.Name, req.Host, req.Port, sourceID, sourceLabel, userID, lastSeen)
	if err != nil {
		logger.Errorf("Failed to create detection: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to create detection")
		return
	}

	// The host's assets follow from their findings.
	h.refreshHosts([]database.HostRef{{Host: req.Host, UserID: userID}})

	// Broadcast to WebSocket clients. Scoped: this payload carries full
	// detection details, so deliver only to clients who may see this owner's
	// data (tenant isolation).
	msg, _ := ws.NewMessage(ws.TypeDetectionCreated, detection)
	h.hub.BroadcastScoped(msg, []int64{userID})

	h.emitTotalDetections(userID)

	respondJSON(w, http.StatusCreated, detection)
}

// DeleteDetectionsBySource deletes all detections associated with a source
func (h *Handler) DeleteDetectionsBySource(w http.ResponseWriter, r *http.Request) {
	source, sourceID, refOK := sourceIDParam(w, r)
	if !refOK {
		return
	}

	// Determine user ID based on request type
	var userID int64
	if IsInternalService(r) { // the middleware's verdict, not the header's presence
		// Internal service request - delete for all users (userID=0)
		userID = 0
	} else {
		// Regular user request - get user ID from JWT context
		var ok bool
		userID, ok = GetUserID(r)
		if !ok {
			respondError(w, http.StatusUnauthorized, "User ID not found in context")
			return
		}
	}

	// The hosts concerned, with their owners: on the internal path userID is
	// 0, and refreshing "for user 0" would match no asset.
	affectedHosts, _ := h.db.DetectionHostRefsBySource(sourceID, userID)

	// Delete detections
	deletedIDs, err := h.db.DeleteDetectionsBySource(sourceID, userID)
	if err != nil {
		logger.Errorf("Failed to delete detections: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete detections")
		return
	}

	h.refreshHosts(affectedHosts)

	// Announce each deletion to the owner's workspace and the services
	h.announce(ws.TypeDetectionDeleted, deletedIDs)

	logger.Infof("Deleted %d detections for source: %s", len(deletedIDs), source)

	// A host left without findings becomes unmanaged, which moves the
	// organisation score; and each owner's count changed.
	if len(deletedIDs) > 0 {
		for _, uid := range usersOf(affectedHosts) {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalDetections(uid)
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": len(deletedIDs),
		"ids":     database.IDs(deletedIDs),
	})
}

// ClearDetectionSourcesBySource clears the source field for all detections associated with a source
func (h *Handler) ClearDetectionSourcesBySource(w http.ResponseWriter, r *http.Request) {
	source, sourceID, refOK := sourceIDParam(w, r)
	if !refOK {
		return
	}

	// Determine user ID based on request type
	var userID int64
	if IsInternalService(r) { // the middleware's verdict, not the header's presence
		// Internal service request - clear for all users (userID=0)
		userID = 0
	} else {
		// Regular user request - get user ID from JWT context
		var ok bool
		userID, ok = GetUserID(r)
		if !ok {
			respondError(w, http.StatusUnauthorized, "User ID not found in context")
			return
		}
	}

	// Clear detection sources
	updatedIDs, err := h.db.ClearDetectionSourceBySource(sourceID, userID)
	if err != nil {
		logger.Errorf("Failed to clear detection sources: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to clear detection sources")
		return
	}

	// Announce each change to the owner's workspace and the services
	h.announce(ws.TypeDetectionUpdated, updatedIDs)

	logger.Infof("Cleared source for %d detections for source: %s", len(updatedIDs), source)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"updated": len(updatedIDs),
		"ids":     database.IDs(updatedIDs),
	})
}

// DeleteStaleDetectionsBySource deletes detections associated with a source that were modified before a given timestamp
// This is used during re-import to clean up records that were not updated
func (h *Handler) DeleteStaleDetectionsBySource(w http.ResponseWriter, r *http.Request) {
	// This endpoint is only for internal service requests
	if !IsInternalService(r) { // the middleware's verdict, not the header's presence
		respondError(w, http.StatusForbidden, "This endpoint is only available for internal services")
		return
	}

	// Get source from query parameter
	source, sourceID, refOK := sourceIDParam(w, r)
	if !refOK {
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

	affectedHosts, _ := h.db.StaleDetectionHostRefsBySource(sourceID, modifiedBefore)

	// Internal service request - delete for all users
	deletedIDs, err := h.db.DeleteStaleDetectionsBySource(sourceID, 0, modifiedBefore)
	if err != nil {
		logger.Errorf("Failed to delete stale detections: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete stale detections")
		return
	}

	// Announce each deletion to the owner's workspace and the services
	h.announce(ws.TypeDetectionDeleted, deletedIDs)

	if len(deletedIDs) > 0 {
		h.refreshHosts(affectedHosts)
		for _, uid := range usersOf(affectedHosts) {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalDetections(uid)
		}
	}

	logger.Infof("Deleted %d stale detections for source: %s", len(deletedIDs), source)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": len(deletedIDs),
		"message": fmt.Sprintf("Deleted %d stale detection(s)", len(deletedIDs)),
	})
}

// DeleteDetectionsByIDsRequest represents the request to delete detections by IDs
type DeleteDetectionsByIDsRequest struct {
	IDs []int64 `json:"ids"`
}

// DeleteDetectionsByIDs deletes detections by their IDs (only if source is NULL or empty)
func (h *Handler) DeleteDetectionsByIDs(w http.ResponseWriter, r *http.Request) {
	userID, ok := GetUserID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "User ID not found in context")
		return
	}

	var req DeleteDetectionsByIDsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if len(req.IDs) == 0 {
		respondError(w, http.StatusBadRequest, "No IDs provided")
		return
	}

	// Get affected hosts before deletion
	affectedHosts, _ := h.db.GetDetectionHostsByIDs(req.IDs, userID)

	// Delete detections (only those with empty source)
	deleted, skipped, err := h.db.DeleteDetectionsByIDs(req.IDs, userID)
	if err != nil {
		logger.Errorf("Failed to delete detections: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete detections")
		return
	}

	h.refreshHosts(database.HostRefsOf(affectedHosts, userID))

	// Broadcast deletion to WebSocket clients
	for _, id := range deleted {
		// Scoped to the deleting user's tenant so other tenants don't
		// receive its delete events. (SECURITY: CRIT-3)
		msg, _ := ws.NewMessage(ws.TypeDetectionDeleted, map[string]int64{"id": id})
		h.hub.BroadcastScoped(msg, []int64{userID})
	}

	logger.Infof("Deleted %d detections, skipped %d (source not empty)", len(deleted), len(skipped))

	if len(deleted) > 0 {
		h.emitAudit(userID, "Info", fmt.Sprintf("User removed %d detection(s)", len(deleted)))
		h.emitTotalDetections(userID)
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"deleted":    len(deleted),
		"skipped":    len(skipped),
		"deletedIds": deleted,
		"skippedIds": skipped,
	})
}
