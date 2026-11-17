// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestPRFeedbackUnseenAtSuccessfulCompletion(t *testing.T) {
	f := newFeedbackFixture(t)
	d := f.cycle(1)[0]
	f.event("issue_comment", "created", 701, "alice")
	f.cycle(0)
	f.finish(d, 0)
	at := f.now.Add(2 * time.Hour)
	if err := f.store.ObserveField(t.Context(), "feedback", at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
		t.Fatal(err)
	}
	next := f.cycle(1)[0]
	cs := f.comments()
	f.finish(next, cs[len(cs)-1].ID)
	f.cycle(0)
}
func TestPRFeedbackClaimRechecksAfterAdmission(t *testing.T) {
	for _, change := range []string{"closed", "paused", "delivered", "rescheduled"} {
		t.Run(change, func(t *testing.T) {
			f := newFeedbackFixture(t)
			f.question(false)
			f.event("issue_comment", "created", 801, "alice")
			through := f.comments()[1].ID
			admit := func(ctx context.Context, _ dispatch.Candidate) (dispatch.Admission, error) {
				var err error
				switch change {
				case "closed":
					err = f.store.ObserveField(ctx, "feedback", f.now, func(o *model.Observation) { o.ClosedAt = &f.now })
				case "paused":
					_, err = f.store.Pause(ctx, model.TaskPause{TaskID: "feedback", PausedAt: f.now, PausedBy: model.Principal{Kind: model.PrincipalHuman, ID: "owner"}})
				case "delivered":
					err = f.store.AdvanceDelivered(ctx, "feedback", through, f.now)
				case "rescheduled":
					later := f.now.Add(24 * time.Hour)
					err = f.store.SetDeferred(ctx, "feedback", &f.now, &later, f.now)
				}
				return dispatch.Admission{Admitted: true}, err
			}
			got, err := dispatch.Cycle(t.Context(), f.store, model.Limits{Workers: 1}, f.now.Add(time.Hour), dispatch.Admit(admit))
			if err != nil || len(got) != 0 {
				t.Fatalf("claim raced %s: %+v, %v", change, got, err)
			}
			attempts, err := f.store.Attempts(t.Context(), "feedback")
			if err != nil || attempts != 0 {
				t.Fatalf("spent attempt: %d, %v", attempts, err)
			}
		})
	}
}
func TestPRFeedbackThreadBatchAndWebhookShareReceipt(t *testing.T) {
	f := newFeedbackFixture(t)
	f.ciWait(true)
	c := model.Comment{TaskID: "feedback", Author: model.Attribution{Actor: githubWebhookAutomation}, Body: "two external review threads", CreatedAt: f.now}
	if err := f.store.RecordPRFeedbackBatch(t.Context(), c, []string{"review_comment:901", "review_comment:902", "review_comment:901"}, true); err != nil {
		t.Fatal(err)
	}
	f.event("pull_request_review_comment", "created", 901, "alice")
	if len(f.comments()) != 1 {
		t.Fatalf("webhook replayed polled thread: %+v", f.comments())
	}
	d := f.cycle(1)[0]
	f.finish(d, f.comments()[0].ID)
	f.cycle(0)
	// A historical receipt from the old ingestion conventions stays spent.
	if err := f.store.UpdateTask(t.Context(), "feedback", func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkGitHubEvent, Target: "issue_comment:903"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.event("issue_comment", "created", 903, "alice")
	f.cycle(0)
}
func TestPRFeedbackPendingBodySurvivesConversationWindow(t *testing.T) {
	f := newFeedbackFixture(t)
	f.question(false)
	body := "External PR feedback by alice: " + strings.Repeat("review detail ", 200) + "FINAL FEEDBACK DETAIL"
	if err := f.store.RecordPRFeedback(t.Context(), model.Comment{TaskID: "feedback",
		Author: model.Attribution{Actor: githubWebhookAutomation}, Body: body, CreatedAt: f.now}, "issue_comment:1001", false); err != nil {
		t.Fatal(err)
	}
	cs := f.comments()
	pending, err := f.store.PendingPRFeedback(t.Context(), "feedback", lastCommentID(cs))
	if err != nil || len(pending) != 1 || pending[0].Body != body {
		t.Fatalf("pending body truncated: %+v, %v", pending, err)
	}
	// This is precisely the automated-history truncation that the dedicated
	// pending section must avoid.
	if strings.Contains(commentThreadSection(cs), "FINAL FEEDBACK DETAIL") {
		t.Fatal("fixture did not exercise automation truncation")
	}
	if err := f.store.AdvanceDeliveredFrom(t.Context(), "feedback", lastCommentID(cs), f.now); err != nil {
		t.Fatal(err)
	}
	pending, err = f.store.PendingPRFeedback(t.Context(), "feedback", lastCommentID(cs))
	if err != nil || len(pending) != 0 {
		t.Fatalf("delivered feedback repeated: %+v, %v", pending, err)
	}
}

func TestPRFeedbackDuringResultProcessingRemainsPending(t *testing.T) {
	f := newFeedbackFixture(t)
	d := f.cycle(1)[0]
	f.finish(d, 0)
	// The run row has finished, but ProcessResult has not completed the task.
	f.event("issue_comment", "created", 1101, "alice")
	at := f.now.Add(2 * time.Hour)
	if err := f.store.ObserveField(t.Context(), "feedback", at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
		t.Fatal(err)
	}
	next := f.cycle(1)[0]
	f.finish(next, f.comments()[0].ID)
	f.cycle(0)
}
func TestPRFeedbackCIWithFallbackTimeStillWakes(t *testing.T) {
	f := newFeedbackFixture(t)
	f.ciWait(true)
	task, err := f.store.GetTask(t.Context(), "feedback")
	if err != nil {
		t.Fatal(err)
	}
	at := f.now.Add(24 * time.Hour)
	if err := f.store.SetDeferred(t.Context(), "feedback", task.DeferredAt, &at, f.now); err != nil {
		t.Fatal(err)
	}
	f.event("issue_comment", "created", 1102, "alice")
	f.cycle(1)
}
func TestPRFeedbackDelegatedGrainCommentsDoNotWake(t *testing.T) {
	f := newFeedbackFixture(t)
	f.question(false)
	body := github.SignCommentBody("Grain's answer", "Alice")
	ev := ghwebhook.Event{Event: "issue_comment", Action: "created", Repo: f.repo.String(), Number: 12,
		IsPullRequest: true, SenderLogin: "alice", CommentID: 1103, CommentBody: body}
	if err := processGitHubEvent(t.Context(), Deps{Store: f.store, Client: feedbackClient{}}, []string{f.repo.String()}, map[string]bool{}, ev, f.now); err != nil {
		t.Fatal(err)
	}
	f.cycle(0)
	if len(f.comments()) != 1 {
		t.Fatal("delegated Grain reply was relayed to itself")
	}
}

func TestPRFeedbackCompletedRepairPreservesDecisionGate(t *testing.T) {
	f := newFeedbackFixture(t)
	at := f.now
	if err := f.store.ObserveField(t.Context(), "feedback", at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateTask(t.Context(), "feedback", func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkReviewQuestions, Target: "awaiting an explicit owner decision"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordPRFeedback(t.Context(), model.Comment{TaskID: "feedback", Body: "changes requested",
		Author: model.Attribution{Actor: githubWebhookAutomation}, CreatedAt: at}, "review:1201", true); err != nil {
		t.Fatal(err)
	}
	f.cycle(0)
	o, _ := f.store.GetObservation(t.Context(), "feedback")
	if o.CompletedAt == nil {
		t.Fatal("external review bypassed the owner's decision gate")
	}
}

func TestPRFeedbackExternalPRUsesExistingOriginMapping(t *testing.T) {
	f := newFeedbackFixture(t)
	if err := f.store.UpdateTask(t.Context(), "feedback", func(task *model.Task) error {
		task.Filing.Reason = model.ReasonReview
		task.WorkOrigin = model.GitHubOrigin(f.repo, 12)
		task.Links = []model.Link{{Kind: model.LinkExternalPR, Target: "acme/widgets#12"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	q := f.question(false)
	f.event("issue_comment", "created", 1301, "alice")
	d := f.cycle(1)[0]
	f.finish(d, f.comments()[1].ID)
	f.cycle(0)
	o, _ := f.store.GetObservation(t.Context(), "feedback")
	if o.PendingQuestionCommentID == nil || *o.PendingQuestionCommentID != q {
		t.Fatal("external PR feedback fabricated an answer")
	}
}

func TestPRFeedbackConcurrentRedeliveryCommitsOneReceipt(t *testing.T) {
	f := newFeedbackFixture(t)
	f.question(false)
	c := model.Comment{TaskID: "feedback", Body: "new external feedback",
		Author: model.Attribution{Actor: githubWebhookAutomation}, CreatedAt: f.now}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.store.RecordPRFeedback(t.Context(), c, "issue_comment:1401", false) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(f.comments()) != 2 {
		t.Fatalf("redelivery duplicated context: %+v", f.comments())
	}
	d := f.cycle(1)[0]
	f.finish(d, f.comments()[1].ID)
	f.cycle(0)
}

func TestPRFeedbackReceiptSurvivesReplacedProvenanceLinks(t *testing.T) {
	f := newFeedbackFixture(t)
	f.question(false)
	f.event("issue_comment", "created", 1501, "alice")
	d := f.cycle(1)[0]
	f.finish(d, f.comments()[1].ID)
	if err := f.store.UpdateTask(t.Context(), "feedback", func(task *model.Task) error {
		task.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#12"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.event("issue_comment", "created", 1501, "alice")
	if len(f.comments()) != 2 {
		t.Fatal("receipt replay duplicated context after a task update")
	}
	f.cycle(0)
}
