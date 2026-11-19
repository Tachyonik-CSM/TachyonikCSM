// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The sources' sweep (AM-DATA-46): AssetManager's records are what the
// sources report (AM-PUR-20), so a record whose source is gone must go too.
// ResourceManager deletes a source's records when it deletes the source, but
// deletes the source even when AssetManager does not answer; and records that
// name no source id at all can never be removed by a source. The sweep finds
// both: a minute after the start and then every hour it asks ResourceManager
// about each source id the records refer to, and removes the records of a
// source it answers with its own "Source not found" exactly as deleting the
// source would.
// Any other answer leaves that source alone until the next round, so an
// outage of ResourceManager never removes anything.
// docs/plans/sources-rule-assetmanager.md.

package handlers

import (
	"context"
	"time"

	"tachyonik/assetmanager/internal/database"
	ws "tachyonik/assetmanager/internal/websocket"
	"tachyonik/lib/logger"
)

const (
	sourceSweepFirst = time.Minute
	sourceSweepEvery = time.Hour
)

// sourceLookup is what the sweep asks of ResourceManager: false only on its
// own answer that the source does not exist (rscmanager.SourceExists).
type sourceLookup interface {
	SourceExists(id int64) (bool, error)
}

// SweepResult is what one round removed.
type SweepResult struct {
	GoneSources     []int64
	Vulnerabilities int
	Detections      int
	DeletedAssets   int
	KeptAssets      int
}

// StartSourceSweep runs SweepSources until ctx ends. Without ResourceManager
// it does nothing and says so.
func (h *Handler) StartSourceSweep(ctx context.Context) {
	if h.rscManagerClient == nil {
		logger.Warn("Sources' sweep: ResourceManager is not configured, so records of deleted sources are not removed")
		return
	}
	go func() {
		timer := time.NewTimer(sourceSweepFirst)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				h.SweepSources(h.rscManagerClient)
				timer.Reset(sourceSweepEvery)
			}
		}
	}()
}

// SweepSources removes the records of the sources ResourceManager no longer
// has, and the records that name no source.
func (h *Handler) SweepSources(sources sourceLookup) SweepResult {
	var res SweepResult

	// Records that name no source id: no source can ever remove them.
	if removed, err := h.db.DeleteSourcelessRecords(); err != nil {
		logger.Errorf("Sources' sweep: %v", err)
	} else if !removed.Empty() {
		h.refreshHosts(removed.Hosts)
		h.announce(ws.TypeVulnerabilityDeleted, removed.Vulnerabilities)
		h.announce(ws.TypeDetectionDeleted, removed.Detections)
		h.announce(ws.TypeAssetDeleted, removed.DeletedAssets)
		h.announceAssetsUpdated(removed.KeptAssets)
		owners := map[int64]bool{}
		for _, group := range [][]int64{usersOf(removed.Hosts), ownerIDs(removed.DeletedAssets), ownerIDs(removed.KeptAssets)} {
			for _, uid := range group {
				owners[uid] = true
			}
		}
		for uid := range owners {
			go h.recalculateOrganisationScore(uid)
			h.emitTotalAssets(uid)
			h.emitTotalVulnerabilities(uid)
			h.emitTotalDetections(uid)
		}
		res.Vulnerabilities += len(removed.Vulnerabilities)
		res.Detections += len(removed.Detections)
		res.DeletedAssets += len(removed.DeletedAssets)
		res.KeptAssets += len(removed.KeptAssets)
		logger.Infof("Sources' sweep: removed what no source names: %d vulnerabilities, %d detections, %d assets (%d kept by other sources)",
			len(removed.Vulnerabilities), len(removed.Detections), len(removed.DeletedAssets), len(removed.KeptAssets))
	}

	ids, err := h.db.SourceIDsInUse()
	if err != nil {
		logger.Errorf("Sources' sweep: %v", err)
		return res
	}
	for _, id := range ids {
		exists, err := sources.SourceExists(id)
		if err != nil {
			// Not ResourceManager's "not found": leave it for the next round.
			logger.Warnf("Sources' sweep: could not ask ResourceManager about source %d, skipped this round: %v", id, err)
			continue
		}
		if exists {
			continue
		}
		vulns, verr := h.removeSourceVulnerabilities(id)
		dets, derr := h.removeSourceDetections(id)
		deleted, kept, aerr := h.removeSourceAssets(id)
		if verr != nil || derr != nil || aerr != nil {
			logger.Errorf("Sources' sweep: removing the records of deleted source %d: %v %v %v", id, verr, derr, aerr)
			continue
		}
		res.GoneSources = append(res.GoneSources, id)
		res.Vulnerabilities += len(vulns)
		res.Detections += len(dets)
		res.DeletedAssets += len(deleted)
		res.KeptAssets += len(kept)
		logger.Infof("Sources' sweep: source %d no longer exists; removed %d vulnerabilities, %d detections, %d assets (%d kept by other sources)",
			id, len(vulns), len(dets), len(deleted), len(kept))
	}
	return res
}

func ownerIDs(records []database.Owned) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, r := range records {
		if !seen[r.UserID] {
			seen[r.UserID] = true
			out = append(out, r.UserID)
		}
	}
	return out
}
