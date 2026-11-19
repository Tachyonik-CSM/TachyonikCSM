// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// GET /api/assets/{id}: one asset, as an execution routine reads it
// (api.assets.get). Scoped to the assets the caller may see — their own, or
// their group's — and 404 for any other id. Nothing of an asset can be changed:
// its name and type are set when an import creates it, the rest is computed
// (AM-DATA-06).

package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"tachyonik/assetmanager/internal/database"
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

func assetIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid asset id")
		return 0, false
	}
	return id, true
}
