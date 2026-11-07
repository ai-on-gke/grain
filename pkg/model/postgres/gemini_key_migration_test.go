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

package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

const geminiMinterSecret = "$secret:gcp-key-minter"

// geminiTestSecrets stands in for the secret store: a configured admin's
// credential must be a JSON key to parse.
var geminiTestSecrets = map[string]string{
	geminiMinterSecret:      `{"type":"service_account","client_email":"minter@acme-ai.iam.gserviceaccount.com"}`,
	"$secret:deploy-minter": `{"type":"service_account","client_email":"deploy@acme-ai.iam.gserviceaccount.com"}`,
}

// parseGCPEntry parses entry as the gcp server would once its secrets
// were resolved.
func parseGCPEntry(entry protocol.ServerConfig) (*gcpsets.Config, error) {
	config := map[string]any{}
	for k, v := range entry.Config {
		config[k] = v
	}
	if ref, ok := config["admin_credential"].(string); ok && geminiTestSecrets[ref] != "" {
		config["admin_credential"] = geminiTestSecrets[ref]
	}
	return gcpsets.ParseWithEnv(config, func(name string) string { return geminiTestSecrets[entry.Env[name]] })
}

// geminiSeedSet is a permission set as migration 97 seeded one.
func geminiSeedSet(admin string) map[string]any {
	set := map[string]any{
		"name":         "sandbox-agent",
		"project":      "acme-pool",
		"accounts":     []any{"sandbox-agent@acme-pool.iam.gserviceaccount.com"},
		"max_lease":    "24h0m0s",
		"adopt_legacy": true,
	}
	if admin != "" {
		set["admin"] = admin
	}
	return set
}

// geminiSeedCases are the shapes a gcp server entry can be in when
// migration 103 seeds gemini_key into it, and the admin it must mint with.
var geminiSeedCases = []struct {
	name      string
	entry     *protocol.ServerConfig
	wantAdmin string // gemini_key's admin as parsed; the top-level one is "default"
	// setAdmin is the admin the existing permission set must still
	// resolve to, and its credential; empty when there is no set.
	setAdmin, setCredential string
	wantAdmins              int // admins afterwards, the top-level one counted
}{
	{
		name:      "no gcp entry",
		entry:     nil,
		wantAdmin: gcpsets.DefaultAdmin, wantAdmins: 1,
	},
	{
		name: "top-level admin already the minter",
		entry: &protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp",
			Config: map[string]any{
				"admin_credential_source": "configured",
				"admin_credential":        geminiMinterSecret,
				"permission_sets":         []any{geminiSeedSet("")},
			}},
		wantAdmin: gcpsets.DefaultAdmin, setAdmin: gcpsets.DefaultAdmin, setCredential: geminiMinterSecret, wantAdmins: 1,
	},
	{
		name: "an admin bound to the minter",
		entry: &protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp",
			Env: map[string]string{"GRAIN_GCP_MINTER_OPS": geminiMinterSecret},
			Config: map[string]any{
				"admins": []any{map[string]any{"name": "ops", "credential_source": "configured",
					"credential_env": "GRAIN_GCP_MINTER_OPS"}},
				"permission_sets": []any{geminiSeedSet("ops")},
			}},
		wantAdmin: "ops", setAdmin: "ops", setCredential: geminiMinterSecret, wantAdmins: 1,
	},
	{
		name: "an ambient top-level admin",
		entry: &protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp",
			Config: map[string]any{
				"permission_sets": []any{geminiSeedSet("")},
			}},
		wantAdmin: "minter", setAdmin: gcpsets.DefaultAdmin, setCredential: "", wantAdmins: 2,
	},
	{
		name: "another configured top-level admin",
		entry: &protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp",
			Config: map[string]any{
				"admin_credential_source": "configured",
				"admin_credential":        "$secret:deploy-minter",
				"permission_sets":         []any{geminiSeedSet("")},
			}},
		wantAdmin: "minter", setAdmin: gcpsets.DefaultAdmin, setCredential: "$secret:deploy-minter", wantAdmins: 2,
	},
	{
		// The set names no admin and resolved to the sole one; with a
		// second admin beside it, it has to name "ops" or it names none.
		name: "a sole named admin bound elsewhere",
		entry: &protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp",
			Env: map[string]string{"GRAIN_GCP_MINTER_OPS": "$secret:deploy-minter"},
			Config: map[string]any{
				"admins": []any{map[string]any{"name": "ops", "credential_source": "configured",
					"credential_env": "GRAIN_GCP_MINTER_OPS"}},
				"permission_sets": []any{geminiSeedSet("")},
			}},
		wantAdmin: "minter", setAdmin: "ops", setCredential: "$secret:deploy-minter", wantAdmins: 2,
	},
}

// Migration 103 seeds gemini_key: {project} into the gcp server entry
// wherever gcp_project is set, minting with the gcp-key-minter secret
// whatever shape the entry's admins were in, keeps every existing set on
// the admin it had, and changes nothing when it runs again.
func TestMigration103SeedsGeminiKey(t *testing.T) {
	t.Parallel()
	for _, tc := range geminiSeedCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, db, ctx := openStore(t)
			if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
				t.Fatal(err)
			}
			raw := ""
			if tc.entry != nil {
				b, err := json.Marshal(map[string]protocol.ServerConfig{model.GCPCapabilityServerID: *tc.entry})
				if err != nil {
					t.Fatal(err)
				}
				raw = string(b)
			}
			if _, err := db.ExecContext(ctx, "UPDATE `grain_config` SET `gcp_project` = ?, `mcp_servers` = ? WHERE `id` = 1",
				"acme-ai", raw); err != nil {
				t.Fatal(err)
			}
			migrateFrom102(t, ctx, db)

			entry := storedGCPEntry(t, ctx, db)
			c, err := parseGCPEntry(entry)
			if err != nil {
				t.Fatalf("the seeded entry does not parse: %v\n%+v", err, entry.Config)
			}
			if c.GeminiKey == nil || c.GeminiKey.Project != "acme-ai" || c.GeminiKey.Admin != tc.wantAdmin {
				t.Fatalf("gemini_key = %+v, want project acme-ai minting as admin %q", c.GeminiKey, tc.wantAdmin)
			}
			if a := c.GeminiAdmin(); a == nil || a.Credential != geminiTestSecrets[geminiMinterSecret] || a.CredentialSource != gcpsets.SourceConfigured {
				t.Errorf("gemini-key mints as %+v, want the configured %s", a, geminiMinterSecret)
			}
			if len(c.Admins) != tc.wantAdmins {
				t.Errorf("admins = %+v, want %d", c.Admins, tc.wantAdmins)
			}
			if tc.setAdmin != "" {
				s := c.Set("sandbox-agent")
				if s == nil {
					t.Fatal("the existing permission set was lost")
				}
				a := c.Admin(s.Admin)
				if a == nil || a.Name != tc.setAdmin || a.Credential != geminiTestSecrets[tc.setCredential] {
					t.Errorf("sandbox-agent mints as %+v, want admin %q with %q", a, tc.setAdmin, tc.setCredential)
				}
			}
			if !strings.Contains(entry.Notice, "gcp#gemini-key") {
				t.Errorf("notice = %q, want one naming gcp#gemini-key", entry.Notice)
			}

			before := storedMCPServers(t, ctx, db)
			migrateFrom102(t, ctx, db)
			if after := storedMCPServers(t, ctx, db); after != before {
				t.Errorf("a second run changed mcp_servers:\n%s\n->\n%s", before, after)
			}
		})
	}
}

// A deployment with no GCP project never registered the in-process
// gemini-key, so migration 103 seeds nothing for it -- and still rewrites
// its grants, policy rules included.
func TestMigration103WithoutAProjectOnlyRewritesGrants(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	cfg := model.DefaultConfig()
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{{Capability: "gemini-key", Repos: []string{"acme/*"}, Deny: true}},
		Catalogue: &model.CapabilityCatalogue{Defaults: model.CatalogueDefaults{
			Task: []string{"release-gate-read", "gemini-key"},
		}},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `task_grant` (`task_id`, `capability`, `via`) VALUES ('t1', 'gemini-key', 'label')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_config` SET `gcp_project` = '', `mcp_servers` = '' WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	migrateFrom102(t, ctx, db)
	if got := storedMCPServers(t, ctx, db); got != "" {
		t.Errorf("mcp_servers = %s, want nothing seeded", got)
	}
	if want := []string{model.GCPGeminiKeyGrant}; !slices.Equal(grantRows(t, ctx, db, "task_grant", "task_id")["t1"], want) {
		t.Errorf("task grants = %v, want %v", grantRows(t, ctx, db, "task_grant", "task_id")["t1"], want)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := got.CapabilityPolicy.Rules; len(r) != 1 || r[0].Capability != model.GCPGeminiKeyRef || !r[0].Deny {
		t.Errorf("rules = %+v, want gemini-key's deny on %s", r, model.GCPGeminiKeyRef)
	}
	if want := []string{"release-gate-read", model.GCPGeminiKeyGrant}; !slices.Equal(got.CapabilityPolicy.Catalogue.Defaults.Task, want) {
		t.Errorf("catalogue task defaults = %v, want %v", got.CapabilityPolicy.Catalogue.Defaults.Task, want)
	}
}

// A gcp entry that already configures gemini_key is left as it is.
func TestMigration103KeepsAConfiguredGeminiKey(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]protocol.ServerConfig{model.GCPCapabilityServerID: {
		Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp",
		Config: map[string]any{
			"admin_credential_source": "ambient",
			"gemini_key":              map[string]any{"project": "acme-other"},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_config` SET `gcp_project` = 'acme-ai', `mcp_servers` = ? WHERE `id` = 1", string(b)); err != nil {
		t.Fatal(err)
	}
	migrateFrom102(t, ctx, db)
	if got := storedMCPServers(t, ctx, db); got != string(b) {
		t.Errorf("mcp_servers = %s, want %s untouched", got, b)
	}
}

// A state-repo export written before schema 103 is migrated on import:
// gemini_key is seeded from its gcp_project and its gemini-key grants
// arrive as gcp#gemini-key={}.
func TestStateRepoImportRetiresGeminiKey(t *testing.T) {
	t.Parallel()
	src, srcDB, ctx := openStore(t)
	cfg := model.DefaultConfig()
	cfg.GCPProject = "acme-ai"
	cfg.DefaultCapabilities = []string{"gemini-key", "release-gate-read"}
	if err := src.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := src.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets,
		DefaultCapabilities: []string{"gemini-key"}, CICapabilities: []string{"gemini-key"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := srcDB.ExecContext(ctx, "INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('t', 'gemini-key', 'label')"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 102); err != nil {
		t.Fatal(err)
	}

	dst, dstDB, _ := openStore(t)
	if err := staterepo.Import(ctx, dstDB, dir); err != nil {
		t.Fatalf("importing a schema-102 export: %v", err)
	}
	got, err := dst.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{model.GCPGeminiKeyGrant, "release-gate-read"}; !slices.Equal(got.DefaultCapabilities, want) {
		t.Errorf("default capabilities = %v, want %v", got.DefaultCapabilities, want)
	}
	repo, err := dst.GetRepoConfig(ctx, widgets)
	if err != nil || repo == nil {
		t.Fatalf("GetRepoConfig: %v, %+v", err, repo)
	}
	if want := []string{model.GCPGeminiKeyGrant}; !slices.Equal(repo.DefaultCapabilities, want) || !slices.Equal(repo.CICapabilities, want) {
		t.Errorf("repo capabilities = %v / %v, want %v", repo.DefaultCapabilities, repo.CICapabilities, want)
	}
	if want := []string{model.GCPGeminiKeyGrant}; !slices.Equal(grantRows(t, ctx, dstDB, "template_grant", "template_id")["t"], want) {
		t.Errorf("template grants = %v, want %v", grantRows(t, ctx, dstDB, "template_grant", "template_id")["t"], want)
	}
	entry := storedGCPEntry(t, ctx, dstDB)
	if g, _ := entry.Config["gemini_key"].(map[string]any); g == nil || g["project"] != "acme-ai" {
		t.Errorf("imported gcp entry = %+v, want gemini_key seeded for acme-ai", entry.Config)
	}
}

func migrateFrom102(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rewindSchemaTo(t, ctx, db, 102)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-102 store: %v", err)
	}
}

func storedMCPServers(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT `mcp_servers` FROM `grain_config` WHERE `id` = 1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func storedGCPEntry(t *testing.T, ctx context.Context, db *sql.DB) protocol.ServerConfig {
	t.Helper()
	servers := map[string]protocol.ServerConfig{}
	if err := json.Unmarshal([]byte(storedMCPServers(t, ctx, db)), &servers); err != nil {
		t.Fatal(err)
	}
	entry, ok := servers[model.GCPCapabilityServerID]
	if !ok {
		t.Fatalf("no %s entry in %v", model.GCPCapabilityServerID, servers)
	}
	return entry
}
