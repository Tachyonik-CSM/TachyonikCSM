// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The sources rule AssetManager's content (AM-PUR-20): a record goes only
// with its source. This file removes a source's records — its findings, its
// links, and the assets left with no link (AM-PUR-04) — for the two callers
// that may: ResourceManager, through DELETE …/by-source when it deletes a
// source, and the sources' sweep (source_sweep.go) for a source deleted while
// AssetManager did not answer. No user may (AM-SEC-15).

package handlers

import (
	"fmt"
	"net/http"

	"tachyonik/assetmanager/internal/database"
	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

// bySourceForServicesOnly is the refusal of a user asking to delete by source.
const bySourceForServicesOnly = "Records go with their source: they are removed when the source is deleted"

// removeSourceVulnerabilities deletes the source's vulnerabilities, of every
// owner, recomputes their hosts and announces it.
func (h *Handler) removeSourceVulnerabilities(sourceID int64) ([]database.Owned, error) {
	hosts, _ := h.db.VulnerabilityHostRefsBySource(sourceID, 0)
	deleted, err := h.db.DeleteVulnerabilitiesBySource(sourceID, 0)
	if err != nil {
		return nil, err
	}
	h.refreshHosts(hosts)
	h.announce(ws.TypeVulnerabilityDeleted, deleted)
	if len(deleted) > 0 {
		for _, uid := range usersOf(hosts) {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalVulnerabilities(uid)
		}
	}
	return deleted, nil
}

// removeSourceDetections deletes the source's detections, of every owner,
// recomputes their hosts and announces it.
func (h *Handler) removeSourceDetections(sourceID int64) ([]database.Owned, error) {
	hosts, _ := h.db.DetectionHostRefsBySource(sourceID, 0)
	deleted, err := h.db.DeleteDetectionsBySource(sourceID, 0)
	if err != nil {
		return nil, err
	}
	h.refreshHosts(hosts)
	h.announce(ws.TypeDetectionDeleted, deleted)
	if len(deleted) > 0 {
		for _, uid := range usersOf(hosts) {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalDetections(uid)
		}
	}
	return deleted, nil
}

// removeSourceAssets removes the source's links, deletes the assets left
// with none and recomputes the others from their remaining sources.
func (h *Handler) removeSourceAssets(sourceID int64) (deleted, kept []database.Owned, err error) {
	owners, _ := h.db.GetUserIDsByAssetSource(sourceID, 0)
	deleted, kept, err = h.db.DeleteAssetsBySource(sourceID, 0)
	if err != nil {
		return nil, nil, err
	}
	h.announce(ws.TypeAssetDeleted, deleted)
	h.announceAssetsUpdated(kept)
	if len(deleted) > 0 || len(kept) > 0 {
		for _, uid := range owners {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalAssets(uid)
		}
	}
	return deleted, kept, nil
}

// bySourceRequest reads the source of DELETE …/by-source and refuses a user.
func bySourceRequest(w http.ResponseWriter, r *http.Request) (string, int64, bool) {
	if !IsInternalService(r) { // the middleware's verdict, not the header's presence
		respondError(w, http.StatusForbidden, bySourceForServicesOnly)
		return "", 0, false
	}
	return sourceIDParam(w, r)
}

// DeleteVulnerabilitiesBySource handles DELETE /api/vulnerabilities/by-source
// (service only).
func (h *Handler) DeleteVulnerabilitiesBySource(w http.ResponseWriter, r *http.Request) {
	source, sourceID, ok := bySourceRequest(w, r)
	if !ok {
		return
	}
	deleted, err := h.removeSourceVulnerabilities(sourceID)
	if err != nil {
		logger.Errorf("Failed to delete vulnerabilities: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete vulnerabilities")
		return
	}
	logger.Infof("Deleted %d vulnerabilities for source: %s", len(deleted), source)
	respondJSON(w, http.StatusOK, changeResult(database.IDs(deleted), nil, nil).withLegacyIDs(database.IDs(deleted)))
}

// DeleteDetectionsBySource handles DELETE /api/detections/by-source (service
// only).
func (h *Handler) DeleteDetectionsBySource(w http.ResponseWriter, r *http.Request) {
	source, sourceID, ok := bySourceRequest(w, r)
	if !ok {
		return
	}
	deleted, err := h.removeSourceDetections(sourceID)
	if err != nil {
		logger.Errorf("Failed to delete detections: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete detections")
		return
	}
	logger.Infof("Deleted %d detections for source: %s", len(deleted), source)
	respondJSON(w, http.StatusOK, changeResult(database.IDs(deleted), nil, nil).withLegacyIDs(database.IDs(deleted)))
}

// DeleteAssetsBySource handles DELETE /api/assets/by-source (service only).
func (h *Handler) DeleteAssetsBySource(w http.ResponseWriter, r *http.Request) {
	source, sourceID, ok := bySourceRequest(w, r)
	if !ok {
		return
	}
	deleted, kept, err := h.removeSourceAssets(sourceID)
	if err != nil {
		logger.Errorf("Failed to delete assets: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to delete assets")
		return
	}
	logger.Infof("Deleted %d assets for source: %s, %d kept by other sources", len(deleted), source, len(kept))
	respondJSON(w, http.StatusOK, changeResult(database.IDs(deleted), database.IDs(kept), nil).
		withMessage(fmt.Sprintf("Deleted %d asset(s)", len(deleted))))
}
