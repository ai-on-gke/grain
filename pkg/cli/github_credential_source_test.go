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

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
)

func testLiveConfig(cfg daemon.Config) *daemon.LiveConfig {
	return daemon.NewLiveConfig(nil, nil, cfg, nil)
}

// writeLadder lays out a data directory's credential ladder: a default
// token every repo falls back to, plus whatever else is named, and
// returns the data directory.
func writeLadder(t *testing.T, files map[string]string) string {
	t.Helper()
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "secrets", "github")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"*":"bot"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dataDir
}

// githubCredentialSource is the one place the credential ladder and the
// capability package meet: a name in, the material a push through it
// would carry out, and which of the two file forms it came from -- which
// is what decides whether a refusal tells an operator to paste a token
// or to replace a file.
func TestGitHubCredentialSourceResolvesBothFileForms(t *testing.T) {
	t.Parallel()
	dataDir := writeLadder(t, map[string]string{
		"bot.token":         "tok\n",
		"release-bot.token": "ghp_live\n",
		// Deliberately not a usable App credential: what is asserted
		// here is that it is recognised as one, and the ladder's own
		// fail-soft-to-anonymous path is exactly the state the check
		// reports as "every push goes out unauthenticated".
		"app-bot.app.json": `{"app_id":"1","installation_id":"2","private_key":"not a key"}`,
	})
	ladder, err := daemon.GitHubCredentialLadder(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	source := daemon.GithubCredentialSource{Set: ladder}

	cred, ok := source.GitHubCredential("release-bot")
	if !ok || cred.Token != "ghp_live" || cred.App {
		t.Errorf("release-bot = %+v, %v, want the token file's own value", cred, ok)
	}
	cred, ok = source.GitHubCredential("app-bot")
	if !ok || !cred.App || cred.Token != "" {
		t.Errorf("app-bot = %+v, %v, want an App credential with nothing minted from it", cred, ok)
	}
	if _, ok := source.GitHubCredential("never-configured"); ok {
		t.Error("a credential with no file behind it was resolved anyway")
	}
	// A process with no ladder at all answers the same way rather than
	// panicking: it offers no named tokens to check in the first place.
	if _, ok := (daemon.GithubCredentialSource{}).GitHubCredential("release-bot"); ok {
		t.Error("a nil ladder resolved a credential")
	}
}
