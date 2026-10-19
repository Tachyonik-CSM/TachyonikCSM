// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the latest-report results against recorded answers of the same
// appliance as gmp_test.go (GMP 22.8): which tasks' reports are asked for,
// that each report's results come back byte for byte, and that a partial page
// or a report id that is not one is refused.

package gmp

import (
	"io"
	"strings"
	"testing"
)

func TestLastReportsAreEachScanTasksLatest(t *testing.T) {
	c, ft := clientWith(t, fixture(t, "get_tasks_scan.xml"))
	reports, err := c.LastReports()
	if err != nil {
		t.Fatalf("LastReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want the 3 tasks' latest", len(reports))
	}
	if r := reports[0]; r.TaskName != "Debian Authenticated" || r.ReportID != "0ad580e4-fbe1-441d-ac66-5a715b6cb0fe" {
		t.Errorf("first report = %+v", r)
	}
	sent := ft.written.String()
	for _, want := range []string{`<get_tasks`, `usage_type="scan"`, `rows=-1`} {
		if !strings.Contains(sent, want) {
			t.Errorf("request lacks %s: %s", want, sent)
		}
	}
}

// A task that has never finished a run has no last report and is left out.
func TestATaskWithoutAReportIsLeftOut(t *testing.T) {
	doc := `<get_tasks_response status="200" status_text="OK">` +
		`<task id="t1"><name>Never run</name><last_report></last_report></task>` +
		`<task id="t2"><name>Ran</name><last_report><report id="0ad580e4-fbe1-441d-ac66-5a715b6cb0fe"/></last_report></task>` +
		`<task_count>2<filtered>2</filtered><page>2</page></task_count></get_tasks_response>`
	c, _ := clientWith(t, doc)
	reports, err := c.LastReports()
	if err != nil || len(reports) != 1 || reports[0].TaskName != "Ran" {
		t.Fatalf("got %+v, %v; want only the task that ran", reports, err)
	}
}

func TestResultsAreTheAppliancesOwnDocument(t *testing.T) {
	doc := fixture(t, "get_results_report.xml")
	c, ft := clientWith(t, doc)
	res, err := c.Results("0ad580e4-fbe1-441d-ac66-5a715b6cb0fe")
	if err != nil {
		t.Fatalf("Results: %v", err)
	}
	if string(res.Document) != strings.TrimLeft(doc, " \t\r\n") {
		t.Error("the document was altered on the way through")
	}
	if res.Count != 13 {
		t.Errorf("count = %d, want 13", res.Count)
	}
	sent := ft.written.String()
	for _, want := range []string{`<get_results`, `details="1"`, `report_id=0ad580e4-fbe1-441d-ac66-5a715b6cb0fe`,
		`apply_overrides=1`, `min_qod=70`, `rows=-1`} {
		if !strings.Contains(sent, want) {
			t.Errorf("request lacks %s: %s", want, sent)
		}
	}
}

// scriptedTransport answers each request with the next response, and only
// after the request: like an appliance, it sends nothing unasked, so the
// decoder's read-ahead cannot reach into the next answer.
type scriptedTransport struct {
	responses []string
	current   *strings.Reader
}

func (s *scriptedTransport) Write(p []byte) (int, error) {
	if len(s.responses) > 0 {
		s.current = strings.NewReader(s.responses[0])
		s.responses = s.responses[1:]
	}
	return len(p), nil
}

func (s *scriptedTransport) Read(p []byte) (int, error) {
	if s.current == nil {
		return 0, io.EOF
	}
	return s.current.Read(p)
}

func (s *scriptedTransport) Close() error { return nil }

// The tasks and each report's results come over one session, one after the
// other — the order the built-in asks in.
func TestTasksThenResultsOverOneSession(t *testing.T) {
	c := New(&scriptedTransport{responses: []string{fixture(t, "get_tasks_scan.xml"), fixture(t, "get_results_report.xml")}})
	reports, err := c.LastReports()
	if err != nil {
		t.Fatalf("LastReports: %v", err)
	}
	res, err := c.Results(reports[0].ReportID)
	if err != nil || res.Count != 13 {
		t.Fatalf("Results after LastReports: %v, %+v", err, res)
	}
}

func TestAPartialResultsPageIsRefused(t *testing.T) {
	doc := strings.Replace(fixture(t, "get_results_report.xml"), "<filtered>13</filtered>", "<filtered>20</filtered>", 1)
	c, _ := clientWith(t, doc)
	_, err := c.Results("0ad580e4-fbe1-441d-ac66-5a715b6cb0fe")
	if err == nil || !strings.Contains(err.Error(), "partial page") {
		t.Fatalf("got %v, want the partial page refused", err)
	}
}

// The id goes into a filter string: anything but an id is refused before a
// request is sent.
func TestAReportIDThatIsNotOneIsRefused(t *testing.T) {
	for _, id := range []string{"", "abc", "0ad580e4-fbe1-441d-ac66-5a715b6cb0fe rows=1", `0ad580e4-fbe1-441d-ac66-5a715b6cb0f"`} {
		c, ft := clientWith(t, "")
		if _, err := c.Results(id); err == nil {
			t.Errorf("%q: accepted", id)
		}
		if ft.written.Len() != 0 {
			t.Errorf("%q: a request was sent", id)
		}
	}
}
