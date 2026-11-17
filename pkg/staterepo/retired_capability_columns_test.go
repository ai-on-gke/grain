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
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A bare retired name hand-added to grain/config's capability columns --
// the deployment's and a repository's default sets, and a repository's CI
// capabilities -- imports as what it became, as one in a settings file's
// grant list does, and the export after writes only canonical names. They
// were kept as written: granting nothing, hidden from the API, and
// written back out on every export (2026-11-14 retired-aliases
// walkthrough, F6).
func TestAnImportUpgradesRetiredNamesInCapabilityColumns(t *testing.T) {
	ctx := context.Background()
	old, oldDB := openDB(t)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := old.UpdateConfig(ctx, func(c *model.Config) error {
		c.DefaultCapabilities = []string{model.APIRef("memory")}
		return nil
	}); err != nil {
		t.Fatalf("putting the config: %v", err)
	}
	if err := old.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, SetupCommand: "make deps"}); err != nil {
		t.Fatalf("putting the repo: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, oldDB, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	// The export as v3.5.0 (schema 136) wrote it, with bare names added
	// by hand.
	if err := staterepo.WriteSchemaVersion(dir, 136); err != nil {
		t.Fatal(err)
	}
	editTable(t, dir, "grain_config", func(row map[string]any) {
		row["default_capabilities"] = row["default_capabilities"].(string) + ",self-debug,read-issues"
	})
	editTable(t, dir, "repo_config", func(row map[string]any) {
		row["default_capabilities"] = "github-sandbox"
		row["ci_capabilities"] = "grain-log,github-credential:bot"
	})
	store, db := openDB(t)
	if err := staterepo.Import(ctx, db, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("reading the config back: %v %v", cfg, err)
	}
	for _, want := range []string{model.APIRef("memory"), model.APIRef("self-debug"), model.GrainSourceGrant, model.GitHubReadIssuesGrant} {
		if !slices.Contains(cfg.DefaultCapabilities, want) {
			t.Errorf("DefaultCapabilities = %v, want %s among them", cfg.DefaultCapabilities, want)
		}
	}
	rc, err := store.GetRepoConfig(ctx, widgets)
	if err != nil || rc == nil {
		t.Fatalf("reading the repo back: %v %v", rc, err)
	}
	if !slices.Equal(rc.DefaultCapabilities, []string{model.GitHubSandboxRepoGrant}) {
		t.Errorf("repo DefaultCapabilities = %v, want [%s]", rc.DefaultCapabilities, model.GitHubSandboxRepoGrant)
	}
	if want := []string{model.GrainJournalGrant, model.RepoCredentialGrant("bot")}; !slices.Equal(rc.CICapabilities, want) {
		t.Errorf("repo CICapabilities = %v, want %v", rc.CICapabilities, want)
	}

	again := t.TempDir()
	if err := staterepo.Export(ctx, db, again); err != nil {
		t.Fatalf("re-exporting: %v", err)
	}
	for _, table := range []string{"grain_config", "repo_config"} {
		data, err := os.ReadFile(filepath.Join(again, filepath.FromSlash(staterepo.DumpPath(table))))
		if err != nil {
			t.Fatal(err)
		}
		for _, bare := range []string{"self-debug", "read-issues", "github-sandbox", "grain-log", "github-credential:bot"} {
			if strings.Contains(string(data), `"`+bare) || strings.Contains(string(data), ","+bare) {
				t.Errorf("the re-exported %s still names %s:\n%s", table, bare, data)
			}
		}
	}
}

// editTable rewrites every row of tables/<table>.json in a clone.
func editTable(t *testing.T, root, table string, edit func(row map[string]any)) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(staterepo.DumpPath(table)))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if len(rows) == 0 {
		t.Fatalf("%s holds no rows", path)
	}
	for _, row := range rows {
		edit(row)
	}
	out, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
