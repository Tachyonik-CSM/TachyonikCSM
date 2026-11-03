// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The vulnerabilities endpoints: listing those found on a user's assets (with a
// filter), creating them, and deleting them by ID, by source or what a
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

// ListVulnerabilitiesResponse represents the response for listing vulnerabilities
type ListVulnerabilitiesResponse struct {
	Vulnerabilities    []database.Vulnerability `json:"vulnerabilities"`
	Total              int                      `json:"total"`
	MaxVulnerabilities int                      `json:"maxVulnerabilities"`
}

// CreateVulnerabilityRequest represents the request to create a vulnerability
type CreateVulnerabilityRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Severity int    `json:"severity"`
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

// ListVulnerabilities returns all vulnerabilities for the authenticated user
func (h *Handler) ListVulnerabilities(w http.ResponseWriter, r *http.Request) {
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

	vulnerabilities, err := h.db.GetVulnerabilitiesByUsersWithFilter(visibleUserIDs, filter)
	if err != nil {
		logger.Errorf("Failed to retrieve vulnerabilities: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to retrieve vulnerabilities")
		return
	}

	// Fetch user's maxVulnerabilities limit
	var maxVulnerabilities int
	if h.systemManagerClient != nil {
		smUser, smErr := h.systemManagerClient.GetUser(userID)
		if smErr != nil {
			logger.Errorf("Failed to fetch user %d from SystemManager: %v", userID, smErr)
		} else if smUser != nil {
			maxVulnerabilities = smUser.MaxVulnerabilities
		}
	}

	response := ListVulnerabilitiesResponse{
		Vulnerabilities:    vulnerabilities,
		Total:              len(vulnerabilities),
		MaxVulnerabilities: maxVulnerabilities,
	}

	respondJSON(w, http.StatusOK, response)
}

// CreateVulnerability creates a new vulnerability for the authenticated user
func (h *Handler) CreateVulnerability(w http.ResponseWriter, r *http.Request) {
	var req CreateVulnerabilityRequest
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
		respondError(w, http.StatusBadRequest, "Vulnerability name is required")
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
	if req.Severity < 0 || req.Severity > 100 {
		respondError(w, http.StatusBadRequest, "Severity must be between 0 and 100")
		return
	}

	sourceID, sourceLabel := findingSource("vulnerability", req.SourceID, req.SourceLabel, req.Source)

	// Enforce vulnerability limit
	if h.systemManagerClient != nil {
		smUser, smErr := h.systemManagerClient.GetUser(userID)
		if smErr != nil {
			logger.Errorf("Failed to fetch user %d from SystemManager for limit check: %v", userID, smErr)
		} else if smUser != nil && smUser.MaxVulnerabilities > 0 {
			currentCount, _ := h.db.CountVulnerabilitiesByUserWithFilter(userID, nil)
			if currentCount >= smUser.MaxVulnerabilities {
				// Coalesced into one summary audit per (user, source) — see limitaudit.go.
				h.limitAudit.record(vulnerabilityLimit, userID, currentCount, smUser.MaxVulnerabilities, sourceLabel, sourceID)
				http.Error(w, fmt.Sprintf("Vulnerability limit reached (%d/%d)", currentCount, smUser.MaxVulnerabilities), http.StatusForbidden)
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

	// Create vulnerability
	vulnerability, err := h.db.CreateVulnerability(req.Name, req.Host, req.Port, req.Severity, sourceID, sourceLabel, userID, lastSeen)
	if err != nil {
		logger.Errorf("Failed to create vulnerability: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to create vulnerability")
		return
	}

	// The host's assets follow from their findings.
	h.refreshHosts([]database.HostRef{{Host: req.Host, UserID: userID}})

	// Broadcast to WebSocket clients. Scoped: this payload carries full
	// vulnerability details, so deliver only to clients who may see this
	// owner's data (tenant isolation).
	msg, _ := ws.NewMessage(ws.TypeVulnerabilityCreated, vulnerability)
	h.hub.BroadcastScoped(msg, []int64{userID})

	// Trigger organisation score recalculation
	go h.recalculateOrganisationScore(userID)
	h.emitTotalVulnerabilities(userID)

	respondJSON(w, http.StatusCreated, vulnerability)
}

// DeleteVulnerabilitiesBySource deletes all vulnerabilities associated with a source
func (h *Handler) DeleteVulnerabilitiesBySource(w http.ResponseWriter, r *http.Request) {
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
	affectedHosts, _ := h.db.VulnerabilityHostRefsBySource(sourceID, userID)

	// Delete vulnerabilities
	deletedIDs, err := h.db.DeleteVulnerabilitiesBySource(sourceID, userID)
	if err != nil {
		logger.Errorf("Failed to delete vulnerabilities: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete vulnerabilities")
		return
	}

	h.refreshHosts(affectedHosts)

	// Broadcast deletion to WebSocket clients
	for _, id := range deletedIDs {
		msg, _ := ws.NewMessage(ws.TypeVulnerabilityDeleted, map[string]int64{"id": id})
		h.hub.Broadcast(msg)
	}

	logger.Infof("Deleted %d vulnerabilities for source: %s", len(deletedIDs), source)

	// A host's findings moved its score, and each owner's count changed.
	if len(deletedIDs) > 0 {
		for _, uid := range usersOf(affectedHosts) {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalVulnerabilities(uid)
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": len(deletedIDs),
		"ids":     deletedIDs,
	})
}

// ClearVulnerabilitySourcesBySource clears the source field for all vulnerabilities associated with a source
func (h *Handler) ClearVulnerabilitySourcesBySource(w http.ResponseWriter, r *http.Request) {
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

	// Clear vulnerability sources
	updatedIDs, err := h.db.ClearVulnerabilitySourceBySource(sourceID, userID)
	if err != nil {
		logger.Errorf("Failed to clear vulnerability sources: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to clear vulnerability sources")
		return
	}

	// Broadcast update to WebSocket clients
	for _, id := range updatedIDs {
		msg, _ := ws.NewMessage(ws.TypeVulnerabilityUpdated, map[string]int64{"id": id})
		h.hub.Broadcast(msg)
	}

	logger.Infof("Cleared source for %d vulnerabilities for source: %s", len(updatedIDs), source)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"updated": len(updatedIDs),
		"ids":     updatedIDs,
	})
}

// DeleteStaleVulnerabilitiesBySource deletes vulnerabilities associated with a source that were modified before a given timestamp
// This is used during re-import to clean up records that were not updated
func (h *Handler) DeleteStaleVulnerabilitiesBySource(w http.ResponseWriter, r *http.Request) {
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

	affectedHosts, _ := h.db.StaleVulnerabilityHostRefsBySource(sourceID, modifiedBefore)

	// Internal service request - delete for all users
	deletedIDs, err := h.db.DeleteStaleVulnerabilitiesBySource(sourceID, 0, modifiedBefore)
	if err != nil {
		logger.Errorf("Failed to delete stale vulnerabilities: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete stale vulnerabilities")
		return
	}

	// Broadcast deletion to WebSocket clients
	for _, id := range deletedIDs {
		msg, _ := ws.NewMessage(ws.TypeVulnerabilityDeleted, map[string]int64{"id": id})
		h.hub.Broadcast(msg)
	}

	// A host's findings moved its score, and each owner's count changed.
	if len(deletedIDs) > 0 {
		h.refreshHosts(affectedHosts)
		for _, uid := range usersOf(affectedHosts) {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalVulnerabilities(uid)
		}
	}

	logger.Infof("Deleted %d stale vulnerabilities for source: %s", len(deletedIDs), source)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": len(deletedIDs),
		"message": fmt.Sprintf("Deleted %d stale vulnerability(ies)", len(deletedIDs)),
	})
}

// DeleteVulnerabilitiesByIDsRequest represents the request to delete vulnerabilities by IDs
type DeleteVulnerabilitiesByIDsRequest struct {
	IDs []int64 `json:"ids"`
}

// DeleteVulnerabilitiesByIDs deletes vulnerabilities by their IDs (only if source is NULL or empty)
func (h *Handler) DeleteVulnerabilitiesByIDs(w http.ResponseWriter, r *http.Request) {
	userID, ok := GetUserID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "User ID not found in context")
		return
	}

	var req DeleteVulnerabilitiesByIDsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if len(req.IDs) == 0 {
		respondError(w, http.StatusBadRequest, "No IDs provided")
		return
	}

	// Get affected hosts before deletion
	affectedHosts, _ := h.db.GetVulnerabilityHostsByIDs(req.IDs, userID)

	// Delete vulnerabilities (only those with empty source)
	deleted, skipped, err := h.db.DeleteVulnerabilitiesByIDs(req.IDs, userID)
	if err != nil {
		logger.Errorf("Failed to delete vulnerabilities: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete vulnerabilities")
		return
	}

	h.refreshHosts(database.HostRefsOf(affectedHosts, userID))

	// Broadcast deletion to WebSocket clients
	for _, id := range deleted {
		// Scoped to the deleting user's tenant so other tenants don't
		// receive its delete events. (SECURITY: CRIT-3)
		msg, _ := ws.NewMessage(ws.TypeVulnerabilityDeleted, map[string]int64{"id": id})
		h.hub.BroadcastScoped(msg, []int64{userID})
	}

	// Trigger organisation score recalculation
	if len(deleted) > 0 {
		go h.recalculateOrganisationScore(userID)
	}

	logger.Infof("Deleted %d vulnerabilities, skipped %d (source not empty)", len(deleted), len(skipped))

	if len(deleted) > 0 {
		h.emitAudit(userID, "Info", fmt.Sprintf("User removed %d vulnerability/vulnerabilities", len(deleted)))
		h.emitTotalVulnerabilities(userID)
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"deleted":    len(deleted),
		"skipped":    len(skipped),
		"deletedIds": deleted,
		"skippedIds": skipped,
	})
}
