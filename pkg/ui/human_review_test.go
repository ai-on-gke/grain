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

// The human-review hold's two doors (human_review.go): a review run
// raising it on the change it reviews, and a person releasing it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// seedReviewed files an approved, auto-merging task with an open pull
// request, and a review of it: a security review or a template review.
func seedReviewed(t *testing.T, client *ui.Client, reviewedID, reviewID, template string) {
	t.Helper()
	ctx := context.Background()
	seedAPITask(t, client, reviewedID, "acme/widgets")
	approve(t, client, reviewedID)
	if err := client.Store.UpdateTask(ctx, reviewedID, func(tk *model.Task) error {
		tk.AutoMerge = true
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#42"},
			model.Link{Kind: model.LinkTrustBoundaryCleared, Target: "trust-boundary/v1@abc"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, reviewID, "acme/widgets")
	approve(t, client, reviewID)
	if err := client.Store.UpdateTask(ctx, reviewID, func(tk *model.Task) error {
		tk.Origin.Reason = model.ReasonReview
		tk.TemplateID = template
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkProposedBy, Target: reviewedID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func agentCtx(id string) context.Context {
	return ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalAgent, ID: id})
}

func personCtx() context.Context {
	return ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalHuman, ID: "alice"})
}

func eventsOf(t *testing.T, client *ui.Client, id string, kind model.TaskEventKind) []model.TaskEvent {
	t.Helper()
	events, err := client.Store.TaskEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.TaskEvent
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func mustTask(t *testing.T, client *ui.Client, id string) model.Task {
	t.Helper()
	task, err := client.Store.GetTask(context.Background(), id)
	if err != nil || task == nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	return *task
}

// A security review asking for human review turns the reviewed change's
// auto-merge off, records the reason with the review that raised it, and
// puts the change in the inbox, on its wire shape and on its timeline.
func TestSecurityReviewRequestsHumanReview(t *testing.T) {
	srv, client := testServer(t)
	seedReviewed(t, client, "500", "501", model.SecurityReviewTemplateID)

	res, err := client.RequestHumanReview(agentCtx("501"), "501", ui.HumanReviewRequestBody{
		Reason: "Changes who may read secrets; product must agree", Details: "## Why\nIt widens the read path.",
		Category: "security"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task != "500" || !res.Added || len(res.Reasons) != 1 {
		t.Fatalf("result = %+v, want task 500 held for one new reason", res)
	}
	got := mustTask(t, client, "500")
	if got.AutoMerge {
		t.Fatal("the reviewed change kept auto-merge")
	}
	reasons := model.HumanReviewReasons(got)
	if len(reasons) != 1 || reasons[0].ReviewTask != "501" || reasons[0].Category != "security" ||
		reasons[0].Source != model.HumanReviewRequested {
		t.Fatalf("reasons = %+v", reasons)
	}
	ev := eventsOf(t, client, "500", model.TaskEventHumanReviewRequested)
	if len(ev) != 1 || ev[0].By.Kind != model.PrincipalAgent || ev[0].By.ID != "501" ||
		!strings.Contains(ev[0].Detail, "review task 501") {
		t.Fatalf("events = %+v, want one attributed to review 501", ev)
	}
	detail, err := client.GetTask(context.Background(), "500")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.HumanReviewHold) != 1 || detail.HumanReviewHold[0].Reason != reasons[0].Reason {
		t.Fatalf("wire humanReviewHold = %+v", detail.HumanReviewHold)
	}
	timeline := false
	for _, tr := range detail.Transitions {
		if tr.State == model.TransitionHumanReviewRequested {
			timeline = true
		}
	}
	if !timeline {
		t.Errorf("timeline = %+v, want a human_review_requested step", detail.Transitions)
	}
	if n, _ := client.Store.OpenNotification(context.Background(), model.HumanReviewNotificationSubject("500")); n == nil {
		t.Error("no notification was raised for the owner")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/inbox", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var inbox ui.InboxResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &inbox); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range inbox.Items {
		if it.Task == "500" {
			if it.Kind != ui.InboxMergeHeld || !strings.Contains(it.Reason, "product must agree") {
				t.Errorf("inbox item = %+v, want merge-held with the reason", it)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("inbox = %+v, want task 500 in it", inbox.Items)
	}
}

// A template review raises the same hold; one hold collects several
// reasons, a repeated reason adds nothing, and a review that keeps asking
// is cut off.
func TestTemplateReviewRequestsHumanReviewDeduplicatedAndRateLimited(t *testing.T) {
	_, client := testServer(t)
	seedReviewed(t, client, "510", "511", "tmpl-code-review")
	ctx := agentCtx("511")
	ask := func(reason string) (ui.HumanReviewResult, error) {
		return client.RequestHumanReview(ctx, "511", ui.HumanReviewRequestBody{Reason: reason, Category: "product-decision"})
	}
	if _, err := ask("Is the new default what the requester meant?"); err != nil {
		t.Fatal(err)
	}
	again, err := ask("Is the new  default what the requester meant?")
	if err != nil {
		t.Fatal(err)
	}
	if again.Added || len(again.Reasons) != 1 {
		t.Fatalf("a repeated reason = %+v, want nothing added", again)
	}
	for i, r := range []string{"two", "three", "four", "five"} {
		res, err := ask(r)
		if err != nil {
			t.Fatalf("reason %d: %v", i+2, err)
		}
		if len(res.Reasons) != i+2 {
			t.Fatalf("reasons = %d, want %d", len(res.Reasons), i+2)
		}
	}
	if _, err := ask("six"); err == nil {
		t.Fatal("a sixth reason from the same review was accepted")
	}
	if n := len(eventsOf(t, client, "510", model.TaskEventHumanReviewRequested)); n != 5 {
		t.Errorf("%d requested events, want 5 (one per new reason)", n)
	}
	// Bad input is refused, and so is a task that is no review.
	if _, err := client.RequestHumanReview(ctx, "511", ui.HumanReviewRequestBody{Reason: "x", Category: "vibes"}); err == nil {
		t.Error("an unknown category was accepted")
	}
	if _, err := client.RequestHumanReview(agentCtx("510"), "510", ui.HumanReviewRequestBody{Reason: "x"}); err == nil {
		t.Error("the implementing task was allowed to hold itself")
	}
	if _, err := client.RequestHumanReview(agentCtx("999"), "511", ui.HumanReviewRequestBody{Reason: "x"}); err == nil {
		t.Error("one task's agent asked as another review")
	}
}

// Nothing grain does on its own turns a held change's auto-merge back on,
// and an agent cannot release it -- not through Release, not through Submit.
func TestHumanReviewHoldIsNotReleasedAutomaticallyOrByAnAgent(t *testing.T) {
	_, client := testServer(t)
	seedReviewed(t, client, "520", "521", model.SecurityReviewTemplateID)
	if _, err := client.RequestHumanReview(agentCtx("521"), "521", ui.HumanReviewRequestBody{Reason: "risky migration", Category: "risk"}); err != nil {
		t.Fatal(err)
	}
	// A clean review, CI, a trigger settling: each is a write turning
	// auto-merge back on without a person in it.
	clean := model.WithAutoMergeChange(context.Background(), model.GrainPrincipal, "security review finished clean")
	if err := client.Store.UpdateTask(clean, "520", func(tk *model.Task) error {
		tk.AutoMerge = true
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkTrustBoundaryCleared, Target: "trust-boundary/v1@def"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := mustTask(t, client, "520"); got.AutoMerge || !model.HumanReviewHeld(got) {
		t.Fatalf("an automatic write released the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if len(eventsOf(t, client, "520", model.TaskEventAutoMergeKeptOff)) == 0 {
		t.Error("the refused write left no kept-off event")
	}

	if _, err := client.ReleaseHumanReviewHold(agentCtx("521"), "520"); err == nil {
		t.Fatal("an agent released the hold")
	} else {
		var fe *ui.ForbiddenError
		if !errors.As(err, &fe) {
			t.Errorf("an agent's release = %v, want a ForbiddenError", err)
		}
	}
	if err := client.Submit(agentCtx("521"), "520"); err == nil {
		t.Fatal("an agent's Submit released the hold")
	}
	if _, err := client.Store.ReleaseHumanReviewHold(context.Background(), "520",
		model.Principal{Kind: model.PrincipalAgent, ID: "521"}, "released"); !errors.Is(err, model.ErrAgentCannotReleaseHumanReview) {
		t.Fatalf("the store's release for an agent = %v", err)
	}
	if got := mustTask(t, client, "520"); got.AutoMerge || !model.HumanReviewHeld(got) {
		t.Fatalf("the hold did not survive the agent: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// A person's Submit releases the hold, recorded with who did it.
func TestHumanReviewHoldIsReleasedBySubmitWithAudit(t *testing.T) {
	_, client := testServer(t)
	seedReviewed(t, client, "530", "531", "tmpl-code-review")
	if _, err := client.RequestHumanReview(agentCtx("531"), "531", ui.HumanReviewRequestBody{Reason: "design call"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Submit(personCtx(), "530"); err != nil {
		t.Fatal(err)
	}
	got := mustTask(t, client, "530")
	if !got.AutoMerge || model.HumanReviewHeld(got) {
		t.Fatalf("Submit did not release the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	ev := eventsOf(t, client, "530", model.TaskEventHumanReviewReleased)
	if len(ev) != 1 || ev[0].By.ID != "alice" || ev[0].Reason != "submitted" || !strings.Contains(ev[0].Detail, "design call") {
		t.Fatalf("released events = %+v, want one by alice", ev)
	}
	if n, _ := client.Store.OpenNotification(context.Background(), model.HumanReviewNotificationSubject("530")); n != nil {
		t.Error("the hold's notification is still open after Submit")
	}
}

// An explicit release by a person ends the hold, recorded with who did it,
// and hands auto-merge back where grain would have let the change merge
// itself.
func TestHumanReviewHoldIsReleasedExplicitlyWithAudit(t *testing.T) {
	_, client := testServer(t)
	seedReviewed(t, client, "540", "541", "tmpl-code-review")
	if _, err := client.RequestHumanReview(agentCtx("541"), "541", ui.HumanReviewRequestBody{Reason: "intent unclear", Category: "intent-unclear"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReleaseHumanReviewHold(personCtx(), "540"); err != nil {
		t.Fatal(err)
	}
	got := mustTask(t, client, "540")
	if model.HumanReviewHeld(got) {
		t.Fatalf("the hold survived its release: links %+v", got.Links)
	}
	if !got.AutoMerge {
		t.Error("releasing the only hold on a cleared change that was merging itself did not hand auto-merge back")
	}
	ev := eventsOf(t, client, "540", model.TaskEventHumanReviewReleased)
	if len(ev) != 1 || ev[0].By.ID != "alice" || ev[0].Reason != "released" {
		t.Fatalf("released events = %+v, want one by alice", ev)
	}
	if _, err := client.ReleaseHumanReviewHold(personCtx(), "540"); err == nil {
		t.Error("releasing a task with no hold succeeded")
	}
}
