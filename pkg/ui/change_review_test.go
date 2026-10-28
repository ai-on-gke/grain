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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeCritique is a changelist provider with the S4 review loop: replies
// are drafts until published, unless degraded is set.
type fakeCritique struct {
	fakeCLs
	degraded bool
	replies  []string // attempt|thread|body
	mailed   []string
	unpub    int
}

func (f *fakeCritique) Kind() model.LinkKind { return model.LinkChangelist }
func (f *fakeCritique) TargetKind() string   { return "fakecritique" }
func (f *fakeCritique) Reply(_ context.Context, _ model.Task, attempt, thread, body string) (change.ReplyResult, error) {
	f.replies = append(f.replies, attempt+"|"+thread+"|"+body)
	if f.degraded {
		return change.ReplyResult{Thread: thread, Degraded: true, Text: body, Note: "Critique is read-only here."}, nil
	}
	f.unpub++
	return change.ReplyResult{Thread: thread, Draft: "d1"}, nil
}
func (f *fakeCritique) Resolve(_ context.Context, _ model.Task, attempt, thread string) (change.ReplyResult, error) {
	f.replies = append(f.replies, attempt+"|"+thread+"|resolve")
	return change.ReplyResult{Thread: thread, Draft: "d2"}, nil
}
func (f *fakeCritique) PublishRun(context.Context, model.Task, string) (change.PublishResult, error) {
	return change.PublishResult{CLs: []change.PublishedCL{{CL: "cl/42", Drafts: f.unpub, Reason: "not mailed by grain"}}}, nil
}
func (f *fakeCritique) PublishReplies(context.Context, model.Task, change.Person) (change.PublishResult, error) {
	n := f.unpub
	f.unpub = 0
	return change.PublishResult{CLs: []change.PublishedCL{{CL: "cl/42", Drafts: n, Published: n}}}, nil
}
func (f *fakeCritique) Mail(_ context.Context, _ model.Task, target string, reviewers, _ []string, by change.Person) (change.Result, error) {
	f.mailed = append(f.mailed, target+"|"+strings.Join(reviewers, ",")+"|"+by.Name)
	return change.Result{Target: target}, nil
}

func TestChangeReviewDraftsRepliesAndPublishesThem(t *testing.T) {
	client, store, ctx := testClient(t)
	fake := &fakeCritique{}
	change.Register(fake)
	t.Cleanup(func() {
		change.Unregister(model.LinkChangelist)
		change.Forget("cl/42")
		change.RememberPublish("91", change.PublishResult{})
	})
	if err := store.PutTask(ctx, model.Task{ID: "91", Title: "t", Intent: model.IntentImplement,
		ChangeTarget: &model.ChangeTarget{Kind: "fakecritique"},
		Links:        []model.Link{{Kind: model.LinkChangelist, Target: "cl/42"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "91-1", TaskID: "91", Attempt: 1, StartedAt: time.Now()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// A thread on somebody else's CL is refused before the provider is asked.
	if _, err := client.ChangeReview(ctx, "91", ui.ChangeReviewRequest{Op: ui.ChangeReviewReply, Thread: "cl/7:c1", Body: "x"}); err == nil ||
		!strings.Contains(err.Error(), "not on this task's CL") {
		t.Fatalf("reply on another CL = %v, want a refusal", err)
	}
	resp, err := client.ChangeReview(ctx, "91", ui.ChangeReviewRequest{Op: ui.ChangeReviewReply, Thread: "cl/42:c1", Body: "Done."})
	if err != nil || resp.Result.Draft != "d1" {
		t.Fatalf("reply = %+v, %v", resp, err)
	}
	if _, err := client.ChangeReview(ctx, "91", ui.ChangeReviewRequest{Op: ui.ChangeReviewResolve, Thread: "cl/42:c1"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.replies) != 2 || fake.replies[0] != "91-1|cl/42:c1|Done." || fake.replies[1] != "91-1|cl/42:c1|resolve" {
		t.Fatalf("replies = %v, want both attributed to the live run", fake.replies)
	}

	// The run ends and its drafts stay unpublished: the button offers them.
	change.Remember(change.Observation{Target: "cl/42", ReviewState: change.ReviewStateInReview, LGTM: []string{"alice"}})
	task, _ := store.GetTask(ctx, "91")
	if _, err := change.PublishRun(ctx, *task, "91-1"); err != nil {
		t.Fatal(err)
	}
	got, err := client.GetTask(ctx, "91")
	if err != nil {
		t.Fatal(err)
	}
	r := got.CLReview
	if r == nil || r.State != "in_review" || !r.CanPublish || r.Drafts != 1 || r.PublishReason != "not mailed by grain" {
		t.Fatalf("review = %+v, want Publish replies offered for 1 draft", r)
	}
	if r.CanMail || r.MailReason != "already mailed" {
		t.Fatalf("review = %+v, want Mail disabled on a mailed CL", r)
	}
	if _, err := client.PublishReplies(ctx, "91", "bsalmon"); err != nil {
		t.Fatal(err)
	}
	got, _ = client.GetTask(ctx, "91")
	if got.CLReview.CanPublish || got.CLReview.PublishReason != "no replies are waiting" {
		t.Fatalf("review after publishing = %+v", got.CLReview)
	}

	// An unmailed CL offers Mail, which goes as the person who pressed it.
	change.Remember(change.Observation{Target: "cl/42", ReviewState: change.ReviewStateDraft, NeedsMailing: true})
	got, _ = client.GetTask(ctx, "91")
	if !got.CLReview.CanMail {
		t.Fatalf("review = %+v, want Mail offered", got.CLReview)
	}
	if _, err := client.MailChange(ctx, "91", "bsalmon", ui.MailChangeRequest{Reviewers: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.mailed) != 1 || fake.mailed[0] != "cl/42|alice|bsalmon" {
		t.Fatalf("mailed = %v", fake.mailed)
	}
}

func TestADegradedCLReplyGoesToTheConversation(t *testing.T) {
	client, store, ctx := testClient(t)
	fake := &fakeCritique{degraded: true}
	change.Register(fake)
	t.Cleanup(func() { change.Unregister(model.LinkChangelist) })
	if err := store.PutTask(ctx, model.Task{ID: "92", Title: "t", Intent: model.IntentImplement,
		Links: []model.Link{{Kind: model.LinkChangelist, Target: "cl/42"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "92-1", TaskID: "92", Attempt: 1, StartedAt: time.Now()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	resp, err := client.ChangeReview(ctx, "92", ui.ChangeReviewRequest{Op: ui.ChangeReviewReply, Thread: "cl/42:c3", Body: "Renamed."})
	if err != nil || !resp.Result.Degraded {
		t.Fatalf("reply = %+v, %v", resp, err)
	}
	comments, err := store.Comments(ctx, "92")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "Renamed.") ||
		!strings.Contains(comments[0].Body, "cl/42:c3") || !strings.Contains(comments[0].Body, "Critique is read-only here.") {
		t.Fatalf("comments = %+v, want the reply and the provider's note", comments)
	}
}
