// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package restclient carries the request mechanics every module's outbound API
// clients share: join a path onto the base URL, attach the internal service
// key, send, check the status, and decode JSON.
//
// The managers all sit behind the same X-Internal-Service-Key scheme, so
// collecting the plumbing here leaves each service client holding only its
// wire types and one line per endpoint — the alternative being the same six
// steps rewritten per endpoint, which is how these clients grew to several
// hundred lines apiece.
//
// Redirects are never followed. Go strips Authorization across hosts but
// forwards custom headers verbatim, so a 302 would hand X-Internal-Service-Key
// to whatever host it names; no manager legitimately redirects, so the safe
// behaviour costs nothing.
package restclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"tachyonik/lib/internal/safehttp"
	"tachyonik/lib/logger"
)

// StatusError reports a response that did not carry the expected status. It is
// a typed error so a caller can react to one specific code — the AI lookups
// turn 404 into their own "not found" wording — without matching on message
// text.
//
// Message is the reason the upstream gave, when it gave one: the "error" of
// the {"error": "…"} body every manager refuses with, or else the first line
// of a plain-text body. Without it a refusal reads "unexpected status code:
// 403" in a log or an audit entry, and nobody can tell a reached limit from a
// missing permission.
type StatusError struct {
	Method     string
	URL        string
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("unexpected status code: %d", e.StatusCode)
	}
	return fmt.Sprintf("unexpected status code: %d: %s", e.StatusCode, e.Message)
}

// maxReasonBytes bounds how much of a refusal's body is read for its reason,
// and maxReasonRunes how much of the reason is kept: enough for any manager's
// message, not enough for a misbehaving upstream to flood a log line.
const (
	maxReasonBytes = 4 << 10
	maxReasonRunes = 300
)

// reasonOf reads the reason from a refusal's body: the "error" field of a JSON
// body, else the first line of the text; "" when there is none. Control
// characters are dropped, so the reason cannot break a log line apart.
func reasonOf(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxReasonBytes))
	if err != nil && len(raw) == 0 {
		return ""
	}
	var reply struct {
		Error string `json:"error"`
	}
	text := string(raw)
	if json.Unmarshal(raw, &reply) == nil {
		text = reply.Error
	} else if strings.HasPrefix(strings.TrimSpace(text), "<") {
		return "" // an HTML error page from a proxy in between says nothing useful
	}
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(strings.ToValidUTF8(text, ""))
	if r := []rune(text); len(r) > maxReasonRunes {
		text = string(r[:maxReasonRunes]) + "…"
	}
	return text
}

// MaxResponseBytes bounds a decoded JSON response. Rule lists and routine
// bodies are far smaller; this exists so a malfunctioning or hostile upstream
// cannot grow the daemon's heap without limit through a reply.
const MaxResponseBytes = 32 << 20 // 32 MiB

// Client issues authenticated JSON requests against one service.
type Client struct {
	baseURL    string
	serviceKey string
	httpClient *http.Client
}

// New creates a client for baseURL. serviceKey may be empty, in which case no
// authentication header is sent.
func New(baseURL, serviceKey string, timeout time.Duration) *Client {
	// Say so when the key would travel in the clear. Every other package that
	// sends this credential warns — heartbeat, wswatcher, the AI clients, the
	// shared SystemManager client — and this one carries most of the platform's
	// internal traffic. Loopback URLs are the common case and harmless; a
	// manager on another host over http:// is the case worth noticing, and
	// nothing was saying it.
	if safehttp.CredentialExposed(baseURL, serviceKey != "") {
		logger.Warnf("REST client configured with an internal service key over a non-TLS URL (%s) — the key will be sent in cleartext", baseURL)
	}
	return &Client{
		baseURL:    baseURL,
		serviceKey: serviceKey,
		httpClient: &http.Client{
			Timeout: timeout,
			// Never follow redirects. Go strips Authorization across hosts but
			// forwards custom headers verbatim, so a 302 would hand
			// X-Internal-Service-Key to whatever host it names. Neither
			// ResourceManager nor AIManager legitimately redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Request issues method against path (joined onto the base URL) and returns the
// raw response, for callers that need the body or headers themselves — those
// callers own resp.Body. body, when non-nil, is sent JSON-encoded. A status
// other than wantStatus closes the body and returns a *StatusError, with the
// reason the upstream gave, so a failed call never leaks a connection back to
// the caller.
//
// alsoOK names further statuses that count as success, for an endpoint whose
// success has more than one shape — AssetManager answers a create with 201 for
// a new asset and 200 for one that already existed and was linked instead.
func (c *Client) Request(method, path string, body any, wantStatus int, alsoOK ...int) (*http.Response, error) {
	url := c.baseURL + path

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.serviceKey != "" {
		req.Header.Set("X-Internal-Service-Key", c.serviceKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	if !statusOK(resp.StatusCode, wantStatus, alsoOK) {
		reason := reasonOf(resp.Body)
		resp.Body.Close()
		return nil, &StatusError{Method: method, URL: url, StatusCode: resp.StatusCode, Message: reason}
	}
	return resp, nil
}

// JSON issues a request and decodes the response into out. Pass a nil out when
// the call has no response body worth reading — the body is drained and closed
// either way. alsoOK is as for Request.
func (c *Client) JSON(method, path string, body, out any, wantStatus int, alsoOK ...int) error {
	resp, err := c.Request(method, path, body, wantStatus, alsoOK...)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if out == nil {
		return nil
	}

	// Read one byte past the cap so an over-limit body is reported as such
	// rather than handed to the decoder as truncated JSON, which would surface
	// as a confusing "unexpected EOF" blamed on the upstream's formatting.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if len(raw) > MaxResponseBytes {
		return fmt.Errorf("response body exceeds the %d byte limit", MaxResponseBytes)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	return nil
}

// statusOK reports whether got is the wanted status or one of the others
// accepted.
func statusOK(got, want int, alsoOK []int) bool {
	if got == want {
		return true
	}
	for _, s := range alsoOK {
		if got == s {
			return true
		}
	}
	return false
}
