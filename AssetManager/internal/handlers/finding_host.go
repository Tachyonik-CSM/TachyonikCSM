// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// A finding is evidence of its host (AM-DATA-13): creating a vulnerability or
// a detection also reports the host asset of its owner, linked to the
// finding's source, as POST /api/assets would. So every finding has its
// asset, and the rules that remove an asset with its last link — delete by
// source, the stale sweep after a re-import — remove both together.
// docs/plans/findings-need-their-host-asset.md.

package handlers

import (
	"net/http"
	"time"

	"tachyonik/assetmanager/internal/database"
	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

// findingHostLimit decides whether a finding on host may be created as far as
// its host asset goes: 0 when the asset exists or may be created, otherwise
// the status and message to refuse the finding with. Only a new host asset is
// held to the host asset limit (AM-DATA-33, -34, -44).
func (h *Handler) findingHostLimit(op string, userID int64, host, sourceLabel string, sourceID int64) (int, string) {
	exists, err := h.db.AssetExists(userID, host, database.AssetTypeHost)
	if err != nil {
		logger.Errorf("%s: %v", op, err)
		return http.StatusInternalServerError, "Failed to look up the host asset"
	}
	if exists || h.systemManagerClient == nil {
		return 0, ""
	}
	return h.hostAssetLimit(op, userID, host, sourceLabel, sourceID)
}

// reportFindingHost reports the host asset of a finding about to be created:
// created when missing, its link to the finding's source added or touched
// otherwise. It runs before the finding is stored, so a finding never exists
// without its asset, even when storing the finding then fails. A finding
// entered by hand has no source, and its asset then has no link.
func (h *Handler) reportFindingHost(userID int64, host string, sourceID int64, sourceLabel string, lastSeen *time.Time) error {
	result, err := h.db.CreateAsset(host, database.AssetTypeHost, userID, sourceID, sourceLabel, lastSeen)
	if err != nil {
		return err
	}
	if result.AssetWasNew {
		logger.Infof("Created host asset %d (%s) for user %d with a finding on it", result.Asset.ID, host, userID)
		msg, _ := ws.NewMessage(ws.TypeAssetCreated, result.Asset)
		h.hub.BroadcastScoped(msg, []int64{userID})
		h.emitTotalAssets(userID)
	} else if result.SourceWasNew {
		msg, _ := ws.NewMessage(ws.TypeAssetUpdated, ws.AssetUpdatedPayload{ID: result.Asset.ID})
		h.hub.BroadcastScoped(msg, []int64{userID})
	}
	return nil
}
