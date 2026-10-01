// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gmp speaks the Greenbone Management Protocol to an OPENVAS SCAN
// appliance, so the proxy can ask it what hosts it knows about.
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
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// Host is one host asset as the appliance knows it.
type Host struct {
	// IP is the asset's name in GMP terms, which for a host asset is its
	// address — that is where the appliance puts it.
	IP string
	// Hostname and OS come from the asset's identifiers and are often absent:
	// a host that answered a ping and nothing else has neither.
	Hostname string
	// OS is a CPE string ("cpe:/o:debian:debian_linux:13"), URL-escaped by the
	// appliance, and reported as the appliance recorded it.
	OS string
	// LastSeen is the asset's modification time, which is when the appliance
	// last learned something about it.
	LastSeen time.Time
}

// Client is a GMP session. Not safe for concurrent use: the protocol is one
// request and one response at a time over a single channel.
type Client struct {
	t Transport
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

// assetsResponse mirrors only the parts of get_assets this needs. GMP returns
// a great deal more per asset — permissions, sources, every identifier the
// scanner ever recorded — and decoding what we do not use would be a standing
// invitation to break on a field the appliance changes.
type assetsResponse struct {
	XMLName    xml.Name `xml:"get_assets_response"`
	Status     string   `xml:"status,attr"`
	StatusText string   `xml:"status_text,attr"`
	Assets     []struct {
		Name             string `xml:"name"`
		ModificationTime string `xml:"modification_time"`
		Identifiers      struct {
			Identifier []struct {
				Name  string `xml:"name"`
				Value string `xml:"value"`
			} `xml:"identifier"`
		} `xml:"identifiers"`
	} `xml:"asset"`
}

// Hosts returns every host asset the appliance holds.
//
// details="1" is what makes the identifiers — hostname and OS — part of the
// answer; without it an asset is only its address.
func (c *Client) Hosts() ([]Host, error) {
	var resp assetsResponse
	if err := c.roundTrip("get_assets", `<get_assets type="host" details="1"/>`, &resp); err != nil {
		return nil, err
	}
	if resp.Status != "200" {
		return nil, &StatusError{Command: "get_assets", Status: resp.Status, Text: resp.StatusText}
	}

	hosts := make([]Host, 0, len(resp.Assets))
	for _, a := range resp.Assets {
		h := Host{IP: a.Name}
		// The first identifier of each kind wins. An asset accumulates one per
		// report that observed it, newest first in practice, and they usually
		// agree; where they do not, the appliance's own ordering is a better
		// guess than ours.
		for _, id := range a.Identifiers.Identifier {
			switch {
			case id.Name == "hostname" && h.Hostname == "":
				h.Hostname = id.Value
			case id.Name == "OS" && h.OS == "":
				h.OS = id.Value
			}
		}
		if t, err := time.Parse(time.RFC3339, a.ModificationTime); err == nil {
			h.LastSeen = t
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// roundTrip writes one request and decodes one response.
func (c *Client) roundTrip(command, request string, out interface{}) error {
	if _, err := io.WriteString(c.t, request); err != nil {
		return fmt.Errorf("%s: failed to send: %w", command, err)
	}
	raw, err := readDocument(c.t)
	if err != nil {
		return fmt.Errorf("%s: failed to read the response: %w", command, err)
	}
	if err := xml.Unmarshal([]byte(raw), out); err != nil {
		return fmt.Errorf("%s: response could not be read as %s: %w", command, command+"_response", err)
	}
	return nil
}

// readDocument reads until one complete XML document has arrived.
//
// GMP frames nothing: responses carry no length and the connection stays open
// for the next command, so the only end marker is the document closing itself.
// Reads are therefore accumulated and re-parsed until the root element
// balances.
func readDocument(r io.Reader) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
			if complete(sb.String()) {
				return sb.String(), nil
			}
		}
		if err != nil {
			if sb.Len() > 0 {
				// A closed channel with a partial document is a truncated
				// answer, not an empty one; say which.
				return "", fmt.Errorf("connection ended after %d bytes of an incomplete response: %w", sb.Len(), err)
			}
			return "", err
		}
	}
}

// complete reports whether the buffer holds one balanced XML element.
func complete(s string) bool {
	dec := xml.NewDecoder(strings.NewReader(s))
	depth := 0
	started := false
	for {
		tok, err := dec.Token()
		if err != nil {
			// Including io.ErrUnexpectedEOF: more is still coming.
			return false
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
			started = true
		case xml.EndElement:
			depth--
			if started && depth == 0 {
				return true
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
