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

package sqlite_test

// The store against a real embedded SQLite database. Unlike the model
// tests, which check what grain generates, these prove SQLite accepts
// the DDL and that the views answer -- which is the thing that could not
// be verified at all while the store shelled out to a CLI that was not
// installed.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

var (
	now   = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	human = model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}
	bot   = model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}
)

func openStore(t testing.TB) (*model.Store, *sql.DB, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store, db, ctx
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

// The schema arrives whole or not at all: Init applies every statement
// in one transaction (Store.applySchema), so a database it refused is
// one it left exactly as it found it rather than one carrying half a
// schema.
//
// The refusal is arranged with a table sitting where a view belongs,
// which is a shape SQLite genuinely cannot reconcile: the DROP VIEW the
// schema emits for `task_state` will not drop a table of that name.
// That failure lands after the CREATE TABLEs -- Statements() puts the
// tables first -- so the tables are the evidence, and finding none of
// them afterwards is what says the whole batch rolled back.
func TestInitLeavesNothingBehindWhenTheSchemaCannotBeApplied(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE `task_state` (`x` INTEGER)"); err != nil {
		t.Fatalf("creating a table where a view belongs: %v", err)
	}

	store := model.New(db)
	if err := store.Init(ctx); err == nil {
		t.Fatal("Init accepted a database with a table where task_state's view belongs")
	}

	var tables int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `sqlite_master` WHERE `type` = 'table' AND `name` = 'task'").Scan(&tables); err != nil {
		t.Fatalf("asking what survived: %v", err)
	}
	if tables != 0 {
		t.Fatal("a refused Init left the task table behind: the schema was applied in pieces, not as one transaction")
	}
}

func TestTaskRoundTripsWithEveryCollection(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	folder := model.FolderRef{Path: []string{"payments", "services/billing"}}
	want := task("a1b2", true)
	want.Body = "some body"
	want.Folder = &folder
	want.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	want.Writes = []model.WriteTarget{{Repo: model.RepoRef{Owner: "owner", Name: "deploy-config"}, Base: "deploy"}}
	want.Grants = []model.Grant{{Capability: "gemini-key", Via: model.GrantByFolder, Folder: &folder}}
	want.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "c3d4"}}
	want.Tags = []string{"nightly"}
	want.AutoMerge = true
	want.Interactive = true
	// Deferred as well as approved, which is a real combination (a task
	// put aside while it was queued) and, more to the point, the one that
	// says PutTask carries the field at all: every edit to a task rewrites
	// the whole row (Store.UpdateTask), so a deferral this path dropped
	// would come undone the next time somebody fixed a typo in the title.
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
	// The branch as well as the repo: a write target without its base is
	// half a declaration, and the half that decides where a proposal for
	// that repo is filed (model.WriteTarget).
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
	// The row set equals the object, rather than being maintained.
	if len(got.Tags) != 1 || got.Tags[0] != "three" {
		t.Errorf("tags after replace: %+v", got.Tags)
	}
}

// WorksInRepo is the gate on a propose_task call naming a repository of
// its own (orchestrator.proposedTarget): a deployment that restricts its
// repos is answered by that list alone, and one that does not is
// answered by where it has already worked.
func TestWorksInRepoAnswersFromTheAllowlistOrFromWhereWorkHasLanded(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	targeted := model.RepoRef{Owner: "owner", Name: "payments-api"} // task()'s own target
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
		targeted:   true,
		declared:   true,
		configured: true,
		// A repository grain may read is not one it lands work in
		// (schema.go's task_read), so it is no evidence here.
		{Owner: "owner", Name: "read-only"}: false,
		stranger:                            false,
		// Case is GitHub's, not the store's (model.RepoRef.Same).
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

	// With an allowlist, that list is the whole answer: a repo off it is
	// one a task filed there would be parked for, whatever the tasks
	// already in the store say.

}

// WorkRepos is the same question read forwards -- the list a run is
// given so it can name one of these repositories in a propose_task call
// (pkg/cli/mcpserver.go's -propose-repo) -- so the property that
// matters is that the two agree: everything the list offers, the gate
// admits.
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
	// A second task, targeting another repository and filed later, so
	// the ordering below is a fact about recency rather than about which
	// row happened to be written first.
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
	// Most recent first, and a repository known only by a RepoConfig of
	// its own -- no task, no date -- last.
	want := []string{"OWNER/Billing", "owner/payments-api", "owner/payments-state", "owner/docs"}
	var got []string
	for _, repo := range repos {
		got = append(got, repo.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorkRepos with no allowlist = %v, want %v", got, want)
	}
	// The property the list exists for: proposedTarget puts every one of
	// these through WorksInRepo, and a list that named a repository the
	// gate refused would send runs after proposals grain then refiles.
	for _, repo := range repos {
		known, err := store.WorksInRepo(ctx, repo)
		if err != nil {
			t.Fatalf("WorksInRepo(%s): %v", repo, err)
		}
		if !known {
			t.Errorf("WorkRepos offered %s, which WorksInRepo refuses", repo)
		}
	}
	// A repo grain may only read is offered no more than it is admitted.
	for _, repo := range got {
		if repo == "owner/read-only" {
			t.Errorf("WorkRepos offered a read-only repo: %v", got)
		}
	}

	// The cap is the head of that list, not an arbitrary slice of it.
	capped, err := store.WorkRepos(ctx, 2)
	if err != nil {
		t.Fatalf("WorkRepos(2): %v", err)
	}
	if len(capped) != 2 || capped[0].String() != want[0] || capped[1].String() != want[1] {
		t.Errorf("WorkRepos(2) = %v, want the first two of %v", capped, want)
	}

	// With an allowlist, that list is the whole answer here too, in the
	// order it was written -- the same narrowing WorksInRepo makes.

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
	// The reason bind parameters were worth a language change: this is a
	// value, and there is no rendering step that could make it anything
	// else.
	store, _, ctx := openStore(t)
	tk := task("a1b2", true)
	tk.Title = "'; DROP TABLE `task`; --"
	tk.Body = "\\'; DELETE FROM `task_run`; -- \x00 🤖"
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

	// Approval is the whole difference between proposed and queued.
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
	// This task carries no fixes-link, so the run that finished pushed
	// nothing: its completion is 'awaiting_reply', an answer waiting on
	// somebody reading it rather than a merge waiting on a queue.
	assertState(model.StateAwaitingReply)

	// Give it the pull request it never had, submitted, and the very same
	// completed_at reads 'completed'. That is the whole of the difference
	// between the two, proved against the view rather than only against
	// model.StateOf.
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
	// Precedence: closed outranks completed.
	assertState(model.StateClosed)
}

// TestAnUnsubmittedPullRequestIsItsOwnStateAndStillSynced proves the
// half of task_state's CASE that reads something other than
// task_observation -- the task's own auto_merge column and its fixes-link
// -- against a real database rather than only against model.StateOf, and
// pins the query that keeps such a task's pull request under grain's
// watch while it waits.
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

	assertState := func(want model.State) {
		t.Helper()
		got, err := store.State(ctx, "a1b2")
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		if got != want {
			t.Fatalf("state = %q, want %q", got, want)
		}
		stored, err := store.GetTask(ctx, "a1b2")
		if err != nil {
			t.Fatal(err)
		}
		obs, err := store.GetObservation(ctx, "a1b2")
		if err != nil {
			t.Fatal(err)
		}
		// The two derivations of one rule, held together the way
		// state_test.go holds the precedence: the view and StateOf must
		// answer the same on the condition only one of them can see.
		if derived := model.StateOf(*stored, obs, false, 0); derived != got {
			t.Fatalf("task_state view = %q, model.StateOf = %q", got, derived)
		}
	}
	assertState(model.StateAwaitingSubmit)

	// A pull request waiting on a human is still watched: SyncPullRequests
	// has to see a hand-merge on GitHub, or the task would never close.
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].PullRequest != "owner/payments-api#7" {
		t.Fatalf("OpenPullRequestLinks = %+v, want the unsubmitted pull request", links)
	}

	// Submit is AutoMerge (ui.Client.Submit), and it is the only way out.
	tk.AutoMerge = true
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	assertState(model.StateCompleted)
	if links, err := store.OpenPullRequestLinks(ctx); err != nil || len(links) != 1 {
		t.Fatalf("OpenPullRequestLinks after submitting = %+v (err %v), want it still watched", links, err)
	}

	// And closing outranks both, which is what drops it out of the sync.
	if err := store.Observe(ctx, model.Observation{
		TaskID: "a1b2", CompletedAt: &done, ClosedAt: &done,
	}); err != nil {
		t.Fatal(err)
	}
	assertState(model.StateClosed)
	if links, err := store.OpenPullRequestLinks(ctx); err != nil || len(links) != 0 {
		t.Fatalf("OpenPullRequestLinks after closing = %+v (err %v), want none", links, err)
	}
}

// The one finished task with no pull request that is not waiting on a
// reader: a review grain filed itself, whose findings are delivered on
// the change under review and not here. model.AwaitsAck says so in Go;
// this proves the view's own origin_reason column says it too, because
// such a review declares IntentImplement and is recognisable by nothing
// else -- and that the view, like AwaitsAck, no longer reads the intent,
// so a task that merely declares itself a review is the ordinary
// answered-in-words case.
func TestAReviewWithNothingToLandStillReadsCompleted(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	done := now.Add(time.Hour)

	stateOfReview := func(id string, prepare func(*model.Task)) model.State {
		t.Helper()
		tk := task(id, true)
		prepare(&tk)
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if err := store.Observe(ctx, model.Observation{TaskID: id, CompletedAt: &done}); err != nil {
			t.Fatal(err)
		}
		got, err := store.State(ctx, id)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		stored, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if derived := model.StateOf(*stored, obs, false, 0); derived != got {
			t.Fatalf("task_state view = %q, model.StateOf = %q", got, derived)
		}
		return got
	}

	if got := stateOfReview("c3d4", func(tk *model.Task) {
		tk.Origin.Reason = model.ReasonReview
	}); got != model.StateCompleted {
		t.Errorf("a review grain filed itself = %q, want %q", got, model.StateCompleted)
	}
	// A task that declared itself a review and is not one of grain's has
	// nowhere its findings could have been delivered but its own
	// conversation, so it waits on a reader like any other task that
	// answered in words.
	if got := stateOfReview("a1b2", func(tk *model.Task) {
		tk.Intent = model.IntentReview
	}); got != model.StateAwaitingReply {
		t.Errorf("a task that declared itself a review = %q, want %q", got, model.StateAwaitingReply)
	}
	// And the contrast, on the same shape otherwise: an ordinary task
	// that finished with nothing to land is the one the reader is waiting
	// on.
	if got := stateOfReview("e5f6", func(*model.Task) {}); got != model.StateAwaitingReply {
		t.Errorf("an ordinary task with nothing to land = %q, want %q", got, model.StateAwaitingReply)
	}
}

// A task the merge queue has sent back to repair its own pull request
// branch (orchestrator.requeueForRepair) has no completed_at at all --
// it reads 'queued' or 'running' like any other attempt -- and yet its
// pull request is exactly the one the queue is in the middle of driving.
// Dropping it would hand its queue position to whatever is behind it and
// leave the repair running with nothing watching the deadline it runs
// against, so OpenPullRequestLinks keeps returning it until it completes
// or closes.
func TestOpenPullRequestLinksKeepsWatchingATaskBeingRepaired(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("a1b2", true)
	tk.AutoMerge = true
	tk.Links = []model.Link{{Kind: model.LinkFixes, Target: "owner/payments-api#7"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	done := now
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if links, err := store.OpenPullRequestLinks(ctx); err != nil || len(links) != 1 {
		t.Fatalf("OpenPullRequestLinks for a completed task = %+v (err %v), want one", links, err)
	}

	// The requeue: completed_at cleared, merge_queue_repair_at written.
	asked := now.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", MergeQueueRepairAt: &asked}); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, "a1b2"); err != nil || st != model.StateQueued {
		t.Fatalf("state = %q (%v), want queued: clearing completed_at is what requeues it", st, err)
	}
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].PullRequest != "owner/payments-api#7" {
		t.Fatalf("OpenPullRequestLinks while being repaired = %+v, want the pull request still watched", links)
	}
	// Once per task, however the two arms of the WHERE overlap.
	repairing, err := store.MergeQueueRepairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !repairing["a1b2"] {
		t.Fatalf("MergeQueueRepairing = %+v, want a1b2", repairing)
	}

	// Closing still outranks it: a task nobody is watching any more.
	closed := asked.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID: "a1b2", MergeQueueRepairAt: &asked, ClosedAt: &closed,
	}); err != nil {
		t.Fatal(err)
	}
	if links, err := store.OpenPullRequestLinks(ctx); err != nil || len(links) != 0 {
		t.Fatalf("OpenPullRequestLinks after closing = %+v (err %v), want none", links, err)
	}
	if repairing, err := store.MergeQueueRepairing(ctx); err != nil || repairing["a1b2"] {
		t.Fatalf("MergeQueueRepairing after closing = %+v (err %v), want none", repairing, err)
	}
}

// Withdrawing approval is the one way a task moves back up the
// precedence order rather than down it, so it is worth proving against a
// real database rather than only against StateOf: the state comes out of
// task_state's own CASE on approval_actor_kind, and dispatch stops
// seeing the task because task_ready reads that view.
func TestWithdrawingApprovalReturnsATaskToTheProposalsAndOutOfReady(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", false)); err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(ctx, "a1b2", model.Attribution{Actor: human}, now); err != nil {
		t.Fatal(err)
	}
	if ready, _ := store.Ready(ctx); len(ready) != 1 || ready[0] != "a1b2" {
		t.Fatalf("ready after approval = %v, want [a1b2]", ready)
	}

	if err := store.WithdrawApproval(ctx, "a1b2"); err != nil {
		t.Fatalf("withdrawing approval: %v", err)
	}
	if state, err := store.State(ctx, "a1b2"); err != nil || state != model.StateProposed {
		t.Fatalf("state = %q (err %v), want proposed", state, err)
	}
	if ready, _ := store.Ready(ctx); len(ready) != 0 {
		t.Fatalf("ready after withdrawal = %v, want nothing dispatchable", ready)
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Approval != nil || got.ApprovedAt != nil {
		t.Fatalf("approval = %+v, approvedAt = %v, want both cleared", got.Approval, got.ApprovedAt)
	}

	// Approving again is the whole of re-queueing it, and the row comes
	// back with a live timestamp rather than the withdrawn one.
	later := now.Add(time.Hour)
	if err := store.Approve(ctx, "a1b2", model.Attribution{Actor: human}, later); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got.ApprovedAt == nil || !got.ApprovedAt.Equal(later) {
		t.Fatalf("approvedAt after re-approval = %v, want %v", got.ApprovedAt, later)
	}
	if ready, _ := store.Ready(ctx); len(ready) != 1 || ready[0] != "a1b2" {
		t.Fatalf("ready after re-approval = %v, want [a1b2]", ready)
	}
}

// TestDeferringTakesATaskOutOfReadyAndGivesItBackApproved is the store's
// own half of what model.TestDeferringLeavesApprovalAlone pins in Go: the
// view has to read a deferred task the same way StateOf does, dispatch
// has to stop offering it, and picking it back up has to return it to the
// queue without a second approval -- which is the whole reason
// task.deferred_at is a column beside the approval rather than a way of
// clearing it.
func TestDeferringTakesATaskOutOfReadyAndGivesItBackApproved(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if ready, _ := store.Ready(ctx); len(ready) != 1 || ready[0] != "a1b2" {
		t.Fatalf("ready before deferring = %v, want [a1b2]", ready)
	}

	if err := store.SetDeferred(ctx, "a1b2", &now, nil, now); err != nil {
		t.Fatalf("deferring: %v", err)
	}
	if state, err := store.State(ctx, "a1b2"); err != nil || state != model.StateDeferred {
		t.Fatalf("state = %q (err %v), want deferred", state, err)
	}
	if ready, _ := store.Ready(ctx); len(ready) != 0 {
		t.Fatalf("ready while deferred = %v, want nothing dispatchable", ready)
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeferredAt == nil || !got.DeferredAt.Equal(now) {
		t.Fatalf("deferredAt = %v, want %v", got.DeferredAt, now)
	}
	if got.Approval == nil {
		t.Fatal("deferring must leave the approval alone")
	}
	// Nothing has been picked back up yet, so nothing is stamped: the
	// stamp is the undeferral and not the deferral (model.Task.
	// UndeferredAt).
	if got.UndeferredAt != nil {
		t.Fatalf("undeferredAt while still deferred = %v, want nil", got.UndeferredAt)
	}

	pickedUp := now.Add(72 * time.Hour)
	if err := store.SetDeferred(ctx, "a1b2", nil, nil, pickedUp); err != nil {
		t.Fatalf("undeferring: %v", err)
	}
	if state, err := store.State(ctx, "a1b2"); err != nil || state != model.StateQueued {
		t.Fatalf("state after undeferring = %q (err %v), want queued", state, err)
	}
	if ready, _ := store.Ready(ctx); len(ready) != 1 || ready[0] != "a1b2" {
		t.Fatalf("ready after undeferring = %v, want [a1b2] with no second approval", ready)
	}
	got, err = store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeferredAt != nil {
		t.Fatalf("deferredAt after undeferring = %v, want nil", got.DeferredAt)
	}
	// And the return is stamped, which is the whole of what stops a task
	// picked back up today from reading as queued since it was approved
	// three days ago (model.StateTimes.Since).
	if got.UndeferredAt == nil || !got.UndeferredAt.Equal(pickedUp) {
		t.Fatalf("undeferredAt = %v, want the moment it was picked back up %v", got.UndeferredAt, pickedUp)
	}
	times, err := store.StateTimes(ctx)
	if err != nil {
		t.Fatalf("gathering state times: %v", err)
	}
	if since := times["a1b2"].Since(model.StateQueued); since == nil || !since.Equal(pickedUp) {
		t.Fatalf("queued since %v, want the undeferral %v and not the approval that outlived it", since, pickedUp)
	}

	// Undeferring a task that is not deferred picks nothing back up, so
	// it stamps nothing: the wait that is going on is the one that was
	// already going on, and a second press of a button that does nothing
	// must not restart its clock.
	if err := store.SetDeferred(ctx, "a1b2", nil, nil, pickedUp.Add(time.Hour)); err != nil {
		t.Fatalf("undeferring a task that is not deferred: %v", err)
	}
	got, err = store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got.UndeferredAt == nil || !got.UndeferredAt.Equal(pickedUp) {
		t.Fatalf("undeferredAt after a second undeferral = %v, want the first one's %v",
			got.UndeferredAt, pickedUp)
	}

	// Deferring again leaves the earlier return where it is -- it is a
	// moment that really happened, and model.Transitions puts it on the
	// timeline -- while the state goes back to being the whole of
	// deferred_at.
	again := pickedUp.Add(24 * time.Hour)
	if err := store.SetDeferred(ctx, "a1b2", &again, nil, again); err != nil {
		t.Fatalf("deferring a second time: %v", err)
	}
	got, err = store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got.UndeferredAt == nil || !got.UndeferredAt.Equal(pickedUp) {
		t.Fatalf("undeferredAt after deferring again = %v, want the return it made before that %v",
			got.UndeferredAt, pickedUp)
	}
	if state, err := store.State(ctx, "a1b2"); err != nil || state != model.StateDeferred {
		t.Fatalf("state after deferring again = %q (err %v), want deferred", state, err)
	}
}

// TestADeferredProposalComesBackAProposal is the same round trip from the
// other end of the approval: deferring is orthogonal to it, so a task
// nobody had approved is a proposal again when it is picked back up
// rather than something that has quietly acquired an approval.
func TestADeferredProposalComesBackAProposal(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", false)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeferred(ctx, "a1b2", &now, nil, now); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, "a1b2"); err != nil || state != model.StateDeferred {
		t.Fatalf("state = %q (err %v), want deferred", state, err)
	}
	if err := store.SetDeferred(ctx, "a1b2", nil, nil, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, "a1b2"); err != nil || state != model.StateProposed {
		t.Fatalf("state after undeferring = %q (err %v), want proposed", state, err)
	}
}

// A deferral that names a date (model.Task.DeferredUntil): the wake time
// survives the round trip through the row, DueDeferrals offers exactly
// the tasks whose moment has come, and undeferring takes the date away
// with the deferral rather than leaving it behind to fire at a task
// somebody has already picked up.
//
// The state is deliberately not part of any of that: a task with a wake
// time in the future and one with no wake time at all are both plain
// 'deferred', because the whole of the state is still deferred_at.
func TestAWakeTimeRidesWithTheDeferralAndComesDue(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for _, id := range []string{"a1b2", "c3d4", "e5f6"} {
		if err := store.PutTask(ctx, task(id, true)); err != nil {
			t.Fatal(err)
		}
	}
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	if err := store.SetDeferred(ctx, "a1b2", &now, &past, now); err != nil {
		t.Fatalf("deferring until a moment that has passed: %v", err)
	}
	if err := store.SetDeferred(ctx, "c3d4", &now, &future, now); err != nil {
		t.Fatalf("deferring until later: %v", err)
	}
	if err := store.SetDeferred(ctx, "e5f6", &now, nil, now); err != nil {
		t.Fatalf("deferring indefinitely: %v", err)
	}

	got, err := store.GetTask(ctx, "c3d4")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeferredUntil == nil || !got.DeferredUntil.Equal(future) {
		t.Fatalf("deferredUntil = %v, want %v", got.DeferredUntil, future)
	}
	if state, err := store.State(ctx, "c3d4"); err != nil || state != model.StateDeferred {
		t.Fatalf("a task with a wake time still to come reads %q (err %v), want deferred", state, err)
	}

	due, err := store.DueDeferrals(ctx, now)
	if err != nil {
		t.Fatalf("listing due deferrals: %v", err)
	}
	if len(due) != 1 || due[0] != "a1b2" {
		t.Fatalf("due deferrals = %v, want [a1b2]: neither a wake time still to come nor a deferral "+
			"with no date on it is due", due)
	}
	// The boundary belongs to the wake: "not before" is satisfied at the
	// instant named, so the task may start then rather than a tick later.
	if due, err := store.DueDeferrals(ctx, future); err != nil || len(due) != 2 {
		t.Fatalf("due deferrals at the wake time itself = %v (err %v), want both dated deferrals", due, err)
	}

	if err := store.SetDeferred(ctx, "c3d4", nil, nil, now.Add(time.Minute)); err != nil {
		t.Fatalf("undeferring by hand: %v", err)
	}
	got, err = store.GetTask(ctx, "c3d4")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeferredUntil != nil {
		t.Fatalf("deferredUntil after undeferring = %v, want nil: a task picked up early must not "+
			"carry a date it would wake on again", got.DeferredUntil)
	}
	if due, err := store.DueDeferrals(ctx, future); err != nil || len(due) != 1 || due[0] != "a1b2" {
		t.Fatalf("due deferrals after undeferring = %v (err %v), want [a1b2] alone", due, err)
	}
}

func TestReadyExcludesBlockedAndUnblocksItself(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	blocker := task("c3d4", true)
	blocker.Title = "the blocker"
	blocked := task("a1b2", true)
	blocked.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "c3d4"}}
	if err := store.PutTask(ctx, blocker); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, blocked); err != nil {
		t.Fatal(err)
	}

	if n, _ := store.OpenBlockers(ctx, "a1b2"); n != 1 {
		t.Fatalf("open blockers = %d, want 1", n)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0] != "c3d4" {
		t.Fatalf("ready = %v, want [c3d4]", ready)
	}

	// Closing the dependency unblocks the task on the next read, with no
	// reply and nothing to re-apply — the reason this is re-evaluated
	// rather than pinned at dispatch.
	if err := store.Observe(ctx, model.Observation{TaskID: "c3d4", ClosedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenBlockers(ctx, "a1b2"); n != 0 {
		t.Fatalf("open blockers after close = %d, want 0", n)
	}
	ready, _ = store.Ready(ctx)
	if len(ready) != 1 || ready[0] != "a1b2" {
		t.Fatalf("ready after close = %v, want [a1b2]", ready)
	}
}

func TestANonTaskLinkTargetNeverBlocks(t *testing.T) {
	t.Parallel()
	// A link to a review thread or a pull request is not a task, so the
	// join finds nothing and it cannot block — a property of the schema
	// rather than a rule anybody wrote.
	store, _, ctx := openStore(t)
	tk := task("a1b2", true)
	tk.Links = []model.Link{{Kind: model.LinkAddresses, Target: "owner/repo#7:thread-3"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenBlockers(ctx, "a1b2"); n != 0 {
		t.Errorf("open blockers = %d, want 0", n)
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

func TestLeasesAreQueryableByMintingCredential(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	expires := now.Add(24 * time.Hour)
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "s1", Attempt: 1, StartedAt: now,
		Leases: []model.Lease{{
			Capability: "gemini-key", Resource: "projects/p/keys/k",
			MintedBy: model.CredentialRef{Name: "gcp-host-service-account"},
			IssuedAt: now, ExpiresAt: &expires,
		}},
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// "What would rotating this credential break?" — a query, where
	// nothing could answer it before.
	live, err := store.LiveLeases(ctx, "gcp-host-service-account")
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].TaskID != "a1b2" {
		t.Fatalf("live leases = %+v", live)
	}
	if none, _ := store.LiveLeases(ctx, "some-other-credential"); len(none) != 0 {
		t.Errorf("wrong credential returned %d leases", len(none))
	}

	// Finishing the run takes its leases out of the live view.
	if err := store.FinishRun(ctx, "r1", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if live, _ = store.LiveLeases(ctx, ""); len(live) != 0 {
		t.Errorf("a finished run still holds leases: %+v", live)
	}
}

func TestDroppingALeaseTwiceIsNotAnError(t *testing.T) {
	t.Parallel()
	// Release and the expiry reaper can both reach the same lease.
	store, _, ctx := openStore(t)
	for i := 0; i < 2; i++ {
		if err := store.DropLease(ctx, "r1", "gemini-key", "projects/p/keys/k"); err != nil {
			t.Fatalf("drop %d: %v", i, err)
		}
	}
}

func TestAttemptsCountsRuns(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"r1", "r2", "r3"} {
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: "a1b2", Sandbox: "s1",
			Attempt: i + 1, StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, now.Add(time.Hour), "requeued", ""); err != nil {
			t.Fatal(err)
		}
	}
	// Answerable because runs are rows; the records previously existed as
	// files that nothing aggregated.
	if n, err := store.Attempts(ctx, "a1b2"); err != nil || n != 3 {
		t.Fatalf("attempts = %d (%v), want 3", n, err)
	}
}

func TestRunsReturnsEveryAttemptOldestFirst(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	// Attempt 1 is given the id that sorts *last*, to prove Runs sorts by
	// attempt rather than by id. It cannot also be started last: a task
	// has at most one live run (schema.go's task_run_open_task), so the
	// earlier attempt has to have finished before the later one starts,
	// which is the order a real dispatch produces them in anyway.
	if err := store.StartRun(ctx, model.Run{
		ID: "r-second-by-id", TaskID: "a1b2", Sandbox: "s1",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r-second-by-id", now.Add(30*time.Minute), "failed", "build error"); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r-first-by-id", TaskID: "a1b2", Sandbox: "s2",
		Attempt: 2, StartedAt: now.Add(time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	// Attempt 2 is left unfinished, to prove a still-running attempt comes
	// back with a nil FinishedAt and empty Outcome rather than erroring.

	runs, err := store.Runs(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want 2", runs)
	}
	if runs[0].Attempt != 1 || runs[0].Outcome != "failed" || runs[0].Detail != "build error" {
		t.Fatalf("runs[0] = %+v, want attempt 1, failed, build error", runs[0])
	}
	if runs[0].FinishedAt == nil {
		t.Fatalf("runs[0].FinishedAt = nil, want the time FinishRun recorded")
	}
	if runs[1].Attempt != 2 || runs[1].Outcome != "" || runs[1].FinishedAt != nil {
		t.Fatalf("runs[1] = %+v, want attempt 2, still running", runs[1])
	}

	if none, err := store.Runs(ctx, "nonexistent"); err != nil || len(none) != 0 {
		t.Fatalf("runs for a nonexistent task = %+v (%v), want none", none, err)
	}
}

func TestRunTranscriptRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "s1",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// A started but not yet finished attempt exists, but has nothing
	// recorded for it yet.
	transcript, found, err := store.RunTranscript(ctx, "a1b2", 1)
	if err != nil || !found || transcript != "" {
		t.Fatalf("RunTranscript before FinishRun = (%q, %v, %v), want (\"\", true, nil)", transcript, found, err)
	}

	if err := store.FinishRun(ctx, "r1", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunTranscript(ctx, "r1", "> read_file(out.txt)\nPONG\n\nfound it"); err != nil {
		t.Fatal(err)
	}

	transcript, found, err = store.RunTranscript(ctx, "a1b2", 1)
	if err != nil || !found || transcript != "> read_file(out.txt)\nPONG\n\nfound it" {
		t.Fatalf("RunTranscript after SetRunTranscript = (%q, %v, %v)", transcript, found, err)
	}

	if _, found, err := store.RunTranscript(ctx, "a1b2", 2); err != nil || found {
		t.Fatalf("RunTranscript for a nonexistent attempt = (found %v, %v), want (false, nil)", found, err)
	}
	if _, found, err := store.RunTranscript(ctx, "nonexistent", 1); err != nil || found {
		t.Fatalf("RunTranscript for a nonexistent task = (found %v, %v), want (false, nil)", found, err)
	}
}

func TestGitScopeFollowsTheLiveRunOnASandbox(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("a1b2", true) // Target: owner/payments-api
	tk.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	// A second writeable repo is fetchable and nothing more: the run
	// pushes to its own target, and work over there is done by the task
	// it proposes for that repo (model.WriteTarget). So it belongs in the
	// list the proxy allows fetches from, beside the read-only ones.
	tk.Writes = []model.WriteTarget{{Repo: model.RepoRef{Owner: "owner", Name: "deploy-config"}, Base: "deploy"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	target, reads, writes, err := store.GitScope(ctx, "sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || target.String() != "owner/payments-api" {
		t.Errorf("target = %+v", target)
	}
	if len(reads) != 1 || reads[0].String() != "owner/shared-lib" {
		t.Errorf("reads = %+v, want the read-only repo", reads)
	}
	if len(writes) != 1 || writes[0].String() != "owner/deploy-config" {
		t.Errorf("writes = %+v, want the second write target", writes)
	}
}

// SandboxNamespaceScope is what /fs/ on the sandbox proxy authorizes
// against (pkg/gitproxy/namespace.go), so it is pinned here against the
// real store rather than only through gitproxy's fake: no grant reads no
// CI; grain-ci reads the run's own repositories; a repository-limited api
// grant adds that one repository; only self-debug reads src:// and only
// a grant for every repository reads all CI. A grant name that is not an
// api grant ParseAPIGrant accepts grants nothing, and an idle sandbox
// resolves to no scope at all.
func TestSandboxNamespaceScopeFollowsTheLiveRunsGrants(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	grants := func(names ...string) []model.Grant {
		var out []model.Grant
		for _, n := range names {
			out = append(out, model.Grant{Capability: n, Via: model.GrantByLabel})
		}
		return out
	}
	none := task("a1b2", true)
	grainCI := task("c3d4", true)
	grainCI.Grants = grants("grain-ci")
	grainCI.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	limited := task("e5f6", true)
	limited.Grants = grants("api:ci-read@other/repo")
	bogus := task("a7b8", true)
	bogus.Grants = grants("api:ci-read:other/repo", "api:ci:read:other/repo", "ci-read")
	debug := task("c9d0", true)
	debug.Grants = grants(model.SelfDebugCapability)
	wide := task("e1f2", true)
	wide.Grants = grants("api:ci-read")
	for i, tk := range []model.Task{none, grainCI, limited, bogus, debug, wide} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID: tk.ID + "-r1", TaskID: tk.ID, Sandbox: fmt.Sprintf("sandbox-%d", i),
			Attempt: 1, StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	type want struct {
		ci, allCI, src bool
		repos          []string
		notRepos       []string
	}
	for sandbox, w := range map[string]want{
		"sandbox-0": {notRepos: []string{"other/repo"}},
		"sandbox-1": {ci: true, repos: []string{"owner/payments-api", "owner/shared-lib"}, notRepos: []string{"other/repo"}},
		"sandbox-2": {ci: true, repos: []string{"other/repo"}},
		"sandbox-3": {notRepos: []string{"other/repo"}},
		"sandbox-4": {ci: true, allCI: true, src: true},
		"sandbox-5": {ci: true, allCI: true},
	} {
		scope, live, err := store.SandboxNamespaceScope(ctx, sandbox)
		if err != nil || !live {
			t.Fatalf("%s: SandboxNamespaceScope = (live %v, %v)", sandbox, live, err)
		}
		if scope.MayReadCI != w.ci || scope.MayReadAllCI != w.allCI || scope.MayReadSrc != w.src {
			t.Errorf("%s: scope = %+v, want ci=%v allCI=%v src=%v", sandbox, scope, w.ci, w.allCI, w.src)
		}
		for _, r := range w.repos {
			if !scope.AllowedRepos[r] {
				t.Errorf("%s: %s not in AllowedRepos %v", sandbox, r, scope.AllowedRepos)
			}
		}
		for _, r := range w.notRepos {
			if scope.AllowedRepos[r] {
				t.Errorf("%s: %s is in AllowedRepos %v, which nothing granted", sandbox, r, scope.AllowedRepos)
			}
		}
	}
	// self-debug opens the task, config, metrics and pipeline reads its
	// MCP tools give (and, through its api:self-debug preset's
	// deployment:read, health) -- and not memory, issues, SQL or the
	// grain log, each of which is a grant of its own.
	scope, _, err := store.SandboxNamespaceScope(ctx, "sandbox-4")
	if err != nil {
		t.Fatal(err)
	}
	if !scope.MayReadAllTasks || !scope.MayReadFullConfig || !scope.MayReadMetrics || !scope.MayReadPipeline {
		t.Errorf("self-debug: scope = %+v, want tasks, config, metrics and pipeline", scope)
	}
	if scope.MayReadAllMemory || scope.MayReadIssues || scope.MayReadDB || scope.MayReadGrainLog {
		t.Errorf("self-debug: scope = %+v, reaches a read another grant gates", scope)
	}
	if _, live, err := store.SandboxNamespaceScope(ctx, "sandbox-idle"); err != nil || live {
		t.Errorf("idle sandbox: (live %v, %v), want (false, nil)", live, err)
	}
}

// A scope set limiting ci:read to some repositories of its own, granted
// with no @repo, reads CI of those repositories alone through /fs/ --
// never every repository's, as an unlimited api:ci:read would. Not
// parallel: it installs the process's scope sets.
func TestSandboxNamespaceScopeKeepsAScopeSetsRepoLimit(t *testing.T) {
	if err := model.InstallAPIScopeSets([]model.APIScopeSet{
		{Name: "ops/ci", Scopes: []string{"ci:read"}, Repos: []string{"other/repo"}},
		{Name: "ops/tasks", Scopes: []string{"tasks:read"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })
	store, _, ctx := openStore(t)
	limited := task("a1b2", true)
	limited.Grants = []model.Grant{{Capability: "api:ops/ci", Via: model.GrantByLabel}}
	unrelated := task("c3d4", true)
	unrelated.Grants = []model.Grant{{Capability: "api:ops/tasks", Via: model.GrantByLabel}}
	for i, tk := range []model.Task{limited, unrelated} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID: tk.ID + "-r1", TaskID: tk.ID, Sandbox: fmt.Sprintf("sandbox-%d", i),
			Attempt: 1, StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	scope, live, err := store.SandboxNamespaceScope(ctx, "sandbox-0")
	if err != nil || !live {
		t.Fatalf("SandboxNamespaceScope = (live %v, %v)", live, err)
	}
	if !scope.MayReadCI || scope.MayReadAllCI || !scope.AllowedRepos["other/repo"] {
		t.Errorf("api:ops/ci: scope = %+v, want CI of other/repo only", scope)
	}
	scope, _, err = store.SandboxNamespaceScope(ctx, "sandbox-1")
	if err != nil || scope.MayReadCI || scope.MayReadAllCI {
		t.Errorf("api:ops/tasks: scope = %+v (%v), want no CI", scope, err)
	}
}

// The self-debug grant reaches no repository through the proxy, in
// either form: a self-debug run's grain-src/ is the deployment's baked-in
// copy, not a clone. A run that names grain in its reads reads it like
// any other repository, once -- and never writes it.
func TestGitScopeAddsNoReadForASelfDebugRun(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	grain := model.GrainSourceRepo()
	selfDebug := []model.Grant{{Capability: model.SelfDebugCapability, Via: model.GrantByLabel}}
	plain := task("a1b2", true)
	granted := task("c3d4", true)
	granted.Grants = selfDebug
	both := task("e5f6", true)
	both.Grants = selfDebug
	both.Reads = []model.RepoRef{grain}
	scoped := task("a7b8", true)
	scoped.Grants = selfDebug
	widened := task("c9d0", true)
	widened.Grants = selfDebug
	scopedReads := task("e1f2", true)
	scopedReads.Grants, scopedReads.Reads = selfDebug, []model.RepoRef{grain}
	for i, tk := range []model.Task{plain, granted, both, scoped, widened, scopedReads} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID: tk.ID + "-r1", TaskID: tk.ID, Sandbox: fmt.Sprintf("sandbox-%d", i),
			Attempt: 1, StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	for sandbox, want := range map[string][]model.RepoRef{
		"sandbox-0": nil,
		"sandbox-1": nil,
		"sandbox-2": {grain},
		"sandbox-3": nil,
		"sandbox-4": nil,
		"sandbox-5": {grain},
	} {
		_, reads, writes, err := store.GitScope(ctx, sandbox)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(reads, want) {
			t.Errorf("%s: reads = %+v, want %+v", sandbox, reads, want)
		}
		if len(writes) != 0 {
			t.Errorf("%s: writes = %+v, want none: self-debug never grants a push", sandbox, writes)
		}
	}
}

// The branch a run may push is its task's own: the one fixed onto the
// task when it was written, or grain/task-<id> for a task with none.
// A sandbox with no live run may push no branch at all.
func TestGitPushBranchIsTheLiveRunsTaskBranch(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	named := task("a1b2", true)
	named.Branch = "bot/work-a1b2"
	plain := task("c3d4", true)
	for i, tk := range []model.Task{named, plain} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID: tk.ID + "-r1", TaskID: tk.ID, Sandbox: fmt.Sprintf("sandbox-%d", i),
			Attempt: 1, StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	for sandbox, want := range map[string]string{"sandbox-0": "bot/work-a1b2", "sandbox-1": "grain/task-c3d4"} {
		branch, live, err := store.GitPushBranch(ctx, sandbox)
		if err != nil || !live || branch != want {
			t.Errorf("%s: (%q, %v, %v), want %q", sandbox, branch, live, err, want)
		}
	}
	if branch, live, err := store.GitPushBranch(ctx, "sandbox-idle"); err != nil || live || branch != "" {
		t.Errorf("a sandbox with no live run: (%q, %v, %v), want no branch", branch, live, err)
	}
}

// A review working on the reviewed change's own branch may only
// fast-forward it; every other run's own branch is its own business.
func TestGitPushFastForwardOnlyBindsAReviewOnTheReviewedBranch(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	review := task("e5f6", true)
	review.Branch = "grain/task-c3d4"
	review.Links = []model.Link{{Kind: model.LinkPushesReviewedBranch, Target: "c3d4"}}
	plain := task("c3d4", true)
	for i, tk := range []model.Task{review, plain} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID: tk.ID + "-r1", TaskID: tk.ID, Sandbox: fmt.Sprintf("sandbox-%d", i),
			Attempt: 1, StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	for sandbox, want := range map[string]bool{"sandbox-0": true, "sandbox-1": false, "sandbox-idle": false} {
		if bound, err := store.GitPushFastForwardOnly(ctx, sandbox); err != nil || bound != want {
			t.Errorf("%s: (%v, %v), want %v", sandbox, bound, err, want)
		}
	}
}

// The repo a run may push is the one it was dispatched against, not the
// one its task names at the moment of the push: a target edit made while
// the run is in flight (the task pane's Repos section, bulk edit's Repo
// row) reaches the task's next run and leaves this one alone. Without
// the pin SetRunSandbox writes, that edit refused the live run's own
// `git push` to the repo it had spent the run working in -- and granted
// it a push to one it had never checked out.
func TestGitScopeKeepsTheTargetTheRunWasDispatchedAgainst(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("a1b2", true) // Target: owner/payments-api
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunSandbox(ctx, "r1", "sandbox-0", tk.Target); err != nil {
		t.Fatal(err)
	}

	// The edit, mid-run.
	moved := tk
	moved.Target = &model.RepoRef{Owner: "owner", Name: "billing-api"}
	if err := store.PutTask(ctx, moved); err != nil {
		t.Fatal(err)
	}

	target, _, _, err := store.GitScope(ctx, "sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || target.String() != "owner/payments-api" {
		t.Errorf("target = %+v, want the repo the run was dispatched against", target)
	}
}

// A run dispatched for a task that had no target at all is pinned to
// nothing rather than left unpinned, so a target set on the task while
// it runs is not a repo the sandbox may suddenly push.
func TestGitScopeKeepsNoTargetForARunDispatchedWithout(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("a1b2", true)
	tk.Target = nil
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunSandbox(ctx, "r1", "sandbox-0", tk.Target); err != nil {
		t.Fatal(err)
	}

	targeted := tk
	targeted.Target = &model.RepoRef{Owner: "owner", Name: "billing-api"}
	if err := store.PutTask(ctx, targeted); err != nil {
		t.Fatal(err)
	}

	target, _, _, err := store.GitScope(ctx, "sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if target != nil {
		t.Errorf("target = %+v, want none: this run was dispatched with no repo to push", target)
	}
}

// A run whose sandbox was recorded before those columns existed carries
// no pin, and is answered out of its task exactly as every run was
// before -- which is what keeps a store upgraded under a live run
// working rather than refusing that run its push.
func TestGitScopeFallsBackToTheTaskForAnUnpinnedRun(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-0", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	// StartRun writes neither target column, so this is the pre-migration
	// row's shape; assert it rather than assume it, since the fallback
	// under test is only reached for a row that really is NULL there.
	var owner sql.NullString
	if err := db.QueryRowContext(ctx,
		"SELECT `target_owner` FROM `task_run` WHERE `id` = 'r1'").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Fatalf("a run recorded without SetRunSandbox should carry no pinned target, got %q", owner.String)
	}

	target, _, _, err := store.GitScope(ctx, "sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || target.String() != "owner/payments-api" {
		t.Errorf("target = %+v, want the task's own", target)
	}
}

func TestGitScopeIsEmptyWithNoLiveRunOnTheSandbox(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	target, reads, writes, err := store.GitScope(ctx, "sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if target != nil || len(reads) != 0 || len(writes) != 0 {
		t.Errorf("expected no scope for an idle sandbox, got target=%+v reads=%+v writes=%+v", target, reads, writes)
	}
}

func TestGitScopeStopsFollowingASandboxOnceItsRunFinishes(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	target, _, _, err := store.GitScope(ctx, "sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if target != nil {
		t.Errorf("a finished run should no longer scope its sandbox, got %+v", target)
	}
}

func TestGitCredentialOverrideFollowsTheLiveRunOnASandbox(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("a1b2", true)
	tk.Grants = []model.Grant{model.GitCredentialGrant("workflow")}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	name, ok, err := store.GitCredentialOverride(ctx, "sandbox-0", "owner", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "workflow" {
		t.Errorf("name=%q ok=%v, want %q true", name, ok, "workflow")
	}
}

func TestGitCredentialOverrideIsAbsentWithoutAGitCredentialGrant(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("a1b2", true)
	tk.Grants = []model.Grant{{Capability: "gemini-key", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// An unrelated grant names no credential, and a team with none of
	// its own has nothing to fall back to: no deployment ladder stands
	// behind a team's work (grain/task-1592).
	if _, ok, err := store.GitCredentialOverride(ctx, "sandbox-0", "owner", "repo"); !errors.Is(err, model.ErrNoTeamGitCredential) || ok {
		t.Errorf("ok=%v err=%v, want false, ErrNoTeamGitCredential -- an unrelated grant should not override", ok, err)
	}
}

func TestGitCredentialOverrideIsAbsentWithNoLiveRunOnTheSandbox(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, ok, err := store.GitCredentialOverride(ctx, "sandbox-0", "owner", "repo"); err != nil || ok {
		t.Errorf("ok=%v err=%v, want false, nil for an idle sandbox", ok, err)
	}
}

func TestGitCredentialOverrideResolvesTeamGitCredential(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	// The deployment's connections: a task resolves through them whatever its team.
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitCredential = "team-cred"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tk := task("a1b2", true)
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	name, ok, err := store.GitCredentialOverride(ctx, "sandbox-0", "owner", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "team-cred" {
		t.Errorf("name=%q ok=%v, want %q true", name, ok, "team-cred")
	}
}

func TestGitCredentialOverrideExplicitGrantPrecedesTeamCredential(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitCredential = "team-cred"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tk := task("a1b2", true)
	tk.Grants = []model.Grant{model.GitCredentialGrant("task-override")}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	name, ok, err := store.GitCredentialOverride(ctx, "sandbox-0", "owner", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "task-override" {
		t.Errorf("name=%q ok=%v, want %q true", name, ok, "task-override")
	}
}

func TestGitCredentialOverrideResolvesTeamRepoGitCredential(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitCredential = "team-cred"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:          model.RepoRef{Owner: "acme", Name: "widgets"},
		GitCredential: "team-repo-cred",
	}); err != nil {
		t.Fatal(err)
	}
	tk := task("a1b2", true)
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// Accessing acme/widgets uses team-repo credential
	name, ok, err := store.GitCredentialOverride(ctx, "sandbox-0", "acme", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "team-repo-cred" {
		t.Errorf("name=%q ok=%v, want %q true", name, ok, "team-repo-cred")
	}

	// Accessing acme/other falls back to team credential
	name, ok, err = store.GitCredentialOverride(ctx, "sandbox-0", "acme", "other")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "team-cred" {
		t.Errorf("name=%q ok=%v, want %q true", name, ok, "team-cred")
	}
}

func TestGitCredentialOverrideExplicitGrantPrecedesTeamRepoCredential(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitCredential = "team-cred"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:          model.RepoRef{Owner: "acme", Name: "widgets"},
		GitCredential: "team-repo-cred",
	}); err != nil {
		t.Fatal(err)
	}
	tk := task("a1b2", true)
	tk.Grants = []model.Grant{model.GitCredentialGrant("task-override")}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	name, ok, err := store.GitCredentialOverride(ctx, "sandbox-0", "acme", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != "task-override" {
		t.Errorf("name=%q ok=%v, want %q true", name, ok, "task-override")
	}
}

func TestObservationBaselinesRoundTrip(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	id := int64(12345)
	if err := store.Observe(ctx, model.Observation{
		TaskID: "a1b2", BaselineCommentID: &id, ObservedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetObservation(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("get observation: %v", err)
	}
	if got.BaselineCommentID == nil || *got.BaselineCommentID != 12345 {
		t.Errorf("baseline did not survive: %+v", got.BaselineCommentID)
	}
}

// TestPullRequestEventTimestampsRoundTrip covers PrOpenedAt/PrMergedAt/
// PrClosedAt (bwsalmon/agents#493) the same way TestObservationBaselinesRoundTrip
// already covers BaselineCommentID -- a plain round trip through Observe/
// GetObservation, since nothing about these three needs the derivation
// TestStateIsDerivedThroughEveryTransition exercises for ClosedAt.
func TestPullRequestEventTimestampsRoundTrip(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	opened := now.Add(-2 * time.Hour)
	merged := now
	if err := store.Observe(ctx, model.Observation{
		TaskID: "a1b2", PrOpenedAt: &opened, PrMergedAt: &merged, ObservedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetObservation(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("get observation: %v", err)
	}
	if got.PrOpenedAt == nil || !got.PrOpenedAt.Equal(opened) {
		t.Errorf("PrOpenedAt did not survive: %+v", got.PrOpenedAt)
	}
	if got.PrMergedAt == nil || !got.PrMergedAt.Equal(merged) {
		t.Errorf("PrMergedAt did not survive: %+v", got.PrMergedAt)
	}
	if got.PrClosedAt != nil {
		t.Errorf("expected PrClosedAt to stay nil, got %+v", got.PrClosedAt)
	}
}

// MergeQueueRefreshedAt is the merge queue's record that it has already
// merged a stale head's base into it once (orchestrator.refreshStaleHead),
// and it is only worth having because it is durable: losing it costs a
// repeated write to GitHub rather than another window of waiting, so a
// process that restarted in a loop would merge in a loop. That makes this
// round trip the property, not an incidental one.
func TestMergeQueueRefreshedAtRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	refreshed := now.Add(-time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID: "a1b2", MergeQueueRefreshedAt: &refreshed, ObservedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetObservation(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("get observation: %v", err)
	}
	if got.MergeQueueRefreshedAt == nil || !got.MergeQueueRefreshedAt.Equal(refreshed) {
		t.Errorf("MergeQueueRefreshedAt did not survive: %+v", got.MergeQueueRefreshedAt)
	}
	if got.MergeQueueBlockedAt != nil {
		t.Errorf("refreshing is not giving up: MergeQueueBlockedAt = %+v, want nil", got.MergeQueueBlockedAt)
	}
}

// --- task identity and the conversation --------------------------------

func TestGrainInstanceIDIsGeneratedOncePerStore(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	first, err := store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Fatalf("instance id = %q, want 128 random bits encoded as 32 hex characters", first)
	}
	if err := store.Init(ctx); err != nil {
		t.Fatalf("initializing the store again: %v", err)
	}
	second, err := store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("instance id changed across initialization: %q then %q", first, second)
	}

	other, _, otherCtx := openStore(t)
	otherID, err := other.InstanceID(otherCtx)
	if err != nil {
		t.Fatal(err)
	}
	if otherID == first {
		t.Fatalf("independent stores got the same instance id %q", first)
	}
}

func TestNewTasksFreezeTheConfiguredBranchName(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	cfg := model.DefaultConfig()
	cfg.TaskBranchTemplate = "alice/{instance}/task-{id}"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	instanceID, err := store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	created := task("42", true)
	if err := store.PutTask(ctx, created); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := "alice/" + instanceID + "/task-42"
	if got.BranchName() != want {
		t.Fatalf("branch = %q, want %q", got.BranchName(), want)
	}

	cfg.TaskBranchTemplate = "bob/task-{id}"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, *got); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BranchName() != want {
		t.Fatalf("branch changed with template: %q, want %q", after.BranchName(), want)
	}
}

// A deployment seeded from NewDeploymentConfig -- what the daemon, the
// first user and the first Settings save all store when there is no row
// yet --
// names its task branches grain/<deployment>/task-<id>-<slug> without anyone
// configuring a template (grain/task-3091), so two grains on one repo no
// longer share grain/task-N.
func TestNewDeploymentsNameTaskBranchesAfterTheirDeployment(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutConfig(ctx, model.NewDeploymentConfig()); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaskBranchTemplate != model.DefaultTaskBranchTemplate {
		t.Fatalf("seeded template = %q, want %q", cfg.TaskBranchTemplate, model.DefaultTaskBranchTemplate)
	}
	dep, err := store.DeploymentID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(dep) {
		t.Fatalf("default deployment id = %q, want 8 hex digits", dep)
	}
	if err := store.PutTask(ctx, task("12", true)); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, "12")
	if err != nil {
		t.Fatal(err)
	}
	// The task's title follows its number ({slug}, grain/task-56).
	want := "grain/" + dep + "/task-12-rename-the-endpoint"
	if got.BranchName() != want {
		t.Fatalf("branch = %q, want %q", got.BranchName(), want)
	}
	if id := model.NormalizeTaskID(got.BranchName()); id != "12" {
		t.Fatalf("NormalizeTaskID(%q) = %q, want 12", got.BranchName(), id)
	}
}

// A deployment whose stored row has the empty template it was written
// with before grain/task-3091 keeps naming branches grain/task-<id>, and
// opting in later renames only the tasks created afterwards: every task
// keeps the branch it was given, so open pull requests are not disturbed.
func TestExistingDeploymentsKeepTheirTaskBranchNames(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	// DefaultConfig, unlike NewDeploymentConfig, leaves the template
	// empty, as a row written before grain/task-3091 has it.
	cfg := model.DefaultConfig()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, task("5", true)); err != nil {
		t.Fatal(err)
	}
	old, err := store.GetTask(ctx, "5")
	if err != nil {
		t.Fatal(err)
	}
	if old.BranchName() != "grain/task-5" {
		t.Fatalf("branch = %q, want grain/task-5", old.BranchName())
	}

	cfg.TaskBranchTemplate = model.DefaultTaskBranchTemplate
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, *old); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, task("6", true)); err != nil {
		t.Fatal(err)
	}
	kept, err := store.GetTask(ctx, "5")
	if err != nil {
		t.Fatal(err)
	}
	if kept.BranchName() != "grain/task-5" {
		t.Fatalf("existing task's branch = %q, want grain/task-5", kept.BranchName())
	}
	dep, err := store.DeploymentID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fresh, err := store.GetTask(ctx, "6"); err != nil || fresh.BranchName() != "grain/"+dep+"/task-6-rename-the-endpoint" {
		t.Fatalf("new task's branch = %v, %v; want grain/%s/task-6-rename-the-endpoint", fresh, err, dep)
	}
}

func TestNewTaskBranchesCarryTheDeploymentID(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	instanceID, err := store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg := model.DefaultConfig()
	cfg.TaskBranchTemplate = "grain/{deployment}/task-{id}"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	// Until an admin sets one, the id is the random default drawn from
	// the installation id.
	dflt, err := store.DeploymentID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dflt != instanceID[:8] {
		t.Fatalf("default deployment id = %q, want %q", dflt, instanceID[:8])
	}
	first := task("7", true)
	if err := store.PutTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetTask(ctx, "7"); err != nil || got.BranchName() != "grain/"+dflt+"/task-7" {
		t.Fatalf("default branch = %v, %v; want grain/%s/task-7", got, err, dflt)
	}

	// An edited id names the next task's branch and leaves the last one's.
	cfg.DeploymentID = "prod"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetConfig(ctx); err != nil || got.DeploymentID != "prod" {
		t.Fatalf("stored deployment id = %v, %v; want prod", got, err)
	}
	second := task("8", true)
	if err := store.PutTask(ctx, second); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetTask(ctx, "8"); err != nil || got.BranchName() != "grain/prod/task-8" {
		t.Fatalf("edited branch = %v, %v; want grain/prod/task-8", got, err)
	}
	if got, err := store.GetTask(ctx, "7"); err != nil || got.BranchName() != "grain/"+dflt+"/task-7" {
		t.Fatalf("earlier task's branch moved: %v, %v", got, err)
	}
}

// A deployment seeded before grain/task-56 has UnsluggedTaskBranchTemplate
// ("grain/{deployment}/task-{id}") stored in grain_config. Migration 73
// upgrades that stored default to DefaultTaskBranchTemplate
// ("grain/{deployment}/task-{id}-{slug}") so newly created tasks include
// a portion of the task's title in their branch name in addition to the
// deployment and task-id uniquifiers, while keeping existing tasks'
// branches untouched and leaving custom or empty templates alone.
func TestMigrationUpgradesUnsluggedDefaultTaskBranchTemplate(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	cfg := model.DefaultConfig()
	cfg.DeploymentID = "prod"
	cfg.TaskBranchTemplate = model.UnsluggedTaskBranchTemplate
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, task("7", true)); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 72 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-72 store: %v", err)
	}

	after, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.TaskBranchTemplate != model.DefaultTaskBranchTemplate {
		t.Fatalf("migrated template = %q, want %q", after.TaskBranchTemplate, model.DefaultTaskBranchTemplate)
	}
	// Existing task keeps its pre-migration branch.
	if got, err := store.GetTask(ctx, "7"); err != nil || got.BranchName() != "grain/prod/task-7" {
		t.Fatalf("existing task branch = %v, %v; want grain/prod/task-7", got, err)
	}
	// Newly created task gets the title slug in addition to the uniquifiers.
	if err := store.PutTask(ctx, task("8", true)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetTask(ctx, "8"); err != nil || got.BranchName() != "grain/prod/task-8-rename-the-endpoint" {
		t.Fatalf("new task branch = %v, %v; want grain/prod/task-8-rename-the-endpoint", got, err)
	}

	// Custom and empty templates are left untouched by the migration.
	for _, custom := range []string{"", "alice/{deployment}/task-{id}"} {
		after.TaskBranchTemplate = custom
		if err := store.PutConfig(ctx, *after); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 72 WHERE `id` = 1"); err != nil {
			t.Fatal(err)
		}
		if err := model.New(db).Init(ctx); err != nil {
			t.Fatalf("migrating a version-72 store with template %q: %v", custom, err)
		}
		got, err := store.GetConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.TaskBranchTemplate != custom {
			t.Fatalf("migrated custom template %q = %q, want it left alone", custom, got.TaskBranchTemplate)
		}
	}
}

func TestNewTaskIDAllocatesDistinctIncreasingIDs(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	seen := map[string]bool{}
	var previous int64
	for i := 0; i < 5; i++ {
		id, err := store.NewTaskID(ctx)
		if err != nil {
			t.Fatalf("allocating id %d: %v", i, err)
		}
		if seen[id] {
			t.Fatalf("id %q handed out twice", id)
		}
		seen[id] = true

		// The decimal shape is what makes `grain get 42` and the branch
		// grain/task-42 read the way they do, so it is worth pinning.
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			t.Fatalf("id %q is not a decimal number: %v", id, err)
		}
		if n <= previous {
			t.Fatalf("id %d did not increase on %d", n, previous)
		}
		previous = n
	}
}

// A task can be created with nothing but a store -- the whole point of
// allocating identity here rather than borrowing a GitHub issue number.
func TestATaskCanBeFiledWithNoGitHubIssueAtAll(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	id, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, task(id, true)); err != nil {
		t.Fatalf("filing a task: %v", err)
	}

	got, err := store.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("task was not stored")
	}
	state, err := store.State(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateQueued {
		t.Fatalf("state = %q, want queued: an approved task needs no issue to be dispatchable", state)
	}
}

func TestCommentsRoundTripInWriteOrder(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("t1", true)); err != nil {
		t.Fatal(err)
	}

	agent := model.Principal{Kind: model.PrincipalAgent, ID: "run-1"}
	bodies := []model.Comment{
		{TaskID: "t1", Author: model.Attribution{Actor: human}, Body: "please do the thing", CreatedAt: now},
		// Grain relaying an agent's question: the case Attribution exists
		// for, and the one a signature substring used to stand in for.
		{TaskID: "t1", Author: model.Attribution{Actor: bot, OnBehalfOf: &agent}, Body: "which endpoint?", CreatedAt: now},
		{TaskID: "t1", Author: model.Attribution{Actor: human}, Body: "the second one", CreatedAt: now},
	}
	for i, c := range bodies {
		if _, err := store.AddComment(ctx, c); err != nil {
			t.Fatalf("adding comment %d: %v", i, err)
		}
	}

	got, err := store.Comments(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(bodies) {
		t.Fatalf("read back %d comments, want %d", len(got), len(bodies))
	}
	for i := range got {
		if got[i].Body != bodies[i].Body {
			t.Fatalf("comment %d body = %q, want %q (order is the write order)", i, got[i].Body, bodies[i].Body)
		}
		if got[i].Author.Actor != bodies[i].Author.Actor {
			t.Fatalf("comment %d actor = %+v, want %+v", i, got[i].Author.Actor, bodies[i].Author.Actor)
		}
	}
	relayed := got[1].Author.OnBehalfOf
	if relayed == nil || *relayed != agent {
		t.Fatalf("relayed comment's OnBehalfOf = %+v, want %+v", relayed, agent)
	}
	if got[0].Author.OnBehalfOf != nil {
		t.Fatalf("a human acting directly got an OnBehalfOf: %+v", got[0].Author.OnBehalfOf)
	}
}

func TestCommentsAreScopedToTheirTask(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for _, id := range []string{"t1", "t2"} {
		if err := store.PutTask(ctx, task(id, true)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID: id, Author: model.Attribution{Actor: human}, Body: "on " + id, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Comments(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "on t1" {
		t.Fatalf("comments on t1 = %+v, want just its own", got)
	}
}

// The id AddComment returns is the one an Observation names, which is how
// "a question is outstanding" survives without a GitHub comment id.
func TestAPendingQuestionNamesAStoredComment(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("t1", true)); err != nil {
		t.Fatal(err)
	}

	agent := model.Principal{Kind: model.PrincipalAgent, ID: "run-1"}
	id, err := store.AddComment(ctx, model.Comment{
		TaskID: "t1", Author: model.Attribution{Actor: bot, OnBehalfOf: &agent},
		Body: "which endpoint?", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID: "t1", PendingQuestionCommentID: &id, ObservedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	state, err := store.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", state)
	}

	obs, err := store.GetObservation(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != id {
		t.Fatalf("pending question = %+v, want the stored comment id %d", obs, id)
	}
}

// Comment bodies are untrusted text, the same as titles and task bodies
// (see TestUntrustedTextIsStoredNotInterpreted above).
func TestAnUntrustedCommentBodyIsStoredNotInterpreted(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("t1", true)); err != nil {
		t.Fatal(err)
	}

	nasty := "'); DROP TABLE `task`; -- \x00 \\ \" ' %s ?"
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: "t1", Author: model.Attribution{Actor: human}, Body: nasty, CreatedAt: now,
	}); err != nil {
		t.Fatalf("storing an untrusted comment body: %v", err)
	}

	got, err := store.Comments(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != nasty {
		t.Fatalf("comment body did not round-trip verbatim: %q", got[0].Body)
	}
	// The table it tried to drop is still answering.
	if _, err := store.GetTask(ctx, "t1"); err != nil {
		t.Fatalf("task table after an injection attempt: %v", err)
	}
}

// --- concurrency: SQLite's own single-writer lock -----------------------
//
// Dolt merged concurrent writers cell by cell and only reported a
// conflict when two of them touched the same cell with different values
// -- which is why the deleted dolt/store_test.go needed a random write
// stamp and a test (TestACounterStampWouldNotConflict) proving a plain
// counter would not have caught every overlap. SQLite admits only one
// writer at a time, full stop (sqlite.Open's _txlock=immediate), so
// there is no equivalent hazard to guard against here: two writers
// either take turns, serialised by the lock itself, or one of them fails
// outright with SQLITE_BUSY. The tests below exercise both outcomes
// instead.

// Two writers changing different fields of the same task at the same
// time: SQLite's write lock makes one wait for the other, and both
// changes end up present once each has had its turn.
func TestConcurrentUpdatesBothLand(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errs[0] = store.UpdateTask(ctx, "a1b2", func(tk *model.Task) error {
			tk.Title = "renamed"
			return nil
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		errs[1] = store.UpdateTask(ctx, "a1b2", func(tk *model.Task) error {
			tk.Base = "release"
			return nil
		})
	}()
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "renamed" {
		t.Fatalf("title = %q, want the first writer's change", got.Title)
	}
	if got.Base != "release" {
		t.Fatalf("base = %q, want the second writer's change -- it was lost", got.Base)
	}
}

// One writer's transaction rolls back whole, child rows included, if it
// loses the race for the write lock and has to retry -- without that,
// two writers each doing PutTask's delete-and-reinsert of a task's tags
// could see their sets silently unioned.
func TestAConflictRollsBackChildRowsToo(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	base := task("a1b2", true)
	base.Tags = []string{"keep"}
	if err := store.PutTask(ctx, base); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	for i, tag := range []string{"from-0", "from-1"} {
		go func(i int, tag string) {
			defer wg.Done()
			<-start
			if err := store.UpdateTask(ctx, "a1b2", func(tk *model.Task) error {
				tk.Tags = append(tk.Tags, tag)
				return nil
			}); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i, tag)
	}
	close(start)
	wg.Wait()

	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got.Tags)
	want := []string{"from-0", "from-1", "keep"}
	if len(got.Tags) != len(want) {
		t.Fatalf("tags = %v, want %v -- a retry that rewrote over the winner would have lost one", got.Tags, want)
	}
	for i := range want {
		if got.Tags[i] != want[i] {
			t.Fatalf("tags = %v, want %v", got.Tags, want)
		}
	}
}

// The two tests above pin the mechanism with two writers. A real
// deployment's daemon, ui, and git-proxy processes all share one store
// (pkg/cli/daemon.go runs all three against the same *sql.DB), so it is
// worth also knowing the same guarantee holds at higher fan-out: every
// writer's change lands somewhere, none silently vanishes, and nothing
// under database/sql itself races even though every one of these
// goroutines shares the one *sql.DB Store wraps.
func TestManyConcurrentWritersEachLandTheirOwnTask(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	const writers = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, writers)
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			id := fmt.Sprintf("task-%02d", i)
			errs[i] = store.PutTask(ctx, task(id, true))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	for i := 0; i < writers; i++ {
		id := fmt.Sprintf("task-%02d", i)
		got, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("reading %s: %v", id, err)
		}
		if got == nil {
			t.Errorf("task %s was never written -- one of %d concurrent writers lost its change", id, writers)
		}
	}
}

// The same "one winner at a time, nothing lost" guarantee
// TestConcurrentUpdatesBothLand and TestAConflictRollsBackChildRowsToo
// check with two writers, at a fan-out closer to what would actually
// force Store.write's retry loop to run more than once for some of them.
func TestManyConcurrentUpdatesToTheSameTaskAllLand(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}

	const writers = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, writers)
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			tag := fmt.Sprintf("from-%02d", i)
			errs[i] = store.UpdateTask(ctx, "a1b2", func(tk *model.Task) error {
				tk.Tags = append(tk.Tags, tag)
				return nil
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tags) != writers {
		t.Fatalf("tags = %v (%d), want %d -- a retry that rewrote over another winner would have lost one",
			got.Tags, len(got.Tags), writers)
	}
	seen := map[string]bool{}
	for _, tag := range got.Tags {
		seen[tag] = true
	}
	for i := 0; i < writers; i++ {
		tag := fmt.Sprintf("from-%02d", i)
		if !seen[tag] {
			t.Errorf("missing tag %q", tag)
		}
	}
}

// A store's read side under the same concurrent writers: GetTask,
// Ready, and OccupiedSlots must never see a half-written row, and none
// of them may return an error just because a writer is mid-transaction
// at the moment they run -- database/sql already guarantees the first;
// this is what tests it against Store's own queries rather than taking
// it on faith.
func TestReadsSeeConsistentStateWhileWritersAreActive(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	const (
		writers = 8
		readers = 8
	)
	var wg sync.WaitGroup
	start := make(chan struct{})
	writeErrs := make([]error, writers)
	readErrs := make([]error, readers)

	wg.Add(writers + readers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			id := fmt.Sprintf("task-%02d", i)
			if err := store.PutTask(ctx, task(id, true)); err != nil {
				writeErrs[i] = err
				return
			}
			writeErrs[i] = store.StartRun(ctx, model.Run{
				ID: "r-" + id, TaskID: id,
				Sandbox: fmt.Sprintf("sandbox-%d", i), Attempt: 1, StartedAt: now,
			}, model.Limits{})
		}(i)
	}
	for i := 0; i < readers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < 20; j++ {
				if _, err := store.Ready(ctx); err != nil {
					readErrs[i] = fmt.Errorf("Ready: %w", err)
					return
				}
				if _, err := store.LiveRunCount(ctx); err != nil {
					readErrs[i] = fmt.Errorf("LiveRunCount: %w", err)
					return
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range writeErrs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	for i, err := range readErrs {
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
	}
}

// The wording model.isSerializationFailure matches on, asserted against
// the real engine rather than assumed. pkg/model's own stamp_test.go
// pins the same string, so a driver reword fails both together.
func TestSQLiteReportsABusyDatabase(t *testing.T) {
	t.Parallel()
	db, cleanup := openWithBusyTimeout(t, 200*time.Millisecond)
	defer cleanup()
	if _, err := db.Exec(
		"CREATE TABLE `probe` (`id` INTEGER NOT NULL, `v` TEXT NOT NULL, PRIMARY KEY (`id`))"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	holder, err := db.BeginTx(ctx, nil) // takes SQLite's one write lock and holds it
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()

	_, err = db.BeginTx(ctx, nil) // must wait out busy_timeout, then fail
	if err == nil {
		t.Fatal("a second writer started while the first was still open")
	}
	if !strings.Contains(err.Error(), "database is locked") && !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("SQLite reports a busy database as %q, which model.isSerializationFailure "+
			"does not recognise -- update the markers there and in pkg/model/stamp_test.go", err)
	}
	t.Logf("SQLite reports a busy database as: %v", err)
}

// The retry path, forced deterministically with a short busy_timeout
// rather than raced for: a competing transaction holds the write lock
// past that timeout, so the first attempt or two are refused with
// SQLITE_BUSY before the lock frees up and Store.write's retry loop
// succeeds.
//
// The one test in this package left serial. It is the only one whose
// pass depends on a wall clock: the blocker holds the lock for 350ms and
// write gets five attempts at a 100ms busy_timeout each, with no backoff
// between them, so the whole margin is the 150ms between those two
// numbers. A test sharing its core with three others can lose that much
// to the scheduler, and it would lose it as a flake rather than as a
// failure. Serial costs nothing: `go test` runs every non-parallel test
// to completion before any parallel test's body starts, so this one
// still has the process to itself.
func TestARetryRecoversFromABusyDatabase(t *testing.T) {
	db, cleanup := openWithBusyTimeout(t, 100*time.Millisecond)
	defer cleanup()
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		// Held comfortably longer than one busy_timeout, so the update
		// below is refused at least once before this releases the lock --
		// exercising write's retry loop rather than hoping to catch it.
		time.Sleep(350 * time.Millisecond)
		_ = blocker.Rollback()
	}()

	if err := store.UpdateTask(ctx, "a1b2", func(tk *model.Task) error {
		tk.Title = "renamed"
		return nil
	}); err != nil {
		t.Fatalf("UpdateTask did not recover from a busy database: %v", err)
	}
	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "renamed" {
		t.Fatalf("title = %q, want the retried write to have landed", got.Title)
	}
}

// The answer to "what if it crashes mid-write": an operation that does
// not reach commit leaves nothing behind. There is no lock left held and
// no merge to reconcile, so the failure needs no cleanup at all.
func TestAFailedWriteLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	before := task("a1b2", true)
	before.Tags = []string{"keep"}
	if err := store.PutTask(ctx, before); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("the process died here")
	err := store.UpdateTask(ctx, "a1b2", func(tk *model.Task) error {
		// Change everything, then fail before the commit.
		tk.Title = "half-written"
		tk.Tags = []string{"gone"}
		tk.Base = "wrong"
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the failure to reach the caller", err)
	}

	got, err := store.GetTask(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != before.Title || got.Base != before.Base {
		t.Fatalf("task changed despite the write failing: %+v", got)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "keep" {
		t.Fatalf("tags = %v, want the original -- a partial write survived", got.Tags)
	}

	// And the store is immediately usable: nothing is held.
	if err := store.UpdateTask(ctx, "a1b2", func(tk *model.Task) error {
		tk.Title = "after the failure"
		return nil
	}); err != nil {
		t.Fatalf("the store was left unusable by a failed write: %v", err)
	}
}

// TestFailureStreakCountsConsecutiveFailuresAndStopsAtASuccess is
// Store.FailureStreak's own baseline: it walks task_run newest-first and
// counts up until (and not including) the first "succeeded" outcome, the
// same cutoff model.MaxConsecutiveFailures compares against.
func TestFailureStreakCountsConsecutiveFailuresAndStopsAtASuccess(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}

	if streak, err := store.FailureStreak(ctx, "a1b2"); err != nil {
		t.Fatal(err)
	} else if streak != nil {
		t.Fatalf("FailureStreak with no runs = %+v, want nil", streak)
	}

	starts := []time.Time{
		now, now.Add(time.Hour), now.Add(2 * time.Hour), now.Add(3 * time.Hour),
	}
	outcomes := []string{"succeeded", "failed", "failed", "failed"}
	for i, at := range starts {
		id := fmt.Sprintf("r%d", i+1)
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: "a1b2", Sandbox: "s1", Attempt: i + 1, StartedAt: at,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, at.Add(time.Minute), outcomes[i], "boom"); err != nil {
			t.Fatal(err)
		}
	}

	streak, err := store.FailureStreak(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 3 {
		t.Fatalf("FailureStreak = %+v, want Count 3 (the three failures after the one success)", streak)
	}
	if streak.LastOutcome != "failed" || streak.LastDetail != "boom" {
		t.Fatalf("FailureStreak = %+v, want the most recent run's own outcome and detail", streak)
	}
}

// TestFailureStreakIsNarrowedByARetryRequest covers bwsalmon/agents#403's
// "Retry" button end to end at the store layer: RetryRequestedAt does not
// delete or rewrite any task_run row, it only tells FailureStreak to stop
// counting once it reaches a run that started at or before the retry
// request -- so a run that already failed before the retry stays in the
// record (Runs, GetTask's own attempt history) but no longer contributes
// to the count that trips StateFailed.
func TestFailureStreakIsNarrowedByARetryRequest(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < model.MaxConsecutiveFailures; i++ {
		id := fmt.Sprintf("r%d", i+1)
		at := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: "a1b2", Sandbox: "s1", Attempt: i + 1, StartedAt: at,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, at.Add(time.Minute), "failed", "boom"); err != nil {
			t.Fatal(err)
		}
	}
	if streak, err := store.FailureStreak(ctx, "a1b2"); err != nil {
		t.Fatal(err)
	} else if streak == nil || streak.Count != model.MaxConsecutiveFailures {
		t.Fatalf("FailureStreak before retrying = %+v, want Count %d", streak, model.MaxConsecutiveFailures)
	}

	retryAt := now.Add(model.MaxConsecutiveFailures * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", RetryRequestedAt: &retryAt}); err != nil {
		t.Fatal(err)
	}

	streak, err := store.FailureStreak(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 0 {
		t.Fatalf("FailureStreak after retrying = %+v, want Count 0: every prior failure started before the retry request", streak)
	}
	if streak.LastOutcome != "failed" {
		t.Fatalf("FailureStreak after retrying = %+v, want LastOutcome still reporting the last run's own outcome", streak)
	}

	runs, err := store.Runs(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != model.MaxConsecutiveFailures {
		t.Fatalf("Runs after retrying = %d, want the retry to narrow the streak without deleting any run", len(runs))
	}

	// A run that starts after the retry request still counts: retrying
	// forgives the past, it does not disable the cap going forward.
	freshID := "r" + fmt.Sprint(model.MaxConsecutiveFailures+1)
	freshAt := retryAt.Add(time.Hour)
	if err := store.StartRun(ctx, model.Run{
		ID: freshID, TaskID: "a1b2", Sandbox: "s1",
		Attempt: model.MaxConsecutiveFailures + 1, StartedAt: freshAt,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, freshID, freshAt.Add(time.Minute), "failed", "boom again"); err != nil {
		t.Fatal(err)
	}
	if streak, err := store.FailureStreak(ctx, "a1b2"); err != nil {
		t.Fatal(err)
	} else if streak == nil || streak.Count != 1 {
		t.Fatalf("FailureStreak after a fresh post-retry failure = %+v, want Count 1", streak)
	}
}

// TestAPausedRunCountsAgainstNeitherStreak is model.PausedOutcome's own
// rule, held against both implementations of it at once: Store.
// FailureStreak's walk in Go, and task_streak's WHERE clause in SQL
// (which task_state reads, and which State answers out of). A run grain
// stopped because the deployment's agent had no budget left says nothing
// about the task it was given, so it neither adds to the streak nor
// clears the failures before it -- otherwise a single provider outage
// would spend the retry budget of every task that happened to be running
// when it began.
func TestAPausedRunCountsAgainstNeitherStreak(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}

	// Every run this task has ever had was stopped by a usage limit.
	for i := 0; i < model.MaxConsecutiveFailures+1; i++ {
		id := fmt.Sprintf("r%d", i+1)
		at := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: "a1b2", Sandbox: "s1", Attempt: i + 1, StartedAt: at,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, at.Add(time.Minute), model.PausedOutcome, "usage limit"); err != nil {
			t.Fatal(err)
		}
	}

	streak, err := store.FailureStreak(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 0 {
		t.Fatalf("FailureStreak over paused runs = %+v, want Count 0", streak)
	}
	if state, err := store.State(ctx, "a1b2"); err != nil {
		t.Fatal(err)
	} else if state != model.StateQueued {
		t.Fatalf("state after %d paused runs = %q, want %q: task_streak must skip them too",
			model.MaxConsecutiveFailures+1, state, model.StateQueued)
	}

	// And a paused run is not a success either: the failures before it
	// still stand.
	failedAt := now.Add(100 * time.Hour)
	if err := store.StartRun(ctx, model.Run{
		ID: "f1", TaskID: "a1b2", Sandbox: "s1", Attempt: 100, StartedAt: failedAt,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "f1", failedAt.Add(time.Minute), "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	pausedAt := failedAt.Add(time.Hour)
	if err := store.StartRun(ctx, model.Run{
		ID: "p1", TaskID: "a1b2", Sandbox: "s1", Attempt: 101, StartedAt: pausedAt,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "p1", pausedAt.Add(time.Minute), model.PausedOutcome, "usage limit"); err != nil {
		t.Fatal(err)
	}
	if streak, err := store.FailureStreak(ctx, "a1b2"); err != nil {
		t.Fatal(err)
	} else if streak == nil || streak.Count != 1 {
		t.Fatalf("FailureStreak = %+v, want Count 1: a pause neither counts nor forgives", streak)
	}
}

// TestACapacityRunCountsAgainstTheStreakButTowardTheBackoff is
// model.CapacityOutcome's own rule, held against both implementations of
// the streak at once the way the paused case above is -- and against the
// one place the two outcomes deliberately differ. A run the provider had
// no capacity for says nothing about the task, so it neither adds to the
// streak nor clears it and the task never reads 'failed' for it. Unlike a
// pause, though, nothing stops dispatch while a provider is overloaded,
// so it does count toward BackoffCount: the next attempt is spaced out
// rather than fired at the same refusal on the next poll.
func TestACapacityRunCountsAgainstTheStreakButTowardTheBackoff(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}

	// Every run this task has ever had was refused by an overloaded
	// provider.
	for i := 0; i < model.MaxConsecutiveFailures+1; i++ {
		id := fmt.Sprintf("c%d", i+1)
		at := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: "a1b2", Sandbox: "s1", Attempt: i + 1, StartedAt: at,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, at.Add(time.Minute), model.CapacityOutcome,
			"antigravity: the provider had no capacity for this run"); err != nil {
			t.Fatal(err)
		}
	}

	streak, err := store.FailureStreak(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 0 {
		t.Fatalf("FailureStreak over capacity refusals = %+v, want Count 0", streak)
	}
	if streak.BackoffCount != model.MaxConsecutiveFailures+1 {
		t.Fatalf("FailureStreak = %+v, want BackoffCount %d: each refusal still spaces the next attempt",
			streak, model.MaxConsecutiveFailures+1)
	}
	if state, err := store.State(ctx, "a1b2"); err != nil {
		t.Fatal(err)
	} else if state != model.StateQueued {
		t.Fatalf("state after %d capacity refusals = %q, want %q: task_streak must skip them too",
			model.MaxConsecutiveFailures+1, state, model.StateQueued)
	}

	// And a capacity refusal is not a success either: the failure before
	// it still stands, and still counts once.
	failedAt := now.Add(100 * time.Hour)
	if err := store.StartRun(ctx, model.Run{
		ID: "cf1", TaskID: "a1b2", Sandbox: "s1", Attempt: 100, StartedAt: failedAt,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "cf1", failedAt.Add(time.Minute), "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	refusedAt := failedAt.Add(time.Hour)
	if err := store.StartRun(ctx, model.Run{
		ID: "cc1", TaskID: "a1b2", Sandbox: "s1", Attempt: 101, StartedAt: refusedAt,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "cc1", refusedAt.Add(time.Minute), model.CapacityOutcome, "overloaded"); err != nil {
		t.Fatal(err)
	}
	if streak, err := store.FailureStreak(ctx, "a1b2"); err != nil {
		t.Fatal(err)
	} else if streak == nil || streak.Count != 1 {
		t.Fatalf("FailureStreak = %+v, want Count 1: a refusal neither counts nor forgives", streak)
	}
}

// TestAnInterruptedRunCountsAgainstNeitherStreak verifies that runs ending with
// model.InterruptedOutcome are ignored by both computeStreakForTask and
// Store.FailureStreak, so a daemon restart or drain does not penalize tasks.
func TestAnInterruptedRunCountsAgainstNeitherStreak(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < model.MaxConsecutiveFailures+1; i++ {
		id := fmt.Sprintf("int%d", i+1)
		at := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: "a1b2", Sandbox: "s1", Attempt: i + 1, StartedAt: at,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, at.Add(time.Minute), model.InterruptedOutcome, model.InterruptedDetail); err != nil {
			t.Fatal(err)
		}
	}

	streak, err := store.FailureStreak(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 0 {
		t.Fatalf("FailureStreak over interrupted runs = %+v, want Count 0", streak)
	}
	if streak.BackoffCount != 0 {
		t.Fatalf("FailureStreak = %+v, want BackoffCount 0: interrupted runs should not cause retry backoff", streak)
	}
	if state, err := store.State(ctx, "a1b2"); err != nil {
		t.Fatal(err)
	} else if state != model.StateQueued {
		t.Fatalf("state after %d interrupted runs = %q, want %q",
			model.MaxConsecutiveFailures+1, state, model.StateQueued)
	}
}

func TestTaskStreakWindowFunctionAndStatesScale(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	// Task 1: 20 failed runs -> StateFailed (streak 20 >= 5)
	if err := store.PutTask(ctx, task("t1", true)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		rID := fmt.Sprintf("t1-r%d", i)
		st := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: "t1", Sandbox: "s", Attempt: i + 1, StartedAt: st}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rID, st.Add(time.Minute), "failed", "err"); err != nil {
			t.Fatal(err)
		}
	}

	// Task 2: 10 failed runs, then 1 succeeded run -> StateQueued (streak 0)
	if err := store.PutTask(ctx, task("t2", true)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		rID := fmt.Sprintf("t2-r%d", i)
		st := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: "t2", Sandbox: "s", Attempt: i + 1, StartedAt: st}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rID, st.Add(time.Minute), "failed", "err"); err != nil {
			t.Fatal(err)
		}
	}
	succAt := now.Add(15 * time.Hour)
	if err := store.StartRun(ctx, model.Run{ID: "t2-succ", TaskID: "t2", Sandbox: "s", Attempt: 11, StartedAt: succAt}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "t2-succ", succAt.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	// Task 3: 5 failed runs, then 1 succeeded run, then 2 failed runs -> StateQueued (streak 2)
	if err := store.PutTask(ctx, task("t3", true)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		rID := fmt.Sprintf("t3-r%d", i)
		st := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: "t3", Sandbox: "s", Attempt: i + 1, StartedAt: st}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rID, st.Add(time.Minute), "failed", "err"); err != nil {
			t.Fatal(err)
		}
	}
	succAt3 := now.Add(6 * time.Hour)
	if err := store.StartRun(ctx, model.Run{ID: "t3-succ", TaskID: "t3", Sandbox: "s", Attempt: 6, StartedAt: succAt3}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "t3-succ", succAt3.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		rID := fmt.Sprintf("t3-post-%d", i)
		st := succAt3.Add(time.Duration(i+1) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: "t3", Sandbox: "s", Attempt: 7 + i, StartedAt: st}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rID, st.Add(time.Minute), "failed", "err"); err != nil {
			t.Fatal(err)
		}
	}

	// Task 4: 10 failed runs, then retry requested, then 1 failed run -> StateQueued (streak 1)
	if err := store.PutTask(ctx, task("t4", true)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		rID := fmt.Sprintf("t4-r%d", i)
		st := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: "t4", Sandbox: "s", Attempt: i + 1, StartedAt: st}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rID, st.Add(time.Minute), "failed", "err"); err != nil {
			t.Fatal(err)
		}
	}
	retryAt := now.Add(12 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: "t4", RetryRequestedAt: &retryAt}); err != nil {
		t.Fatal(err)
	}
	postRetryAt := retryAt.Add(time.Hour)
	if err := store.StartRun(ctx, model.Run{ID: "t4-post", TaskID: "t4", Sandbox: "s", Attempt: 11, StartedAt: postRetryAt}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "t4-post", postRetryAt.Add(time.Minute), "failed", "err"); err != nil {
		t.Fatal(err)
	}

	// Task 5: 10 paused runs, 3 capacity runs, 2 failed runs -> StateQueued (streak 2)
	if err := store.PutTask(ctx, task("t5", true)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		rID := fmt.Sprintf("t5-p%d", i)
		st := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: "t5", Sandbox: "s", Attempt: i + 1, StartedAt: st}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rID, st.Add(time.Minute), model.PausedOutcome, "paused"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		rID := fmt.Sprintf("t5-c%d", i)
		st := now.Add(time.Duration(10+i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: "t5", Sandbox: "s", Attempt: 11 + i, StartedAt: st}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rID, st.Add(time.Minute), model.CapacityOutcome, "capacity"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		rID := fmt.Sprintf("t5-f%d", i)
		st := now.Add(time.Duration(15+i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: "t5", Sandbox: "s", Attempt: 14 + i, StartedAt: st}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rID, st.Add(time.Minute), "failed", "err"); err != nil {
			t.Fatal(err)
		}
	}

	// Task 6: 0 runs -> StateQueued (streak 0)
	if err := store.PutTask(ctx, task("t6", true)); err != nil {
		t.Fatal(err)
	}

	// Task 7: 1 live run (running)
	if err := store.PutTask(ctx, task("t7", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "t7-r1", TaskID: "t7", Sandbox: "s", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// Verify individual FailureStreak values
	expectedStreaks := map[string]int{
		"t1": 20,
		"t2": 0,
		"t3": 2,
		"t4": 1,
		"t5": 2,
		"t6": 0,
		"t7": 0,
	}
	for id, expStreak := range expectedStreaks {
		st, err := store.FailureStreak(ctx, id)
		if err != nil {
			t.Fatalf("FailureStreak(%s): %v", id, err)
		}
		count := 0
		if st != nil {
			count = st.Count
		}
		if count != expStreak {
			t.Errorf("task %s streak = %d, want %d", id, count, expStreak)
		}
	}

	// Verify States() across all tasks
	states, err := store.States(ctx)
	if err != nil {
		t.Fatalf("States(): %v", err)
	}
	expectedStates := map[string]model.State{
		"t1": model.StateFailed,
		"t2": model.StateQueued,
		"t3": model.StateQueued,
		"t4": model.StateQueued,
		"t5": model.StateQueued,
		"t6": model.StateQueued,
		"t7": model.StateRunning,
	}
	for id, expState := range expectedStates {
		if got := states[id]; got != expState {
			t.Errorf("States()[%s] = %q, want %q", id, got, expState)
		}
		singleState, err := store.State(ctx, id)
		if err != nil {
			t.Fatalf("State(%s): %v", id, err)
		}
		if singleState != expState {
			t.Errorf("State(%s) = %q, want %q", id, singleState, expState)
		}
	}

	// Verify StatesPage() pagination
	page1, cursor1, err := store.StatesPage(ctx, 3, "")
	if err != nil {
		t.Fatalf("StatesPage page 1: %v", err)
	}
	if len(page1) != 3 || cursor1 != "t3" {
		t.Fatalf("StatesPage page 1 = %v, cursor = %q; want 3 items and cursor t3", page1, cursor1)
	}

	page2, cursor2, err := store.StatesPage(ctx, 3, cursor1)
	if err != nil {
		t.Fatalf("StatesPage page 2: %v", err)
	}
	if len(page2) != 3 || cursor2 != "t6" {
		t.Fatalf("StatesPage page 2 = %v, cursor = %q; want 3 items and cursor t6", page2, cursor2)
	}

	page3, cursor3, err := store.StatesPage(ctx, 3, cursor2)
	if err != nil {
		t.Fatalf("StatesPage page 3: %v", err)
	}
	if len(page3) != 1 || cursor3 != "" {
		t.Fatalf("StatesPage page 3 = %v, cursor = %q; want 1 item and cursor empty", page3, cursor3)
	}

	// Verify EachState streaming
	streamed := make(map[string]model.State)
	err = store.EachState(ctx, 2, func(id string, state model.State) error {
		streamed[id] = state
		return nil
	})
	if err != nil {
		t.Fatalf("EachState: %v", err)
	}
	if len(streamed) != len(expectedStates) {
		t.Fatalf("EachState returned %d states, want %d", len(streamed), len(expectedStates))
	}
}

func BenchmarkStatesLongRunHistories(b *testing.B) {
	store, _, ctx := openStore(b)
	const numTasks = 1000
	const runsPerTask = 10

	for i := 0; i < numTasks; i++ {
		tID := fmt.Sprintf("bench-task-%04d", i)
		if err := store.PutTask(ctx, task(tID, true)); err != nil {
			b.Fatal(err)
		}
		for r := 0; r < runsPerTask; r++ {
			rID := fmt.Sprintf("%s-r%d", tID, r)
			st := now.Add(time.Duration(r) * time.Hour)
			if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: tID, Sandbox: "s", Attempt: r + 1, StartedAt: st}, model.Limits{}); err != nil {
				b.Fatal(err)
			}
			outcome := "failed"
			if r == runsPerTask-1 && (i%3 == 0) {
				outcome = "succeeded"
			}
			if err := store.FinishRun(ctx, rID, st.Add(time.Minute), outcome, ""); err != nil {
				b.Fatal(err)
			}
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		states, err := store.States(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(states) != numTasks {
			b.Fatalf("States() returned %d tasks, want %d", len(states), numTasks)
		}
	}
}

func BenchmarkStatesSinglePage(b *testing.B) {
	store, _, ctx := openStore(b)
	const numTasks = 1000
	const runsPerTask = 5

	for i := 0; i < numTasks; i++ {
		tID := fmt.Sprintf("bench-task-%04d", i)
		if err := store.PutTask(ctx, task(tID, true)); err != nil {
			b.Fatal(err)
		}
		for r := 0; r < runsPerTask; r++ {
			rID := fmt.Sprintf("%s-r%d", tID, r)
			st := now.Add(time.Duration(r) * time.Hour)
			if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: tID, Sandbox: "s", Attempt: r + 1, StartedAt: st}, model.Limits{}); err != nil {
				b.Fatal(err)
			}
			outcome := "failed"
			if r == runsPerTask-1 && (i%3 == 0) {
				outcome = "succeeded"
			}
			if err := store.FinishRun(ctx, rID, st.Add(time.Minute), outcome, ""); err != nil {
				b.Fatal(err)
			}
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page, _, err := store.StatesPage(ctx, 100, "")
		if err != nil {
			b.Fatal(err)
		}
		if len(page) != 100 {
			b.Fatalf("StatesPage returned %d tasks, want 100", len(page))
		}
	}
}

func BenchmarkStatesPage(b *testing.B) {
	store, _, ctx := openStore(b)
	const numTasks = 1000
	const runsPerTask = 5

	for i := 0; i < numTasks; i++ {
		tID := fmt.Sprintf("bench-task-%04d", i)
		if err := store.PutTask(ctx, task(tID, true)); err != nil {
			b.Fatal(err)
		}
		for r := 0; r < runsPerTask; r++ {
			rID := fmt.Sprintf("%s-r%d", tID, r)
			st := now.Add(time.Duration(r) * time.Hour)
			if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: tID, Sandbox: "s", Attempt: r + 1, StartedAt: st}, model.Limits{}); err != nil {
				b.Fatal(err)
			}
			outcome := "failed"
			if r == runsPerTask-1 && (i%3 == 0) {
				outcome = "succeeded"
			}
			if err := store.FinishRun(ctx, rID, st.Add(time.Minute), outcome, ""); err != nil {
				b.Fatal(err)
			}
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var cursor string
		total := 0
		for {
			page, nextCursor, err := store.StatesPage(ctx, 100, cursor)
			if err != nil {
				b.Fatal(err)
			}
			total += len(page)
			if nextCursor == "" || len(page) == 0 {
				break
			}
			cursor = nextCursor
		}
		if total != numTasks {
			b.Fatalf("StatesPage total = %d, want %d", total, numTasks)
		}
	}
}

func BenchmarkStatesEachStateStreaming(b *testing.B) {
	store, _, ctx := openStore(b)
	const numTasks = 1000
	const runsPerTask = 5

	for i := 0; i < numTasks; i++ {
		tID := fmt.Sprintf("bench-task-%04d", i)
		if err := store.PutTask(ctx, task(tID, true)); err != nil {
			b.Fatal(err)
		}
		for r := 0; r < runsPerTask; r++ {
			rID := fmt.Sprintf("%s-r%d", tID, r)
			st := now.Add(time.Duration(r) * time.Hour)
			if err := store.StartRun(ctx, model.Run{ID: rID, TaskID: tID, Sandbox: "s", Attempt: r + 1, StartedAt: st}, model.Limits{}); err != nil {
				b.Fatal(err)
			}
			outcome := "failed"
			if r == runsPerTask-1 && (i%3 == 0) {
				outcome = "succeeded"
			}
			if err := store.FinishRun(ctx, rID, st.Add(time.Minute), outcome, ""); err != nil {
				b.Fatal(err)
			}
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		count := 0
		err := store.EachState(ctx, 100, func(id string, state model.State) error {
			count++
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
		if count != numTasks {
			b.Fatalf("EachState total = %d, want %d", count, numTasks)
		}
	}
}

// TestStartRunRejectsASecondLiveRunOnTheSameTask pins what is left of
// the bwsalmon/agents#434 guard once slots stopped existing. It used to
// be one live run per slot, catching two overlapping dispatch.Cycle
// callers that both decided the same slot was free; the concurrency
// limit is now enforced inside StartRun's own transaction instead, and
// what this index still rules out is a task running twice at once --
// which task_state assumes when it reads a live run as 'running', and
// which a REPLACE INTO would have hidden by silently overwriting the
// first run's row.
func TestStartRunRejectsASecondLiveRunOnTheSameTask(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}

	first := model.Run{ID: "a1b2-1", TaskID: "a1b2", Sandbox: "a1b2-1", Attempt: 1, StartedAt: now}
	if err := store.StartRun(ctx, first, model.Limits{}); err != nil {
		t.Fatalf("first StartRun for an idle task: %v", err)
	}

	second := model.Run{ID: "a1b2-2", TaskID: "a1b2", Sandbox: "a1b2-2", Attempt: 2, StartedAt: now}
	if err := store.StartRun(ctx, second, model.Limits{}); err == nil {
		t.Fatal("a second live run on a task that already has one should have failed, not landed")
	}

	live, err := store.LiveRunCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live runs = %d, want exactly 1 -- the rejected second run must not have landed", live)
	}
	streak, err := store.FailureStreak(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if streak != nil {
		t.Fatalf("first run's row was overwritten: FailureStreak(a1b2) = %+v, want nil (never finished)", streak)
	}

	// Once the first attempt has finished, a second one starts fine --
	// the index guards against an overlap, not against a retry.
	if err := store.FinishRun(ctx, "a1b2-1", now.Add(time.Hour), "failed", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, second, model.Limits{}); err != nil {
		t.Fatalf("StartRun for a second attempt after the first finished: %v", err)
	}
}

// TestStartRunRefusesToExceedTheConcurrencyLimit pins the guard that
// replaced the one-run-per-slot index as the thing keeping two
// overlapping dispatch.Cycle callers from over-dispatching. Cycle reads
// the live-run count and task_ready outside any one transaction, so
// nothing in Go stops both from seeing the same headroom; StartRun
// re-counts inside the transaction that records the run, so the second
// one loses with model.ErrAtCapacity rather than landing an extra run.
func TestStartRunRefusesToExceedTheConcurrencyLimit(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for _, id := range []string{"a1b2", "c3d4", "e5f6"} {
		if err := store.PutTask(ctx, task(id, true)); err != nil {
			t.Fatal(err)
		}
	}

	for _, id := range []string{"a1b2", "c3d4"} {
		run := model.Run{ID: id + "-1", TaskID: id, Sandbox: id + "-1", Attempt: 1, StartedAt: now}
		if err := store.StartRun(ctx, run, model.Limits{Workers: 2}); err != nil {
			t.Fatalf("StartRun for %s within the limit: %v", id, err)
		}
	}

	third := model.Run{ID: "e5f6-1", TaskID: "e5f6", Sandbox: "e5f6-1", Attempt: 1, StartedAt: now}
	err := store.StartRun(ctx, third, model.Limits{Workers: 2})
	if !errors.Is(err, model.ErrAtCapacity) {
		t.Fatalf("StartRun past the limit = %v, want ErrAtCapacity", err)
	}
	if live, err := store.LiveRunCount(ctx); err != nil || live != 2 {
		t.Fatalf("live runs = %d (%v), want exactly 2 -- the refused run must not have landed", live, err)
	}

	// Finishing one makes room for exactly one more.
	if err := store.FinishRun(ctx, "a1b2-1", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, third, model.Limits{Workers: 2}); err != nil {
		t.Fatalf("StartRun into freed capacity: %v", err)
	}

	// A limit of zero or less means "no limit of mine to enforce" -- what
	// a caller with its own reason to record a run passes.
	fourth := model.Run{ID: "a1b2-2", TaskID: "a1b2", Sandbox: "a1b2-2", Attempt: 2, StartedAt: now.Add(2 * time.Hour)}
	if err := store.StartRun(ctx, fourth, model.Limits{}); err != nil {
		t.Fatalf("StartRun with no limit: %v", err)
	}
}

// fixTask is a task the merge queue filed to repair a stuck queue head
// (Origin.Reason == ReasonFix) -- the one kind whose runs count against
// model.Limits.Mergers rather than against the worker ceiling.
func fixTask(id string) model.Task {
	t := task(id, true)
	t.Origin.Reason = model.ReasonFix
	return t
}

// A repair is a merger too, and it is not a ReasonFix task: it is
// another attempt of whatever task the merge queue sent back to work on
// its own branch, whose origin_reason is whatever it was filed as. The
// store answers "is this run a merger" off both halves at once
// (mergerTaskSQL), so a repair draws on the capacity kept back for
// exactly this rather than queueing behind ordinary work.
func TestARepairInFlightCountsAsAMergerThoughItsReasonIsNot(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for _, id := range []string{"w1", "r1"} {
		if err := store.PutTask(ctx, task(id, true)); err != nil {
			t.Fatal(err)
		}
	}
	asked := now
	if err := store.Observe(ctx, model.Observation{TaskID: "r1", MergeQueueRepairAt: &asked}); err != nil {
		t.Fatal(err)
	}

	ready, err := store.ReadyMergers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0] != "r1" {
		t.Fatalf("ReadyMergers = %v, want just the task being repaired", ready)
	}

	limits := model.Limits{Workers: 1, Mergers: 1}
	start := func(id string) error {
		return store.StartRun(ctx, model.Run{
			ID: id + "-1", TaskID: id, Sandbox: id + "-1", Attempt: 1, StartedAt: now,
		}, limits)
	}
	if err := start("w1"); err != nil {
		t.Fatalf("StartRun for the one worker slot: %v", err)
	}
	// The worker ceiling is full, so this only starts if the repair is
	// classified as a merger.
	if err := start("r1"); err != nil {
		t.Fatalf("StartRun for a repair with the worker ceiling full: %v", err)
	}
	counts, err := store.LiveRunCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Workers != 1 || counts.Mergers != 1 {
		t.Fatalf("LiveRunCounts = %+v, want one worker and one merger", counts)
	}

	// And once the repair has completed, the same task is ordinary work
	// again -- nothing holds merger capacity open for a finished repair.
	finished := now.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID: "r1", MergeQueueRepairAt: &asked, CompletedAt: &finished,
	}); err != nil {
		t.Fatal(err)
	}
	if counts, err := store.LiveRunCounts(ctx); err != nil || counts.Mergers != 0 || counts.Workers != 2 {
		t.Fatalf("LiveRunCounts once the repair completed = %+v (%v), want two workers", counts, err)
	}
}

// TestStartRunCountsMergersAgainstTheirOwnHalfOfTheLimit is
// grain/task-63 at the only place the limit is really enforced: inside
// the transaction that records the run. Ordinary work stops at
// Limits.Workers even with merger capacity free; a merge-queue fix task
// takes that free capacity *and* a spare worker slot; and nothing of
// either kind gets past the sum of the two.
func TestStartRunCountsMergersAgainstTheirOwnHalfOfTheLimit(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for _, id := range []string{"w1", "w2", "f1", "f2"} {
		tk := task(id, true)
		if id[0] == 'f' {
			tk = fixTask(id)
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	limits := model.Limits{Workers: 1, Mergers: 2}
	start := func(id string) error {
		return store.StartRun(ctx, model.Run{
			ID: id + "-1", TaskID: id, Sandbox: id + "-1", Attempt: 1, StartedAt: now,
		}, limits)
	}

	if err := start("w1"); err != nil {
		t.Fatalf("StartRun for the one worker slot: %v", err)
	}
	if err := start("w2"); !errors.Is(err, model.ErrAtCapacity) {
		t.Fatalf("StartRun for a second worker = %v, want ErrAtCapacity -- merger capacity is not a general pool", err)
	}
	// Both fix tasks fit: one in a merger slot, one in the other.
	if err := start("f1"); err != nil {
		t.Fatalf("StartRun for the first fix task: %v", err)
	}
	if err := start("f2"); err != nil {
		t.Fatalf("StartRun for the second fix task: %v", err)
	}

	counts, err := store.LiveRunCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Workers != 1 || counts.Mergers != 2 {
		t.Fatalf("LiveRunCounts = %+v, want one worker and two mergers", counts)
	}
	if live, err := store.LiveRunCount(ctx); err != nil || live != counts.Total() {
		t.Fatalf("LiveRunCount = %d (%v), want the same %d LiveRunCounts totals", live, err, counts.Total())
	}

	// Everything together is capped at the sum, so with three live there
	// is nothing left for a fix task either.
	if err := store.PutTask(ctx, fixTask("f3")); err != nil {
		t.Fatal(err)
	}
	if err := start("f3"); !errors.Is(err, model.ErrAtCapacity) {
		t.Fatalf("StartRun past the sum of both limits = %v, want ErrAtCapacity", err)
	}

	// Freeing the worker slot frees it for a merger too -- that is the
	// direction the asymmetry runs.
	if err := store.FinishRun(ctx, "w1-1", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := start("f3"); err != nil {
		t.Fatalf("StartRun for a fix task into a freed worker slot: %v", err)
	}
	if counts, err := store.LiveRunCounts(ctx); err != nil || counts.Mergers != 3 || counts.Workers != 0 {
		t.Fatalf("LiveRunCounts = %+v (%v), want three mergers and no worker", counts, err)
	}
}

func TestGetConfigReturnsNilOnAFreshDatabase(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	got, err := store.GetConfig(ctx)
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil) before anything has written a config, got (%+v, %v)", got, err)
	}
}

func testConfig() model.Config {
	return model.Config{
		PollInterval: 30 * time.Second, MaxWorkers: 2, MaxCIJobs: 2,
		// The CI gate's two ordering windows (grain/task-325), named
		// because they are durations stored as milliseconds: a column
		// that dropped the unit would still round-trip a zero, which is
		// what a deployment that has chosen neither stores.
		CIJobsPerRun: 6, CIAgeBoost: 90 * time.Second, CIReserveAfter: 45 * time.Second,
		// And the merge queue's own duration-as-milliseconds column
		// (grain/task-360), named for the same reason -- and negative,
		// which is the value that says something rather than nothing here:
		// it is how an operator turns the gather window off, so a column
		// that lost the sign would turn it back on.
		MergeGatherWindow: -1 * time.Second,
		// AgentFramework is named explicitly rather than left zero
		// because it is the one Config field that never reads back
		// exactly as written: GetConfig runs it through
		// model.NormalizeAgentFramework, so "" and the legacy "gemini"
		// both come back as AgentFrameworkAntigravity (see
		// TestGetConfigNormalizesTheLegacyAgentFrameworkName below).
		MaxAgentTurns: 40,
		GitHubHost:    "github.com", GitHubInsecureHTTP: false,
		GCPProject: "grain-prod", GCPServiceAccountEmail: "agent@grain-prod.iam.gserviceaccount.com",
		SandboxCPUs:         4,
		SandboxMemoryMB:     8192,
		SandboxDiskGB:       40,
		DefaultCapabilities: []string{"gcp-key", "github-sandbox"},
		DefaultFilters:      "f=state:any:queued|in_progress&f=repo:none:acme/widgets",
		EnvironmentName:     "staging",
		// Multi-line on purpose: the deployment's standing instructions
		// are the one Config field that is prose (grain/task-114), and a
		// column that stored only its first line would still round-trip a
		// one-line value.
		PromptExtension:    "Run `make lint` before you push.\nSay what you tried.",
		TaskBranchTemplate: "alice/grain/task-{id}",
		// The git identity this deployment's agents commit under
		// (grain/task-14). Both halves are named, since a column that
		// round-tripped one and dropped the other would still look right
		// on every commit made under a deployment that had only renamed
		// the committer.
		// Named explicitly for AgentFramework's own reason a third time:
		// GetConfig runs it through model.NormalizeUpgradeCheckMode, so
		// "" reads back as model.UpgradeCheckAuto rather than as what was
		// written (see TestGetConfigDefaultsAnUnsetUpgradeCheckMode).
		UpgradeCheckMode: model.UpgradeCheckApprove,
	}
}

func TestConfigRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	want := testConfig()
	if err := store.PutConfig(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
}

// A row written before the upgrade-check setting existed carries the
// empty string, and reads back as the default rather than as "off" --
// model.Config.UpgradeCheckMode's own doc comment on why that direction
// is the safe one. Written straight through the driver, since PutConfig
// normalizes on the way in and so cannot produce the row this is about.
func TestGetConfigDefaultsAnUnsetUpgradeCheckMode(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutConfig(ctx, testConfig()); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE `grain_config` SET `upgrade_check_mode` = '' WHERE `id` = 1"); err != nil {
		t.Fatalf("blanking upgrade_check_mode: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if got.UpgradeCheckMode != model.UpgradeCheckAuto {
		t.Errorf("UpgradeCheckMode = %q, want %q", got.UpgradeCheckMode, model.UpgradeCheckAuto)
	}
}

// The deployment-wide pause (grain/task-38): absent means running, a set
// pause reads back whole, and clearing reports whether there was one --
// which is what both `grain pause -resume` and the UI's Resume button
// say back to whoever pressed them.
func TestPipelinePauseRoundTripsAndClears(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	got, err := store.GetPipelinePause(ctx)
	if err != nil {
		t.Fatalf("get on a fresh store: %v", err)
	}
	if got != nil {
		t.Fatalf("a deployment nobody has paused reads %+v, want nil", got)
	}
	if cleared, err := store.ClearPipelinePause(ctx); err != nil || cleared {
		t.Fatalf("clearing a pause that was never taken = (%v, %v), want (false, nil)", cleared, err)
	}

	want := model.PipelinePause{
		PausedAt:    time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		Reason:      "checking the upgrade to main (task 12)",
		By:          model.Principal{Kind: model.PrincipalAutomation, ID: "upgrade"},
		UntilTaskID: "12",
	}
	if err := store.SetPipelinePause(ctx, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err = store.GetPipelinePause(ctx)
	if err != nil || got == nil {
		t.Fatalf("get after set: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
	if !got.Exempts("12") || got.Exempts("13") {
		t.Errorf("Exempts: 12 = %v, 13 = %v, want true and false", got.Exempts("12"), got.Exempts("13"))
	}

	// Replaced rather than accumulated: an operator pausing a deployment
	// grain had already paused says the more recent thing, and there is
	// still exactly one pause afterwards.
	byHand := model.PipelinePause{
		PausedAt: want.PausedAt.Add(time.Minute),
		Reason:   "patching the host",
		By:       model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
	}
	if err := store.SetPipelinePause(ctx, byHand); err != nil {
		t.Fatalf("set a second time: %v", err)
	}
	got, err = store.GetPipelinePause(ctx)
	if err != nil || got == nil {
		t.Fatalf("get after the second set: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, byHand) {
		t.Fatalf("got %+v, want %+v", *got, byHand)
	}
	if got.Exempts("12") {
		t.Error("an operator's own pause exempts task 12, want nothing exempted")
	}

	cleared, err := store.ClearPipelinePause(ctx)
	if err != nil || !cleared {
		t.Fatalf("clear = (%v, %v), want (true, nil)", cleared, err)
	}
	got, err = store.GetPipelinePause(ctx)
	if err != nil || got != nil {
		t.Fatalf("get after clear: (%+v, %v), want (nil, nil)", got, err)
	}
}

// The orchestrator lifts a pause taken until a task finishes, but only
// that pause: an operator who replaces it with their own between the
// reconciler's read and its write keeps theirs.
func TestClearPipelinePauseUntilLeavesAReplacingPauseAlone(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	if cleared, err := store.ClearPipelinePauseUntil(ctx, "12"); err != nil || cleared {
		t.Fatalf("clearing a pause that was never taken = (%v, %v), want (false, nil)", cleared, err)
	}

	forTask := model.PipelinePause{
		PausedAt:    time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		Reason:      "checking the upgrade to main (task 12)",
		By:          model.Principal{Kind: model.PrincipalAutomation, ID: "upgrade"},
		UntilTaskID: "12",
	}
	if err := store.SetPipelinePause(ctx, forTask); err != nil {
		t.Fatalf("set: %v", err)
	}
	read, err := store.GetPipelinePause(ctx)
	if err != nil || read == nil {
		t.Fatalf("get: (%+v, %v)", read, err)
	}

	// The reconciler has read the task-bound pause; the operator replaces
	// it before the reconciler clears it.
	byHand := model.PipelinePause{
		PausedAt: forTask.PausedAt.Add(time.Minute),
		Reason:   "patching the host",
		By:       model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
	}
	if err := store.SetPipelinePause(ctx, byHand); err != nil {
		t.Fatalf("set by hand: %v", err)
	}
	if cleared, err := store.ClearPipelinePauseUntil(ctx, read.UntilTaskID); err != nil || cleared {
		t.Fatalf("clearing task 12's pause over the operator's = (%v, %v), want (false, nil)", cleared, err)
	}
	got, err := store.GetPipelinePause(ctx)
	if err != nil || got == nil {
		t.Fatalf("get after the refused clear: (%+v, %v), want the operator's pause", got, err)
	}
	if !reflect.DeepEqual(*got, byHand) {
		t.Fatalf("got %+v, want %+v", *got, byHand)
	}
	// An empty task ID must never match the operator's own pause, whose
	// until_task_id is empty too.
	if cleared, err := store.ClearPipelinePauseUntil(ctx, ""); err != nil || cleared {
		t.Fatalf("clearing with no task = (%v, %v), want (false, nil)", cleared, err)
	}

	// A pause for another task is not task 12's either.
	other := forTask
	other.UntilTaskID = "13"
	if err := store.SetPipelinePause(ctx, other); err != nil {
		t.Fatalf("set for task 13: %v", err)
	}
	if cleared, err := store.ClearPipelinePauseUntil(ctx, "12"); err != nil || cleared {
		t.Fatalf("clearing task 12's pause over task 13's = (%v, %v), want (false, nil)", cleared, err)
	}
	if cleared, err := store.ClearPipelinePauseUntil(ctx, "13"); err != nil || !cleared {
		t.Fatalf("clearing task 13's own pause = (%v, %v), want (true, nil)", cleared, err)
	}
	if got, err := store.GetPipelinePause(ctx); err != nil || got != nil {
		t.Fatalf("get after clear: (%+v, %v), want (nil, nil)", got, err)
	}
}

// The upgrade that pause is sometimes taken for (grain/task-156):
// absent means nothing is scheduled, a set schedule reads back whole, a
// second one replaces the first, and clearing reports whether there was
// one -- which is what the cancel button says back to whoever pressed
// it.
func TestScheduledUpgradeRoundTripsAndClears(t *testing.T) {
	store, _, ctx := openStore(t)

	got, err := store.GetScheduledUpgrade(ctx)
	if err != nil {
		t.Fatalf("get on a fresh store: %v", err)
	}
	if got != nil {
		t.Fatalf("a deployment with no upgrade scheduled reads %+v, want nil", got)
	}
	if cleared, err := store.ClearScheduledUpgrade(ctx); err != nil || cleared {
		t.Fatalf("clearing a schedule that was never made = (%v, %v), want (false, nil)", cleared, err)
	}

	want := model.ScheduledUpgrade{
		Branch:      "release-2026-03",
		RequestedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		By:          model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
	}
	if err := store.SetScheduledUpgrade(ctx, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err = store.GetScheduledUpgrade(ctx)
	if err != nil || got == nil {
		t.Fatalf("get after set: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}

	// Somebody changing their mind about the branch before the
	// deployment has gone idle: the second answer is the one they mean.
	second := model.ScheduledUpgrade{
		Branch:      "main",
		RequestedAt: want.RequestedAt.Add(time.Minute),
		By:          want.By,
	}
	if err := store.SetScheduledUpgrade(ctx, second); err != nil {
		t.Fatalf("set a second time: %v", err)
	}
	got, err = store.GetScheduledUpgrade(ctx)
	if err != nil || got == nil {
		t.Fatalf("get after the second set: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, second) {
		t.Fatalf("got %+v, want %+v", *got, second)
	}

	cleared, err := store.ClearScheduledUpgrade(ctx)
	if err != nil || !cleared {
		t.Fatalf("clear = (%v, %v), want (true, nil)", cleared, err)
	}
	got, err = store.GetScheduledUpgrade(ctx)
	if err != nil || got != nil {
		t.Fatalf("get after clear: (%+v, %v), want (nil, nil)", got, err)
	}
}

// grain/task-24: one repo's own configuration, the per-repo layer of
// what grain_config says deployment-wide. Round-trips, replaces rather
// than accumulating, and lists only repos that actually say something.
func TestRepoConfigRoundTripsAndReplaces(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}

	// A repo nobody has configured reads back as nil, with no error --
	// the same "nothing configured one yet" GetQualificationPlan gives.
	got, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatalf("get on an unconfigured repo: %v", err)
	}
	if got != nil {
		t.Fatalf("get on an unconfigured repo = %+v, want nil", got)
	}

	want := model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"gcp-key", "github-sandbox"},
		// The second field this row carries (grain/task-114), stored
		// beside the first rather than instead of it: both are written
		// through the same wholesale replace, so a put that named only
		// one of them and dropped the other is exactly what this pins.
		PromptExtension: "Widgets keeps its migrations in db/.",
	}
	if err := store.PutRepoConfig(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err = store.GetRepoConfig(ctx, widgets)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}

	// Replaced wholesale, not merged into.
	want.DefaultCapabilities = []string{"gemini-key"}
	if err := store.PutRepoConfig(ctx, want); err != nil {
		t.Fatalf("second put: %v", err)
	}
	got, err = store.GetRepoConfig(ctx, widgets)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}

	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: gadgets, DefaultCapabilities: []string{"gcp-key"},
	}); err != nil {
		t.Fatalf("put for a second repo: %v", err)
	}
	list, err := store.ListRepoConfigs(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	wantList := []model.RepoConfig{
		{Repo: gadgets, DefaultCapabilities: []string{"gcp-key"}},
		{Repo: widgets, DefaultCapabilities: []string{"gemini-key"},
			PromptExtension: "Widgets keeps its migrations in db/."},
	}
	if !reflect.DeepEqual(list, wantList) {
		t.Fatalf("list = %+v, want %+v, sorted by repo", list, wantList)
	}
}

// A config that says nothing leaves no row: "has a row" and "has
// something of its own to say" are one fact, so a repo whose last
// default capability is unticked stops being listed at all rather than
// lingering as an empty entry (model.RepoConfig.Empty).
func TestPutRepoConfigWithNothingToSayDeletesTheRow(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, DefaultCapabilities: []string{"gcp-key"},
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo}); err != nil {
		t.Fatalf("put with nothing to say: %v", err)
	}
	got, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("get = %+v, want nil", got)
	}
	list, err := store.ListRepoConfigs(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %+v, want no rows left", list)
	}
}

// The other half of that rule, since RepoConfig.Empty gained a second
// term with grain/task-114: standing instructions alone are something to
// say, so a repo that has unticked its last default capability but still
// carries a prompt extension keeps its row -- and stays listed, which is
// what puts it on the repos page and in `grain repo list` at all. A row
// dropped here would leave text that every run against the repo is still
// told but nothing admits exists.
func TestPutRepoConfigKeepsARowThatOnlyHasStandingInstructions(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	want := model.RepoConfig{Repo: repo, PromptExtension: "Read db/README.md before touching migrations."}
	if err := store.PutRepoConfig(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetRepoConfig(ctx, repo)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v), want the row to have survived", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
	list, err := store.ListRepoConfigs(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !reflect.DeepEqual(list, []model.RepoConfig{want}) {
		t.Fatalf("list = %+v, want %+v", list, []model.RepoConfig{want})
	}

	// A setup command alone is the third such term (grain/task-154), and
	// keeps the row for the same reason: it is run in every checkout made
	// for this repo, so a row dropped here would leave a command grain
	// still runs and nothing admits exists.
	setupOnly := model.RepoConfig{Repo: repo, SetupCommand: "make deps"}
	if err := store.PutRepoConfig(ctx, setupOnly); err != nil {
		t.Fatalf("put with only a setup command: %v", err)
	}
	if got, err := store.GetRepoConfig(ctx, repo); err != nil || got == nil ||
		!reflect.DeepEqual(*got, setupOnly) {
		t.Fatalf("get = (%+v, %v), want %+v", got, err, setupOnly)
	}

	// And clearing that last thing it had to say does delete it, exactly
	// as unticking a last capability does.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo}); err != nil {
		t.Fatalf("put with nothing to say: %v", err)
	}
	if got, err := store.GetRepoConfig(ctx, repo); err != nil || got != nil {
		t.Fatalf("get after clearing = (%+v, %v), want nil", got, err)
	}
}

// The jobs that judge a repository's base branches while its releases are
// off (grain/task-330). They round-trip like the capability lists beside
// them, and they are something to say on their own: a row dropped here
// would leave a repository that had deliberately kept the merge queue's
// compensating control with nothing recording that it had.
func TestPutRepoConfigKeepsTheJobsThatJudgeABaseBranch(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	want := model.RepoConfig{
		Repo:             repo,
		Work:             model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
		BaseBranchCIJobs: []string{".grain/workflows/tests.yml:go", "lint"},
	}
	if err := store.PutRepoConfig(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetRepoConfig(ctx, repo)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
	jobsOnly := model.RepoConfig{Repo: repo, BaseBranchCIJobs: []string{"go"}}
	if err := store.PutRepoConfig(ctx, jobsOnly); err != nil {
		t.Fatalf("put with only the judging jobs: %v", err)
	}
	if got, err := store.GetRepoConfig(ctx, repo); err != nil || got == nil ||
		!reflect.DeepEqual(*got, jobsOnly) {
		t.Fatalf("get = (%+v, %v), want %+v", got, err, jobsOnly)
	}
}

// And the jobs grain dispatches on a base branch after a merge whatever
// that repository's releases are doing (grain/task-409). They round-trip
// beside the judging jobs above and are something to say on their own
// for the same reason: with releases off they are the only thing still
// following the branch, so a row dropped here would stop a build cache
// being filled with nothing recording that anybody had asked for it.
func TestPutRepoConfigKeepsTheJobsDispatchedAfterAMerge(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	want := model.RepoConfig{
		Repo:                  repo,
		Work:                  model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
		PostMergeDispatchJobs: []string{".grain/workflows/image.yml:guest-cache", "warm"},
	}
	if err := store.PutRepoConfig(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetRepoConfig(ctx, repo)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
	dispatchOnly := model.RepoConfig{Repo: repo, PostMergeDispatchJobs: []string{"guest-cache"}}
	if err := store.PutRepoConfig(ctx, dispatchOnly); err != nil {
		t.Fatalf("put with only the dispatched jobs: %v", err)
	}
	if got, err := store.GetRepoConfig(ctx, repo); err != nil || got == nil ||
		!reflect.DeepEqual(*got, dispatchOnly) {
		t.Fatalf("get = (%+v, %v), want %+v", got, err, dispatchOnly)
	}
	list, err := store.ListRepoConfigs(ctx)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], dispatchOnly) {
		t.Fatalf("list = (%+v, %v), want just %+v -- the reconciler that dispatches these walks "+
			"the listing rather than reading one repository at a time", list, err, dispatchOnly)
	}
}

// PutConfig replaces the single row wholesale rather than accumulating a
// second one -- there is exactly one deployment configuration, the same
// discipline grain_schema holds to.
func TestPutConfigReplacesRatherThanAccumulating(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutConfig(ctx, testConfig()); err != nil {
		t.Fatalf("first put: %v", err)
	}
	updated := testConfig()
	updated.PollInterval = time.Minute
	updated.MaxWorkers = 1
	if err := store.PutConfig(ctx, updated); err != nil {
		t.Fatalf("second put: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, updated) {
		t.Fatalf("got %+v, want %+v", *got, updated)
	}
}

// grain/task-202: filing a task remembers which end of the backlog it
// joined, and does it through SetNewestFirst rather than through a
// read-modify-write of the whole row -- so it must move that one column
// and leave everything else stored exactly as it was, including whatever
// was written between the read and this write.
func TestSetNewestFirstLeavesEveryOtherSettingAlone(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	stored := testConfig()
	if err := store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.SetNewestFirst(ctx, true); err != nil {
		t.Fatalf("remembering the front of the backlog: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	want := stored
	want.NewestFirst = true
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}

	if err := store.SetNewestFirst(ctx, false); err != nil {
		t.Fatalf("remembering the end of the backlog: %v", err)
	}
	if got, err = store.GetConfig(ctx); err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, stored) {
		t.Fatalf("got %+v, want the original %+v back", *got, stored)
	}
}

// A deployment nothing has ever configured has no row to remember it in,
// and this must not write one: the row's existence is what
// ui.Settings.Configured reports, and a task filed before anyone opened
// Settings must not answer "has this deployment been configured" on an
// operator's behalf.
func TestSetNewestFirstWritesNoConfigRowOnAFreshDatabase(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.SetNewestFirst(ctx, true); err != nil {
		t.Fatalf("remembering with nothing stored: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil) still, got (%+v, %v)", got, err)
	}
}

// bwsalmon/agents#427: grain_config.target_repos did not exist at all
// before this, on any database, so an already-created grain_config table
// has no such column -- CREATE TABLE IF NOT EXISTS (schema.go's own
// Tables) does nothing to a table that's already there. This simulates
// exactly that database, built with the pre-#427 column set directly
// rather than through Store, and checks Store.Init's own migration step
// (ensureConfigTargetReposColumn) brings it up to date in place without
// disturbing the row already sitting in it.

// openWithBusyTimeout opens a database file with a shorter busy_timeout
// than sqlite.Open's own (five seconds -- too long for a test to wait
// out on purpose), so the two determinism tests above can force
// SQLITE_BUSY on a bounded, predictable schedule instead of racing for
// it.
func openWithBusyTimeout(t *testing.T, timeout time.Duration) (*sql.DB, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grain.db")
	q := url.Values{}
	q.Add("_pragma", "busy_timeout("+strconv.FormatInt(timeout.Milliseconds(), 10)+")")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		t.Fatalf("opening sqlite with a custom busy_timeout: %v", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		t.Fatalf("enabling WAL: %v", err)
	}
	return db, func() { db.Close() }
}

// --- backlog order (bwsalmon/agents#476) --------------------------------

func listedIDs(t *testing.T, store *model.Store, ctx context.Context) []string {
	t.Helper()
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	ids := make([]string, len(tasks))
	for i, tk := range tasks {
		ids[i] = tk.ID
	}
	return ids
}

// TestReadyAndListTasksFollowOrderKeyNotID puts three tasks in an order
// their ids alone would not produce, to pin down that both Ready (the
// dispatch order) and ListTasks (the backlog order a UI or CLI defaults
// to) sort by OrderKey rather than id or CreatedAt.
func TestReadyAndListTasksFollowOrderKeyNotID(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	first := task("c3d4", true)
	first.OrderKey = 10
	second := task("a1b2", true)
	second.OrderKey = 20
	third := task("z9y8", true)
	third.OrderKey = 30
	for _, tk := range []model.Task{third, first, second} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"c3d4", "a1b2", "z9y8"}; !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready = %v, want %v", ready, want)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"c3d4", "a1b2", "z9y8"}) {
		t.Fatalf("ListTasks = %v, want ascending OrderKey order", got)
	}
}

func TestOrderKeyForNewTaskExtendsOrJumpsTheQueue(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	// An empty backlog has no extreme to step past, either direction.
	for _, atFront := range []bool{false, true} {
		key, err := store.OrderKeyForNewTask(ctx, atFront)
		if err != nil || key != 0 {
			t.Fatalf("OrderKeyForNewTask(empty, %v) = (%v, %v), want (0, nil)", atFront, key, err)
		}
	}

	only := task("a1b2", true)
	only.OrderKey = 100
	if err := store.PutTask(ctx, only); err != nil {
		t.Fatal(err)
	}

	back, err := store.OrderKeyForNewTask(ctx, false)
	if err != nil || back <= 100 {
		t.Fatalf("OrderKeyForNewTask(atFront=false) = (%v, %v), want > 100", back, err)
	}
	front, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil || front >= 100 {
		t.Fatalf("OrderKeyForNewTask(atFront=true) = (%v, %v), want < 100", front, err)
	}
}

// TestReorderPlacesBetweenNeighbours drags one task at a time and checks
// the resulting backlog order, including the two unbounded cases
// (dropped at the very head or the very tail of the list) the issue's own
// "just before the following job if moved to the head of the list" calls
// out by name.
func TestReorderPlacesBetweenNeighbours(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for id, key := range map[string]float64{"a": 10, "b": 20, "c": 30} {
		tk := task(id, true)
		tk.OrderKey = key
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	after, before := ptr("a"), ptr("b")

	// c dropped between a and b.
	if err := store.Reorder(ctx, []string{"c"}, after, before); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"a", "c", "b"}) {
		t.Fatalf("after dropping c between a and b: %v, want [a c b]", got)
	}

	// b dropped at the very head -- no preceding job, so it goes just
	// before the (new) first task, a.
	if err := store.Reorder(ctx, []string{"b"}, nil, ptr("a")); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"b", "a", "c"}) {
		t.Fatalf("after dropping b at the head: %v, want [b a c]", got)
	}

	// a dropped at the very tail -- no following job.
	if err := store.Reorder(ctx, []string{"a"}, ptr("c"), nil); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"b", "c", "a"}) {
		t.Fatalf("after dropping a at the tail: %v, want [b c a]", got)
	}
}

// TestReorderMultiSelectKeepsRelativeOrder drags two tasks at once,
// passed in the opposite order from how they currently sit in the
// backlog, and checks Reorder still lands them as a block in their own
// existing relative order (a before c) rather than in whatever order the
// ids argument happened to list them.
func TestReorderMultiSelectKeepsRelativeOrder(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for id, key := range map[string]float64{"a": 10, "b": 20, "c": 30, "d": 40} {
		tk := task(id, true)
		tk.OrderKey = key
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	// ids names c before a -- the reverse of their current order -- to
	// prove Reorder sorts by OrderKey rather than trusting this order.
	if err := store.Reorder(ctx, []string{"c", "a"}, ptr("d"), nil); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"b", "d", "a", "c"}) {
		t.Fatalf("after multi-select drag: %v, want [b d a c] -- a and c keep their own relative order", got)
	}
}

// TestReorderRejectsAnUnknownID is TestReorderRejectsAStaleNeighbour's
// counterpart for ids itself: a task named in the drag rather than as a
// neighbour it was dropped against.
func TestReorderRejectsAnUnknownID(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.Reorder(ctx, []string{"gone"}, ptr("a1b2"), nil); err == nil {
		t.Fatal("Reorder with an unknown id succeeded, want an error")
	}
}

// TestReorderRejectsAStaleNeighbour is what a caller sees when the
// neighbour it computed the drop against (afterID or beforeID) no longer
// names a task -- closed, or reordered out from under it, between when it
// built the request and when this ran.
func TestReorderRejectsAStaleNeighbour(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.Reorder(ctx, []string{"a1b2"}, ptr("gone"), nil); err == nil {
		t.Fatal("Reorder with an unknown afterID succeeded, want an error")
	}
}

// TestReorderRebalancesWhenNeighboursAreCrowded forces two neighbours'
// OrderKey closer together than minOrderKeyGap allows splitting again,
// directly through the database rather than through repeated drags, and
// checks Reorder still lands a task strictly between them -- proof the
// rebalance backstop actually ran rather than Reorder simply producing an
// indistinguishable float and silently misplacing the drop.
func TestReorderRebalancesWhenNeighboursAreCrowded(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	for id, key := range map[string]float64{"a": 10, "b": 10 + 1e-9, "c": 30} {
		tk := task(id, true)
		tk.OrderKey = key
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	// Belt and braces: pin the two neighbours' keys directly, in case a
	// future change to PutTask ever stopped taking OrderKey as given.
	if _, err := db.ExecContext(ctx, "UPDATE `task` SET `order_key` = 10 WHERE `id` = 'a'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `task` SET `order_key` = 10.0000001 WHERE `id` = 'b'"); err != nil {
		t.Fatal(err)
	}

	if err := store.Reorder(ctx, []string{"c"}, ptr("a"), ptr("b")); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"a", "c", "b"}) {
		t.Fatalf("after dropping c into a crowded gap: %v, want [a c b]", got)
	}
}

func ptr[T any](v T) *T { return &v }

// --- the merge queue at the front of the backlog -------------------------

// putOrdered files tasks at the OrderKey each name maps to, so a test can
// state the backlog it starts from as the order itself rather than as a
// sequence of writes.
func putOrdered(t *testing.T, store *model.Store, ctx context.Context, keys map[string]float64) {
	t.Helper()
	for id, key := range keys {
		tk := task(id, true)
		tk.OrderKey = key
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
}

func orderKeys(t *testing.T, store *model.Store, ctx context.Context) map[string]float64 {
	t.Helper()
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	keys := map[string]float64{}
	for _, tk := range tasks {
		keys[tk.ID] = tk.OrderKey
	}
	return keys
}

// TestMoveToFrontOfBacklogCarriesTheQueuePastOrdinaryWork is the merge
// queue making its order visible: the tasks whose pull requests are
// waiting to land go to the front of the list, in the order they are
// listed, and everything else keeps its own.
func TestMoveToFrontOfBacklogCarriesTheQueuePastOrdinaryWork(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	putOrdered(t, store, ctx, map[string]float64{"ordinary": 10, "queued-2": 20, "queued-1": 5})

	// Passed in the order queued-2 then queued-1, so they land at the front
	// in that exact order rather than in the order the backlog already gave them.
	if err := store.MoveToFrontOfBacklog(ctx, []string{"queued-2", "queued-1"}); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"queued-2", "queued-1", "ordinary"}) {
		t.Fatalf("backlog = %v, want the two queued tasks in order at the front of the ordinary one", got)
	}
}

// TestMoveToFrontOfBacklogReordersABlockAlreadyAtTheFront: both tasks
// already sit ahead of the ordinary backlog, in the opposite order to the
// one asked for. The move still happens -- "already at the front" is
// about the order given, not the order the keys happen to hold
// (grain/task-545: a task filed at the front joins the merge queue behind
// its members, and this is the write that records it).
func TestMoveToFrontOfBacklogReordersABlockAlreadyAtTheFront(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	putOrdered(t, store, ctx, map[string]float64{"ordinary": 10, "member": 0, "newcomer": -5})

	if err := store.MoveToFrontOfBacklog(ctx, []string{"member", "newcomer"}); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"member", "newcomer", "ordinary"}) {
		t.Fatalf("backlog = %v, want the newcomer moved behind the member", got)
	}
}

// TestMoveToFrontOfBacklogStaysBehindAFixTaskAtTheHead is the other half
// of the ordering: one of the separate merge tasks the queue used to file
// sits at the very head, so the queue it repairs goes in front of the
// ordinary backlog but behind that.
func TestMoveToFrontOfBacklogStaysBehindAFixTaskAtTheHead(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	putOrdered(t, store, ctx, map[string]float64{"ordinary": 10, "queued": 20})
	fix := task("fix", true)
	fix.Origin.Reason = model.ReasonFix
	fix.OrderKey = -10
	if err := store.PutTask(ctx, fix); err != nil {
		t.Fatal(err)
	}

	if err := store.MoveToFrontOfBacklog(ctx, []string{"queued"}); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"fix", "queued", "ordinary"}) {
		t.Fatalf("backlog = %v, want the fix task still at the very head", got)
	}
}

// TestMoveToFrontOfBacklogIgnoresAFixTaskInsideTheBacklog is the same
// question asked of a fix task that is *not* at the head -- dragged down
// by a human, or filed before fix tasks were placed at all and left at
// OrderKey's zero value. It bounds nothing: the queue goes to the front
// of the whole list, which is the only place it can be seen.
func TestMoveToFrontOfBacklogIgnoresAFixTaskInsideTheBacklog(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	putOrdered(t, store, ctx, map[string]float64{"ordinary": 10, "queued": 30})
	fix := task("fix", true)
	fix.Origin.Reason = model.ReasonFix
	fix.OrderKey = 20
	if err := store.PutTask(ctx, fix); err != nil {
		t.Fatal(err)
	}

	if err := store.MoveToFrontOfBacklog(ctx, []string{"queued"}); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"queued", "ordinary", "fix"}) {
		t.Fatalf("backlog = %v, want the queue in front of a fix task sitting inside the backlog", got)
	}
}

// TestMoveToFrontOfBacklogWritesNothingWhenTheOrderAlreadyHolds is what
// makes it safe for orchestrator.SyncPullRequests to call every cycle: an
// order already correct is left exactly as it is, rather than rewritten to
// the same order with fresh keys that crowd every later split.
func TestMoveToFrontOfBacklogWritesNothingWhenTheOrderAlreadyHolds(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	putOrdered(t, store, ctx, map[string]float64{"queued-1": 5, "queued-2": 6, "ordinary": 10})

	before := orderKeys(t, store, ctx)
	if err := store.MoveToFrontOfBacklog(ctx, []string{"queued-1", "queued-2"}); err != nil {
		t.Fatal(err)
	}
	if after := orderKeys(t, store, ctx); !reflect.DeepEqual(after, before) {
		t.Fatalf("order keys changed from %v to %v with nothing out of place", before, after)
	}
}

// TestMoveToFrontOfBacklogRebalancesWhenTheFrontIsCrowded is Reorder's own
// backstop reached from here: the gap between the fix task at the head and
// the first ordinary task is too fine to split two keys out of, so the
// whole backlog is renumbered first and the move still lands in order.
func TestMoveToFrontOfBacklogRebalancesWhenTheFrontIsCrowded(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	putOrdered(t, store, ctx, map[string]float64{"ordinary": 10, "queued-1": 20, "queued-2": 30})
	fix := task("fix", true)
	fix.Origin.Reason = model.ReasonFix
	fix.OrderKey = 9.99999999
	if err := store.PutTask(ctx, fix); err != nil {
		t.Fatal(err)
	}
	// Belt and braces, the same as TestReorderRebalancesWhenNeighboursAreCrowded.
	if _, err := db.ExecContext(ctx, "UPDATE `task` SET `order_key` = 9.99999999 WHERE `id` = 'fix'"); err != nil {
		t.Fatal(err)
	}

	if err := store.MoveToFrontOfBacklog(ctx, []string{"queued-1", "queued-2"}); err != nil {
		t.Fatal(err)
	}
	if got := listedIDs(t, store, ctx); !reflect.DeepEqual(got, []string{"fix", "queued-1", "queued-2", "ordinary"}) {
		t.Fatalf("backlog = %v, want the queue landed between a crowded head and the backlog", got)
	}
}

func TestMoveToFrontOfBacklogRejectsAnUnknownID(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.MoveToFrontOfBacklog(ctx, []string{"gone"}); err == nil {
		t.Fatal("MoveToFrontOfBacklog with an unknown id succeeded, want an error")
	}
}

// TestReadyDispatchesAFixTaskInBacklogOrderLikeAnythingElse pins the
// carve-out Store.Ready used to make for Origin.Reason == ReasonFix being
// gone: a fix task is dispatched first because it was filed at the head
// of the backlog, and one a human has since dragged behind other work
// waits its turn there like anything else. Dispatch order is the order on screen, both ways round.
func TestReadyDispatchesAFixTaskInBacklogOrderLikeAnythingElse(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	putOrdered(t, store, ctx, map[string]float64{"ordinary": 10})
	fix := task("fix", true)
	fix.Origin.Reason = model.ReasonFix
	fix.OrderKey = 20
	if err := store.PutTask(ctx, fix); err != nil {
		t.Fatal(err)
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ordinary", "fix"}; !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready = %v, want %v -- a fix task behind other work in the backlog waits behind it", ready, want)
	}
}

// --- backlog order settings (bwsalmon/agents#476) ------------------------

// TestInitMigratesAnExistingDatabaseMissingAgentGitIdentity is the same
// pattern again, applied to grain_config.agent_git_name/agent_git_email
// (grain/task-14). Both backfill to the empty string, which is not an
// unusable identity but grain's own -- so a deployment upgrading across
// these columns goes on committing under exactly the name it always did,
// and only starts committing under another once somebody chooses one.

// --- the prompt extension's three columns (grain/task-114) ---------------
//
// One migration each, on three tables every existing deployment already
// has rows in, and CREATE TABLE IF NOT EXISTS never alters one of those
// -- so each is simulated here directly: the table as it was, a row in
// it, then Init, then the column read and written for real. Getting any
// of the three wrong breaks an upgraded deployment rather than a fresh
// one, which is the failure nothing else in this suite would see.

// The other direction, which has always been refused: a store written by
// a build that knows a later schema.
func TestInitRefusesAStoreNewerThanThisBuild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", model.SchemaVersion+1); err != nil {
		t.Fatal(err)
	}

	if err := model.New(db).Init(ctx); !errors.Is(err, model.ErrSchemaTooNew) {
		t.Fatalf("Init against a newer store = %v, want ErrSchemaTooNew", err)
	}
}

// A store this build itself just created re-opens without complaint --
// the ordinary daemon restart, which neither guard may catch.
func TestInitAcceptsAStoreAtThisBuildsOwnVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("re-opening a store this build created: %v", err)
	}
}

// StrandedByTargetRepos is the third question asked of this one list --
// after "may this repo be dispatched into" (RepoAllowed) and "which
// repos does this deployment work in" (WorkRepos): what a narrowing of
// it would leave behind (grain/task-411). Its callers are ui.PlanSettings
// and ui.UpdateSettings, which is where an operator is told before and
// as they narrow it.
func TestStrandedByTargetReposReportsWhatANarrowingLeavesBehind(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	kept := model.RepoRef{Owner: "owner", Name: "payments-api"} // task()'s own target
	losing := model.RepoRef{Owner: "owner", Name: "billing"}

	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	// Filed under another spelling of the repo coming off the list --
	// which is exactly the task most likely to be missed, and one
	// repository to GitHub either way (RepoRef.Same).
	stranded := task("c3d4", true)
	stranded.Target = &model.RepoRef{Owner: "OWNER", Name: "Billing"}
	if err := store.PutTask(ctx, stranded); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "s1", Title: "nightly", Target: losing, Enabled: true,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		CreatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	// A paused schedule fires nothing, so it strands nothing: counting it
	// would pad the number this exists to make honest.
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "s2", Title: "paused", Target: losing, Enabled: false,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		CreatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTargetRepos(ctx, []string{kept.String(), losing.String()}); err != nil {
		t.Fatal(err)
	}

	got, err := store.StrandedByTargetRepos(ctx, []string{kept.String()})
	if err != nil {
		t.Fatalf("StrandedByTargetRepos: %v", err)
	}
	if len(got) != 1 || !got[0].Repo.Same(losing) {
		t.Fatalf("stranded = %+v, want just %s", got, losing)
	}
	if len(got[0].Tasks) != 1 || got[0].Tasks[0] != "c3d4" {
		t.Errorf("stranded tasks = %v, want the task filed under the other spelling", got[0].Tasks)
	}
	if !reflect.DeepEqual(got[0].Schedules, []string{"s1"}) {
		t.Errorf("stranded schedules = %v, want only the enabled one", got[0].Schedules)
	}
	if got[0].QualificationPlan {
		t.Error("qualificationPlan = true, want false: no plan is configured for that repo")
	}

	// The narrowings that strand nothing, which are most of them: the
	// same list again, a wider one, and the empty list that means
	// unrestricted.
	for _, proposed := range [][]string{
		{kept.String(), losing.String()},
		{kept.String(), losing.String(), "owner/docs"},
		nil,
	} {
		got, err := store.StrandedByTargetRepos(ctx, proposed)
		if err != nil {
			t.Fatalf("StrandedByTargetRepos(%v): %v", proposed, err)
		}
		if len(got) != 0 {
			t.Errorf("StrandedByTargetRepos(%v) = %+v, want nothing stranded", proposed, got)
		}
	}
}

// TestTaskTagIndexUsed verifies that task_tag has the task_tag_tag index
// and SQLite's query planner uses it for TaskIDWithTag instead of a full
// table scan.
func TestTaskTagIndexUsed(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	// Seed multiple tasks with various tags.
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("task-%03d", i)
		tk := task(id, true)
		tk.Tags = []string{fmt.Sprintf("tag-%d", i%5)}
		if i == 42 {
			tk.Tags = append(tk.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask: %v", err)
		}
	}

	foundID, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil {
		t.Fatalf("TaskIDWithTag: %v", err)
	}
	if foundID != "task-042" {
		t.Fatalf("TaskIDWithTag = %q, want task-042", foundID)
	}

	// Verify EXPLAIN QUERY PLAN uses index task_tag_tag.
	tagQuery := "EXPLAIN QUERY PLAN SELECT `tg`.`task_id` FROM `task_tag` AS `tg` " +
		"JOIN `task` AS `t` ON `t`.`id` = `tg`.`task_id` " +
		"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `tg`.`task_id` " +
		"WHERE `tg`.`tag` = ? " +
		"ORDER BY (`o`.`closed_at` IS NOT NULL), `t`.`created_at` DESC, `tg`.`task_id` DESC LIMIT 1"
	rows, err := db.QueryContext(ctx, tagQuery, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var planLines []string
	usesIndex := false
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scanning query plan: %v", err)
		}
		planLines = append(planLines, detail)
		if strings.Contains(detail, "task_tag_tag") {
			usesIndex = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading query plan: %v", err)
	}
	if !usesIndex {
		t.Fatalf("TaskIDWithTag query plan did not use task_tag_tag index:\n%s", strings.Join(planLines, "\n"))
	}
}

func TestTaskAndTaskRunCompositeIndexes(t *testing.T) {
	t.Parallel()
	_, db, ctx := openStore(t)

	indexes := []string{
		"task_run_task_outcome_started",
		"task_run_started",
	}

	for _, idx := range indexes {
		var name string
		err := db.QueryRowContext(ctx,
			"SELECT `name` FROM `sqlite_master` WHERE `type` = 'index' AND `name` = ?",
			idx).Scan(&name)
		if err != nil {
			t.Errorf("expected index %q to exist in initialized database, but got error: %v", idx, err)
		}
	}
}

func TestTaskIssueRoundTrip(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	tk := task("issue-task", true)
	tk.Issue = 42

	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	got, err := store.GetTask(ctx, "issue-task")
	if err != nil || got == nil {
		t.Fatalf("GetTask: (%+v, %v)", got, err)
	}
	if got.Issue != 42 {
		t.Errorf("got.Issue = %d, want 42", got.Issue)
	}

	list, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var found *model.Task
	for _, tk := range list {
		if tk.ID == "issue-task" {
			found = &tk
			break
		}
	}
	if found == nil {
		t.Fatalf("task issue-task not found in ListTasks")
	}
	if found.Issue != 42 {
		t.Errorf("found.Issue in ListTasks = %d, want 42", found.Issue)
	}
}

// TestNothingNeededActionRoundTripsAndClosesThroughTheView is the store
// half of the nothing_needed parity: the setting survives a write and a
// read, and the two observations ProcessResult writes for it read through
// the task_state view as StateOf reads them (closed for "close",
// awaiting_ack for "ack").
func TestNothingNeededActionRoundTripsAndClosesThroughTheView(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for id, tc := range map[string]struct {
		action model.NothingNeededAction
		closed bool
		want   model.State
	}{
		"nn-close":   {model.NothingNeededClose, true, model.StateClosed},
		"nn-default": {"", true, model.StateClosed},
		"nn-ack":     {model.NothingNeededAck, false, model.StateAwaitingReply},
	} {
		tk := task(id, true)
		tk.NothingNeededAction = tc.action
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetTask(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.NothingNeededAction != tc.action {
			t.Errorf("%s: NothingNeededAction = %q, want %q", id, got.NothingNeededAction, tc.action)
		}
		if err := store.ObserveField(ctx, id, now, func(o *model.Observation) {
			o.CompletedAt = &now
			if tc.closed {
				o.ClosedAt = &now
			}
		}); err != nil {
			t.Fatal(err)
		}
		state, err := store.State(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if goState := model.StateOf(*got, obs, false, 0); state != tc.want || goState != tc.want {
			t.Errorf("%s: view = %q, StateOf = %q, want %q", id, state, goState, tc.want)
		}
	}
}

// TaskRepos is ListTasks narrowed to a task's target, and
// must agree with it on both -- the empty team reading as the default
// one, and a task with no target having none.
func TestTaskReposAgreesWithListTasks(t *testing.T) {
	store, _, ctx := openStore(t)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, task := range []model.Task{
		{ID: "a", Title: "a", Target: &widgets},
		{ID: "b", Title: "b"},
	} {
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.TaskRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(tasks) {
		t.Fatalf("TaskRepos listed %d tasks, ListTasks %d", len(got), len(tasks))
	}
	for i, task := range tasks {
		want := model.TaskRepo{ID: task.ID, Target: task.Target}
		if !reflect.DeepEqual(got[i], want) {
			t.Errorf("TaskRepos[%d] = %+v, want %+v", i, got[i], want)
		}
	}
	if got[1].Target != nil {
		t.Errorf("task b = %+v, want no target", got[1])
	}
}

// A store from before the single-tenant baseline was written by a build
// that could serve several tenants, and this build has no migration from
// it: Init refuses it in the startup guard's words, naming both versions,
// so the failure names itself once at startup rather than as a missing
// column a tick at a time.
func TestInitRefusesAStoreFromBeforeTheBaseline(t *testing.T) {
	t.Parallel()
	_, db, ctx := openStore(t)
	old := model.BaselineSchemaVersion - 1
	if _, err := db.ExecContext(ctx,
		"UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", old); err != nil {
		t.Fatal(err)
	}

	err := model.New(db).Init(ctx)
	if !errors.Is(err, model.ErrBeforeBaseline) {
		t.Fatalf("Init against a store from before the baseline = %v, want ErrBeforeBaseline", err)
	}
	// The store holds no tenancy rows, so the refusal must not say it does
	// (task 4800): most stores from before the reset were single-tenant.
	if errors.Is(err, model.ErrMultiTenantData) || strings.Contains(err.Error(), "multi-tenant") {
		t.Errorf("the refusal of a store with no tenancy rows says multi-tenant: %v", err)
	}
	if !strings.Contains(err.Error(), "this data dir") || !strings.Contains(err.Error(), "Nothing in this data dir has been changed") {
		t.Errorf("the refusal of a store should talk about the data dir: %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(old)) ||
		!strings.Contains(err.Error(), strconv.Itoa(model.BaselineSchemaVersion)) {
		t.Errorf("the error should name both versions, got: %v", err)
	}
}

// A store at or past the baseline but before V2BaselineSchemaVersion is
// refused as merely old; stores at or past V2BaselineSchemaVersion are
// migrated in place (pkg/model/v2baseline).
func TestInitRefusesAStoreOlderThanThisBuild(t *testing.T) {
	t.Parallel()
	if model.V2BaselineSchemaVersion == model.BaselineSchemaVersion {
		t.Skip("this build is at the baseline, so no version is both after it and older than this build")
	}
	_, db, ctx := openStore(t)
	if _, err := db.ExecContext(ctx,
		"UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", model.V2BaselineSchemaVersion-1); err != nil {
		t.Fatal(err)
	}
	err := model.New(db).Init(ctx)
	if !errors.Is(err, model.ErrSchemaTooOld) {
		t.Fatalf("Init against an older store = %v, want ErrSchemaTooOld", err)
	}
	// The way out the refusal names first is the one that keeps the rows
	// (grain/task-4851): an operator who followed its older advice -- move
	// the store aside and reformat -- came back to an empty deployment.
	msg := err.Error()
	prepare, reformat := strings.Index(msg, "prepare-upgrade"), strings.Index(msg, "state reformat")
	if prepare < 0 || (reformat >= 0 && reformat < prepare) {
		t.Errorf("the refusal should name `grain state prepare-upgrade` before any reset: %v", err)
	}
	if strings.Contains(msg, "../scripts") {
		t.Errorf("the refusal names a path relative to the source tree: %v", err)
	}
}

// A store Init refuses is one the operator moves aside and keeps, so Init
// refuses it before touching anything in it.
func TestInitLeavesAStoreItRefusesUntouched(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutConfig(ctx, testConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE `scheduled_upgrade`"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("UPDATE `grain_schema` SET `version` = %d WHERE `id` = 1",
		model.BaselineSchemaVersion-1)); err != nil {
		t.Fatal(err)
	}

	if err := model.New(db).Init(ctx); !errors.Is(err, model.ErrBeforeBaseline) {
		t.Fatalf("Init against a store from before the baseline = %v, want ErrBeforeBaseline", err)
	}
	if _, err := db.ExecContext(ctx, "SELECT 1 FROM `scheduled_upgrade` WHERE 1 = 0"); err == nil {
		t.Error("Init created a table in a store it then refused")
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT `version` FROM `grain_schema` WHERE `id` = 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != model.BaselineSchemaVersion-1 {
		t.Errorf("Init restamped a store it refused: version %d", version)
	}
}

// TestAChangelistIsTheTasksChangeLikeAPullRequest holds task_state and
// model.StateOf to one answer for a changelist task whose change is a CL
// (model.LinkChangelist, docs/design/changelists.md#G3-2): a
// finished task with a live CL is awaiting_submit, not awaiting_reply,
// and a task with several CLs is still exactly one row of the view.
func TestAChangelistIsTheTasksChangeLikeAPullRequest(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	done := now.Add(time.Hour)
	for id, links := range map[string][]model.Link{
		"cl01": {{Kind: model.LinkChangelist, Target: "cl/612345678"}},
		"cl02": {
			{Kind: model.LinkChangelist, Target: "cl/612345678"},
			{Kind: model.LinkChangelist, Target: "cl/612345690"},
			{Kind: model.LinkChangelist, Target: "cl/612345701"},
		},
	} {
		tk := task(id, true)
		tk.ChangeTarget = &model.ChangeTarget{Kind: "changelist"}
		tk.Links = links
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if err := store.Observe(ctx, model.Observation{TaskID: id, CompletedAt: &done}); err != nil {
			t.Fatal(err)
		}
		got, err := store.State(ctx, id)
		if err != nil {
			t.Fatalf("state %s: %v", id, err)
		}
		if got != model.StateAwaitingSubmit {
			t.Errorf("task %s with %d CL(s): state = %q, want %q", id, len(links), got, model.StateAwaitingSubmit)
		}
		stored, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if derived := model.StateOf(*stored, obs, false, 0); derived != got {
			t.Errorf("task %s: task_state view = %q, model.StateOf = %q", id, got, derived)
		}
		var rows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_state` WHERE `task_id` = ?", id).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Errorf("task %s with %d CL links has %d task_state rows, want 1", id, len(links), rows)
		}
	}
}

// TaskCIRef names the repository a task works in and the branch fixed
// on it, which is what `grain ci list --task` and `grain pr status --task`
// read CI under; and a live run's scope may read the task:// records of
// other tasks only in the repositories an api:tasks:read@repo grant
// names -- not its own target or the ones it reads.
func TestTaskCIRefAndTaskReadRepos(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("f1e2", true)
	tk.Branch = "grain/dep/task-f1e2"
	tk.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	tk.Grants = []model.Grant{{Capability: "api:tasks:read@other/repo", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	repo, branch, found, err := store.TaskCIRef(ctx, "#f1e2")
	if err != nil || !found || repo != tk.Target.String() || branch != "grain/dep/task-f1e2" {
		t.Errorf("TaskCIRef = (%q, %q, %v, %v), want (%q, grain/dep/task-f1e2, true, nil)", repo, branch, found, err, tk.Target.String())
	}
	if _, _, found, err := store.TaskCIRef(ctx, "nope"); found || err != nil {
		t.Errorf("TaskCIRef of a missing task = (found %v, %v), want (false, nil)", found, err)
	}

	if err := store.StartRun(ctx, model.Run{
		ID: "f1e2-r1", TaskID: "f1e2", Sandbox: "sandbox-taskrepos", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	scope, live, err := store.SandboxNamespaceScope(ctx, "sandbox-taskrepos")
	if err != nil || !live {
		t.Fatalf("SandboxNamespaceScope = (live %v, %v)", live, err)
	}
	if scope.MayReadAllTasks {
		t.Errorf("a repository-limited tasks:read reads every task: %+v", scope)
	}
	if !scope.TaskReadRepos["other/repo"] {
		t.Errorf("other/repo not in TaskReadRepos %v", scope.TaskReadRepos)
	}
	for r := range scope.TaskReadRepos {
		if r != "other/repo" {
			t.Errorf("TaskReadRepos %v holds %s, which no api:tasks:read grant names", scope.TaskReadRepos, r)
		}
	}
}

// A live run of a task with a target, reads and writes but no api: grant
// reads no other task's task:// record: its repositories are its git and
// CI scope, not a grant to read the tasks working in them.
func TestSandboxNamespaceScopeReadsNoSiblingTasksWithoutAGrant(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tk := task("f1e3", true)
	tk.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	tk.Writes = []model.WriteTarget{{Repo: model.RepoRef{Owner: "owner", Name: "deploy-config"}, Base: "deploy"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "f1e3-r1", TaskID: "f1e3", Sandbox: "sandbox-nogrant", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	scope, live, err := store.SandboxNamespaceScope(ctx, "sandbox-nogrant")
	if err != nil || !live {
		t.Fatalf("SandboxNamespaceScope = (live %v, %v)", live, err)
	}
	if scope.MayReadAllTasks || len(scope.TaskReadRepos) != 0 {
		t.Errorf("a run with no tasks:read grant reads other tasks: MayReadAllTasks %v, TaskReadRepos %v",
			scope.MayReadAllTasks, scope.TaskReadRepos)
	}
	// Its repositories are still its git and CI scope.
	for _, r := range []string{tk.Target.String(), "owner/shared-lib", "owner/deploy-config"} {
		if !scope.AllowedRepos[r] {
			t.Errorf("%s not in AllowedRepos %v", r, scope.AllowedRepos)
		}
	}
}
