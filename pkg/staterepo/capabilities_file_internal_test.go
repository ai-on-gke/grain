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

package staterepo

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// designCapabilities is docs/design/config-repo-settings.md §3.2's
// example, trimmed to what this build's vocabulary knows.
const designCapabilities = `
defaults:
  task:
    - corp-research
  ci: []
  repos:
    ai-on-gke/grain:
      task:
        - self-debug
        - https://capabilities.internal/gcp#gcp-key:
            with:
              project: grain-dev
      ci:
        - gcp-key
servers:
  - id: https://capabilities.internal/gcp
    name: GCP Credentials
    url: https://capabilities.internal/gcp/mcp
    enabled: true
    secrets:
      service_account: $secret:gcp-keys/grain-deployer
    methods:
      - name: gcp-key
        description: Mint a short-lived GCP access token for a project.
        args:
          project:
            allowed: ["grain-dev", "grain-staging", "grain-prod"]
        restrictions:
          repos: ["ai-on-gke/*", "bwsalmon/*"]
          templates: ["*"]
          kinds: ["task", "ci", "session"]
          requires_person_when:
            project: ["grain-prod"]
in_process:
  corp-research:
    enabled: true
    restrictions:
      repos: ["*"]
      kinds: ["task", "session", "director", "review"]
  self-debug:
    restrictions:
      repos: ["ai-on-gke/grain", "bwsalmon/grain"]
      kinds: ["task", "session", "director"]
  gcp-key:
    secret: $secret:gcp-keys/default
    restrictions:
      repos: ["ai-on-gke/grain"]
      kinds: ["ci"]
      ci_only: true
  k8s-kubeconfig:
    enabled: false
    restrictions:
      repos: []
      kinds: ["ci"]
      ci_only: true
      requires_person: true
api_scopes:
  presets:
    ops/triage:
      description: Read tasks and CI.
      scopes: ["tasks:read", "ci:read"]
      restrictions:
        kinds: ["task", "session"]
        requires_person: true
  raw_scopes:
    "*:write":
      kinds: ["task", "session", "director"]
      requires_person: true
`

func writeCapabilities(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, SettingsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SettingsDir, CapabilitiesFile), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDesignCapabilitiesFileValidates(t *testing.T) {
	cat, err := ValidateCapabilitiesFile([]byte(designCapabilities))
	if err != nil {
		t.Fatalf("ValidateCapabilitiesFile: %v", err)
	}
	if got := cat.Defaults.Repos["ai-on-gke/grain"].Task; len(got) != 2 || !strings.HasPrefix(got[1], "https://capabilities.internal/gcp#gcp-key=") {
		t.Fatalf("repo task defaults = %q", got)
	}
}

func TestCapabilitiesFileRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"unknown key":         {"in_process:\n  self-debug:\n    restriction: {}\n", "restriction"},
		"bad kind":            {"in_process:\n  x:\n    restrictions:\n      kinds: [cron]\n", `kind "cron"`},
		"secret by value":     {"servers:\n  - id: https://x.example/s\n    url: https://x.example/s\n    secrets:\n      token: hunter2\n", "never a value"},
		"inconsistent ci":     {"in_process:\n  x:\n    restrictions:\n      ci_only: true\n      kinds: [task]\n", "ci_only"},
		"default refused":     {"defaults:\n  task: [gcp-key]\nin_process:\n  gcp-key:\n    restrictions:\n      ci_only: true\n", "defaults.task"},
		"duplicate server":    {"servers:\n  - id: https://x.example/s\n    url: https://x.example/s\n  - id: https://x.example/s\n    url: https://x.example/s\n", "listed twice"},
		"bad scope in preset": {"api_scopes:\n  presets:\n    mine:\n      scopes: [nope:never]\n", "api_scopes.presets"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateCapabilitiesFile([]byte(tc.text))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateCapabilitiesFile = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// Importing a dump that carries settings/capabilities.yml puts the file's
// values in the columns it projects onto, and exporting the result
// writes the file back with the same judgement.
func TestCapabilitiesFileImportsAndExports(t *testing.T) {
	ctx := context.Background()
	db := openSchema(t)
	store := model.New(db)
	if err := store.PutConfig(ctx, model.Config{DefaultCapabilities: []string{"memory"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: model.RepoRef{Owner: "other", Name: "repo"}, DefaultCapabilities: []string{"memory"}}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := ExportConfig(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, SettingsDir, CapabilitiesFile)); !os.IsNotExist(err) {
		t.Fatalf("a deployment with no catalogue exported %s (stat: %v)", CapabilitiesPath, err)
	}
	writeCapabilities(t, dir, designCapabilities)

	fresh := openSchema(t)
	if err := Import(ctx, fresh, dir); err != nil {
		t.Fatalf("Import: %v", err)
	}
	got := model.New(fresh)
	cfg, err := got.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if !slices.Equal(cfg.DefaultCapabilities, []string{"corp-research"}) {
		t.Errorf("default_capabilities = %q, want the file's", cfg.DefaultCapabilities)
	}
	srv, ok := cfg.MCPServers["https://capabilities.internal/gcp"]
	if !ok || srv.ServerURL != "https://capabilities.internal/gcp/mcp" || srv.Config["service_account"] != "$secret:gcp-keys/grain-deployer" {
		t.Errorf("mcp_servers = %#v", cfg.MCPServers)
	}
	cat := cfg.CapabilityPolicy.Catalogue
	if cat == nil || cat.InProcess["self-debug"].Restrictions == nil {
		t.Fatalf("catalogue not stored: %#v", cfg.CapabilityPolicy)
	}
	rc, err := got.GetRepoConfig(ctx, model.RepoRef{Owner: "ai-on-gke", Name: "grain"})
	if err != nil || rc == nil || len(rc.DefaultCapabilities) != 2 || !slices.Equal(rc.CICapabilities, []string{"gcp-key"}) {
		t.Errorf("ai-on-gke/grain repo_config = %#v (%v)", rc, err)
	}
	other, err := got.GetRepoConfig(ctx, model.RepoRef{Owner: "other", Name: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if other != nil && len(other.DefaultCapabilities) != 0 {
		t.Errorf("a repository the file does not list kept its defaults: %q", other.DefaultCapabilities)
	}
	sets, err := got.ListAPIScopeSets(ctx)
	if err != nil || len(sets) != 1 || sets[0].Name != "ops/triage" || !sets[0].RequiresPerson {
		t.Errorf("api_scope_set = %#v (%v)", sets, err)
	}

	out := t.TempDir()
	if err := ExportConfig(ctx, fresh, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, SettingsDir, CapabilitiesFile))
	if err != nil {
		t.Fatalf("export wrote no %s: %v", CapabilitiesPath, err)
	}
	again, err := ValidateCapabilitiesFile(data)
	if err != nil {
		t.Fatalf("exported file does not validate: %v\n%s", err, data)
	}
	for _, gc := range []model.GrantContext{
		{Repo: &model.RepoRef{Owner: "x", Name: "y"}, Kind: "task", Grants: []string{"self-debug"}},
		{Kind: "task", Grants: []string{"gcp-key"}},
		{Kind: "ci", Grants: []string{"k8s-kubeconfig"}},
	} {
		if _, err := again.ValidateGrants(gc); err == nil {
			t.Errorf("exported catalogue admits %v", gc)
		}
	}
	// And a second import of the export is the same database.
	third := openSchema(t)
	if err := Import(ctx, third, out); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	cfg3, _ := model.New(third).GetConfig(ctx)
	if !slices.Equal(cfg3.DefaultCapabilities, cfg.DefaultCapabilities) || len(cfg3.MCPServers) != len(cfg.MCPServers) {
		t.Errorf("re-import differs: %#v vs %#v", cfg3.DefaultCapabilities, cfg.DefaultCapabilities)
	}
}

// Check refuses a template whose grants the catalogue does not admit.
func TestCheckCapabilitiesJudgesTemplateGrants(t *testing.T) {
	dir := t.TempDir()
	writeCapabilities(t, dir, designCapabilities)
	if err := os.WriteFile(filepath.Join(dir, SettingsDir, "templates.yml"), []byte(`templates:
  - id: template-1
    name: nightly
    target: other/repo
    grants: [self-debug]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := checkCapabilities(dir)
	if err == nil || !strings.Contains(err.Error(), "template template-1 grants self-debug") {
		t.Fatalf("checkCapabilities = %v", err)
	}
}
