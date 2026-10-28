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

// One repository, one spelling. GitHub resolves owner/name
// case-insensitively, so `ACME/Widgets` and `acme/widgets` are one repo
// to clone, push to and open pull requests against -- but every key
// grain has for a repo (a repo_config row, the repos page's grouping,
// the target repo allowlist) matches exactly, so two spellings of one
// repo used to be two repos here. ui.(*Client).parseRepo is where that
// is decided now: a repo the deployment already names is stored the way
// the deployment names it.

import (
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateTaskSpellsTheTargetTheAllowlistsWay(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the thing", Repo: "ACME/Widgets", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating a task naming the allowed repo in another case: %v", err)
	}
	if task.Repo != "acme/widgets" {
		t.Fatalf("repo = %q, want acme/widgets -- the allowlist's own spelling", task.Repo)
	}
	// The point of storing it that way: the repo it is filed against is
	// the one every repo-keyed thing here already knows about.
	if task.State != model.StateQueued {
		t.Fatalf("state = %q, want queued: the allowlist names this repo, whatever case it was typed in",
			task.State)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Target == nil || stored.Target.String() != "acme/widgets" {
		t.Fatalf("stored target = %v, want acme/widgets", stored.Target)
	}
}

// With no allowlist at all (the unrestricted deployment), the default
// target is the other place this deployment has spelled a repo out.
func TestCreateTaskSpellsTheTargetTheDefaultTargetsWay(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t) // DefaultTarget is acme/widgets

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the thing", Repo: "Acme/WIDGETS", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Repo != "acme/widgets" {
		t.Fatalf("repo = %q, want acme/widgets -- the default target's own spelling", task.Repo)
	}
}

// A repo this deployment has never named has no canonical spelling to
// impose, and whoever typed it is not one: it is stored as given.
func TestCreateTaskLeavesAnUnknownRepoSpelledAsTyped(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the thing", Repo: "Someone-Else/Unrelated", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Repo != "Someone-Else/Unrelated" {
		t.Fatalf("repo = %q, want it stored exactly as typed", task.Repo)
	}
}

// A repo grain both works in and reads from is one repo, and which field
// it was typed into does not change that.
func TestCreateTaskSpellsReadsTheDeploymentsWay(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "acme/gadgets"})

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the thing", Repo: "acme/widgets", Approved: true,
		Reads: []string{"ACME/Gadgets"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(task.Reads, []string{"acme/gadgets"}) {
		t.Fatalf("reads = %v, want [acme/gadgets]", task.Reads)
	}
}

// The failure this whole change is about: a repo's own defaults are a
// row keyed (owner, name), and a task that spelled the repo differently
// used to be filed without them.
func TestCreateTaskGetsRepoDefaultsHoweverTheRepoIsSpelled(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})
	putRepoDefaultCapabilities(t, ctx, store, "acme/widgets", "gcp-key")

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the thing", Repo: "ACME/WIDGETS", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(task.Capabilities, []string{"gcp-key"}) {
		t.Fatalf("capabilities = %v, want [gcp-key]: the repo's own defaults", task.Capabilities)
	}
}

// And the same row from the other side: configuring a repo through the
// repos pane writes the row a task will actually read, rather than a
// second row nothing looks at.
func TestRepoDefaultsAreOneRowHoweverTheRepoIsSpelled(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	if _, err := c.SetRepoPromptExtension(ctx, "ACME/Widgets", "run make deps first"); err != nil {
		t.Fatalf("setting a prompt extension: %v", err)
	}
	got, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "acme/widgets" {
		t.Fatalf("repo = %q, want acme/widgets", got.Repo)
	}
	if got.PromptExtension != "run make deps first" {
		t.Fatalf("promptExtension = %q, want the text written under the other spelling", got.PromptExtension)
	}
}

func TestUpdateTaskSpellsTheTargetTheDeploymentsWay(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "acme/gadgets"})

	task := create(t, c, ctx)
	repo := "ACME/Gadgets"
	updated, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Repo: &repo})
	if err != nil {
		t.Fatalf("moving a task to another repo: %v", err)
	}
	if updated.Repo != "acme/gadgets" {
		t.Fatalf("repo = %q, want acme/gadgets", updated.Repo)
	}
}

func TestCreateScheduleSpellsTheTargetTheDeploymentsWay(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "nightly", Repo: "ACME/Widgets", Recurrence: ui.Recurrence{Kind: "everyNHours", EveryNHours: 24},
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	if sched.Repo != "acme/widgets" {
		t.Fatalf("repo = %q, want acme/widgets", sched.Repo)
	}
}

func TestCreateTemplateSpellsItsBindingTheDeploymentsWay(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "sweep", Title: "sweep the thing", Repo: "ACME/Widgets",
	})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	if tmpl.Repo != "acme/widgets" {
		t.Fatalf("repo = %q, want acme/widgets", tmpl.Repo)
	}
}

// A task already stored in some other case -- filed before any of this,
// or by an operator editing the allowlist afterwards -- names an allowed
// repo and must not be parked off the list for how it is capitalised.
func TestTaskStoredInAnotherCaseIsStillOnTheAllowlist(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	// Filed with no allowlist at all, so nothing rewrites the spelling.
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the thing", Repo: "Someone-Else/Unrelated", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Target.String() != "Someone-Else/Unrelated" {
		t.Fatalf("stored target = %v, want it as typed", stored.Target)
	}

	// The operator then writes the allowlist in their own case. The task
	// they already have targets a repo it names.
	setDefaultTeamTargetRepos(t, store, ctx, []string{"someone-else/unrelated"})
	same, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the other thing", Repo: "Someone-Else/Unrelated", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if same.State != model.StateQueued {
		t.Fatalf("state = %q, want queued: the allowlist names this repo", same.State)
	}
}

// The allowlist is what parseRepo reads a repo's canonical spelling
// from, so what goes into it has to be a repo rather than whatever was
// in the clipboard.
func TestUpdateTeamStoresTargetReposAsOwnerName(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	repos := []string{"https://github.com/acme/widgets", "git@github.com:acme/gadgets.git"}
	got, err := c.UpdateTeam(ctx, model.DefaultTeamID, ui.UpdateTeamRequest{TargetRepos: &repos})
	if err != nil {
		t.Fatalf("setting target repos: %v", err)
	}
	want := []string{"acme/widgets", "acme/gadgets"}
	if !reflect.DeepEqual(got.TargetRepos, want) {
		t.Fatalf("targetRepos = %v, want %v", got.TargetRepos, want)
	}
}
