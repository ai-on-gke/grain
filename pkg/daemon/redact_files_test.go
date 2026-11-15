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

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets"
)

// A credential grain keeps in a file beside the store rather than in it
// -- a Codex login, a GitHub App's webhook secret, setup.sh's agent key
// files, the state repository's token, the Postgres password -- is cut
// out of a transcript and out of the journal and Logs pane by name, the
// same as one in the store. Before, only the store's values and GitHub
// PATs were, so a Codex run that printed its own auth.json kept its
// refresh token in its transcript (credential exposure audit,
// docs/security/audits/2026-11-10-credential-exposure.md).
func TestLooseCredentialFilesAreRedactedByName(t *testing.T) {
	dataDir := t.TempDir()
	secretsDir := SecretsDir(dataDir)
	files := map[string]string{
		"codex-auth.json":               `{"OPENAI_API_KEY":null,"tokens":{"refresh_token":"planted-codex-refresh-0001","access_token":"planted-codex-access-0002","account_id":"acct-1"}}`,
		"codex-auth-0123abcd.json":      `{"tokens":{"refresh_token":"planted-backend-refresh-0003"}}`,
		"github/ci-app.app.json":        `{"app_id":"1","installation_id":"2","private_key":"x","webhook_secret":"planted-webhook-0004"}`,
		"gemini-api-key":                "planted-gemini-file-0005\n",
		"state-repo-token":              "planted-state-repo-0006\n",
		"postgres-password":             "planted-postgres-0007",
		filepath.Join("relay", "token"): "planted-relay-0008",
		"secrets-repo-token":            "short",
	}
	for name, data := range files {
		path := filepath.Join(secretsDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secStore := secrets.New(secretsDir)
	if err := secStore.Set("stored", "value", []byte("planted-stored-0009")); err != nil {
		t.Fatal(err)
	}
	planted := []string{
		"planted-codex-refresh-0001", "planted-codex-access-0002", "planted-backend-refresh-0003",
		"planted-webhook-0004", "planted-gemini-file-0005", "planted-state-repo-0006",
		"planted-postgres-0007", "planted-relay-0008", "planted-stored-0009",
	}
	text := "a run printed: " + strings.Join(planted, " ") + " acct-1 short\n"

	transcript, err := transcriptObjectRedactor(Config{DataDir: dataDir}, secStore)()
	if err != nil {
		t.Fatal(err)
	}
	logs, err := logRedactor(secretsDir, secStore)()
	if err != nil {
		t.Fatal(err)
	}
	for what, r := range map[string]interface {
		Text(string) (string, int)
	}{"transcript": transcript, "log": logs, "namespace": buildNamespaceRedactorFor(dataDir, secStore)} {
		got, _ := r.Text(text)
		for _, v := range planted {
			if strings.Contains(got, v) {
				t.Errorf("the %s redactor left %q in %q", what, v, got)
			}
		}
		// A Codex account id is not a credential, and a value shorter
		// than minKnownCredentialLen is not redacted everywhere it occurs.
		if !strings.Contains(got, "acct-1 short") {
			t.Errorf("the %s redactor cut more than credentials: %q", what, got)
		}
	}
}
