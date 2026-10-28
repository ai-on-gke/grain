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

// The agent-credential half of Settings: setting the key each framework
// runs as, clearing it, and reporting which are set -- without any
// response ever carrying a value back, the same rule the secrets pane
// these are built on already holds to.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestAgentKeysDisabledWithoutASecretsStore(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)

	rec := do(t, srv, http.MethodGet, "/api/agent-keys", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[map[string]any](t, rec); got["enabled"] != false {
		t.Fatalf("enabled = %v, want false", got["enabled"])
	}

	for _, call := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/agent-keys/claude", `{"value":"sk-ant-oat01-fake"}`},
		{http.MethodDelete, "/api/agent-keys/claude", ""},
	} {
		rec := do(t, srv, call.method, call.path, call.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404: %s", call.method, call.path, rec.Code, rec.Body)
		}
	}
}

func TestSetClaudeOAuthTokenStoresItWhereTheDaemonReadsIt(t *testing.T) {
	t.Parallel()
	srv, store := testServerWithSecrets(t)

	rec := do(t, srv, http.MethodPut, "/api/agent-keys/claude", `{"value":"  sk-ant-oat01-fake\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sk-ant-oat01-fake") {
		t.Fatalf("response leaked the token: %s", rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["claudeOAuthTokenSet"] != true {
		t.Fatalf("claudeOAuthTokenSet = %v, want true", got["claudeOAuthTokenSet"])
	}
	if got["geminiApiKeySet"] != false {
		t.Fatalf("geminiApiKeySet = %v, want false -- only the claude key was set", got["geminiApiKeySet"])
	}

	// The bare secret name, not "<name>/<key>": that sole-key form is
	// exactly how cmd/grain's own agentCredential resolves it, so this is
	// the assertion that a token pasted into the UI is a token the daemon
	// finds. Whitespace around a pasted value is gone, since the same
	// call would otherwise store something that reads as set and
	// authenticates as nothing.
	value, err := store.Resolve(context.Background(), secrets.ClaudeOAuthTokenSecret)
	if err != nil {
		t.Fatalf("Resolve(%q) = %v", secrets.ClaudeOAuthTokenSecret, err)
	}
	if value != "sk-ant-oat01-fake" {
		t.Fatalf("stored token = %q, want it trimmed", value)
	}
}

func TestSetGeminiAPIKeyThenClearIt(t *testing.T) {
	t.Parallel()
	srv, store := testServerWithSecrets(t)

	rec := do(t, srv, http.MethodPut, "/api/agent-keys/gemini", `{"value":"AIza-fake"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[map[string]any](t, rec); got["geminiApiKeySet"] != true {
		t.Fatalf("geminiApiKeySet = %v, want true", got["geminiApiKeySet"])
	}

	rec = do(t, srv, http.MethodDelete, "/api/agent-keys/gemini", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[map[string]any](t, rec); got["geminiApiKeySet"] != false {
		t.Fatalf("geminiApiKeySet = %v after clearing, want false", got["geminiApiKeySet"])
	}
	if _, err := store.Resolve(context.Background(), secrets.GeminiAPIKeySecret); err == nil {
		t.Fatal("the cleared key still resolves")
	}
}

func TestSetAgentKeyRejectsBlanksAndUnknownFrameworks(t *testing.T) {
	t.Parallel()
	srv, _ := testServerWithSecrets(t)

	rec := do(t, srv, http.MethodPut, "/api/agent-keys/gemini", `{"value":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("whitespace-only value status = %d, want 400: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodPut, "/api/agent-keys/gpt", `{"value":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown framework status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// The codex framework's own credential, through the same endpoints: an
// OpenAI API key, under the name cmd/grain's agentCredential resolves
// for a codex dispatch.
func TestSetOpenAIAPIKeyStoresItWhereTheDaemonReadsIt(t *testing.T) {
	t.Parallel()
	srv, store := testServerWithSecrets(t)

	rec := do(t, srv, http.MethodPut, "/api/agent-keys/codex", `{"value":"  sk-openai-fake\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sk-openai-fake") {
		t.Fatalf("response leaked the key: %s", rec.Body)
	}
	got := decode[map[string]any](t, rec)
	if got["openaiApiKeySet"] != true {
		t.Fatalf("openaiApiKeySet = %v, want true", got["openaiApiKeySet"])
	}
	if got["claudeOAuthTokenSet"] != false || got["geminiApiKeySet"] != false {
		t.Fatalf("response = %v, want only the openai key set", got)
	}

	value, err := store.Resolve(context.Background(), secrets.OpenAIAPIKeySecret)
	if err != nil {
		t.Fatalf("Resolve(%q) = %v", secrets.OpenAIAPIKeySecret, err)
	}
	if value != "sk-openai-fake" {
		t.Fatalf("stored key = %q, want it trimmed", value)
	}
}

func TestSetCodexAuthJSONThenClearIt(t *testing.T) {
	t.Parallel()
	srv, _ := testServerWithSecrets(t)

	body := `{"value":"{\"tokens\":{\"refresh_token\":\"never-return-this\"}}"}`
	rec := do(t, srv, http.MethodPut, "/api/codex-auth", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "never-return-this") {
		t.Fatalf("response leaked auth.json: %s", rec.Body)
	}
	if got := decode[map[string]any](t, rec); got["codexAuthJSONSet"] != true {
		t.Fatalf("codexAuthJSONSet = %v, want true", got["codexAuthJSONSet"])
	}

	rec = do(t, srv, http.MethodDelete, "/api/codex-auth", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[map[string]any](t, rec); got["codexAuthJSONSet"] != false {
		t.Fatalf("codexAuthJSONSet = %v after clearing, want false", got["codexAuthJSONSet"])
	}
}

func TestSetCodexAuthJSONRejectsNonObjectJSON(t *testing.T) {
	t.Parallel()
	srv, _ := testServerWithSecrets(t)
	for _, body := range []string{
		`{"value":""}`,
		`{"value":"[]"}`,
		`{"value":"not json"}`,
	} {
		rec := do(t, srv, http.MethodPut, "/api/codex-auth", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, want 400: %s", body, rec.Code, rec.Body)
		}
	}
}

func TestSettingsReportsWhichAgentKeysAreSet(t *testing.T) {
	t.Parallel()
	srv, _ := testServerWithSecrets(t)

	// Before anything is stored, and before this deployment's settings
	// have ever been saved at all -- the state an operator setting one up
	// is actually in.
	settings := decode[ui.Settings](t, do(t, srv, http.MethodGet, "/api/settings", ""))
	if !settings.AgentKeysEnabled {
		t.Fatal("agentKeysEnabled = false on a server with a secrets store")
	}
	if settings.GeminiAPIKeySet || settings.ClaudeOAuthTokenSet || settings.OpenAIAPIKeySet || settings.CodexAuthJSONSet {
		t.Fatalf("keys report as set before anything was stored: %+v", settings)
	}

	if rec := do(t, srv, http.MethodPut, "/api/agent-keys/claude", `{"value":"sk-ant-oat01-fake"}`); rec.Code != http.StatusOK {
		t.Fatalf("setting the claude token: %d %s", rec.Code, rec.Body)
	}
	settings = decode[ui.Settings](t, do(t, srv, http.MethodGet, "/api/settings", ""))
	if !settings.ClaudeOAuthTokenSet || settings.GeminiAPIKeySet || settings.OpenAIAPIKeySet {
		t.Fatalf("settings = %+v, want only the claude token set", settings)
	}
}
