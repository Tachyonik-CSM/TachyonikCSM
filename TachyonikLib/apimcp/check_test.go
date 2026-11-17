// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins Check (PA-MCP-03): a server offering exactly the marked operations, each
// callable by the user, passes; an extra or missing tool, or an operation the
// user is refused or that has no route, is reported.

package apimcp

import (
	"net/http"
	"strings"
	"testing"

	"tachyonik/lib/apicatalogue"
)

// mounted is a router with the things API and its MCP server built from
// served (which may differ from what Check is given).
func mounted(t *testing.T, served *apicatalogue.Document, things http.HandlerFunc) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/things/", things)
	s, err := New("things", "1", served, mux)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/api/mcp", s)
	return mux
}

func parsed(t *testing.T) *apicatalogue.Document {
	t.Helper()
	d, err := apicatalogue.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func answering(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"name":"n"}`))
	}
}

var session = http.Header{"Authorization": {"Bearer good"}}

func TestCheckPassesAServerThatOffersWhatIsMarked(t *testing.T) {
	d := parsed(t)
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusNotFound} {
		if problems := Check(mounted(t, d, answering(status)), session, d); len(problems) > 0 {
			t.Errorf("answering %d: %v", status, problems)
		}
	}
	if got := strings.Join(Marked(d), ","); got != "things.get" {
		t.Errorf("Marked = %s", got)
	}
}

func TestCheckReportsAToolNotMarked(t *testing.T) {
	served := parsed(t)
	for _, op := range served.Operations {
		if op.ID == "things.list" {
			op.MCP = apicatalogue.ExposeRead
		}
	}
	problems := Check(mounted(t, served, answering(http.StatusOK)), session, parsed(t))
	if len(problems) == 0 || !strings.Contains(problems[0], "things.list") {
		t.Errorf("problems = %v", problems)
	}
}

func TestCheckReportsAMarkedOperationTheUserCannotCall(t *testing.T) {
	d := parsed(t)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed} {
		problems := Check(mounted(t, d, answering(status)), session, d)
		if len(problems) != 1 || !strings.Contains(problems[0], "things.get cannot be called") {
			t.Errorf("answering %d: %v", status, problems)
		}
	}
	// No route at all: the mux's own 404.
	problems := Check(mounted(t, d, http.NotFound), session, d)
	if len(problems) != 1 {
		t.Errorf("no route: %v", problems)
	}
}

func TestCheckReportsAServerThatIsNotThere(t *testing.T) {
	if problems := Check(http.NotFoundHandler(), session, parsed(t)); len(problems) != 1 {
		t.Errorf("problems = %v", problems)
	}
}
