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
	"tachyonik/lib/listquery"
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

	filter, err := filterParam(r, database.CheckVulnerabilityFilter)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// sort, order and limit (AM-API-16); absent, the list is as it always was.
	lq, err := listquery.Parse(r.URL.Query(), database.VulnerabilitySortFields)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	visibleUserIDs := h.GetVisibleUserIDs(r)

	vulnerabilities, err := h.db.ListVulnerabilities(visibleUserIDs, filter, lq)
	if err != nil {
		logger.Errorf("Failed to retrieve vulnerabilities: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to retrieve vulnerabilities")
		return
	}
	total, err := listTotal(lq, len(vulnerabilities), func() (int, error) {
		return h.db.CountVulnerabilitiesByUsersWithFilter(visibleUserIDs, filter)
	})
	if err != nil {
		logger.Errorf("Failed to count vulnerabilities: %v", err)
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
		Total:              total,
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

	// The owner: the user of the token, or the one a service names
	// (AM-SEC-13).
	userID, ok := creatorOf(w, r, req.UserID)
	if !ok {
		return
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
	if sourceID == 0 {
		respondError(w, http.StatusBadRequest, sourceRequired)
		return
	}

	// A finding of a known identity is reported again, not created
	// (AM-DATA-47): it creates nothing, so no limit applies to it and its host
	// asset exists (AM-DATA-13, -35).
	report := database.FindingReport{Name: req.Name, Host: req.Host, Port: req.Port, Severity: &req.Severity, SourceID: sourceID, SourceLabel: sourceLabel, UserID: userID}
	known, err := h.db.FindingExists(database.VulnerabilityTable, report)
	if err != nil {
		logger.Errorf("Failed to look up the vulnerability: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to look up the vulnerability")
		return
	}

	// Enforce vulnerability limit
	if !known && h.systemManagerClient != nil {
		smUser, smErr := h.systemManagerClient.GetUser(userID)
		if smErr != nil {
			// A limit that cannot be read cannot be kept (AM-DATA-44).
			logger.Errorf("Failed to fetch user %d from SystemManager for the vulnerability limit check: %v", userID, smErr)
			respondError(w, http.StatusServiceUnavailable, limitsUnavailable)
			return
		} else if smUser == nil {
			// An owner no one knows would get records no one sees, held to
			// no limit (AM-DATA-35).
			logger.Warnf("User %d does not exist, rejecting the create", userID)
			respondError(w, http.StatusBadRequest, unknownOwner)
			return
		} else if smUser.MaxVulnerabilities > 0 {
			currentCount, _ := h.db.CountVulnerabilitiesByUserWithFilter(userID, nil)
			if currentCount >= smUser.MaxVulnerabilities {
				// Coalesced into one summary audit per (user, source) — see limitaudit.go.
				h.limitAudit.record(vulnerabilityLimit, userID, currentCount, smUser.MaxVulnerabilities, sourceLabel, sourceID)
				respondError(w, http.StatusForbidden, fmt.Sprintf("Vulnerability limit reached (%d/%d)", currentCount, smUser.MaxVulnerabilities))
				return
			}
		}
	}

	// The host asset of the finding: a new one is held to the host asset
	// limit like any other (AM-DATA-13, AM-DATA-33).
	if !known {
		if status, msg := h.findingHostLimit("CreateVulnerability", userID, req.Host, sourceLabel, sourceID); status != 0 {
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

	// Create vulnerability
	// Its host asset first, so the finding never exists without it.
	if err := h.reportFindingHost(userID, req.Host, sourceID, sourceLabel, lastSeen); err != nil {
		logger.Errorf("Failed to report the host asset of a vulnerability: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to create the host asset of the vulnerability")
		return
	}

	report.LastSeen = lastSeen
	outcome, err := h.db.ReportFinding(database.VulnerabilityTable, report)
	if err != nil {
		logger.Errorf("Failed to create vulnerability: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to create vulnerability")
		return
	}
	vulnerability, err := h.db.GetVulnerabilityByID(outcome.ID)
	if err != nil {
		logger.Errorf("Failed to read the vulnerability: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to create vulnerability")
		return
	}

	// The host's assets follow from their findings.
	h.refreshHosts([]database.HostRef{{Host: req.Host, UserID: userID}})

	// Broadcast to WebSocket clients. Scoped: this payload carries full
	// vulnerability details, so deliver only to clients who may see this
	// owner's data (tenant isolation).
	// A finding reported again is announced only when something shown changed
	// (AM-DATA-47), whole, as a created one is.
	if outcome.New || outcome.Changed {
		kind := ws.TypeVulnerabilityCreated
		if !outcome.New {
			kind = ws.TypeVulnerabilityUpdated
		}
		msg, _ := ws.NewMessage(kind, vulnerability)
		h.hub.BroadcastScoped(msg, []int64{userID})

		// The severity may have moved the organisation score.
		go h.recalculateOrganisationScore(userID)
	}
	if outcome.New {
		h.emitTotalVulnerabilities(userID)
		respondJSON(w, http.StatusCreated, vulnerability)
		return
	}
	respondJSON(w, http.StatusOK, vulnerability)
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

	// Announce each deletion to the owner's workspace and the services
	h.announce(ws.TypeVulnerabilityDeleted, deletedIDs)

	// A host's findings moved its score, and each owner's count changed.
	if len(deletedIDs) > 0 {
		h.refreshHosts(affectedHosts)
		for _, uid := range usersOf(affectedHosts) {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalVulnerabilities(uid)
		}
	}

	logger.Infof("Deleted %d stale vulnerabilities for source: %s", len(deletedIDs), source)

	respondJSON(w, http.StatusOK, changeResult(database.IDs(deletedIDs), nil, nil).
		withMessage(fmt.Sprintf("Deleted %d stale vulnerability(ies)", len(deletedIDs))))
}
