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
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
)

// looseCredentialFiles are the single-value credential files grain keeps
// under <data-dir>/secrets beside the store rather than in it
// (design/design/secrets-key-protection.design.md §1.2): setup.sh's
// agent keys, which stay the -gemini-api-key-file fallback after
// `grain connections import`, the state and secrets repositories'
// tokens, the relay token's file fallback and the local Postgres
// password.
var looseCredentialFiles = []string{
	"gemini-api-key", "claude-oauth-token", "openai-api-key",
	"state-repo-token", "secrets-repo-token",
	filepath.Join("relay", "token"),
	"postgres-password",
}

// minKnownCredentialLen is the shortest file value redacted by name: one
// shorter is more likely a placeholder than a credential, and replacing
// it everywhere it occurs would eat ordinary output.
const minKnownCredentialLen = 8

// knownLooseCredentials is every credential under secretsDir that is
// neither in the secrets store nor a GitHub PAT (knownGitHubTokens): the
// files above, each Codex login's tokens (codex-auth.json and the
// per-backend codex-auth-<hash>.json, which every Codex run's CODEX_HOME
// links to, so a run can print them), and each GitHub App's webhook
// secret. Without them a run that printed one would have it kept in its
// transcript, its console and the journal; their shapes are not
// distinctive enough for pkg/redact to cut by pattern. A file that is
// missing or unreadable contributes nothing.
func knownLooseCredentials(secretsDir string) []grainlog.KnownValue {
	if secretsDir == "" {
		return nil
	}
	var known []grainlog.KnownValue
	add := func(label, value string) {
		if value = strings.TrimSpace(value); len(value) >= minKnownCredentialLen {
			known = append(known, grainlog.KnownValue{Label: label, Value: value})
		}
	}
	for _, name := range looseCredentialFiles {
		if data, err := os.ReadFile(filepath.Join(secretsDir, name)); err == nil {
			add("credential file "+filepath.ToSlash(name), string(data))
		}
	}
	codexFiles, _ := filepath.Glob(filepath.Join(secretsDir, "codex-auth*.json"))
	sort.Strings(codexFiles)
	for _, path := range codexFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var auth map[string]any
		if json.Unmarshal(data, &auth) != nil {
			continue
		}
		label := "codex login " + filepath.Base(path)
		if key, ok := auth["OPENAI_API_KEY"].(string); ok {
			add(label, key)
		}
		if tokens, ok := auth["tokens"].(map[string]any); ok {
			names := make([]string, 0, len(tokens))
			for name := range tokens {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				// account_id names the account; it is not a credential.
				if v, ok := tokens[name].(string); ok && name != "account_id" {
					add(label, v)
				}
			}
		}
	}
	apps, _ := filepath.Glob(filepath.Join(secretsDir, "github", "*.app.json"))
	sort.Strings(apps)
	for _, path := range apps {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var app struct {
			WebhookSecret string `json:"webhook_secret"`
		}
		if json.Unmarshal(data, &app) == nil {
			add("github app "+strings.TrimSuffix(filepath.Base(path), ".app.json")+" webhook secret", app.WebhookSecret)
		}
	}
	return known
}
