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
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// TestConnectionsImportRecordsConnectionsAndIsIdempotent: a seed file's
// git connections land on the deployment, its token on disk, its model
// connection and key in settings and secrets, and importing it again
// changes nothing.
func TestConnectionsImportRecordsConnectionsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	token := write("bot.token", "ghp_seed\n")
	key := write("gemini.key", "AIza-seed\n")
	seed := write("seed.yaml", `git:
  - credential: bot
    token_file: `+token+`
  - credential: infra
    scope: Acme/Infra
models:
  - id: gemini
    framework: antigravity
    models: [gemini-3.1-pro]
    credential_file: `+key+`
default_model: gemini-3.1-pro
`)
	// Model connections live on the settings row a daemon seeds on its
	// first start, so they are refused until there is one.
	if err := runConnections(ctx, []string{"-data-dir", dataDir, "import", seed}, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "start grain daemon on it once") {
		t.Fatalf("import before the daemon's first start = %v, want a refusal", err)
	}
	// ...without leaving an empty SQLite store behind: the daemon on a
	// data dir like this one may well be running on Postgres.
	if daemon.HasSQLiteStore(dataDir) {
		t.Fatal("the refused import created store/grain.db")
	}
	if s, db, err := daemon.OpenStore("", "", dataDir); err != nil {
		t.Fatal(err)
	} else {
		if err := s.PutConfig(ctx, model.DefaultConfig()); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	for i := 0; i < 2; i++ {
		if err := runConnections(ctx, []string{"-data-dir", dataDir, "import", seed}, io.Discard); err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
	}

	store, db, err := daemon.OpenStore("", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	team, err := store.GetConfig(ctx)
	if err != nil || team == nil {
		t.Fatalf("GetConfig = %v, %v", team, err)
	}
	want := []model.TeamGitConnection{{Credential: "bot"}, {Credential: "infra", Scope: "acme/infra"}}
	if len(team.GitConnections) != len(want) {
		t.Fatalf("git connections = %+v, want %+v", team.GitConnections, want)
	}
	for i := range want {
		if team.GitConnections[i] != want[i] {
			t.Fatalf("git connections = %+v, want %+v", team.GitConnections, want)
		}
	}
	if team.DefaultModel != "gemini-3.1-pro" {
		t.Errorf("default model = %q", team.DefaultModel)
	}
	if b, err := os.ReadFile(filepath.Join(dataDir, "secrets", "github", "bot.token")); err != nil || strings.TrimSpace(string(b)) != "ghp_seed" {
		t.Errorf("bot.token = %q, %v", b, err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("GetConfig = %v, %v", cfg, err)
	}
	if len(cfg.AgentBackends) != 1 || cfg.AgentBackends[0].ID != "gemini" || cfg.AgentBackends[0].Framework != model.AgentFrameworkAntigravity {
		t.Fatalf("model connections = %+v", cfg.AgentBackends)
	}
	v, err := daemon.OpenSecrets(dataDir).Resolve(ctx, secrets.AgentBackendCredentialSecret("gemini"))
	if err != nil || v != "AIza-seed" {
		t.Errorf("gemini key = %q, %v", v, err)
	}
}

// TestConnectionsImportRefusesAMistakeBeforeWritingAnything: a file with
// one bad entry writes none of the good ones.
func TestConnectionsImportRefusesAMistakeBeforeWritingAnything(t *testing.T) {
	for name, body := range map[string]string{
		"bad scope":     "git:\n  - credential: bot\n    scope: a/b/c\n",
		"bad framework": "models:\n  - id: x\n    framework: nope\n",
		"unknown field": "git:\n  - credential: bot\n    pattern: '*'\n",
		"no id":         "models:\n  - framework: claude\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConnectionSeed([]byte(body)); err == nil {
				t.Fatalf("parseConnectionSeed(%q) accepted it", body)
			}
		})
	}
}

func TestConnectionsImportSkipsRelaySettingsWithNote(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	path := filepath.Join(t.TempDir(), "connections.yaml")
	body := `git:
  - credential: bot
relay:
  url: https://relay.example
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runConnections(ctx, []string{"-data-dir", dataDir, "import", path}, &out); err != nil {
		t.Fatalf("runConnections import with relay section: %v", err)
	}
	if !strings.Contains(out.String(), "skipping relay settings") {
		t.Errorf("output = %q; want note about skipping relay settings", out.String())
	}
}

func TestParseConnectionSeedMultipleDefaultModels(t *testing.T) {
	seed, err := parseConnectionSeed([]byte("default_model:\n  - gemini-3.1-pro\n  - claude-opus-5\n"))
	if err != nil {
		t.Fatalf("parseConnectionSeed sequence: %v", err)
	}
	if string(seed.DefaultModel) != "gemini-3.1-pro,claude-opus-5" {
		t.Fatalf("DefaultModel = %q, want gemini-3.1-pro,claude-opus-5", seed.DefaultModel)
	}

	seed2, err := parseConnectionSeed([]byte("default_models:\n  - claude-opus-5\n  - gemini-3.1-pro\n"))
	if err != nil {
		t.Fatalf("parseConnectionSeed default_models: %v", err)
	}
	if string(seed2.DefaultModel) != "claude-opus-5,gemini-3.1-pro" {
		t.Fatalf("DefaultModel = %q, want claude-opus-5,gemini-3.1-pro", seed2.DefaultModel)
	}
}

// seedTestConnections records seed (a `grain connections import` file's
// body) as dataDir's deployment connections, the way an install seeds
// them before its daemon starts.
func seedTestConnections(t *testing.T, dataDir, seed string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "connections.yaml")
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConnections(context.Background(), []string{"-data-dir", dataDir, "import", path}, io.Discard); err != nil {
		t.Fatalf("seeding connections: %v", err)
	}
}

// seedTestDaemonConnections is seedTestConnections for a seed with model
// connections, which live on the settings row: that row is first seeded
// from cfg, the way cfg's daemon would seed it on its first start.
func seedTestDaemonConnections(t *testing.T, cfg daemon.Config, seed string) {
	t.Helper()
	store, db, err := daemon.OpenStore("", "", cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if stored, err := store.GetConfig(context.Background()); err != nil {
		t.Fatal(err)
	} else if stored == nil {
		if err := store.PutConfig(context.Background(), cfg.ToModelConfig()); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	seedTestConnections(t, cfg.DataDir, seed)
}
