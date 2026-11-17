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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

type feedbackClient struct{ github.Client }

func (feedbackClient) CollaboratorPermission(owner, repo, login string) (string, error) {
	if login == "outsider" {
		return "read", nil
	}
	return "write", nil
}

type feedbackFixture struct {
	t     *testing.T
	store *model.Store
	now   time.Time
	repo  model.RepoRef
}

func newFeedbackFixture(t *testing.T) *feedbackFixture {
	t.Helper()
	store, _ := sqlitetest.Open(t)
	f := &feedbackFixture{t: t, store: store, now: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), repo: model.RepoRef{Owner: "acme", Name: "widgets"}}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "owner"}
	err := store.PutTask(t.Context(), model.Task{ID: "feedback", Title: "repair", Intent: model.IntentImplement,
		Filing:   model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective, Target: &f.repo,
		Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#12"}}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *feedbackFixture) event(kind, action string, id int64, sender string) {
	f.t.Helper()
	ev := ghwebhook.Event{Event: kind, Action: action, Repo: f.repo.String(), Number: 12, IsPullRequest: true,
		SenderLogin: sender, CommentID: id, ReviewID: id, CommentBody: "Please cover the boundary case", ReviewBody: "Please cover the boundary case",
		ReviewState: "commented", HTMLURL: fmt.Sprintf("https://github.com/acme/widgets/pull/12#issuecomment-%d", id)}
	err := processGitHubEvent(f.t.Context(), Deps{Store: f.store, Client: feedbackClient{}, AppBotIdentity: func(context.Context, model.RepoRef) (string, bool) { return "grain-bot[bot]", true }},
		[]string{f.repo.String()}, map[string]bool{}, ev, f.now.Add(10*time.Minute))
	if err != nil {
		f.t.Fatal(err)
	}
}
func (f *feedbackFixture) cycle(want int) []dispatch.Dispatch {
	f.t.Helper()
	got, err := dispatch.Cycle(f.t.Context(), f.store, model.Limits{Workers: 1}, f.now.Add(time.Hour))
	if err != nil || len(got) != want {
		f.t.Fatalf("dispatch = %+v, %v; want %d", got, err, want)
	}
	return got
}
func (f *feedbackFixture) comments() []model.Comment {
	f.t.Helper()
	cs, err := f.store.Comments(f.t.Context(), "feedback")
	if err != nil {
		f.t.Fatal(err)
	}
	return cs
}
func (f *feedbackFixture) question(secret bool) int64 {
	f.t.Helper()
	id, err := f.store.AddComment(f.t.Context(), model.Comment{TaskID: "feedback", Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "feedback"}}, Body: "Which compatibility policy?", CreatedAt: f.now.Add(2 * time.Minute)})
	if err != nil {
		f.t.Fatal(err)
	}
	err = f.store.ObserveField(f.t.Context(), "feedback", f.now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &id
		if secret {
			o.PendingSecret = "token"
		}
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}
func (f *feedbackFixture) ciWait(finish bool) {
	f.t.Helper()
	ctx := f.t.Context()
	if err := f.store.StartRun(ctx, model.Run{ID: "feedback-1", TaskID: "feedback", Attempt: 1, StartedAt: f.now}, model.Limits{}); err != nil {
		f.t.Fatal(err)
	}
	at := f.now.Add(time.Minute)
	if err := f.store.SetDeferred(ctx, "feedback", &at, nil, at); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.HoldResumeOn(ctx, "feedback", model.ResumeOn{CI: "my-branch", Head: "still-pending-head"}, at); err != nil {
		f.t.Fatal(err)
	}
	if finish {
		if err := f.store.FinishRun(ctx, "feedback-1", f.now.Add(3*time.Minute), "succeeded", ""); err != nil {
			f.t.Fatal(err)
		}
	}
}
func (f *feedbackFixture) finish(d dispatch.Dispatch, through int64) {
	f.t.Helper()
	if err := f.store.AdvanceDeliveredFrom(f.t.Context(), "feedback", through, f.now.Add(2*time.Hour)); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.FinishRun(f.t.Context(), d.RunID, f.now.Add(2*time.Hour), "succeeded", ""); err != nil {
		f.t.Fatal(err)
	}
}

func TestPRFeedbackCIWaitOneDispatchRetainsEvidence(t *testing.T) {
	for _, kind := range []string{"issue_comment", "pull_request_review", "pull_request_review_comment"} {
		t.Run(kind, func(t *testing.T) {
			f := newFeedbackFixture(t)
			f.ciWait(true)
			f.cycle(0)
			action := "created"
			if kind == "pull_request_review" {
				action = "submitted"
			}
			f.event(kind, action, 101, "alice")
			cs := f.comments()
			if len(cs) != 1 || !strings.Contains(cs[0].Body, "alice") || !strings.Contains(cs[0].Body, "https://github.com/") || !strings.Contains(cs[0].Body, "untrusted") || model.CommentByPerson(cs[0]) {
				t.Fatalf("feedback provenance = %+v", cs)
			}
			pending, err := f.store.PendingPRFeedback(t.Context(), "feedback", cs[0].ID)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending = %+v, %v", pending, err)
			}
			d := f.cycle(1)[0]
			f.cycle(0)
			holds, err := f.store.HeldCIResumes(t.Context())
			if err != nil || len(holds) != 1 || holds[0].Head != "still-pending-head" {
				t.Fatalf("CI evidence lost: %+v, %v", holds, err)
			}
			f.finish(d, cs[0].ID)
			f.cycle(0)
			f.event(kind, action, 101, "alice")
			f.cycle(0)
			if len(f.comments()) != 1 {
				t.Fatal("replay duplicated context")
			}
		})
	}
}
func TestPRFeedbackQuestionIsNotAnAnswerOrApproval(t *testing.T) {
	f := newFeedbackFixture(t)
	q := f.question(false)
	f.cycle(0)
	f.event("issue_comment", "created", 201, "alice")
	d := f.cycle(1)[0]
	cs := f.comments()
	f.finish(d, cs[len(cs)-1].ID)
	f.cycle(0)
	o, err := f.store.GetObservation(t.Context(), "feedback")
	if err != nil || o.PendingQuestionCommentID == nil || *o.PendingQuestionCommentID != q || o.PendingAccess != nil || o.CompletedAt != nil {
		t.Fatalf("question changed: %+v, %v", o, err)
	}
}
func TestPRFeedbackQueuedAndRunningNoDuplicateAndUnseenSurvives(t *testing.T) {
	f := newFeedbackFixture(t)
	f.event("issue_comment", "created", 301, "alice")
	initial := f.comments()[0].ID
	d := f.cycle(1)[0]
	f.event("issue_comment", "created", 302, "alice")
	f.event("issue_comment", "created", 303, "alice")
	f.cycle(0)
	// The final parking tool does not poll addenda. A newer question must not
	// silently consume comments the agent was never shown.
	q := f.question(false)
	f.finish(d, initial)
	next := f.cycle(1)[0]
	f.cycle(0)
	cs := f.comments()
	pending, err := f.store.PendingPRFeedback(t.Context(), "feedback", cs[len(cs)-1].ID)
	if err != nil || len(pending) != 2 {
		t.Fatalf("unseen feedback lost: %+v, %v", pending, err)
	}
	f.finish(next, cs[len(cs)-1].ID)
	f.cycle(0)
	o, _ := f.store.GetObservation(t.Context(), "feedback")
	if o.PendingQuestionCommentID == nil || *o.PendingQuestionCommentID != q {
		t.Fatal("unrelated question answered")
	}
}
func TestPRFeedbackBeforeCIWaitFinishesStillPending(t *testing.T) {
	f := newFeedbackFixture(t)
	f.ciWait(false)
	f.event("issue_comment", "created", 401, "alice")
	f.cycle(0)
	if err := f.store.FinishRun(t.Context(), "feedback-1", f.now.Add(3*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	f.cycle(1)
}
func TestPRFeedbackReplayEditsSelfAndUnauthorized(t *testing.T) {
	f := newFeedbackFixture(t)
	f.question(false)
	f.event("issue_comment", "created", 501, "grain-bot[bot]")
	f.event("issue_comment", "created", 502, "outsider")
	f.event("issue_comment", "edited", 503, "alice")
	f.cycle(0)
	f.event("issue_comment", "created", 504, "alice")
	f.event("issue_comment", "created", 504, "alice")
	f.event("issue_comment", "created", 505, "alice")
	if len(f.comments()) != 3 {
		t.Fatalf("expected question plus two new comments, got %+v", f.comments())
	}
	d := f.cycle(1)[0]
	cs := f.comments()
	f.finish(d, cs[len(cs)-1].ID)
	f.cycle(0)
}
func TestPRFeedbackProtectsOperatorAndPermissionHolds(t *testing.T) {
	for _, hold := range []string{"pause", "stop", "closed", "secret", "access", "operator-defer", "future", "unapproved", "review-questions", "blocked"} {
		t.Run(hold, func(t *testing.T) {
			f := newFeedbackFixture(t)
			f.question(hold == "secret")
			ctx := t.Context()
			var err error
			switch hold {
			case "stop":
				var id int64
				id, err = f.store.AddComment(ctx, model.Comment{TaskID: "feedback", Body: model.TurnStoppedComment, CreatedAt: f.now, Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "owner"}}})
				if err == nil {
					err = f.store.ObserveField(ctx, "feedback", f.now, func(o *model.Observation) { o.PendingQuestionCommentID = &id })
				}
			case "pause":
				_, err = f.store.Pause(ctx, model.TaskPause{TaskID: "feedback", PausedAt: f.now, PausedBy: model.Principal{Kind: model.PrincipalHuman, ID: "owner"}})
			case "closed":
				err = f.store.ObserveField(ctx, "feedback", f.now, func(o *model.Observation) { o.ClosedAt = &f.now })
			case "access":
				err = f.store.ObserveField(ctx, "feedback", f.now, func(o *model.Observation) { o.PendingAccess = &model.PendingAccessRequest{} })
			case "operator-defer":
				err = f.store.SetDeferred(ctx, "feedback", &f.now, nil, f.now)
			case "future":
				future := f.now.Add(24 * time.Hour)
				err = f.store.SetDeferred(ctx, "feedback", &f.now, &future, f.now)
			case "unapproved":
				err = f.store.UpdateTask(ctx, "feedback", func(task *model.Task) error { task.Approval = nil; return nil })
			case "review-questions":
				err = f.store.UpdateTask(ctx, "feedback", func(task *model.Task) error {
					task.Links = append(task.Links, model.Link{Kind: model.LinkReviewQuestions, Target: "pending"})
					return nil
				})
			case "blocked":
				err = f.store.UpdateTask(ctx, "feedback", func(task *model.Task) error {
					task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: "other"})
					return nil
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			f.event("issue_comment", "created", 601, "alice")
			f.cycle(0)
		})
	}
}
