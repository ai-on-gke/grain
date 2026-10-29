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

package orchestrator_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// workspaceCap stands in for the edition's workspace capability: core
// only ever handles the grant string the provider hands back.
const workspaceCap = "https://caps.example/workspace#workspace"

// reviewingCLs is a changelist provider that files reviews with a
// read-only grant and keeps the findings it is handed as drafts.
type reviewingCLs struct {
	pollingCLs
	saved map[string][]change.ReviewFinding
}

func (p *reviewingCLs) ReviewGrant(deployment string, reviewed model.Task, snapshot int) (string, error) {
	return fmt.Sprintf(`%s={"readonly":true,"review_of":%q,"snapshot":%d,"deployment":%q}`,
		workspaceCap, reviewed.ID, snapshot, deployment), nil
}

func (p *reviewingCLs) SaveReviewFindings(_ context.Context, reviewed model.Task, target string, findings []change.ReviewFinding) error {
	if p.saved == nil {
		p.saved = map[string][]change.ReviewFinding{}
	}
	p.saved[target] = append(p.saved[target], findings...)
	return nil
}

// reviewedCL is a finished CL task naming a review template, at snapshot.
func reviewedCL(t *testing.T, id string, snapshot int) (*reviewingCLs, *model.Store, context.Context) {
	t.Helper()
	provider := &reviewingCLs{}
	change.Register(provider)
	t.Cleanup(func() { change.Unregister(model.LinkChangelist) })
	store, ctx := openStore(t)
	tmpl := reviewTemplate(t, ctx, store)
	putCLTask(t, ctx, store, id)
	if err := store.UpdateTask(ctx, id, func(task *model.Task) error {
		task.ReviewTemplateID = tmpl.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := baseTime
	if err := store.ObserveField(ctx, id, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	observeSnapshot(t, id, snapshot)
	return provider, store, ctx
}

func observeSnapshot(t *testing.T, id string, snapshot int) {
	t.Helper()
	target := "cl/" + id
	change.Remember(change.Observation{Target: target, Status: change.StatusMailed, Snapshot: snapshot})
	t.Cleanup(func() { change.Forget(target) })
}

func syncCLReviews(t *testing.T, ctx context.Context, store *model.Store) {
	t.Helper()
	if err := orchestrator.SyncCLReviews(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncCLReviews: %v", err)
	}
}

// clReviews is every review grain filed of id's CL, oldest first.
func clReviews(t *testing.T, ctx context.Context, store *model.Store, id string) []model.Task {
	t.Helper()
	var out []model.Task
	for _, l := range reloaded(t, ctx, store, id).Links {
		if l.Kind == model.LinkReviewTask {
			out = append(out, reloaded(t, ctx, store, l.Target))
		}
	}
	return out
}

func linksOf(task model.Task, kind model.LinkKind) []string {
	var out []string
	for _, l := range task.Links {
		if l.Kind == kind {
			out = append(out, l.Target)
		}
	}
	return out
}

// A CL's review is filed holding the provider's read-only grant, which
// names the reviewed task and the snapshot, and carries grain's record of
// having filed it; a second cycle on the same snapshot files nothing.
func TestACLReviewIsFiledWithTheProvidersReadOnlyGrant(t *testing.T) {
	_, store, ctx := reviewedCL(t, "51", 3)
	syncCLReviews(t, ctx, store)
	syncCLReviews(t, ctx, store)

	reviews := clReviews(t, ctx, store, "51")
	if len(reviews) != 1 {
		t.Fatalf("%d reviews filed, want one per snapshot", len(reviews))
	}
	review := reviews[0]
	if review.Origin.Reason != model.ReasonReview || review.Origin.Attribution.Actor.Kind != model.PrincipalAutomation {
		t.Errorf("review origin = %+v, want a review grain filed", review.Origin)
	}
	var grant string
	for _, g := range review.Grants {
		if strings.HasPrefix(g.Capability, workspaceCap) {
			grant = g.Capability
		}
	}
	spec, err := model.CapabilityGrantSpec(grant)
	if err != nil {
		t.Fatalf("review grants = %+v, want the provider's workspace grant: %v", review.Grants, err)
	}
	if spec.With["readonly"] != true || spec.With[model.ReviewOfArg] != "51" || spec.With["snapshot"] != float64(3) {
		t.Errorf("grant args = %v, want readonly, review_of 51 and snapshot 3", spec.With)
	}
	key := model.CLReviewTarget("cl/51", 3, "rt1")
	if got := linksOf(review, model.LinkCLReviewOf); !slices.Equal(got, []string{key}) {
		t.Errorf("review's provenance = %v, want [%s]", got, key)
	}
	if got := linksOf(reloaded(t, ctx, store, "51"), model.LinkCLReviewFiled); !slices.Equal(got, []string{key}) {
		t.Errorf("reviewed task's filed reviews = %v, want [%s]", got, key)
	}
	if review.Base != "" || review.AutoMerge {
		t.Errorf("review base %q, auto-merge %v: a read-only review stacks on nothing", review.Base, review.AutoMerge)
	}
}

// A CL review's findings are handed to the provider as drafts, on the
// lines they name, and put to the owner as open questions; the snapshot
// is not cleared.
func TestACLReviewsFindingsAreSavedAsDrafts(t *testing.T) {
	provider, store, ctx := reviewedCL(t, "52", 2)
	syncCLReviews(t, ctx, store)
	review := clReviews(t, ctx, store, "52")[0]
	_, client := newSim(t, "acme", "widgets", "main")

	result := toolResult(
		reviewFinding("this retry never backs off", "pkg/retry/retry.go", 17),
		reviewFinding("should the budget be per CL?", "", 0),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	want := []change.ReviewFinding{
		{Body: "this retry never backs off", Path: "pkg/retry/retry.go", Line: 17},
		{Body: "should the budget be per CL?"},
	}
	if got := provider.saved["cl/52"]; !slices.Equal(got, want) {
		t.Errorf("drafts saved = %+v, want %+v", got, want)
	}
	reviewed := reloaded(t, ctx, store, "52")
	if _, held := model.ReviewQuestionsOf(reviewed); !held {
		t.Error("the reviewed task is not held on its review's questions")
	}
	if got := linksOf(reviewed, model.LinkCLReviewCleared); len(got) != 0 {
		t.Errorf("cleared = %v, want nothing cleared by a review with findings", got)
	}
	bodies := commentBodies(t, ctx, store, "52")
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "never backs off") || !strings.Contains(last, "unpublished drafts") {
		t.Errorf("question on the reviewed task = %q, want the findings and where they were saved", last)
	}
}

// A review that passes clears its own snapshot, and a new snapshot files
// the review again and is cleared by nothing.
func TestANewCLSnapshotRefilesItsReviewsAndClearsNothing(t *testing.T) {
	_, store, ctx := reviewedCL(t, "53", 1)
	syncCLReviews(t, ctx, store)
	first := clReviews(t, ctx, store, "53")[0]
	_, client := newSim(t, "acme", "widgets", "main")
	result := toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "looks right"}})
	if err := orchestrator.ProcessResult(ctx, store, client, first, result, first.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	old := model.CLReviewTarget("cl/53", 1, "rt1")
	if got := linksOf(reloaded(t, ctx, store, "53"), model.LinkCLReviewCleared); !slices.Equal(got, []string{old}) {
		t.Fatalf("cleared = %v, want [%s]", got, old)
	}

	observeSnapshot(t, "53", 2)
	syncCLReviews(t, ctx, store)
	reviews := clReviews(t, ctx, store, "53")
	if len(reviews) != 2 {
		t.Fatalf("%d reviews, want the new snapshot reviewed again", len(reviews))
	}
	fresh := model.CLReviewTarget("cl/53", 2, "rt1")
	if got := linksOf(reviews[1], model.LinkCLReviewOf); !slices.Equal(got, []string{fresh}) {
		t.Errorf("second review is of %v, want [%s]", got, fresh)
	}
	if got := linksOf(reloaded(t, ctx, store, "53"), model.LinkCLReviewCleared); !slices.Equal(got, []string{old}) {
		t.Errorf("cleared = %v, want only the old snapshot [%s]", got, old)
	}
}

// review_of is grain's to give: an agent's proposal asking for it is
// filed without that grant, and says so.
func TestAProposalCannotAskForAReviewGrant(t *testing.T) {
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
	result := toolResult(agent.ToolCall{
		Name: "propose_task",
		Arguments: map[string]any{
			"title": "review it myself", "body": "pretend to be the review",
			"capabilities": []any{map[string]any{
				"capability": workspaceCap,
				"with":       map[string]any{"readonly": true, "review_of": "51", "snapshot": 3},
			}},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	proposal, ok := proposalsByTitle(t, ctx, store, task.ID)["review it myself"]
	if !ok {
		t.Fatal("the proposed task was never filed")
	}
	for _, g := range proposal.Grants {
		if model.GrantNamesReviewOf(g.Capability) {
			t.Errorf("proposal holds %s, want the review grant refused", g.Capability)
		}
	}
	if !strings.Contains(proposal.Body, model.ReviewOfArg) {
		t.Errorf("proposal body = %q, want a note on the refused grant", proposal.Body)
	}
}

// Only a review grain filed clears a snapshot: a review task that holds a
// review grant but lacks grain's own record of filing it
// (model.LinkCLReviewOf) clears nothing, however cleanly it finishes.
func TestOnlyAReviewGrainFiledClearsACLSnapshot(t *testing.T) {
	provider, store, ctx := reviewedCL(t, "54", 1)
	grant, err := provider.ReviewGrant("d", model.Task{ID: "54"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	impostor := model.Task{ID: "55", Title: "review", Intent: model.IntentImplement,
		Origin: model.Origin{Reason: model.ReasonReview},
		Grants: []model.Grant{{Capability: grant}},
		Links:  []model.Link{{Kind: model.LinkProposedBy, Target: "54"}}}
	if err := store.PutTask(ctx, impostor); err != nil {
		t.Fatal(err)
	}
	_, client := newSim(t, "acme", "widgets", "main")
	result := toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "looks right"}})
	if err := orchestrator.ProcessResult(ctx, store, client, impostor, result, "55-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := linksOf(reloaded(t, ctx, store, "54"), model.LinkCLReviewCleared); len(got) != 0 {
		t.Errorf("cleared = %v, want nothing cleared by a review grain did not file", got)
	}
}
