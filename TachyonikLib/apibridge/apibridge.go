// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apibridge gives a generated routine the platform's API: an `api`
// object in a goja runtime with one function per bridge operation of the
// platform API catalogue, `api.<namespace>.<verb>(args)`, which makes the
// documented HTTP call for the user the evaluation is for
// (docs/specs/01-platform-api-catalogue.md, §5).
//
// The functions are built from the managers' API documents
// (tachyonik/lib/apicatalogue), not written here: a manager that documents and
// registers an operation makes it available without a change to the module
// that runs the routine (PA-BR-01).
//
// A function takes one optional object. Its members are the operation's path
// and query parameters by name, and `body` for a write operation's request
// body. A member the operation does not have is an error, so a misspelt
// argument fails instead of being dropped. What the routine cannot choose, the
// bridge sets: the parameters and body fields that name the user (PA-BR-03),
// the method, path and host (PA-BR-02), and the service key.
//
// An answer reaches the routine only after it has been checked against the
// operation's schema and reduced to the fields the schema documents
// (PA-BR-04, -06). A failed call throws a JavaScript Error naming the
// operation.
//
// Within one Pass, the same read with the same arguments for the same user is
// answered from the first answer (PA-BR-05); a write clears that user's
// answers, so a read after it sees it (PA-BR-08).
//
// A Pass can also answer from the documents' examples for one mock scenario
// instead of calling anyone, recording what the routine called and read. That
// is how a routine is validated (PA-VAL-02).
package apibridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"tachyonik/lib/apicatalogue"
	"tachyonik/lib/jsctxtrace"
)

// Profile says which operations a bridge offers (PA-BR-08).
type Profile int

const (
	// ReadOnly offers the `read` operations: ActionGenerator.
	ReadOnly Profile = iota
	// ReadWrite offers `read` and `write` operations: ActionExecutor.
	ReadWrite
)

// Module is one manager's registered document and where to reach it.
type Module struct {
	Name       string
	Document   *apicatalogue.Document
	BaseURL    string
	ServiceKey string
}

// Options tune a Bridge. Zero values take the defaults.
type Options struct {
	// CallTimeout bounds one HTTP call (PA-BR-10). Default 3 seconds. A
	// context given to Pass.SetContext can shorten it further.
	CallTimeout time.Duration
	// MaxAnswerBytes bounds the body read from one answer. Default 64 MiB.
	MaxAnswerBytes int64
	// HTTPClient replaces the default client (tests). Redirects must not be
	// followed: the request carries the service key.
	HTTPClient *http.Client
}

// Bridge is the set of functions built from a catalogue for one profile.
type Bridge struct {
	profile Profile
	ops     map[string]*boundOp // by operation id
	spaces  map[string][]string // namespace -> verbs, sorted
	opts    Options
	client  *http.Client
}

type boundOp struct {
	module *Module
	op     *apicatalogue.Operation
}

// New builds a bridge from the modules' documents. Two modules offering the
// same namespace or operation is an error: a name must mean one operation.
func New(modules []Module, profile Profile, opts Options) (*Bridge, error) {
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = 3 * time.Second
	}
	if opts.MaxAnswerBytes <= 0 {
		opts.MaxAnswerBytes = 64 << 20
	}
	b := &Bridge{profile: profile, ops: map[string]*boundOp{}, spaces: map[string][]string{}, opts: opts}
	b.client = opts.HTTPClient
	if b.client == nil {
		b.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	owner := map[string]string{}
	for i := range modules {
		m := &modules[i]
		if m.Document == nil {
			continue
		}
		for _, op := range m.Document.BridgeOperations(profile == ReadWrite) {
			ns := op.Namespace()
			if prev, ok := owner[ns]; ok && prev != m.Name {
				return nil, fmt.Errorf("namespace %q is offered by %s and %s", ns, prev, m.Name)
			}
			owner[ns] = m.Name
			if _, dup := b.ops[op.ID]; dup {
				return nil, fmt.Errorf("operation %s is offered twice", op.ID)
			}
			b.ops[op.ID] = &boundOp{module: m, op: op}
			_, verb, _ := strings.Cut(op.ID, ".")
			b.spaces[ns] = append(b.spaces[ns], verb)
		}
	}
	for ns := range b.spaces {
		sort.Strings(b.spaces[ns])
	}
	return b, nil
}

// Operations returns the ids of the operations the bridge offers, sorted.
func (b *Bridge) Operations() []string {
	ids := make([]string, 0, len(b.ops))
	for id := range b.ops {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Operation returns the documented operation behind an id, or nil.
func (b *Bridge) Operation(id string) *apicatalogue.Operation {
	if bo := b.ops[id]; bo != nil {
		return bo.op
	}
	return nil
}

// Pass is one evaluation pass: the cache of answers it shares between the
// routines it runs (PA-BR-05). Not for use across passes — data changes.
type Pass struct {
	bridge   *Bridge
	scenario string // non-empty: answer from the examples (mock mode)

	mu    sync.Mutex
	ctx   context.Context
	cache map[string]interface{} // key -> filtered answer
	calls []Call
}

// Identity is who a routine's calls are made for: the user, and the user's
// role where a manager decides on it (x-bridge-bound: role). An empty role
// is not sent.
type Identity struct {
	UserID int64
	Role   string
}

// Call is one call a routine made, as a Pass records it.
type Call struct {
	Operation string
	UserID    int64
	// Answer is a write call's answer, as the routine got it; nil for reads
	// and failed calls. A run's audit names what its writes did from it —
	// which tools ran, for instance.
	Answer interface{}
	// Err is why a call that was made failed, or nil.
	Err error
	// Problem is why the call was refused before it was made — an unknown
	// operation, an argument the operation does not have — or "".
	Problem string
	// trace records what the routine read of a mock answer.
	trace *jsctxtrace.Trace
}

// NewPass starts a pass that calls the managers.
func (b *Bridge) NewPass() *Pass {
	return &Pass{bridge: b, ctx: context.Background(), cache: map[string]interface{}{}}
}

// NewMockPass starts a pass that answers from the documents' examples for one
// mock scenario, and calls no one (PA-VAL-02).
func (b *Bridge) NewMockPass(scenario string) *Pass {
	p := b.NewPass()
	p.scenario = scenario
	return p
}

// SetContext sets the context the next calls are made under. A routine
// runner sets one per guarded routine call, ending with its budget, so a slow
// manager cannot hold a routine past it (PA-BR-10).
func (p *Pass) SetContext(ctx context.Context) {
	p.mu.Lock()
	p.ctx = ctx
	p.mu.Unlock()
}

// Calls returns the calls made so far.
func (p *Pass) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Call(nil), p.calls...)
}

// Violations returns, for a mock pass, what the routine did that its
// documentation does not allow (PA-VAL-02): a call refused before it was
// made, and a field read that the answer's schema does not have — as
// "<operation>: <field path>". Sorted, without repeats.
func (p *Pass) Violations() []string {
	seen := map[string]bool{}
	for _, c := range p.Calls() {
		if c.Problem != "" {
			seen[c.Operation+": "+c.Problem] = true
			continue
		}
		if c.trace == nil {
			continue
		}
		op := p.bridge.Operation(c.Operation)
		for _, path := range c.trace.Missing() {
			if op == nil || op.Response == nil || !op.Response.HasPath(path) {
				seen[c.Operation+": reads "+path+", which the answer does not have"] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Reads returns, for a mock pass, the fields read per operation, as
// "<operation>:<path>", sorted. Paths are leaves (see jsctxtrace.Leaves).
func (p *Pass) Reads() []string {
	set := map[string]bool{}
	for _, c := range p.Calls() {
		if c.trace == nil {
			continue
		}
		for _, path := range jsctxtrace.Leaves(c.trace.Read()) {
			set[c.Operation+":"+path] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Object builds the `api` object for one runtime and one user, without a
// role. Every evaluation gets a fresh runtime (AG-SEC-12), and so a fresh
// object; the cache is the Pass's.
func (p *Pass) Object(vm *goja.Runtime, userID int64) goja.Value {
	return p.ObjectFor(vm, Identity{UserID: userID})
}

// ObjectFor builds the `api` object for one runtime and one identity.
func (p *Pass) ObjectFor(vm *goja.Runtime, who Identity) goja.Value {
	api := vm.NewObject()
	b := p.bridge
	names := make([]string, 0, len(b.spaces))
	for ns := range b.spaces {
		names = append(names, ns)
	}
	sort.Strings(names)
	for _, ns := range names {
		obj := vm.NewObject()
		for _, verb := range b.spaces[ns] {
			id := ns + "." + verb
			_ = obj.Set(verb, func(call goja.FunctionCall) goja.Value {
				return p.invoke(vm, id, who, call)
			})
		}
		_ = api.Set(ns, obj)
	}
	return api
}

func (p *Pass) invoke(vm *goja.Runtime, id string, who Identity, call goja.FunctionCall) goja.Value {
	bo := p.bridge.ops[id]
	userID := who.UserID
	var args map[string]interface{}
	if a := call.Argument(0); !goja.IsUndefined(a) && !goja.IsNull(a) {
		m, ok := a.Export().(map[string]interface{})
		if !ok {
			p.record(Call{Operation: id, UserID: userID, Problem: "its argument is not an object"})
			panic(vm.NewGoError(fmt.Errorf("api.%s: the argument must be an object of parameters", id)))
		}
		args = m
	}
	req, problem := p.bridge.prepare(bo, who, args)
	if problem != "" {
		p.record(Call{Operation: id, UserID: userID, Problem: problem})
		panic(vm.NewGoError(fmt.Errorf("api.%s: %s", id, problem)))
	}

	write := bo.op.Bridge == apicatalogue.ExposeWrite
	key := fmt.Sprintf("%d|%s|%s", userID, id, req.cacheKey())

	if p.scenario != "" {
		answer, err := p.mockAnswer(bo)
		if err != nil {
			c := Call{Operation: id, UserID: userID}
			if !errors.Is(err, errNotFound) {
				c.Problem = err.Error()
			}
			p.record(c)
			panic(vm.NewGoError(fmt.Errorf("api.%s: %w", id, err)))
		}
		tr := jsctxtrace.New()
		p.record(Call{Operation: id, UserID: userID, trace: tr})
		return tr.Wrap(vm, answer)
	}

	if !write {
		p.mu.Lock()
		cached, ok := p.cache[key]
		p.mu.Unlock()
		if ok {
			p.record(Call{Operation: id, UserID: userID})
			return vm.ToValue(deepCopy(cached))
		}
	}

	p.mu.Lock()
	ctx := p.ctx
	p.mu.Unlock()
	answer, err := p.bridge.do(ctx, bo, req)
	c := Call{Operation: id, UserID: userID, Err: err}
	if write && err == nil {
		c.Answer = answer
	}
	p.record(c)
	if err != nil {
		panic(vm.NewGoError(fmt.Errorf("api.%s: %w", id, err)))
	}
	p.mu.Lock()
	if write {
		prefix := fmt.Sprintf("%d|", userID)
		for k := range p.cache {
			if strings.HasPrefix(k, prefix) {
				delete(p.cache, k)
			}
		}
	} else {
		p.cache[key] = answer
	}
	p.mu.Unlock()
	// A copy: goja exposes a Go map as a live object, so a routine writing
	// to its answer would otherwise change the cached one the next rule gets.
	return vm.ToValue(deepCopy(answer))
}

func (p *Pass) record(c Call) {
	p.mu.Lock()
	p.calls = append(p.calls, c)
	p.mu.Unlock()
}

func (p *Pass) mockAnswer(bo *boundOp) (interface{}, error) {
	if bo.op.Bridge == apicatalogue.ExposeWrite {
		// A write in validation must not pretend to have changed anything a
		// later read would then show; it answers what its example says, or
		// an empty object.
		if ex, ok := bo.op.Examples[p.scenario]; ok {
			return bo.op.Response.Filter(ex), nil
		}
		return map[string]interface{}{}, nil
	}
	ex, ok := bo.op.Examples[p.scenario]
	if !ok {
		if bo.op.AnswersNotFound() {
			return nil, errNotFound
		}
		return nil, fmt.Errorf("no mock answer for scenario %q", p.scenario)
	}
	return bo.op.Response.Filter(ex), nil
}

// errNotFound is a mock pass's answer where the scenario has no such record.
// Thrown like a live 404, and not a violation: the routine did nothing wrong.
var errNotFound = errors.New("answered 404: not found")

// request is a call worked out from the documentation and the arguments.
type request struct {
	path   string
	query  url.Values
	header http.Header
	body   interface{}
}

func (r request) cacheKey() string {
	b, _ := json.Marshal(r.body)
	h, _ := json.Marshal(r.header)
	return r.path + "?" + r.query.Encode() + "#" + string(h) + "#" + string(b)
}

// prepare builds the request, or says why it cannot be made. The routine's
// arguments are used only where the documentation has a parameter for them,
// and never for a bound one.
func (b *Bridge) prepare(bo *boundOp, who Identity, args map[string]interface{}) (request, string) {
	userID := who.UserID
	op := bo.op
	req := request{path: op.Path, query: url.Values{}, header: http.Header{}}
	known := map[string]bool{}
	for _, prm := range op.Parameters {
		// A header parameter is the bridge's to set, never the routine's: a
		// routine choosing headers could choose whom a call acts for.
		if prm.In == "header" {
			switch {
			case prm.Bound == apicatalogue.BoundUser:
				req.header.Set(prm.Name, strconv.FormatInt(userID, 10))
			case prm.Bound == apicatalogue.BoundRole && who.Role != "":
				req.header.Set(prm.Name, who.Role)
			}
			continue
		}
		if prm.In != "path" && prm.In != "query" {
			continue
		}
		// Never sent, and not the routine's to send: an argument of that
		// name is refused below as unknown.
		if prm.Bound == apicatalogue.BoundOmit {
			continue
		}
		known[prm.Name] = true
		var value interface{}
		switch {
		case prm.Bound == apicatalogue.BoundUser:
			value = userID
		case prm.Bound == apicatalogue.BoundRole:
			if who.Role != "" {
				value = who.Role
			}
		default:
			if v, ok := args[prm.Name]; ok && v != nil {
				value = v
			}
		}
		if value == nil {
			if prm.Required {
				return req, fmt.Sprintf("parameter %q is required", prm.Name)
			}
			continue
		}
		text, err := paramText(prm, value)
		if err != nil {
			return req, fmt.Sprintf("parameter %q: %v", prm.Name, err)
		}
		if prm.In == "path" {
			req.path = strings.ReplaceAll(req.path, "{"+prm.Name+"}", url.PathEscape(text))
		} else {
			req.query.Set(prm.Name, text)
		}
	}
	if op.RequestBody != nil {
		known["body"] = true
		body := args["body"]
		if body == nil {
			body = map[string]interface{}{}
		}
		if m, ok := body.(map[string]interface{}); ok {
			m = copyMap(m)
			for name, fs := range op.RequestBody.Properties {
				switch {
				case fs.Bound == apicatalogue.BoundUser:
					m[name] = userID
				case fs.Bound == apicatalogue.BoundRole && who.Role != "":
					m[name] = who.Role
				}
			}
			body = m
		}
		req.body = body
	}
	for name := range args {
		if !known[name] {
			return req, fmt.Sprintf("the operation has no parameter %q", name)
		}
	}
	if strings.Contains(req.path, "{") {
		return req, "a path parameter is missing"
	}
	return req, ""
}

func paramText(prm *apicatalogue.Parameter, v interface{}) (string, error) {
	if prm.JSON {
		b, err := json.Marshal(v)
		return string(b), err
	}
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case int:
		return strconv.Itoa(x), nil
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return strconv.FormatInt(int64(x), 10), nil
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("a %T cannot be sent as a parameter", v)
}

// do makes the call and returns the checked, filtered answer.
func (b *Bridge) do(ctx context.Context, bo *boundOp, req request) (interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, b.opts.CallTimeout)
	defer cancel()

	u := strings.TrimRight(bo.module.BaseURL, "/") + req.path
	if len(req.query) > 0 {
		u += "?" + req.query.Encode()
	}
	var body io.Reader
	if req.body != nil {
		data, err := json.Marshal(req.body)
		if err != nil {
			return nil, fmt.Errorf("the body cannot be sent: %w", err)
		}
		body = bytes.NewReader(data)
	}
	hreq, err := http.NewRequestWithContext(ctx, bo.op.Method, u, body)
	if err != nil {
		return nil, err
	}
	for name, values := range req.header {
		for _, v := range values {
			hreq.Header.Add(name, v)
		}
	}
	hreq.Header.Set("X-Internal-Service-Key", bo.module.ServiceKey)
	hreq.Header.Set("Accept", "application/json")
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(hreq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s did not answer in time", bo.module.Name)
		}
		return nil, fmt.Errorf("%s cannot be reached", bo.module.Name)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, b.opts.MaxAnswerBytes+1))
	if err != nil {
		return nil, fmt.Errorf("the answer of %s could not be read", bo.module.Name)
	}
	if int64(len(data)) > b.opts.MaxAnswerBytes {
		return nil, fmt.Errorf("the answer of %s is larger than %d bytes", bo.module.Name, b.opts.MaxAnswerBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s answered %d%s", bo.module.Name, resp.StatusCode, errorText(data))
	}
	var answer interface{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &answer); err != nil {
			return nil, fmt.Errorf("the answer of %s is not JSON", bo.module.Name)
		}
	}
	if bo.op.Response != nil {
		if err := bo.op.Response.Validate(answer); err != nil {
			return nil, fmt.Errorf("the answer of %s does not match its documentation: %w", bo.module.Name, err)
		}
		answer = bo.op.Response.Filter(answer)
	}
	return answer, nil
}

// errorText lifts a short message out of an error answer: the `error` field
// of {"error": "..."} or a short plain text, at most 200 characters.
func errorText(data []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	msg := ""
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		msg = e.Error
	} else if s := strings.TrimSpace(string(data)); s != "" && !strings.ContainsAny(s, "<{") {
		msg = s
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	if msg == "" {
		return ""
	}
	return ": " + msg
}

// deepCopy copies a decoded JSON value, so no two routines share one.
func deepCopy(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[k] = deepCopy(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, val := range x {
			out[i] = deepCopy(val)
		}
		return out
	}
	return v
}

func copyMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
