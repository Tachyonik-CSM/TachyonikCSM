// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gmp speaks the Greenbone Management Protocol to an OPENVAS SCAN
// appliance, so the proxy can ask it what hosts it knows about.
//
// It transports and does not interpret. The host inventory is handed on as the
// appliance's own document, unmodified, and turned into assets by the
// platform's analysis and import rules like any other source — the same way an
// Nmap or OpenVAS report file is. Format knowledge kept here would sit on
// customer hosts, change only with a proxy release, and drop whatever this
// package did not think to keep.
//
// Re-implemented rather than shelling out to gvm-tools: the proxy cannot
// require a Python toolchain on a customer's host, and the protocol is small —
// XML requests, XML responses, one command at a time.
//
// The transport is SSH, which is what current appliances offer. Greenbone's own
// manual puts it plainly: "All current appliances use SSH to encrypt GMP. The
// use of TLS is deprecated, not officially supported and may be removed in a
// future version." The appliance this was built against (Greenbone OS 25.0.7,
// GMP 22.8) has 9390 closed and 22 open, which is that statement in practice.
//
// Two identities are involved and they are not the same. The SSH account —
// conventionally "gmp" — opens the channel to the appliance's gvmd; the GMP
// user named in <authenticate> is the scanner account whose data is returned.
// An appliance may well give them different passwords.
package gmp

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Client is a GMP session. Not safe for concurrent use: the protocol is one
// request and one response at a time over a single channel.
type Client struct {
	t Transport
	// MaxResponseBytes caps a single response; reading stops with a
	// ResponseTooLargeError past it. Zero means no cap. Set by a caller that
	// has a size limit of its own to honour, before the response is read
	// rather than after it is all in memory.
	MaxResponseBytes int
}

// ResponseTooLargeError is a response that exceeded Client.MaxResponseBytes.
type ResponseTooLargeError struct {
	Command string
	Limit   int
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("%s: the response exceeds the limit of %d bytes", e.Command, e.Limit)
}

// Transport carries bytes to and from gvmd. Separated so the protocol can be
// tested without a network, and so a second transport could be added if an
// installation still speaks the deprecated TLS one.
type Transport interface {
	io.ReadWriter
	Close() error
}

// New wraps an open transport.
func New(t Transport) *Client { return &Client{t: t} }

// Close releases the transport.
func (c *Client) Close() error { return c.t.Close() }

// StatusError is a response the appliance refused. GMP answers every command
// with a status, and 200 is the only success.
type StatusError struct {
	Command string
	Status  string
	Text    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s refused by the appliance: %s %s", e.Command, e.Status, e.Text)
}

// IsAuthFailure reports whether the appliance rejected the credential rather
// than the request. GMP answers a bad username or password with 400 on
// authenticate, which is worth telling apart from a malformed command: one is
// the operator's to fix, the other is ours.
func IsAuthFailure(err error) bool {
	se, ok := err.(*StatusError)
	return ok && se.Command == "authenticate"
}

// Authenticate opens the session as a GMP user. It must come first: every
// other command answers 401 until it has.
func (c *Client) Authenticate(username, password string) error {
	req := "<authenticate><credentials><username>" + escape(username) +
		"</username><password>" + escape(password) +
		"</password></credentials></authenticate>"

	var resp struct {
		XMLName    xml.Name `xml:"authenticate_response"`
		Status     string   `xml:"status,attr"`
		StatusText string   `xml:"status_text,attr"`
	}
	if err := c.roundTrip("authenticate", req, &resp); err != nil {
		return err
	}
	if resp.Status != "200" {
		return &StatusError{Command: "authenticate", Status: resp.Status, Text: resp.StatusText}
	}
	return nil
}

// Version returns the appliance's GMP version. Allowed before authenticating,
// which makes it the cheapest proof that the transport works.
func (c *Client) Version() (string, error) {
	var resp struct {
		XMLName    xml.Name `xml:"get_version_response"`
		Status     string   `xml:"status,attr"`
		StatusText string   `xml:"status_text,attr"`
		Version    string   `xml:"version"`
	}
	if err := c.roundTrip("get_version", "<get_version/>", &resp); err != nil {
		return "", err
	}
	if resp.Status != "200" {
		return "", &StatusError{Command: "get_version", Status: resp.Status, Text: resp.StatusText}
	}
	return resp.Version, nil
}

// HostAssets is the appliance's host inventory as it sent it.
type HostAssets struct {
	// Document is the complete <get_assets_response>, byte for byte.
	Document []byte
	// Count is how many host assets the document holds.
	Count int
}

// hostAssetsRequest asks for every host asset with its details.
//
// details="1" makes the identifiers — hostname, OS, MAC — part of the answer;
// without it an asset is only its address. rows=-1 lifts GMP's paging: by
// default the appliance applies the user's page size, ten rows unless changed,
// and answers with the first page alone.
const hostAssetsRequest = `<get_assets type="host" details="1" filter="first=1 rows=-1"/>`

// HostAssets returns every host asset the appliance holds, as the appliance's
// own document.
//
// Only the status and the counts are read from it. A response holding fewer
// assets than the appliance says exist is an error rather than a smaller
// inventory: a partial list imported as the whole would look complete.
func (c *Client) HostAssets() (*HostAssets, error) {
	raw, err := c.exchange("get_assets", hostAssetsRequest)
	if err != nil {
		return nil, err
	}
	var resp struct {
		XMLName    xml.Name `xml:"get_assets_response"`
		Status     string   `xml:"status,attr"`
		StatusText string   `xml:"status_text,attr"`
		Assets     []struct {
			ID string `xml:"id,attr"`
		} `xml:"asset"`
		AssetCount struct {
			Filtered int `xml:"filtered"`
		} `xml:"asset_count"`
	}
	if err := xml.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("get_assets: response could not be read as get_assets_response: %w", err)
	}
	if resp.Status != "200" {
		return nil, &StatusError{Command: "get_assets", Status: resp.Status, Text: resp.StatusText}
	}
	if resp.AssetCount.Filtered > len(resp.Assets) {
		return nil, fmt.Errorf("get_assets: the appliance reports %d host assets but returned %d — the response is a partial page",
			resp.AssetCount.Filtered, len(resp.Assets))
	}
	return &HostAssets{Document: raw, Count: len(resp.Assets)}, nil
}

// exchange writes one request and returns the raw response document.
func (c *Client) exchange(command, request string) ([]byte, error) {
	if _, err := io.WriteString(c.t, request); err != nil {
		return nil, fmt.Errorf("%s: failed to send: %w", command, err)
	}
	raw, err := readDocument(c.t, c.MaxResponseBytes)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return nil, &ResponseTooLargeError{Command: command, Limit: c.MaxResponseBytes}
		}
		return nil, fmt.Errorf("%s: failed to read the response: %w", command, err)
	}
	return raw, nil
}

// roundTrip writes one request and decodes one response.
func (c *Client) roundTrip(command, request string, out interface{}) error {
	raw, err := c.exchange(command, request)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: response could not be read as %s: %w", command, command+"_response", err)
	}
	return nil
}

// endWatcher records whether its reader has reported the end of the stream.
type endWatcher struct {
	r     io.Reader
	ended bool
}

func (e *endWatcher) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil {
		e.ended = true
	}
	return n, err
}

// errTooLarge is readDocument's signal that the limit was passed.
var errTooLarge = errors.New("response too large")

// readDocument reads until one complete XML document has arrived, and returns
// it byte for byte.
//
// GMP frames nothing: responses carry no length and the connection stays open
// for the next command, so the only end marker is the document closing itself.
// One decoder follows the stream token by token until the root element
// balances, which keeps the cost linear in the size of the response — a host
// inventory can run to megabytes. The decoder reads ahead in chunks, but only
// within this response: the appliance sends nothing more until the next
// request.
//
// limit caps the bytes read; zero means none.
func readDocument(r io.Reader, limit int) ([]byte, error) {
	var buf bytes.Buffer
	src := io.Reader(r)
	if limit > 0 {
		// One byte over, so reaching it is distinguishable from fitting.
		src = io.LimitReader(r, int64(limit)+1)
	}
	ended := &endWatcher{r: src}
	dec := xml.NewDecoder(io.TeeReader(ended, &buf))
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if limit > 0 && buf.Len() > limit {
				return nil, errTooLarge
			}
			// The decoder reports a stream that stops mid-document as a syntax
			// error, so whether the channel ended is asked of the reader.
			if buf.Len() > 0 && ended.ended {
				// A closed channel with a partial document is a truncated
				// answer, not an empty one; say which.
				return nil, fmt.Errorf("connection ended after %d bytes of an incomplete response: %w", buf.Len(), io.ErrUnexpectedEOF)
			}
			return nil, err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
			if depth == 0 {
				end := int(dec.InputOffset())
				return bytes.TrimLeft(buf.Bytes()[:end], " \t\r\n"), nil
			}
		}
	}
}

// escape makes a credential safe to put inside an XML element. A password
// containing & or < is otherwise a malformed request — and, worse, a password
// containing a tag would be a request of the caller's choosing.
func escape(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}
