// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"tachyonik/tachyonikproxy/internal/config"
)

// LoadServerTLSConfig creates a tls.Config that:
// 1. Presents the proxy's certificate to connecting clients
// 2. Requires client certificates signed by the shared CA
// 3. Verifies client CN/SAN against the allowed_clients list
func LoadServerTLSConfig(cfg *config.TLSConfig) (*tls.Config, error) {
	if cfg.CACert == "" || cfg.ServerCert == "" || cfg.ServerKey == "" {
		return nil, fmt.Errorf("TLS configuration incomplete: ca_cert, server_cert, and server_key are required")
	}

	caCert, err := os.ReadFile(cfg.CACert)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	cert, err := tls.LoadX509KeyPair(cfg.ServerCert, cfg.ServerKey)
	if err != nil {
		return nil, fmt.Errorf("failed to load server certificate: %w", err)
	}

	// Fail closed: with an empty allowed_clients list the CN/SAN verifier
	// below is never installed, and RequireAndVerifyClientCert alone accepts
	// ANY certificate chaining to the shared CA — i.e. no per-proxy identity
	// pinning. In a fleet where one CA signs many peers that is effectively
	// "any enrolled party may invoke this proxy's tools". Require at least
	// one allowed client so client identity is always pinned.
	if len(cfg.AllowedClients) == 0 {
		return nil, fmt.Errorf("TLS configuration insecure: allowed_clients is empty; at least one client identity must be pinned (re-run enrollment or set tls.allowed_clients)")
	}

	tlsConfig := &tls.Config{
		Certificates:          []tls.Certificate{cert},
		ClientAuth:            tls.RequireAndVerifyClientCert,
		ClientCAs:             caPool,
		MinVersion:            tls.VersionTLS13,
		VerifyPeerCertificate: makeClientVerifier(cfg.AllowedClients),
	}

	return tlsConfig, nil
}

func makeClientVerifier(allowedClients []string) func([][]byte, [][]*x509.Certificate) error {
	allowed := make(map[string]bool, len(allowedClients))
	for _, c := range allowedClients {
		allowed[strings.ToLower(c)] = true
	}

	return func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		if len(verifiedChains) == 0 || len(verifiedChains[0]) == 0 {
			return fmt.Errorf("no verified client certificate")
		}

		leaf := verifiedChains[0][0]
		cn := strings.ToLower(leaf.Subject.CommonName)
		if allowed[cn] {
			return nil
		}

		for _, san := range leaf.DNSNames {
			if allowed[strings.ToLower(san)] {
				return nil
			}
		}

		return fmt.Errorf("client certificate CN=%q not in allowed list", leaf.Subject.CommonName)
	}
}
