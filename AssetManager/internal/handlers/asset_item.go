// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// GET and PATCH /api/assets/{id}: one asset, as an execution routine reads and
// changes it (api.assets.get / api.assets.update in ActionExecutor), which had
// no route to reach before.
//
// Both are scoped to the assets the caller may see — their own, or their
// group's — and answer 404 for any other id. A PATCH may change the type only;
// see database/asset_item.go for why the other fields are not the caller's to
// set.

package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"tachyonik/assetmanager/internal/database"
	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

// GetAsset handles GET /api/assets/{id}.
func (h *Handler) GetAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := assetIDFromPath(w, r)
	if !ok {
		return
	}
	asset, err := h.db.GetAssetForUsers(id, h.GetVisibleUserIDs(r))
	if errors.Is(err, database.ErrAssetNotFound) {
		respondError(w, http.StatusNotFound, "Asset not found")
		return
	}
	if err != nil {
		logger.Errorf("Failed to get asset %d: %v", id, err)
		respondError(w, http.StatusInternalServerError, "Failed to get asset")
		return
	}
	respondJSON(w, http.StatusOK, asset)
}

// derivedAssetFields cannot be set by a PATCH, with the reason the caller gets.
var derivedAssetFields = map[string]string{
	"name":     "the name is what the asset's vulnerabilities and detections are linked by",
	"severity": "severity is computed from the asset's vulnerabilities",
	"score":    "score is computed from the asset's vulnerabilities and detections",
}

// UpdateAsset handles PATCH /api/assets/{id}. Body: {"type": "..."}.
func (h *Handler) UpdateAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := assetIDFromPath(w, r)
	if !ok {
		return
	}
	userID, ok := GetUserID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "User ID not found in context")
		return
	}

	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	var refused []string
	for field := range body {
		if field == "type" {
			continue
		}
		if why, ok := derivedAssetFields[field]; ok {
			refused = append(refused, field+" ("+why+")")
		} else {
			refused = append(refused, field+" (not a field of an asset that can be changed)")
		}
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		respondError(w, http.StatusBadRequest, "Only the type of an asset can be changed; refused: "+strings.Join(refused, "; "))
		return
	}
	var assetType string
	if raw, ok := body["type"]; !ok || json.Unmarshal(raw, &assetType) != nil || strings.TrimSpace(assetType) == "" {
		respondError(w, http.StatusBadRequest, "A non-empty \"type\" is required")
		return
	}
	assetType = strings.TrimSpace(assetType)
	// The canonical spelling, as CreateAsset stores it: host limits and counts
	// compare against "Host" exactly.
	if strings.EqualFold(assetType, "Host") {
		assetType = "Host"
	}

	asset, err := h.db.UpdateAssetTypeForUsers(id, h.GetVisibleUserIDs(r), assetType)
	if errors.Is(err, database.ErrAssetNotFound) {
		respondError(w, http.StatusNotFound, "Asset not found")
		return
	}
	if err != nil {
		logger.Errorf("Failed to update asset %d: %v", id, err)
		respondError(w, http.StatusInternalServerError, "Failed to update asset")
		return
	}

	msg, _ := ws.NewMessage(ws.TypeAssetUpdated, ws.AssetUpdatedPayload{ID: asset.ID})
	h.hub.BroadcastScoped(msg, []int64{asset.UserID})
	logger.Infof("Asset %d (%s) of user %d set to type %q by user %d", asset.ID, asset.Name, asset.UserID, assetType, userID)
	respondJSON(w, http.StatusOK, asset)
}

func assetIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid asset id")
		return 0, false
	}
	return id, true
}
