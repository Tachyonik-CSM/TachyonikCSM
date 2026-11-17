// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The fields the asset, vulnerability and detection lists may be sorted by
// (AM-API-16), each with the SQL expression it sorts by. A list's sort
// parameter is looked up here and never written into a query itself. Times
// sort through julianday(), since they are stored in more than one layout;
// names case-insensitively.

package database

import "tachyonik/lib/listquery"

// The tie-break after a sort field: newest first, as the lists are by default.
const listTieBreak = "julianday(created_at) DESC, id DESC"

// AssetSortFields are the fields assets.list sorts by.
var AssetSortFields = []listquery.Field{
	{Name: "severity", Column: "severity"},
	{Name: "score", Column: "score"},
	{Name: "name", Column: "name COLLATE NOCASE", Text: true},
	{Name: "type", Column: "type COLLATE NOCASE", Text: true},
	{Name: "createdAt", Column: "julianday(created_at)"},
	{Name: "modifiedAt", Column: "julianday(modified_at)"},
	{Name: "lastSeen", Column: "julianday(last_seen)"},
	{Name: "lastScanned", Column: "julianday(last_scanned)"},
}

// VulnerabilitySortFields are the fields vulnerabilities.list sorts by.
var VulnerabilitySortFields = []listquery.Field{
	{Name: "severity", Column: "severity"},
	{Name: "name", Column: "name COLLATE NOCASE", Text: true},
	{Name: "host", Column: "host COLLATE NOCASE", Text: true},
	{Name: "createdAt", Column: "julianday(created_at)"},
	{Name: "modifiedAt", Column: "julianday(modified_at)"},
	{Name: "lastSeen", Column: "julianday(last_seen)"},
}

// DetectionSortFields are the fields detections.list sorts by.
var DetectionSortFields = []listquery.Field{
	{Name: "name", Column: "name COLLATE NOCASE", Text: true},
	{Name: "host", Column: "host COLLATE NOCASE", Text: true},
	{Name: "createdAt", Column: "julianday(created_at)"},
	{Name: "modifiedAt", Column: "julianday(modified_at)"},
	{Name: "lastSeen", Column: "julianday(last_seen)"},
}
