// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gmp speaks the Greenbone Management Protocol to an OPENVAS SCAN
// appliance, so the proxy can ask it what hosts it knows about and what its
// latest scans found.
//
// It transports and does not interpret. The host inventory is handed on as the
// appliance's own document, unmodified, and turned into assets by the
// platform's analysis and import rules like any other source — the same way an
// Nmap or OpenVAS report file is. Format knowledge kept here would sit on
// customer hosts, change only with a proxy release, and drop whatever this
// package did not think to keep. The one exception, the scan coverage — an
// answer no single document of the appliance holds — is worked out in the
// subpackage scancoverage, so this package stays transport alone.
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
	// Count is how many host asset records the document holds.
	Count int
	// Hosts is how many distinct hosts those records are, by the asset's
	// name (its address). The appliance can hold several records of one host
	// — created by different scans — and the platform's import merges them
	// into one asset, so this is the number of assets an import yields.
	Hosts int
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
			ID   string `xml:"id,attr"`
			Name string `xml:"name"`
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
	hosts := map[string]bool{}
	for _, a := range resp.Assets {
		hosts[strings.TrimSpace(a.Name)] = true
	}
	return &HostAssets{Document: raw, Count: len(resp.Assets), Hosts: len(hosts)}, nil
}

// TaskReport is a scan task and the report of its latest run.
type TaskReport struct {
	TaskID   string
	TaskName string
	ReportID string
}

// lastReportsRequest asks for every scan task. usage_type="scan" leaves out
// compliance audits, whose results are policy checks rather than
// vulnerabilities; rows=-1 lifts the paging as for the host assets.
const lastReportsRequest = `<get_tasks usage_type="scan" filter="first=1 rows=-1"/>`

// LastReports returns each scan task's latest report. A task that has never
// finished a run has none and is left out. Only identifiers are read here;
// the results themselves come from Results.
func (c *Client) LastReports() ([]TaskReport, error) {
	raw, err := c.exchange("get_tasks", lastReportsRequest)
	if err != nil {
		return nil, err
	}
	var resp struct {
		XMLName    xml.Name `xml:"get_tasks_response"`
		Status     string   `xml:"status,attr"`
		StatusText string   `xml:"status_text,attr"`
		Tasks      []struct {
			ID         string `xml:"id,attr"`
			Name       string `xml:"name"`
			LastReport struct {
				Report struct {
					ID string `xml:"id,attr"`
				} `xml:"report"`
			} `xml:"last_report"`
		} `xml:"task"`
		TaskCount struct {
			Filtered int `xml:"filtered"`
		} `xml:"task_count"`
	}
	if err := xml.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("get_tasks: response could not be read as get_tasks_response: %w", err)
	}
	if resp.Status != "200" {
		return nil, &StatusError{Command: "get_tasks", Status: resp.Status, Text: resp.StatusText}
	}
	if resp.TaskCount.Filtered > len(resp.Tasks) {
		return nil, fmt.Errorf("get_tasks: the appliance reports %d tasks but returned %d — the response is a partial page",
			resp.TaskCount.Filtered, len(resp.Tasks))
	}
	var reports []TaskReport
	for _, t := range resp.Tasks {
		if t.LastReport.Report.ID == "" {
			continue
		}
		reports = append(reports, TaskReport{TaskID: t.ID, TaskName: t.Name, ReportID: t.LastReport.Report.ID})
	}
	return reports, nil
}

// Results is one report's results as the appliance sent them.
type Results struct {
	// Document is the complete <get_results_response>, byte for byte.
	Document []byte
	// Count is how many results the document holds.
	Count int
}

// resultsFilter selects one report's results the way the appliance's own
// web interface shows them by default: overrides applied, and only results
// of a quality of detection of at least 70 %. rows=-1 lifts the paging.
const resultsFilter = "apply_overrides=1 min_qod=70 first=1 rows=-1"

// Results returns the results of one report, as the appliance's own
// document. details="1" includes each result's NVT details.
//
// As with the host assets, a response holding fewer results than the
// appliance says match is an error rather than a shorter list.
func (c *Client) Results(reportID string) (*Results, error) {
	if !isGMPID(reportID) {
		return nil, fmt.Errorf("get_results: %q is not a report id", reportID)
	}
	raw, err := c.exchange("get_results",
		`<get_results details="1" filter="report_id=`+reportID+` `+resultsFilter+`"/>`)
	if err != nil {
		return nil, err
	}
	var resp struct {
		XMLName    xml.Name `xml:"get_results_response"`
		Status     string   `xml:"status,attr"`
		StatusText string   `xml:"status_text,attr"`
		Results    []struct {
			ID string `xml:"id,attr"`
		} `xml:"result"`
		ResultCount struct {
			Filtered int `xml:"filtered"`
		} `xml:"result_count"`
	}
	if err := xml.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("get_results: response could not be read as get_results_response: %w", err)
	}
	if resp.Status != "200" {
		return nil, &StatusError{Command: "get_results", Status: resp.Status, Text: resp.StatusText}
	}
	if resp.ResultCount.Filtered > len(resp.Results) {
		return nil, fmt.Errorf("get_results: the appliance reports %d results but returned %d — the response is a partial page",
			resp.ResultCount.Filtered, len(resp.Results))
	}
	return &Results{Document: raw, Count: len(resp.Results)}, nil
}

// scanTasksRequest asks for every scan task, with its target's id and its
// schedule — the schedule's iCalendar text included. Compliance audits are
// left out as for the results.
const scanTasksRequest = `<get_tasks usage_type="scan" filter="first=1 rows=-1"/>`

// targetsRequest asks for every target, with its hosts and excluded hosts.
const targetsRequest = `<get_targets filter="first=1 rows=-1"/>`

// ScanTasks returns every scan task as the appliance's own
// <get_tasks_response>. Unlike the results, this is a document the caller
// interprets: the scan coverage is worked out from it and the targets.
func (c *Client) ScanTasks() ([]byte, error) {
	return c.completeList("get_tasks", scanTasksRequest, "task")
}

// Targets returns every target as the appliance's own <get_targets_response>.
func (c *Client) Targets() ([]byte, error) {
	return c.completeList("get_targets", targetsRequest, "target")
}

// completeList sends a get_<things> command and returns its response, refused
// unless the status is 200 and the response holds every item the appliance
// counts — a partial list read as the whole would look complete.
func (c *Client) completeList(command, request, item string) ([]byte, error) {
	raw, err := c.exchange(command, request)
	if err != nil {
		return nil, err
	}
	var resp struct {
		XMLName    xml.Name
		Status     string `xml:"status,attr"`
		StatusText string `xml:"status_text,attr"`
	}
	if err := xml.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%s: response could not be read as %s_response: %w", command, command, err)
	}
	if resp.XMLName.Local != command+"_response" {
		return nil, fmt.Errorf("%s: the appliance answered with <%s>", command, resp.XMLName.Local)
	}
	if resp.Status != "200" {
		return nil, &StatusError{Command: command, Status: resp.Status, Text: resp.StatusText}
	}
	var count struct {
		Filtered *int `xml:"filtered"`
	}
	got := 0
	dec := xml.NewDecoder(bytes.NewReader(raw))
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch el := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 2 && el.Name.Local == item {
				got++
			}
			if depth == 2 && el.Name.Local == item+"_count" {
				if err := dec.DecodeElement(&count, &el); err != nil {
					return nil, fmt.Errorf("%s: unreadable %s_count: %w", command, item, err)
				}
				depth--
			}
		case xml.EndElement:
			depth--
		}
	}
	if count.Filtered != nil && *count.Filtered > got {
		return nil, fmt.Errorf("%s: the appliance reports %d %ss but returned %d — the response is a partial page",
			command, *count.Filtered, item, got)
	}
	return raw, nil
}

// isGMPID reports whether s looks like a GMP resource id — a UUID. The id
// goes into a filter string, so anything else is refused rather than quoted.
func isGMPID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
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
