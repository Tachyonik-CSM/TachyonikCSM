// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the shared request path: path joining, the internal-service-key
// header, JSON encode/decode of request and response bodies, and the typed
// status error that lets a caller single out a 404. Also the two precautions
// the path exists to apply uniformly — that a cross-host redirect is refused
// rather than followed with the service key attached, and that an oversized
// response is rejected instead of buffered without limit.

package restclient

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type payload struct {
	Name string `json:"name"`
}

func TestJSON_DecodesResponseAndSendsAuthHeader(t *testing.T) {
	var gotPath, gotKey, gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotMethod = r.URL.Path, r.Header.Get("X-Internal-Service-Key"), r.Method
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"name":"routine-42"}`)
	}))
	defer srv.Close()

	var out payload
	if err := New(srv.URL, "SECRET", time.Second).JSON("GET", "/api/internal/routines/42", nil, &out, http.StatusOK); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	if gotMethod != "GET" {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/api/internal/routines/42" {
		t.Errorf("path = %q, want the path joined onto the base URL", gotPath)
	}
	if gotKey != "SECRET" {
		t.Errorf("X-Internal-Service-Key = %q, want SECRET", gotKey)
	}
	if out.Name != "routine-42" {
		t.Errorf("decoded name = %q, want routine-42", out.Name)
	}
}

// An empty service key is the unauthenticated case: send no header at all
// rather than an empty one, which a server could read as a failed auth attempt.
func TestJSON_OmitsAuthHeaderWhenKeyEmpty(t *testing.T) {
	var present bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["X-Internal-Service-Key"]
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	var out payload
	if err := New(srv.URL, "", time.Second).JSON("GET", "/x", nil, &out, http.StatusOK); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if present {
		t.Error("auth header was sent despite an empty service key")
	}
}

func TestJSON_EncodesRequestBody(t *testing.T) {
	var gotBody, gotContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotContentType = string(b), r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"name":"created"}`)
	}))
	defer srv.Close()

	var out payload
	err := New(srv.URL, "k", time.Second).
		JSON("POST", "/api/internal/routines", payload{Name: "gen"}, &out, http.StatusCreated)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	if gotBody != `{"name":"gen"}` {
		t.Errorf("body = %q, want the marshalled payload", gotBody)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if out.Name != "created" {
		t.Errorf("decoded name = %q, want created", out.Name)
	}
}

// The AI lookups turn a 404 into their own wording, so the status has to reach
// them as a typed error rather than baked into a message string.
func TestJSON_StatusErrorCarriesCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	var out payload
	err := New(srv.URL, "k", time.Second).JSON("GET", "/api/internal/ais/7", nil, &out, http.StatusOK)
	if err == nil {
		t.Fatal("expected an error for a 404")
	}

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error %v is not a *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", statusErr.StatusCode, http.StatusNotFound)
	}
}

// A wrong status must not be reported as success even when the body would
// decode cleanly — the expected status is part of the contract.
func TestJSON_UnexpectedStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // caller wants 201
		io.WriteString(w, `{"name":"x"}`)
	}))
	defer srv.Close()

	var out payload
	if err := New(srv.URL, "k", time.Second).JSON("POST", "/x", payload{}, &out, http.StatusCreated); err == nil {
		t.Fatal("expected an error when the status is not the wanted one")
	}
}

// A nil out is the no-response-body case (ClearGenerateRequest, UpdateSource):
// the call still succeeds and the body is closed.
func TestJSON_NilOutDiscardsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ignored":true}`)
	}))
	defer srv.Close()

	if err := New(srv.URL, "k", time.Second).JSON("PATCH", "/x", payload{Name: "n"}, nil, http.StatusOK); err != nil {
		t.Fatalf("JSON with nil out: %v", err)
	}
}

// Request hands the live response to the caller (DownloadSourceFile needs the
// body and the X-Original-Filename header).
func TestRequest_ReturnsLiveResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Original-Filename", "report.pdf")
		io.WriteString(w, "%PDF-1.4 body")
	}))
	defer srv.Close()

	resp, err := New(srv.URL, "k", time.Second).Request("GET", "/api/sources/1/file", nil, http.StatusOK)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Original-Filename"); got != "report.pdf" {
		t.Errorf("filename header = %q, want report.pdf", got)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "%PDF-1.4 body" {
		t.Errorf("body = %q, want the raw bytes", b)
	}
}

// Go strips Authorization on a cross-host redirect but forwards custom headers
// verbatim, so following one would hand X-Internal-Service-Key to the target.
func TestRequest_DoesNotFollowRedirects(t *testing.T) {
	var reached bool
	var leaked string

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		leaked = r.Header.Get("X-Internal-Service-Key")
	}))
	defer target.Close()

	// Same server, different hostname — a genuinely cross-host redirect.
	elsewhere := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere, http.StatusFound)
	}))
	defer origin.Close()

	var out payload
	err := New(origin.URL, "SECRET", time.Second).JSON("GET", "/api/sources", nil, &out, http.StatusOK)
	if err == nil {
		t.Fatal("expected the unfollowed 302 to surface as an error")
	}
	if reached {
		t.Fatalf("redirect was followed; target received key %q", leaked)
	}

	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusFound {
		t.Fatalf("err = %v, want a *StatusError carrying 302", err)
	}
}

// A hostile or malfunctioning upstream must not be able to grow the heap
// without limit through a reply.
func TestJSON_RejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"`))
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for written := 0; written <= MaxResponseBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		w.Write([]byte(`"}`))
	}))
	defer srv.Close()

	var out payload
	err := New(srv.URL, "k", 30*time.Second).JSON("GET", "/x", nil, &out, http.StatusOK)
	if err == nil {
		t.Fatal("expected an oversized body to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want the size-limit error rather than a decode failure", err)
	}
}
