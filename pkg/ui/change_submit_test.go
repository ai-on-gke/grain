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

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// submitOutcome is the error shape the edition's Submit and Sync answer
// with, seen only through change's interfaces.
type submitOutcome struct {
	outcome string
	files   []string
	checks  []string
	detail  string
}

func (e *submitOutcome) Error() string              { return "submit: " + e.outcome }
func (e *submitOutcome) ChangeOutcome() string      { return e.outcome }
func (e *submitOutcome) ConflictingFiles() []string { return e.files }
func (e *submitOutcome) FailingChecks() []string    { return e.checks }
func (e *submitOutcome) UntrustedDetail() string    { return e.detail }

// fakeSubmitter is a changelist provider with S5's Submit and S6's Sync.
type fakeSubmitter struct {
	fakeCLs
	submitted []string // target|person
	granted   []bool   // whether the task Submit was handed held the submit grant
	syncs     int
	result    change.Result
	err       error
	syncErr   error
}

func (f *fakeSubmitter) Kind() model.LinkKind { return model.LinkChangelist }
func (f *fakeSubmitter) TargetKind() string   { return "fakesubmit" }
func (f *fakeSubmitter) Submit(_ context.Context, task model.Task, target string, by change.Person) (change.Result, error) {
	f.submitted = append(f.submitted, target+"|"+by.Name)
	f.granted = append(f.granted, hasSubmitGrant(task))
	return f.result, f.err
}
func (f *fakeSubmitter) Sync(context.Context, model.Task, string) (change.SyncResult, error) {
	f.syncs++
	return change.SyncResult{Message: "synced to CL 900, cl/42 new snapshot 4"}, f.syncErr
}
func (f *fakeSubmitter) SubmitGrant(target string) (string, map[string]any) {
	return "https://example.test/capabilities/ws#submit", map[string]any{"cl": strings.TrimPrefix(target, "cl/")}
}

func hasSubmitGrant(task model.Task) bool {
	for _, g := range task.Grants {
		if strings.Contains(g.Capability, "ws#submit") && strings.Contains(g.Capability, `"42"`) {
			return true
		}
	}
	return false
}

var submittable = change.Observation{Target: "cl/42", Status: change.StatusLGTM, Health: change.HealthOK, Snapshot: 3,
	Presubmit: change.PresubmitPassed, ReviewState: change.ReviewStateApproved}

func seedAwaitingSubmit(t *testing.T, store *model.Store, ctx context.Context, id string, cls ...string) {
	t.Helper()
	var links []model.Link
	for _, cl := range cls {
		links = append(links, model.Link{Kind: model.LinkChangelist, Target: cl})
	}
	if err := store.PutTask(ctx, model.Task{ID: id, Title: "t", Intent: model.IntentImplement,
		ChangeTarget: &model.ChangeTarget{Kind: "fakesubmit"}, Links: links,
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.ObserveField(ctx, id, now, func(o *model.Observation) { o.CompletedAt = &now }); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, id); st != model.StateAwaitingSubmit {
		t.Fatalf("seeded state %s, want awaiting_submit", st)
	}
}

func registerSubmitter(t *testing.T, f *fakeSubmitter) {
	t.Helper()
	change.Register(f)
	t.Cleanup(func() {
		change.Unregister(model.LinkChangelist)
		change.Forget("cl/42")
		change.Forget("cl/43")
		change.ClearBlocked("fakesubmit")
	})
}

func person(ctx context.Context) context.Context {
	return ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice"})
}

func lastComment(t *testing.T, store *model.Store, ctx context.Context, id string) string {
	t.Helper()
	cs, err := store.Comments(ctx, id)
	if err != nil || len(cs) == 0 {
		t.Fatalf("comments on %s: %v, %v", id, cs, err)
	}
	return cs[len(cs)-1].Body
}

func TestSubmitCLRefusesAgentsAndUnsubmittableCLs(t *testing.T) {
	client, store, ctx := testClient(t)
	fake := &fakeSubmitter{}
	registerSubmitter(t, fake)
	seedAwaitingSubmit(t, store, ctx, "70", "cl/42")

	// Not yet observed, then observed but not approved: refused with reasons.
	if err := client.Submit(person(ctx), "70"); err == nil || !strings.Contains(err.Error(), "has not read this CL") {
		t.Fatalf("unobserved submit = %v", err)
	}
	pending := submittable
	pending.ReviewState, pending.Presubmit, pending.Health = change.ReviewStateInReview, change.PresubmitRunning, change.HealthBehind
	change.Remember(pending)
	got, _ := client.GetTask(ctx, "70")
	if got.CLSubmit == nil || got.CLSubmit.CanSubmit || !got.CLSubmit.Behind || len(got.CLSubmit.Reasons) != 3 {
		t.Fatalf("clSubmit = %+v, want disabled with review, presubmit and behind reasons", got.CLSubmit)
	}
	if err := client.Submit(person(ctx), "70"); err == nil || !strings.Contains(err.Error(), "review is not approved") {
		t.Fatalf("unapproved submit = %v", err)
	}

	// Submittable, but an agent or API caller is not a person.
	change.Remember(submittable)
	for _, p := range []model.Principal{{Kind: model.PrincipalAgent, ID: "70"}, {Kind: model.PrincipalAutomation, ID: "api"}} {
		err := client.Submit(ui.WithPrincipal(ctx, p), "70")
		var fe *ui.ForbiddenError
		if !errors.As(err, &fe) {
			t.Fatalf("submit as %v = %v, want forbidden", p, err)
		}
	}
	// A grain hold blocks it too.
	if err := store.UpdateTask(ctx, "70", func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkHumanOnlyHold, Target: "x"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Submit(person(ctx), "70"); err == nil || !strings.Contains(err.Error(), "held for a person") {
		t.Fatalf("held submit = %v", err)
	}
	if len(fake.submitted) != 0 {
		t.Fatalf("provider was asked to submit: %v", fake.submitted)
	}
}

func TestSubmitCLRecordsThePersonsGrantAndClosesRenumbered(t *testing.T) {
	client, store, ctx := testClient(t)
	fake := &fakeSubmitter{result: change.Result{Target: "cl/99", Message: "Submitted as cl/99."}}
	registerSubmitter(t, fake)
	seedAwaitingSubmit(t, store, ctx, "71", "cl/42")
	change.Remember(submittable)
	got, _ := client.GetTask(ctx, "71")
	if got.CLSubmit == nil || !got.CLSubmit.CanSubmit {
		t.Fatalf("clSubmit = %+v, want submittable", got.CLSubmit)
	}
	if err := client.Submit(person(ctx), "71"); err != nil {
		t.Fatal(err)
	}
	if len(fake.submitted) != 1 || fake.submitted[0] != "cl/42|alice" {
		t.Fatalf("submitted = %v", fake.submitted)
	}
	task, _ := store.GetTask(ctx, "71")
	// The person's grant was on the task Submit was handed, and is gone
	// once it answered: it is not carried into a later session.
	if len(fake.granted) != 1 || !fake.granted[0] {
		t.Fatalf("Submit was handed the grant: %v, want true", fake.granted)
	}
	if hasSubmitGrant(*task) {
		t.Fatalf("grants = %+v, want the submit grant released after Submit", task.Grants)
	}
	if task.AutoMerge {
		t.Fatal("a CL Submit turned auto-merge on")
	}
	submitted := false
	for _, l := range task.Links {
		submitted = submitted || (l.Kind == model.LinkChangelistSubmitted && l.Target == "cl/99 (was cl/42)")
	}
	if !submitted {
		t.Fatalf("links = %+v, want cl/99 recorded submitted", task.Links)
	}
	obs, _ := store.GetObservation(ctx, "71")
	if obs == nil || obs.ClosedAt == nil || obs.PrMergedAt == nil {
		t.Fatalf("observation = %+v, want closed and merged", obs)
	}
	cs, _ := store.Comments(ctx, "71")
	if len(cs) != 2 || cs[0].Author.Actor.ID != "alice" || cs[1].Body != "Submitted as cl/99." {
		t.Fatalf("comments = %+v, want alice's press then the provider's message", cs)
	}
}

func TestSubmitCLOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		wantErr bool
		woken   bool
		syncs   int
		comment string
	}{
		{name: "needs sync", err: &submitOutcome{outcome: change.OutcomeNeedsSync}, syncs: 1, comment: "Press Submit again"},
		{name: "conflicts", err: &submitOutcome{outcome: change.OutcomeConflicts, files: []string{"a/b.go"}}, woken: true, comment: "citc_resolve"},
		{name: "presubmit failed", err: &submitOutcome{outcome: change.OutcomePresubmitFailed, checks: []string{"//a:test"}}, woken: true, comment: "//a:test"},
		{name: "error", err: &submitOutcome{outcome: change.OutcomeError, detail: "<untrusted>piper said no</untrusted>"}, wantErr: true, comment: "piper said no"},
		{name: "credentials", err: &change.CredentialsExpiredError{Kind: "fakesubmit"}, wantErr: true, comment: "credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, store, ctx := testClient(t)
			fake := &fakeSubmitter{err: tc.err}
			registerSubmitter(t, fake)
			seedAwaitingSubmit(t, store, ctx, "72", "cl/42")
			change.Remember(submittable)
			err := client.Submit(person(ctx), "72")
			if (err != nil) != tc.wantErr {
				t.Fatalf("submit = %v, wantErr %v", err, tc.wantErr)
			}
			if fake.syncs != tc.syncs || len(fake.submitted) != 1 {
				t.Fatalf("syncs %d submits %d, want %d and exactly one (never resubmitted)", fake.syncs, len(fake.submitted), tc.syncs)
			}
			state, _ := store.State(ctx, "72")
			if tc.woken && state != model.StateQueued {
				t.Fatalf("state %s, want queued for a run", state)
			}
			if !tc.woken && state != model.StateAwaitingSubmit {
				t.Fatalf("state %s, want still awaiting_submit", state)
			}
			if task, _ := store.GetTask(ctx, "72"); hasSubmitGrant(*task) {
				t.Fatalf("submit grant still held after a %s outcome", tc.name)
			}
			if c := lastComment(t, store, ctx, "72"); !strings.Contains(c, tc.comment) {
				t.Fatalf("comment %q, want it to mention %q", c, tc.comment)
			}
		})
	}
}

func TestSyncCLButtonIsAPersonsAndRefusedDuringARun(t *testing.T) {
	client, store, ctx := testClient(t)
	fake := &fakeSubmitter{}
	registerSubmitter(t, fake)
	seedAwaitingSubmit(t, store, ctx, "73", "cl/42")
	change.Remember(submittable)
	var fe *ui.ForbiddenError
	if err := client.SyncChange(ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "73"}), "73"); !errors.As(err, &fe) {
		t.Fatalf("agent sync = %v, want forbidden", err)
	}
	if err := client.SyncChange(person(ctx), "73"); err != nil {
		t.Fatal(err)
	}
	if fake.syncs != 1 || !strings.Contains(lastComment(t, store, ctx, "73"), "new snapshot 4") {
		t.Fatalf("syncs %d, comment %q", fake.syncs, lastComment(t, store, ctx, "73"))
	}
	if err := store.StartRun(ctx, model.Run{ID: "73-2", TaskID: "73", Attempt: 2, StartedAt: time.Now()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, "73", time.Now(), func(o *model.Observation) { o.CompletedAt = nil }); err != nil {
		t.Fatal(err)
	}
	if err := client.SyncChange(person(ctx), "73"); err == nil || !strings.Contains(err.Error(), "a run is going") {
		t.Fatalf("sync during a run = %v", err)
	}
	if fake.syncs != 1 {
		t.Fatalf("synced during a run")
	}
}
