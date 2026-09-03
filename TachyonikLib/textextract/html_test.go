// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for HTML → text: that scripts and styling never reach the prompt, that
// block elements keep an address on separate lines, that links come back
// resolved, and that the imprint link is found the way a German site publishes
// it.

package textextract

import (
	"strings"
	"testing"
)

const samplePage = `<!doctype html>
<html><head>
  <title>Acme</title>
  <style>.a{color:red}</style>
  <script>var secret = "do not put me in a prompt";</script>
</head>
<body>
  <header><a href="/">Home</a> <a href="/impressum">Impressum</a></header>
  <h1>Acme GmbH</h1>
  <div>Hauptstra&szlig;e 1</div>
  <div>28195 Bremen</div>
  <div>Germany</div>
  <a href="#top">Back to top</a>
  <a href="mailto:info@acme.example">Mail us</a>
  <a href="https://elsewhere.example/impressum">Someone else</a>
</body></html>`

func TestFromHTMLDropsCodeAndKeepsText(t *testing.T) {
	page, err := FromHTML(strings.NewReader(samplePage), "https://acme.example/")
	if err != nil {
		t.Fatalf("FromHTML: %v", err)
	}

	if strings.Contains(page.Text, "do not put me in a prompt") {
		t.Error("script contents reached the extracted text")
	}
	if strings.Contains(page.Text, "color:red") {
		t.Error("stylesheet contents reached the extracted text")
	}
	if !strings.Contains(page.Text, "Acme GmbH") {
		t.Errorf("heading missing from text:\n%s", page.Text)
	}
	// The entity has to be decoded, or the address is unreadable.
	if !strings.Contains(page.Text, "Hauptstraße 1") {
		t.Errorf("street missing or entity not decoded:\n%s", page.Text)
	}

	// Each part of the address on its own line: run together, "Hauptstraße 1
	// 28195 Bremen Germany" is far harder to read back as fields.
	for _, want := range []string{"Hauptstraße 1", "28195 Bremen", "Germany"} {
		found := false
		for _, line := range strings.Split(page.Text, "\n") {
			if strings.TrimSpace(line) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q is not on a line of its own:\n%s", want, page.Text)
		}
	}
}

func TestFromHTMLResolvesAndFiltersLinks(t *testing.T) {
	page, err := FromHTML(strings.NewReader(samplePage), "https://acme.example/")
	if err != nil {
		t.Fatalf("FromHTML: %v", err)
	}

	var got []string
	for _, l := range page.Links {
		got = append(got, l.URL)
	}
	joined := strings.Join(got, " ")

	if !strings.Contains(joined, "https://acme.example/impressum") {
		t.Errorf("relative href not resolved against the page URL: %v", got)
	}
	if strings.Contains(joined, "mailto:") {
		t.Errorf("mailto: link should not be offered as somewhere to fetch: %v", got)
	}
	for _, l := range got {
		if strings.HasPrefix(l, "#") || strings.HasSuffix(l, "#top") {
			t.Errorf("fragment link should have been dropped: %v", got)
		}
	}
}

func TestFindImprintLink(t *testing.T) {
	page, err := FromHTML(strings.NewReader(samplePage), "https://acme.example/")
	if err != nil {
		t.Fatalf("FromHTML: %v", err)
	}

	link, ok := FindImprintLink(page, "https://acme.example/")
	if !ok {
		t.Fatal("no imprint link found on a page that has one")
	}
	if link != "https://acme.example/impressum" {
		t.Errorf("imprint link = %q, want the same-site one", link)
	}

	// Another organisation's imprint is not ours to read.
	offsite := `<html><body><a href="https://elsewhere.example/impressum">Impressum</a></body></html>`
	other, err := FromHTML(strings.NewReader(offsite), "https://acme.example/")
	if err != nil {
		t.Fatalf("FromHTML: %v", err)
	}
	if link, ok := FindImprintLink(other, "https://acme.example/"); ok {
		t.Errorf("followed an off-site imprint link: %q", link)
	}
}

// A page with no imprint link is normal, not an error.
func TestFindImprintLinkAbsent(t *testing.T) {
	page, err := FromHTML(strings.NewReader(`<html><body><p>Nothing here</p></body></html>`), "https://acme.example/")
	if err != nil {
		t.Fatalf("FromHTML: %v", err)
	}
	if _, ok := FindImprintLink(page, "https://acme.example/"); ok {
		t.Error("reported an imprint link on a page that has none")
	}
}

// The text a page may contribute is bounded: it is going into a prompt.
func TestFromHTMLTruncatesHugePages(t *testing.T) {
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := 0; i < 20000; i++ {
		b.WriteString("<p>filler filler filler filler</p>")
	}
	b.WriteString("</body></html>")

	page, err := FromHTML(strings.NewReader(b.String()), "https://acme.example/")
	if err != nil {
		t.Fatalf("FromHTML: %v", err)
	}
	if !page.Truncated {
		t.Error("an oversized page must report that it was truncated")
	}
	if len(page.Text) > MaxHTMLTextBytes {
		t.Errorf("text is %d bytes, over the %d cap", len(page.Text), MaxHTMLTextBytes)
	}
}
