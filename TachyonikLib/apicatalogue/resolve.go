// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Turning the decoded YAML of an API document into Operations, Parameters and
// Schemas: following local $refs, merging allOf, and refusing the schema
// keywords whose meaning this package does not implement (oneOf, anyOf, not),
// since a schema read without them would accept and filter the wrong values.

package apicatalogue

import (
	"fmt"
	"strings"
	"time"
)

type resolver struct {
	root map[string]interface{}
}

// maxRefDepth bounds $ref chains, so a cycle is an error rather than a hang.
const maxRefDepth = 32

// deref follows a local $ref ("#/components/…") until it reaches a value
// that is not one. Anything else is returned as it is.
func (r *resolver) deref(v interface{}) interface{} {
	out, _ := r.derefErr(v)
	return out
}

func (r *resolver) derefErr(v interface{}) (interface{}, error) {
	for depth := 0; depth < maxRefDepth; depth++ {
		m, ok := v.(map[string]interface{})
		if !ok {
			return v, nil
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return v, nil
		}
		if !strings.HasPrefix(ref, "#/") {
			return nil, fmt.Errorf("$ref %q: only local references are read", ref)
		}
		var cur interface{} = r.root
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			cm, ok := cur.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("$ref %q does not resolve", ref)
			}
			if cur, ok = cm[part]; !ok {
				return nil, fmt.Errorf("$ref %q does not resolve", ref)
			}
		}
		v = cur
	}
	return nil, fmt.Errorf("$ref chain deeper than %d", maxRefDepth)
}

func (r *resolver) operation(method, path string, raw map[string]interface{}, shared []*Parameter) (*Operation, error) {
	op := &Operation{Method: method, Path: path, Examples: map[string]interface{}{}}
	op.ID, _ = raw["operationId"].(string)
	op.Summary, _ = raw["summary"].(string)
	op.Description, _ = raw["description"].(string)
	op.Deprecated, _ = raw["deprecated"].(bool)
	op.Bridge, _ = raw["x-bridge"].(string)
	op.MCP, _ = raw["x-mcp"].(string)
	if list, ok := raw["x-invalidated-by"].([]interface{}); ok {
		for _, v := range list {
			s, ok := v.(string)
			if !ok || s == "" {
				return nil, fmt.Errorf("x-invalidated-by: %v is not a message type", v)
			}
			op.InvalidatedBy = append(op.InvalidatedBy, s)
		}
	}

	own, err := r.parameters(raw["parameters"])
	if err != nil {
		return nil, err
	}
	// An operation's parameter overrides a path-level one of the same name
	// and location.
	byKey := map[string]*Parameter{}
	var order []string
	for _, p := range append(shared, own...) {
		key := p.In + ":" + p.Name
		if _, seen := byKey[key]; !seen {
			order = append(order, key)
		}
		byKey[key] = p
	}
	for _, key := range order {
		op.Parameters = append(op.Parameters, byKey[key])
	}

	if body, ok := r.deref(raw["requestBody"]).(map[string]interface{}); ok {
		if s, err := r.jsonSchemaOf(body); err != nil {
			return nil, fmt.Errorf("request body: %w", err)
		} else {
			op.RequestBody = s
		}
	}

	responses, _ := raw["responses"].(map[string]interface{})
	op.Statuses = sortedKeys(responses)
	for _, code := range sortedKeys(responses) {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		resp, ok := r.deref(responses[code]).(map[string]interface{})
		if !ok {
			continue
		}
		content, _ := resp["content"].(map[string]interface{})
		media, ok := r.deref(content["application/json"]).(map[string]interface{})
		if !ok {
			continue
		}
		s, err := r.schema(media["schema"], 0)
		if err != nil {
			return nil, fmt.Errorf("answer %s: %w", code, err)
		}
		op.Response = s
		if exs, ok := media["examples"].(map[string]interface{}); ok {
			for name, ex := range exs {
				exm, ok := r.deref(ex).(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("example %q is not an object", name)
				}
				value, ok := exm["value"]
				if !ok {
					return nil, fmt.Errorf("example %q has no value", name)
				}
				op.Examples[name] = normalise(value)
			}
		}
		break
	}
	return op, nil
}

func (r *resolver) parameters(v interface{}) ([]*Parameter, error) {
	list, _ := v.([]interface{})
	var out []*Parameter
	for _, item := range list {
		raw, err := r.derefErr(item)
		if err != nil {
			return nil, err
		}
		m, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("a parameter is not an object")
		}
		p := &Parameter{}
		p.Name, _ = m["name"].(string)
		p.In, _ = m["in"].(string)
		p.Required, _ = m["required"].(bool)
		p.Description, _ = m["description"].(string)
		p.Deprecated, _ = m["deprecated"].(bool)
		p.Bound, _ = m["x-bridge-bound"].(string)
		if p.Name == "" {
			return nil, fmt.Errorf("a parameter has no name")
		}
		switch p.In {
		case "path", "query", "header", "cookie":
		default:
			return nil, fmt.Errorf("parameter %s: in %q", p.Name, p.In)
		}
		if content, ok := m["content"].(map[string]interface{}); ok {
			media, _ := content["application/json"].(map[string]interface{})
			s, err := r.schema(media["schema"], 0)
			if err != nil {
				return nil, fmt.Errorf("parameter %s: %w", p.Name, err)
			}
			p.Schema, p.JSON = s, true
		} else if m["schema"] != nil {
			s, err := r.schema(m["schema"], 0)
			if err != nil {
				return nil, fmt.Errorf("parameter %s: %w", p.Name, err)
			}
			p.Schema = s
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *resolver) jsonSchemaOf(body map[string]interface{}) (*Schema, error) {
	content, _ := body["content"].(map[string]interface{})
	media, ok := r.deref(content["application/json"]).(map[string]interface{})
	if !ok {
		return nil, nil
	}
	return r.schema(media["schema"], 0)
}

func (r *resolver) schema(v interface{}, depth int) (*Schema, error) {
	if v == nil {
		return nil, nil
	}
	if depth > maxRefDepth {
		return nil, fmt.Errorf("schema nested deeper than %d", maxRefDepth)
	}
	raw, err := r.derefErr(v)
	if err != nil {
		return nil, err
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("a schema is not an object")
	}
	for _, kw := range []string{"oneOf", "anyOf", "not"} {
		if _, ok := m[kw]; ok {
			return nil, fmt.Errorf("schema keyword %s is not supported", kw)
		}
	}
	s := &Schema{}
	switch t := m["type"].(type) {
	case string:
		s.Types = []string{t}
	case []interface{}:
		for _, x := range t {
			if name, ok := x.(string); ok {
				s.Types = append(s.Types, name)
			}
		}
	}
	s.Description, _ = m["description"].(string)
	s.Deprecated, _ = m["deprecated"].(bool)
	s.Bound, _ = m["x-bridge-bound"].(string)
	if enum, ok := m["enum"].([]interface{}); ok {
		s.Enum = enum
	}
	if req, ok := m["required"].([]interface{}); ok {
		for _, x := range req {
			if name, ok := x.(string); ok {
				s.Required = append(s.Required, name)
			}
		}
	}
	if props, ok := m["properties"].(map[string]interface{}); ok {
		s.Properties = make(map[string]*Schema, len(props))
		for name, pv := range props {
			ps, err := r.schema(pv, depth+1)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			s.Properties[name] = ps
		}
	}
	if items, ok := m["items"]; ok {
		is, err := r.schema(items, depth+1)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		s.Items = is
	}
	if all, ok := m["allOf"].([]interface{}); ok {
		for _, part := range all {
			ps, err := r.schema(part, depth+1)
			if err != nil {
				return nil, fmt.Errorf("allOf: %w", err)
			}
			s.merge(ps)
		}
	}
	return s, nil
}

// merge folds an allOf part into s: the union of properties and required
// fields; types, items and description where s has none of its own.
func (s *Schema) merge(o *Schema) {
	if o == nil {
		return
	}
	if len(s.Types) == 0 {
		s.Types = o.Types
	}
	if s.Items == nil {
		s.Items = o.Items
	}
	if s.Description == "" {
		s.Description = o.Description
	}
	if len(o.Properties) > 0 && s.Properties == nil {
		s.Properties = map[string]*Schema{}
	}
	for name, ps := range o.Properties {
		if _, ok := s.Properties[name]; !ok {
			s.Properties[name] = ps
		}
	}
	s.Required = append(s.Required, o.Required...)
}

// normalise turns what YAML decoded into what JSON decoding would produce, so
// an example and a live answer look the same to the bridge: maps keyed by
// string, numbers as float64, times as RFC 3339 text.
func normalise(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[k] = normalise(val)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[fmt.Sprint(k)] = normalise(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, val := range x {
			out[i] = normalise(val)
		}
		return out
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case uint64:
		return float64(x)
	case time.Time:
		// YAML reads an unquoted date as a timestamp; JSON carries text.
		return x.UTC().Format(time.RFC3339)
	}
	return v
}
