// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The source of a finding, as the requests name it.
//
// A new vulnerability or detection names its source by sourceId and
// sourceLabel, as an asset does. For one release the older single string
// "filename (ID: N)" is still read when both are missing, so an importer
// updated after AssetManager keeps working; the first such request of each
// kind is logged, so a deployment can tell whether anything still sends it.
//
// The by-source and stale endpoints are called with the source reference
// "filename (ID: N)" in ?source=; a source's findings are found by the id in
// it, so a renamed file still finds its own.

package handlers

import (
	"net/http"
	"strings"
	"sync"

	"tachyonik/assetmanager/internal/database"
	"tachyonik/lib/logger"
)

// legacySourceWarned records, per kind of finding, that the older source
// string has been reported.
var legacySourceWarned sync.Map

// findingSource is the source id and label a create request names: sourceId
// and sourceLabel, or, when both are missing, those read from the older
// "filename (ID: N)" string. Zero and "" for a finding entered by hand.
func findingSource(kind string, id *int64, label, legacy string) (int64, string) {
	var sourceID int64
	if id != nil && *id > 0 {
		sourceID = *id
	}
	label = strings.TrimSpace(label)
	if sourceID != 0 || label != "" || strings.TrimSpace(legacy) == "" {
		return sourceID, label
	}
	if _, seen := legacySourceWarned.LoadOrStore(kind, true); !seen {
		logger.Warnf("A %s was created with the older source string %q instead of sourceId and sourceLabel; the string is read for this release only — update the sender", kind, legacy)
	}
	return database.ParseSourceRef(legacy)
}

// sourceIDParam reads ?source=, the source reference "filename (ID: N)", and
// returns it with the id in it. Without either it answers 400 and ok is false.
func sourceIDParam(w http.ResponseWriter, r *http.Request) (source string, sourceID int64, ok bool) {
	source = r.URL.Query().Get("source")
	if source == "" {
		respondError(w, http.StatusBadRequest, "source parameter is required")
		return "", 0, false
	}
	if sourceID, _ = database.ParseSourceRef(source); sourceID == 0 {
		respondError(w, http.StatusBadRequest, "source parameter must include '(ID: N)' suffix")
		return "", 0, false
	}
	return source, sourceID, true
}
