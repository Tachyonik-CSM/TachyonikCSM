// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apimcp serves a manager's MCP server generated from its API
// document (docs/specs/01-platform-api-catalogue.md §8): one tool per
// operation marked `x-mcp`, named by its operationId, described by its
// summary, description, parameters and answer (PA-MCP-01, -02, -04).
//
// A tool call is made as the API call it stands for, through the manager's
// own router, with the caller's credentials copied onto it: the same
// authentication, the same permissions, the same answer (PA-MCP-06). The
// handler is mounted behind the manager's authentication; it adds none of
// its own and grants nothing the API does not.
//
// The protocol is MCP's JSON-RPC over HTTP POST, answered with one JSON
// object per request (no streaming): initialize, ping, tools/list,
// tools/call, and the initialized notification.
package apimcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"tachyonik/lib/apicatalogue"
	"tachyonik/lib/apiholder"
)

// ProtocolVersion is the MCP revision this server speaks.
const ProtocolVersion = "2025-06-18"

// maxRequest bounds a JSON-RPC request.
const maxRequest = 1 << 20

// Server is one manager's MCP server.
type Server struct {
	name, version string
	api           http.Handler
	ops           map[string]*apicatalogue.Operation
	tools         []Tool
}

// Tool is a tool as tools/list describes it.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
	Annotations map[string]interface{} `json:"annotations,omitempty"`
}

// New builds the server for module (e.g. "assetmanager") from its document;
// api is the manager's router the calls go through. It refuses a document
// whose MCP operations cannot be called by a signed-in user as documented:
// one with a parameter the bridge binds, which a session supplies instead.
func New(module, version string, doc *apicatalogue.Document, api http.Handler) (*Server, error) {
	s := &Server{name: module, version: version, api: api, ops: map[string]*apicatalogue.Operation{}, tools: []Tool{}}
	for _, op := range doc.Operations {
		if op.MCP == "" {
			continue
		}
		for _, p := range op.Parameters {
			if p.Bound != "" && p.Required {
				return nil, fmt.Errorf("%s is marked x-mcp but needs %s %q, which only the bridge sets", op.ID, p.In, p.Name)
			}
		}
		s.ops[op.ID] = op
		s.tools = append(s.tools, toolOf(op))
	}
	sort.Slice(s.tools, func(i, j int) bool { return s.tools[i].Name < s.tools[j].Name })
	return s, nil
}

// Tools returns the tools the server offers, sorted by name.
func (s *Server) Tools() []Tool { return append([]Tool(nil), s.tools...) }

func toolOf(op *apicatalogue.Operation) Tool {
	var d strings.Builder
	d.WriteString(strings.TrimSpace(op.Summary))
	if desc := strings.TrimSpace(op.Description); desc != "" {
		d.WriteString("\n\n" + desc)
	}
	if op.Deprecated {
		d.WriteString("\n\nDEPRECATED: prefer another tool.")
	}
	if op.Response != nil {
		if fields := strings.TrimRight(apiholder.DescribeFields(op.Response), "\n"); fields != "" {
			d.WriteString("\n\nAnswers:\n" + fields)
		}
	}
	props := map[string]interface{}{}
	var required []string
	for _, p := range op.Parameters {
		if p.Bound != "" || (p.In != "path" && p.In != "query") {
			continue
		}
		ps := schemaJSON(p.Schema)
		if p.Description != "" {
			ps["description"] = p.Description
		}
		props[p.Name] = ps
		if p.Required {
			required = append(required, p.Name)
		}
	}
	if op.MCP == apicatalogue.ExposeWrite && op.RequestBody != nil {
		props["body"] = schemaJSON(op.RequestBody)
		required = append(required, "body")
	}
	in := map[string]interface{}{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		sort.Strings(required)
		in["required"] = required
	}
	t := Tool{Name: op.ID, Description: d.String(), InputSchema: in}
	if op.MCP == apicatalogue.ExposeRead {
		t.Annotations = map[string]interface{}{"readOnlyHint": true}
	}
	return t
}

// schemaJSON writes a parsed schema back as JSON Schema, leaving out fields
// the bridge binds and fields hidden from routines.
func schemaJSON(s *apicatalogue.Schema) map[string]interface{} {
	out := map[string]interface{}{}
	if s == nil {
		return out
	}
	switch len(s.Types) {
	case 0:
	case 1:
		out["type"] = s.Types[0]
	default:
		out["type"] = append([]string(nil), s.Types...)
	}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Items != nil {
		out["items"] = schemaJSON(s.Items)
	}
	if len(s.Properties) > 0 {
		props := map[string]interface{}{}
		for n, p := range s.Properties {
			if p.Bound != "" || p.Hidden {
				continue
			}
			props[n] = schemaJSON(p)
		}
		out["properties"] = props
		var req []string
		for _, r := range s.Required {
			if p, ok := s.Properties[r]; ok && p.Bound == "" && !p.Hidden {
				req = append(req, r)
			}
		}
		if len(req) > 0 {
			out["required"] = req
		}
	}
	return out
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ServeHTTP answers one JSON-RPC request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "MCP takes POST", http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequest)).Decode(&req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "not JSON-RPC"}})
		return
	}
	if len(req.ID) == 0 { // a notification: nothing to answer
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]interface{}{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": s.name, "version": s.version},
		}
	case "ping":
		resp.Result = map[string]interface{}{}
	case "tools/list":
		resp.Result = map[string]interface{}{"tools": s.tools}
	case "tools/call":
		var p struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "tools/call takes {name, arguments}"}
			break
		}
		op, ok := s.ops[p.Name]
		if !ok {
			resp.Error = &rpcError{Code: -32602, Message: fmt.Sprintf("no tool %q", p.Name)}
			break
		}
		resp.Result = s.call(r, op, p.Arguments)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	writeRPC(w, resp)
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func textResult(text string, isError bool) map[string]interface{} {
	return map[string]interface{}{
		"content": []interface{}{map[string]interface{}{"type": "text", "text": text}},
		"isError": isError,
	}
}

// forwarded are the caller's headers a tool call carries to the API: its
// credentials, and what the API reads about the client.
var forwarded = []string{"Authorization", "Cookie", "Accept-Language", "X-Forwarded-For", "X-Real-IP"}

// call makes the API call a tool call stands for.
func (s *Server) call(outer *http.Request, op *apicatalogue.Operation, args map[string]interface{}) map[string]interface{} {
	path, query, body, problem := build(op, args)
	if problem != "" {
		return textResult("Error: "+op.ID+": "+problem, true)
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	inner, err := http.NewRequestWithContext(outer.Context(), op.Method, target, bytes.NewReader(body))
	if err != nil {
		return textResult("Error: "+op.ID+": "+err.Error(), true)
	}
	inner.RemoteAddr = outer.RemoteAddr
	inner.Host = outer.Host
	for _, h := range forwarded {
		if v := outer.Header.Values(h); len(v) > 0 {
			inner.Header[h] = append([]string(nil), v...)
		}
	}
	inner.Header.Set("Accept", "application/json")
	if body != nil {
		inner.Header.Set("Content-Type", "application/json")
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	s.api.ServeHTTP(rec, inner)

	if rec.status < 200 || rec.status > 299 {
		msg := strings.TrimSpace(rec.body.String())
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(rec.body.Bytes(), &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if rec.status == http.StatusBadRequest {
			return textResult(fmt.Sprintf("Error: %s refused the arguments: %s", op.ID, msg), true)
		}
		return textResult(fmt.Sprintf("%s failed (%d %s): %s", op.ID, rec.status, http.StatusText(rec.status), msg), true)
	}
	if !strings.Contains(rec.header.Get("Content-Type"), "json") {
		return textResult(rec.body.String(), false)
	}
	var v interface{}
	if err := json.Unmarshal(rec.body.Bytes(), &v); err != nil {
		return textResult(rec.body.String(), false)
	}
	if op.Response != nil {
		v = op.Response.Filter(v) // the documented fields only, without hidden ones
	}
	out, _ := json.Marshal(v)
	return textResult(string(out), false)
}

// build turns a tool call's arguments into the API call's path, query and
// body. An argument the operation does not have is refused, as by the bridge.
func build(op *apicatalogue.Operation, args map[string]interface{}) (string, url.Values, []byte, string) {
	known := map[string]bool{}
	path := op.Path
	query := url.Values{}
	for _, p := range op.Parameters {
		if p.Bound != "" || (p.In != "path" && p.In != "query") {
			continue
		}
		known[p.Name] = true
		v, ok := args[p.Name]
		if !ok || v == nil {
			if p.Required {
				return "", nil, nil, fmt.Sprintf("%s is required", p.Name)
			}
			continue
		}
		text, err := argText(v, p.JSON)
		if err != nil {
			return "", nil, nil, fmt.Sprintf("%s: %v", p.Name, err)
		}
		if p.In == "path" {
			path = strings.Replace(path, "{"+p.Name+"}", url.PathEscape(text), 1)
		} else {
			query.Set(p.Name, text)
		}
	}
	var body []byte
	if op.MCP == apicatalogue.ExposeWrite && op.RequestBody != nil {
		known["body"] = true
		b, ok := args["body"]
		if !ok {
			return "", nil, nil, "body is required"
		}
		body, _ = json.Marshal(b)
	}
	var unknown []string
	for name := range args {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return "", nil, nil, "it has no argument " + strings.Join(unknown, ", ")
	}
	return path, query, body, ""
}

func argText(v interface{}, asJSON bool) (string, error) {
	if asJSON {
		b, err := json.Marshal(v)
		return string(b), err
	}
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case map[string]interface{}, []interface{}:
		return "", fmt.Errorf("must be a single value")
	}
	return fmt.Sprint(v), nil
}

// recorder collects the API's answer to a tool call.
type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	if r.body.Len()+len(b) > maxAnswer {
		return 0, fmt.Errorf("answer larger than %d bytes", maxAnswer)
	}
	return r.body.Write(b)
}

// maxAnswer bounds one tool call's answer; ToolManager cuts what it passes
// to the model further.
const maxAnswer = 8 << 20
