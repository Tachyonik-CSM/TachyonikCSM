// ActionExecutor
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tests the automatic run's own two requests — reading the user's automation
// setting and deleting the handled action. They go through the bridge now, and
// must carry the service key and the action's owner as before.

package autoexecute

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"tachyonik/actionexecutor/internal/apibridge"
	"tachyonik/actionexecutor/internal/config"
)

type seenRequest struct {
	method, path, key, userID, body string
}

func handlerAgainst(t *testing.T, answer string) (*Handler, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, seenRequest{r.Method, r.URL.Path, r.Header.Get("X-Internal-Service-Key"), r.Header.Get("X-User-ID"), string(b)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.SystemManager.URL, cfg.SystemManager.InternalServiceKey = srv.URL, "sm-key"
	cfg.ActionManager.URL, cfg.ActionManager.InternalServiceKey = srv.URL, "am-key"
	return &Handler{bridge: apibridge.New(cfg), cfg: cfg}, &seen
}

func TestTheAutomationSettingIsReadAsTheUser(t *testing.T) {
	h, seen := handlerAgainst(t, `{"settings":[{"executionRuleId":4,"status":"automatic"},{"executionRuleId":3,"status":"manual"}]}`)
	auto, err := h.checkAutomationStatus(52, 4)
	if err != nil || !auto {
		t.Fatalf("rule 4: automatic=%v err=%v, want automatic", auto, err)
	}
	if manual, _ := h.checkAutomationStatus(52, 3); manual {
		t.Error("rule 3 reported automatic")
	}
	if missing, _ := h.checkAutomationStatus(52, 9); missing {
		t.Error("a rule without a setting reported automatic; the default is manual")
	}
	r := (*seen)[0]
	if r.path != "/api/user-execution-rule-settings" || r.key != "sm-key" || r.userID != "52" {
		t.Errorf("request %+v, want the settings route with the SystemManager key as user 52", r)
	}
}

func TestTheHandledActionIsDeletedAsTheUser(t *testing.T) {
	h, seen := handlerAgainst(t, `{}`)
	if err := h.deleteAction(42, 52); err != nil {
		t.Fatalf("deleteAction: %v", err)
	}
	r := (*seen)[0]
	if r.method != http.MethodDelete || r.path != "/api/actions" || r.key != "am-key" || r.userID != "52" {
		t.Errorf("request %+v, want DELETE /api/actions with the ActionManager key as user 52", r)
	}
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.Unmarshal([]byte(r.body), &body); err != nil || len(body.IDs) != 1 || body.IDs[0] != 42 {
		t.Errorf("body %q, want {\"ids\":[42]}", r.body)
	}
}
