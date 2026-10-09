// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests that a page's own logo is offered ahead of other organisations' logos
// shown on it.
//
// The page below has the structure of the one that found the problem: the
// organisation's logo is an inline <svg> in the header's home link, its favicon
// is an SVG, and the body carries a slider of customer reference logos — every
// one an <img> with "logo" in its file name and alt text. The old ranking
// offered the customers' logos first, dropped the favicon off the end of the
// list, and never saw the inline SVG at all.

package textextract

import (
	"net/url"
	"strings"
	"testing"
)

const referencesPage = `<!doctype html><html><head>
<link rel="icon" href="/themes/acme/favicon.svg">
</head><body>
<header id="header"><div id="block-acme-branding" class="block block--system-branding-block">
  <a href="/" title="Startseite" rel="home"><svg width="120" height="46" viewBox="0 0 120 46"><path fill="#3479AE" d="M0 0h10v10H0z"/></svg></a>
</div></header>
<main>
  <div class="paragraph paragraph--selected-partners"><div class="swiper-container"><div class="swiper-wrapper">
    <div class="swiper-slide"><img src="/files/styles/acme_logo_medium/adidas-logo.png" alt="Adidas Logo"></div>
    <div class="swiper-slide"><img src="/files/styles/acme_logo_medium/amnesty-logo.png" alt="Amnesty International Logo"></div>
    <div class="swiper-slide"><img src="/files/styles/acme_logo_medium/storck-logo.png" alt="Storck Logo"></div>
    <div class="swiper-slide"><img src="/files/styles/acme_logo_medium/solarlux-logo.png" alt="Solarlux Logo"></div>
    <div class="swiper-slide"><img src="/files/styles/acme_logo_medium/ukm-logo.png" alt="UKM Logo"></div>
    <div class="swiper-slide"><img src="/files/styles/acme_logo_medium/noz-logo.png" alt="NOZ Logo"></div>
    <div class="swiper-slide"><img src="/files/styles/acme_logo_medium/winkhaus-logo.png" alt="Winkhaus Logo"></div>
    <div class="swiper-slide"><img src="/files/styles/acme_logo_medium/hellmann-logo.png" alt="Hellmann Logo"></div>
  </div></div></div>
</main></body></html>`

func TestTheSitesOwnLogoComesBeforeItsCustomersLogos(t *testing.T) {
	page, err := FromHTML(strings.NewReader(referencesPage), "https://acme-online.example/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(page.Images) < 2 {
		t.Fatalf("got %d candidates: %+v", len(page.Images), page.Images)
	}

	first := page.Images[0]
	if first.Source != "inline-svg" || !strings.Contains(first.SVG, "<svg") {
		t.Fatalf("first candidate is %+v, want the inline SVG from the home link", first)
	}
	if !strings.Contains(first.SVG, `xmlns="http://www.w3.org/2000/svg"`) {
		t.Error("the inline SVG lacks the xmlns a data: URI needs to render")
	}

	if page.Images[1].Source != "icon" {
		t.Errorf("second candidate is %q, want the site's favicon ahead of any customer logo", page.Images[1].Source)
	}

	customers := 0
	for _, c := range page.Images {
		if c.Source == "img" {
			customers++
			if c.Rank != rankForeignLogo {
				t.Errorf("customer logo %q ranked %d, want the foreign rank", c.Alt, c.Rank)
			}
		}
	}
	if customers > maxLogoImages {
		t.Errorf("%d customer logos offered, want at most %d", customers, maxLogoImages)
	}
}

// A logo wall without telling class names is still a wall: three or more
// logo images under one close ancestor.
func TestALogoWallWithoutClassNamesRanksLast(t *testing.T) {
	const wall = `<html><head><link rel="icon" href="/favicon.png"></head><body><section><div>
	<img src="/a-logo.png" alt="A"><img src="/b-logo.png" alt="B"><img src="/c-logo.png" alt="C">
	</div></section><footer><img src="/brandco-logo.png" alt="BrandCo"></footer></body></html>`
	page, err := FromHTML(strings.NewReader(wall), "https://brandco.example/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var order []string
	for _, c := range page.Images {
		order = append(order, c.URL)
	}
	if len(page.Images) == 0 || !strings.HasSuffix(page.Images[0].URL, "/brandco-logo.png") {
		t.Fatalf("order %v: the logo naming the site should lead", order)
	}
	if page.Images[0].Rank != rankOwnLogo {
		t.Errorf("own logo ranked %d, want %d", page.Images[0].Rank, rankOwnLogo)
	}
	for _, c := range page.Images {
		if strings.Contains(c.URL, "/a-logo.png") && c.Rank != rankForeignLogo {
			t.Errorf("a logo in a wall ranked %d, want the foreign rank", c.Rank)
		}
	}
}

func TestOwnerTokens(t *testing.T) {
	for host, want := range map[string]string{
		"https://pco-online.de/":    "pco",
		"https://www.acme.example/": "acme",
		"https://my-web-shop.de/":   "",
	} {
		base, _ := parseURL(host)
		got := strings.Join(ownerTokens(base), ",")
		if got != want {
			t.Errorf("%s: tokens %q, want %q", host, got, want)
		}
	}
}

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }
