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
	"tachyonik/lib/listquery"
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

	filter, err := filterParam(r, database.CheckDetectionFilter)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// sort, order and limit (AM-API-16); absent, the list is as it always was.
	lq, err := listquery.Parse(r.URL.Query(), database.DetectionSortFields)
	if err == nil {
		lq, err = listquery.ParseOffset(r.URL.Query(), lq) // paging (AM-API-16)
	}
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	visibleUserIDs := h.GetVisibleUserIDs(r)

	detections, err := h.db.ListDetections(visibleUserIDs, filter, lq)
	if err != nil {
		logger.Errorf("Failed to retrieve detections: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to retrieve detections")
		return
	}
	total, err := listTotal(lq, len(detections), func() (int, error) {
		return h.db.CountDetectionsByUsersWithFilter(visibleUserIDs, filter)
	})
	if err != nil {
		logger.Errorf("Failed to count detections: %v", err)
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
		Total:         total,
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

	// The owner: the user of the token, or the one a service names
	// (AM-SEC-13).
	userID, ok := creatorOf(w, r, req.UserID)
	if !ok {
		return
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
	if sourceID == 0 {
		respondError(w, http.StatusBadRequest, sourceRequired)
		return
	}

	// A finding of a known identity is reported again, not created
	// (AM-DATA-47): it creates nothing, so no limit applies to it and its host
	// asset exists (AM-DATA-13, -35).
	report := database.FindingReport{Name: req.Name, Host: req.Host, Port: req.Port, SourceID: sourceID, SourceLabel: sourceLabel, UserID: userID}
	known, err := h.db.FindingExists(database.DetectionTable, report)
	if err != nil {
		logger.Errorf("Failed to look up the detection: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to look up the detection")
		return
	}

	// Enforce detection limit
	if !known && h.systemManagerClient != nil {
		smUser, smErr := h.systemManagerClient.GetUser(userID)
		if smErr != nil {
			// A limit that cannot be read cannot be kept (AM-DATA-44).
			logger.Errorf("Failed to fetch user %d from SystemManager for the detection limit check: %v", userID, smErr)
			respondError(w, http.StatusServiceUnavailable, limitsUnavailable)
			return
		} else if smUser == nil {
			// An owner no one knows would get records no one sees, held to
			// no limit (AM-DATA-35).
			logger.Warnf("User %d does not exist, rejecting the create", userID)
			respondError(w, http.StatusBadRequest, unknownOwner)
			return
		} else if smUser.MaxDetections > 0 {
			currentCount, _ := h.db.CountDetectionsByUserWithFilter(userID, nil)
			if currentCount >= smUser.MaxDetections {
				// Coalesced into one summary audit per (user, source) — see limitaudit.go.
				h.limitAudit.record(detectionLimit, userID, currentCount, smUser.MaxDetections, sourceLabel, sourceID)
				respondError(w, http.StatusForbidden, fmt.Sprintf("Detection limit reached (%d/%d)", currentCount, smUser.MaxDetections))
				return
			}
		}
	}

	// The host asset of the finding: a new one is held to the host asset
	// limit like any other (AM-DATA-13, AM-DATA-33).
	if !known {
		if status, msg := h.findingHostLimit("CreateDetection", userID, req.Host, sourceLabel, sourceID); status != 0 {
			respondError(w, status, msg)
			return
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
	// Its host asset first, so the finding never exists without it.
	if err := h.reportFindingHost(userID, req.Host, sourceID, sourceLabel, lastSeen); err != nil {
		logger.Errorf("Failed to report the host asset of a detection: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to create the host asset of the detection")
		return
	}

	report.LastSeen = lastSeen
	outcome, err := h.db.ReportFinding(database.DetectionTable, report)
	if err != nil {
		logger.Errorf("Failed to create detection: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to create detection")
		return
	}
	detection, err := h.db.GetDetectionByID(outcome.ID)
	if err != nil {
		logger.Errorf("Failed to read the detection: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to create detection")
		return
	}

	// The host's assets follow from their findings.
	h.refreshHosts([]database.HostRef{{Host: req.Host, UserID: userID}})

	// Broadcast to WebSocket clients. Scoped: this payload carries full
	// detection details, so deliver only to clients who may see this owner's
	// data (tenant isolation).
	// A finding reported again is announced only when something shown changed
	// (AM-DATA-47), whole, as a created one is.
	if outcome.New || outcome.Changed {
		kind := ws.TypeDetectionCreated
		if !outcome.New {
			kind = ws.TypeDetectionUpdated
		}
		msg, _ := ws.NewMessage(kind, detection)
		h.hub.BroadcastScoped(msg, []int64{userID})
	}
	if outcome.New {
		h.emitTotalDetections(userID)
		respondJSON(w, http.StatusCreated, detection)
		return
	}
	respondJSON(w, http.StatusOK, detection)
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

	respondJSON(w, http.StatusOK, changeResult(database.IDs(deletedIDs), nil, nil).
		withMessage(fmt.Sprintf("Deleted %d stale detection(s)", len(deletedIDs))))
}
