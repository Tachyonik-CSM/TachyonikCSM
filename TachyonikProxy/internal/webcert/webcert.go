// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package webcert reads the TLS certificate a tool's web interface presents,
// so a user can confirm it once and later compare it with what their browser
// shows when it opens the web interface.
//
// It only completes a TLS handshake: no request is sent, nothing is logged in
// to, and no credential is involved. The certificate is accepted whatever it
// is — reading it is the point, and judging it is the user's job — so the
// result also says what a browser would think of it: whether it names the
// address and whether it is self-signed or expired.
package webcert

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Timeout bounds the connection and the handshake together.
const Timeout = 10 * time.Second

// Result describes the certificate at an address, or why there is none.
type Result struct {
	// OK: a certificate was read; the fields below describe it.
	OK bool `json:"ok"`
	// Fingerprint is SHA-256 over the DER encoding, in upper-case hex pairs
	// behind "SHA256:" — the form a browser's certificate viewer shows.
	Fingerprint string `json:"fingerprint,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Issuer      string `json:"issuer,omitempty"`
	// Names are the DNS names and IP addresses the certificate is for.
	Names     []string  `json:"names,omitempty"`
	NotBefore time.Time `json:"notBefore,omitempty"`
	NotAfter  time.Time `json:"notAfter,omitempty"`
	// NamesAddress: the certificate is for the address's host, so a browser
	// would not complain about the name.
	NamesAddress bool `json:"namesAddress"`
	SelfSigned   bool `json:"selfSigned"`
	Expired      bool `json:"expired"`
	// Message says what happened, in words; on failure, why.
	Message string `json:"message"`
}

// Fingerprint names a certificate as the web interface pin stores it.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = fmt.Sprintf("%02X", b)
	}
	return "SHA256:" + strings.Join(pairs, ":")
}

// Read connects to an https address and reports the certificate it presents.
// Always a result: a failure is described in it, not returned as an error.
func Read(ctx context.Context, rawURL string) Result {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return Result{Message: "the address is not a valid URL"}
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return Result{Message: "only https addresses can be checked"}
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{},
		Config: &tls.Config{
			// The certificate is read, not trusted: nothing is sent over
			// this connection, and the user judges what it shows.
			InsecureSkipVerify: true, //nolint:gosec
			ServerName:         serverName(host),
			MinVersion:         tls.VersionTLS12,
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{Message: fmt.Sprintf("could not read a certificate from %s: %v", addr, err)}
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return Result{Message: fmt.Sprintf("%s presented no certificate", addr)}
	}
	return describe(certs[0], host, time.Now())
}

// serverName is the SNI to send: the host name, or none for an IP address.
func serverName(host string) string {
	if net.ParseIP(host) != nil {
		return ""
	}
	return host
}

func describe(cert *x509.Certificate, host string, now time.Time) Result {
	r := Result{
		OK:           true,
		Fingerprint:  Fingerprint(cert),
		Subject:      cert.Subject.String(),
		Issuer:       cert.Issuer.String(),
		NotBefore:    cert.NotBefore,
		NotAfter:     cert.NotAfter,
		NamesAddress: cert.VerifyHostname(host) == nil,
		SelfSigned:   cert.CheckSignatureFrom(cert) == nil && cert.Subject.String() == cert.Issuer.String(),
		Expired:      now.After(cert.NotAfter) || now.Before(cert.NotBefore),
	}
	r.Names = append(r.Names, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		r.Names = append(r.Names, ip.String())
	}
	r.Message = "certificate read"
	return r
}
