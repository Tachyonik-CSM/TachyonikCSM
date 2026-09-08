// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// HTML → text, for the pages this product retrieves rather than parses: an
// organisation's own homepage, read so an AI can pick the postal address out of
// it. What a model needs is the words a visitor sees; markup, scripts and
// styling are noise it would be charged for by the token.
//
// The anchors come back with the text because the address is usually not on the
// homepage at all — German sites carry it on the legally required Impressum —
// so the caller needs somewhere to go next.

package textextract

import (
	"io"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// MaxHTMLTextBytes caps the text one page may yield. A page is fetched to be
// put in a prompt, and a prompt is paid for by the token; a runaway page must
// cost a truncated answer, not an unbounded bill.
const MaxHTMLTextBytes = 40 << 10 // 40 KiB

// Link is an anchor found in a page, with its href resolved against the page's
// own URL so the caller does not have to.
type Link struct {
	URL  string
	Text string
}

// HTMLPage is what one retrieved page yields.
type HTMLPage struct {
	Text string
	// Truncated reports that Text hit MaxHTMLTextBytes and the rest was
	// dropped, so a caller can say so rather than quietly answering from half
	// a page.
	Truncated bool
	Links     []Link
	// Images the page offers as its own mark, best candidate first. Empty for
	// a page that declares none — most pages declare at least a favicon.
	Images []ImageCandidate
}

// ImageCandidate is one image a page presents as identifying itself.
//
// Collected because a logo cannot be read out of text: it is declared in markup
// the text extraction deliberately drops (head, and img attributes). Ranked
// rather than filtered, so a caller can prefer an apple-touch-icon — typically
// a square PNG made for exactly this purpose — over a social banner that is the
// wrong shape for a logo tile.
type ImageCandidate struct {
	URL string
	// Where it was declared: "apple-touch-icon", "icon", "og:image" or "img".
	Source string
	// The img element's alt text, where there was one.
	Alt string
	// Lower sorts first. A property of where it was found, not of the image.
	Rank int
}

// dropped elements never contribute visible text; their contents would arrive
// as a wall of code.
var droppedElements = map[string]bool{
	"script": true, "style": true, "noscript": true,
	"svg": true, "template": true, "head": true,
}

// breakingElements end a line of text. Without them "Hauptstraße 1" and "28195
// Bremen" from two adjacent divs run together into one unreadable line, which
// is exactly the kind of thing that makes an address unparseable.
var breakingElements = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "td": true, "th": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"section": true, "article": true, "header": true, "footer": true,
	"address": true, "table": true, "ul": true, "ol": true, "blockquote": true,
}

// FromHTML renders a page as the text a reader would see, plus its links.
//
// Malformed markup is not an error: html.Parse repairs what it can, which is
// the right behaviour for pages found in the wild rather than authored here.
func FromHTML(r io.Reader, pageURL string) (HTMLPage, error) {
	doc, err := html.Parse(io.LimitReader(r, MaxInputBytes))
	if err != nil {
		return HTMLPage{}, err
	}

	base, _ := url.Parse(pageURL)

	var out strings.Builder
	page := HTMLPage{}

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if page.Truncated {
			return
		}
		switch n.Type {
		case html.ElementNode:
			if droppedElements[n.Data] {
				return
			}
			if n.Data == "a" {
				if link, ok := anchor(n, base); ok {
					page.Links = append(page.Links, link)
				}
			}
			if breakingElements[n.Data] {
				out.WriteByte('\n')
			}
		case html.TextNode:
			text := strings.TrimSpace(collapseSpace(n.Data))
			if text != "" {
				if out.Len()+len(text)+1 > MaxHTMLTextBytes {
					page.Truncated = true
					return
				}
				out.WriteString(text)
				out.WriteByte(' ')
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}

		if n.Type == html.ElementNode && breakingElements[n.Data] {
			out.WriteByte('\n')
		}
	}
	walk(doc)

	page.Text = tidyLines(out.String())
	page.Images = collectImages(doc, base)
	return page, nil
}

// anchor resolves one <a> into a Link, dropping the ones that go nowhere
// useful: fragments, javascript:, mailto:, and empty hrefs.
func anchor(n *html.Node, base *url.URL) (Link, bool) {
	var href string
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "href") {
			href = strings.TrimSpace(a.Val)
			break
		}
	}
	if href == "" || strings.HasPrefix(href, "#") {
		return Link{}, false
	}
	ref, err := url.Parse(href)
	if err != nil {
		return Link{}, false
	}
	if base != nil {
		ref = base.ResolveReference(ref)
	}
	if ref.Scheme != "http" && ref.Scheme != "https" {
		return Link{}, false
	}
	return Link{URL: ref.String(), Text: strings.TrimSpace(collapseSpace(nodeText(n)))}, true
}

// nodeText is the visible text of one node, used for a link's label.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// collapseSpace turns every run of whitespace — including the non-breaking
// spaces that pad addresses on real pages — into one plain space.
func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || r == ' ' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// tidyLines trims each line and drops runs of blank ones, so the text reads as
// paragraphs rather than as the whitespace of the original layout.
func tidyLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// imprintWords are the link labels and paths that lead to the page carrying a
// company's postal address. German first, because the Impressum is a legal
// requirement there and is where the address reliably is.
var imprintWords = []string{
	"impressum", "imprint", "kontakt", "contact", "legal-notice", "legal_notice",
	"legalnotice", "about-us", "ueber-uns", "über-uns",
}

// FindImprintLink picks the one link most likely to carry the postal address,
// restricted to the same host as the page it was found on.
//
// Same host on purpose: following a link off-site would have us fetching an
// address from somewhere the organisation does not control, and would turn one
// bounded request into a crawl.
func FindImprintLink(page HTMLPage, pageURL string) (string, bool) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return "", false
	}
	for _, word := range imprintWords {
		for _, link := range page.Links {
			u, err := url.Parse(link.URL)
			if err != nil || !strings.EqualFold(u.Host, base.Host) {
				continue
			}
			if u.String() == base.String() {
				continue // the page we already have
			}
			haystack := strings.ToLower(link.Text + " " + u.Path)
			if strings.Contains(haystack, word) {
				return u.String(), true
			}
		}
	}
	return "", false
}

// maxImageCandidates bounds what one page can offer. A caller fetches these,
// so an unbounded list would be an unbounded number of requests.
const maxImageCandidates = 8

// collectImages walks the document for images that identify the site.
//
// A separate pass from the text walk on purpose: that one returns early for
// <head>, which is exactly where the icons and og:image are declared, and
// teaching it to descend selectively would tangle two unrelated jobs.
func collectImages(doc *html.Node, base *url.URL) []ImageCandidate {
	var found []ImageCandidate
	seen := map[string]bool{}

	add := func(raw, source, alt string, rank int) {
		resolved, ok := resolveURL(raw, base)
		if !ok || seen[resolved] {
			return
		}
		seen[resolved] = true
		found = append(found, ImageCandidate{URL: resolved, Source: source, Alt: alt, Rank: rank})
	}

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "link":
				rel := strings.ToLower(attr(n, "rel"))
				switch {
				case strings.Contains(rel, "apple-touch-icon"):
					// Square, made to be shown on its own, and almost always a
					// PNG: the best logo a page routinely declares.
					add(attr(n, "href"), "apple-touch-icon", "", 1)
				case strings.Contains(rel, "icon"):
					add(attr(n, "href"), "icon", "", 3)
				}
			case "meta":
				property := strings.ToLower(attr(n, "property") + " " + attr(n, "name"))
				if strings.Contains(property, "og:image") {
					// A social card: usually 1200x630 and not a logo at all, so
					// it ranks last and is only worth offering as a fallback.
					add(attr(n, "content"), "og:image", "", 4)
				}
			case "img":
				alt := attr(n, "alt")
				haystack := strings.ToLower(alt + " " + attr(n, "class") + " " + attr(n, "id") + " " + attr(n, "src"))
				if strings.Contains(haystack, "logo") {
					add(attr(n, "src"), "img", strings.TrimSpace(alt), 2)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	sort.SliceStable(found, func(i, j int) bool { return found[i].Rank < found[j].Rank })
	if len(found) > maxImageCandidates {
		found = found[:maxImageCandidates]
	}
	return found
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

// resolveURL turns a possibly relative reference into an absolute http(s) URL.
// Anything else — data:, javascript:, a malformed reference — is not something
// a caller can fetch, so it is not offered.
func resolveURL(raw string, base *url.URL) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	resolved := ref
	if base != nil {
		resolved = base.ResolveReference(ref)
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	return resolved.String(), true
}
