// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Checks the calls this client makes against AssetManager's API document
// (TachyonikLib apicontract): every path exists, and every field the client
// sends or decodes is documented.

package assetmanager

import (
	"testing"

	"tachyonik/lib/apicontract"
)

// scanCoverageRequest is the body ApplyScanCoverage sends, named so the check
// can walk it.
type scanCoverageRequest struct {
	UserID      int64               `json:"userId"`
	SourceID    int64               `json:"sourceId"`
	SourceLabel string              `json:"sourceLabel"`
	Entries     []ScanCoverageEntry `json:"entries"`
}

var calls = []apicontract.Call{
	{Method: "POST", Path: "/api/assets", Request: CreateAssetRequest{}, Answer: Asset{}},
	{Method: "POST", Path: "/api/assets/scan-coverage", Request: scanCoverageRequest{}, Answer: ScanCoverageOutcome{}},
	{Method: "POST", Path: "/api/vulnerabilities", Request: CreateVulnerabilityRequest{}, Answer: Vulnerability{}},
	{Method: "POST", Path: "/api/detections", Request: CreateDetectionRequest{}, Answer: Detection{}},
	{Method: "DELETE", Path: "/api/assets/stale", Query: []string{"source", "modifiedBefore"}, Answer: DeleteStaleResponse{}},
	{Method: "DELETE", Path: "/api/vulnerabilities/stale", Query: []string{"source", "modifiedBefore"}, Answer: DeleteStaleResponse{}},
	{Method: "DELETE", Path: "/api/detections/stale", Query: []string{"source", "modifiedBefore"}, Answer: DeleteStaleResponse{}},
}

func TestTheClientMatchesAssetManagersDocument(t *testing.T) {
	apicontract.Test(t, "AssetManager", calls)
	apicontract.TestDeclared(t, ".", calls)
}
