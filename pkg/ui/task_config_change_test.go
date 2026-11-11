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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const proposedHead = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// changeStateRepo is fakeStateRepo carrying config changes the way
// pkg/daemon's StateManager does, recording what it was asked.
type changeStateRepo struct {
	*fakeStateRepo
	merged, discarded []model.PendingConfigChange
	refuse            error
	mergeErr          error
}

func (c *changeStateRepo) ProposeConfigChange(_ context.Context, p ui.ConfigChangeProposal) (*model.PendingConfigChange, error) {
	if c.refuse != nil {
		return nil, c.refuse
	}
	return &model.PendingConfigChange{
		Branch: p.Branch, HeadSHA: p.HeadSHA, Summary: p.Summary,
		Files: []string{"tables/grain_config.json"}, Diff: "diff --git a/x b/x\n",
		Validation: model.ConfigChangeValidation{OK: true},
	}, nil
}

func (c *changeStateRepo) MergeConfigChange(_ context.Context, p model.PendingConfigChange) (string, error) {
	if c.mergeErr != nil {
		return "", c.mergeErr
	}
	c.merged = append(c.merged, p)
	return "cccccccccccccccccccccccccccccccccccccccc", nil
}

func (c *changeStateRepo) DiscardConfigChange(_ context.Context, p model.PendingConfigChange) error {
	c.discarded = append(c.discarded, p)
	return nil
}

func proposingTask(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) (string, *changeStateRepo) {
	t.Helper()
	repo := &changeStateRepo{fakeStateRepo: &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}}
	c.Config.StateRepo = repo
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Writes = []model.WriteTarget{{Repo: model.ConfigRepo()}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return task.ID, repo
}

func TestProposeConfigChangeIsAGrainConfigWritersOwnBranchOnly(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	id, _ := proposingTask(t, c, store, ctx)
	other := create(t, c, ctx)

	if _, err := c.ProposeConfigChange(ctx, other.ID, ui.ConfigChangeProposal{
		Summary: "x", Branch: model.ConfigProposalBranchPrefix(other.ID) + "x", HeadSHA: proposedHead,
	}); err == nil {
		t.Fatal("a task without grain/config in its writes proposed a change")
	}
	for _, branch := range []string{"main", model.ConfigProposalBranchPrefix("999") + "x", model.ConfigProposalBranchPrefix(id)} {
		if _, err := c.ProposeConfigChange(ctx, id, ui.ConfigChangeProposal{Summary: "x", Branch: branch, HeadSHA: proposedHead}); err == nil {
			t.Fatalf("proposing from %q was accepted", branch)
		}
	}
	pending, err := c.ProposeConfigChange(ctx, id, ui.ConfigChangeProposal{
		Summary: "Be kind", Branch: model.ConfigProposalBranchPrefix(id) + "be-kind", HeadSHA: proposedHead,
	})
	if err != nil {
		t.Fatalf("ProposeConfigChange: %v", err)
	}
	detail, err := c.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.PendingConfigChange == nil || detail.PendingConfigChange.HeadSHA != pending.HeadSHA {
		t.Fatalf("task detail does not offer the change: %+v", detail.PendingConfigChange)
	}
}

// A manager's refusal of the proposal is the proposer's to fix, so it is a
// validation error (400, with its text) rather than a 500 with only a log
// reference; any other manager error stays a fault.
func TestProposeConfigChangeAnswersARefusalAsInvalid(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	id, repo := proposingTask(t, c, store, ctx)
	branch := model.ConfigProposalBranchPrefix(id) + "again"
	repo.refuse = fmt.Errorf("%w: branch %s changes nothing against main", ui.ErrConfigChangeRefused, branch)
	_, err := c.ProposeConfigChange(ctx, id, ui.ConfigChangeProposal{Summary: "x", Branch: branch, HeadSHA: proposedHead})
	var invalid *ui.ValidationError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "changes nothing") {
		t.Fatalf("refused proposal = %v, want a ValidationError saying why", err)
	}
	repo.refuse = errors.New("disk on fire")
	if _, err := c.ProposeConfigChange(ctx, id, ui.ConfigChangeProposal{Summary: "x", Branch: branch, HeadSHA: proposedHead}); errors.As(err, &invalid) {
		t.Fatalf("a manager fault was answered as invalid input: %v", err)
	}
}

func TestApproveConfigChangeIsAPersonsAndPinnedToTheCommitShown(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	id, repo := proposingTask(t, c, store, ctx)
	if _, err := c.ProposeConfigChange(ctx, id, ui.ConfigChangeProposal{
		Summary: "Be kind", Branch: model.ConfigProposalBranchPrefix(id) + "be-kind", HeadSHA: proposedHead,
	}); err != nil {
		t.Fatal(err)
	}

	agent := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "task-" + id})
	var forbidden *ui.ForbiddenError
	if err := c.ApproveConfigChange(agent, id, ui.ConfigChangeDecision{HeadSHA: proposedHead}); !errors.As(err, &forbidden) {
		t.Fatalf("an agent approving = %v, want forbidden", err)
	}
	if err := c.RejectConfigChange(agent, id, ui.ConfigChangeDecision{HeadSHA: proposedHead}); !errors.As(err, &forbidden) {
		t.Fatalf("an agent rejecting = %v, want forbidden", err)
	}
	if err := c.ApproveConfigChange(ctx, id, ui.ConfigChangeDecision{HeadSHA: strings.Repeat("a", 40)}); !errors.Is(err, ui.ErrConfigChangeMoved) {
		t.Fatalf("approving another commit = %v, want ErrConfigChangeMoved", err)
	}
	if len(repo.merged) != 0 {
		t.Fatal("a refused approval merged")
	}

	if err := c.ApproveConfigChange(ctx, id, ui.ConfigChangeDecision{HeadSHA: proposedHead}); err != nil {
		t.Fatalf("ApproveConfigChange: %v", err)
	}
	if len(repo.merged) != 1 || repo.merged[0].HeadSHA != proposedHead || !repo.synced {
		t.Fatalf("merged %+v, synced %v", repo.merged, repo.synced)
	}
	obs, err := store.GetObservation(ctx, id)
	if err != nil || obs == nil || obs.PendingConfigChange != nil {
		t.Fatalf("pending change not cleared: %+v %v", obs, err)
	}
	comments, err := store.Comments(ctx, id)
	if err != nil || len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "Approved and applied config change") {
		t.Fatalf("the session was not told: %+v %v", comments, err)
	}
}

func TestRejectConfigChangeRelaysTheReason(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	id, repo := proposingTask(t, c, store, ctx)
	if _, err := c.ProposeConfigChange(ctx, id, ui.ConfigChangeProposal{
		Summary: "Be kind", Branch: model.ConfigProposalBranchPrefix(id) + "be-kind", HeadSHA: proposedHead,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.RejectConfigChange(ctx, id, ui.ConfigChangeDecision{HeadSHA: proposedHead, Reason: "too chatty"}); err != nil {
		t.Fatalf("RejectConfigChange: %v", err)
	}
	if len(repo.discarded) != 1 || len(repo.merged) != 0 {
		t.Fatalf("discarded %+v merged %+v", repo.discarded, repo.merged)
	}
	comments, err := store.Comments(ctx, id)
	if err != nil || len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "too chatty") {
		t.Fatalf("the reason was not relayed: %+v %v", comments, err)
	}
}

// A proposal whose stored validation was green when proposed can fail
// validation when approving is tried later -- for example after an operator
// write moves grain/config's main onto the same line. Refusing with 422
// replaces the stored validation so subsequent reads (and reloads through
// taskDetailCache) stop claiming "Schema valid" and keep the proposal
// parked for Reject.
func TestApproveConfigChangeRecordsRefusedValidationOnThePendingChange(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	id, repo := proposingTask(t, c, store, ctx)
	srv := ui.NewServerWithClient(c)
	if _, err := c.ProposeConfigChange(ctx, id, ui.ConfigChangeProposal{
		Summary: "Raise poll interval", Branch: model.ConfigProposalBranchPrefix(id) + "poll", HeadSHA: proposedHead,
	}); err != nil {
		t.Fatal(err)
	}

	// Prime the detail cache with the green validation, as opening the task
	// pane before clicking Approve & Apply does.
	before := do(t, srv, http.MethodGet, "/api/tasks/"+id, "")
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"ok":true`) {
		t.Fatalf("initial task detail = %d %s", before.Code, before.Body.String())
	}

	// A transient git fault is not a validation verdict: the stored
	// validation stays ok.
	repo.mergeErr = errors.New("git status failed")
	fault := do(t, srv, http.MethodPost, "/api/tasks/"+id+"/config-change/approve", `{"headSha":"`+proposedHead+`"}`)
	if fault.Code != http.StatusInternalServerError {
		t.Fatalf("merge fault = %d, want 500", fault.Code)
	}
	if detail, err := c.GetTask(ctx, id); err != nil || detail.PendingConfigChange == nil || !detail.PendingConfigChange.Validation.OK {
		t.Fatalf("a merge fault overwrote stored validation: %+v %v", detail.PendingConfigChange, err)
	}

	conflictReason := "the change conflicts with the current main: rebase it onto origin/main and propose it again"
	liveness := []string{"max_parallel is 0, which lets no run start"}
	repo.mergeErr = &ui.ConfigPullRequestInvalid{
		Err: errors.New(conflictReason),
		Validation: staterepo.CandidateReport{
			Check:    staterepo.CheckReport{Warnings: []string{"system schedule edited"}},
			Liveness: liveness,
		},
	}
	refused := do(t, srv, http.MethodPost, "/api/tasks/"+id+"/config-change/approve", `{"headSha":"`+proposedHead+`"}`)
	if refused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("conflicting approve = %d %s, want 422", refused.Code, refused.Body.String())
	}

	after := do(t, srv, http.MethodGet, "/api/tasks/"+id, "")
	if after.Code != http.StatusOK {
		t.Fatalf("re-reading task detail = %d %s", after.Code, after.Body.String())
	}
	var detail ui.TaskDetail
	if err := json.Unmarshal(after.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.PendingConfigChange == nil {
		t.Fatal("pending config change was cleared on 422 refusal")
	}
	got := detail.PendingConfigChange.Validation
	if got.OK || !got.Stale || got.Error != conflictReason ||
		!slices.Equal(got.Warnings, []string{"system schedule edited"}) ||
		!slices.Equal(got.Liveness, liveness) {
		t.Fatalf("stored validation after 422 = %+v", got)
	}

	// Reject still clears the stale proposal and relays the rebase request.
	if err := c.RejectConfigChange(ctx, id, ui.ConfigChangeDecision{
		HeadSHA: proposedHead, Reason: "rebase onto origin/main",
	}); err != nil {
		t.Fatalf("RejectConfigChange after 422: %v", err)
	}
	if len(repo.discarded) != 1 {
		t.Fatalf("discarded = %+v, want 1", repo.discarded)
	}
}
