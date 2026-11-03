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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The daemon's store is what the poll and the goal check act through.
var (
	_ ChangeOutcomeStore = (*model.Store)(nil)
	_ CLGoalStore        = (*model.Store)(nil)
)

// landingCLs is a changelist provider whose poll and Observe answer what
// a test sets, and whose Sync counts.
type landingCLs struct {
	polled  map[string]change.Observation
	errs    map[string]error
	syncs   int
	syncErr error
}

func (p *landingCLs) Kind() model.LinkKind { return model.LinkChangelist }
func (p *landingCLs) Observe(_ context.Context, _ model.Task, target string) (change.Observation, error) {
	if err := p.errs[target]; err != nil {
		return change.Observation{}, err
	}
	return p.polled[target], nil
}
func (p *landingCLs) Threads(context.Context, model.Task, string) ([]change.Thread, error) {
	return nil, nil
}
func (p *landingCLs) Submit(context.Context, model.Task, string, change.Person) (change.Result, error) {
	return change.Result{}, change.ErrNotSupported
}
func (p *landingCLs) Sync(context.Context, model.Task, string) (change.SyncResult, error) {
	p.syncs++
	return change.SyncResult{Message: "synced to CL 9"}, p.syncErr
}
func (p *landingCLs) SyncAfter() time.Duration { return time.Hour }
func (p *landingCLs) PollTick(_ context.Context, tasks []model.Task) []change.Observation {
	var out []change.Observation
	for _, t := range tasks {
		for _, cl := range change.LiveChanges(t) {
			if o, ok := p.polled[cl]; ok {
				out = append(out, o)
			}
		}
	}
	return out
}

func registerLanding(t *testing.T, p *landingCLs) {
	t.Helper()
	change.Register(p)
	t.Cleanup(func() {
		change.Unregister(model.LinkChangelist)
		for target := range p.polled {
			change.Forget(target)
		}
	})
}

func seedCLTask(t *testing.T, ctx context.Context, store *model.Store, id string, cls ...string) {
	t.Helper()
	var links []model.Link
	for _, cl := range cls {
		links = append(links, model.Link{Kind: model.LinkChangelist, Target: cl})
	}
	if err := store.PutTask(ctx, model.Task{ID: id, Title: "t", Intent: model.IntentImplement, Links: links,
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.ObserveField(ctx, id, now, func(o *model.Observation) { o.CompletedAt = &now }); err != nil {
		t.Fatal(err)
	}
}

func commentsOf(t *testing.T, ctx context.Context, store *model.Store, id string) string {
	t.Helper()
	cs, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Body + "\n")
	}
	return b.String()
}

func TestPollClosesACLSubmittedOutsideGrainAndCountsItAsMerged(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	p := &landingCLs{polled: map[string]change.Observation{"cl/5": {Target: "cl/5", Status: change.StatusSubmitted}}}
	registerLanding(t, p)
	seedCLTask(t, ctx, store, "50", "cl/5")

	PollChangesOnce(ctx, store)
	if st, _ := store.State(ctx, "50"); st != model.StateClosed {
		t.Fatalf("state %s, want closed", st)
	}
	obs, _ := store.GetObservation(ctx, "50")
	if obs.PrMergedAt == nil {
		t.Fatal("no merged moment recorded")
	}
	if c := commentsOf(t, ctx, store, "50"); !strings.Contains(c, "submitted outside grain") {
		t.Fatalf("comments %q", c)
	}
	timings, err := store.TaskTimings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range timings {
		if tt.TaskID == "50" && (tt.ChangeKind != model.LinkChangelist || tt.PrMergedAt == nil) {
			t.Fatalf("timing %+v, want a merged changelist", tt)
		}
	}
}

func TestPollMarksAnAbandonedCLAndReturnsATaskWithNoneLiveToAwaitingReply(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	p := &landingCLs{polled: map[string]change.Observation{
		"cl/6": {Target: "cl/6", Status: change.StatusAbandoned},
		"cl/7": {Target: "cl/7", Status: change.StatusMailed},
		"cl/8": {Target: "cl/8", Status: change.StatusAbandoned},
	}}
	registerLanding(t, p)
	seedCLTask(t, ctx, store, "60", "cl/6", "cl/7") // another CL stays live
	seedCLTask(t, ctx, store, "61", "cl/8")         // none left

	PollChangesOnce(ctx, store)
	if st, _ := store.State(ctx, "60"); st != model.StateAwaitingSubmit {
		t.Fatalf("task 60 state %s, want still awaiting_submit on cl/7", st)
	}
	task, _ := store.GetTask(ctx, "60")
	if live := change.LiveChanges(*task); len(live) != 1 || live[0] != "cl/7" {
		t.Fatalf("live = %v, want cl/7", live)
	}
	if st, _ := store.State(ctx, "61"); st != model.StateAwaitingReply {
		t.Fatalf("task 61 state %s, want awaiting_reply", st)
	}
	if c := commentsOf(t, ctx, store, "61"); !strings.Contains(c, "no live CL left") {
		t.Fatalf("comments %q", c)
	}
	// Polling again changes nothing: the abandoned CL is settled.
	PollChangesOnce(ctx, store)
	if n := strings.Count(commentsOf(t, ctx, store, "60"), "abandoned"); n != 1 {
		t.Fatalf("task 60 told %d times", n)
	}
}

func TestPollSendsATaskWhoseOnlyCLIsEmptyOrDeletedToAwaitingReply(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	p := &landingCLs{polled: map[string]change.Observation{
		"cl/10": {Target: "cl/10", Status: change.StatusPending, Empty: true},
		"cl/11": {Target: "cl/11", Status: change.StatusAbandoned}, // citc_cl_delete: state "deleted"
		"cl/12": {Target: "cl/12", Status: change.StatusPending, Empty: true},
		"cl/13": {Target: "cl/13", Status: change.StatusPending, Empty: true},
	}}
	registerLanding(t, p)
	seedCLTask(t, ctx, store, "62", "cl/10")
	seedCLTask(t, ctx, store, "63", "cl/11")
	if err := store.PutTask(ctx, model.Task{ID: "64", Title: "t", Intent: model.IntentImplement,
		Links: []model.Link{{Kind: model.LinkChangelist, Target: "cl/12"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "64-1", TaskID: "64", Attempt: 1, StartedAt: time.Now()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "64"); st != model.StateRunning {
		t.Fatalf("task 64 state %s, want running", st)
	}
	if err := store.PutTask(ctx, model.Task{ID: "65", Title: "t", Intent: model.IntentImplement,
		Links:    []model.Link{{Kind: model.LinkChangelist, Target: "cl/13"}},
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "65"); st != model.StateQueued {
		t.Fatalf("task 65 state %s, want queued", st)
	}

	PollChangesOnce(ctx, store)
	for id, want := range map[string]string{"62": "cl/10 holds no files", "63": "cl/11 was abandoned or deleted."} {
		if st, _ := store.State(ctx, id); st != model.StateAwaitingReply {
			t.Fatalf("task %s state %s, want awaiting_reply", id, st)
		}
		task, _ := store.GetTask(ctx, id)
		if live := change.LiveChanges(*task); len(live) != 0 {
			t.Fatalf("task %s live = %v, want none", id, live)
		}
		if c := commentsOf(t, ctx, store, id); !strings.Contains(c, want) || !strings.Contains(c, "no live CL left") {
			t.Fatalf("task %s comments %q, want %q", id, c, want)
		}
	}
	// A run may still upload to the CL it is working on, and a queued
	// task's next run may upload to its CL.
	for id, cl := range map[string]string{"64": "cl/12", "65": "cl/13"} {
		task, _ := store.GetTask(ctx, id)
		if live := change.LiveChanges(*task); len(live) != 1 {
			t.Fatalf("task %s live = %v, want %s left alone", id, live, cl)
		}
	}
	PollChangesOnce(ctx, store)
	if n := strings.Count(commentsOf(t, ctx, store, "62"), "holds no files"); n != 1 {
		t.Fatalf("task 62 told %d times", n)
	}
}

func TestAutoSyncFiresOncePerSnapshotNeverDuringARunAndNotAfterAFailure(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	p := &landingCLs{}
	seedCLTask(t, ctx, store, "70", "cl/70")
	task, _ := store.GetTask(ctx, "70")
	now := time.Now()
	for _, tc := range []struct {
		name string
		obs  change.Observation
		want string
	}{
		{"behind", change.Observation{Target: "cl/70", Snapshot: 1, Health: change.HealthBehind}, change.SyncBehind},
		{"conflicted", change.Observation{Target: "cl/70", Snapshot: 1, Health: change.HealthConflicted}, change.SyncConflicted},
		{"stale", change.Observation{Target: "cl/70", Snapshot: 1, Health: change.HealthOK, SnapshotAt: now.Add(-2 * time.Hour)}, change.SyncStale},
		{"needs sync", change.Observation{Target: "cl/70", Snapshot: 1, Health: change.HealthOK}, change.SyncNeedsSync},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := change.NewSyncPlanner()
			if tc.want == change.SyncNeedsSync {
				planner.WantSync("cl/70")
			}
			if got := planner.Due(tc.obs, true, true, time.Hour, now); got != "" {
				t.Fatalf("due during a run: %q", got)
			}
			before := p.syncs
			for i := 0; i < 3; i++ {
				if err := AutoSync(ctx, store, p, planner, *task, tc.obs, model.StateAwaitingSubmit, now); err != nil {
					t.Fatal(err)
				}
			}
			if p.syncs-before != 1 {
				t.Fatalf("synced %d times on one snapshot, want 1", p.syncs-before)
			}
			// A new snapshot may be synced once more.
			next := tc.obs
			next.Snapshot = 2
			if got := planner.Due(next, false, true, time.Hour, now); got == "" && tc.want != change.SyncNeedsSync {
				t.Fatalf("not due on a new snapshot")
			}
		})
	}
	if planner := change.NewSyncPlanner(); planner.Due(change.Observation{Target: "cl/70", Snapshot: 1, SnapshotAt: now.Add(-2 * time.Hour)},
		false, true, 0, now) != "" {
		t.Fatal("stale timer fired with sync_after 0")
	}

	// A sync that keeps failing does not loop, even as snapshots move,
	// until the CL is uploaded anew.
	planner := change.NewSyncPlanner()
	p.syncErr, p.syncs = errors.New("boom"), 0
	for snap := 1; snap <= 3; snap++ {
		obs := change.Observation{Target: "cl/70", Snapshot: snap, Health: change.HealthBehind}
		if err := AutoSync(ctx, store, p, planner, *task, obs, model.StateAwaitingSubmit, now); err != nil {
			t.Fatal(err)
		}
	}
	if p.syncs != 1 {
		t.Fatalf("failing sync tried %d times, want 1", p.syncs)
	}
	planner.NoteUpload("cl/70")
	if got := planner.Due(change.Observation{Target: "cl/70", Snapshot: 4, Health: change.HealthBehind}, false, true, time.Hour, now); got != change.SyncBehind {
		t.Fatalf("after an upload due = %q", got)
	}
}

func TestCLSubmittedGoal(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	p := &landingCLs{polled: map[string]change.Observation{
		"cl/1": {Target: "cl/1", Status: change.StatusSubmitted},
		"cl/2": {Target: "cl/2", Status: change.StatusLGTM},
	}, errs: map[string]error{"cl/3": errors.New("not the operator's CL")}}
	registerLanding(t, p)
	t.Cleanup(func() { change.ClearBlocked(string(model.LinkChangelist)) })
	seedCLTask(t, ctx, store, "80", "cl/1", "cl/2")
	seedCLTask(t, ctx, store, "81", "cl/3")
	now := time.Now()
	read := func(spec model.GoalSpec) goalReading {
		t.Helper()
		d := model.GoalDecl{Kind: model.GoalCLSubmitted, Spec: spec}
		if err := d.Normalize(); err != nil {
			t.Fatal(err)
		}
		r, err := checkGoal(ctx, Deps{Store: store}, model.TaskGoal{TaskID: "81", Kind: d.Kind, Spec: d.Spec}, now)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := read(model.GoalSpec{Task: "80"}); r.met || !strings.Contains(r.evidence, "cl/2 (lgtm)") {
		t.Fatalf("task with one CL left = %+v", r)
	}
	if r := read(model.GoalSpec{CL: 1}); !r.met {
		t.Fatalf("submitted cl/1 = %+v", r)
	}
	p.polled["cl/2"] = change.Observation{Target: "cl/2", Status: change.StatusSubmitted}
	if r := read(model.GoalSpec{Task: "80"}); !r.met {
		t.Fatalf("every CL submitted = %+v", r)
	}
	if r := read(model.GoalSpec{Task: "81"}); r.met || !strings.Contains(r.evidence, "cannot read cl/3") {
		t.Fatalf("unreadable CL = %+v", r)
	}
	p.errs["cl/3"] = &change.CredentialsExpiredError{Kind: "changelist"}
	if r := read(model.GoalSpec{CL: 3}); r.met || !strings.Contains(r.evidence, "credentials") {
		t.Fatalf("expired credentials = %+v", r)
	}
	if err := (&model.GoalDecl{Kind: model.GoalCLSubmitted}).Normalize(); err == nil {
		t.Fatal("a cl-submitted goal with no task or cl was accepted")
	}
}
