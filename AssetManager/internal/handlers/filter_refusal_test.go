// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that the lists answer a filter they cannot read with 400 and the
// reason as {"error"} (AM-API-05, AM-API-02).

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTheListsRefuseAnUnreadableFilter(t *testing.T) {
	h := scopedHandler(t)
	bad := url.QueryEscape(`{"rules":[{"column":"owner","operator":"contains","value":"x"}],"matchAll":true}`)
	for _, c := range []struct {
		what    string
		handler http.HandlerFunc
		path    string
	}{
		{"assets", h.ListAssets, "/api/assets"},
		{"vulnerabilities", h.ListVulnerabilities, "/api/vulnerabilities"},
		{"detections", h.ListDetections, "/api/detections"},
	} {
		w := httptest.NewRecorder()
		c.handler(w, callAs("GET", c.path+"?filter="+bad, "", 7, false))
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(w.Body.Bytes(), &e)
		if w.Code != http.StatusBadRequest || !strings.Contains(e.Error, `unknown column "owner"`) {
			t.Errorf("%s: %d %s, want 400 naming the column", c.what, w.Code, w.Body)
		}
	}
}
