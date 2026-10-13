// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package insightvm speaks the REST API (v3) of a Rapid7 InsightVM Security
// Console, so the proxy can ask it what hosts it knows about.
//
// The console is an appliance in the customer's network: it serves its API
// over HTTPS on the console's own port (3780 unless changed), authenticates
// with HTTP Basic, and pages every collection. Built against Rapid7's
// published OpenAPI document for the API (help.rapid7.com/insightvm/en-us/api/
// api-v3.json), not against a console: field names, paging and error shapes
// are the specification's.
//
// Unlike package gmp, this one does not hand the console's answer on
// unmodified. A full InsightVM asset carries its installed software, services,
// files, users and scan history and can run to tens of kilobytes, so a few
// hundred hosts would exceed what a source may be. Each asset is cut down to
// what identifies and summarises it — see keptAssetFields — and the pages are
// joined into one document of this package's own shape (Format). That is the
// one piece of format knowledge kept here, and the reason the document says
// whose format it is.
//
// The console's certificate is self-signed on most installations, so the
// usual chain verification cannot apply. Its SHA-256 fingerprint is pinned
// instead, the way an SSH host key is: until a user has confirmed it, the
// handshake is aborted before any request — and so before the credential — is
// sent.
package insightvm

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultPort is where a Security Console serves its web interface and API.
const DefaultPort = 3780

// Format names the document HostAssets returns, so an analysis rule can tell
// it from anything the console itself would send.
const Format = "tachyonik-insightvm-host-assets"

// FormatVersion moves when the document's shape changes.
const FormatVersion = 1

// pageSize is how many assets one request asks for. The API allows up to 500;
// a smaller page keeps each untrimmed response within maxPageBytes even for
// assets with long software lists.
const pageSize = 100

// maxPageBytes caps one untrimmed response. A page that exceeds it is refused
// rather than read without limit.
const maxPageBytes = 64 << 20

// Config is what it takes to reach a console.
type Config struct {
	Host string
	Port int
	// Username and Password are the console account. Held for the life of the
	// call and never written anywhere by this package.
	Username string
	Password string
	// CertFingerprint is the console's TLS certificate in the form Fingerprint
	// returns ("SHA256:AB:CD:…"). Empty means none has been confirmed yet, and
	// no request is sent.
	CertFingerprint string
	// Timeout bounds a whole operation: every page of HostAssets together.
	Timeout time.Duration
}

// Client is a connection to one console.
type Client struct {
	cfg  Config
	http *http.Client
	base string
	// MaxDocumentBytes caps the document HostAssets builds; past it the
	// retrieval stops with a DocumentTooLargeError. Zero means no cap.
	MaxDocumentBytes int
}

// UnknownCertificateError is returned when the console's certificate is not
// the confirmed one, and carries what was offered so it can be shown and
// pinned.
type UnknownCertificateError struct {
	Host     string
	Got      string
	Expected string
}

func (e *UnknownCertificateError) Error() string {
	if e.Expected == "" {
		return fmt.Sprintf("the TLS certificate of %s has not been confirmed (%s)", e.Host, e.Got)
	}
	return fmt.Sprintf("the TLS certificate of %s is %s, but %s was confirmed", e.Host, e.Got, e.Expected)
}

// StatusError is an answer other than 200.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the console answered %d", e.Status)
	}
	return fmt.Sprintf("the console answered %d: %s", e.Status, e.Message)
}

// IsAuthFailure reports whether the console refused the credential. An account
// with two-factor authentication is refused the same way, since a time-based
// token cannot be stored with a credential.
func IsAuthFailure(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == http.StatusUnauthorized
}

// IsForbidden reports whether the console accepted the credential but the
// account may not read assets.
func IsForbidden(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == http.StatusForbidden
}

// DocumentTooLargeError is an inventory that exceeded Client.MaxDocumentBytes.
type DocumentTooLargeError struct {
	Limit int
}

func (e *DocumentTooLargeError) Error() string {
	return fmt.Sprintf("the host inventory exceeds the limit of %d bytes", e.Limit)
}

// Fingerprint is how a certificate is named for pinning: SHA-256 over its DER
// encoding, in upper-case hex pairs as browsers show it, behind the same
// "SHA256:" prefix an SSH fingerprint carries.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = fmt.Sprintf("%02X", b)
	}
	return "SHA256:" + strings.Join(pairs, ":")
}

// New prepares a client. Nothing is sent until a method is called.
func New(cfg Config) *Client {
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	host := cfg.Host

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Chain verification is replaced by the pin below, not dropped: a
		// console's certificate is self-signed unless its operator replaced
		// it, so no chain would verify, and a pin is the stronger check.
		InsecureSkipVerify: true, //nolint:gosec // pinned in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("%s presented no TLS certificate", host)
			}
			got := Fingerprint(cs.PeerCertificates[0])
			if cfg.CertFingerprint == "" || !strings.EqualFold(got, strings.TrimSpace(cfg.CertFingerprint)) {
				return &UnknownCertificateError{Host: host, Got: got, Expected: cfg.CertFingerprint}
			}
			return nil
		},
	}
	transport := &http.Transport{
		// The console is on the proxy's own network; a proxy configured in
		// the environment for internet traffic must not see this request.
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: cfg.Timeout}).DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   cfg.Timeout,
		ResponseHeaderTimeout: cfg.Timeout,
	}
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Transport: transport,
			// A redirect could carry the Authorization header to another
			// address. The API never redirects, so one is reported, not followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		base: "https://" + net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
	}
}

// Probe checks that the credential may read the console's assets, and returns
// how many there are. It reads a single asset, so it costs the console next to
// nothing and tests exactly the permission HostAssets needs.
func (c *Client) Probe() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	defer cancel()
	p, err := c.assetPage(ctx, 0, 1)
	if err != nil {
		return 0, err
	}
	return p.Page.TotalResources, nil
}

// HostAssets is the console's host inventory, cut down and joined.
type HostAssets struct {
	// Document is the JSON document described in the package comment.
	Document []byte
	// Count is how many assets it holds.
	Count int
}

// assetPage is one page of GET /api/3/assets.
type assetPage struct {
	Resources []json.RawMessage `json:"resources"`
	Page      struct {
		Number         int `json:"number"`
		Size           int `json:"size"`
		TotalPages     int `json:"totalPages"`
		TotalResources int `json:"totalResources"`
	} `json:"page"`
}

// document is what HostAssets returns, in this field order.
type document struct {
	Format         string            `json:"format"`
	FormatVersion  int               `json:"formatVersion"`
	Console        string            `json:"console"`
	RetrievedAt    string            `json:"retrievedAt"`
	TotalResources int               `json:"totalResources"`
	Resources      []json.RawMessage `json:"resources"`
}

// HostAssets returns every asset the account may see.
//
// The pages are read in id order. An inventory that changes while it is being
// read — its total moves, or an asset turns up twice — is an error rather than
// a document, and so is one that ends short of the total the console reported:
// a partial list imported as the whole would look complete.
func (c *Client) HostAssets() (*HostAssets, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	defer cancel()

	var (
		resources []json.RawMessage
		seen      = map[string]bool{}
		total     = -1
		size      = 0
	)
	for n := 0; ; n++ {
		p, err := c.assetPage(ctx, n, pageSize)
		if err != nil {
			return nil, err
		}
		if total < 0 {
			total = p.Page.TotalResources
		} else if p.Page.TotalResources != total {
			return nil, fmt.Errorf("the inventory changed while it was read (%d assets, then %d) — try again", total, p.Page.TotalResources)
		}

		for _, raw := range p.Resources {
			trimmed, id, err := trimAsset(raw)
			if err != nil {
				return nil, fmt.Errorf("asset on page %d: %w", n, err)
			}
			if id != "" {
				if seen[id] {
					return nil, fmt.Errorf("asset %s was returned twice — the inventory changed while it was read; try again", id)
				}
				seen[id] = true
			}
			size += len(trimmed) + 1
			if c.MaxDocumentBytes > 0 && size > c.MaxDocumentBytes {
				return nil, &DocumentTooLargeError{Limit: c.MaxDocumentBytes}
			}
			resources = append(resources, trimmed)
		}

		if len(p.Resources) == 0 || n+1 >= p.Page.TotalPages {
			break
		}
		// A console that keeps paging past its own total is not one to
		// follow indefinitely.
		if len(resources) >= total {
			return nil, fmt.Errorf("the console kept paging past its total of %d assets", total)
		}
	}
	if len(resources) != total {
		return nil, fmt.Errorf("the console reports %d assets but returned %d — the inventory is incomplete", total, len(resources))
	}

	doc, err := json.Marshal(document{
		Format:         Format,
		FormatVersion:  FormatVersion,
		Console:        net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port)),
		RetrievedAt:    time.Now().UTC().Format(time.RFC3339),
		TotalResources: total,
		Resources:      nonNil(resources),
	})
	if err != nil {
		return nil, fmt.Errorf("could not build the inventory document: %w", err)
	}
	if c.MaxDocumentBytes > 0 && len(doc) > c.MaxDocumentBytes {
		return nil, &DocumentTooLargeError{Limit: c.MaxDocumentBytes}
	}
	return &HostAssets{Document: doc, Count: len(resources)}, nil
}

// nonNil keeps an empty inventory a JSON array rather than null.
func nonNil(r []json.RawMessage) []json.RawMessage {
	if r == nil {
		return []json.RawMessage{}
	}
	return r
}

func (c *Client) assetPage(ctx context.Context, page, size int) (*assetPage, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("size", strconv.Itoa(size))
	q.Set("sort", "id,ASC")
	body, err := c.get(ctx, "/api/3/assets", q)
	if err != nil {
		return nil, err
	}
	var p assetPage
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("the answer to /api/3/assets is not an asset page: %w", err)
	}
	return &p, nil
}

// get sends one authenticated request and returns the body of a 200 answer.
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.cfg.Username+":"+c.cfg.Password)))
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		var uc *UnknownCertificateError
		if errors.As(err, &uc) {
			return nil, uc
		}
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read the answer: %w", err)
	}
	if len(body) > maxPageBytes {
		return nil, fmt.Errorf("one page of the answer exceeds %d bytes", maxPageBytes)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return body, nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, &StatusError{Status: resp.StatusCode, Message: "redirected to " + resp.Header.Get("Location") + "; not followed"}
	default:
		return nil, &StatusError{Status: resp.StatusCode, Message: errorMessage(body)}
	}
}

// errorMessage reads the console's reason from an error body, which the API
// shapes as {"status": "...", "message": "..."}.
func errorMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return truncate(e.Message)
	}
	return truncate(strings.TrimSpace(string(body)))
}

func truncate(s string) string {
	const max = 200
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
