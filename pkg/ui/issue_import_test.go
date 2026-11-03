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

package ui_test

// The API surface of the GitHub issue importer
// (model.RepoConfig.IssueImport): the "imported" status and kind tag an
// imported task carries, its settings, and the manual trigger. What the
// importer files is pinned in pkg/orchestrator.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func putImportedTask(t *testing.T, ctx context.Context, store *model.Store, id string) {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{
		ID: id, Intent: model.IntentImplement, Title: "[#12] Crash on save", Body: "Imported from GitHub issue acme/widgets#12",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "issue-import"}},
			Reason:      model.ReasonImported,
		},
		Binding: model.BindingDirective, Target: &repo, Issue: 12,
		WorkOrigin: model.GitHubOrigin(repo, 12), CreatedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
}

func findTask(t *testing.T, ctx context.Context, c *ui.Client, id string) ui.Task {
	t.Helper()
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task %s is not listed", id)
	return ui.Task{}
}

// An unapproved import reads as "imported" -- its kind and its status --
// on the wire, and once approved it is queued and still tagged imported.
func TestAnImportedTaskReadsAsImportedBeforeAndAfterApproval(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putImportedTask(t, ctx, store, "1")

	task := findTask(t, ctx, c, "1")
	if task.State != model.StateProposed || !task.Imported || !task.AwaitingImportApproval {
		t.Fatalf("an unapproved import is state %s imported=%v awaiting=%v, want proposed, imported, awaiting approval",
			task.State, task.Imported, task.AwaitingImportApproval)
	}
	if task.Origin != "acme/widgets#12" {
		t.Errorf("origin = %q, want acme/widgets#12", task.Origin)
	}
	if kinds := ui.TaskKindTags(task); !slices.Contains(kinds, ui.TaskKindImported) || !slices.Contains(kinds, ui.TaskKindProposal) {
		t.Errorf("kinds = %v, want imported and proposal", kinds)
	}
	if err := (ui.TaskExpectation{Kind: "imported"}).Check(task); err != nil {
		t.Errorf("expect kind imported: %v", err)
	}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"imported":true`, `"awaitingImportApproval":true`, `"state":"proposed"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the wire shape does not carry %s: %s", want, raw)
		}
	}

	if err := c.Approve(ctx, "1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	task = findTask(t, ctx, c, "1")
	if task.State != model.StateQueued || !task.Imported || task.AwaitingImportApproval {
		t.Errorf("an approved import is state %s imported=%v awaiting=%v, want queued, imported, not awaiting",
			task.State, task.Imported, task.AwaitingImportApproval)
	}
	if err := (ui.TaskExpectation{Kind: "imported"}).Check(task); err != nil {
		t.Errorf("expect kind imported after approval: %v", err)
	}
	if !slices.Contains(ui.TaskKinds, "imported") {
		t.Errorf("TaskKinds %v does not offer imported", ui.TaskKinds)
	}
}

// A task filed by hand is not imported.
func TestAHandFiledTaskIsNotImported(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "by hand", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	task := findTask(t, ctx, c, created.ID)
	if task.Imported || task.AwaitingImportApproval || slices.Contains(ui.TaskKindTags(task), ui.TaskKindImported) {
		t.Errorf("a hand-filed task reads as imported: %+v", task)
	}
}

func TestSetRepoSettingsConfiguresTheIssueImporter(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	defaults, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if defaults.IssueImport.Enabled || defaults.IssueImport.Approved ||
		defaults.IssueImport.EffectiveIntervalSeconds != int(model.DefaultIssueImportInterval/time.Second) {
		t.Errorf("a repository with nothing set reads %+v, want off, unapproved, the default interval", defaults.IssueImport)
	}

	on, source, label, interval := true, " https://github.com/kubernetes/autoscaler ", " bug ", 900
	got, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{
		IssueImportEnabled: &on, IssueImportRepo: &source, IssueImportLabel: &label,
		IssueImportIntervalSeconds: &interval,
	})
	if err != nil {
		t.Fatalf("SetRepoSettings: %v", err)
	}
	want := ui.RepoIssueImport{Enabled: true, Repo: "kubernetes/autoscaler", Label: "bug",
		IntervalSeconds: 900, EffectiveIntervalSeconds: 900}
	if got.IssueImport != want {
		t.Errorf("issueImport = %+v, want %+v", got.IssueImport, want)
	}

	// Approved is its own decision, and leaving the others out of a
	// request leaves them alone.
	got, err = c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{IssueImportApproved: &on})
	if err != nil {
		t.Fatal(err)
	}
	want.Approved = true
	if got.IssueImport != want {
		t.Errorf("after approving, issueImport = %+v, want %+v", got.IssueImport, want)
	}

	bad := "not a repo at all"
	if _, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{IssueImportRepo: &bad}); err == nil {
		t.Error("SetRepoSettings accepted an import source that is not a repository")
	}
	short := 10
	if _, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{IssueImportIntervalSeconds: &short}); err == nil {
		t.Error("SetRepoSettings accepted a ten-second poll")
	}
}

func TestImportIssuesNowNeedsTheImporterOn(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()
	var asked []model.RepoRef
	c.Config.IssueImporter = func(_ context.Context, repo model.RepoRef) (model.IssueImportResult, error) {
		asked = append(asked, repo)
		return model.IssueImportResult{Repo: repo.String(), Source: repo.String(), Open: 3,
			Imported: []string{"7", "8"}, Skipped: 1}, nil
	}

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/import-issues", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("with the importer off: %d %s, want 400", rec.Code, rec.Body)
	}
	if len(asked) != 0 {
		t.Fatalf("the importer was run for a repository that has it off: %v", asked)
	}

	on := true
	if _, err := c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{IssueImportEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, srv, http.MethodPost, "/api/repos/acme/widgets/import-issues", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("with the importer on: %d %s, want 200", rec.Code, rec.Body)
	}
	var got model.IssueImportResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Imported, []string{"7", "8"}) || got.Skipped != 1 || got.Open != 3 {
		t.Errorf("answer %+v", got)
	}
	if len(asked) != 1 || asked[0].String() != "acme/widgets" {
		t.Errorf("importer asked for %v, want [acme/widgets]", asked)
	}
}
