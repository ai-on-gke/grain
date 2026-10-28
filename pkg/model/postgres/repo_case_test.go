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

// One repository, one spelling -- for rows that were already stored
// before anything enforced it (grain/task-34, backfilling what
// grain/task-32 left alone).
//
// These run against real SQLite rather than in pkg/model, because the
// whole of what is being checked is what the database does with a
// primary key: that a rewrite lands, that a merge drops the row it
// merged, and that a `SELECT ... WHERE owner = 'acme'` stops finding the
// row that used to answer it.

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

var (
	canonicalRepo = model.RepoRef{Owner: "acme", Name: "widgets"}
	shoutedRepo   = model.RepoRef{Owner: "ACME", Name: "Widgets"}
	deployment    = []model.RepoRef{canonicalRepo}
)

// taskIn is a filed task targeting repo, in whatever case repo spells
// it, reading whatever it is told to read.
func taskIn(id string, repo model.RepoRef, reads ...model.RepoRef) model.Task {
	t := task(id, true)
	t.Target = &repo
	t.Reads = reads
	return t
}

func TestRepoCaseRewritesATaskStoredInAnotherCase(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, taskIn("a1b2", shoutedRepo)); err != nil {
		t.Fatal(err)
	}

	plan, err := store.PlanRepoCase(ctx, deployment)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if plan.Rows() != 1 || len(plan.Conflicts) != 0 {
		t.Fatalf("plan = %+v, want one row and no conflicts", plan)
	}
	// A plan is a report: it says what it would do and does none of it.
	if got := targetOf(t, ctx, store, "a1b2"); got != shoutedRepo {
		t.Fatalf("target after planning = %s, want it untouched at %s", got, shoutedRepo)
	}

	if _, err := store.ApplyRepoCase(ctx, deployment); err != nil {
		t.Fatalf("applying: %v", err)
	}
	if got := targetOf(t, ctx, store, "a1b2"); got != canonicalRepo {
		t.Fatalf("target after applying = %s, want %s", got, canonicalRepo)
	}
	// And the whole point of doing it: the repo is now one repo, so a
	// second pass has nothing left to find.
	after, err := store.PlanRepoCase(ctx, deployment)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Empty() {
		t.Fatalf("a second plan = %+v, want nothing left to do", after)
	}
}

// A repo this deployment has never named has no canonical spelling to
// move to -- the same answer ui.parseRepo gives a repo typed into a
// deployment that does not name it.
func TestRepoCaseLeavesAnUnnamedRepoAlone(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	stranger := model.RepoRef{Owner: "Someone-Else", Name: "Unrelated"}
	if err := store.PutTask(ctx, taskIn("a1b2", stranger)); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Fatalf("plan = %+v, want nothing: this deployment says nothing about that repo", plan)
	}
	if got := targetOf(t, ctx, store, "a1b2"); got != stranger {
		t.Fatalf("target = %s, want it exactly as stored", got)
	}
}

// A task that read one repository twice, once per spelling, reads it
// once afterwards -- which is what task_read's own primary key already
// meant by (task_id, owner, name).
func TestRepoCaseMergesATasksDuplicateReads(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	if err := store.PutTask(ctx, taskIn("a1b2", gadgets, canonicalRepo, shoutedRepo)); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if err != nil {
		t.Fatalf("applying: %v", err)
	}
	if plan.Rows() != 1 {
		t.Fatalf("plan = %+v, want the one duplicate read", plan)
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if want := []model.RepoRef{canonicalRepo}; !reflect.DeepEqual(got.Reads, want) {
		t.Fatalf("reads = %v, want %v -- one repository, read once", got.Reads, want)
	}
}

// A task's extra write targets are keyed (task_id, owner, name), so one
// repository named under both spellings is two write targets on one
// task -- and after the rewrite it is the one it always was. The base
// both of them carry is the same, which is what makes them mergeable.
func TestRepoCaseMergesATasksDuplicateWriteTargets(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := taskIn("a1b2", model.RepoRef{Owner: "acme", Name: "gadgets"})
	tk.Writes = []model.WriteTarget{
		{Repo: canonicalRepo, Base: "deploy"},
		{Repo: shoutedRepo, Base: "deploy"},
	}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if err != nil {
		t.Fatalf("applying: %v", err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Table != "task_write" || plan.Changes[0].Merged != 1 {
		t.Fatalf("plan = %+v, want the one duplicate write target merged", plan)
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	want := []model.WriteTarget{{Repo: canonicalRepo, Base: "deploy"}}
	if !reflect.DeepEqual(got.Writes, want) {
		t.Fatalf("writes = %+v, want %+v -- one repository, written to once", got.Writes, want)
	}
}

// Two spellings of one repository that name different base branches are
// two distinct write targets for that repository: a task may have multiple
// branches configured. Both rows are rewritten to the canonical spelling.
func TestRepoCasePreservesWriteTargetsWithDifferentBases(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := taskIn("a1b2", model.RepoRef{Owner: "acme", Name: "gadgets"})
	tk.Writes = []model.WriteTarget{
		{Repo: canonicalRepo},
		{Repo: shoutedRepo, Base: "rel.1.0"},
	}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if err != nil {
		t.Fatalf("applying: %v", err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Table != "task_write" || plan.Changes[0].Rewritten != 1 {
		t.Fatalf("plan = %+v, want the one divergent write target rewritten", plan)
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	want := []model.WriteTarget{
		{Repo: canonicalRepo},
		{Repo: canonicalRepo, Base: "rel.1.0"},
	}
	if !reflect.DeepEqual(got.Writes, want) {
		t.Fatalf("writes = %+v, want %+v", got.Writes, want)
	}
}

// The same table twice over, on the two kinds of stored content that
// file tasks: a schedule's write targets and a template's move with
// everything else they carry.
func TestRepoCaseMovesScheduleAndTemplateWriteTargets(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "s1", Title: "nightly", Target: canonicalRepo,
		Writes:     []model.WriteTarget{{Repo: shoutedRepo, Base: "deploy"}},
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		CreatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "t1", Name: "smoke", Title: "smoke", Target: &canonicalRepo,
		Writes:    []model.WriteTarget{{Repo: shoutedRepo, Base: "deploy"}},
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyRepoCase(ctx, deployment); err != nil {
		t.Fatalf("applying: %v", err)
	}
	sched, err := store.GetSchedule(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	want := []model.WriteTarget{{Repo: canonicalRepo, Base: "deploy"}}
	if sched == nil || !reflect.DeepEqual(sched.Writes, want) {
		t.Fatalf("schedule writes = %+v, want %+v", sched, want)
	}
	tmpl, err := store.GetTemplate(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl == nil || !reflect.DeepEqual(tmpl.Writes, want) {
		t.Fatalf("template writes = %+v, want %+v", tmpl, want)
	}
}

// A run's pinned write target is a copy of its task's, taken at dispatch
// so that an edit cannot move it mid-run. A respelling is not such an
// edit -- it is the same repository -- and leaving it behind would be a
// run history naming a repo in a case nothing else in the store uses.
func TestRepoCaseMovesARunsPinnedTarget(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutTask(ctx, taskIn("a1b2", shoutedRepo)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunSandbox(ctx, "r1", "sandbox-0", &shoutedRepo); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyRepoCase(ctx, deployment); err != nil {
		t.Fatalf("applying: %v", err)
	}
	var owner, name string
	if err := db.QueryRowContext(ctx,
		"SELECT `target_owner`,`target_name` FROM `task_run` WHERE `id` = 'r1'").Scan(&owner, &name); err != nil {
		t.Fatal(err)
	}
	if got := (model.RepoRef{Owner: owner, Name: name}); got != canonicalRepo {
		t.Fatalf("the run's pinned target = %s, want %s", got, canonicalRepo)
	}
	// And the pin still does its job: the proxy resolves this sandbox to
	// the repository the run is working in.
	target, _, _, err := store.GitScope(ctx, "sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || *target != canonicalRepo {
		t.Fatalf("git scope target = %v, want %s", target, canonicalRepo)
	}
}

// A repo's memory is read by an exact (owner, name) match, so one left
// in the old spelling is a memory nothing will ever read again while the
// repo appears to have none. Its documents move with it.
func TestRepoCaseMovesARepoMemoryAndItsDocuments(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutMemory(ctx, model.Memory{Repo: &shoutedRepo, Text: "the tests are slow"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMemoryDocument(ctx, model.MemoryDocument{
		Repo: &shoutedRepo, Name: "deploys", Summary: "how a release goes out", Text: "push the tag",
	}); err != nil {
		t.Fatal(err)
	}
	// The deployment's own memory is keyed by two empty strings, which
	// name no repository and match no spelling: it must be left alone.
	if err := store.PutMemory(ctx, model.Memory{Text: "deployment-wide"}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyRepoCase(ctx, deployment); err != nil {
		t.Fatalf("applying: %v", err)
	}
	got, err := store.GetMemory(ctx, &canonicalRepo)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Text != "the tests are slow" {
		t.Fatalf("memory under the canonical spelling = %+v, want the one written under the other", got)
	}
	stale, err := store.GetMemory(ctx, &shoutedRepo)
	if err != nil {
		t.Fatal(err)
	}
	if stale != nil {
		t.Fatalf("memory still under %s = %+v, want it gone", shoutedRepo, stale)
	}
	doc, err := store.GetMemoryDocument(ctx, &canonicalRepo, "deploys")
	if err != nil {
		t.Fatal(err)
	}
	if doc == nil || doc.Text != "push the tag" {
		t.Fatalf("document under the canonical spelling = %+v, want the one written under the other", doc)
	}
	wide, err := store.GetMemory(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if wide == nil || wide.Text != "deployment-wide" {
		t.Fatalf("the deployment's own memory = %+v, want it untouched", wide)
	}
}

// Two memories for one repository are two accumulations of prose, and
// the older is not a stale copy of the newer: whichever this dropped, it
// would be deleting the only record of whatever that side alone
// remembered. So it refuses, with both texts still there to be read.
func TestRepoCaseRefusesTwoDifferentMemories(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutMemory(ctx, model.Memory{Repo: &canonicalRepo, Text: "the tests are slow"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMemory(ctx, model.Memory{Repo: &shoutedRepo, Text: "the deploy needs a tag"}); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if !errors.Is(err, model.ErrRepoCaseConflict) {
		t.Fatalf("applying = %v, want ErrRepoCaseConflict", err)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Table != "memory" {
		t.Fatalf("conflicts = %+v, want the one memory row", plan.Conflicts)
	}
	for _, repo := range []model.RepoRef{canonicalRepo, shoutedRepo} {
		got, err := store.GetMemory(ctx, &repo)
		if err != nil || got == nil {
			t.Fatalf("memory for %s = %+v (%v), want it exactly as it was", repo, got, err)
		}
	}
}

// The failure the whole exercise is about: a repo's own defaults are a
// row keyed (owner, name), and a row under the other spelling is a row
// no task will ever read.
func TestRepoCaseMovesARepoConfigRowNothingWasReading(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: shoutedRepo, DefaultCapabilities: []string{"gcp-key"},
		PromptExtension: "run make deps first",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyRepoCase(ctx, deployment); err != nil {
		t.Fatalf("applying: %v", err)
	}
	got, err := store.GetRepoConfig(ctx, canonicalRepo)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !reflect.DeepEqual(got.DefaultCapabilities, []string{"gcp-key"}) ||
		got.PromptExtension != "run make deps first" {
		t.Fatalf("config under the canonical spelling = %+v, want the row written under the other one", got)
	}
	stale, err := store.GetRepoConfig(ctx, shoutedRepo)
	if err != nil {
		t.Fatal(err)
	}
	if stale != nil {
		t.Fatalf("config still under %s = %+v, want it gone", shoutedRepo, stale)
	}
}

// Two repo_config rows for one repository: the capability sets are
// unioned (the composition rule that field already has with the
// deployment-wide default), and text only one side sets is taken from
// whichever side set it.
func TestRepoCaseUnionsTwoRepoConfigRows(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: canonicalRepo, DefaultCapabilities: []string{"gcp-key"},
		SetupCommand: "make deps",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: shoutedRepo, DefaultCapabilities: []string{"gcp-key", "github-sandbox"},
		PromptExtension: "the tests are slow; be patient",
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if err != nil {
		t.Fatalf("applying: %v", err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Merged != 1 || plan.Changes[0].Rewritten != 0 {
		t.Fatalf("plan = %+v, want one merged row and nothing rewritten", plan)
	}
	got, err := store.GetRepoConfig(ctx, canonicalRepo)
	if err != nil {
		t.Fatal(err)
	}
	want := model.RepoConfig{
		Repo:                canonicalRepo,
		DefaultCapabilities: []string{"gcp-key", "github-sandbox"},
		PromptExtension:     "the tests are slow; be patient",
		SetupCommand:        "make deps",
	}
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("merged config = %+v, want %+v", got, want)
	}
}

// Two setup commands for one repository is not a merge anybody can do
// mechanically: it is reported, and nothing at all is written -- not
// even the rows that would have been fine.
func TestRepoCaseRefusesTwoDifferentSetupCommands(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, taskIn("a1b2", shoutedRepo)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: canonicalRepo, SetupCommand: "make deps"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: shoutedRepo, SetupCommand: "npm ci"}); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if !errors.Is(err, model.ErrRepoCaseConflict) {
		t.Fatalf("applying = %v, want ErrRepoCaseConflict", err)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Table != "repo_config" {
		t.Fatalf("conflicts = %+v, want the one repo_config row", plan.Conflicts)
	}
	if got := targetOf(t, ctx, store, "a1b2"); got != shoutedRepo {
		t.Fatalf("target = %s, want it untouched: a refused reconciliation writes nothing at all", got)
	}
	both, err := store.GetRepoConfig(ctx, shoutedRepo)
	if err != nil || both == nil || both.SetupCommand != "npm ci" {
		t.Fatalf("the divergent config row = %+v (%v), want it exactly as it was", both, err)
	}
}

// A qualification plan under the other spelling moves wholesale --
// config row, items and the dependency edges between them.
func TestRepoCaseMovesAQualificationPlan(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	plan := model.QualificationPlan{
		Repo: shoutedRepo, Configured: true, RequireApproval: true,
		Items: []model.QualificationItem{
			{TemplateID: "tmpl-1", Repeat: 2},
			{TemplateID: "tmpl-2", Repeat: 1, DependsOn: []string{"tmpl-1"}},
		},
	}
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyRepoCase(ctx, deployment); err != nil {
		t.Fatalf("applying: %v", err)
	}
	got, err := store.GetQualificationPlan(ctx, canonicalRepo)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.RequireApproval || len(got.Items) != 2 {
		t.Fatalf("plan under the canonical spelling = %+v, want the one written under the other", got)
	}
	if got.Items[1].TemplateID != "tmpl-2" || !reflect.DeepEqual(got.Items[1].DependsOn, []string{"tmpl-1"}) {
		t.Fatalf("items = %+v, want the dependency edge carried over too", got.Items)
	}
	stale, err := store.GetQualificationPlan(ctx, shoutedRepo)
	if err != nil {
		t.Fatal(err)
	}
	if stale != nil && stale.Configured {
		t.Fatalf("plan still under %s = %+v, want it gone", shoutedRepo, stale)
	}
}

// A qualification_config row that says nothing -- both switches off,
// which is what no row at all says -- yields to the row that does.
func TestRepoCaseMergesASilentQualificationConfig(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutQualificationPlan(ctx, model.QualificationPlan{Repo: canonicalRepo}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutQualificationPlan(ctx, model.QualificationPlan{
		Repo: shoutedRepo, RequireApproval: true, AutoPromote: true,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyRepoCase(ctx, deployment); err != nil {
		t.Fatalf("applying: %v", err)
	}
	got, err := store.GetQualificationPlan(ctx, canonicalRepo)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.RequireApproval || !got.AutoPromote {
		t.Fatalf("merged plan = %+v, want the switches the other spelling set", got)
	}
}

// Two rows that both say something, differently, are the operator's
// call: these are gates on whether a human sees a task and whether a
// candidate promotes itself, and grain has no basis for turning either
// on.
func TestRepoCaseRefusesDisagreeingQualificationSwitches(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutQualificationPlan(ctx, model.QualificationPlan{
		Repo: canonicalRepo, RequireApproval: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutQualificationPlan(ctx, model.QualificationPlan{
		Repo: shoutedRepo, AutoPromote: true,
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if !errors.Is(err, model.ErrRepoCaseConflict) {
		t.Fatalf("applying = %v, want ErrRepoCaseConflict", err)
	}
	if len(plan.Conflicts) == 0 || plan.Conflicts[0].Table != "qualification_config" {
		t.Fatalf("conflicts = %+v, want the qualification_config row", plan.Conflicts)
	}
}

// Releases and the candidates cut from them move together, and so do
// requested branches: everything the repo page reads for one repo.
func TestRepoCaseMovesReleasesCandidatesAndBranches(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	// CreateRelease cuts the release's first candidate itself, which is
	// the second table this has to carry across.
	if _, err := store.CreateRelease(ctx, shoutedRepo, "prod", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBranch(ctx, shoutedRepo, "spike", now); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyRepoCase(ctx, deployment); err != nil {
		t.Fatalf("applying: %v", err)
	}
	releases, err := store.ListReleases(ctx, canonicalRepo)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 || releases[0].Repo != canonicalRepo {
		t.Fatalf("releases = %+v, want the one release under the canonical spelling", releases)
	}
	candidates, err := store.ListCandidates(ctx, canonicalRepo, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v, want the candidate to have moved with its release", candidates)
	}
	branches, err := store.ListBranches(ctx, canonicalRepo)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0].Name != "spike" {
		t.Fatalf("branches = %+v, want the requested branch under the canonical spelling", branches)
	}
}

// Two releases of one name, one per spelling, are two separate records
// that GetRelease would afterwards have to choose between -- so this
// refuses rather than choosing.
func TestRepoCaseRefusesTwoReleasesOfOneName(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, err := store.CreateRelease(ctx, canonicalRepo, "prod", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRelease(ctx, shoutedRepo, "prod", now); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if !errors.Is(err, model.ErrRepoCaseConflict) {
		t.Fatalf("applying = %v, want ErrRepoCaseConflict", err)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Table != "release" || plan.Conflicts[0].Row != "prod" {
		t.Fatalf("conflicts = %+v, want the release named prod", plan.Conflicts)
	}
}

// A store with nothing divergent in it -- what a deployment installed
// after grain/task-32 looks like -- reports nothing and, since applying
// is the same code path, changes nothing.
func TestRepoCaseIsANoOpOnAHealthyStore(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutTask(ctx, taskIn("a1b2", canonicalRepo, canonicalRepo)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: canonicalRepo, DefaultCapabilities: []string{"gcp-key"},
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := store.ApplyRepoCase(ctx, deployment)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Fatalf("plan = %+v, want nothing to do", plan)
	}
	if n := countRows(t, ctx, db, "SELECT COUNT(*) FROM `task_read`"); n != 1 {
		t.Fatalf("task_read rows = %d, want the one that was there", n)
	}
}

// Every table repoCaseTables names has to exist, and so does every
// column, or a typo is a runtime error on an operator's own database
// rather than a failing test here. Running the whole reconciliation
// against an empty store touches every one of them.
//
// This says nothing about the tables the list *leaves out*, which is a
// different question and the one below asks it.
func TestRepoCaseQueriesEveryTableItNames(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	plan, err := store.PlanRepoCase(ctx, deployment)
	if err != nil {
		t.Fatalf("planning against an empty store: %v", err)
	}
	if !plan.Empty() {
		t.Fatalf("plan = %+v, want nothing: there is nothing in this store", plan)
	}
}

// repoKeyColumns are the three column pairs schema.go writes a
// repository in: (owner, repo) on the release machinery, (owner, name)
// wherever a repo is the key itself, and (target_owner, target_name) on
// the rows that carry a repo somebody chose to work in. A table with any
// of these pairs holds a repository, and a repository grain stores is a
// repository this reconciliation has to be able to respell.
var repoKeyColumns = [][2]string{
	{"owner", "repo"},
	{"owner", "name"},
	{"target_owner", "target_name"},
}

// The claim repoCaseTables' own doc comment makes -- "every repo-keyed
// table in schema.go is here" -- checked against the schema rather than
// against the list's own reading of itself.
//
// It is worth a test of its own because the failure it catches is
// silent and the tool's whole job is to be believed: a repo-keyed table
// nobody adds to the list is left in the old spelling by -apply, is
// never mentioned in the report, and a second run then tells the
// operator their store is clean while it is not (grain/task-314, and
// docs/hand-runs/2026-09-07-repo-case.md for the six tables that were in
// exactly that state). Nothing about writing a new table reminds anybody
// to come here, so the schema has to do the reminding.
//
// The tables come from the store's own sqlite_master rather than from a
// list written down twice: Init applies model.Statements() and its
// migration rungs, so what an opened database holds is the schema
// itself.
func TestRepoCaseNamesEveryRepoKeyedTable(t *testing.T) {
	t.Parallel()
	_, db, ctx := openStore(t)
	named := model.RepoCaseTables()
	for _, table := range schemaTables(t, ctx, db) {
		cols := schemaColumns(t, ctx, db, table)
		pair, ok := repoKeyIn(cols)
		if !ok {
			continue
		}
		i := slices.IndexFunc(named, func(n model.RepoCaseTable) bool { return n.Name == table })
		if i < 0 {
			t.Errorf("`%s` holds a repository in (`%s`, `%s`) and model.repoCaseTables does not name it: "+
				"`grain repo-case` leaves its rows in the old spelling and does not say so",
				table, pair[0], pair[1])
			continue
		}
		if got := [2]string{named[i].Owner, named[i].Repo}; got != pair {
			t.Errorf("`%s` is reconciled on (`%s`, `%s`), but the schema spells its repository (`%s`, `%s`)",
				table, got[0], got[1], pair[0], pair[1])
		}
	}
	// And the other direction, which the reconciliation itself would only
	// fail on at runtime: a table on the list that the schema no longer
	// has.
	for _, n := range named {
		if !slices.Contains(schemaTables(t, ctx, db), n.Name) {
			t.Errorf("model.repoCaseTables names `%s`, which this schema has no such table for", n.Name)
		}
	}
}

func schemaTables(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		"SELECT `name` FROM `sqlite_master` WHERE `type` = 'table' AND `name` NOT LIKE 'sqlite_%' ORDER BY `name`")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func schemaColumns(t *testing.T, ctx context.Context, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT `name` FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatalf("reading the columns of %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// repoKeyIn is the pair of columns this table holds a repository in, if
// it holds one at all.
func repoKeyIn(cols []string) ([2]string, bool) {
	for _, pair := range repoKeyColumns {
		if slices.Contains(cols, pair[0]) && slices.Contains(cols, pair[1]) {
			return pair, true
		}
	}
	return [2]string{}, false
}

func targetOf(t *testing.T, ctx context.Context, store *model.Store, id string) model.RepoRef {
	t.Helper()
	task, err := store.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Target == nil {
		t.Fatalf("task %s has no target", id)
	}
	return *task.Target
}

func countRows(t *testing.T, ctx context.Context, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A guard on the driver, not on grain: the reconciliation scans an
// INTEGER column into a string to compare two rows' values, and the
// merge rules are only correct if that conversion is the identity a Go
// bool went in as.
func TestSQLiteScansAStoredBoolAsText(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE `t` (`flag` INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `t` (`flag`) VALUES (?)", true); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRowContext(ctx, "SELECT `flag` FROM `t`").Scan(&got); err != nil {
		t.Fatalf("scanning an INTEGER column as text: %v", err)
	}
	if got != "1" {
		t.Fatalf("stored true reads back as %q, want \"1\"", got)
	}
}
