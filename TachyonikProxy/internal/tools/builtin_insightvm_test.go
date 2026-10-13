// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pins the InsightVM built-in end to end against a fake console over TLS: the
// path through the registry with its argument checks, the output file, and the
// credential test's answers for an unconfirmed certificate, a refused account
// and a working one.

package tools

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"tachyonik/tachyonikproxy/internal/config"
	"tachyonik/tachyonikproxy/internal/insightvm"
)

func TestInsightVMTargetDefaultsAndValidation(t *testing.T) {
	got, err := insightvmTargetFromArgs(map[string]interface{}{"host": " 192.168.178.170 "})
	if err != nil || got.Host != "192.168.178.170" || got.Port != 3780 {
		t.Errorf("defaults not applied: %+v, %v", got, err)
	}
	got, err = insightvmTargetFromArgs(map[string]interface{}{"host": "console.local", "port": float64(443)})
	if err != nil || got.Port != 443 {
		t.Errorf("explicit port not taken: %+v, %v", got, err)
	}
	for _, bad := range []map[string]interface{}{
		{},
		{"host": "https://console"},
		{"host": "user@console"},
		{"host": "console", "port": float64(0)},
	} {
		if _, err := insightvmTargetFromArgs(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestInsightVMAssetsFilename(t *testing.T) {
	at := time.Date(2026, 10, 13, 15, 30, 5, 0, time.FixedZone("CEST", 2*3600))
	if got, want := insightvmAssetsFilename("192.168.178.170", at), "insightvm-host-assets-192.168.178.170-20261013T133005Z.json"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestInsightVMWithoutACredentialSaysSo(t *testing.T) {
	res, err := insightvmGetAssets(&config.ToolConfig{Command: builtinInsightVMGetAssets},
		map[string]interface{}{"host": "192.0.2.1"}, Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "no credential") {
		t.Errorf("missing credential not reported plainly: %+v", res)
	}
}

// fakeInsightVM answers /api/3/assets with n assets, all on one page, and 401
// for any other password than "right".
func fakeInsightVM(t *testing.T, n int) (host string, port int, fingerprint string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pw, _ := r.BasicAuth(); pw != "right" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":"UNAUTHORIZED","message":"Invalid username or password."}`))
			return
		}
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		res := []map[string]interface{}{}
		for i := 0; i < n && i < size; i++ {
			res = append(res, map[string]interface{}{"id": i + 1, "ip": "10.0.0." + strconv.Itoa(i+1), "software": []string{"big"}})
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"resources": res,
			"page":      map[string]int{"number": 0, "size": size, "totalPages": 1, "totalResources": n},
		})
	}))
	t.Cleanup(srv.Close)
	h, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	port, _ = strconv.Atoi(p)
	return h, port, insightvm.Fingerprint(srv.Certificate())
}

func TestInsightVMGetAssetsThroughTheRegistry(t *testing.T) {
	host, port, fp := fakeInsightVM(t, 3)
	tool := config.ToolConfig{
		Name:         "insightvm get host assets",
		Command:      builtinInsightVMGetAssets,
		AllowedChars: `a-zA-Z0-9.:_\-`,
		Timeout:      10,
		ArgsSchema:   config.JSONSchemaString(`{"properties":{"host":{"type":"string"},"port":{"type":"integer"}},"required":["host"]}`),
	}
	reg := NewRegistry([]config.ToolConfig{tool}, nil)

	res, err := reg.CallTool(tool.Name, map[string]interface{}{"host": host, "port": float64(port)},
		Secrets{Login: "nxadmin", Password: "right", HostKey: fp})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.OutputFiles) != 1 {
		t.Fatalf("got %+v", res)
	}
	f := res.OutputFiles[0]
	if f.MimeType != "application/json" || !strings.HasPrefix(f.Filename, "insightvm-host-assets-") {
		t.Errorf("output file %s (%s)", f.Filename, f.MimeType)
	}
	raw, _ := base64.StdEncoding.DecodeString(f.Data)
	if !strings.Contains(string(raw), `"format":"tachyonik-insightvm-host-assets"`) || strings.Contains(string(raw), "software") {
		t.Errorf("document not as intended: %s", raw)
	}
	if !strings.Contains(res.Content, "Retrieved 3 host assets") {
		t.Errorf("summary %q", res.Content)
	}

	// An unconfirmed certificate stops the call with advice, not a document.
	res, _ = reg.CallTool(tool.Name, map[string]interface{}{"host": host, "port": float64(port)},
		Secrets{Login: "nxadmin", Password: "right"})
	if !res.IsError || !strings.Contains(res.Content, "has not been confirmed") {
		t.Errorf("unconfirmed certificate: %+v", res)
	}
}

func TestInsightVMCredentialTest(t *testing.T) {
	host, port, fp := fakeInsightVM(t, 7)
	args := map[string]interface{}{"host": host, "port": float64(port)}

	// First contact: the certificate is reported for confirmation.
	r := TestInsightVMCredential(args, Secrets{Login: "nxadmin", Password: "right"})
	if r.OK || !r.HostKeyUnconfirmed || r.HostKeyFingerprint != fp || r.KeyKind != "tls" {
		t.Errorf("unconfirmed: %+v", r)
	}

	// Confirmed, wrong password.
	r = TestInsightVMCredential(args, Secrets{Login: "nxadmin", Password: "wrong", HostKey: fp})
	if r.OK || !r.AuthFailed || r.KeyKind != "tls" {
		t.Errorf("refused: %+v", r)
	}

	// Confirmed, right password.
	r = TestInsightVMCredential(args, Secrets{Login: "nxadmin", Password: "right", HostKey: fp})
	if !r.OK || !strings.Contains(r.Message, "7 assets") || r.HostKeyFingerprint != fp {
		t.Errorf("working: %+v", r)
	}
}
