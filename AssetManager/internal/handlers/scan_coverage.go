// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The endpoint an import applies a scan coverage table through.
//
// A coverage table cannot go through POST /api/assets one row at a time: a
// network or range covers whichever host assets fall inside it, which only
// AssetManager knows, and none of it is a sighting of a host. So the import
// sends the whole table here, and database.ApplyScanCoverage does the
// matching (see scan_coverage_apply.go).

package handlers

import (
	"encoding/json"
	"net/http"

	"tachyonik/assetmanager/internal/database"
	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

// ScanCoverageEntry is one row of a coverage table.
type ScanCoverageEntry struct {
	Target                   string `json:"target"`
	ScanCoverageIntervalDays int    `json:"scanCoverageIntervalDays"`
	ScanCoverageVerifiedAt   string `json:"scanCoverageVerifiedAt,omitempty"`
}

// ApplyScanCoverageRequest is a coverage table from one source.
type ApplyScanCoverageRequest struct {
	UserID      *int64              `json:"userId,omitempty"`
	SourceID    *int64              `json:"sourceId,omitempty"`
	SourceLabel string              `json:"sourceLabel"`
	Entries     []ScanCoverageEntry `json:"entries"`
}

// ApplyScanCoverage handles POST /api/assets/scan-coverage. Like POST
// /api/assets, a service names the user in the body and a user is the user of
// their token. Answers with what was done (database.CoverageOutcome).
func (h *Handler) ApplyScanCoverage(w http.ResponseWriter, r *http.Request) {
	var req ApplyScanCoverageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	var userID int64
	if IsInternalService(r) {
		if req.UserID != nil {
			userID = *req.UserID
		}
	} else {
		var ok bool
		if userID, ok = GetUserID(r); !ok {
			respondError(w, http.StatusUnauthorized, "User ID not found in context")
			return
		}
	}
	var sourceID int64
	if req.SourceID != nil {
		sourceID = *req.SourceID
	}
	if sourceID <= 0 && req.SourceLabel == "" {
		respondError(w, http.StatusBadRequest, "Source is required (supply sourceId+sourceLabel)")
		return
	}

	entries := make([]database.CoverageEntry, 0, len(req.Entries))
	for _, e := range req.Entries {
		entry := database.CoverageEntry{Target: e.Target, IntervalDays: e.ScanCoverageIntervalDays}
		if e.ScanCoverageVerifiedAt != "" {
			if t, ok := parseTimestamp(e.ScanCoverageVerifiedAt); ok {
				entry.VerifiedAt = &t
			} else {
				logger.Warnf("ApplyScanCoverage: ignoring unreadable scanCoverageVerifiedAt %q for %q", e.ScanCoverageVerifiedAt, e.Target)
			}
		}
		entries = append(entries, entry)
	}

	// The host asset limits apply to an asset made from a coverage table as
	// to any other. Once refused, the rest of the table is refused too
	// without asking again. Refused because SystemManager could not be asked,
	// the table is answered with 503 rather than with its outcome, so the
	// importer tries again (AM-DATA-44).
	refused, unavailable := false, false
	allow := func() bool {
		if !refused && userID != 0 {
			status, _ := h.hostAssetLimit("ApplyScanCoverage", userID, "", req.SourceLabel, sourceID)
			refused = status != 0
			unavailable = status == http.StatusServiceUnavailable
		}
		return !refused
	}

	out, err := h.db.ApplyScanCoverage(userID, sourceID, req.SourceLabel, entries, allow)
	if err != nil {
		logger.Errorf("ApplyScanCoverage: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to apply the scan coverage")
		return
	}
	logger.Infof("ApplyScanCoverage: user %d source %d (%s): %d entries, %d assets covered, %d created, %d hostnames skipped, %d ranges without a known host, %d invalid, %d refused by the limit",
		userID, sourceID, req.SourceLabel, len(entries), len(out.Covered), len(out.Created), out.Hostnames, out.Unmatched, out.Invalid, out.Refused)

	created := map[int64]bool{}
	for _, id := range out.Created {
		created[id] = true
		if asset, err := h.db.GetAssetForUsers(id, []int64{userID}); err == nil {
			msg, _ := ws.NewMessage(ws.TypeAssetCreated, asset)
			h.hub.BroadcastScoped(msg, []int64{userID})
		}
	}
	for _, id := range out.Covered {
		if !created[id] {
			msg, _ := ws.NewMessage(ws.TypeAssetUpdated, ws.AssetUpdatedPayload{ID: id})
			h.hub.BroadcastScoped(msg, []int64{userID})
		}
	}
	if len(out.Created) > 0 {
		go h.recalculateOrganisationScore(userID)
		h.emitTotalAssets(userID)
	}
	if unavailable {
		// What was applied stays; applying the table again changes nothing
		// more of it.
		respondError(w, http.StatusServiceUnavailable, limitsUnavailable)
		return
	}
	respondJSON(w, http.StatusOK, out)
}
