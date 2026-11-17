// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Check is the test every manager runs against its own MCP server
// (docs/specs/01-platform-api-catalogue.md, PA-MCP-03): the server mounted on
// the manager's real router offers exactly the operations its document marks
// `x-mcp`, and each of them can be called by the signed-in user the test
// names — the route exists, takes a session, and does not refuse that user.
// It reports what it found rather than failing a test itself, so it needs no
// testing import and is tested like any other function.

package apimcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"

	"tachyonik/lib/apicatalogue"
)

// Marked are the ids of the operations doc marks for MCP, sorted.
func Marked(doc *apicatalogue.Document) []string {
	var ids []string
	for _, op := range doc.Operations {
		if op.MCP != "" {
			ids = append(ids, op.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Check calls the MCP server at POST /api/mcp on router with header (a
// signed-in user's credentials) and reports every way it differs from doc:
// a tool offered that doc does not mark, or one marked and not offered; and a
// marked operation whose call is refused as unauthenticated (401) or
// forbidden (403), or reaches no route (404 page not found, 405). A call is
// made with the required arguments only, each its schema's first enum value
// or a placeholder of its type, so a 400 or a 404 for a record is fine: the
// user reached the operation.
func Check(router http.Handler, header http.Header, doc *apicatalogue.Document) []string {
	var problems []string
	want := Marked(doc)
	var tools struct {
		Tools []Tool `json:"tools"`
	}
	if err := checkRPC(router, header, "tools/list", nil, &tools); err != "" {
		return []string{"tools/list: " + err}
	}
	var got []string
	for _, t := range tools.Tools {
		got = append(got, t.Name)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		problems = append(problems, fmt.Sprintf("tools/list offers [%s]; the document marks [%s]",
			strings.Join(got, ", "), strings.Join(want, ", ")))
	}

	for _, op := range doc.Operations {
		if op.MCP == "" {
			continue
		}
		var result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := checkRPC(router, header, "tools/call", map[string]interface{}{"name": op.ID, "arguments": placeholders(op)}, &result); err != "" {
			problems = append(problems, op.ID+": "+err)
			continue
		}
		if !result.IsError || len(result.Content) == 0 {
			continue
		}
		text := result.Content[0].Text
		for _, refused := range []string{"(401 ", "(403 ", "(405 ", "404 page not found"} {
			if strings.Contains(text, refused) {
				problems = append(problems, fmt.Sprintf("%s cannot be called by a signed-in user: %s", op.ID, text))
				break
			}
		}
	}
	return problems
}

// placeholders are arguments for op's required parameters.
func placeholders(op *apicatalogue.Operation) map[string]interface{} {
	args := map[string]interface{}{}
	for _, p := range op.Parameters {
		if !p.Required || p.Bound != "" || (p.In != "path" && p.In != "query") {
			continue
		}
		args[p.Name] = placeholder(p.Schema, p.JSON)
	}
	if op.MCP == apicatalogue.ExposeWrite && op.RequestBody != nil {
		args["body"] = map[string]interface{}{}
	}
	return args
}

func placeholder(s *apicatalogue.Schema, asJSON bool) interface{} {
	if asJSON {
		return map[string]interface{}{}
	}
	if s != nil && len(s.Enum) > 0 {
		return s.Enum[0]
	}
	if s != nil {
		for _, t := range s.Types {
			switch t {
			case "integer", "number":
				return 1
			case "boolean":
				return false
			}
		}
	}
	return "x"
}

// checkRPC makes one JSON-RPC request and decodes its result into out; it
// answers what went wrong, or "".
func checkRPC(router http.Handler, header http.Header, method string, params interface{}, out interface{}) string {
	body := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewReader(b))
	for k, v := range header {
		req.Header[k] = append([]string(nil), v...)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return fmt.Sprintf("answered %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "the answer is not JSON-RPC: " + err.Error()
	}
	if resp.Error != nil {
		return resp.Error.Message
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return "the result cannot be read: " + err.Error()
	}
	return ""
}
