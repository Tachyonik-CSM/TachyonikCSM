// SourceImporter
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the records' schemas from AssetManager's document at work: the
// system prompt's {{OUTPUT_SCHEMA}} gets them, a prompt without it is used as
// it is, generation with it waits for the document, a routine whose records
// do not fit fails validation, and the import test reports the records that
// do not fit.

package aiimporter

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"tachyonik/lib/apicontract"
	"tachyonik/sourceimporter/internal/aimanager"
	"tachyonik/sourceimporter/internal/config"
	"tachyonik/sourceimporter/internal/outputschema"
	"tachyonik/sourceimporter/internal/rscmanager"
)

// recordingChat answers with a fixed routine and remembers the system prompt.
type recordingChat struct {
	routine string
	mu      sync.Mutex
	system  string
	asked   bool
}

func (c *recordingChat) Chat(model, systemPrompt, userPrompt string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.system, c.asked = systemPrompt, true
	return c.routine, nil
}

// fakeAIManager stores routines and serves one routine and one file; it
// remembers the routine stored last.
type fakeAIManager struct {
	srv    *httptest.Server
	mu     sync.Mutex
	stored aimanager.CreateRoutineRequest
}

func newFakeAIManager(t *testing.T, routine, file string) *fakeAIManager {
	f := &fakeAIManager{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/internal/routines":
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			_ = json.Unmarshal(body, &f.stored)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 5, "rule": 1, "type": "SourceImporter", "status": "passed"}`))
		case r.Method == "GET" && r.URL.Path == "/api/internal/routines/5":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "rule": 1, "code": routine, "status": "draft"})
		case r.Method == "GET" && r.URL.Path == "/api/sources/9/file":
			w.Header().Set("X-Original-Filename", "scan.txt")
			_, _ = w.Write([]byte(file))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// loadedSchemas loads AssetManager's real document into ai.
func loadedSchemas(t *testing.T, ai *AIImporter) {
	t.Helper()
	path := apicontract.DocumentPath("AssetManager")
	if path == "" {
		t.Skip("AssetManager/spec/openapi.yaml not found: the module is built without its siblings")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ai.OutputSchemas().Load(string(data)); err != nil {
		t.Fatal(err)
	}
}

var rule = &aimanager.ImportRule{ID: 1, Type: "nmap", Version: "1.0"}

func TestThePlaceholderGetsTheRecordSchemas(t *testing.T) {
	f := newFakeAIManager(t, "", "")
	chat := &recordingChat{routine: sampleImportRoutine}
	ai := New(nil, aimanager.NewClient(f.srv.URL, "k"), nil, chat, &config.Config{AI: config.AIConfig{SystemPrompt: "Write a routine.\n" + outputschema.Placeholder + "\nDone.", Model: "m"}})
	loadedSchemas(t, ai)
	if _, err := ai.GenerateForRule(rule, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(chat.system, outputschema.Placeholder) ||
		!strings.Contains(chat.system, "Vulnerability record (in vulnerabilities)") ||
		!strings.Contains(chat.system, "- severity: integer, required") ||
		!strings.HasPrefix(chat.system, "Write a routine.\nRETURN VALUE") {
		t.Errorf("system prompt:\n%s", chat.system)
	}
	if f.stored.Status != "passed" {
		t.Errorf("the routine was stored %q: %s", f.stored.Status, f.stored.Log)
	}
}

// A prompt without the placeholder predates AssetManager's record schemas:
// it is refused before the AI is asked, and nothing is stored.
func TestAPromptWithoutThePlaceholderIsRefused(t *testing.T) {
	f := newFakeAIManager(t, "", "")
	chat := &recordingChat{routine: sampleImportRoutine}
	ai := New(nil, aimanager.NewClient(f.srv.URL, "k"), nil, chat, &config.Config{AI: config.AIConfig{SystemPrompt: "the old prompt", Model: "m"}})
	loadedSchemas(t, ai)
	_, err := ai.GenerateForRule(rule, true)
	if err == nil || !strings.Contains(err.Error(), "predates AssetManager's record schemas") {
		t.Fatalf("err = %v", err)
	}
	if chat.asked {
		t.Error("the AI was asked with a prompt that predates the record schemas")
	}
	if f.stored.Status != "" {
		t.Errorf("a routine was stored: %q", f.stored.Status)
	}
}

func TestThePlaceholderWithoutTheDocumentHoldsGenerationBack(t *testing.T) {
	f := newFakeAIManager(t, "", "")
	chat := &recordingChat{routine: sampleImportRoutine}
	ai := New(nil, aimanager.NewClient(f.srv.URL, "k"), nil, chat, &config.Config{AI: config.AIConfig{SystemPrompt: outputschema.Placeholder, Model: "m"}})
	_, err := ai.GenerateForRule(rule, true)
	if err == nil || !strings.Contains(err.Error(), outputschema.Placeholder) {
		t.Errorf("GenerateForRule = %v, want it held back for the placeholder", err)
	}
	if chat.asked {
		t.Error("the AI was asked with the placeholder unfilled")
	}
}

// misfitRoutine returns an asset of an unknown type and a severity on the
// CVSS scale.
const misfitRoutine = `
var importFunction = function (ctx) {
  return {
    assets: [{ name: "192.168.1.1", type: "server" }],
    vulnerabilities: [{ name: "SQLi", host: "192.168.1.1", port: "443/tcp", severity: 7.5 }],
    detections: []
  };
};
`

func TestARoutineWhoseRecordsDoNotFitFailsValidation(t *testing.T) {
	f := newFakeAIManager(t, "", "")
	ai := New(nil, aimanager.NewClient(f.srv.URL, "k"), nil, &recordingChat{routine: misfitRoutine}, &config.Config{AI: config.AIConfig{SystemPrompt: "p\n" + outputschema.Placeholder, Model: "m"}})
	loadedSchemas(t, ai)
	if _, err := ai.GenerateForRule(rule, true); err == nil {
		t.Fatal("a routine whose records do not fit passed validation")
	}
	if f.stored.Status != "failed" ||
		!strings.Contains(f.stored.Log, `assets[0]: type: server is not one of [Host]`) ||
		!strings.Contains(f.stored.Log, `vulnerabilities[0]: severity: a number is not integer`) {
		t.Errorf("stored %q with log:\n%s", f.stored.Status, f.stored.Log)
	}
	if ai.HasExecutor(1) {
		t.Error("the failed routine was put to work")
	}
}

func TestTheImportTestReportsRecordsThatDoNotFit(t *testing.T) {
	f := newFakeAIManager(t, misfitRoutine, "anything")
	client := aimanager.NewClient(f.srv.URL, "k")
	ai := New(rscmanager.NewClient(f.srv.URL, "k"), client, nil, nil, &config.Config{})
	source := &rscmanager.Source{ID: 9, Filename: "scan.txt", SourceType: "nmap"}

	// Without the document the test runs as it did, unchecked.
	summary, err := ai.RunImportTest(source, 5)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Assets != 1 || summary.Violations != nil {
		t.Errorf("unchecked summary = %+v", summary)
	}

	loadedSchemas(t, ai)
	if summary, err = ai.RunImportTest(source, 5); err != nil {
		t.Fatal(err)
	}
	if summary.Assets != 1 || summary.Vulnerabilities != 1 || len(summary.Violations) != 2 ||
		!strings.HasPrefix(summary.Violations[0], "1 of the assets do not fit AssetManager's document: assets[0]: type:") {
		t.Errorf("summary = %+v", summary)
	}
}
