// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the two lists the scan coverage is worked out from — the scan tasks
// and the targets: what is asked for, that the document comes back whole, and
// that a partial page, a refusal or an answer to another command is an error.

package gmp

import (
	"strings"
	"testing"
)

func TestScanTasksAreAskedForWithoutPaging(t *testing.T) {
	doc := fixture(t, "get_tasks_scan.xml")
	c, ft := clientWith(t, doc)
	raw, err := c.ScanTasks()
	if err != nil {
		t.Fatalf("ScanTasks: %v", err)
	}
	if string(raw) != strings.TrimLeft(doc, " \t\r\n") {
		t.Error("the document was altered on the way through")
	}
	for _, want := range []string{`<get_tasks`, `usage_type="scan"`, `rows=-1`} {
		if !strings.Contains(ft.written.String(), want) {
			t.Errorf("request lacks %s: %s", want, ft.written.String())
		}
	}
}

func TestTargetsAreReadWhole(t *testing.T) {
	doc := `<get_targets_response status="200" status_text="OK">` +
		`<target id="a"><name>A</name><hosts>10.0.0.1</hosts></target>` +
		`<target id="b"><name>B</name><hosts>10.0.0.2</hosts></target>` +
		`<target_count>2<filtered>2</filtered><page>2</page></target_count></get_targets_response>`
	c, ft := clientWith(t, doc)
	if _, err := c.Targets(); err != nil {
		t.Fatalf("Targets: %v", err)
	}
	if !strings.Contains(ft.written.String(), `<get_targets filter="first=1 rows=-1"/>`) {
		t.Errorf("request = %s", ft.written.String())
	}
}

func TestAPartialListOfTargetsIsRefused(t *testing.T) {
	doc := `<get_targets_response status="200" status_text="OK">` +
		`<target id="a"><name>A</name></target>` +
		`<target_count>2<filtered>12</filtered><page>1</page></target_count></get_targets_response>`
	c, _ := clientWith(t, doc)
	if _, err := c.Targets(); err == nil || !strings.Contains(err.Error(), "partial page") {
		t.Errorf("err = %v, want a partial page refused", err)
	}
}

func TestARefusedOrForeignAnswerIsAnError(t *testing.T) {
	c, _ := clientWith(t, `<get_targets_response status="400" status_text="Bogus"/>`)
	if _, err := c.Targets(); err == nil {
		t.Error("a refusal was accepted")
	}
	c, _ = clientWith(t, `<get_tasks_response status="200" status_text="OK"/>`)
	if _, err := c.Targets(); err == nil {
		t.Error("an answer to another command was accepted")
	}
}
