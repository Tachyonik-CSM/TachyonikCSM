// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the scan coverage part of an import routine's contract: the routine
// sees when its file was received as ctx.fileDate, may return scanCoverage
// rows besides assets, vulnerabilities and detections, and a routine for a
// coverage table passes validation.

package jsruntime

import "testing"

// The routine the import rule for "target|scan-interval" tables asks for.
const coverageRoutine = `
function importFunction(ctx) {
	var out = { assets: [], vulnerabilities: [], detections: [], scanCoverage: [] };
	var lines = (ctx.fileContent || "").split(/\r?\n/);
	if (lines.length === 0 || lines[0].trim() !== "target|scan-interval") return out;
	for (var i = 1; i < lines.length; i++) {
		var parts = lines[i].split("|");
		if (parts.length !== 2) continue;
		var days = parseInt(parts[1], 10);
		if (!(days >= 1)) continue;
		out.scanCoverage.push({ target: parts[0].trim(), scanCoverageIntervalDays: days, scanCoverageVerifiedAt: ctx.fileDate });
	}
	return out;
}`

func TestARoutineReturnsScanCoverageDatedByItsFile(t *testing.T) {
	e := New()
	if err := e.LoadFromString(coverageRoutine); err != nil {
		t.Fatal(err)
	}
	res, err := e.Execute(ImportContext{
		FileContent: "target|scan-interval\n192.168.178.54|7\n10.0.0.0/24|1\nbad line\n10.0.0.9|0\n",
		FileDate:    "2026-10-21T07:10:09Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ScanCoverage) != 2 || len(res.Assets) != 0 {
		t.Fatalf("result = %+v", res)
	}
	want := ScanCoverageResult{Target: "192.168.178.54", ScanCoverageIntervalDays: 7, ScanCoverageVerifiedAt: "2026-10-21T07:10:09Z"}
	if res.ScanCoverage[0] != want {
		t.Errorf("first row = %+v, want %+v", res.ScanCoverage[0], want)
	}
}

func TestACoverageRoutinePassesValidation(t *testing.T) {
	e := New()
	if err := e.LoadFromString(coverageRoutine); err != nil {
		t.Fatal(err)
	}
	if errs := e.ValidateWithMockCtx(); len(errs) > 0 {
		t.Errorf("validation: %v", errs)
	}
}

func TestScanCoverageIsOptional(t *testing.T) {
	e := New()
	if err := e.LoadFromString(`function importFunction(ctx) { return { assets: [], vulnerabilities: [], detections: [] }; }`); err != nil {
		t.Fatal(err)
	}
	res, err := e.Execute(ImportContext{FileContent: "x"})
	if err != nil || res.ScanCoverage != nil {
		t.Errorf("got %+v, %v; want no scan coverage", res, err)
	}
}
