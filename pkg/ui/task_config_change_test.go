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

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const proposedHead = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// changeStateRepo is fakeStateRepo carrying config changes the way
// pkg/daemon's StateManager does, recording what it was asked.
type changeStateRepo struct {
	*fakeStateRepo
	merged, discarded []model.PendingConfigChange
}

func (c *changeStateRepo) ProposeConfigChange(_ context.Context, p ui.ConfigChangeProposal) (*model.PendingConfigChange, error) {
	return &model.PendingConfigChange{
		Branch: p.Branch, HeadSHA: p.HeadSHA, Summary: p.Summary,
		Files: []string{"tables/grain_config.json"}, Diff: "diff --git a/x b/x\n",
		Validation: model.ConfigChangeValidation{OK: true},
	}, nil
}

func (c *changeStateRepo) MergeConfigChange(_ context.Context, p model.PendingConfigChange) (string, error) {
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
