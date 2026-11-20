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

// A template-less sensitive trigger's findings sit on `targeted-review`:
// POST /api/tasks/{id}/accept-findings accepts them on `targeted-review`,
// refuses to clear them under `review` (and vice versa), and restores
// auto-merge once both gates' findings are accepted.
func TestAcceptFindingsTargetedReviewGate(t *testing.T) {
	_, client := testServer(t)
	ctx := context.Background()
	seedReviewed(t, client, "710", "711", "pr-review")
	seedAPITask(t, client, "712", "acme/widgets")
	approve(t, client, "712")
	if err := client.Store.UpdateTask(ctx, "712", func(tk *model.Task) error {
		tk.Filing.Reason = model.ReasonReview
		tk.TemplateID = model.SecurityReviewTemplateID
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkProposedBy, Target: "710"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.UpdateTask(ctx, "710", func(tk *model.Task) error {
		tk.Links = append(tk.Links,
			model.Link{Kind: model.LinkPathTriggerTask, Target: "every-pr=711"},
			model.Link{Kind: model.LinkPathTriggerTask, Target: "payments=712"},
			model.Link{Kind: model.LinkTargetedReviewTrigger, Target: "payments"},
		)
		model.AwaitPersonForAutoMerge(tk, "review 712 left findings")
		model.AddReviewFindings(tk, model.ReviewFindingsRecord{
			Gate: "trigger:every-pr", ReviewTask: "711", Summary: "every-pr finding", HeadSHA: findingsHead, At: time.Now(),
		})
		model.AddReviewFindings(tk, model.ReviewFindingsRecord{
			Gate: "trigger:payments", ReviewTask: "712", Summary: "payments finding", HeadSHA: findingsHead, At: time.Now(),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := time.Now()
	if err := client.Store.Observe(ctx, model.Observation{
		TaskID: "710", CompletedAt: &done,
		CI: &model.CIStatus{RunID: "ci-1", Commit: findingsHead, Conclusion: "success", FinishedAt: &done},
	}); err != nil {
		t.Fatal(err)
	}

	person := personCtx()
	if _, err := client.AcceptReviewFindings(person, "710", ui.AcceptFindingsRequest{
		Gate: model.GateReview, ReviewTask: "712", Reason: "wrong gate",
	}); err == nil {
		t.Error("targeted review 712's findings were accepted under -gate review")
	}

	res, err := client.AcceptReviewFindings(person, "710", ui.AcceptFindingsRequest{
		Gate: model.GateTargetedReview, Reason: "payments fixture only; follow-up filed", Head: findingsHead[:12],
	})
	if err != nil {
		t.Fatalf("accepting targeted-review findings: %v", err)
	}
	if res.Acceptance.Gate != model.GateTargetedReview || res.Acceptance.ReviewTask != "712" || res.Acceptance.Findings != "payments finding" {
		t.Errorf("acceptance = %+v, want targeted-review review 712", res.Acceptance)
	}
	if g := gateOf(res.Gates, model.GateTargetedReview); g.State != model.GateStatePassed || g.Acceptable {
		t.Errorf("targeted-review gate = %+v, want passed and no longer acceptable", g)
	}
	if g := gateOf(res.Gates, model.GateReview); g.State != model.GateStateFindings || !g.Acceptable {
		t.Errorf("code-review gate = %+v, want still acceptable findings", g)
	}
	if mustTask(t, client, "710").AutoMerge {
		t.Error("auto-merge came back while the code-review gate's findings still stand")
	}

	res, err = client.AcceptReviewFindings(person, "710", ui.AcceptFindingsRequest{
		Gate: model.GateReview, Reason: "nit accepted as-is",
	})
	if err != nil {
		t.Fatalf("accepting code-review findings: %v", err)
	}
	if g := gateOf(res.Gates, model.GateReview); g.State != model.GateStatePassed {
		t.Errorf("code-review gate = %+v, want passed", g)
	}
	got := mustTask(t, client, "710")
	if !got.AutoMerge {
		t.Error("auto-merge was not restored once both review gates' findings were accepted")
	}

	// Accepting targeted-review findings must not re-enable auto-merge when
	// the repository's merge policy (`auto_merge: never`), a QA hold, or a
	// broken-rules hold still stands.
	for i, hold := range []model.Link{
		{Kind: model.LinkMergePolicyWithdrawn, Target: "never"},
		{Kind: model.LinkQAHold, Target: "qa-running"},
		{Kind: model.LinkBrokenRulesHold, Target: "bad-rules"},
	} {
		tid := "72" + string(rune('0'+i))
		rid := "73" + string(rune('0'+i))
		seedReviewed(t, client, tid, rid, model.SecurityReviewTemplateID)
		if err := client.Store.UpdateTask(ctx, tid, func(tk *model.Task) error {
			tk.AutoMerge = false
			tk.Links = append(tk.Links,
				hold,
				model.Link{Kind: model.LinkPathTriggerTask, Target: "payments=" + rid},
				model.Link{Kind: model.LinkTargetedReviewTrigger, Target: "payments"},
			)
			model.AddReviewFindings(tk, model.ReviewFindingsRecord{
				Gate: "trigger:payments", ReviewTask: rid, Summary: "payments finding", HeadSHA: findingsHead, At: time.Now(),
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := client.Store.Observe(ctx, model.Observation{
			TaskID: tid, CompletedAt: &done,
			CI: &model.CIStatus{RunID: "ci-1", Commit: findingsHead, Conclusion: "success", FinishedAt: &done},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.AcceptReviewFindings(person, tid, ui.AcceptFindingsRequest{
			Gate: model.GateTargetedReview, Reason: "accepted under hold",
		}); err != nil {
			t.Fatalf("%s: accepting targeted-review findings: %v", hold.Kind, err)
		}
		if mustTask(t, client, tid).AutoMerge {
			t.Errorf("%s: accepting findings restored auto-merge while %s still stood", tid, hold.Kind)
		}
	}
}

// A superseded security review's findings (recorded under security-review
// after LinkSecurityReviewTask moved to a newer review) are accepted on
// security-review -- even without naming reviewTask -- and refused on review.
func TestAcceptFindingsSupersededSecurityReview(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	ctx := context.Background()
	person := personCtx()
	seedReviewed(t, client, "740", "741", model.SecurityReviewTemplateID)
	if err := client.Store.UpdateTask(ctx, "740", func(tk *model.Task) error {
		tk.AutoMerge = false
		tk.Links = append(tk.Links,
			model.Link{Kind: model.LinkSecurityReviewTask, Target: "742"},
			model.Link{Kind: model.LinkReviewQuestions, Target: model.ReviewQuestions{ReviewTaskID: "741", AfterCommentID: 12}.Target()},
		)
		model.AddReviewFindings(tk, model.ReviewFindingsRecord{
			Gate: model.GateSecurityReview, ReviewTask: "741", Summary: "old security finding", HeadSHA: findingsHead, At: time.Now(),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := time.Now().Add(-time.Minute)
	if err := client.Store.Observe(ctx, model.Observation{
		TaskID: "740", CompletedAt: &done,
		CI: &model.CIStatus{RunID: "ci-1", Commit: findingsHead, Conclusion: "success", FinishedAt: &done},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := client.AcceptReviewFindings(person, "740", ui.AcceptFindingsRequest{
		Gate: model.GateReview, Reason: "wrong gate",
	}); err == nil {
		t.Fatal("AcceptReviewFindings accepted superseded security review 741 on code-review gate")
	}

	res, err := client.AcceptReviewFindings(person, "740", ui.AcceptFindingsRequest{
		Gate: model.GateSecurityReview, Reason: "addressed on newer head",
	})
	if err != nil {
		t.Fatalf("AcceptReviewFindings on security-review: %v", err)
	}
	if res.Acceptance.Gate != model.GateSecurityReview || res.Acceptance.ReviewTask != "741" {
		t.Errorf("acceptance = %+v, want security-review / 741", res.Acceptance)
	}
	if g := gateOf(res.Gates, model.GateSecurityReview); g.State != model.GateStatePassed || g.Acceptable {
		t.Errorf("security-review gate = %+v, want passed and no longer acceptable", g)
	}
	for _, g := range res.Gates {
		if g.ID == model.GateReview {
			t.Errorf("unexpected code-review gate: %+v", g)
		}
	}
	got := mustTask(t, client, "740")
	if model.HasReviewFindings(got) {
		t.Errorf("findings or questions remain: %+v", got.Links)
	}
	if !got.AutoMerge {
		t.Error("auto-merge was not restored once superseded security findings were accepted")
	}
}
