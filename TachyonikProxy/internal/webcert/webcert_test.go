// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests reading a web interface's certificate against a local TLS server: the
// fingerprint matches the certificate served, the names are reported, and
// what cannot be read is described rather than returned as an error.

package webcert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheCertificateOfAWebInterfaceIsRead(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request was sent; only the handshake should happen")
	}))
	defer srv.Close()

	r := Read(context.Background(), srv.URL+"/login")
	if !r.OK {
		t.Fatalf("not read: %s", r.Message)
	}
	if want := Fingerprint(srv.Certificate()); r.Fingerprint != want {
		t.Errorf("fingerprint %s, want %s", r.Fingerprint, want)
	}
	if !r.NamesAddress {
		t.Errorf("the test certificate names 127.0.0.1, got names %v", r.Names)
	}
	if !strings.HasPrefix(r.Fingerprint, "SHA256:") || len(r.Fingerprint) != len("SHA256:")+32*3-1 {
		t.Errorf("fingerprint format: %s", r.Fingerprint)
	}
}

func TestWhatCannotBeReadIsDescribed(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1/", "not a url", "https://127.0.0.1:1/"} {
		if r := Read(context.Background(), u); r.OK || r.Message == "" {
			t.Errorf("%s: %+v, want a failure with a message", u, r)
		}
	}
}
