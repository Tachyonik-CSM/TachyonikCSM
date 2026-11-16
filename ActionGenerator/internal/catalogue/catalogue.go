// ActionGenerator
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package catalogue holds this daemon's copy of the platform API catalogue
// (docs/specs/01-platform-api-catalogue.md) and the API bridge built from it:
// the `api` object bridge routines read through, in its read-only profile
// (PA-BR-08).
//
// It is loaded from AIManager at startup and again when AIManager announces
// API_CATALOGUE_UPDATED (PA-RT-08). From it come three further answers the
// daemon needs: the operations a routine may call, rendered for the AI that
// writes it (PA-RT-05); which of a routine's operations the catalogue no
// longer has (PA-RT-06); and which WebSocket messages invalidate what a
// routine reads, which is what wakes the daemon (PA-RT-07).
package catalogue

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"tachyonik/actiongenerator/internal/aimanager"
	"tachyonik/lib/apibridge"
	"tachyonik/lib/apicatalogue"
)

// Endpoint is how this daemon reaches one manager.
type Endpoint struct {
	BaseURL    string
	ServiceKey string
}

// Holder is the current catalogue and bridge. Safe for concurrent use.
type Holder struct {
	endpoints map[string]Endpoint // by module name, e.g. "assetmanager"
	opts      apibridge.Options

	mu          sync.RWMutex
	bridge      *apibridge.Bridge
	fingerprint string
	ops         map[string]*apicatalogue.Operation // read operations, by id
	modules     map[string]string                  // operation id -> module
}

// New makes an empty holder. A manager without an endpoint here is left out
// of the bridge: its operations cannot be called from this daemon.
func New(endpoints map[string]Endpoint, opts apibridge.Options) *Holder {
	return &Holder{endpoints: endpoints, opts: opts}
}

// Load replaces the catalogue and rebuilds the bridge. A document that cannot
// be read is skipped and named in the error; the others are still loaded.
func (h *Holder) Load(cat *aimanager.APICatalogue) error {
	var modules []apibridge.Module
	var problems []string
	ops := map[string]*apicatalogue.Operation{}
	owner := map[string]string{}
	for _, d := range cat.Documents {
		ep, ok := h.endpoints[d.Module]
		if !ok {
			continue
		}
		doc, err := apicatalogue.Parse([]byte(d.Document))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", d.Module, err))
			continue
		}
		modules = append(modules, apibridge.Module{Name: d.Module, Document: doc, BaseURL: ep.BaseURL, ServiceKey: ep.ServiceKey})
		for _, op := range doc.BridgeOperations(false) {
			ops[op.ID] = op
			owner[op.ID] = d.Module
		}
	}
	b, err := apibridge.New(modules, apibridge.ReadOnly, h.opts)
	if err != nil {
		return fmt.Errorf("the catalogue cannot be bridged: %w", err)
	}
	h.mu.Lock()
	h.bridge, h.fingerprint, h.ops, h.modules = b, cat.Fingerprint, ops, owner
	h.mu.Unlock()
	if len(problems) > 0 {
		return fmt.Errorf("documents left out: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Bridge returns the current bridge and the catalogue fingerprint it was
// built from; nil before a catalogue was loaded.
func (h *Holder) Bridge() (*apibridge.Bridge, string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.bridge, h.fingerprint
}

// Loaded reports whether a catalogue has been loaded.
func (h *Holder) Loaded() bool {
	b, _ := h.Bridge()
	return b != nil
}

// Missing returns the operations of ops the catalogue does not offer to
// routines (any more), sorted (PA-RT-06).
func (h *Holder) Missing(ops []string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []string
	for _, id := range ops {
		if _, ok := h.ops[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Deprecated returns the operations of ops the catalogue marks deprecated.
func (h *Holder) Deprecated(ops []string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []string
	for _, id := range ops {
		if op, ok := h.ops[id]; ok && op.Deprecated {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// InvalidatedBy adds to set every message type that invalidates one of ops
// (PA-RT-07).
func (h *Holder) InvalidatedBy(ops []string, set map[string]bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, id := range ops {
		if op, ok := h.ops[id]; ok {
			for _, m := range op.InvalidatedBy {
				set[m] = true
			}
		}
	}
}

// Render describes every read operation for the AI that writes a routine
// (PA-RT-05): how to call it, what it answers, and what that means. Built
// from the documents alone, so it says nothing the managers do not.
func (h *Holder) Render() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.ops))
	for id := range h.ops {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		op := h.ops[id]
		var params []string
		for _, p := range op.Parameters {
			if p.Bound != "" || (p.In != "path" && p.In != "query") {
				continue
			}
			params = append(params, p.Name)
		}
		fmt.Fprintf(&b, "api.%s(%s)", id, argList(params))
		if op.Summary != "" {
			fmt.Fprintf(&b, " — %s", op.Summary)
		}
		if op.Deprecated {
			b.WriteString(" (DEPRECATED: do not use in new routines)")
		}
		b.WriteString("\n")
		if d := strings.TrimSpace(op.Description); d != "" {
			for _, line := range strings.Split(d, "\n") {
				fmt.Fprintf(&b, "    %s\n", strings.TrimSpace(line))
			}
		}
		for _, p := range op.Parameters {
			if p.Bound != "" || (p.In != "path" && p.In != "query") {
				continue
			}
			req := "optional"
			if p.Required {
				req = "required"
			}
			typ := "value"
			if p.JSON {
				typ = "object (sent JSON-encoded)"
			} else if p.Schema != nil && len(p.Schema.Types) > 0 {
				typ = strings.Join(p.Schema.Types, " or ")
			}
			fmt.Fprintf(&b, "    parameter %s: %s, %s. %s\n", p.Name, typ, req, oneLine(p.Description))
		}
		if op.Response != nil {
			b.WriteString("    answers:\n")
			renderSchema(&b, op.Response, "      ", 0)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func argList(params []string) string {
	if len(params) == 0 {
		return ""
	}
	return "{" + strings.Join(params, ", ") + "}"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

const maxRenderDepth = 6

func renderSchema(b *strings.Builder, s *apicatalogue.Schema, indent string, depth int) {
	if depth > maxRenderDepth || s == nil {
		return
	}
	names := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := s.Properties[n]
		fmt.Fprintf(b, "%s%s: %s", indent, n, typeOf(p))
		if d := oneLine(p.Description); d != "" {
			fmt.Fprintf(b, " — %s", d)
		}
		b.WriteString("\n")
		if len(p.Properties) > 0 {
			renderSchema(b, p, indent+"  ", depth+1)
		} else if p.Items != nil && len(p.Items.Properties) > 0 {
			renderSchema(b, p.Items, indent+"  ", depth+1)
		}
	}
}

func typeOf(s *apicatalogue.Schema) string {
	types := append([]string(nil), s.Types...)
	t := strings.Join(types, " or ")
	if t == "" {
		t = "any"
	}
	if s.Items != nil {
		item := strings.Join(s.Items.Types, " or ")
		if len(s.Items.Properties) > 0 {
			item = "objects"
		}
		if item == "" {
			item = "values"
		}
		t = strings.Replace(t, "array", "array of "+item, 1)
	}
	if len(s.Enum) > 0 {
		vals := make([]string, len(s.Enum))
		for i, v := range s.Enum {
			vals[i] = fmt.Sprintf("%q", fmt.Sprint(v))
		}
		t += " (one of " + strings.Join(vals, ", ") + ")"
	}
	if s.Deprecated {
		t += ", deprecated"
	}
	return t
}
