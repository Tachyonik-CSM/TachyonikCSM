// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Cutting an InsightVM asset down to what identifies and summarises it.
//
// What is kept is what an asset inventory needs: the addresses and names that
// identify a host, its operating system, its type, its risk and vulnerability
// counts, and when it was last assessed. What is dropped is what makes an asset
// large and changes with every scan — installed software, services, files,
// users, configuration values and the full scan history — and the API's
// hypermedia links.

package insightvm

import (
	"encoding/json"
	"fmt"
	"time"
)

// keptAssetFields are the Asset fields passed on unchanged, as the API names
// them.
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
//
// Beyond keptAssetFields it adds one field of its own, lastAssessed: the latest
// assessment date in the asset's history, which is the one thing worth keeping
// from a list that grows with every scan.
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

	if last := lastAssessed(full["history"]); last != "" {
		b, _ := json.Marshal(last)
		out["lastAssessed"] = b
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	return b, assetID(full["id"]), nil
}

// lastAssessed is the latest assessment date in a history, as RFC 3339 UTC,
// or "" when there is none.
func lastAssessed(history json.RawMessage) string {
	if len(history) == 0 {
		return ""
	}
	var entries []struct {
		Date string `json:"date"`
		Type string `json:"type"`
	}
	if json.Unmarshal(history, &entries) != nil {
		return ""
	}
	var latest time.Time
	for _, e := range entries {
		if !assessmentHistoryTypes[e.Type] {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Date)
		if err != nil {
			continue
		}
		if t.After(latest) {
			latest = t
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339)
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
