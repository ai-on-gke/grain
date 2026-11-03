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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Alice Operator",
		"GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=Alice Operator",
		"GIT_COMMITTER_EMAIL=alice@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mergeBranchNoFF(t *testing.T, dir, branchName, commitSubject string, mutate func(dir string)) string {
	t.Helper()
	runGitIn(t, dir, "checkout", "-b", branchName)
	mutate(dir)
	runGitIn(t, dir, "add", "-A")
	runGitIn(t, dir, "commit", "-m", commitSubject)
	leaf := runGitIn(t, dir, "rev-parse", "HEAD")
	runGitIn(t, dir, "checkout", staterepo.DefaultBranch)
	runGitIn(t, dir, "merge", "--no-ff", "-m", "Merge pull request from "+branchName, branchName)
	runGitIn(t, dir, "branch", "-D", branchName)
	return leaf
}

func exportTemplatesYML(t *testing.T, ctx context.Context, tpls ...model.Template) []byte {
	t.Helper()
	tmp := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(tmp, "scratch")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := model.New(db)
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tpl := range tpls {
		if err := s.PutTemplate(ctx, tpl); err != nil {
			t.Fatal(err)
		}
	}
	dumpDir := filepath.Join(tmp, "dump")
	if err := staterepo.ExportConfig(ctx, db, dumpDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dumpDir, "settings", "templates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func exportTriggerSchedulesYML(t *testing.T, ctx context.Context, sched model.Schedule) []byte {
	t.Helper()
	tmp := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(tmp, "scratch")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := model.New(db)
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	dumpDir := filepath.Join(tmp, "dump")
	if err := staterepo.ExportConfig(ctx, db, dumpDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dumpDir, "settings", "schedules.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMigrateRepoAppliesPendingConfigCommitsAcrossSchemaChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()

	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID:    "tpl-base",
		Name:  "base-template",
		Title: "Base Title",
		Body:  "Base prompt",
	}); err != nil {
		t.Fatal(err)
	}

	repoDir := filepath.Join(dataDir, "state-repo")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir:         repoDir,
		DataDir:     dataDir,
		AuthorName:  "grain",
		AuthorEmail: "grain@localhost",
		NoWorkflow:  true,
		Build:       "test-build",
	})
	if err != nil {
		t.Fatal(err)
	}

	oldSchema := model.SchemaVersion - 7
	if oldSchema < model.V2BaselineSchemaVersion {
		oldSchema = model.V2BaselineSchemaVersion
	}

	// Seed the repository and record loaded-head at the old schema, matching
	// the last healthy sync before the old daemon stopped applying commits.
	if err := staterepo.ExportConfig(ctx, db, repoDir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(repoDir, oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "Initial export at prior schema"); err != nil {
		t.Fatal(err)
	}
	initialHead, err := repo.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetLoadedHead(ctx, initialHead); err != nil {
		t.Fatal(err)
	}

	// Database-only change made before the upgrade that has never been exported:
	// a three-way merge must keep it alongside the unapplied repo commits.
	if err := store.PutTemplate(ctx, model.Template{
		ID:    "tpl-db-unexported",
		Name:  "db-unexported-template",
		Title: "Kept From DB",
		Body:  "Database prompt",
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 3, 5, 0, 0, 0, time.UTC)
	schedulesYML := exportTriggerSchedulesYML(t, ctx, model.Schedule{
		ID:     "sched-post-publish",
		Title:  "Post-publish check",
		Body:   "Verify published artifacts.",
		Target: model.RepoRef{Owner: "acme", Name: "widgets"},
		Recurrence: model.Recurrence{
			Kind: model.RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{
				Workflow:    "publish.yml",
				Conclusions: []string{"success"},
			},
		},
		Overlap:   model.OverlapSkip,
		Enabled:   true,
		NextRunAt: now,
		CreatedAt: now,
	})

	// Three human config changes merged into state-repo at oldSchema while
	// loaded-head stays at initialHead (two via --no-ff PR merges and one
	// direct commit, including an event-triggered schedule).
	c1 := mergeBranchNoFF(t, repoDir, "pr-1-schedule", "config: add workflow-triggered audit schedule", func(dir string) {
		schedulesPath := filepath.Join(dir, "settings", "schedules.yml")
		if err := os.MkdirAll(filepath.Dir(schedulesPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(schedulesPath, schedulesYML, 0o644); err != nil {
			t.Fatal(err)
		}
	})

	// Second commit directly on main editing base-template's title.
	templatesYML := filepath.Join(repoDir, "settings", "templates.yml")
	rawTemplates, err := os.ReadFile(templatesYML)
	if err != nil {
		t.Fatal(err)
	}
	updatedTemplates := strings.Replace(string(rawTemplates), "Base Title", "Updated By Human Commit", 1)
	if updatedTemplates == string(rawTemplates) {
		t.Fatalf("expected Base Title in %s:\n%s", templatesYML, rawTemplates)
	}
	if err := os.WriteFile(templatesYML, []byte(updatedTemplates), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, repoDir, "add", "-A")
	runGitIn(t, repoDir, "commit", "-m", "config: refine base-template title")
	c2 := runGitIn(t, repoDir, "rev-parse", "HEAD")

	// Third commit via --no-ff merge adding a second template.
	triageTemplatesYML := exportTemplatesYML(t, ctx,
		model.Template{
			ID:    "tpl-base",
			Name:  "base-template",
			Title: "Updated By Human Commit",
			Body:  "Base prompt",
		},
		model.Template{
			ID:    "tpl-triage",
			Name:  "triage-helper",
			Title: "Triage Helper",
			Body:  "Triage incoming issues.",
		},
	)
	c3 := mergeBranchNoFF(t, repoDir, "pr-3-template", "config: add triage-helper template", func(dir string) {
		p := filepath.Join(dir, "settings", "templates.yml")
		if werr := os.WriteFile(p, triageTemplatesYML, 0o644); werr != nil {
			t.Fatal(werr)
		}
	})

	report, err := staterepo.MigrateRepo(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("MigrateRepo failed: %v", err)
	}
	if !report.Committed {
		t.Fatal("expected MigrateRepo to commit rewritten dump")
	}
	if len(report.Dropped) != 0 || report.ApplyErr != nil {
		t.Fatalf("expected no dropped commits, got dropped=%v err=%v", report.Dropped, report.ApplyErr)
	}
	if len(report.Applied) != 3 {
		t.Fatalf("expected 3 applied commits, got %d: %v", len(report.Applied), report.Applied)
	}
	if report.Applied[0].Hash != c1 || report.Applied[1].Hash != c2 || report.Applied[2].Hash != c3 {
		t.Fatalf("unexpected applied commit order: got %v, want [%s, %s, %s]", report.Applied, c1, c2, c3)
	}

	// Verify all three unapplied commits AND the unexported DB template are in db.
	templates, err := store.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]model.Template{}
	for _, tpl := range templates {
		byName[tpl.Name] = tpl
	}
	if got := byName["base-template"].Title; got != "Updated By Human Commit" {
		t.Errorf("base-template title = %q, want %q", got, "Updated By Human Commit")
	}
	if _, ok := byName["triage-helper"]; !ok {
		t.Errorf("triage-helper template from 3rd unapplied commit missing in db")
	}
	if _, ok := byName["db-unexported-template"]; !ok {
		t.Errorf("db-unexported-template lost during 3-way merge")
	}

	sched, err := store.GetSchedule(ctx, "sched-post-publish")
	if err != nil {
		t.Fatal(err)
	}
	if sched == nil {
		t.Fatalf("sched-post-publish schedule from 1st unapplied commit missing in db")
	}
	if sched.Recurrence.Kind != model.RecurrenceGitHubWorkflowRun ||
		sched.Recurrence.GitHubWorkflowRun == nil ||
		sched.Recurrence.GitHubWorkflowRun.Workflow != "publish.yml" {
		t.Errorf("unexpected recurrence on sched-post-publish: %+v", sched.Recurrence)
	}

	// Verify the rewritten dump in state-repo is at SchemaVersion and carries
	// the merged rows, so a subsequent Load agrees with db.
	gotVersion, err := staterepo.ReadSchemaVersion(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if gotVersion != model.SchemaVersion {
		t.Errorf("dump schema version = %d, want %d", gotVersion, model.SchemaVersion)
	}
	rawSchedulesAfter, err := os.ReadFile(filepath.Join(repoDir, "settings", "schedules.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawSchedulesAfter), "sched-post-publish") {
		t.Errorf("rewritten settings/schedules.yml lost sched-post-publish:\n%s", rawSchedulesAfter)
	}

	commitMsg := runGitIn(t, repoDir, "log", "-1", "--format=%B")
	if !strings.Contains(commitMsg, "Applied previously unapplied configuration commit(s)") ||
		!strings.Contains(commitMsg, "config: add workflow-triggered audit schedule") ||
		!strings.Contains(commitMsg, "config: refine base-template title") ||
		!strings.Contains(commitMsg, "config: add triage-helper template") {
		t.Errorf("migration commit message missing applied commit list:\n%s", commitMsg)
	}
}

func TestMigrateRepoReportsDroppedCommitsWhenPendingApplyFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()

	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID:    "tpl-intact",
		Name:  "intact-template",
		Title: "Intact Title",
		Body:  "Intact prompt",
	}); err != nil {
		t.Fatal(err)
	}

	repoDir := filepath.Join(dataDir, "state-repo")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir:         repoDir,
		DataDir:     dataDir,
		AuthorName:  "grain",
		AuthorEmail: "grain@localhost",
		NoWorkflow:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	oldSchema := model.SchemaVersion - 7
	if oldSchema < model.V2BaselineSchemaVersion {
		oldSchema = model.V2BaselineSchemaVersion
	}
	if err := staterepo.ExportConfig(ctx, db, repoDir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(repoDir, oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "Initial export"); err != nil {
		t.Fatal(err)
	}
	initialHead, err := repo.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetLoadedHead(ctx, initialHead); err != nil {
		t.Fatal(err)
	}

	// Commit 1 and Commit 2 leave settings/schedules.yml unparseable even at
	// the new schema.
	c1 := mergeBranchNoFF(t, repoDir, "pr-good-before-broken", "config: add note to README", func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("custom note\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	c2 := mergeBranchNoFF(t, repoDir, "pr-broken-yaml", "config: corrupt schedules yaml", func(dir string) {
		p := filepath.Join(dir, "settings", "schedules.yml")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("schedules: [\n  unclosed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	report, err := staterepo.MigrateRepo(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("MigrateRepo failed: %v", err)
	}
	if !report.Committed {
		t.Fatal("expected MigrateRepo to commit rewritten dump")
	}
	if len(report.Applied) != 0 {
		t.Fatalf("expected 0 applied commits, got %v", report.Applied)
	}
	if len(report.Dropped) != 2 {
		t.Fatalf("expected 2 dropped commits, got %d: %v", len(report.Dropped), report.Dropped)
	}
	if report.Dropped[0].Hash != c1 || report.Dropped[1].Hash != c2 {
		t.Fatalf("unexpected dropped commits: got %v, want [%s, %s]", report.Dropped, c1, c2)
	}
	if report.ApplyErr == nil {
		t.Fatal("expected non-nil ApplyErr when pending commits fail to import")
	}

	// Database kept its rows intact and Load succeeds cleanly on the rewritten dump.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("Load after MigrateRepo failed: %v", err)
	}
	templates, err := store.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 1 || templates[0].Name != "intact-template" {
		t.Fatalf("unexpected templates in db: %+v", templates)
	}

	commitMsg := runGitIn(t, repoDir, "log", "-1", "--format=%B")
	if !strings.Contains(commitMsg, "WARNING:") ||
		!strings.Contains(commitMsg, "config: add note to README") ||
		!strings.Contains(commitMsg, "config: corrupt schedules yaml") ||
		!strings.Contains(commitMsg, "Apply error:") {
		t.Errorf("migration commit message missing warning or dropped commits:\n%s", commitMsg)
	}
}

func TestApplyIncludesUnappliedCommitsInError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()

	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID:    "tpl-seed",
		Name:  "seed",
		Title: "Seed",
		Body:  "Prompt",
	}); err != nil {
		t.Fatal(err)
	}

	repoDir := filepath.Join(dataDir, "state-repo")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir:         repoDir,
		DataDir:     dataDir,
		AuthorName:  "grain",
		AuthorEmail: "grain@localhost",
		NoWorkflow:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}

	commitHash := mergeBranchNoFF(t, repoDir, "pr-bad-settings", "config: broken schedule syntax", func(dir string) {
		p := filepath.Join(dir, "settings", "schedules.yml")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("schedules: [\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	_, err = staterepo.Apply(ctx, repo, db, model.SchemaVersion)
	if !errors.Is(err, staterepo.ErrNotApplied) {
		t.Fatalf("expected ErrNotApplied, got %v", err)
	}
	if !strings.Contains(err.Error(), "config: broken schedule syntax") || !strings.Contains(err.Error(), commitHash[:7]) {
		t.Errorf("expected Apply error to name unapplied commit %s, got: %v", commitHash, err)
	}
}
