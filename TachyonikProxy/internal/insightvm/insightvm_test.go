// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the client against a fake console over real TLS.
//
// There was no console to record from. testdata/asset_example.json is built
// from the example values in Rapid7's OpenAPI document for API v3 — the shape
// the specification promises, not one observed on a console. The tests pin
// what this package decides on top of that shape: the certificate pin, paging,
// completeness, and what is kept of an asset.

package insightvm

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeConsole serves GET /api/3/assets from a list of assets, paged the way
// the API pages.
type fakeConsole struct {
	assets []map[string]interface{}
	// total overrides the totalResources reported, when non-negative.
	total int
	// totalAfterFirst, when non-negative, is reported from the second page on.
	totalAfterFirst int
	// status, when set, answers every request with it.
	status  int
	body    string
	headers map[string]string

	requests atomic.Int32
	lastAuth atomic.Value
	queries  []string
}

func newFakeConsole(assets []map[string]interface{}) *fakeConsole {
	return &fakeConsole{assets: assets, total: -1, totalAfterFirst: -1}
}

func (f *fakeConsole) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	f.lastAuth.Store(r.Header.Get("Authorization"))
	f.queries = append(f.queries, r.URL.RawQuery)
	for k, v := range f.headers {
		w.Header().Set(k, v)
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
		return
	}
	if r.URL.Path != "/api/3/assets" {
		http.NotFound(w, r)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	start, end := page*size, page*size+size
	if start > len(f.assets) {
		start = len(f.assets)
	}
	if end > len(f.assets) {
		end = len(f.assets)
	}
	total := len(f.assets)
	if f.total >= 0 {
		total = f.total
	}
	if page > 0 && f.totalAfterFirst >= 0 {
		total = f.totalAfterFirst
	}
	pages := (total + size - 1) / size
	resp := map[string]interface{}{
		"resources": f.assets[start:end],
		"page":      map[string]int{"number": page, "size": size, "totalPages": pages, "totalResources": total},
		"links":     []interface{}{map[string]string{"href": "https://console/api/3/assets", "rel": "self"}},
	}
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	_ = json.NewEncoder(w).Encode(resp)
}

// exampleAsset is the specification's example asset with its own id.
func exampleAsset(t *testing.T, id int) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile("testdata/asset_example.json")
	if err != nil {
		t.Fatal(err)
	}
	var a map[string]interface{}
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	a["id"] = id
	return a
}

func assets(t *testing.T, n int) []map[string]interface{} {
	out := make([]map[string]interface{}, n)
	for i := range out {
		out[i] = exampleAsset(t, 1000+i)
	}
	return out
}

// start serves f over TLS and returns a client for it. pin decides what the
// client has confirmed: "right", "" (nothing) or a literal fingerprint.
func start(t *testing.T, f http.Handler, pin string) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	port, _ := strconv.Atoi(portStr)
	if pin == "right" {
		pin = Fingerprint(srv.Certificate())
	}
	return New(Config{
		Host: host, Port: port,
		Username: "nxadmin", Password: "s3cret:with-colon",
		CertFingerprint: pin,
		Timeout:         10 * time.Second,
	}), srv
}

func TestFingerprintFormat(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	fp := Fingerprint(srv.Certificate())
	if !strings.HasPrefix(fp, "SHA256:") || len(fp) != len("SHA256:")+32*3-1 {
		t.Errorf("unexpected fingerprint form %q", fp)
	}
	if strings.ToUpper(fp) != fp {
		t.Errorf("fingerprint is not upper case: %q", fp)
	}
}

// The point of the pin: with no certificate confirmed, nothing — and above all
// not the credential — reaches whatever answers at the address.
func TestAnUnconfirmedCertificateSendsNothing(t *testing.T) {
	f := newFakeConsole(assets(t, 1))
	c, srv := start(t, f, "")

	_, err := c.Probe()
	var uc *UnknownCertificateError
	if !errors.As(err, &uc) {
		t.Fatalf("want UnknownCertificateError, got %v", err)
	}
	if uc.Expected != "" || uc.Got != Fingerprint(srv.Certificate()) {
		t.Errorf("error does not carry the offered certificate: %+v", uc)
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("%d request(s) reached the console before its certificate was confirmed", n)
	}
}

func TestAChangedCertificateSendsNothing(t *testing.T) {
	f := newFakeConsole(assets(t, 1))
	other := "SHA256:" + strings.TrimSuffix(strings.Repeat("00:", 32), ":")
	c, _ := start(t, f, other)

	_, err := c.HostAssets()
	var uc *UnknownCertificateError
	if !errors.As(err, &uc) || uc.Expected != other {
		t.Fatalf("want a mismatch naming the confirmed certificate, got %v", err)
	}
	if n := f.requests.Load(); n != 0 {
		t.Errorf("%d request(s) reached a console whose certificate changed", n)
	}
}

func TestProbeReadsOneAsset(t *testing.T) {
	f := newFakeConsole(assets(t, 42))
	c, _ := start(t, f, "right")

	n, err := c.Probe()
	if err != nil || n != 42 {
		t.Fatalf("got %d, %v", n, err)
	}
	if len(f.queries) != 1 || !strings.Contains(f.queries[0], "size=1") {
		t.Errorf("probe asked %v", f.queries)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("nxadmin:s3cret:with-colon"))
	if got := f.lastAuth.Load(); got != want {
		t.Errorf("Authorization = %v", got)
	}
}

func TestHostAssetsReadsEveryPageAndTrims(t *testing.T) {
	f := newFakeConsole(assets(t, 250))
	c, _ := start(t, f, "right")

	got, err := c.HostAssets()
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 250 {
		t.Errorf("count = %d", got.Count)
	}
	if len(f.queries) != 3 {
		t.Errorf("want 3 pages of %d, asked %v", pageSize, f.queries)
	}
	for i, q := range f.queries {
		if !strings.Contains(q, fmt.Sprintf("page=%d", i)) || !strings.Contains(q, "sort=id%2CASC") {
			t.Errorf("page %d asked %q", i, q)
		}
	}

	var doc struct {
		Format         string                   `json:"format"`
		FormatVersion  int                      `json:"formatVersion"`
		Console        string                   `json:"console"`
		RetrievedAt    string                   `json:"retrievedAt"`
		TotalResources int                      `json:"totalResources"`
		Resources      []map[string]interface{} `json:"resources"`
	}
	if err := json.Unmarshal(got.Document, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Format != Format || doc.FormatVersion != FormatVersion || doc.TotalResources != 250 || len(doc.Resources) != 250 {
		t.Errorf("document header wrong: %+v", doc.Format)
	}
	if _, err := time.Parse(time.RFC3339, doc.RetrievedAt); err != nil {
		t.Errorf("retrievedAt %q: %v", doc.RetrievedAt, err)
	}

	a := doc.Resources[0]
	for _, k := range []string{"id", "ip", "addresses", "hostName", "hostNames", "mac", "os", "osFingerprint", "vulnerabilities", "riskScore"} {
		if _, ok := a[k]; !ok {
			t.Errorf("kept field %q is missing", k)
		}
	}
	for _, k := range []string{"software", "services", "files", "users", "userGroups", "configurations", "databases", "history", "links"} {
		if _, ok := a[k]; ok {
			t.Errorf("field %q should have been dropped", k)
		}
	}
	if fp, _ := a["osFingerprint"].(map[string]interface{}); fp == nil || fp["configurations"] != nil || fp["cpe"] == nil {
		t.Errorf("osFingerprint not trimmed as intended: %v", a["osFingerprint"])
	}
	if a["lastAssessed"] != "2018-04-09T06:23:49Z" {
		t.Errorf("lastAssessed = %v", a["lastAssessed"])
	}
	if a["id"] != float64(1000) {
		t.Errorf("first asset is %v, want 1000", a["id"])
	}
	if per := len(got.Document) / got.Count; per > 2000 {
		t.Errorf("a trimmed asset still takes %d bytes", per)
	}
}

func TestAnEmptyInventoryIsAnEmptyList(t *testing.T) {
	c, _ := start(t, newFakeConsole(nil), "right")
	got, err := c.HostAssets()
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 0 || !strings.Contains(string(got.Document), `"resources":[]`) {
		t.Errorf("got %d, %s", got.Count, got.Document)
	}
}

func TestAShortInventoryIsRefused(t *testing.T) {
	f := newFakeConsole(assets(t, 3))
	f.total = 5
	c, _ := start(t, f, "right")
	if _, err := c.HostAssets(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("a short inventory was accepted: %v", err)
	}
}

func TestAnInventoryThatChangesIsRefused(t *testing.T) {
	f := newFakeConsole(assets(t, 150))
	f.totalAfterFirst = 151
	c, _ := start(t, f, "right")
	if _, err := c.HostAssets(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("a changing inventory was accepted: %v", err)
	}
}

func TestADuplicatedAssetIsRefused(t *testing.T) {
	list := assets(t, 3)
	list[2]["id"] = 1000
	c, _ := start(t, newFakeConsole(list), "right")
	if _, err := c.HostAssets(); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a duplicated asset was accepted: %v", err)
	}
}

func TestARefusedCredentialIsAnAuthFailure(t *testing.T) {
	f := newFakeConsole(nil)
	f.status = http.StatusUnauthorized
	f.body = `{"status":"UNAUTHORIZED","message":"Invalid username or password."}`
	c, _ := start(t, f, "right")

	_, err := c.Probe()
	if !IsAuthFailure(err) || !strings.Contains(err.Error(), "Invalid username or password.") {
		t.Errorf("got %v", err)
	}
}

func TestAnAccountWithoutAccessIsForbidden(t *testing.T) {
	f := newFakeConsole(nil)
	f.status = http.StatusForbidden
	f.body = `{"status":"FORBIDDEN","message":"Access denied."}`
	c, _ := start(t, f, "right")

	_, err := c.HostAssets()
	if !IsForbidden(err) || IsAuthFailure(err) {
		t.Errorf("got %v", err)
	}
}

// A redirect is reported, not followed: following it would send the
// Authorization header to wherever it points.
func TestARedirectIsNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()

	f := newFakeConsole(nil)
	f.status = http.StatusFound
	f.headers = map[string]string{"Location": other.URL + "/api/3/assets"}
	c, _ := start(t, f, "right")

	_, err := c.Probe()
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusFound {
		t.Errorf("got %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

func TestAnInventoryOverTheLimitIsRefused(t *testing.T) {
	c, _ := start(t, newFakeConsole(assets(t, 50)), "right")
	c.MaxDocumentBytes = 4096
	_, err := c.HostAssets()
	var tl *DocumentTooLargeError
	if !errors.As(err, &tl) || tl.Limit != 4096 {
		t.Errorf("got %v", err)
	}
}

func TestLastAssessedIgnoresExceptions(t *testing.T) {
	h := json.RawMessage(`[
		{"date":"2026-01-01T10:00:00Z","type":"SCAN"},
		{"date":"2026-03-01T10:00:00+02:00","type":"AGENT-IMPORT"},
		{"date":"2026-09-01T10:00:00Z","type":"VULNERABILITY_EXCEPTION_APPLIED"}
	]`)
	if got := lastAssessed(h); got != "2026-03-01T08:00:00Z" {
		t.Errorf("got %q", got)
	}
	if got := lastAssessed(nil); got != "" {
		t.Errorf("no history gave %q", got)
	}
}
