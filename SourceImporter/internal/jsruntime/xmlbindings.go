// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The ctx.xml binding: a read-only DOM over an XML source, queried with XPath.
//
// Whether a file is XML is decided by sniffing its content, never its extension.
// The binding wraps the document node rather than the root element, so that a
// routine's `//foo` means "anywhere in the document" as XPath users expect
// rather than "below the root element".
//
// Content that is not well-formed yields null, so a routine sees ctx.xml === null
// and can fall back instead of the import failing outright.

package jsruntime

import (
	"strings"
	"unicode"

	"github.com/antchfx/xmlquery"
	"github.com/dop251/goja"
)

// LooksLikeXML sniffs the content to decide whether the import file is XML.
// Only content is consulted — extensions lie. Accepts:
//   - optional BOM
//   - optional leading whitespace
//   - optional XML declaration (<?xml ...?>) or processing instruction
//   - first non-whitespace, non-declaration byte must be '<' followed by a name
func LooksLikeXML(content string) bool {
	s := strings.TrimLeft(content, "\uFEFF")
	s = strings.TrimLeftFunc(s, unicode.IsSpace)

	// Skip XML declarations / processing instructions / comments that may
	// appear before the root element.
	for {
		if strings.HasPrefix(s, "<?xml") || strings.HasPrefix(s, "<?") {
			end := strings.Index(s, "?>")
			if end < 0 {
				return false
			}
			s = strings.TrimLeftFunc(s[end+2:], unicode.IsSpace)
			continue
		}
		if strings.HasPrefix(s, "<!--") {
			end := strings.Index(s, "-->")
			if end < 0 {
				return false
			}
			s = strings.TrimLeftFunc(s[end+3:], unicode.IsSpace)
			continue
		}
		if strings.HasPrefix(s, "<!DOCTYPE") {
			end := strings.Index(s, ">")
			if end < 0 {
				return false
			}
			s = strings.TrimLeftFunc(s[end+1:], unicode.IsSpace)
			continue
		}
		break
	}

	if len(s) < 2 || s[0] != '<' {
		return false
	}
	// Root element must start with a name character.
	return isXMLNameStart(rune(s[1]))
}

func isXMLNameStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

// ParseXMLDoc parses content into a read-only xmlquery document. Returns nil
// when the content is not well-formed XML — the caller treats that as "no
// DOM available" and the JS side will see ctx.xml === null.
func ParseXMLDoc(content string) *xmlquery.Node {
	if content == "" {
		return nil
	}
	doc, err := xmlquery.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}
	return doc
}

// BuildXMLBinding wraps a parsed xmlquery document into a goja value that
// matches the ctx.xml API documented in the importer prompt:
//
//	ctx.xml.find(xpath)        -> Node[]
//	ctx.xml.findOne(xpath)     -> Node | null
//	ctx.xml.text()             -> string   (direct text children only)
//	ctx.xml.innerText()        -> string   (all descendant text concatenated)
//	ctx.xml.attr(name)         -> string
//	ctx.xml.children(tag?)     -> Node[]
//	ctx.xml.localName          -> string   (property, not a call)
//	ctx.xml.name               -> string   (property, fully qualified name)
//
// Node objects returned from find/findOne/children expose the same surface
// relative to themselves. Everything is read-only.
//
// `ctx.xml` wraps the document node itself rather than the document's root
// element. antchfx/xpath evaluates `//foo` as "descendant of the context
// node"; wrapping the document makes `//foo` behave as XPath users expect
// (descendant of the document root) instead of silently excluding the root
// element from the search.
func BuildXMLBinding(vm *goja.Runtime, doc *xmlquery.Node) goja.Value {
	if doc == nil {
		return goja.Null()
	}
	return wrapXMLNode(vm, doc)
}

func wrapXMLNode(vm *goja.Runtime, node *xmlquery.Node) goja.Value {
	if node == nil {
		return goja.Null()
	}
	obj := vm.NewObject()

	_ = obj.Set("name", fullName(node))
	_ = obj.Set("localName", node.Data)

	// text() returns only this element's direct text children. Matches the
	// XPath text() node test and the intent of rule authors for fields like
	// <host>192.168.1.1<hostname>foo</hostname></host>, where they want the
	// IP rather than concatenated descendant text.
	_ = obj.Set("text", func() string {
		return directText(node)
	})

	// innerText() preserves the legacy concatenate-all-descendant-text
	// behaviour for the rare cases where it is actually desired (e.g.
	// prose with inline tags).
	_ = obj.Set("innerText", func() string {
		return node.InnerText()
	})

	_ = obj.Set("attr", func(name string) string {
		return node.SelectAttr(name)
	})

	_ = obj.Set("find", func(xpath string) goja.Value {
		nodes, err := xmlquery.QueryAll(node, xpath)
		if err != nil {
			panic(vm.NewTypeError("ctx.xml.find: invalid XPath: " + err.Error()))
		}
		return wrapNodeList(vm, nodes)
	})

	_ = obj.Set("findOne", func(xpath string) goja.Value {
		found, err := xmlquery.Query(node, xpath)
		if err != nil {
			panic(vm.NewTypeError("ctx.xml.findOne: invalid XPath: " + err.Error()))
		}
		if found == nil {
			return goja.Null()
		}
		return wrapXMLNode(vm, found)
	})

	_ = obj.Set("children", func(call goja.FunctionCall) goja.Value {
		var filter string
		if len(call.Arguments) > 0 && !goja.IsUndefined(call.Arguments[0]) && !goja.IsNull(call.Arguments[0]) {
			filter = call.Arguments[0].String()
		}
		var kids []*xmlquery.Node
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != xmlquery.ElementNode {
				continue
			}
			if filter == "" || c.Data == filter {
				kids = append(kids, c)
			}
		}
		return wrapNodeList(vm, kids)
	})

	return obj
}

func wrapNodeList(vm *goja.Runtime, nodes []*xmlquery.Node) goja.Value {
	arr := make([]interface{}, 0, len(nodes))
	for _, n := range nodes {
		arr = append(arr, wrapXMLNode(vm, n))
	}
	return vm.ToValue(arr)
}

func fullName(node *xmlquery.Node) string {
	if node.Prefix != "" {
		return node.Prefix + ":" + node.Data
	}
	return node.Data
}

// directText concatenates the direct text children of an element (ignoring
// descendant text in nested elements). For the document node this returns
// an empty string, since document-level text is not meaningful.
func directText(node *xmlquery.Node) string {
	if node == nil {
		return ""
	}
	var b strings.Builder
	for c := node.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xmlquery.TextNode || c.Type == xmlquery.CharDataNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}
