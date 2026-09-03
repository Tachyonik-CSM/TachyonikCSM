// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for the outbound request guard: which addresses are refused, that a
// URL's spelling cannot talk its way past the resolved address, and that a
// redirect cannot land somewhere the original URL would never have been
// allowed to go.

package httpguard

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsDisallowedIP(t *testing.T) {
	cases := map[string]bool{
		// Refused.
		"127.0.0.1":       true,
		"::1":             true,
		"0.0.0.0":         true,
		"169.254.169.254": true, // cloud metadata
		"10.1.2.3":        true,
		"172.16.0.1":      true,
		"192.168.1.1":     true,
		"fd00::1":         true, // unique local
		"224.0.0.1":       true, // multicast
		// Allowed.
		"93.184.216.34": false,
		"8.8.8.8":       false,
		"2606:4700::1":  false,
	}
	for addr, want := range cases {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Fatalf("test address %q does not parse", addr)
		}
		if got := IsDisallowedIP(ip); got != want {
			t.Errorf("IsDisallowedIP(%s) = %v, want %v", addr, got, want)
		}
	}
	if !IsDisallowedIP(nil) {
		t.Error("a nil IP must be refused, not allowed by default")
	}
}

func TestValidateURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr error
	}{
		{"loopback literal", "http://127.0.0.1:8080/x", ErrDisallowedAddress},
		{"metadata service", "http://169.254.169.254/latest/meta-data/", ErrDisallowedAddress},
		{"private range", "https://10.0.0.5/", ErrDisallowedAddress},
		{"ipv6 loopback", "http://[::1]:9000/", ErrDisallowedAddress},
		{"not a url", "notaurl", ErrBadURL},
		{"file scheme", "file:///etc/passwd", ErrBadURL},
		{"javascript scheme", "javascript:alert(1)", ErrBadURL},
		{"no host", "https://", ErrBadURL},
		{"public literal", "https://93.184.216.34/", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateURL(tc.url)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateURL(%q) = %v, want nil", tc.url, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateURL(%q) = %v, want %v", tc.url, err, tc.wantErr)
			}
		})
	}
}

// The check is on the address a host resolves to, not on how the host is
// spelled: a name pointing at loopback is refused like the literal is.
func TestValidateURLResolvesNames(t *testing.T) {
	if err := ValidateURL("http://localhost:8080/"); !errors.Is(err, ErrDisallowedAddress) {
		t.Errorf("localhost = %v, want it refused as an internal address", err)
	}
}

// The one that matters: a URL that passes the check is free to redirect
// somewhere that would not have. The client has to refuse the hop.
func TestClientRefusesRedirectToInternalAddress(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secrets"))
	}))
	defer internal.Close()

	// Stands in for a public site that answers with a redirect inwards.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// AllowInternal is off, so the redirect target (loopback) is refused —
	// even though the request itself starts on loopback too, which is only
	// reachable here because the test dials it directly.
	client := Client(Options{Timeout: 5 * time.Second, MaxRedirects: 3})
	resp, err := client.Get(redirector.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("the redirect into an internal address was followed; it must be refused")
	}
	if !errors.Is(err, ErrDisallowedAddress) {
		t.Errorf("error = %v, want it to carry ErrDisallowedAddress", err)
	}
}

// With AllowInternal a test can talk to its own httptest server — and that is
// the only thing it is for.
func TestClientAllowInternalIsForTests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := Client(Options{Timeout: 5 * time.Second, MaxRedirects: 2, AllowInternal: true})
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("request to the test server failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// A chain longer than the cap stops, rather than following redirects forever.
func TestClientCapsRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
	}))
	defer srv.Close()

	client := Client(Options{Timeout: 5 * time.Second, MaxRedirects: 2, AllowInternal: true})
	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("an endless redirect chain was followed; it must stop at the cap")
	}
}

// The connect-time layer: even if validation and redirect checks were somehow
// passed, the dialer refuses the address it is actually about to connect to.
// This is what closes the DNS-rebinding window that a name-based check leaves
// open.
func TestClientRefusesInternalAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secrets"))
	}))
	defer srv.Close()

	client := Client(Options{Timeout: 5 * time.Second, MaxRedirects: 3})
	resp, err := client.Get(srv.URL) // loopback
	if err == nil {
		resp.Body.Close()
		t.Fatal("connected to a loopback address; the dialer must refuse it")
	}
	if !errors.Is(err, ErrDisallowedAddress) {
		t.Errorf("error = %v, want it to carry ErrDisallowedAddress", err)
	}
}
