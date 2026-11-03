// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Helpers every query file shares: the visibility clause that scopes a query
// to the users a shared workspace makes visible, and the reader of a source
// reference — "filename (ID: N)", the form vulnerabilities and detections store
// as their source and the by-source endpoints are called with.

package database

import (
	"regexp"
	"strconv"
	"strings"
)

// sourceRefRegex matches the "(ID: N)" at the end of a source reference.
var sourceRefRegex = regexp.MustCompile(`\(ID:\s*(\d+)\)$`)

// ParseSourceRef splits a source reference "filename (ID: N)" into the
// ResourceManager source id and the filename. Without the "(ID: N)" suffix it
// returns (0, raw): no id, and the whole string as the name.
func ParseSourceRef(raw string) (id int64, label string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ""
	}
	m := sourceRefRegex.FindStringSubmatchIndex(raw)
	if m == nil {
		return 0, raw
	}
	id, err := strconv.ParseInt(raw[m[2]:m[3]], 10, 64)
	if err != nil {
		return 0, raw
	}
	return id, strings.TrimSpace(raw[:m[0]])
}

// buildUserIDsClause builds a "user_id IN (?, ?, ...)" SQL clause and args from a slice of user IDs.
func buildUserIDsClause(userIDs []int64) (string, []interface{}) {
	if len(userIDs) == 1 {
		return "user_id = ?", []interface{}{userIDs[0]}
	}
	placeholders := make([]string, len(userIDs))
	args := make([]interface{}, len(userIDs))
	for i, id := range userIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	return "user_id IN (" + strings.Join(placeholders, ",") + ")", args
}
