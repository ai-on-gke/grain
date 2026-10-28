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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

var (
	now   = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	human = model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}
	bot   = model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}
)

func openStore(t *testing.T) (*model.Store, *sql.DB, context.Context) {
	t.Helper()
	store, db := postgrestest.Open(t)
	return store, db, context.Background()
}

func task(id string, approved bool) model.Task {
	t := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "Rename the endpoint",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: human},
			Reason:      model.ReasonDirect,
		},
		Target:    &model.RepoRef{Owner: "owner", Name: "payments-api"},
		Binding:   model.BindingDirective,
		CreatedAt: &now,
	}
	if approved {
		t.Approval = &model.Attribution{Actor: human}
	}
	return t
}

func TestSchemaAppliesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	// Init runs on every start; a second one must be a no-op, not an error.
	if err := store.Init(ctx); err != nil {
		t.Fatalf("re-initialising: %v", err)
	}
}

func TestPutBoardTreatsZeroAsAKeyInANumberedChildSet(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	want := model.Board{
		ID: "board-1", Name: "Ordered children", Kind: model.BoardPanels,
		Panels: []model.BoardPanel{
			{Kind: model.PanelList, Title: "Before zero", OrderKey: -10},
			{Kind: model.PanelStates, Title: "Zero", OrderKey: 0},
			{Kind: model.PanelGantt, Title: "After zero", OrderKey: 10},
		},
	}
	if err := store.PutBoard(ctx, want); err != nil {
		t.Fatalf("PutBoard: %v", err)
	}
	got, err := store.GetBoard(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if !reflect.DeepEqual(got.Panels, want.Panels) {
		t.Errorf("panels = %+v, want %+v", got.Panels, want.Panels)
	}

	want.Kind = model.BoardReport
	want.Panels = nil
	want.Sections = []model.BoardSection{
		{Kind: model.SectionThroughput, Title: "Before zero", OrderKey: -10},
		{Kind: model.SectionLatency, Title: "Zero", OrderKey: 0},
		{Kind: model.SectionRuns, Title: "After zero", OrderKey: 10},
	}
	if err := store.PutBoard(ctx, want); err != nil {
		t.Fatalf("PutBoard with sections: %v", err)
	}
	got, err = store.GetBoard(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetBoard with sections: %v", err)
	}
	if !reflect.DeepEqual(got.Sections, want.Sections) {
		t.Errorf("sections = %+v, want %+v", got.Sections, want.Sections)
	}
}

func TestPutBoardNumbersWhollyUnnumberedChildSetsByPosition(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	want := model.Board{
		ID: "board-1", Name: "Unnumbered children", Kind: model.BoardPanels,
		Panels: []model.BoardPanel{{Kind: model.PanelList}, {Kind: model.PanelStates}},
	}
	if err := store.PutBoard(ctx, want); err != nil {
		t.Fatalf("PutBoard: %v", err)
	}
	got, err := store.GetBoard(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	keys := []float64{got.Panels[0].OrderKey, got.Panels[1].OrderKey}
	if !reflect.DeepEqual(keys, []float64{0, 1}) {
		t.Errorf("panel keys = %v, want [0 1]", keys)
	}
}

func TestTaskRoundTripsWithEveryCollection(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	folder := model.FolderRef{Path: []string{"payments", "services/billing"}}
	want := task("a1b2", true)
	want.Body = "some body"
	want.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	want.Writes = []model.WriteTarget{{Repo: model.RepoRef{Owner: "owner", Name: "deploy-config"}, Base: "deploy"}}
	want.Grants = []model.Grant{{Capability: "gemini-key", Via: model.GrantByFolder, Folder: &folder}}
	want.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "c3d4"}}
	want.Tags = []string{"nightly"}
	want.AutoMerge = true
	want.Interactive = true
	deferredAt := now.Add(time.Hour)
	want.DeferredAt = &deferredAt

	if err := store.PutTask(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.Title != want.Title || got.Body != want.Body {
		t.Errorf("text did not survive: %q / %q", got.Title, got.Body)
	}
	if len(got.Reads) != 1 || got.Reads[0].Name != "shared-lib" {
		t.Errorf("reads: %+v", got.Reads)
	}
	if len(got.Writes) != 1 || got.Writes[0].Repo.Name != "deploy-config" || got.Writes[0].Base != "deploy" {
		t.Errorf("writes: %+v", got.Writes)
	}
	if len(got.Grants) != 1 || got.Grants[0].Via != model.GrantByFolder {
		t.Errorf("grants: %+v", got.Grants)
	}
	if got.Grants[0].Folder == nil || got.Grants[0].Folder.String() != folder.String() {
		t.Errorf("grant folder: %+v", got.Grants[0].Folder)
	}
	if len(got.Links) != 1 || !got.Links[0].Kind.Blocks() {
		t.Errorf("links: %+v", got.Links)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "nightly" {
		t.Errorf("tags: %+v", got.Tags)
	}
	if got.Approval == nil || got.Approval.Actor.Kind != model.PrincipalHuman {
		t.Errorf("approval: %+v", got.Approval)
	}
	if !got.AutoMerge || !got.Interactive {
		t.Errorf("flags: %+v", got)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(now) {
		t.Errorf("created_at did not survive: %+v, want %v", got.CreatedAt, now)
	}
	if got.DeferredAt == nil || !got.DeferredAt.Equal(deferredAt) {
		t.Errorf("deferred_at did not survive: %+v, want %v", got.DeferredAt, deferredAt)
	}
}

func TestPutTaskReplacesChildRowsRatherThanAccumulating(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("a1b2", true)
	tk.Tags = []string{"one", "two"}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	tk.Tags = []string{"three"}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetTask(ctx, "a1b2")
	if len(got.Tags) != 1 || got.Tags[0] != "three" {
		t.Errorf("tags after replace: %+v", got.Tags)
	}
}

func TestDanglingBlockerDoesNotFailOpen(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	blocked := task("a1b2", true)
	blocked.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "non-existent-task"}}
	if err := store.PutTask(ctx, blocked); err != nil {
		t.Fatal(err)
	}

	if n, _ := store.OpenBlockers(ctx, "a1b2"); n != 1 {
		t.Fatalf("open blockers for dangling blocker = %d, want 1 (must fail closed)", n)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready = %v, want empty because task has an unresolvable open blocker", ready)
	}
}

func TestWorksInRepoAnswersFromTheAllowlistOrFromWhereWorkHasLanded(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	targeted := model.RepoRef{Owner: "owner", Name: "payments-api"}
	declared := model.RepoRef{Owner: "owner", Name: "payments-state"}
	configured := model.RepoRef{Owner: "owner", Name: "docs"}
	stranger := model.RepoRef{Owner: "owner", Name: "nowhere"}

	tk := task("a1b2", true)
	tk.Writes = []model.WriteTarget{{Repo: declared, Base: "deploy"}}
	tk.Reads = []model.RepoRef{{Owner: "owner", Name: "read-only"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: configured, DefaultCapabilities: []string{"gcp-key"},
	}); err != nil {
		t.Fatal(err)
	}

	unrestricted := map[model.RepoRef]bool{
		targeted:                               true,
		declared:                               true,
		configured:                             true,
		{Owner: "owner", Name: "read-only"}:    false,
		stranger:                               false,
		{Owner: "OWNER", Name: "Payments-API"}: true,
	}
	for repo, want := range unrestricted {
		got, err := store.WorksInRepo(ctx, repo)
		if err != nil {
			t.Fatalf("WorksInRepo(%s): %v", repo, err)
		}
		if got != want {
			t.Errorf("WorksInRepo(%s) = %v with no allowlist, want %v", repo, got, want)
		}
	}

}

func TestWorkReposListsExactlyWhatWorksInRepoAdmits(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	declared := model.RepoRef{Owner: "owner", Name: "payments-state"}
	configured := model.RepoRef{Owner: "owner", Name: "docs"}

	tk := task("a1b2", true)
	tk.Writes = []model.WriteTarget{{Repo: declared, Base: "deploy"}}
	tk.Reads = []model.RepoRef{{Owner: "owner", Name: "read-only"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	recent := task("c3d4", true)
	recent.Target = &model.RepoRef{Owner: "OWNER", Name: "Billing"}
	recent.CreatedAt = &later
	if err := store.PutTask(ctx, recent); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: configured, DefaultCapabilities: []string{"gcp-key"},
	}); err != nil {
		t.Fatal(err)
	}

	repos, err := store.WorkRepos(ctx, 0)
	if err != nil {
		t.Fatalf("WorkRepos: %v", err)
	}
	want := []string{"OWNER/Billing", "owner/payments-api", "owner/payments-state", "owner/docs"}
	var got []string
	for _, repo := range repos {
		got = append(got, repo.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorkRepos with no allowlist = %v, want %v", got, want)
	}
	for _, repo := range repos {
		known, err := store.WorksInRepo(ctx, repo)
		if err != nil {
			t.Fatalf("WorksInRepo(%s): %v", repo, err)
		}
		if !known {
			t.Errorf("WorkRepos offered %s, which WorksInRepo refuses", repo)
		}
	}
	for _, repo := range got {
		if repo == "owner/read-only" {
			t.Errorf("WorkRepos offered a read-only repo: %v", got)
		}
	}

	capped, err := store.WorkRepos(ctx, 2)
	if err != nil {
		t.Fatalf("WorkRepos(2): %v", err)
	}
	if len(capped) != 2 || capped[0].String() != want[0] || capped[1].String() != want[1] {
		t.Errorf("WorkRepos(2) = %v, want the first two of %v", capped, want)
	}

}

func TestGetTaskReturnsNilWhenAbsent(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	got, err := store.GetTask(ctx, "nope")
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil), got (%v, %v)", got, err)
	}
}

func TestUntrustedTextIsStoredNotInterpreted(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("a1b2", true)
	tk.Title = "'; DROP TABLE \"task\"; --"
	tk.Body = "\\'; DELETE FROM \"task_run\"; -- \x00 🤖"
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("the table should still exist: %v", err)
	}
	if got.Title != tk.Title {
		t.Errorf("title round-trip: %q", got.Title)
	}
}

func TestStateIsDerivedThroughEveryTransition(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", false)); err != nil {
		t.Fatal(err)
	}
	assertState := func(want model.State) {
		t.Helper()
		got, err := store.State(ctx, "a1b2")
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		if got != want {
			t.Fatalf("state = %q, want %q", got, want)
		}
	}
	assertState(model.StateProposed)

	if err := store.Approve(ctx, "a1b2", model.Attribution{Actor: bot, OnBehalfOf: &human}, now); err != nil {
		t.Fatal(err)
	}
	assertState(model.StateQueued)

	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-1",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	assertState(model.StateRunning)

	if err := store.FinishRun(ctx, "r1", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	assertState(model.StateQueued)

	id := int64(99)
	if err := store.Observe(ctx, model.Observation{
		TaskID: "a1b2", PendingQuestionCommentID: &id,
	}); err != nil {
		t.Fatal(err)
	}
	assertState(model.StateAwaitingReply)

	done := now.Add(2 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	assertState(model.StateAwaitingReply)

	landed, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	landed.AutoMerge = true
	landed.Links = []model.Link{{Kind: model.LinkFixes, Target: "owner/payments-api#7"}}
	if err := store.PutTask(ctx, *landed); err != nil {
		t.Fatal(err)
	}
	assertState(model.StateCompleted)

	if err := store.Observe(ctx, model.Observation{
		TaskID: "a1b2", CompletedAt: &done, ClosedAt: &done,
	}); err != nil {
		t.Fatal(err)
	}
	assertState(model.StateClosed)
}

func TestAnUnsubmittedPullRequestIsItsOwnStateAndStillSynced(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("a1b2", true)
	tk.Links = []model.Link{{Kind: model.LinkFixes, Target: "owner/payments-api#7"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	done := now.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}

	got, err := store.State(ctx, "a1b2")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if got != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want %q", got, model.StateAwaitingSubmit)
	}

	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].PullRequest != "owner/payments-api#7" {
		t.Fatalf("OpenPullRequestLinks = %+v, want the unsubmitted pull request", links)
	}
}

func TestComments(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("task-1", true)); err != nil {
		t.Fatal(err)
	}

	c1, err := store.AddComment(ctx, model.Comment{
		TaskID: "task-1", Author: model.Attribution{Actor: human}, Body: "First comment", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if c1 <= 0 {
		t.Errorf("comment ID = %d, want > 0", c1)
	}

	comments, err := store.Comments(ctx, "task-1")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "First comment" {
		t.Errorf("comments = %+v", comments)
	}
}

func TestPostgresSequencesAllocation(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	boardID1, err := store.NewBoardID(ctx)
	if err != nil {
		t.Fatalf("NewBoardID failed on Postgres: %v", err)
	}
	if !strings.HasPrefix(boardID1, "board-") {
		t.Errorf("unexpected board ID format: %q", boardID1)
	}

	boardID2, err := store.NewBoardID(ctx)
	if err != nil {
		t.Fatalf("second NewBoardID failed on Postgres: %v", err)
	}
	if boardID1 == boardID2 {
		t.Errorf("expected unique board IDs, got %q twice", boardID1)
	}

	execTokID1, err := store.NewExecutorTokenID(ctx)
	if err != nil {
		t.Fatalf("NewExecutorTokenID failed on Postgres: %v", err)
	}
	if !strings.HasPrefix(execTokID1, "exec-tok-") {
		t.Errorf("unexpected executor token ID format: %q", execTokID1)
	}

	execTokID2, err := store.NewExecutorTokenID(ctx)
	if err != nil {
		t.Fatalf("second NewExecutorTokenID failed on Postgres: %v", err)
	}
	if execTokID1 == execTokID2 {
		t.Errorf("expected unique executor token IDs, got %q twice", execTokID1)
	}
}
