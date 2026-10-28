// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestBackendCodexAuthUploadReplaceClear(t *testing.T) {
	t.Parallel()
	_, store, ctx := testClient(t)
	mc := model.DefaultConfig()
	mc.AgentBackends = []model.AgentBackend{{ID: "one", Framework: "codex"}, {ID: "two", Framework: "codex"}, {ID: "claude", Framework: "claude"}}
	if err := store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(t.TempDir(), "auth.json")
	if err := codex.WriteAuthFile(shared, []byte(`{"account":"shared"}`)); err != nil {
		t.Fatal(err)
	}
	srv := ui.NewServer(ui.Config{Actor: ui.DefaultActor("alice"), CodexAuthFile: shared}, store)
	put := func(id, value string, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"value": value})
		r := do(t, srv, http.MethodPut, "/api/agent-backends/"+id+"/codex-auth", string(body))
		if r.Code != want {
			t.Fatalf("PUT %s: %d %s", id, r.Code, r.Body)
		}
		if strings.Contains(r.Body.String(), "private-token") {
			t.Fatal("credential leaked in response")
		}
	}
	put("one", `{"token":"private-token-one"}`, http.StatusOK)
	put("two", `{"token":"private-token-two"}`, http.StatusOK)
	put("one", `{"token":"private-token-replaced"}`, http.StatusOK)
	put("one", `[]`, http.StatusBadRequest)
	put("missing", `{}`, http.StatusNotFound)
	put("claude", `{}`, http.StatusNotFound)
	for id, want := range map[string]string{"one": `{"token":"private-token-replaced"}`, "two": `{"token":"private-token-two"}`} {
		path := codex.BackendAuthFile(shared, id)
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s login was overwritten", id)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s login permissions", id)
		}
		r := do(t, srv, http.MethodGet, "/api/agent-backends/"+id+"/codex-auth", "")
		got := decode[map[string]bool](t, r)
		if !got["enabled"] || !got["set"] || !got["sharedSet"] {
			t.Fatalf("status: %v", got)
		}
	}
	r := do(t, srv, http.MethodDelete, "/api/agent-backends/one/codex-auth", "")
	got := decode[map[string]bool](t, r)
	if got["set"] || !got["sharedSet"] {
		t.Fatalf("cleared status: %v", got)
	}
	if _, err := os.Stat(codex.BackendAuthFile(shared, "one")); !os.IsNotExist(err) {
		t.Fatal("login not removed")
	}
	if err := codex.ValidateAuthFile(codex.BackendAuthFile(shared, "two")); err != nil {
		t.Fatal("other backend changed", err)
	}
	data, _ := os.ReadFile(shared)
	if string(data) != `{"account":"shared"}` {
		t.Fatal("shared login changed")
	}
}

func TestBackendCodexAuthModeSettings(t *testing.T) {
	t.Parallel()
	_, store, ctx := testClient(t)
	cfg := model.DefaultConfig()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	srv := ui.NewServer(ui.Config{Actor: ui.DefaultActor("alice")}, store)
	for _, mode := range []string{"", "api_key", "subscription"} {
		body, _ := json.Marshal(map[string]any{"agentBackends": []model.AgentBackend{{ID: "codex", Framework: "codex", AuthMode: mode}}})
		r := do(t, srv, http.MethodPut, "/api/settings", string(body))
		if r.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", mode, r.Code, r.Body)
		}
		mc, err := store.GetConfig(ctx)
		if err != nil || mc.AgentBackends[0].AuthMode != mode {
			t.Fatal("auth mode did not persist")
		}
	}
	for _, mode := range []string{"", "api_key", "ambient"} {
		body, _ := json.Marshal(map[string]any{"agentBackends": []model.AgentBackend{{ID: "agy", Framework: "antigravity", AuthMode: mode}}})
		r := do(t, srv, http.MethodPut, "/api/settings", string(body))
		if r.Code != http.StatusOK {
			t.Fatalf("antigravity %s: %d %s", mode, r.Code, r.Body)
		}
	}
	for _, mode := range []string{"", "api_key", "vertex"} {
		body, _ := json.Marshal(map[string]any{"agentBackends": []model.AgentBackend{{ID: "claude", Framework: "claude", AuthMode: mode}}})
		r := do(t, srv, http.MethodPut, "/api/settings", string(body))
		if r.Code != http.StatusOK {
			t.Fatalf("claude %s: %d %s", mode, r.Code, r.Body)
		}
	}
	for _, body := range []string{
		`{"agentBackends":[{"id":"a","framework":"codex","authMode":"unknown"}]}`,
		`{"agentBackends":[{"id":"a","framework":"claude","authMode":"subscription"}]}`,
	} {
		r := do(t, srv, http.MethodPut, "/api/settings", body)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("invalid auth mode: %d %s", r.Code, r.Body)
		}
	}
}

func TestBackendAuthenticationReuseMustNameADirectBackendOfTheSameType(t *testing.T) {
	t.Parallel()
	_, store, ctx := testClient(t)
	cfg := model.DefaultConfig()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	srv := ui.NewServer(ui.Config{Actor: ui.DefaultActor("alice")}, store)
	valid := `{"agentBackends":[{"id":"one","framework":"codex","authMode":"api_key"},{"id":"two","framework":"codex","credentialBackendId":"one"}]}`
	if r := do(t, srv, http.MethodPut, "/api/settings", valid); r.Code != http.StatusOK {
		t.Fatalf("valid reuse: %d %s", r.Code, r.Body)
	}
	for _, body := range []string{
		`{"agentBackends":[{"id":"one","framework":"codex","credentialBackendId":"one"}]}`,
		`{"agentBackends":[{"id":"one","framework":"claude"},{"id":"two","framework":"codex","credentialBackendId":"one"}]}`,
		`{"agentBackends":[{"id":"two","framework":"codex","credentialBackendId":"missing"}]}`,
	} {
		if r := do(t, srv, http.MethodPut, "/api/settings", body); r.Code != http.StatusBadRequest {
			t.Fatalf("invalid reuse accepted: %d %s", r.Code, r.Body)
		}
	}
}

// TestBackendCodexAuthReportsARefusedRefresh: when the authority refuses
// grain's refresh of a backend's login for good, the login's status says
// so, and the file is left in place for the operator to replace.
func TestBackendCodexAuthReportsARefusedRefresh(t *testing.T) {
	t.Parallel()
	_, store, ctx := testClient(t)
	mc := model.DefaultConfig()
	mc.AgentBackends = []model.AgentBackend{{ID: "one", Framework: "codex"}}
	if err := store.PutConfig(ctx, mc); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(t.TempDir(), "auth.json")
	login := `{"tokens":{"access_token":"not-a-jwt","refresh_token":"private-refresh-token"}}`
	path := codex.BackendAuthFile(shared, "one")
	if err := codex.WriteAuthFile(path, []byte(login)); err != nil {
		t.Fatal(err)
	}
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"code":"refresh_token_reused","message":"already used"}}`))
	}))
	defer authority.Close()
	if _, err := codex.RefreshAuthFile(ctx, path, codex.RefreshOptions{TokenURL: authority.URL, Force: true}); err == nil {
		t.Fatal("a refused refresh reported success")
	}
	srv := ui.NewServer(ui.Config{Actor: ui.DefaultActor("alice"), CodexAuthFile: shared}, store)
	r := do(t, srv, http.MethodGet, "/api/agent-backends/one/codex-auth", "")
	var got struct {
		Set          bool   `json:"set"`
		RefreshError string `json:"refreshError"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Set || !strings.Contains(got.RefreshError, "refresh_token_reused") {
		t.Fatalf("status %s does not report the refusal", r.Body)
	}
	if strings.Contains(r.Body.String(), "private-refresh-token") {
		t.Fatal("credential leaked in response")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != login {
		t.Fatalf("the refused login was changed or removed: %v", err)
	}
	// Uploading a new login clears it.
	body, _ := json.Marshal(map[string]string{"value": `{"tokens":{"refresh_token":"new"}}`})
	r = do(t, srv, http.MethodPut, "/api/agent-backends/one/codex-auth", string(body))
	if strings.Contains(r.Body.String(), "refreshError") {
		t.Fatalf("a new login still reports the old refusal: %s", r.Body)
	}
}
