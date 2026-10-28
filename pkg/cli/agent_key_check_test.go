package cli

// The daemon's half of "test this agent credential": the adapter
// Settings' Agents tab reaches a real vendor through. Its own job is
// narrow and is the whole of what these cover -- resolve the credential
// a dispatch would resolve, from the same two places and in the same
// order, and carry back what the vendor said. The call itself belongs to
// pkg/agent/credcheck and is tested there; the stub below stands in for
// the vendor so nothing here touches a real API.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent/credcheck"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// stubGemini answers Google's model listing and records the key it was
// given, which is how these tests see *which* credential the adapter
// resolved.
func stubGemini(t *testing.T, gotKey *string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotKey = r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-3-pro"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAgentKeyCheckSaysWhenNoCredentialIsSet(t *testing.T) {
	t.Parallel()
	adapter := daemon.AgentKeyCheckAdapter{
		Live:  testLiveConfig(daemon.Config{}),
		Creds: secrets.New(t.TempDir()),
	}

	result, err := adapter.CheckAgentKey(context.Background(), "claude")
	if err == nil {
		t.Fatal("expected a check with no credential stored to say so")
	}
	if !strings.Contains(err.Error(), "no credential is set") {
		t.Errorf("error %q does not say the credential is unset", err)
	}
	// Named even on this path, since the remedy is the field that writes
	// that secret.
	if result.Secret != secrets.ClaudeOAuthTokenSecret {
		t.Errorf("Secret = %q, want %q", result.Secret, secrets.ClaudeOAuthTokenSecret)
	}
}

// A deployment seeded by file -- scripts/setup.sh's own
// -gemini-api-key-file -- is testable too: the adapter resolves exactly
// as agentCredential does for a dispatch, so what it checks is what the
// next run would authenticate with.
func TestAgentKeyCheckReadsTheKeyFileWhenNoSecretIsStored(t *testing.T) {
	t.Parallel()
	keyFile := filepath.Join(t.TempDir(), "gemini-key")
	if err := os.WriteFile(keyFile, []byte("AIza-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got string
	adapter := daemon.AgentKeyCheckAdapter{
		Live:    testLiveConfig(daemon.Config{GeminiAPIKeyFile: keyFile}),
		Creds:   secrets.New(t.TempDir()),
		Checker: credcheck.Checker{GeminiBaseURL: stubGemini(t, &got)},
	}

	result, err := adapter.CheckAgentKey(context.Background(), "antigravity")
	if err != nil {
		t.Fatalf("CheckAgentKey: %v", err)
	}
	if got != "AIza-from-file" {
		t.Errorf("vendor was given %q, want the trimmed contents of the key file", got)
	}
	if !strings.Contains(result.Detail, "gemini-3-pro") {
		t.Errorf("Detail = %q, want the vendor's listing in it", result.Detail)
	}
}

// And a key pasted into Settings wins over that file, exactly as it does
// on the dispatch path -- otherwise the button would keep testing a
// credential the next run would not use.
func TestAgentKeyCheckPrefersTheStoredSecretOverTheKeyFile(t *testing.T) {
	t.Parallel()
	keyFile := filepath.Join(t.TempDir(), "gemini-key")
	if err := os.WriteFile(keyFile, []byte("AIza-from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := secrets.New(t.TempDir())
	if err := store.Set(secrets.GeminiAPIKeySecret, secrets.AgentCredentialKey, []byte("AIza-from-ui")); err != nil {
		t.Fatal(err)
	}
	var got string
	adapter := daemon.AgentKeyCheckAdapter{
		Live:    testLiveConfig(daemon.Config{GeminiAPIKeyFile: keyFile}),
		Creds:   store,
		Checker: credcheck.Checker{GeminiBaseURL: stubGemini(t, &got)},
	}

	if _, err := adapter.CheckAgentKey(context.Background(), "gemini"); err != nil {
		t.Fatalf("CheckAgentKey: %v", err)
	}
	if got != "AIza-from-ui" {
		t.Errorf("vendor was given %q, want the key stored through the UI", got)
	}
}

func TestAgentKeyCheckAcceptsASharedCodexLogin(t *testing.T) {
	t.Parallel()
	authFile := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authFile, []byte(`{"tokens":{"refresh_token":"fake"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := daemon.AgentKeyCheckAdapter{
		Live:  testLiveConfig(daemon.Config{CodexAuthFile: authFile}),
		Creds: secrets.New(t.TempDir()),
	}

	result, err := adapter.CheckAgentKey(context.Background(), "codex")
	if err != nil {
		t.Fatalf("CheckAgentKey: %v", err)
	}
	if !strings.Contains(result.Detail, "shared ChatGPT login") {
		t.Errorf("Detail = %q, want shared login named", result.Detail)
	}
}

func TestAgentKeyCheckRefusesAFrameworkGrainDoesNotHave(t *testing.T) {
	t.Parallel()
	adapter := daemon.AgentKeyCheckAdapter{Live: testLiveConfig(daemon.Config{}), Creds: secrets.New(t.TempDir())}

	if _, err := adapter.CheckAgentKey(context.Background(), "gpt"); err == nil {
		t.Fatal("expected an unknown framework to be refused")
	}
}
