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

package model_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

var acceptAt = time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)

// findingsTask is a change whose pull request review 4710 left findings
// on: on the security-review gate (as its security review) and on the
// every-pr trigger's gate, which the code-review gate lists.
func findingsTask() model.Task {
	t := model.Task{ID: "4591", Links: []model.Link{
		{Kind: model.LinkFixes, Target: "acme/widgets#1269"},
		{Kind: model.LinkSecurityReviewTask, Target: "4710"},
		{Kind: model.LinkPathTriggerTask, Target: "every-pr=4710"},
	}}
	model.AddReviewFindings(&t, model.ReviewFindingsRecord{Gate: model.GateSecurityReview, ReviewTask: "4710",
		Summary: "security review left findings", HeadSHA: "aaa111", At: acceptAt})
	model.AddReviewFindings(&t, model.ReviewFindingsRecord{Gate: "trigger:every-pr", ReviewTask: "4710",
		Summary: "every-pr review left findings", HeadSHA: "aaa111", At: acceptAt})
	return t
}

func gateState(t *testing.T, task model.Task, id string) model.MergeGateState {
	t.Helper()
	for _, g := range model.ComputeMergeGates(task, nil, nil) {
		if g.ID == id {
			return g.State
		}
	}
	t.Fatalf("no %s gate on %+v", id, task.Links)
	return ""
}

func accept(gate, review, head string) model.ReviewFindingsAcceptance {
	return model.ReviewFindingsAcceptance{Gate: gate, ReviewTask: review, Reason: "out of scope; follow-up filed",
		By: "alice", HeadSHA: head, At: acceptAt}
}

// Accepting one gate's findings releases that gate and nothing else: the
// other gate's findings, from the very same review, still hold, and so
// does CI.
func TestAcceptedGateReleasesWhileOtherGatesStillHold(t *testing.T) {
	task := findingsTask()
	if !model.AcceptReviewFindings(&task, accept(model.GateSecurityReview, "4710", "aaa111")) {
		t.Fatal("nothing was accepted")
	}
	if got := gateState(t, task, model.GateSecurityReview); got != model.GateStatePassed {
		t.Errorf("security-review gate = %s after its findings were accepted, want passed", got)
	}
	if got := gateState(t, task, model.GateReview); got != model.GateStateFindings {
		t.Errorf("code-review gate = %s, want findings: accepting security-review must not clear it", got)
	}
	if got := gateState(t, task, model.GateCI); got.Satisfied() {
		t.Errorf("CI gate = %s, want unsatisfied: accepting findings says nothing about CI", got)
	}
	if !model.HasReviewFindings(task) || !model.HasPendingReviewHold(task) {
		t.Error("the trigger's findings no longer hold the change")
	}

	// The code-review gate names the trigger's findings; accepting them
	// there clears trigger:every-pr's record.
	if !model.AcceptReviewFindings(&task, accept(model.GateReview, "4710", "aaa111")) {
		t.Fatal("the code-review gate's findings were not accepted")
	}
	if got := gateState(t, task, model.GateReview); got != model.GateStatePassed {
		t.Errorf("code-review gate = %s, want passed", got)
	}
	if model.HasReviewFindings(task) {
		t.Errorf("findings remain: %+v", task.Links)
	}
	if got := model.ReviewFindingsAcceptances(task); len(got) != 2 || got[0].By != "alice" || got[1].Gate != model.GateReview {
		t.Errorf("acceptances = %+v, want two by alice", got)
	}
}

// An acceptance is of one review's findings: another review's on the same
// gate still hold, a security hold still holds, and accepting what is not
// there records nothing.
func TestAcceptanceIsOfOneReviewOnly(t *testing.T) {
	task := findingsTask()
	model.AddReviewFindings(&task, model.ReviewFindingsRecord{Gate: "trigger:docs", ReviewTask: "4800",
		Summary: "docs review left findings", At: acceptAt})
	model.AcceptReviewFindings(&task, accept(model.GateReview, "4710", "aaa111"))
	if got := model.ReviewFindingsForGate(task, "trigger:docs"); len(got) != 1 {
		t.Errorf("review 4800's findings = %+v, want them untouched", got)
	}
	if model.AcceptReviewFindings(&task, accept(model.GateReview, "9999", "aaa111")) {
		t.Error("accepting a review with no findings on the gate recorded an acceptance")
	}
	task.Links = append(task.Links, model.Link{Kind: model.LinkSecurityHold, Target: "trust-boundary/v1"})
	model.AcceptReviewFindings(&task, accept(model.GateSecurityReview, "4710", "aaa111"))
	if got := gateState(t, task, model.GateSecurityReview); got != model.GateStateRunning {
		t.Errorf("security-review gate under a security hold = %s, want running", got)
	}
}

// A later head is gated as any head is: its classification's security
// hold and its review's findings hold the change again, whatever was
// accepted at the head before.
func TestNewHeadReGatesAfterAcceptance(t *testing.T) {
	task := findingsTask()
	model.AcceptReviewFindings(&task, accept(model.GateSecurityReview, "4710", "aaa111"))
	// The push of bbb222 touches the trust boundary again.
	task.Links = append(task.Links, model.Link{Kind: model.LinkSecurityHold, Target: "trust-boundary/v1"})
	if got := gateState(t, task, model.GateSecurityReview); got != model.GateStateRunning {
		t.Errorf("security-review gate on a new head = %s, want running", got)
	}
	// Its re-review (the same review task, re-run) leaves findings at bbb222.
	task.Links = slices.DeleteFunc(task.Links, func(l model.Link) bool { return l.Kind == model.LinkSecurityHold })
	model.AddReviewFindings(&task, model.ReviewFindingsRecord{Gate: model.GateSecurityReview, ReviewTask: "4710",
		Summary: "still unsafe", HeadSHA: "bbb222", At: acceptAt.Add(time.Hour)})
	if got := gateState(t, task, model.GateSecurityReview); got != model.GateStateFindings {
		t.Errorf("security-review gate after the new head's findings = %s, want findings", got)
	}
}

// Findings a new review leaves need a new decision: an earlier acceptance
// does not cover them.
func TestNewReviewsFindingsReBlock(t *testing.T) {
	task := findingsTask()
	model.AcceptReviewFindings(&task, accept(model.GateReview, "4710", "aaa111"))
	model.AddReviewFindings(&task, model.ReviewFindingsRecord{Gate: "trigger:every-pr", ReviewTask: "4811",
		Summary: "every-pr re-review left findings", HeadSHA: "ccc333", At: acceptAt.Add(time.Hour)})
	if got := gateState(t, task, model.GateReview); got != model.GateStateFindings {
		t.Errorf("code-review gate after a new review's findings = %s, want findings", got)
	}
	if !model.HasPendingReviewHold(task) {
		t.Error("a new review's findings do not hold the change")
	}
}

// The open questions a review put to the owner are that review's
// findings too: accepting them clears LinkReviewQuestions with them.
func TestAcceptanceClearsThatReviewsQuestions(t *testing.T) {
	task := model.Task{ID: "60", Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#60"},
		{Kind: model.LinkReviewQuestions, Target: model.ReviewQuestions{ReviewTaskID: "61", AfterCommentID: 3}.Target()}}}
	if got := gateState(t, task, model.GateReview); got != model.GateStateFindings {
		t.Fatalf("code-review gate with open questions = %s, want findings", got)
	}
	if model.AcceptReviewFindings(&task, accept(model.GateSecurityReview, "61", "")) {
		t.Error("a code review's questions were accepted on the security-review gate")
	}
	if !model.AcceptReviewFindings(&task, accept(model.GateReview, "61", "")) {
		t.Fatal("the review's questions were not accepted")
	}
	if _, ok := model.ReviewQuestionsOf(task); ok || model.HasReviewFindings(task) {
		t.Errorf("questions remain: %+v", task.Links)
	}
}

// The acceptance record -- who, why, which review and gate, at which head
// -- is kept by both stores.
func TestReviewFindingsAcceptancePersists(t *testing.T) {
	t.Parallel()
	backends := []struct {
		name string
		open func(t *testing.T) *model.Store
	}{
		{"sqlite", func(t *testing.T) *model.Store {
			db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			st := model.New(db)
			if err := st.Init(context.Background()); err != nil {
				t.Fatal(err)
			}
			return st
		}},
		{"postgres", func(t *testing.T) *model.Store {
			st, _ := postgrestest.Open(t)
			return st
		}},
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := b.open(t)
			task := findingsTask()
			task.Title = "change"
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			want := accept(model.GateSecurityReview, "4710", "aaa111")
			want.Findings = "security review left findings"
			if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
				model.AcceptReviewFindings(tk, want)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetTask(ctx, task.ID)
			if err != nil || got == nil {
				t.Fatalf("reading back %s: %v", task.ID, err)
			}
			accs := model.ReviewFindingsAcceptances(*got)
			if len(accs) != 1 || accs[0] != want {
				t.Fatalf("acceptances read back = %+v, want [%+v]", accs, want)
			}
			if len(model.ReviewFindingsForGate(*got, model.GateSecurityReview)) != 0 ||
				len(model.ReviewFindingsForGate(*got, "trigger:every-pr")) != 1 {
				t.Errorf("findings read back = %+v, want only the trigger's", model.ReviewFindings(*got))
			}
		})
	}
}
