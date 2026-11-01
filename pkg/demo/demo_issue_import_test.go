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

package demo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestDemoGitHubSimImportsIssues is "Import issues now" on `grain demo
// -github-sim` (docs/test-runs/2026-10-31-github-issue-import.md, F1: the
// demo wired no ui.Config.IssueImporter, so the button answered that the
// deployment had no GitHub client). Through the e2e hook it also walks
// what the importer must not take -- a closed issue, a pull request in the
// issues listing -- an issue labelled after the fact, an issue whose task
// was closed, and an upstream source on the second simulated repository.
func TestDemoGitHubSimImportsIssues(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the simulated GitHub needs git")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg, _, err := configureGitHubSimDemo(dir, ui.Config{GitHubPullRequests: demoPullRequests{repo: repo, store: store}}, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	if cfg.IssueImporter == nil {
		t.Fatal("-github-sim wired no IssueImporter: Import issues now cannot run")
	}
	client, _ := cfg.GitHubBranches.(github.Client)
	hook := githubSimDemoHandler(http.NotFoundHandler(), client)
	sim := func(method, path, body string) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/e2e/github-sim/repos/"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		hook.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
		}
	}
	sim("POST", "acme/widgets/issues", `{"title":"Labelled","labels":["grain-agent"]}`) // 8001
	sim("POST", "acme/widgets/issues", `{"title":"Unlabelled"}`)                        // 8002
	sim("POST", "acme/widgets/issues", `{"title":"Closed","labels":["grain-agent"]}`)   // 8003
	sim("PATCH", "acme/widgets/issues/8003", `{"state":"closed"}`)

	put := func(imp model.IssueImport) {
		t.Helper()
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, IssueImport: imp}); err != nil {
			t.Fatal(err)
		}
	}
	put(model.IssueImport{Enabled: true, Label: "grain-agent"})
	out, err := cfg.IssueImporter(ctx, repo)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// The seeded #1 and #8001; not the closed #8003.
	if out.Open != 2 || len(out.Imported) != 2 || out.Skipped != 0 {
		t.Fatalf("first pass = %+v, want 2 read, 2 imported", out)
	}
	if out, err = cfg.IssueImporter(ctx, repo); err != nil || len(out.Imported) != 0 || out.Skipped != 2 {
		t.Fatalf("second pass = %+v, %v; want nothing new, 2 skipped", out, err)
	}

	// Closing #8001's task does not let it back in; labelling #8002
	// after the fact does.
	id, err := store.TaskForIssue(ctx, repo, 8001, repo)
	if err != nil || id == "" {
		t.Fatalf("TaskForIssue(8001) = %q, %v", id, err)
	}
	closedAt := time.Now().UTC()
	if err := store.ObserveField(ctx, id, closedAt, func(o *model.Observation) {
		o.ClosedAt = &closedAt
	}); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, id); err != nil || state != model.StateClosed {
		t.Fatalf("closed task is %s (%v), want %s", state, err, model.StateClosed)
	}
	sim("POST", "acme/widgets/issues/8002/labels", `{"labels":["grain-agent"]}`)
	out, err = cfg.IssueImporter(ctx, repo)
	if err != nil || len(out.Imported) != 1 || out.Skipped != 2 {
		t.Fatalf("pass after a late label = %+v, %v; want #8002 alone imported", out, err)
	}

	// The upstream: a second simulated repository, rather than a Sim
	// configured for acme/widgets panicking at the other name.
	put(model.IssueImport{Enabled: true, Repo: demoGitHubSimUpstream.String()})
	out, err = cfg.IssueImporter(ctx, repo)
	if err != nil || len(out.Imported) != 1 || out.Source != demoGitHubSimUpstream.String() {
		t.Fatalf("upstream pass = %+v, %v; want one import from %s", out, err, demoGitHubSimUpstream)
	}
	task, err := store.GetTask(ctx, out.Imported[0])
	if err != nil || task == nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Issue != 0 || !strings.HasPrefix(task.Title, "[acme-upstream/widgets#7] ") {
		t.Fatalf("upstream import: Issue %d, title %q; want no Issue and an [owner/name#N] title", task.Issue, task.Title)
	}

	// A repository no Sim stands for reads as GitHub's 404.
	put(model.IssueImport{Enabled: true, Repo: "nobody/nothing"})
	if _, err := cfg.IssueImporter(ctx, repo); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("pass on an unknown source = %v, want GitHub's 404", err)
	}
}
