// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins what a holder answers from a loaded catalogue: the operations of its
// profile only, rendered for the AI with what a write takes and leaving out
// what the bridge sets itself (PA-RT-05); which operations are gone
// (PA-RT-06); and which messages invalidate a routine's reads (PA-RT-07).

package apiholder

import (
	"strings"
	"testing"

	"tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
)

func document() string {
	var ex strings.Builder
	for _, sc := range apicatalogue.Scenarios {
		ex.WriteString("                " + sc + ": { value: { n: 1 } }\n")
	}
	return `openapi: 3.1.0
info: { title: T, version: "1" }
paths:
  /api/things:
    get:
      operationId: things.list
      summary: The things
      x-bridge: read
      x-invalidated-by: [THING_CREATED, THING_DELETED]
      parameters:
        - { name: X-User-ID, in: header, x-bridge-bound: user, schema: { type: integer } }
        - { name: kind, in: query, description: Only this kind., schema: { type: string } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { type: object, properties: { n: { type: integer, description: How many. } } }
              examples:
` + ex.String() + `
    post:
      operationId: things.create
      summary: Make a thing
      x-bridge: write
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: { type: string, description: What it is called. }
                userId: { type: integer, x-bridge-bound: user }
      responses:
        "201":
          description: ok
          content:
            application/json:
              schema: { type: object, properties: { id: { type: integer } } }
`
}

func holder(t *testing.T, profile apibridge.Profile) *Holder {
	t.Helper()
	h := New(map[string]Endpoint{"assetmanager": {BaseURL: "http://127.0.0.1:1"}}, profile, apibridge.Options{})
	if err := h.Load("fp", []Document{{Module: "assetmanager", Document: document()}, {Module: "unknown", Document: "x"}}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRenderFollowsTheProfile(t *testing.T) {
	read := holder(t, apibridge.ReadOnly).Render()
	if !strings.Contains(read, "api.things.list({kind}) — The things") || !strings.Contains(read, "n: integer — How many.") {
		t.Errorf("read profile:\n%s", read)
	}
	if strings.Contains(read, "things.create") {
		t.Error("the read profile must not offer writes")
	}
	rw := holder(t, apibridge.ReadWrite).Render()
	for _, want := range []string{"api.things.create({body}) — Make a thing", "CHANGES DATA", "name: string, required — What it is called."} {
		if !strings.Contains(rw, want) {
			t.Errorf("read-write profile lacks %q:\n%s", want, rw)
		}
	}
	if strings.Contains(rw, "userId") {
		t.Error("a field the bridge sets must not be offered to the routine")
	}
}

func TestMissingAndTriggers(t *testing.T) {
	h := holder(t, apibridge.ReadOnly)
	if !h.Loaded() {
		t.Fatal("not loaded")
	}
	if got := h.Missing([]string{"things.list", "things.gone", "things.create"}); strings.Join(got, ",") != "things.create,things.gone" {
		t.Errorf("missing = %v (a write is missing from the read profile)", got)
	}
	set := map[string]bool{}
	h.InvalidatedBy([]string{"things.list"}, set)
	if !set["THING_CREATED"] || !set["THING_DELETED"] || len(set) != 2 {
		t.Errorf("triggers = %v", set)
	}
	if _, fp := h.Bridge(); fp != "fp" {
		t.Errorf("fingerprint = %q", fp)
	}
}

// A manager that has not registered, and one registered but without a URL
// here, are named; a registered and reachable one is not. The unreachable
// one's operations are not offered.
func TestGapsNameWhatRoutinesLack(t *testing.T) {
	h := New(map[string]Endpoint{
		"assetmanager":  {BaseURL: "http://127.0.0.1:1"},
		"systemmanager": {},
	}, apibridge.ReadOnly, apibridge.Options{})
	sys := strings.ReplaceAll(document(), "things.", "settings.")
	if err := h.Load("fp", []Document{{Module: "assetmanager", Document: document()}, {Module: "systemmanager", Document: sys}}); err != nil {
		t.Fatal(err)
	}
	gaps := strings.Join(h.Gaps(), "\n")
	for _, want := range []string{
		"toolmanager has not registered its API document",
		"actionmanager has not registered",
		"systemmanager's operations are left out: no URL is configured for it here",
	} {
		if !strings.Contains(gaps, want) {
			t.Errorf("gaps lack %q:\n%s", want, gaps)
		}
	}
	if strings.Contains(gaps, "assetmanager") {
		t.Errorf("a registered, reachable manager is named:\n%s", gaps)
	}
	if strings.Contains(h.Render(), "settings.") {
		t.Error("an unreachable manager's operations are offered to the AI")
	}
}
