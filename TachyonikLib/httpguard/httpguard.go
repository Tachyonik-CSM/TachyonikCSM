// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpguard makes outbound HTTP requests to user-influenced addresses
// survivable.
//
// Several services fetch something a user pointed them at: an AI provider
// endpoint, an organisation's own homepage. Each such request is a
// server-side request forgery waiting to happen — the server sits inside a
// network the caller cannot reach, so "fetch this URL for me" is an invitation
// to have it read a metadata service, a database admin port, or a neighbouring
// container.
//
// The guard is deliberately two-layered, because one layer is not enough:
//
//   - ValidateURL resolves the host and refuses internal addresses BEFORE the
//     request is made, which is what a caller checks when it wants to reject a
//     configuration outright.
//   - Client re-runs that check on every redirect hop, because a public URL is
//     free to redirect into the private range, and a validated URL therefore
//     says nothing about where the request ends up.
package httpguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrDisallowedAddress is returned when a URL resolves to an address the guard
// refuses. Callers match on it with errors.Is to word their own message.
var ErrDisallowedAddress = errors.New("URL resolves to a disallowed internal address")

// ErrBadURL is returned for input that is not a usable http(s) URL.
var ErrBadURL = errors.New("not a valid http or https URL")

// ValidateURL reports whether rawURL is safe to request: an http(s) URL whose
// host resolves only to addresses outside the internal ranges.
//
// A hostname that resolves to an internal address is refused too — the check is
// on the resolved IPs, not on the spelling of the host, so "localtest.me" and
// friends do not slip through.
//
// This is a request-time check. Between it and the connection the DNS answer
// can change (a rebinding attack); Client closes most of that window by
// re-checking each redirect, and a caller that needs certainty must use a
// dialer that pins the validated address.
func ValidateURL(rawURL string) error {
	u, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil {
		return ErrBadURL
	}
	return ValidateParsed(u)
}

// ValidateParsed is ValidateURL for an already-parsed URL.
func ValidateParsed(u *url.URL) error {
	if u == nil {
		return ErrBadURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrBadURL
	}
	host := u.Hostname()
	if host == "" {
		return ErrBadURL
	}

	// An IP literal is checked directly; a name is resolved first.
	var ips []net.IP
	if lit := net.ParseIP(host); lit != nil {
		ips = []net.IP{lit}
	} else {
		resolved, err := net.LookupIP(host)
		if err != nil || len(resolved) == 0 {
			return fmt.Errorf("host does not resolve")
		}
		ips = resolved
	}
	// EVERY resolved address has to be acceptable. Checking only the first
	// would let a host that answers with both a public and an internal address
	// through, and which one the dialer picks is not ours to decide.
	for _, ip := range ips {
		if IsDisallowedIP(ip) {
			return ErrDisallowedAddress
		}
	}
	return nil
}

// IsDisallowedIP reports whether connecting to ip would be a request-forgery
// risk: loopback, unspecified, link-local (which is where cloud metadata lives
// at 169.254.169.254), multicast, and the private/ULA ranges.
func IsDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsPrivate()
}

// Options configure a guarded client.
type Options struct {
	// Timeout bounds the whole request, redirects included. Required.
	Timeout time.Duration
	// MaxRedirects caps the redirect chain. 0 refuses every redirect.
	MaxRedirects int
	// AllowInternal disables the address checks. ONLY for tests, which have to
	// talk to an httptest server on loopback — the address the guard exists to
	// refuse. Never set it from configuration: it would turn the guard off in
	// production from a config file, which is the failure mode this comment is
	// here to prevent.
	AllowInternal bool
}

// Client returns an http.Client that will not connect to an internal address,
// however it is asked to.
//
// Three layers, because each one alone has a hole:
//
//   - The caller validates the URL up front (ValidateURL), which is what lets
//     it reject a bad configuration with a clear message.
//   - CheckRedirect re-validates every hop, because a public URL is free to
//     answer 302 with http://169.254.169.254/ and a validated URL therefore
//     says nothing about where the request ends up.
//   - The dialer's Control hook runs after DNS resolution, on the address
//     actually being connected to. This is the one that defeats DNS
//     rebinding: between validating a name and dialling it, the answer can
//     change, and only a check at connect time sees the address that is really
//     used.
func Client(opts Options) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if opts.AllowInternal {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("guard: malformed dial address %q", address)
			}
			if IsDisallowedIP(net.ParseIP(host)) {
				return fmt.Errorf("guard: refusing to connect to internal address %s: %w", host, ErrDisallowedAddress)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: opts.Timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > opts.MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", opts.MaxRedirects)
			}
			if opts.AllowInternal {
				return nil
			}
			return ValidateParsed(req.URL)
		},
	}
}
