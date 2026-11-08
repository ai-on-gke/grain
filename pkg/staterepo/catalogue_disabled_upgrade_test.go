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

package staterepo_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// Regression for 2026-11-07 walk O1: a schema-108 dump whose catalogue disables
// in_process.read-issues while read-issues still sits in defaults.task,
// defaults.repos[acme/widgets].ci, template-1 and sched-1 passes
// staterepo.CheckMigration (which backs grain state prepare-upgrade), imports
// cleanly via ImportMigrated, and exports a state repository that passes
// staterepo.Check at the current schema.
func TestCheckMigrationAccepts108DumpWithDisabledCatalogueGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	off := false
	on := true

	srcStore, srcDB := openDB(t)
	cfg := model.DefaultConfig()
	cfg.DefaultCapabilities = []string{"read-issues", "release-gate-read"}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: "read-issues", Repos: []string{"acme/denied"}, Deny: true},
		},
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"release-gate-read"},
			},
			InProcess: map[string]model.CatalogueInProcess{
				"read-issues": {Enabled: &off},
				"self-debug":  {Enabled: &on},
			},
		},
	}
	if err := srcStore.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := srcStore.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"release-gate-read"},
		CICapabilities:      []string{"read-issues", "release-gate-read"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := srcStore.PutTemplate(ctx, model.Template{
		ID: "template-1", Name: "walk-tmpl", Title: "Walk template", Body: "seed", Target: &widgets, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srcDB.ExecContext(ctx,
		"INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('template-1', 'read-issues', 'label'), ('template-1', 'release-gate-read', 'label')"); err != nil {
		t.Fatal(err)
	}
	if err := srcStore.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "Walk schedule", Body: "seed", Target: widgets,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily},
		Overlap:    model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srcDB.ExecContext(ctx,
		"INSERT INTO `schedule_grant` (`schedule_id`, `capability`, `via`) VALUES ('sched-1', 'read-issues', 'label'), ('sched-1', 'release-gate-read', 'label')"); err != nil {
		t.Fatal(err)
	}

	dump108 := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dump108); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dump108, 108); err != nil {
		t.Fatal(err)
	}

	_, rehearseDB := openDB(t)
	if _, err := staterepo.CheckMigration(ctx, rehearseDB, dump108, t.TempDir(), model.SchemaVersion); err != nil {
		t.Fatalf("CheckMigration from 108 failed: %v", err)
	}

	dstStore, dstDB := openDB(t)
	if err := staterepo.ImportMigrated(ctx, dstDB, dump108); err != nil {
		t.Fatalf("ImportMigrated from 108 failed: %v", err)
	}

	gotCfg, err := dstStore.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotCfg.DefaultCapabilities, []string{"release-gate-read"}) {
		t.Errorf("migrated default_capabilities = %v, want [release-gate-read]", gotCfg.DefaultCapabilities)
	}
	tmpl, err := dstStore.GetTemplate(ctx, "template-1")
	if err != nil || tmpl == nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if !strings.Contains(tmpl.Body, "grain: capability catalogue migration dropped grant") {
		t.Errorf("migrated template-1 body missing note: %q", tmpl.Body)
	}

	upgradedRepo := t.TempDir()
	if err := staterepo.Export(ctx, dstDB, upgradedRepo); err != nil {
		t.Fatalf("Export upgraded store: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(upgradedRepo, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}

	_, checkDB := openDB(t)
	if _, err := staterepo.Check(ctx, checkDB, upgradedRepo, model.SchemaVersion); err != nil {
		t.Fatalf("Check of upgraded state repository failed: %v", err)
	}

	// At the current schema, reintroducing a disabled grant in capabilities.yml
	// must still be refused by staterepo.Check.
	capPath := filepath.Join(upgradedRepo, staterepo.CapabilitiesPath)
	rawCap, err := os.ReadFile(capPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(rawCap), "  task: [release-gate-read]\n", "  task: [release-gate-read, https://grain.dev/capabilities/github#read-issues]\n", 1)
	if edited == string(rawCap) {
		t.Fatalf("expected task defaults block in %s:\n%s", capPath, rawCap)
	}
	if err := os.WriteFile(capPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	_, checkBadDB := openDB(t)
	if _, err := staterepo.Check(ctx, checkBadDB, upgradedRepo, model.SchemaVersion); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Check at current schema accepted reintroduced disabled default: %v", err)
	}
}

// CheckMigration must still refuse structural errors (such as a plaintext
// server secret) and non-disabled catalogue refusals (such as a template grant
// violating a repos restriction) on older-schema dumps.
func TestCheckMigrationStillRefusesPlaintextSecretAndRepoRestrictedTemplate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	t.Run("plaintext_secret", func(t *testing.T) {
		t.Parallel()
		_, srcDB := openDB(t)
		dir := t.TempDir()
		if err := staterepo.Export(ctx, srcDB, dir); err != nil {
			t.Fatal(err)
		}
		if err := staterepo.WriteSchemaVersion(dir, 108); err != nil {
			t.Fatal(err)
		}
		badYAML := `servers:
  - id: https://capabilities.example/ext
    command: grain
    args: ["capability", "config-map"]
    secrets:
      api_key: not-a-secret-ref
`
		if err := os.WriteFile(filepath.Join(dir, staterepo.CapabilitiesPath), []byte(badYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		_, checkDB := openDB(t)
		if _, err := staterepo.CheckMigration(ctx, checkDB, dir, t.TempDir(), model.SchemaVersion); err == nil || !strings.Contains(err.Error(), "$secret:") {
			t.Fatalf("CheckMigration accepted plaintext secret: %v", err)
		}
	})

	t.Run("repo_restricted_template", func(t *testing.T) {
		t.Parallel()
		srcStore, srcDB := openDB(t)
		cfg := model.DefaultConfig()
		cfg.CapabilityPolicy = model.CapabilityPolicy{
			Catalogue: &model.CapabilityCatalogue{
				InProcess: map[string]model.CatalogueInProcess{
					"read-issues": {Restrictions: &model.CapabilityRestrictions{
						Repos: model.Patterns{"acme/*"},
					}},
				},
			},
		}
		if err := srcStore.PutConfig(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		other := model.RepoRef{Owner: "other", Name: "repo"}
		if err := srcStore.PutTemplate(ctx, model.Template{
			ID: "tpl-other", Name: "tpl-other", Title: "Other", Body: "seed", Target: &other, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := srcDB.ExecContext(ctx,
			"INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('tpl-other', 'read-issues', 'label')"); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := staterepo.Export(ctx, srcDB, dir); err != nil {
			t.Fatal(err)
		}
		if err := staterepo.WriteSchemaVersion(dir, 108); err != nil {
			t.Fatal(err)
		}
		_, checkDB := openDB(t)
		if _, err := staterepo.CheckMigration(ctx, checkDB, dir, t.TempDir(), model.SchemaVersion); err == nil || !strings.Contains(err.Error(), "other/repo") {
			t.Fatalf("CheckMigration accepted repo-violating template grant: %v", err)
		}
	})
}
