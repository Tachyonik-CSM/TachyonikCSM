// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Whom a created record belongs to (AM-SEC-12, AM-SEC-13): a user's own
// records are theirs; a service must name the owner, in the body or with
// X-User-ID. A service that names no one is refused rather than given user
// 0's records, which no one sees, no limit holds and the next orphan sweep
// deletes.

package handlers

import (
	"net/http"
)

const (
	// ownerRequired is the refusal of a service that names no owner.
	ownerRequired = "userId is required: a service must name the owner"
	// unknownOwner is the refusal of an owner SystemManager does not know
	// (AM-DATA-33, AM-DATA-35).
	unknownOwner = "userId names no user"
)

// creatorOf returns whom a record the request creates belongs to. For a user
// it is the user of the token. For a service it is bodyUserID, else the user
// of X-User-ID; when neither names a user above 0 it answers 400 and returns
// false.
func creatorOf(w http.ResponseWriter, r *http.Request, bodyUserID *int64) (int64, bool) {
	if !IsInternalService(r) { // the middleware's verdict, not the header's presence
		userID, ok := GetUserID(r)
		if !ok {
			respondError(w, http.StatusUnauthorized, "User ID not found in context")
			return 0, false
		}
		return userID, true
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
