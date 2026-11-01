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

package daemon

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// proposalFixture is a synced controller-local grain/config with a
// proposal branch on it carrying sqlText's rehearsed edit, as a session
// pushing through the git proxy would leave it.
func proposalFixture(t *testing.T, sqlText, branch string) (*StateManager, *model.Store, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := model.DefaultConfig()
	cfg.MaxWorkers = 2
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadStateRepo(ctx, dataDir, repo, db, false); err != nil {
		t.Fatal(err)
	}
	manager := NewStateManager(dataDir, db, repo, OpenSecrets(dataDir), nil, nil)
	if _, err := manager.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	changes := rehearsedChange(t, db, sqlText)
	base, err := localResolve(repo.Dir(), repo.Branch())
	if err != nil {
		t.Fatal(err)
	}
	candidate := t.TempDir()
	if err := extractCommit(ctx, repo.Dir(), base, candidate); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.ApplyChanges(candidate, changes); err != nil {
		t.Fatal(err)
	}
	head, err := commitChanges(ctx, repo.Dir(), base, candidate, changes, "proposed", "director")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localGit(repo.Dir(), "update-ref", "refs/heads/"+branch, head); err != nil {
		t.Fatal(err)
	}
	return manager, store, head
}

func TestProposedConfigChangeIsDiffedValidatedAndMergedAtItsHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	branch := model.ConfigProposalBranchPrefix("42") + "be-kind"
	manager, store, head := proposalFixture(t, `UPDATE grain_config SET prompt_extension = 'be kind'`, branch)

	pending, err := manager.ProposeConfigChange(ctx, ui.ConfigChangeProposal{Summary: "Be kind", Branch: branch, HeadSHA: head})
	if err != nil {
		t.Fatalf("ProposeConfigChange: %v", err)
	}
	if pending.HeadSHA != head || !slices.Contains(pending.Files, "tables/grain_config.json") || pending.Diff == "" {
		t.Fatalf("pending = %+v", pending)
	}
	if !pending.Validation.OK {
		t.Fatalf("validation = %+v, want ok", pending.Validation)
	}
	if _, err := manager.ProposeConfigChange(ctx, ui.ConfigChangeProposal{Summary: "Be kind", Branch: branch, HeadSHA: "0123456789012345678901234567890123456789"}); err == nil {
		t.Fatal("a proposal naming a head the branch is not at was recorded")
	}

	moved := *pending
	moved.HeadSHA = "0123456789012345678901234567890123456789"
	if _, err := manager.MergeConfigChange(ctx, moved); !errors.Is(err, ui.ErrConfigChangeMoved) {
		t.Fatalf("merging a commit the branch is not at = %v, want ErrConfigChangeMoved", err)
	}
	if _, err := manager.MergeConfigChange(ctx, *pending); err != nil {
		t.Fatalf("MergeConfigChange: %v", err)
	}
	if _, err := manager.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if live, err := store.GetConfig(ctx); err != nil || live.PromptExtension != "be kind" {
		t.Fatalf("approved change not applied: %+v %v", live, err)
	}
	if exists, err := localRefExists(manager.repo.Dir(), branch); err != nil || exists {
		t.Fatalf("proposal branch survived its merge: %v %v", exists, err)
	}
}

func TestRejectedConfigChangeDeletesItsBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	branch := model.ConfigProposalBranchPrefix("42") + "be-kind"
	manager, store, head := proposalFixture(t, `UPDATE grain_config SET prompt_extension = 'be kind'`, branch)
	pending, err := manager.ProposeConfigChange(ctx, ui.ConfigChangeProposal{Summary: "Be kind", Branch: branch, HeadSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DiscardConfigChange(ctx, *pending); err != nil {
		t.Fatalf("DiscardConfigChange: %v", err)
	}
	if exists, err := localRefExists(manager.repo.Dir(), branch); err != nil || exists {
		t.Fatalf("rejected branch survived: %v %v", exists, err)
	}
	if live, err := store.GetConfig(ctx); err != nil || live.PromptExtension != "" {
		t.Fatalf("a rejected change was applied: %+v %v", live, err)
	}
}
