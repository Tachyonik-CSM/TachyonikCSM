// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the registration of an API document with AIManager: what is sent,
// that it is retried until taken, that a refusal is final, and that it stops
// with its context (PA-REG-01).

package apiregister

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func config(url string) Config {
	return Config{Module: "assetmanager", Version: "1.2.3", Document: []byte("openapi: 3.1.0\n"),
		AIManagerURL: url, ServiceKey: "svc", FirstRetry: 5 * time.Millisecond, MaxRetry: 10 * time.Millisecond}
}

func wait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("registration did not finish")
		return nil
	}
}

// PA-REG-01: registered with module, version and digest; retried until
// AIManager takes it.
func TestRegistersAndRetries(t *testing.T) {
	var calls atomic.Int32
	var got Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Method != "PUT" || r.URL.Path != "/api/internal/api-catalogue/assetmanager" || r.Header.Get("X-Internal-Service-Key") != "svc" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := wait(t, Start(context.Background(), config(srv.URL))); err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
	if got.Version != "1.2.3" || got.Document != "openapi: 3.1.0\n" || got.SHA256 != Digest([]byte("openapi: 3.1.0\n")) {
		t.Errorf("request body = %+v", got)
	}
}

// A refusal of the document is final, and says why.
func TestRefusalIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"namespace \"assets\" is registered by resourcemanager"}`))
	}))
	defer srv.Close()

	err := wait(t, Start(context.Background(), config(srv.URL)))
	if err == nil || !strings.Contains(err.Error(), `namespace "assets"`) {
		t.Errorf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := Start(ctx, config("http://127.0.0.1:1"))
	cancel()
	if err := wait(t, done); err == nil {
		t.Error("want the context's error")
	}
}

// An AIManager that does not answer is named with its address and the reason,
// so a configuration pointing at the wrong place shows in the log.
func TestAnUnreachableAIManagerIsNamed(t *testing.T) {
	_, err := register(context.Background(), &http.Client{Timeout: time.Second}, config("http://127.0.0.1:1/"))
	if err == nil {
		t.Fatal("registered with nothing listening")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "AIManager at http://127.0.0.1:1 cannot be reached: ") || !strings.Contains(msg, "refused") {
		t.Errorf("message %q, want the address and the reason", msg)
	}
	if strings.Contains(msg, "/api/internal/api-catalogue") {
		t.Errorf("message %q repeats the request path", msg)
	}
}
