// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests reading the instance mode from SystemManager: the mode in any case,
// a value that names no mode refused at once, and waiting until SystemManager
// answers.

package systemmanager

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstanceMode(t *testing.T) {
	for answer, want := range map[string]string{`{"instanceMode":"saas"}`: "saas", `{"instanceMode":" Appliance "}`: "appliance"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/internal/workspace" || r.Header.Get("X-Internal-Service-Key") != "k" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(answer))
		}))
		got, err := NewClient(srv.URL, "k").InstanceMode()
		srv.Close()
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", answer, got, err, want)
		}
	}
}

func TestABadModeIsRefusedAtOnce(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Write([]byte(`{"instanceMode":"hybrid"}`))
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, "").WaitForInstanceMode(context.Background(), time.Millisecond, time.Hour)
	if !errors.Is(err, ErrBadMode) || asked.Load() != 1 {
		t.Errorf("%v after %d questions, want ErrBadMode after 1", err, asked.Load())
	}
}

func TestWaitForInstanceModeUntilSystemManagerAnswers(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asked.Add(1) < 3 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"instanceMode":"saas"}`))
	}))
	defer srv.Close()
	mode, err := NewClient(srv.URL, "").WaitForInstanceMode(context.Background(), time.Millisecond, time.Millisecond)
	if err != nil || mode != "saas" || asked.Load() != 3 {
		t.Errorf("%q %v after %d questions", mode, err, asked.Load())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := NewClient("http://127.0.0.1:1", "").WaitForInstanceMode(ctx, time.Millisecond, time.Hour); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("no SystemManager: %v, want the context's end", err)
	}
}

func TestStartupInstanceModeNeedsAURL(t *testing.T) {
	if _, err := StartupInstanceMode(context.Background(), " ", "", "", ""); err == nil {
		t.Error("started without a SystemManager URL")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"instanceMode":"saas"}`))
	}))
	defer srv.Close()
	// The service's own setting is not used, whatever it says.
	if mode, err := StartupInstanceMode(context.Background(), srv.URL, "", "appliance", "instance.mode in config.yaml"); err != nil || mode != "saas" {
		t.Errorf("%q %v, want saas", mode, err)
	}
}
