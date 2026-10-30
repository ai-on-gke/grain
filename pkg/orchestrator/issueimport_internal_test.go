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

package orchestrator

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// issueClient is the GitHub an issue importer reads: the open issues of
// each repository and their comments, plus the progress comments the work
// tree posts back.
type issueClient struct {
	github.Client
	issues   map[string][]github.Issue
	comments map[int][]github.Comment
	listed   []string
	created  []string
}

func (c *issueClient) ListIssues(owner, repo, label string) ([]github.Issue, error) {
	c.listed = append(c.listed, owner+"/"+repo+"?label="+label)
	var out []github.Issue
	for _, i := range c.issues[owner+"/"+repo] {
		if label == "" || i.HasLabel(label) {
			out = append(out, i)
		}
	}
	return out, nil
}

func (c *issueClient) ListComments(owner, repo string, number int) ([]github.Comment, error) {
	return c.comments[number], nil
}

func (c *issueClient) CreateComment(owner, repo string, number int, body string) (int, error) {
	c.created = append(c.created, body)
	return len(c.created), nil
}

func openIssue(n int, title string, labels ...string) github.Issue {
	set := map[string]struct{}{}
	for _, l := range labels {
		set[l] = struct{}{}
	}
	return github.Issue{
		Number: n, Title: title, Body: "Steps to reproduce " + title, State: "open",
		HTMLURL: "https://github.com/acme/widgets/issues/" + strings.TrimSpace(title),
		Labels:  set, Author: "reporter",
	}
}

func putImportConfig(t *testing.T, ctx context.Context, store *model.Store, imp model.IssueImport) {
	t.Helper()
	// issueImportPolled is keyed by the store's address, which a later
	// test's store may reuse once this one is gone.
	issueImportPolled.Clear()
	t.Cleanup(issueImportPolled.Clear)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, IssueImport: imp}); err != nil {
		t.Fatal(err)
	}
}

func tasksByReason(t *testing.T, ctx context.Context, store *model.Store, reason model.OriginReason) []model.Task {
	t.Helper()
	all, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, task := range all {
		if task.Origin.Reason == reason {
			out = append(out, task)
		}
	}
	slices.SortFunc(out, func(a, b model.Task) int { return a.Issue - b.Issue })
	return out
}

// The default: every open issue becomes one unapproved task, proposed and
// not dispatchable, carrying the issue's provenance -- and a later poll,
// even after one of those tasks has been closed, files nothing again.
func TestIssueImportFilesEachOpenIssueOnceUnapprovedByDefault(t *testing.T) {
	store, ctx := openTreeStore(t)
	client := &issueClient{
		issues: map[string][]github.Issue{"acme/widgets": {openIssue(2, "Crash on save", "bug"), openIssue(1, "Typo")}},
		comments: map[int][]github.Comment{2: {
			{User: "maintainer", Body: "Confirmed on main."},
		}},
	}
	putImportConfig(t, ctx, store, model.IssueImport{Enabled: true})
	deps := Deps{Store: store, Client: client}
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

	if err := reconcileIssueImports(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	tasks := tasksByReason(t, ctx, store, model.ReasonImported)
	if len(tasks) != 2 {
		t.Fatalf("imported %d tasks, want 2", len(tasks))
	}
	for _, task := range tasks {
		if task.Approval != nil || task.ApprovedAt != nil {
			t.Errorf("task %s for #%d is approved; the default is unapproved", task.ID, task.Issue)
		}
		state, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state != model.StateProposed {
			t.Errorf("task for #%d is %s, want %s (held for a person)", task.Issue, state, model.StateProposed)
		}
		if want := model.GitHubOrigin(widgets, task.Issue); task.WorkOrigin != want {
			t.Errorf("task for #%d has origin %q, want %q", task.Issue, task.WorkOrigin, want)
		}
		if task.Target == nil || !task.Target.Same(widgets) {
			t.Errorf("task for #%d targets %v, want %s", task.Issue, task.Target, widgets)
		}
		if !slices.Contains(model.TaskKindTags(task, state), model.TaskKindImported) {
			t.Errorf("task for #%d carries kinds %v, want %q among them", task.Issue, model.TaskKindTags(task, state), model.TaskKindImported)
		}
	}
	crash := tasks[1]
	if crash.Issue != 2 || crash.Title != "[#2] Crash on save" {
		t.Errorf("second task is #%d %q, want #2 %q", crash.Issue, crash.Title, "[#2] Crash on save")
	}
	for _, want := range []string{"acme/widgets#2", "https://github.com/acme/widgets/issues/Crash on save",
		"Author: @reporter", "Labels: bug", "Steps to reproduce Crash on save", "@maintainer", "Confirmed on main."} {
		if !strings.Contains(crash.Body, want) {
			t.Errorf("body does not carry %q:\n%s", want, crash.Body)
		}
	}

	// Close one, and poll again past the interval: neither issue is
	// filed a second time, whatever state its task is in.
	closedAt := now.Add(time.Minute)
	if err := store.ObserveField(ctx, tasks[0].ID, closedAt, func(o *model.Observation) {
		o.ClosedAt = &closedAt
	}); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, tasks[0].ID); err != nil || state != model.StateClosed {
		t.Fatalf("closed task is %s (%v), want %s", state, err, model.StateClosed)
	}
	later := now.Add(model.DefaultIssueImportInterval)
	if err := reconcileIssueImports(ctx, deps, later); err != nil {
		t.Fatal(err)
	}
	if got := len(tasksByReason(t, ctx, store, model.ReasonImported)); got != 2 {
		t.Fatalf("after a second poll there are %d imported tasks, want still 2", got)
	}
	if len(client.listed) != 2 {
		t.Errorf("listed issues %d times over two polls, want 2", len(client.listed))
	}
	result, err := ImportIssues(ctx, store, client, widgets, later)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Imported) != 0 || result.Skipped != 2 || result.Open != 2 {
		t.Errorf("a manual pass reported %+v, want nothing imported and both skipped", result)
	}
}

// A poll inside the interval reads nothing; one after it reads again.
func TestIssueImportWaitsOutItsInterval(t *testing.T) {
	store, ctx := openTreeStore(t)
	client := &issueClient{issues: map[string][]github.Issue{"acme/widgets": {openIssue(1, "One")}}}
	putImportConfig(t, ctx, store, model.IssueImport{Enabled: true, IntervalSeconds: 300})
	deps := Deps{Store: store, Client: client}
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	for _, at := range []time.Duration{0, time.Minute, 4 * time.Minute} {
		if err := reconcileIssueImports(ctx, deps, now.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.listed) != 1 {
		t.Fatalf("read the issues %d times inside one interval, want 1", len(client.listed))
	}
	client.issues["acme/widgets"] = append(client.issues["acme/widgets"], openIssue(2, "Two"))
	if err := reconcileIssueImports(ctx, deps, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(client.listed) != 2 {
		t.Fatalf("read the issues %d times after the interval, want 2", len(client.listed))
	}
	if got := len(tasksByReason(t, ctx, store, model.ReasonImported)); got != 2 {
		t.Errorf("imported %d tasks, want 2 (the new issue only, the second time)", got)
	}
}

// Approved: what the importer files is queued at once.
func TestIssueImportApprovedLandsQueued(t *testing.T) {
	store, ctx := openTreeStore(t)
	client := &issueClient{issues: map[string][]github.Issue{"acme/widgets": {openIssue(4, "Approve me")}}}
	putImportConfig(t, ctx, store, model.IssueImport{Enabled: true, Approved: true})
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	if err := reconcileIssueImports(ctx, Deps{Store: store, Client: client}, now); err != nil {
		t.Fatal(err)
	}
	tasks := tasksByReason(t, ctx, store, model.ReasonImported)
	if len(tasks) != 1 {
		t.Fatalf("imported %d tasks, want 1", len(tasks))
	}
	if tasks[0].Approval == nil || tasks[0].Approval.Actor != IssueImportPrincipal {
		t.Errorf("approval is %+v, want the importer's", tasks[0].Approval)
	}
	state, err := store.State(ctx, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Errorf("state is %s, want %s", state, model.StateQueued)
	}
}

// A label filter and an upstream source: only labelled issues are read,
// the tasks still target the configured repository, and Task.Issue -- a
// number in the target, where its pull request says "Fixes #N" -- is left
// unset so it cannot name an unrelated issue there.
func TestIssueImportFromAnotherRepository(t *testing.T) {
	store, ctx := openTreeStore(t)
	client := &issueClient{issues: map[string][]github.Issue{"kubernetes/autoscaler": {
		openIssue(7, "Scale down stuck", "grain"), openIssue(8, "Unlabelled"),
	}}}
	putImportConfig(t, ctx, store, model.IssueImport{Enabled: true, Repo: "kubernetes/autoscaler", Label: "grain"})
	// A task filed by hand in the fork against its own issue 7 is not
	// the upstream issue, and does not stop the import.
	local := model.Task{ID: "1", Intent: model.IntentImplement, Title: "local seven", Issue: 7, Target: &widgets,
		Binding: model.BindingDirective,
		Origin:  model.Origin{Attribution: model.Attribution{Actor: model.TriggerPrincipal}, Reason: model.ReasonDirect}}
	if err := store.PutTask(ctx, local); err != nil {
		t.Fatal(err)
	}
	result, err := ImportIssues(ctx, store, client, widgets, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "kubernetes/autoscaler" || len(result.Imported) != 1 {
		t.Fatalf("result %+v, want one task imported from kubernetes/autoscaler", result)
	}
	if want := "kubernetes/autoscaler?label=grain"; len(client.listed) != 1 || client.listed[0] != want {
		t.Errorf("listed %v, want [%s]", client.listed, want)
	}
	task, err := store.GetTask(ctx, result.Imported[0])
	if err != nil || task == nil {
		t.Fatal(task, err)
	}
	if task.Issue != 0 {
		t.Errorf("Task.Issue is %d for an issue on another repository, want 0", task.Issue)
	}
	if task.WorkOrigin != "kubernetes/autoscaler#7" || task.Title != "[kubernetes/autoscaler#7] Scale down stuck" {
		t.Errorf("origin %q title %q", task.WorkOrigin, task.Title)
	}
	if task.Target == nil || !task.Target.Same(widgets) {
		t.Errorf("target %v, want %s", task.Target, widgets)
	}
}

// A task already filed against an issue of the same repository by number
// -- a GitHub mention, or a person -- stands for it.
func TestIssueImportSkipsAnIssueATaskAlreadyNames(t *testing.T) {
	store, ctx := openTreeStore(t)
	client := &issueClient{issues: map[string][]github.Issue{"acme/widgets": {openIssue(3, "Already filed")}}}
	putImportConfig(t, ctx, store, model.IssueImport{Enabled: true})
	existing := model.Task{ID: "1", Intent: model.IntentImplement, Title: "by hand", Issue: 3, Target: &widgets,
		Binding: model.BindingDirective,
		Origin:  model.Origin{Attribution: model.Attribution{Actor: model.TriggerPrincipal}, Reason: model.ReasonDirect}}
	if err := store.PutTask(ctx, existing); err != nil {
		t.Fatal(err)
	}
	result, err := ImportIssues(ctx, store, client, widgets, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Imported) != 0 || result.Skipped != 1 {
		t.Errorf("result %+v, want the issue skipped", result)
	}
}

// Nothing is imported for a repository that has not switched it on.
func TestIssueImportNeedsSwitchingOn(t *testing.T) {
	store, ctx := openTreeStore(t)
	client := &issueClient{issues: map[string][]github.Issue{"acme/widgets": {openIssue(1, "One")}}}
	putImportConfig(t, ctx, store, model.IssueImport{Label: "bug"})
	if err := reconcileIssueImports(ctx, Deps{Store: store, Client: client}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportIssues(ctx, store, client, widgets, time.Now()); err == nil {
		t.Error("a manual import of a repository with the importer off succeeded")
	}
	if len(client.listed) != 0 {
		t.Errorf("listed issues %v with the importer off", client.listed)
	}
}

// An unapproved import posts no progress comment on its issue; once a
// person approves it, the work tree reports as it does for any origin.
func TestUnapprovedImportPostsNoProgressComment(t *testing.T) {
	store, ctx := openTreeStore(t)
	client := &issueClient{issues: map[string][]github.Issue{"acme/widgets": {openIssue(9, "Quiet")}}}
	putImportConfig(t, ctx, store, model.IssueImport{Enabled: true})
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	deps := Deps{Store: store, Client: client}
	if err := reconcileIssueImports(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	if err := reconcileWorkTrees(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	if len(client.created) != 0 {
		t.Fatalf("posted %d progress comments on an issue nobody has approved work on", len(client.created))
	}
	task := tasksByReason(t, ctx, store, model.ReasonImported)[0]
	task.Approval = &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}
	task.ApprovedAt = &now
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := reconcileWorkTrees(ctx, deps, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(client.created) != 1 {
		t.Fatalf("posted %d progress comments once the import was approved, want 1", len(client.created))
	}
}

func TestImportedIssueBodyBoundsWhatItCarries(t *testing.T) {
	issue := openIssue(1, "Huge")
	issue.Body = strings.Repeat("é", issueImportMaxBody)
	var comments []github.Comment
	for i := 0; i < issueImportMaxComments+3; i++ {
		comments = append(comments, github.Comment{User: "u", Body: "c"})
	}
	body := importedIssueBody(widgets, issue, comments)
	if len(body) > issueImportMaxBody+4096 {
		t.Errorf("body is %d bytes", len(body))
	}
	if !strings.Contains(body, "(truncated)") {
		t.Error("a clipped description does not say so")
	}
	if !strings.Contains(body, "Comments (5 of 8)") {
		t.Errorf("comments heading missing:\n%s", body[len(body)-300:])
	}
	if !strings.HasPrefix(body, "Imported from GitHub issue acme/widgets#1") {
		t.Errorf("body starts %q", body[:60])
	}
}
