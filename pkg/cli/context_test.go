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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useConfigHome points the CLI's grain config directory at a temporary
// $XDG_CONFIG_HOME of the test's own and returns that directory.
func useConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(contextEnvVar, "")
	return filepath.Join(dir, "grain")
}

// TestContextConfigXDGMode0600AndNoRawSecrets is
// external-cli.spec.md#XC-1: config.json lives under $XDG_CONFIG_HOME
// (else ~/.config), in a 0700 directory, mode 0600, in the spec's shape;
// a missing one is no contexts rather than an error; and a secret where
// a locator belongs is never written.
func TestContextConfigXDGMode0600AndNoRawSecrets(t *testing.T) {
	dir := useConfigHome(t)
	cfg, err := loadContexts()
	if err != nil || len(cfg.Contexts) != 0 {
		t.Fatalf("with no config.json, loadContexts() = %+v, %v; want none and no error", cfg, err)
	}
	if sel, err := selectContext(""); err != nil || sel.ctx != nil {
		t.Fatalf("with no config.json, selectContext() = %+v, %v", sel, err)
	}

	cfg.Current = "team-prod"
	cfg.Contexts["team-prod"] = &cliContext{Server: "https://grain.team.example.com", Auth: "external-token",
		Credential: "keyring:grain/team-prod", TokenID: "eat_1"}
	cfg.Contexts["personal"] = &cliContext{Server: "https://grain-abc123.run.app", Auth: "google-id-token",
		Audience: "https://grain-abc123.run.app"}
	if err := saveContexts(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %#o, want %#o", p, info.Mode().Perm(), want)
		}
	}
	raw, _ := os.ReadFile(path)
	var shape struct {
		Current  string                       `json:"current"`
		Contexts map[string]map[string]string `json:"contexts"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	if shape.Current != "team-prod" || shape.Contexts["team-prod"]["credential"] != "keyring:grain/team-prod" ||
		shape.Contexts["team-prod"]["tokenId"] != "eat_1" || shape.Contexts["personal"]["audience"] != "https://grain-abc123.run.app" {
		t.Errorf("config.json = %s", raw)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the atomic write left %d entries in %s, want config.json alone", len(entries), dir)
	}

	cfg.Contexts["leaky"] = &cliContext{Server: "https://x.example.com", Auth: "external-token",
		Credential: "gma_" + strings.Repeat("ab", 32)}
	if err := saveContexts(cfg); err == nil {
		t.Fatal("saveContexts wrote a raw token as a credential")
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "gma_") {
		t.Fatalf("config.json holds a raw token: %s", raw)
	}

	// No $XDG_CONFIG_HOME: ~/.config/grain.
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	if got, _ := contextConfigPath(); got != filepath.Join(home, ".config", "grain", "config.json") {
		t.Errorf("contextConfigPath() = %q, want under ~/.config", got)
	}
}
