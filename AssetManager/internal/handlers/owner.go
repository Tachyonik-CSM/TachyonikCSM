// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Who may create a record, and whom it belongs to. Only a service importing
// a source creates records (AM-PUR-20, AM-SEC-12): a user is refused. The
// service must name the owner, in the body or with X-User-ID (AM-SEC-13), and
// the source by its id (AM-DATA-07, AM-DATA-11). A service that names no one
// is refused rather than given user 0's records, which no one sees, no limit
// holds and the next orphan sweep deletes.

package handlers

import (
	"net/http"
)

const (
	// importOnly is the refusal of a user who creates a record.
	importOnly = "Records are created only by importing a source"
	// sourceRequired is the refusal of a create that names no source id.
	sourceRequired = "sourceId is required: every record belongs to a source"
	// ownerRequired is the refusal of a service that names no owner.
	ownerRequired = "userId is required: a service must name the owner"
	// unknownOwner is the refusal of an owner SystemManager does not know
	// (AM-DATA-33, AM-DATA-35).
	unknownOwner = "userId names no user"
)

// creatorOf returns whom a record the request creates belongs to: for a
// service, bodyUserID, else the user of X-User-ID. A user is refused with 403;
// a service that names no user above 0 with 400. It returns false when it
// answered.
func creatorOf(w http.ResponseWriter, r *http.Request, bodyUserID *int64) (int64, bool) {
	if !IsInternalService(r) { // the middleware's verdict, not the header's presence
		respondError(w, http.StatusForbidden, importOnly)
		return 0, false
	}
	if bodyUserID != nil && *bodyUserID > 0 {
		return *bodyUserID, true
	}
	if bodyUserID == nil {
		if userID, ok := GetUserID(r); ok && userID > 0 {
			return userID, true
		}
	}
	respondError(w, http.StatusBadRequest, ownerRequired)
	return 0, false
}
