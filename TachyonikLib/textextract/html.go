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
	// URL of the image. Empty for an inline SVG, which has no address.
	URL string
	// Where it was declared: "branding" (an image in the site's own home link
	// or branding block), "inline-svg" (an <svg> there, carried in SVG),
	// "apple-touch-icon", "icon", "og:image" or "img".
	Source string
	// SVG is the markup of an inline <svg> candidate, ready to be wrapped in a
	// data: URI. Empty for every candidate that has a URL.
	SVG string
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

// maxLogoImages bounds the <img> candidates that are not the site's branding.
// Without it a page with a wall of customer logos filled the whole list and
// pushed out the favicon and touch icon the site declares for itself.
const maxLogoImages = 3

// maxBrandingCandidates bounds the images and inline SVGs taken from the
// site's home link or branding block — usually exactly one.
const maxBrandingCandidates = 2

// maxInlineSVGBytes bounds one inline SVG. A logo is a few kilobytes; a page
// that inlines an illustration is not offering a logo.
const maxInlineSVGBytes = 64 << 10

// Candidate ranks; lower sorts first. Properties of where an image was found,
// not of the image.
const (
	rankBranding    = 0 // in the site's own home link or branding block
	rankTouchIcon   = 1 // apple-touch-icon: square, made to stand alone
	rankOwnLogo     = 2 // a "logo" img naming the site itself
	rankLogo        = 3 // any other "logo" img
	rankIcon        = 4 // favicon
	rankSocialImage = 5 // og:image: usually a 1200x630 banner, not a logo
	rankForeignLogo = 6 // a logo shown in a references/partners block or a logo wall
)

// brandingWords mark an element as the site's own branding block when they
// appear in its id or class.
var brandingWords = []string{"branding", "brand", "site-logo", "sitelogo", "header-logo", "navbar-brand"}

// foreignWords mark a container of other organisations' logos — references,
// customers, partners — or a slider, which is where such walls live.
var foreignWords = []string{
	"reference", "referenz", "customer", "kunde", "client", "partner", "sponsor",
	"slider", "swiper", "carousel", "marquee", "testimonial",
}

// ownerStopWords are domain parts too common to identify an organisation.
var ownerStopWords = map[string]bool{
	"www": true, "online": true, "web": true, "shop": true, "home": true,
	"info": true, "net": true, "the": true, "gmbh": true, "group": true, "site": true,
}

// collectImages walks the document for images that identify the site.
//
// A separate pass from the text walk on purpose: that one returns early for
// <head>, which is exactly where the icons and og:image are declared, and
// teaching it to descend selectively would tangle two unrelated jobs.
//
// The order matters more than the set. What the site marks as its own comes
// first: an image or inline SVG in its home link or branding block, then the
// icons it declares. "logo" imgs follow, those naming the site ahead of the
// rest, and a logo shown among others' — a customer reference wall, a partner
// slider — comes last. Every one of those walls is full of images called
// "logo", and before this ordering they were offered ahead of the site's own.
func collectImages(doc *html.Node, base *url.URL) []ImageCandidate {
	var found []ImageCandidate
	seen := map[string]bool{}
	owner := ownerTokens(base)

	add := func(raw, source, alt string, rank int) {
		resolved, ok := resolveURL(raw, base)
		if !ok || seen[resolved] {
			return
		}
		seen[resolved] = true
		found = append(found, ImageCandidate{URL: resolved, Source: source, Alt: alt, Rank: rank})
	}

	// "logo" imgs outside any branding block, resolved after the walk: whether
	// one stands in a logo wall depends on its siblings.
	type logoImg struct {
		node *html.Node
		alt  string
		src  string
	}
	var logoImgs []logoImg
	branding := 0

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "link":
				rel := strings.ToLower(attr(n, "rel"))
				switch {
				case strings.Contains(rel, "apple-touch-icon"):
					add(attr(n, "href"), "apple-touch-icon", "", rankTouchIcon)
				case strings.Contains(rel, "icon"):
					add(attr(n, "href"), "icon", "", rankIcon)
				}
			case "meta":
				property := strings.ToLower(attr(n, "property") + " " + attr(n, "name"))
				if strings.Contains(property, "og:image") {
					add(attr(n, "content"), "og:image", "", rankSocialImage)
				}
			case "img":
				alt := strings.TrimSpace(attr(n, "alt"))
				if branding < maxBrandingCandidates && inBranding(n, base) && !inForeignBlock(n) {
					before := len(found)
					add(attr(n, "src"), "branding", alt, rankBranding)
					if len(found) > before {
						branding++
					}
					break
				}
				haystack := strings.ToLower(alt + " " + attr(n, "class") + " " + attr(n, "id") + " " + attr(n, "src"))
				if strings.Contains(haystack, "logo") {
					logoImgs = append(logoImgs, logoImg{node: n, alt: alt, src: attr(n, "src")})
				}
			case "svg":
				if branding < maxBrandingCandidates && inBranding(n, base) && !inForeignBlock(n) {
					if markup, ok := renderSVG(n); ok && !seen[markup] {
						seen[markup] = true
						found = append(found, ImageCandidate{Source: "inline-svg", SVG: markup, Rank: rankBranding})
						branding++
					}
				}
				return // an svg's children are drawing, not images
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	walls := logoWalls(func() []*html.Node {
		nodes := make([]*html.Node, len(logoImgs))
		for i, l := range logoImgs {
			nodes[i] = l.node
		}
		return nodes
	}())
	var logos []ImageCandidate
	for _, l := range logoImgs {
		rank := rankLogo
		switch {
		case inForeignBlock(l.node) || walls[l.node]:
			rank = rankForeignLogo
		case namesOwner(strings.ToLower(l.alt+" "+l.src), owner):
			rank = rankOwnLogo
		}
		resolved, ok := resolveURL(l.src, base)
		if !ok || seen[resolved] {
			continue
		}
		seen[resolved] = true
		logos = append(logos, ImageCandidate{URL: resolved, Source: "img", Alt: l.alt, Rank: rank})
	}
	sort.SliceStable(logos, func(i, j int) bool { return logos[i].Rank < logos[j].Rank })
	if len(logos) > maxLogoImages {
		logos = logos[:maxLogoImages]
	}
	found = append(found, logos...)

	sort.SliceStable(found, func(i, j int) bool { return found[i].Rank < found[j].Rank })
	if len(found) > maxImageCandidates {
		found = found[:maxImageCandidates]
	}
	return found
}

// inBranding reports whether n stands in the site's own home link or branding
// block: an <a rel="home">, a link to the site's root, or an element whose id
// or class names it as branding. A home link anywhere counts — a footer that
// repeats the logo repeats the site's own.
func inBranding(n *html.Node, base *url.URL) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		if p.Data == "a" {
			if strings.Contains(strings.ToLower(attr(p, "rel")), "home") || isRootLink(attr(p, "href"), base) {
				return true
			}
		}
		if containsAny(strings.ToLower(attr(p, "id")+" "+attr(p, "class")), brandingWords) {
			return true
		}
	}
	return false
}

// inForeignBlock reports whether n stands in a container of other
// organisations' logos.
func inForeignBlock(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && containsAny(strings.ToLower(attr(p, "id")+" "+attr(p, "class")), foreignWords) {
			return true
		}
	}
	return false
}

// logoWalls marks the logo images that share a close ancestor with two or more
// others — a grid of logos, which on a homepage is someone else's logos far
// more often than the site's own. Close means within four levels: far enough
// for the wrappers a grid item gets, near enough that the page body itself is
// not one ancestor every image shares.
func logoWalls(imgs []*html.Node) map[*html.Node]bool {
	const levels = 4
	// The page-level elements are an ancestor of every image on a shallow
	// page; counting them would make any three logos anywhere a "wall".
	container := func(p *html.Node) bool {
		return p.Type == html.ElementNode && !pageLevelElements[p.Data]
	}
	counts := map[*html.Node]int{}
	for _, n := range imgs {
		p := n.Parent
		for i := 0; i < levels && p != nil; i++ {
			if container(p) {
				counts[p]++
			}
			p = p.Parent
		}
	}
	walls := map[*html.Node]bool{}
	for _, n := range imgs {
		p := n.Parent
		for i := 0; i < levels && p != nil; i++ {
			if container(p) && counts[p] >= 3 {
				walls[n] = true
				break
			}
			p = p.Parent
		}
	}
	return walls
}

// pageLevelElements are never the container of a logo wall.
var pageLevelElements = map[string]bool{"html": true, "body": true, "main": true, "header": true, "footer": true}

// isRootLink reports whether href points at the site's own root.
func isRootLink(href string, base *url.URL) bool {
	resolved, ok := resolveURL(href, base)
	if !ok || base == nil {
		return false
	}
	u, err := url.Parse(resolved)
	if err != nil || !strings.EqualFold(strings.TrimPrefix(u.Host, "www."), strings.TrimPrefix(base.Host, "www.")) {
		return false
	}
	return u.Path == "" || u.Path == "/"
}

// ownerTokens are the distinctive parts of the site's domain name — "pco" for
// pco-online.de — which an image naming the site itself carries in its alt
// text or file name.
func ownerTokens(base *url.URL) []string {
	if base == nil {
		return nil
	}
	labels := strings.Split(strings.ToLower(base.Hostname()), ".")
	if len(labels) < 2 {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(labels[len(labels)-2], func(r rune) bool { return r == '-' || r == '_' }) {
		if len(part) >= 3 && !ownerStopWords[part] {
			out = append(out, part)
		}
	}
	return out
}

// namesOwner reports whether text contains one of the owner tokens as a word.
func namesOwner(text string, owner []string) bool {
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, w := range words {
		for _, o := range owner {
			if w == o {
				return true
			}
		}
	}
	return false
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// renderSVG serialises an inline <svg> as a standalone document. The xmlns is
// added when the page left it out, as HTML allows: without it the markup is
// not an image a browser will render from a data: URI.
func renderSVG(n *html.Node) (string, bool) {
	var buf strings.Builder
	if err := html.Render(&buf, n); err != nil {
		return "", false
	}
	markup := buf.String()
	if len(markup) > maxInlineSVGBytes {
		return "", false
	}
	if !strings.Contains(markup[:min(len(markup), 512)], "xmlns=") {
		markup = strings.Replace(markup, "<svg", `<svg xmlns="http://www.w3.org/2000/svg"`, 1)
	}
	return markup, true
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
