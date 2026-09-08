// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the images a page offers as its own mark: that the icons and
// og:image declared in <head> are found even though the text extraction drops
// that element wholesale, that they are ranked so a square touch icon beats a
// social banner nobody would want as a logo, that only references a caller can
// actually retrieve are offered, and that the list stays deduplicated and
// bounded — every entry becomes an outbound request.

package textextract

import (
	"strings"
	"testing"
)

const logoPage = `<!doctype html><html><head>
<link rel="shortcut icon" href="/favicon.png">
<link rel="apple-touch-icon" sizes="180x180" href="/touch.png">
<meta property="og:image" content="https://cdn.example/social-card.jpg">
<title>Acme</title></head><body>
<img src="/img/acme-logo.svg" alt="Acme logo" class="brand">
<img src="/img/hero.jpg" alt="A photo of the office">
<a href="/imprint">Imprint</a>
</body></html>`

// A logo is declared in markup the text extraction drops, so it has to be
// collected separately — and ranked, because the shapes differ wildly.
func TestImageCandidatesAreCollectedAndRanked(t *testing.T) {
	page, err := FromHTML(strings.NewReader(logoPage), "https://acme.example/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(page.Images) != 4 {
		t.Fatalf("expected 4 candidates, got %d: %+v", len(page.Images), page.Images)
	}

	// apple-touch-icon first: square, standalone, and made for this.
	if page.Images[0].Source != "apple-touch-icon" {
		t.Errorf("best candidate is %q, want apple-touch-icon", page.Images[0].Source)
	}
	if page.Images[0].URL != "https://acme.example/touch.png" {
		t.Errorf("relative href was not resolved: %q", page.Images[0].URL)
	}
	// og:image last: a social banner is the wrong shape for a logo tile.
	if page.Images[len(page.Images)-1].Source != "og:image" {
		t.Errorf("last candidate is %q, want og:image", page.Images[len(page.Images)-1].Source)
	}

	var img *ImageCandidate
	for i := range page.Images {
		if page.Images[i].Source == "img" {
			img = &page.Images[i]
		}
	}
	if img == nil || img.Alt != "Acme logo" {
		t.Errorf("the logo img was not collected with its alt text: %+v", img)
	}
	for _, c := range page.Images {
		if strings.Contains(c.URL, "hero.jpg") {
			t.Error("a photo with no logo marking was offered as a candidate")
		}
	}
}

// Only fetchable references are offered: a data: or javascript: reference is
// not something the caller can retrieve.
func TestImageCandidatesSkipUnfetchableReferences(t *testing.T) {
	page, err := FromHTML(strings.NewReader(
		`<html><head><link rel="icon" href="data:image/png;base64,AAA">`+
			`<link rel="apple-touch-icon" href="javascript:void(0)"></head><body>`+
			`<img src="/real-logo.png" alt="logo"></body></html>`), "https://acme.example/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(page.Images) != 1 || page.Images[0].URL != "https://acme.example/real-logo.png" {
		t.Errorf("expected only the fetchable candidate, got %+v", page.Images)
	}
}

// The same URL declared twice is one candidate, and the list is bounded.
func TestImageCandidatesAreDedupedAndBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("<html><head>")
	for i := 0; i < 20; i++ {
		b.WriteString(`<link rel="icon" href="/icon` + string(rune('a'+i)) + `.png">`)
	}
	b.WriteString(`<link rel="icon" href="/icona.png">`) // duplicate
	b.WriteString("</head><body></body></html>")

	page, err := FromHTML(strings.NewReader(b.String()), "https://acme.example/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(page.Images) != maxImageCandidates {
		t.Errorf("expected the list capped at %d, got %d", maxImageCandidates, len(page.Images))
	}
	seen := map[string]bool{}
	for _, c := range page.Images {
		if seen[c.URL] {
			t.Errorf("duplicate candidate %s", c.URL)
		}
		seen[c.URL] = true
	}
}
