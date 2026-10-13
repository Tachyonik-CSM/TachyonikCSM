// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Cutting an InsightVM asset down to what identifies and summarises it.
//
// Trimming only deletes; every field that remains is the console's own, as it
// sent it. What is kept is what an asset inventory needs: the addresses and
// names that identify a host, its operating system, its type, its risk and
// vulnerability counts, and its latest assessment. What is dropped is what
// makes an asset large and changes with every scan — installed software,
// services, files, users, configuration values and all but one history entry —
// and the API's hypermedia links.

package insightvm

import (
	"encoding/json"
	"fmt"
	"time"
)

// keptAssetFields are the Asset fields passed on, as the API names them.
// history is kept too, reduced by latestAssessment.
var keptAssetFields = []string{
	"id",
	"ip",
	"addresses",
	"hostName",
	"hostNames",
	"mac",
	"ids",
	"os",
	"osCertainty",
	"osFingerprint",
	"type",
	"riskScore",
	"rawRiskScore",
	"vulnerabilities",
	"assessedForVulnerabilities",
	"assessedForPolicies",
}

// assessmentHistoryTypes are the history entries that mean the console learned
// something about the host itself — a scan, an agent report, an imported scan
// log — rather than that someone applied an exception to it.
var assessmentHistoryTypes = map[string]bool{
	"SCAN":            true,
	"AGENT-IMPORT":    true,
	"SCAN-LOG-IMPORT": true,
}

// trimAsset returns the cut-down asset and its id ("" when it has none).
func trimAsset(raw json.RawMessage) (json.RawMessage, string, error) {
	var full map[string]json.RawMessage
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, "", fmt.Errorf("not an asset object: %w", err)
	}

	out := make(map[string]json.RawMessage, len(keptAssetFields)+1)
	for _, k := range keptAssetFields {
		if v, ok := full[k]; ok {
			out[k] = v
		}
	}

	// The operating system's configuration values are per-scan detail, like
	// the asset's own; the rest of the fingerprint identifies the system.
	if fp, ok := out["osFingerprint"]; ok {
		var os map[string]json.RawMessage
		if json.Unmarshal(fp, &os) == nil && os != nil {
			delete(os, "configurations")
			if b, err := json.Marshal(os); err == nil {
				out["osFingerprint"] = b
			}
		}
	}

	// The history grows with every scan; its latest assessment is the one
	// entry worth keeping, as the date the console last learned about the
	// host. Without one, history is left out rather than sent empty.
	if latest := latestAssessment(full["history"]); latest != nil {
		out["history"] = json.RawMessage("[" + string(latest) + "]")
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	return b, assetID(full["id"]), nil
}

// latestAssessment returns the newest assessment entry of a history exactly as
// the console sent it, or nil when there is none.
func latestAssessment(history json.RawMessage) json.RawMessage {
	if len(history) == 0 {
		return nil
	}
	var entries []json.RawMessage
	if json.Unmarshal(history, &entries) != nil {
		return nil
	}
	var (
		latest     json.RawMessage
		latestDate time.Time
	)
	for _, raw := range entries {
		var e struct {
			Date string `json:"date"`
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &e) != nil || !assessmentHistoryTypes[e.Type] {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Date)
		if err != nil {
			continue
		}
		if latest == nil || t.After(latestDate) {
			latest, latestDate = raw, t
		}
	}
	return latest
}

// assetID renders the id for duplicate detection. The API declares it a 64-bit
// integer; it is compared as written rather than converted, so no value is
// lost to a float.
func assetID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}
