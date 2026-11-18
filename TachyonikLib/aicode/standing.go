// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Which generation requests a starting daemon may clear as left behind.
//
// A daemon handles a rule's generation request when AIManager announces the
// rule's change. A request still standing when the daemon has loaded its rules
// for the first time was made before it started — cut off by a stop, or asked
// for while it was not running — and no announcement will come for it any
// more. That first load is not always at startup: when AIManager could not be
// reached then, it happens later, with the rule watcher already running and a
// new request possibly being handled. Only a request made before the daemon
// started is therefore cleared.

package aicode

import "time"

// RequestedBefore reports whether a generation request stamped requestedAt
// (AIManager's generateRequestedAt, RFC 3339) was made before t. No request
// is never before t. A stamp that cannot be read counts as before, so that it
// is cleared rather than left standing for good.
func RequestedBefore(requestedAt *string, t time.Time) bool {
	if requestedAt == nil {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, *requestedAt)
	if err != nil {
		return true
	}
	return at.Before(t)
}
