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

// A person accepting a review's findings (accept_findings.go).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const findingsHead = "aaaa1111bbbb2222cccc3333dddd4444eeee5555"

// seedFindings is #4591's shape: change 700 whose review 701 left
// findings on its security-review gate and on its every-pr trigger's,
// with grain CI green at findingsHead.
func seedFindings(t *testing.T, client *ui.Client) {
	t.Helper()
	ctx := context.Background()
	seedReviewed(t, client, "700", "701", model.SecurityReviewTemplateID)
	if err := client.Store.UpdateTask(ctx, "700", func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkSecurityReviewTask, Target: "701"},
			model.Link{Kind: model.LinkPathTriggerTask, Target: "every-pr=701"})
		model.AddReviewFindings(tk, model.ReviewFindingsRecord{Gate: model.GateSecurityReview, ReviewTask: "701",
			Summary: "security review left findings", HeadSHA: findingsHead, At: time.Now()})
		model.AddReviewFindings(tk, model.ReviewFindingsRecord{Gate: "trigger:every-pr", ReviewTask: "701",
			Summary: "every-pr review left findings", HeadSHA: findingsHead, At: time.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := time.Now()
	if err := client.Store.Observe(ctx, model.Observation{TaskID: "700", CompletedAt: &done,
		CI: &model.CIStatus{RunID: "ci-1", Commit: findingsHead, Conclusion: "success", FinishedAt: &done}}); err != nil {
		t.Fatal(err)
	}
}

func gateOf(gates []model.MergeGate, id string) model.MergeGate {
	for _, g := range gates {
		if g.ID == id {
			return g
		}
	}
	return model.MergeGate{}
}

// An agent never accepts findings, whatever it holds: the change's own
// run resolving threads and filing a follow-up is not the owner's decision.
func TestAcceptFindingsRefusesAnAgent(t *testing.T) {
	_, client := testServer(t)
	seedFindings(t, client)
	for _, ctx := range []context.Context{agentCtx("700"), agentCtx("701")} {
		_, err := client.AcceptReviewFindings(ctx, "700", ui.AcceptFindingsRequest{
			Gate: model.GateSecurityReview, ReviewTask: "701", Reason: "merge as-is"})
		var fe *ui.ForbiddenError
		if !errors.As(err, &fe) {
			t.Errorf("an agent's acceptance = %v, want a ForbiddenError", err)
		}
	}
	got := mustTask(t, client, "700")
	if len(model.ReviewFindings(got)) != 2 || len(model.ReviewFindingsAcceptances(got)) != 0 {
		t.Fatalf("an agent's refused acceptance changed the record: %+v", got.Links)
	}
}

// A person's acceptance releases that gate alone, recorded with who, why,
// which review and which head; once every findings gate is accepted and
// nothing else holds the change, it merges itself as it was going to.
func TestAcceptFindingsReleasesOneGateWithAudit(t *testing.T) {
	_, client := testServer(t)
	seedFindings(t, client)
	ctx := personCtx()

	if _, err := client.AcceptReviewFindings(ctx, "700", ui.AcceptFindingsRequest{Gate: model.GateSecurityReview}); err == nil {
		t.Error("an acceptance without a reason was taken")
	}
	if _, err := client.AcceptReviewFindings(ctx, "700", ui.AcceptFindingsRequest{Gate: "ci", Reason: "x"}); err == nil {
		t.Error("accepting findings on the CI gate was taken")
	}
	if _, err := client.AcceptReviewFindings(ctx, "700", ui.AcceptFindingsRequest{
		Gate: model.GateSecurityReview, ReviewTask: "799", Reason: "x"}); err == nil {
		t.Error("accepting a review with no findings on the gate was taken")
	}
	if _, err := client.AcceptReviewFindings(ctx, "700", ui.AcceptFindingsRequest{
		Gate: model.GateSecurityReview, Reason: "x", Head: "0123456789abcdef0123456789abcdef01234567"}); err == nil ||
		!strings.Contains(err.Error(), "moved") {
		t.Errorf("an acceptance pinned to a head the change has moved from = %v, want refused", err)
	}

	res, err := client.AcceptReviewFindings(ctx, "700", ui.AcceptFindingsRequest{
		Gate: model.GateSecurityReview, Reason: "merge as-is; follow-up filed to discuss the extensions"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Acceptance.ReviewTask != "701" || res.Acceptance.By != "alice" || res.Acceptance.HeadSHA != findingsHead {
		t.Errorf("acceptance = %+v, want review 701 by alice at %s", res.Acceptance, findingsHead)
	}
	if g := gateOf(res.Gates, model.GateSecurityReview); g.State != model.GateStatePassed {
		t.Errorf("security-review gate = %+v, want passed", g)
	}
	if g := gateOf(res.Gates, model.GateReview); g.State != model.GateStateFindings {
		t.Errorf("code-review gate = %+v, want still findings", g)
	}
	got := mustTask(t, client, "700")
	if got.AutoMerge {
		t.Error("auto-merge came back while the trigger's findings still stand")
	}
	ev := eventsOf(t, client, "700", model.TaskEventReviewFindingsAccepted)
	if len(ev) != 1 || ev[0].By.ID != "alice" || !strings.Contains(ev[0].Reason, "merge as-is") ||
		!strings.Contains(ev[0].Detail, "gate=security-review review=701") {
		t.Fatalf("accepted events = %+v, want one by alice naming the gate and review", ev)
	}
	comments, err := client.Store.Comments(context.Background(), "700")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(comments); n == 0 || !strings.Contains(comments[n-1].Body, "Review findings accepted") {
		t.Errorf("no acceptance note on the task's conversation: %+v", comments)
	}

	if _, err := client.AcceptReviewFindings(ctx, "700", ui.AcceptFindingsRequest{
		Gate: model.GateReview, ReviewTask: "701", Reason: "same: merge as-is", Head: findingsHead[:12]}); err != nil {
		t.Fatal(err)
	}
	got = mustTask(t, client, "700")
	if model.HasReviewFindings(got) {
		t.Errorf("findings remain after both gates were accepted: %+v", got.Links)
	}
	if !got.AutoMerge {
		t.Error("with every findings gate accepted and nothing else holding it, the change was not handed its auto-merge back")
	}
	if accs := model.ReviewFindingsAcceptances(got); len(accs) != 2 {
		t.Errorf("acceptances = %+v, want two", accs)
	}
	if _, err := client.AcceptReviewFindings(ctx, "700", ui.AcceptFindingsRequest{
		Gate: model.GateReview, Reason: "again"}); err == nil {
		t.Error("accepting a gate with no findings left was taken")
	}
}

// Accepting findings clears nothing else: a `human: required` trigger's
// approver is still owed, so the change does not merge itself.
func TestAcceptFindingsLeavesApprovalsStanding(t *testing.T) {
	_, client := testServer(t)
	seedFindings(t, client)
	if err := client.Store.UpdateTask(context.Background(), "700", func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkApprovalRequired, Target: "trigger:every-pr=bob"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, gate := range []string{model.GateSecurityReview, model.GateReview} {
		if _, err := client.AcceptReviewFindings(personCtx(), "700", ui.AcceptFindingsRequest{Gate: gate, Reason: "as-is"}); err != nil {
			t.Fatal(err)
		}
	}
	got := mustTask(t, client, "700")
	if got.AutoMerge {
		t.Error("accepting findings let a change whose approver is still owed merge itself")
	}
	if len(model.PendingApprovals(got)) == 0 {
		t.Error("accepting findings cleared the approval a trigger asks for")
	}
}
