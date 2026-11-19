// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the sources' sweep (AM-DATA-46): the records of a source
// ResourceManager no longer has go as deleting the source would — an asset
// another source still reports stays — while a source ResourceManager could
// not be asked about is left alone; and records that name no source id go.

package handlers

import (
	"errors"
	"testing"

	"tachyonik/assetmanager/internal/database"
	"tachyonik/lib/listquery"
)

// fakeSources answers SourceExists from a map; an id missing from it errs.
type fakeSources map[int64]bool

func (f fakeSources) SourceExists(id int64) (bool, error) {
	exists, known := f[id]
	if !known {
		return false, errors.New("ResourceManager did not answer")
	}
	return exists, nil
}

func names(t *testing.T, h *Handler, userID int64) map[string]database.Asset {
	t.Helper()
	assets, err := h.db.ListAssets([]int64{userID}, nil, listquery.Query{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]database.Asset{}
	for _, a := range assets {
		out[a.Name] = a
	}
	return out
}

func TestTheSweepRemovesTheRecordsOfADeletedSource(t *testing.T) {
	h := scopedHandler(t)
	must := func(_ interface{}, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// 10.0.0.1: sources 1 and 2. 10.0.0.2: source 2 only, with a
	// vulnerability. 10.0.0.3: source 3, which ResourceManager cannot be
	// asked about, with a detection.
	must(h.db.CreateAsset("10.0.0.1", "Host", 7, 1, "a.xml", nil))
	must(h.db.CreateAsset("10.0.0.1", "Host", 7, 2, "b.xml", nil))
	must(h.db.CreateAsset("10.0.0.2", "Host", 7, 2, "b.xml", nil))
	must(h.db.CreateVulnerability("CVE-1", "10.0.0.2", "443", 50, 2, "b.xml", 7, nil))
	must(h.db.CreateAsset("10.0.0.3", "Host", 7, 3, "c.xml", nil))
	must(h.db.CreateDetection("ssh", "10.0.0.3", "22", 3, "c.xml", 7, nil))
	// The hand-made 10.0.0.7 of scopedHandler has no link: it goes too.

	res := h.SweepSources(fakeSources{1: true, 2: false})

	if len(res.GoneSources) != 1 || res.GoneSources[0] != 2 || res.Vulnerabilities != 1 {
		t.Errorf("result %+v, want source 2 gone with its vulnerability", res)
	}
	got := names(t, h, 7)
	if a, ok := got["10.0.0.1"]; !ok || len(a.Sources) != 1 || *a.Sources[0].SourceID != 1 {
		t.Errorf("10.0.0.1: %+v, want it kept with source 1 only", a)
	}
	if _, ok := got["10.0.0.2"]; ok {
		t.Error("10.0.0.2 outlived its only source")
	}
	if a, ok := got["10.0.0.3"]; !ok || len(a.Sources) != 1 {
		t.Error("10.0.0.3 was removed although ResourceManager could not be asked about its source")
	}
	if _, ok := got["10.0.0.7"]; ok {
		t.Error("10.0.0.7, which names no source, was kept")
	}
	if n, _ := h.db.CountDetectionsByUserWithFilter(7, nil); n != 1 {
		t.Errorf("%d detections, want source 3's one", n)
	}
	noFindingWithoutItsAsset(t, h)

	// A second round removes nothing more.
	if again := h.SweepSources(fakeSources{1: true, 2: false}); len(again.GoneSources) != 0 || again.DeletedAssets != 0 {
		t.Errorf("second round: %+v", again)
	}
}

func TestTheSweepRemovesWhatNamesNoSource(t *testing.T) {
	h := scopedHandler(t)
	if _, err := h.db.CreateAsset("10.0.0.1", "Host", 7, 1, "a.xml", nil); err != nil {
		t.Fatal(err)
	}
	// As older versions stored them: a finding without a source on a host
	// that keeps one, and an asset known only by a label.
	for _, q := range []string{
		"INSERT INTO vulnerabilities (name, host, port, severity, source_label, user_id, created_at, modified_at, last_seen) VALUES ('CVE-9', '10.0.0.1', '80', 30, '', 7, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
		"INSERT INTO assets (name, type, severity, score, user_id, created_at, modified_at, last_seen) VALUES ('10.0.0.5', 'Host', 0, 0, 7, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
		"INSERT INTO asset_source (asset_id, source_id, source_label, last_seen, created_at, modified_at) SELECT id, NULL, 'old.xml', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM assets WHERE name = '10.0.0.5'",
	} {
		if _, err := h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	res := h.SweepSources(fakeSources{1: true})

	if res.Vulnerabilities != 1 || res.DeletedAssets != 2 {
		t.Errorf("result %+v, want the vulnerability, 10.0.0.5 and 10.0.0.7 removed", res)
	}
	got := names(t, h, 7)
	if len(got) != 1 {
		t.Errorf("assets left: %v, want only 10.0.0.1", got)
	}
	if n, _ := h.db.CountVulnerabilitiesByUserWithFilter(7, nil); n != 0 {
		t.Errorf("%d vulnerabilities left", n)
	}
	noFindingWithoutItsAsset(t, h)
}

func TestWithoutResourceManagerTheSweepRemovesNoSourcesRecords(t *testing.T) {
	h := scopedHandler(t)
	if _, err := h.db.CreateAsset("10.0.0.1", "Host", 7, 1, "a.xml", nil); err != nil {
		t.Fatal(err)
	}
	res := h.SweepSources(fakeSources{}) // every question errs
	if len(res.GoneSources) != 0 {
		t.Errorf("gone %v while ResourceManager did not answer", res.GoneSources)
	}
	if _, ok := names(t, h, 7)["10.0.0.1"]; !ok {
		t.Error("10.0.0.1 was removed while ResourceManager did not answer")
	}
}
