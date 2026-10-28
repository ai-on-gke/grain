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

// Settings could say an agent credential was "set" -- the secret exists
// and resolves -- and be describing a key the vendor stopped accepting
// weeks ago, with the first thing to notice being a dispatched run
// failing to authenticate. These cover the action that closes that gap
// for the three agent frameworks, the same way capability_check_test.go
// covers it for the capabilities.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeAgentKeyChecker stands in for pkg/cli/daemon.go's own adapter
// over the secrets store and pkg/agent/credcheck: it answers whatever the
// test set and records what it was asked, with no vendor and no
// credential anywhere near it.
type fakeAgentKeyChecker struct {
	result ui.AgentKeyCheckResult
	err    error
	asked  []string
}

func (f *fakeAgentKeyChecker) CheckAgentKey(ctx context.Context, framework string) (ui.AgentKeyCheckResult, error) {
	f.asked = append(f.asked, framework)
	return f.result, f.err
}

func testServerWithAgentKeyChecker(t *testing.T, checker ui.AgentKeyChecker) *ui.Server {
	t.Helper()
	_, store, _ := testClient(t)
	cfg := ui.Config{
		Actor:          ui.DefaultActor("alice"),
		Capabilities:   ui.OfferedCapabilities(),
		Secrets:        secrets.New(t.TempDir()),
		AgentKeyChecks: checker,
	}
	if err := store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	return ui.NewServer(cfg, store)
}

func TestCheckAgentKeyReportsWhatTheVendorAnswered(t *testing.T) {
	t.Parallel()
	checker := &fakeAgentKeyChecker{result: ui.AgentKeyCheckResult{
		Secret: secrets.ClaudeOAuthTokenSecret,
		Detail: "Anthropic accepted the token held in \"claude-oauth-token\" and listed 6 model(s): claude-opus-4, ...",
	}}
	srv := testServerWithAgentKeyChecker(t, checker)

	rec := do(t, srv, http.MethodPost, "/api/agent-keys/claude/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	check := decode[ui.AgentKeyCheck](t, rec)
	if !check.OK {
		t.Errorf("OK = false for a credential the vendor accepted: %+v", check)
	}
	if check.Framework != "claude" {
		t.Errorf("Framework = %q, want claude", check.Framework)
	}
	if check.Secret != secrets.ClaudeOAuthTokenSecret {
		t.Errorf("Secret = %q, want the secret the credential came from", check.Secret)
	}
	if check.Detail != checker.result.Detail {
		t.Errorf("Detail = %q, want the checker's own sentence", check.Detail)
	}
	// A point-in-time answer says when it was true: read an hour later,
	// nothing about this should look current.
	if check.CheckedAt.IsZero() {
		t.Error("CheckedAt is zero -- an answer with no moment attached")
	}
	if len(checker.asked) != 1 || checker.asked[0] != "claude" {
		t.Errorf("checker was asked %v, want exactly [claude]", checker.asked)
	}
}

// The state this action exists to make visible: set, checked, refused.
// It is an answer about this deployment, not a failure of the API, so it
// is a 200 with ok=false carrying the sentence that names the secret to
// replace -- not an error a pane would draw as grain being broken.
func TestCheckAgentKeyReportsARefusedCredentialAsAnAnswer(t *testing.T) {
	t.Parallel()
	checker := &fakeAgentKeyChecker{
		result: ui.AgentKeyCheckResult{Secret: secrets.OpenAIAPIKeySecret},
		err: errors.New("OpenAI refused the credential held in \"openai-api-key\" " +
			"(HTTP 401: Incorrect API key provided)"),
	}
	srv := testServerWithAgentKeyChecker(t, checker)

	rec := do(t, srv, http.MethodPost, "/api/agent-keys/codex/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a refusal is an answer: %s", rec.Code, rec.Body)
	}
	check := decode[ui.AgentKeyCheck](t, rec)
	if check.OK {
		t.Errorf("OK = true for a refused credential: %+v", check)
	}
	if check.Detail != checker.err.Error() {
		t.Errorf("Detail = %q, want the refusal's own sentence", check.Detail)
	}
	// Reported alongside the refusal, since the remedy is replacing what
	// is in that secret.
	if check.Secret != secrets.OpenAIAPIKeySecret {
		t.Errorf("Secret = %q, want it named on a refusal too", check.Secret)
	}
}

// The legacy spelling reaches the credential agy really runs on, exactly
// as PUT/DELETE on this same family already do.
func TestCheckAgentKeyNormalizesTheLegacyGeminiSpelling(t *testing.T) {
	t.Parallel()
	checker := &fakeAgentKeyChecker{result: ui.AgentKeyCheckResult{Secret: secrets.GeminiAPIKeySecret}}
	srv := testServerWithAgentKeyChecker(t, checker)

	rec := do(t, srv, http.MethodPost, "/api/agent-keys/gemini/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if check := decode[ui.AgentKeyCheck](t, rec); check.Framework != "antigravity" {
		t.Fatalf("Framework = %q, want antigravity", check.Framework)
	}
	if len(checker.asked) != 1 || checker.asked[0] != "antigravity" {
		t.Fatalf("checker was asked %v, want exactly [antigravity]", checker.asked)
	}
}

// A framework no build of grain has is a mistake about grain rather than
// an answer about a credential, and nothing is asked of the vendor over
// it.
func TestCheckAgentKeyRejectsAFrameworkGrainDoesNotHave(t *testing.T) {
	t.Parallel()
	checker := &fakeAgentKeyChecker{}
	srv := testServerWithAgentKeyChecker(t, checker)

	rec := do(t, srv, http.MethodPost, "/api/agent-keys/gpt/check", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if len(checker.asked) != 0 {
		t.Fatalf("checker was asked %v, want nothing", checker.asked)
	}
}

// A UI with no daemon behind it holding the credentials says so, rather
// than turning a feature it was never wired for into a 500.
func TestCheckAgentKeyUnavailableWithoutAChecker(t *testing.T) {
	t.Parallel()
	srv, _ := testServerWithSecrets(t)

	rec := do(t, srv, http.MethodPost, "/api/agent-keys/claude/check", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	// And the pane is told, so it offers no button that could only 404.
	settings := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/settings", ""))
	if settings["agentKeyChecksEnabled"] != false {
		t.Fatalf("agentKeyChecksEnabled = %v, want false", settings["agentKeyChecksEnabled"])
	}
}

func TestSettingsAnnouncesAgentKeyChecksWhenWired(t *testing.T) {
	t.Parallel()
	srv := testServerWithAgentKeyChecker(t, &fakeAgentKeyChecker{})

	settings := decode[map[string]any](t, do(t, srv, http.MethodGet, "/api/settings", ""))
	if settings["agentKeyChecksEnabled"] != true {
		t.Fatalf("agentKeyChecksEnabled = %v, want true", settings["agentKeyChecksEnabled"])
	}
}
