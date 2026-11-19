// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// AM-DATA-45's usage is for internal services only: a signed-in user must not
// learn what the other users hold.

package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUserUsageIsForInternalServicesOnly(t *testing.T) {
	w := httptest.NewRecorder()
	(&Handler{}).UserUsage(w, httptest.NewRequest("GET", "/api/internal/users/usage", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("without the service key: %d, want 403", w.Code)
	}
}
