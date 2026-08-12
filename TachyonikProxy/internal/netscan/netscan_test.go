// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests for network resolution — above all the refusal to sweep anything
// public or oversized — target enumeration, and the bounded parallel probe.

package netscan

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveNetwork(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		want    string
		wantErr string
	}{
		{name: "private /24", cidr: "192.168.178.0/24", want: "192.168.178.0/24"},
		{name: "host address is masked to its network", cidr: "192.168.178.42/24", want: "192.168.178.0/24"},
		{name: "10/8 range", cidr: "10.1.2.0/24", want: "10.1.2.0/24"},
		{name: "172.16/12 lower bound", cidr: "172.16.0.0/24", want: "172.16.0.0/24"},
		{name: "172.31 upper bound", cidr: "172.31.255.0/24", want: "172.31.255.0/24"},

		// The guard that replaces the hand-written "must start with 192.168."
		// check every detection rule would otherwise have to repeat.
		{name: "public range refused", cidr: "8.8.8.0/24", wantErr: "outside the sweepable ranges"},
		{name: "172.15 is not private", cidr: "172.15.0.0/24", wantErr: "outside the sweepable ranges"},
		{name: "172.32 is not private", cidr: "172.32.0.0/24", wantErr: "outside the sweepable ranges"},
		{name: "loopback allowed", cidr: "127.0.0.0/24", want: "127.0.0.0/24"},

		{name: "too wide", cidr: "10.0.0.0/8", wantErr: "larger than /22"},
		{name: "/22 is the widest allowed", cidr: "10.0.0.0/22", want: "10.0.0.0/22"},
		{name: "malformed", cidr: "not-a-cidr", wantErr: "invalid netscan.network"},
		{name: "ipv6 refused", cidr: "fd00::/64", wantErr: "not IPv4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveNetwork(tt.cidr)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveNetwork(%q) = %v, want error containing %q", tt.cidr, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveNetwork(%q): %v", tt.cidr, err)
			}
			if got.String() != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIsSweepableIPv4(t *testing.T) {
	sweepable := []string{
		"10.0.0.1", "10.255.255.254",
		"172.16.0.1", "172.31.255.254",
		"192.168.0.1", "192.168.255.254",
		"127.0.0.1", // our own machine
	}
	refused := []string{"8.8.8.8", "1.1.1.1", "172.15.255.255", "172.32.0.1", "192.167.255.255", "192.169.0.1"}

	for _, s := range sweepable {
		if !isSweepableIPv4(net.ParseIP(s)) {
			t.Errorf("isSweepableIPv4(%s) = false, want true", s)
		}
	}
	for _, s := range refused {
		if isSweepableIPv4(net.ParseIP(s)) {
			t.Errorf("isSweepableIPv4(%s) = true, want false — that is somebody else's network", s)
		}
	}
}

func TestTargets(t *testing.T) {
	tests := []struct {
		cidr      string
		ports     []int
		wantCount int
		wantFirst string
		wantLast  string
	}{
		{cidr: "192.168.178.0/24", ports: []int{443}, wantCount: 254, wantFirst: "192.168.178.1", wantLast: "192.168.178.254"},
		{cidr: "192.168.178.0/24", ports: []int{443, 8443}, wantCount: 508},
		// Crossing an octet boundary is where naive byte arithmetic breaks.
		{cidr: "10.0.0.0/22", ports: []int{443}, wantCount: 1022, wantFirst: "10.0.0.1", wantLast: "10.0.3.254"},
	}

	for _, tt := range tests {
		t.Run(tt.cidr, func(t *testing.T) {
			s, err := New(Config{Network: tt.cidr, Ports: tt.ports})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got := s.targets()
			if len(got) != tt.wantCount {
				t.Fatalf("target count = %d, want %d", len(got), tt.wantCount)
			}
			if tt.wantFirst != "" && got[0].ip != tt.wantFirst {
				t.Errorf("first = %s, want %s", got[0].ip, tt.wantFirst)
			}
			if tt.wantLast != "" && got[len(got)-1].ip != tt.wantLast {
				t.Errorf("last = %s, want %s", got[len(got)-1].ip, tt.wantLast)
			}
		})
	}
}

// A sweep must reach every target, keep only the responders, and cap the body.
func TestScanOnce_CollectsRespondersOnly(t *testing.T) {
	var hits int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Server", "openvas-test")
		fmt.Fprint(w, "<html><head><title>OPENVAS SCAN</title></head><body>"+strings.Repeat("x", 5000)+"</body></html>")
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)

	// A /32 sweep aimed at the test server: one target, guaranteed responder.
	s, err := New(Config{Network: host + "/32", Ports: []int{port}, MaxBodyBytes: 64, Concurrency: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if snap := s.Snapshot(); snap.Ready {
		t.Fatal("Ready is true before the first sweep; a routine could report a false negative")
	}

	s.ScanOnce(context.Background())

	snap := s.Snapshot()
	if !snap.Ready {
		t.Fatal("Ready is false after a completed sweep")
	}
	if snap.Scanning {
		t.Error("Scanning still set after the sweep finished")
	}
	if len(snap.Hosts) != 1 {
		t.Fatalf("hosts = %d, want 1", len(snap.Hosts))
	}

	h := snap.Hosts[0]
	if h.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", h.Status)
	}
	if len(h.Body) != 64 {
		t.Errorf("body length = %d, want it truncated to MaxBodyBytes (64)", len(h.Body))
	}
	if !strings.Contains(h.Body, "<title>OPENVAS SCAN</title>") {
		t.Errorf("body %q lost the marker that detection depends on", h.Body)
	}
	if h.Headers["server"] != "openvas-test" {
		t.Errorf("headers = %v, want the lower-cased server header", h.Headers)
	}
	// httptest's certificate is not in the system roots: reachable, not trusted.
	if h.TLSTrusted {
		t.Error("TLSTrusted = true for a self-signed test certificate")
	}
	if h.CertSubject == "" {
		t.Error("CertSubject is empty; the routine cannot inspect the presented certificate")
	}
}

// Unreachable addresses are the overwhelming majority of a real subnet and must
// not be cached, or hosts() would be mostly noise.
func TestScanOnce_DropsUnreachable(t *testing.T) {
	// 192.0.2.0/24 is TEST-NET-1, but it is public, so use a private range
	// with nothing listening on a port nothing binds.
	s, err := New(Config{Network: "192.168.199.0/30", Ports: []int{1}, TimeoutSeconds: 1, Concurrency: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s.ScanOnce(ctx)

	snap := s.Snapshot()
	if !snap.Ready {
		t.Fatal("Ready is false after a completed sweep")
	}
	if len(snap.Hosts) != 0 {
		t.Fatalf("hosts = %d, want 0 — nothing is listening", len(snap.Hosts))
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	s, err := New(Config{Network: "192.168.1.0/24"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.cfg.IntervalMinutes != DefaultIntervalMinutes ||
		s.cfg.Concurrency != DefaultConcurrency ||
		s.cfg.TimeoutSeconds != DefaultTimeoutSeconds ||
		s.cfg.MaxBodyBytes != DefaultMaxBodyBytes ||
		s.cfg.MaxScanDurationMinutes != DefaultMaxScanDurationMinutes {
		t.Fatalf("defaults not applied: %+v", s.cfg)
	}
	if len(s.cfg.Ports) != 1 || s.cfg.Ports[0] != 443 {
		t.Fatalf("ports = %v, want [443]", s.cfg.Ports)
	}
}

func splitHostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	trimmed := strings.TrimPrefix(rawURL, "https://")
	host, portStr, err := net.SplitHostPort(trimmed)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", trimmed, err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	return host, port
}

func TestHostTitle(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "plain", body: "<html><head><title>OPENVAS SCAN</title></head>", want: "OPENVAS SCAN"},
		{name: "mixed case tag", body: "<TITLE>Fritz!Box</TITLE>", want: "Fritz!Box"},
		{name: "attributes on the tag", body: `<title lang="en">Router</title>`, want: "Router"},
		{name: "newlines and padding collapse", body: "<title>\n  OPENVAS\n  SCAN\n</title>", want: "OPENVAS SCAN"},
		{name: "first title wins", body: "<title>One</title><title>Two</title>", want: "One"},
		{name: "absent", body: "<html><body>no title here</body></html>", want: ""},
		// A body truncated at max_body_bytes mid-element must not yield garbage.
		{name: "truncated before the closing tag", body: "<html><head><title>OPENV", want: ""},
		{name: "empty body", body: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Host{Body: tt.body}).Title(); got != tt.want {
				t.Fatalf("Title() = %q, want %q", got, tt.want)
			}
		})
	}
}

// An appliance answering "/" with a redirect to its login page used to be
// cached as an empty body, since a 3xx carries no content. Same-address hops
// are now followed, so the real page is what gets stored.
func TestProbe_FollowsSameHostRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><head><title>OPENVAS SCAN</title></head></html>")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	s, err := New(Config{Network: host + "/32", Ports: []int{port}, Concurrency: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.ScanOnce(context.Background())

	hosts := s.Snapshot().Hosts
	if len(hosts) != 1 {
		t.Fatalf("hosts = %d, want 1", len(hosts))
	}
	h := hosts[0]
	if h.Status != http.StatusOK {
		t.Errorf("status = %d, want 200 — the redirect was not followed", h.Status)
	}
	if h.Title() != "OPENVAS SCAN" {
		t.Errorf("title = %q, want the redirected page's title", h.Title())
	}
	if !strings.HasSuffix(h.FinalURL, "/login") {
		t.Errorf("finalUrl = %q, want the redirect target", h.FinalURL)
	}
	if h.URL == h.FinalURL {
		t.Error("url and finalUrl are identical; the redirect is invisible to a routine")
	}
}

// Chasing a redirect off the probed address would attribute another machine's
// page to this record — and could reach a host the sweep never selected.
func TestProbe_DoesNotFollowOffHostRedirect(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.invalid/login", http.StatusFound)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	s, err := New(Config{Network: host + "/32", Ports: []int{port}, Concurrency: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.ScanOnce(context.Background())

	hosts := s.Snapshot().Hosts
	if len(hosts) != 1 {
		t.Fatalf("hosts = %d, want 1", len(hosts))
	}
	h := hosts[0]
	if h.Status != http.StatusFound {
		t.Errorf("status = %d, want 302 recorded rather than followed", h.Status)
	}
	if h.Headers["location"] != "https://elsewhere.invalid/login" {
		t.Errorf("location = %q, want it preserved as a detection signal", h.Headers["location"])
	}
}

// A redirect loop must terminate rather than spin.
func TestProbe_BoundsRedirectChain(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	s, err := New(Config{Network: host + "/32", Ports: []int{port}, TimeoutSeconds: 5, Concurrency: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	go func() { s.ScanOnce(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("sweep did not finish: the redirect chain is unbounded")
	}

	hosts := s.Snapshot().Hosts
	if len(hosts) != 1 || hosts[0].Status != http.StatusFound {
		t.Fatalf("want the loop stopped with the last 3xx recorded, got %+v", hosts)
	}
}

// Certificate detail is the only usable identifier when the body is not —
// an auth wall, or a shell page that fills itself in via JavaScript.
func TestProbe_CapturesCertificateDetail(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // no body at all
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.URL)
	s, err := New(Config{Network: host + "/32", Ports: []int{port}, Concurrency: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.ScanOnce(context.Background())

	hosts := s.Snapshot().Hosts
	if len(hosts) != 1 {
		t.Fatalf("hosts = %d, want 1", len(hosts))
	}
	h := hosts[0]
	if h.Body != "" {
		t.Fatalf("test premise broken: body = %q, expected empty", h.Body)
	}
	if h.CertSubject == "" || h.CertIssuer == "" {
		t.Errorf("cert subject/issuer empty: %+v", h)
	}
	if h.CertNotAfter == "" {
		t.Error("certNotAfter empty; a routine cannot reason about expiry")
	}
	if len(h.CertDNSNames) == 0 {
		t.Errorf("certDnsNames empty — httptest certs carry example.com; got %+v", h.CertDNSNames)
	}
}
