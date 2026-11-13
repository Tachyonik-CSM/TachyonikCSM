// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The one answer of every deletion and unlinking (AM-API-13): by id, by
// source, unlinking a source and stale, for assets, vulnerabilities and
// detections alike.
//
// They used to answer in four shapes — counts with a message, counts with
// "ids", counts with "deletedIds" — so a client could update its list after
// some deletions and not after others. The WebUI's asset list, which expected
// deletedIds, removed every selected asset, including the ones that were
// skipped because a source still reported them.

package handlers

// ChangeResult says what a deletion or an unlinking did, by id. Every pair is
// always present, empty when it does not apply, so a client reads every
// answer the same way.
type ChangeResult struct {
	// Deleted: the records that are gone.
	Deleted    int     `json:"deleted"`
	DeletedIDs []int64 `json:"deletedIds"`
	// Updated: the records that remain but changed — lost the source, or
	// were recomputed because another source still reports them.
	Updated    int     `json:"updated"`
	UpdatedIDs []int64 `json:"updatedIds"`
	// Skipped: records named by id that were not deleted — one a source still
	// reports, or one outside the caller's own records (AM-SEC-10, AM-SEC-11).
	Skipped    int     `json:"skipped"`
	SkippedIDs []int64 `json:"skippedIds"`

	// Deprecated (AM-API-08): the fields of the earlier answers, sent for one
	// release beside the ones above and removed in the next.
	Message string   `json:"message,omitempty"`
	IDs     *[]int64 `json:"ids,omitempty"`
}

// changeResult builds the answer; nil lists become empty ones.
func changeResult(deleted, updated, skipped []int64) ChangeResult {
	nonNil := func(ids []int64) []int64 {
		if ids == nil {
			return []int64{}
		}
		return ids
	}
	d, u, s := nonNil(deleted), nonNil(updated), nonNil(skipped)
	return ChangeResult{Deleted: len(d), DeletedIDs: d, Updated: len(u), UpdatedIDs: u, Skipped: len(s), SkippedIDs: s}
}

// withLegacyIDs adds the deprecated "ids" the finding by-source answers had.
func (r ChangeResult) withLegacyIDs(ids []int64) ChangeResult {
	cp := append([]int64{}, ids...)
	r.IDs = &cp
	return r
}

// withMessage adds the deprecated "message" the asset and stale answers had.
func (r ChangeResult) withMessage(m string) ChangeResult {
	r.Message = m
	return r
}
