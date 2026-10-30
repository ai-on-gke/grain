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
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The changelist Submit/Sync/review walkthrough
// (docs/test-runs/2026-10-29-changelist-submit-sync-reviews.md) over the
// demo's own seeded CL tasks and its in-process provider, for what the
// running demo cannot show: its reconcile loop runs neither the goal,
// cl-reviews nor change-poll arms, and its provider neither polls nor
// files reviews. Each test here runs those arms of the real orchestrator
// directly.

const (
	demoSubmittable = "Move the depot retry budget behind a flag"
	demoBehind      = "Sync the depot retry budget with head"
	demoBlocked     = "Port the retry budget to the depot"
)

// demoCLs seeds the demo into a fresh store and answers its client, the
// task ids by title, and a person's context. Not parallel: the change
// registry and its observations are the process's.
func demoCLs(t *testing.T) (*ui.Client, *model.Store, context.Context, map[string]string) {
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
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{Actor: ui.DefaultActor("demo-operator"), DefaultTarget: &repo, Capabilities: ui.OfferedCapabilities()}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}
	// What a Submit, Sync or poll here remembers is forgotten afterwards,
	// so the next seedDemo starts from its own seed.
	t.Cleanup(func() {
		for _, cl := range []string{"cl/612400001", "cl/612400017", "cl/612400101", "cl/612400102",
			"cl/612400103", "cl/612400104", "cl/612400105"} {
			change.Forget(cl)
		}
		change.Unregister(model.LinkChangelist)
	})
	client := ui.NewClient(cfg, store)
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, task := range tasks {
		ids[task.Title] = task.ID
	}
	person := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "demo-operator"})
	return client, store, person, ids
}

// reconcile runs the named arms of the orchestrator's cycle once.
func reconcile(t *testing.T, ctx context.Context, store *model.Store, now time.Time, names ...string) {
	t.Helper()
	for _, r := range orchestrator.Reconcilers() {
		if slices.Contains(names, r.Name) {
			if err := r.Reconcile(ctx, orchestrator.Deps{Store: store}, now); err != nil {
				t.Fatalf("reconciling %s: %v", r.Name, err)
			}
		}
	}
}

func lastComment(t *testing.T, ctx context.Context, store *model.Store, id string) string {
	t.Helper()
	cs, err := store.Comments(ctx, id)
	if err != nil || len(cs) == 0 {
		t.Fatalf("comments on %s: %v (%d)", id, err, len(cs))
	}
	return cs[len(cs)-1].Body
}

// Pressing Submit on the submittable demo CL lands it: the task closes
// merged, the merge counts under the demo forge, and cl-submitted goals
// naming the CL or the task are met on the next goal pass.
func TestDemoCLSubmitLandsAndMeetsCLSubmittedGoals(t *testing.T) {
	client, store, ctx, ids := demoCLs(t)
	id := ids[demoSubmittable]
	byCL, byTask := ids["Fix flaky retry test"], ids["Add a dark mode toggle"]
	for goalOn, spec := range map[string]model.GoalSpec{byCL: {CL: 612400101}, byTask: {Task: id}} {
		if err := client.SetGoal(ctx, goalOn, model.GoalDecl{Kind: model.GoalCLSubmitted, Spec: spec, OnMet: model.GoalRestOnMet}); err != nil {
			t.Fatalf("SetGoal on %s: %v", goalOn, err)
		}
	}

	if err := client.Submit(ctx, id); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if st, _ := store.State(ctx, id); st != model.StateClosed {
		t.Fatalf("state after Submit = %s, want closed", st)
	}
	if obs, _ := store.GetObservation(ctx, id); obs == nil || obs.PrMergedAt == nil {
		t.Fatalf("observation %+v, want a merged moment", obs)
	}
	report, err := client.Metrics(ctx, ui.MetricsRequest{Window: 7 * 24 * time.Hour, Section: "merges"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Merges.Landed != 1 || report.Merges.LandedByForge["demo"] != 1 {
		t.Errorf("merges landed %d by forge %v, want one under the demo forge", report.Merges.Landed, report.Merges.LandedByForge)
	}

	reconcile(t, ctx, store, time.Now().UTC(), "task-goals")
	for _, goalOn := range []string{byCL, byTask} {
		g, err := store.TaskGoal(ctx, goalOn)
		if err != nil || g == nil {
			t.Fatalf("goal on %s: %v", goalOn, err)
		}
		if g.MetAt == nil {
			t.Errorf("goal on %s (%+v) not met after the CL landed: %s", goalOn, g.Spec, g.Evidence)
		}
	}
}

// Sync on the behind demo CL brings it up to date: the behind badge goes,
// and Submit now waits on presubmit for the new snapshot instead.
func TestDemoCLSyncClearsBehind(t *testing.T) {
	client, _, ctx, ids := demoCLs(t)
	id := ids[demoBehind]
	before, err := client.GetTask(ctx, id)
	if err != nil || before.CLSubmit == nil || !before.CLSubmit.Behind {
		t.Fatalf("before Sync clSubmit = %+v (%v), want behind", before.CLSubmit, err)
	}
	if err := client.SyncChange(ctx, id); err != nil {
		t.Fatalf("SyncChange: %v", err)
	}
	after, err := client.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.CLSubmit == nil || after.CLSubmit.Behind {
		t.Fatalf("after Sync clSubmit = %+v, want no longer behind", after.CLSubmit)
	}
	if got := strings.Join(after.CLSubmit.Reasons, "; "); !strings.Contains(got, "snapshot 3 (not run)") || strings.Contains(got, "behind") {
		t.Errorf("after Sync reasons = %q, want presubmit on the new snapshot and nothing about head", got)
	}
}

// reviewingDemoCLs is the demo provider filing grain's read-only CL
// reviews (change.ReviewFiler) and polling (change.Poller), as an
// edition's does: the two things the demo's own provider leaves out.
type reviewingDemoCLs struct {
	demoChangelists
	saved  map[string][]change.ReviewFinding
	polled map[string]change.Observation
	syncs  int
}

func (p *reviewingDemoCLs) ReviewGrant(deployment string, reviewed model.Task, snapshot int) (string, error) {
	return fmt.Sprintf(`https://caps.example/workspace#workspace={"readonly":true,"review_of":%q,"snapshot":%d,"deployment":%q}`,
		reviewed.ID, snapshot, deployment), nil
}

func (p *reviewingDemoCLs) SaveReviewFindings(_ context.Context, _ model.Task, target string, findings []change.ReviewFinding) error {
	p.saved[target] = append(p.saved[target], findings...)
	return nil
}

func (p *reviewingDemoCLs) PollTick(_ context.Context, tasks []model.Task) []change.Observation {
	var out []change.Observation
	for _, task := range tasks {
		for _, cl := range change.LiveChanges(task) {
			if o, ok := p.polled[cl]; ok {
				out = append(out, o)
			}
		}
	}
	return out
}

func (p *reviewingDemoCLs) Sync(ctx context.Context, task model.Task, to string) (change.SyncResult, error) {
	p.syncs++
	return p.demoChangelists.Sync(ctx, task, to)
}

func withReviewingProvider(t *testing.T) *reviewingDemoCLs {
	t.Helper()
	change.Unregister(model.LinkChangelist)
	p := &reviewingDemoCLs{saved: map[string][]change.ReviewFinding{}, polled: map[string]change.Observation{}}
	change.Register(p)
	return p
}

func clReviewTasks(t *testing.T, ctx context.Context, store *model.Store, id string) []model.Task {
	t.Helper()
	task, _ := store.GetTask(ctx, id)
	var out []model.Task
	for _, l := range task.Links {
		if l.Kind == model.LinkReviewTask {
			r, _ := store.GetTask(ctx, l.Target)
			out = append(out, *r)
		}
	}
	return out
}

// The review gate over snapshots: a finished CL's review is filed on the
// next cycle and Submit waits on it; a finding is saved as a draft, put
// to the owner as a question and keeps Submit refused; the owner's answer
// releases the hold; a new snapshot is reviewed again, inherits nothing,
// and a clean review of it lets Submit through.
func TestDemoCLReviewGateOverSnapshots(t *testing.T) {
	client, store, ctx, ids := demoCLs(t)
	provider := withReviewingProvider(t)
	id := ids[demoSubmittable]
	if err := store.PutTemplate(ctx, model.Template{ID: "cl-review", Name: "CL review", Title: "Review the CL",
		Body: "Read the CL."}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, id, func(task *model.Task) error {
		task.ReviewTemplateID = "cl-review"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	refused := func(want string) {
		t.Helper()
		got, _ := client.GetTask(ctx, id)
		if got.CLSubmit == nil || got.CLSubmit.CanSubmit || !strings.Contains(strings.Join(got.CLSubmit.Reasons, "; "), want) {
			t.Fatalf("clSubmit = %+v, want Submit disabled with %q", got.CLSubmit, want)
		}
		// Submit names the first reason it meets, the gate's own before
		// grain's holds; the page lists them all.
		if err := client.Submit(ctx, id); err == nil || !strings.Contains(err.Error(), "cannot be submitted yet") {
			t.Fatalf("Submit = %v, want it refused", err)
		}
	}
	now := time.Now().UTC()

	refused("grain has not reviewed snapshot 1 yet")
	reconcile(t, ctx, store, now, "cl-reviews")
	reviews := clReviewTasks(t, ctx, store, id)
	if len(reviews) != 1 {
		t.Fatalf("%d reviews filed, want 1", len(reviews))
	}
	refused("grain's review of snapshot 1 has not passed yet")

	first := reviews[0]
	finding := agent.ToolCall{Name: "add_review_comment", Arguments: map[string]any{
		"body": "should the flag default to off?", "path": "devtools/grain/retry/flag.go", "line": float64(12)}}
	if err := orchestrator.ProcessResult(ctx, store, nil, first, &agent.Result{ToolCalls: []agent.ToolCall{finding}},
		first.ID+"-1", now); err != nil {
		t.Fatalf("ProcessResult (finding): %v", err)
	}
	if got := provider.saved["cl/612400101"]; len(got) != 1 || got[0].Line != 12 {
		t.Fatalf("drafts saved = %+v, want the finding on its line", got)
	}
	held, _ := store.GetTask(ctx, id)
	if _, ok := model.ReviewQuestionsOf(*held); !ok {
		t.Fatal("the reviewed task is not held on its review's question")
	}
	refused("a grain review left questions open")

	// The owner answers; the next cycle releases the hold and queues a run
	// to carry the decision out.
	if _, err := store.AddComment(ctx, model.Comment{TaskID: id, CreatedAt: now.Add(time.Minute),
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "demo-operator"}},
		Body:   "Default it to off."}); err != nil {
		t.Fatal(err)
	}
	reconcile(t, ctx, store, now.Add(2*time.Minute), "review-questions")
	released, _ := store.GetTask(ctx, id)
	if _, ok := model.ReviewQuestionsOf(*released); ok {
		t.Fatal("the owner's answer did not release the CL task's review-questions hold")
	}
	if st, _ := store.State(ctx, id); st != model.StateQueued {
		t.Fatalf("state after the owner's answer = %s, want queued to carry it out", st)
	}
	if c := lastComment(t, ctx, store, id); !strings.Contains(c, "new snapshot of its CL") {
		t.Errorf("the run is told %q, want to upload a new snapshot", c)
	}

	// That run uploads snapshot 2 and finishes.
	obs, _ := change.LastObserved("cl/612400101")
	obs.Snapshot = 2
	change.Remember(obs)
	done := now.Add(3 * time.Minute)
	if err := store.ObserveField(ctx, id, done, func(o *model.Observation) {
		o.CompletedAt, o.RetryRequestedAt = &done, nil
	}); err != nil {
		t.Fatal(err)
	}
	refused("grain has not reviewed snapshot 2 yet")
	reconcile(t, ctx, store, done, "cl-reviews")
	reviews = clReviewTasks(t, ctx, store, id)
	if len(reviews) != 2 {
		t.Fatalf("%d reviews after snapshot 2, want it reviewed again", len(reviews))
	}
	refused("grain's review of snapshot 2 has not passed yet")
	second := reviews[1]
	clean := agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "looks right"}}
	if err := orchestrator.ProcessResult(ctx, store, nil, second, &agent.Result{ToolCalls: []agent.ToolCall{clean}},
		second.ID+"-1", done); err != nil {
		t.Fatalf("ProcessResult (clean): %v", err)
	}
	if err := client.Submit(ctx, id); err != nil {
		t.Fatalf("Submit after a clean review of snapshot 2: %v", err)
	}
	if st, _ := store.State(ctx, id); st != model.StateClosed {
		t.Fatalf("state after Submit = %s, want closed", st)
	}
}

// The poll closes a demo CL submitted outside grain and returns one whose
// only CL was abandoned to awaiting_reply; it never syncs a CL whose task
// has a run going.
func TestDemoCLPollLandsAbandonsAndNeverSyncsDuringARun(t *testing.T) {
	_, store, ctx, ids := demoCLs(t)
	provider := withReviewingProvider(t)
	landed, abandoned, behind := ids[demoSubmittable], ids["Add a TAP project for the retry budget"], ids[demoBehind]
	provider.polled["cl/612400101"] = change.Observation{Target: "cl/612400101", Status: change.StatusSubmitted, Snapshot: 1}
	provider.polled["cl/612400102"] = change.Observation{Target: "cl/612400102", Status: change.StatusAbandoned, Snapshot: 1}
	provider.polled["cl/612400105"] = change.Observation{Target: "cl/612400105", Health: change.HealthBehind,
		Presubmit: change.PresubmitPassed, Snapshot: 2}
	// The behind CL's task has a run going.
	start := time.Now().UTC()
	if err := store.ObserveField(ctx, behind, start, func(o *model.Observation) {
		o.CompletedAt, o.RetryRequestedAt = nil, &start
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: behind + "-9", TaskID: behind, Attempt: 9, StartedAt: start}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, behind); st != model.StateRunning {
		t.Fatalf("behind task state = %s, want running for this check", st)
	}

	orchestrator.PollChangesOnce(ctx, store)

	if st, _ := store.State(ctx, landed); st != model.StateClosed {
		t.Errorf("CL submitted outside grain: task %s, want closed", st)
	}
	if obs, _ := store.GetObservation(ctx, landed); obs == nil || obs.PrMergedAt == nil {
		t.Errorf("CL submitted outside grain: no merged moment")
	}
	if c := lastComment(t, ctx, store, landed); !strings.Contains(c, "submitted outside grain") {
		t.Errorf("landed comment = %q", c)
	}
	if st, _ := store.State(ctx, abandoned); st != model.StateAwaitingReply {
		t.Errorf("abandoned CL's task is %s, want awaiting_reply", st)
	}
	if c := lastComment(t, ctx, store, abandoned); !strings.Contains(c, "no live CL left") {
		t.Errorf("abandoned comment = %q", c)
	}
	if provider.syncs != 0 {
		t.Errorf("the poll synced %d time(s) while the task had a run going, want none", provider.syncs)
	}
}

// Submit and Sync are a person's: an agent's call is refused before the
// provider is asked.
func TestDemoCLSubmitAndSyncRefuseAnAgent(t *testing.T) {
	client, store, ctx, ids := demoCLs(t)
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: ids[demoBlocked]})
	for _, id := range []string{ids[demoSubmittable], ids[demoBehind]} {
		if err := client.Submit(agentCtx, id); err == nil || !strings.Contains(err.Error(), "only a person") {
			t.Errorf("agent Submit of %s = %v, want refused", id, err)
		}
		if err := client.SyncChange(agentCtx, id); err == nil || !strings.Contains(err.Error(), "only a person") {
			t.Errorf("agent Sync of %s = %v, want refused", id, err)
		}
		if st, _ := store.State(ctx, id); st != model.StateAwaitingSubmit {
			t.Errorf("task %s is %s after the refused calls, want still awaiting_submit", id, st)
		}
	}
}
