// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apibridge exposes a synchronous "api" object to goja JavaScript that
// proxies HTTP calls to the various Tachyonik backend services on behalf of the
// requesting user.
//
// All methods are blocking. They throw a JavaScript error (via panic-with-Value
// caught by goja) when the underlying HTTP call returns a non-2xx status, when
// the network fails, or when the response can't be decoded. The generated JS is
// expected to wrap calls in try/catch where recovery is meaningful.
//
// Its Call is also the one place any request from this module to another
// service is authenticated — an automatic run's own requests go through it
// too — so the user's token or the service key is attached by a single piece
// of code.
package apibridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"

	"tachyonik/actionexecutor/internal/config"
	"tachyonik/lib/logger"
)

// throwJS panics with a goja Error object, which goja translates into a real
// JavaScript exception. The JS catch block then sees `e instanceof Error` and
// `e.message` populated. Using `panic(vm.ToValue(err.Error()))` instead would
// throw a bare string, leaving `e.message` undefined in JS.
func throwJS(vm *goja.Runtime, err error) {
	panic(vm.NewGoError(err))
}

// AuthContext is who a run acts for. Every call is made with the service's
// internal key and the user in X-User-ID / X-User-Role, so the backend applies
// that user's permissions. There is no user token: a manual run arrives from
// AIManager, which checked the user's login when they asked, and an automatic
// run has nobody pressing anything.
type AuthContext struct {
	UserID   int64
	UserRole string
}

// ownedBy is a create request's body with its owner set to the run's user.
//
// The call goes out with the service key, and AssetManager and ActionManager
// let a service name any owner in the body — so a routine, whoever's data it
// was given, could otherwise create assets or actions in another user's
// account by writing a userId of its choosing. Whatever it wrote, the owner is
// the user the run is for.
func ownedBy(body interface{}, auth AuthContext) interface{} {
	m, ok := body.(map[string]interface{})
	if !ok {
		return body // not an object: the backend refuses it as it is
	}
	out := make(map[string]interface{}, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out["userId"] = auth.UserID
	return out
}

// Bridge holds backend service URLs and per-service internal service keys.
// One Bridge instance is created at daemon startup; it spawns short-lived
// per-request goja "api" objects via Build.
type Bridge struct {
	cfg        *config.Config
	httpClient *http.Client
	// toolClient carries api.tools.execute, which waits for a security tool
	// to finish: ToolManager allows a tool up to 10 minutes, and the 30 s of
	// every other call failed longer scans in the routine while the tool
	// kept running. It may take as long as the run itself may.
	toolClient *http.Client
}

// MaxResponseBytes bounds one backend answer, as TachyonikLib's restclient
// does for every other client in the platform.
const MaxResponseBytes = 32 << 20 // 32 MiB

// New creates a Bridge.
func New(cfg *config.Config) *Bridge {
	return &Bridge{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirects},
		toolClient: &http.Client{Timeout: cfg.ExecutionTimeout(), CheckRedirect: noRedirects},
	}
}

// noRedirects stops a client following a redirect. Go drops Authorization on a
// redirect to another host but forwards custom headers as they are, so
// following one would hand X-Internal-Service-Key to whatever address a
// backend's Location names. No backend redirects legitimately; a 3xx is
// reported as the failure it is. restclient does the same.
func noRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// Build constructs the api object exposed to JavaScript for one execution
// request. The returned goja.Value should be passed as the second argument
// when invoking the run() function.
func (b *Bridge) Build(vm *goja.Runtime, auth AuthContext) goja.Value {
	api := vm.NewObject()

	api.Set("user", b.buildUser(vm, auth))
	api.Set("organisation", b.buildOrganisation(vm, auth))
	api.Set("assets", b.buildAssets(vm, auth))
	api.Set("actions", b.buildActions(vm, auth))
	api.Set("sources", b.buildSources(vm, auth))
	api.Set("tools", b.buildTools(vm, auth))
	api.Set("vulnerabilities", b.buildVulnerabilities(vm, auth))
	api.Set("detections", b.buildDetections(vm, auth))

	return api
}

// ----------------------------------------------------------------------------
// Per-namespace builders
// ----------------------------------------------------------------------------

// buildUser is who the run is for: the user record SystemManager has for the
// login token, or for the user an automatic run acts on behalf of.
func (b *Bridge) buildUser(vm *goja.Runtime, auth AuthContext) *goja.Object {
	obj := vm.NewObject()
	obj.Set("get", func(call goja.FunctionCall) goja.Value {
		var result map[string]interface{}
		if err := b.do("GET", b.cfg.SystemManager.URL, "/api/auth/me", b.cfg.SystemManager.InternalServiceKey, auth, nil, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})
	return obj
}

func (b *Bridge) buildOrganisation(vm *goja.Runtime, auth AuthContext) *goja.Object {
	obj := vm.NewObject()

	obj.Set("get", func(call goja.FunctionCall) goja.Value {
		var result map[string]interface{}
		if err := b.do("GET", b.cfg.SystemManager.URL, "/api/users/me/organisation", b.cfg.SystemManager.InternalServiceKey, auth, nil, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	// SystemManager's PATCH /api/users/me/organisation is a true partial
	// update — it validates and changes only the fields sent — so the routine's
	// patch goes as it is. The bridge used to read the organisation first and
	// send the merged whole back, from when the endpoint required every field;
	// that cost a request and could write back a name or cost someone had just
	// changed in the WebUI.
	obj.Set("update", func(call goja.FunctionCall) goja.Value {
		patch, ok := exportArg(call, 0).(map[string]interface{})
		if !ok {
			throwJS(vm, fmt.Errorf("api.organisation.update: argument must be an object"))
		}
		var result map[string]interface{}
		if err := b.do("PATCH", b.cfg.SystemManager.URL, "/api/users/me/organisation", b.cfg.SystemManager.InternalServiceKey, auth, patch, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})
	return obj
}

func (b *Bridge) buildAssets(vm *goja.Runtime, auth AuthContext) *goja.Object {
	obj := vm.NewObject()
	base := b.cfg.AssetManager.URL
	key := b.cfg.AssetManager.InternalServiceKey

	obj.Set("list", func(call goja.FunctionCall) goja.Value {
		filter := exportArg(call, 0)
		path := "/api/assets"
		if filter != nil {
			if buf, err := json.Marshal(filter); err == nil {
				path += "?filter=" + url.QueryEscape(string(buf))
			}
		}
		var result struct {
			Assets []map[string]interface{} `json:"assets"`
		}
		if err := b.do("GET", base, path, key, auth, nil, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result.Assets)
	})

	obj.Set("get", func(call goja.FunctionCall) goja.Value {
		id := call.Argument(0).ToInteger()
		var result map[string]interface{}
		if err := b.do("GET", base, fmt.Sprintf("/api/assets/%d", id), key, auth, nil, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	obj.Set("create", func(call goja.FunctionCall) goja.Value {
		body := ownedBy(exportArg(call, 0), auth)
		var result map[string]interface{}
		if err := b.do("POST", base, "/api/assets", key, auth, body, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	obj.Set("update", func(call goja.FunctionCall) goja.Value {
		id := call.Argument(0).ToInteger()
		body := exportArg(call, 1)
		var result map[string]interface{}
		if err := b.do("PATCH", base, fmt.Sprintf("/api/assets/%d", id), key, auth, body, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	obj.Set("delete", func(call goja.FunctionCall) goja.Value {
		idsArg := call.Argument(0).Export()
		var result map[string]interface{}
		if err := b.do("DELETE", base, "/api/assets", key, auth, map[string]interface{}{"ids": idsArg}, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	return obj
}

func (b *Bridge) buildActions(vm *goja.Runtime, auth AuthContext) *goja.Object {
	obj := vm.NewObject()
	base := b.cfg.ActionManager.URL
	key := b.cfg.ActionManager.InternalServiceKey

	obj.Set("list", func(call goja.FunctionCall) goja.Value {
		var result struct {
			Actions []map[string]interface{} `json:"actions"`
		}
		if err := b.do("GET", base, "/api/actions", key, auth, nil, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result.Actions)
	})

	obj.Set("create", func(call goja.FunctionCall) goja.Value {
		body := ownedBy(exportArg(call, 0), auth)
		var result map[string]interface{}
		if err := b.do("POST", base, "/api/actions", key, auth, body, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	obj.Set("update", func(call goja.FunctionCall) goja.Value {
		id := call.Argument(0).ToInteger()
		body := exportArg(call, 1)
		var result map[string]interface{}
		if err := b.do("PATCH", base, fmt.Sprintf("/api/actions/%d", id), key, auth, body, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	obj.Set("delete", func(call goja.FunctionCall) goja.Value {
		idsArg := call.Argument(0).Export()
		var result map[string]interface{}
		if err := b.do("DELETE", base, "/api/actions", key, auth, map[string]interface{}{"ids": idsArg}, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	return obj
}

func (b *Bridge) buildSources(vm *goja.Runtime, auth AuthContext) *goja.Object {
	obj := vm.NewObject()
	base := b.cfg.ResourceManager.URL
	key := b.cfg.ResourceManager.InternalServiceKey

	obj.Set("list", func(call goja.FunctionCall) goja.Value {
		var result struct {
			Sources []map[string]interface{} `json:"sources"`
		}
		if err := b.do("GET", base, "/api/sources", key, auth, nil, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result.Sources)
	})

	obj.Set("delete", func(call goja.FunctionCall) goja.Value {
		idsArg := call.Argument(0).Export()
		var result map[string]interface{}
		if err := b.do("DELETE", base, "/api/sources", key, auth, map[string]interface{}{"ids": idsArg}, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	// create writes a new text Source for the executing user. Backed by
	// ResourceManager's POST /api/sources/json (public, JWT-authed) —
	// the bridge does NOT support binary upload (operators that need
	// that should use the multipart upload flow in the WebUI). The
	// owner is determined server-side from the auth context, so
	// routines can't write into another user's archive.
	obj.Set("create", func(call goja.FunctionCall) goja.Value {
		dataRaw := exportArg(call, 0)
		dataMap, ok := dataRaw.(map[string]interface{})
		if !ok {
			throwJS(vm, fmt.Errorf("api.sources.create: argument must be an object {filename, content, sourceType?, category?}"))
		}
		filename, _ := dataMap["filename"].(string)
		content, _ := dataMap["content"].(string)
		if filename == "" {
			throwJS(vm, fmt.Errorf("api.sources.create: filename is required"))
		}
		body := map[string]interface{}{
			"filename": filename,
			"content":  content,
		}
		if st, ok := dataMap["sourceType"].(string); ok && st != "" {
			body["sourceType"] = st
		}
		if cat, ok := dataMap["category"].(string); ok && cat != "" {
			body["category"] = cat
		}
		var result map[string]interface{}
		if err := b.do("POST", base, "/api/sources/json", key, auth, body, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	return obj
}

// buildTools exposes a flat, capability-aware view of the user's tools
// plus a synchronous execute method. Capabilities are surfaced as string
// arrays (e.g. ["Host detection", "Port scan"]) so routines can filter
// without dereferencing capability IDs themselves; the join lives here
// rather than in any single backend endpoint because no current
// endpoint has the right shape.
func (b *Bridge) buildTools(vm *goja.Runtime, auth AuthContext) *goja.Object {
	obj := vm.NewObject()

	obj.Set("list", func(call goja.FunctionCall) goja.Value {
		filter := exportArg(call, 0)
		path := "/api/tools"
		if filter != nil {
			filterJSON, err := json.Marshal(filter)
			if err == nil {
				path = path + "?filter=" + url.QueryEscape(string(filterJSON))
			}
		}
		enriched, err := b.fetchEnrichedTools(auth, path)
		if err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(enriched)
	})

	obj.Set("get", func(call goja.FunctionCall) goja.Value {
		idArg := call.Argument(0).Export()
		var id int64
		switch v := idArg.(type) {
		case int64:
			id = v
		case float64:
			id = int64(v)
		default:
			throwJS(vm, fmt.Errorf("api.tools.get: id must be a number"))
		}

		var tool map[string]interface{}
		// Public endpoint enforces user ownership server-side (returns
		// 404 for cross-user reads), so no extra check is needed here.
		if err := b.do("GET", b.cfg.ResourceManager.URL,
			fmt.Sprintf("/api/tools/%d", id),
			b.cfg.ResourceManager.InternalServiceKey, auth, nil, &tool); err != nil {
			throwJS(vm, err)
		}
		enriched, err := b.enrichTools([]map[string]interface{}{tool}, auth)
		if err != nil {
			throwJS(vm, err)
		}
		if len(enriched) == 0 {
			throwJS(vm, fmt.Errorf("api.tools.get: tool %d not found", id))
		}
		return vm.ToValue(enriched[0])
	})

	// execute fires a synchronous tool invocation through ToolManager.
	// Signature: api.tools.execute(toolId, arguments, ruleId?). The optional
	// ruleId is required by ToolManager when the tool's overview has more
	// than one rule attached; without it the call returns HTTP 400. Returns
	// the raw {content, isError, storedFiles, errors, warnings} response so routines
	// can branch on isError. Output files are stored as Sources by ToolManager.
	obj.Set("execute", func(call goja.FunctionCall) goja.Value {
		idArg := call.Argument(0).Export()
		var toolID int64
		switch v := idArg.(type) {
		case int64:
			toolID = v
		case float64:
			toolID = int64(v)
		default:
			throwJS(vm, fmt.Errorf("api.tools.execute: first argument (toolId) must be a number"))
		}
		argsRaw := exportArg(call, 1)
		args, _ := argsRaw.(map[string]interface{})
		if args == nil {
			args = map[string]interface{}{}
		}
		body := map[string]interface{}{"toolId": toolID, "arguments": args}
		if ruleArg := call.Argument(2); !goja.IsUndefined(ruleArg) && !goja.IsNull(ruleArg) {
			if rid, ok := numberAsInt64(ruleArg.Export()); ok {
				body["ruleId"] = rid
			} else {
				throwJS(vm, fmt.Errorf("api.tools.execute: third argument (ruleId) must be a number when supplied"))
			}
		}
		var result map[string]interface{}
		if err := b.doWith(b.toolClient, "POST", b.cfg.ToolManager.URL, "/api/tools/execute",
			b.cfg.ToolManager.InternalServiceKey, auth,
			body, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result)
	})

	return obj
}

// fetchEnrichedTools fetches tools at the given RM path and joins capability
// names from AIManager. Two AIManager calls happen per invocation; that's
// acceptable at routine cadence and avoids a third AIManager endpoint.
func (b *Bridge) fetchEnrichedTools(auth AuthContext, path string) ([]map[string]interface{}, error) {
	var listResp struct {
		Tools []map[string]interface{} `json:"tools"`
	}
	if err := b.do("GET", b.cfg.ResourceManager.URL, path,
		b.cfg.ResourceManager.InternalServiceKey, auth, nil, &listResp); err != nil {
		return nil, err
	}
	return b.enrichTools(listResp.Tools, auth)
}

// enrichTools attaches automatedCapabilities, manualCapabilities (string
// arrays), and rules ([{id, name, description}]) to each tool, resolving
// via the tool's toolId → ToolOverview → capability IDs / rule list.
// Tools without an associated overview (toolId == null, "unmanaged") get
// empty arrays. The rules array is what ToolManager's /api/tools/execute
// disambiguates against — when len(rules) > 1, the routine MUST pass a
// ruleId to api.tools.execute or the call returns 400.
func (b *Bridge) enrichTools(tools []map[string]interface{}, auth AuthContext) ([]map[string]interface{}, error) {
	if len(tools) == 0 {
		return tools, nil
	}

	var overviewResp struct {
		ToolOverview []map[string]interface{} `json:"toolOverview"`
	}
	if err := b.do("GET", b.cfg.AIManager.URL, "/api/tool-overview",
		b.cfg.AIManager.InternalServiceKey, auth, nil, &overviewResp); err != nil {
		return nil, fmt.Errorf("enrich tools (overviews): %w", err)
	}

	var capResp struct {
		ToolCapabilities []map[string]interface{} `json:"toolCapabilities"`
	}
	if err := b.do("GET", b.cfg.AIManager.URL, "/api/internal/tool-capabilities",
		b.cfg.AIManager.InternalServiceKey, auth, nil, &capResp); err != nil {
		return nil, fmt.Errorf("enrich tools (capabilities): %w", err)
	}

	// Tool rules carry the actual invocation profile (command + args
	// schema + limits). Multiple rules can attach to the same overview;
	// each rule has its own ToolOverviewID so we bucket by it directly
	// without needing the overview's toolRuleIds index. Disabled rules
	// are filtered server-side by /api/internal/tool-rules.
	var ruleResp struct {
		Tools []map[string]interface{} `json:"tools"`
	}
	if err := b.do("GET", b.cfg.AIManager.URL, "/api/internal/tool-rules",
		b.cfg.AIManager.InternalServiceKey, auth, nil, &ruleResp); err != nil {
		return nil, fmt.Errorf("enrich tools (rules): %w", err)
	}
	rulesByOverview := make(map[int64][]map[string]interface{}, len(ruleResp.Tools))
	for _, r := range ruleResp.Tools {
		oidRaw, ok := r["toolOverviewId"]
		if !ok || oidRaw == nil {
			continue
		}
		oid, ok := numberAsInt64(oidRaw)
		if !ok {
			continue
		}
		rulesByOverview[oid] = append(rulesByOverview[oid], map[string]interface{}{
			"id":          r["id"],
			"name":        r["name"],
			"description": r["description"],
		})
	}

	capName := make(map[int64]string, len(capResp.ToolCapabilities))
	for _, c := range capResp.ToolCapabilities {
		id, ok := numberAsInt64(c["id"])
		if !ok {
			continue
		}
		if name, ok := c["name"].(string); ok {
			capName[id] = name
		}
	}

	type ovCaps struct{ auto, manual []string }
	overviewCaps := make(map[int64]ovCaps, len(overviewResp.ToolOverview))
	for _, o := range overviewResp.ToolOverview {
		oid, ok := numberAsInt64(o["id"])
		if !ok {
			continue
		}
		var entry ovCaps
		if arr, ok := o["automatedCapabilityIds"].([]interface{}); ok {
			for _, x := range arr {
				if id, ok := numberAsInt64(x); ok {
					if name := capName[id]; name != "" {
						entry.auto = append(entry.auto, name)
					}
				}
			}
		}
		if arr, ok := o["manualCapabilityIds"].([]interface{}); ok {
			for _, x := range arr {
				if id, ok := numberAsInt64(x); ok {
					if name := capName[id]; name != "" {
						entry.manual = append(entry.manual, name)
					}
				}
			}
		}
		overviewCaps[oid] = entry
	}

	for _, tool := range tools {
		tool["automatedCapabilities"] = []string{}
		tool["manualCapabilities"] = []string{}
		tool["rules"] = []map[string]interface{}{}
		if rawTID, ok := tool["toolId"]; ok && rawTID != nil {
			if tid, ok := numberAsInt64(rawTID); ok {
				if entry, ok := overviewCaps[tid]; ok {
					if entry.auto != nil {
						tool["automatedCapabilities"] = entry.auto
					}
					if entry.manual != nil {
						tool["manualCapabilities"] = entry.manual
					}
				}
				if rules, ok := rulesByOverview[tid]; ok {
					tool["rules"] = rules
				}
			}
		}
	}
	return tools, nil
}

// numberAsInt64 normalizes JSON numbers (which decode as float64) and
// real ints to int64. Returns (0, false) for anything that isn't a
// finite number.
func numberAsInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

func (b *Bridge) buildVulnerabilities(vm *goja.Runtime, auth AuthContext) *goja.Object {
	obj := vm.NewObject()
	base := b.cfg.AssetManager.URL
	key := b.cfg.AssetManager.InternalServiceKey

	obj.Set("list", func(call goja.FunctionCall) goja.Value {
		var result struct {
			Vulnerabilities []map[string]interface{} `json:"vulnerabilities"`
		}
		if err := b.do("GET", base, "/api/vulnerabilities", key, auth, nil, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result.Vulnerabilities)
	})

	return obj
}

func (b *Bridge) buildDetections(vm *goja.Runtime, auth AuthContext) *goja.Object {
	obj := vm.NewObject()
	base := b.cfg.AssetManager.URL
	key := b.cfg.AssetManager.InternalServiceKey

	obj.Set("list", func(call goja.FunctionCall) goja.Value {
		var result struct {
			Detections []map[string]interface{} `json:"detections"`
		}
		if err := b.do("GET", base, "/api/detections", key, auth, nil, &result); err != nil {
			throwJS(vm, err)
		}
		return vm.ToValue(result.Detections)
	})

	return obj
}

// ----------------------------------------------------------------------------
// Low-level HTTP helper
// ----------------------------------------------------------------------------

// do performs a single HTTP call to a backend service. The body is JSON-encoded
// if non-nil; the response is JSON-decoded into outPtr if non-nil. Returns an
// error on network failures, non-2xx status codes, or decoding errors.
//
// At DEBUG log level it traces the outgoing request (method, URL, body) and
// the response (status, truncated body). At WARN level it always emits a line
// when the response is a non-2xx status, including the request body that
// caused it — this makes script-level validation failures self-explanatory in
// the log.
func (b *Bridge) do(method, base, path, internalKey string, auth AuthContext, body interface{}, outPtr interface{}) error {
	return b.doWith(b.httpClient, method, base, path, internalKey, auth, body, outPtr)
}

// Call is do for the rest of the module: the one place a request to another
// service is authenticated, whether for a routine's api object or an
// automatic run's own bookkeeping. Keeping a second copy of the
// header logic is how a token once reached a route that only takes the key.
func (b *Bridge) Call(method, base, path, internalKey string, auth AuthContext, body, outPtr interface{}) error {
	return b.do(method, base, path, internalKey, auth, body, outPtr)
}

// doWith is do over a given client, for the one call that needs a longer
// timeout than the rest (see toolClient).
func (b *Bridge) doWith(client *http.Client, method, base, path, internalKey string, auth AuthContext, body interface{}, outPtr interface{}) error {
	url := strings.TrimRight(base, "/") + path

	var reqBody io.Reader
	var bodyBytes []byte
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		bodyBytes = buf
		reqBody = bytes.NewReader(buf)
	}

	logger.Debugf("apibridge → %s %s body=%s", method, url, truncate(string(bodyBytes), 500))

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	if internalKey != "" {
		req.Header.Set("X-Internal-Service-Key", internalKey)
	}
	if auth.UserID > 0 {
		req.Header.Set("X-User-ID", fmt.Sprintf("%d", auth.UserID))
	}
	if auth.UserRole != "" {
		req.Header.Set("X-User-Role", auth.UserRole)
	}

	resp, err := client.Do(req)
	if err != nil {
		logger.Warnf("apibridge ← %s %s network error: %v", method, url, err)
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	// Bounded: a backend's answer is read into memory and then handed to the
	// routine, so one oversized reply — a faulty backend, a very large tenant —
	// must fail the call rather than the daemon. One byte past the cap tells an
	// over-limit body apart from one exactly at it.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%s %s: reading the response: %w", method, url, err)
	}
	if len(respBody) > MaxResponseBytes {
		logger.Warnf("apibridge ← %s %s: response larger than %d bytes, refused", method, url, MaxResponseBytes)
		return fmt.Errorf("%s %s: response larger than %d MiB", method, url, MaxResponseBytes>>20)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Always log the failing request body alongside the response so that
		// validation failures (e.g. "Assets hosts must be at least 1") show
		// the value that triggered them.
		logger.Warnf("apibridge ← %s %s status=%d req=%s resp=%s",
			method, url, resp.StatusCode,
			truncate(string(bodyBytes), 500),
			truncate(string(respBody), 500))
		return fmt.Errorf("%s %s: status %d: %s", method, url, resp.StatusCode, truncate(string(respBody), 500))
	}

	logger.Debugf("apibridge ← %s %s status=%d resp=%s",
		method, url, resp.StatusCode, truncate(string(respBody), 500))

	if outPtr != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, outPtr); err != nil {
			return fmt.Errorf("decode response from %s %s: %w", method, url, err)
		}
	}

	return nil
}

// exportArg returns the goja argument at index as a Go interface{}, or nil if absent.
func exportArg(call goja.FunctionCall, index int) interface{} {
	v := call.Argument(index)
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return v.Export()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
