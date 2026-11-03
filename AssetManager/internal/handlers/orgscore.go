// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The two Tachyonik sub-scores this service owns.
//
// Exposure is the average asset score, where an asset scores 100 minus the worst
// severity found on it; with no assets at all it is 0. Coverage measures how
// much of the organisation's declared estate is actually accounted for, and is
// likewise 0 when the organisation has declared nothing.
//
// The other two, Capability (ToolManager's) and Compliance (computed by
// SystemManager itself), are not this service's. SystemManager holds the
// weighted sum, so each owner pushes its own and the overall score is
// recomputed in one place.

package handlers

import (
	"encoding/json"
	"math"
	"net/http"

	"tachyonik/lib/logger"
)

// AssetManager owns two of the three Tachyonik sub-scores:
//
//   - Exposure Score: average asset score (each asset score = 100 - max severity).
//     Goes to 0 when the user has no assets at all.
//   - Coverage Score: 100 - ((missing + unmanaged) / org_assets * 100). Goes to 0 when
//     the organisation has declared no assets_hosts. Unmanaged assets are those
//     with no detections at all.
//
// The third sub-score (Capability) is owned by ToolManager. The overall
// Tachyonik Score is the weighted sum of the three; SystemManager recomputes it
// inline whenever any sub-score is pushed (see UpdateOrganisationSubScore).

// calculateExposureScore returns a percentage in [0, 100].
func calculateExposureScore(assetCount, assetScoreSum int) float64 {
	if assetCount <= 0 {
		return 0
	}
	s := float64(assetScoreSum) / float64(assetCount)
	return clampScore(s)
}

// calculateCoverageScore returns a percentage in [0, 100].
func calculateCoverageScore(assetsHosts, assetCount, unmanagedCount int) float64 {
	if assetsHosts <= 0 {
		return 0
	}
	missing := assetsHosts - assetCount
	if missing < 0 {
		missing = 0
	}
	uncovered := missing + unmanagedCount
	return clampScore(100.0 - (float64(uncovered)/float64(assetsHosts))*100.0)
}

func clampScore(v float64) float64 {
	v = math.Round(v*100) / 100
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// recalculateOrganisationScore recomputes AssetManager's two sub-scores
// (exposure + coverage) for a user and pushes each to SystemManager. The
// overall Tachyonik Score is recomputed server-side when each sub-score lands.
func (h *Handler) recalculateOrganisationScore(userID int64) {
	// Nothing to push to without SystemManager, as the other SystemManager
	// calls here already assume; this one ran in a goroutine and crashed.
	if userID == 0 || h.systemManagerClient == nil {
		return
	}

	// Get organisation from SystemManager (for assets_hosts).
	org, err := h.systemManagerClient.GetOrganisationByUserID(userID)
	if err != nil {
		logger.Errorf("recalculateScore: failed to get organisation for user %d: %v", userID, err)
		return
	}
	if org == nil {
		logger.Warnf("recalculateScore: no organisation found for user %d, skipping", userID)
		return
	}

	// Asset aggregates (count + SUM(score)) and unmanaged count.
	agg, err := h.db.GetAssetScoreAggregatesByUser(userID)
	if err != nil {
		logger.Errorf("recalculateScore: failed to get asset aggregates for user %d: %v", userID, err)
		return
	}
	var unmanagedCount int
	if assetStats, statsErr := h.db.GetAssetStatsByUser(userID); statsErr == nil {
		unmanagedCount = assetStats.Unmanaged
	} else {
		logger.Warnf("recalculateScore: failed to get asset stats for user %d: %v", userID, statsErr)
	}

	exposure := calculateExposureScore(agg.Count, agg.ScoreSum)
	coverage := calculateCoverageScore(org.AssetsHosts, agg.Count, unmanagedCount)

	logger.Debugf("recalculateScore: user %d exposure=%.2f coverage=%.2f (assetsHosts=%d assetCount=%d scoreSum=%d unmanaged=%d)",
		userID, exposure, coverage, org.AssetsHosts, agg.Count, agg.ScoreSum, unmanagedCount)

	if err := h.systemManagerClient.UpdateOrganisationSubScore(userID, "exposure", exposure); err != nil {
		logger.Errorf("recalculateScore: failed to push exposure sub-score for user %d: %v", userID, err)
	}
	if err := h.systemManagerClient.UpdateOrganisationSubScore(userID, "coverage", coverage); err != nil {
		logger.Errorf("recalculateScore: failed to push coverage sub-score for user %d: %v", userID, err)
	}
}

// RecalculateAllOrganisationScores recomputes exposure + coverage for every
// known user, not just users with assets — users without assets legitimately
// score 0/0 on both, which still contributes to the overall score via the
// weighted sum and keeps the dashboard consistent.
func (h *Handler) RecalculateAllOrganisationScores() {
	userIDs, err := h.systemManagerClient.ListAllUserIDs()
	if err != nil {
		logger.Errorf("RecalculateAllOrganisationScores: failed to list users: %v", err)
		return
	}

	if len(userIDs) == 0 {
		logger.Infof("RecalculateAllOrganisationScores: no users, skipping")
		return
	}

	logger.Infof("RecalculateAllOrganisationScores: recalculating exposure+coverage for %d user(s)", len(userIDs))
	for _, uid := range userIDs {
		h.recalculateOrganisationScore(uid)
	}
	logger.Infof("RecalculateAllOrganisationScores: done")
}

// RecalculateOrganisationScoreRequest represents the request body
type RecalculateOrganisationScoreRequest struct {
	UserID int64 `json:"userId"`
}

// RecalculateOrganisationScore handles internal requests to recalculate the organisation score
func (h *Handler) RecalculateOrganisationScore(w http.ResponseWriter, r *http.Request) {
	var req RecalculateOrganisationScoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.UserID == 0 {
		respondError(w, http.StatusBadRequest, "userId is required")
		return
	}

	h.recalculateOrganisationScore(req.UserID)

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
