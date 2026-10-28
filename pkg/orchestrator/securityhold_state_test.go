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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// finishStateChange is finishTouching for any repository: an auto-merging
// task whose run pushed path to repo, taken through ProcessResult.
func finishStateChange(t *testing.T, store *model.Store, ctx context.Context, repo model.RepoRef, path string) model.Task {
	t.Helper()
	return finishStateChangeWithBaseFiles(t, store, ctx, repo, nil, map[string]string{path: "changed\n"})
}

func finishStateChangeWithBaseFiles(t *testing.T, store *model.Store, ctx context.Context, repo model.RepoRef,
	baseFiles, prFiles map[string]string) model.Task {
	t.Helper()
	sim, client := newSim(t, repo.Owner, repo.Name, "main")
	for p, content := range baseFiles {
		pushFile(t, sim.BareRepo, "main", p, content)
	}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	for p, content := range prFiles {
		pushFile(t, sim.BareRepo, branch, p, content)
	}
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if reviewID := linkOf(got, model.LinkSecurityReviewTask); reviewID != "" {
		completeReview(t, ctx, store, reviewID)
		if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got = reloaded(t, ctx, store, task.ID)
	}
	return got
}

// A grant merged into the state repository is applied to the running
// daemon on its next tick, so a change to one waits for a person: a clean
// review does not give auto-merge back.
func TestStateRepoGrantChangeIsHeldForAPerson(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	got := finishStateChange(t, store, ctx, model.ConfigRepo(), "tables/template_grant.json")
	if linkOf(got, model.LinkSecurityReviewTask) == "" {
		t.Fatalf("no security review filed for a grant change: %+v", got.Links)
	}
	if got.AutoMerge {
		t.Fatal("a clean review released a change to tables/template_grant.json in the state repository")
	}
}

// Every config repository pull request is held for a human Submit by default,
// including formerly benign paths like tables/board.json and memory/**.
func TestStateRepoBoardChangeIsHeldByDefault(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"tables/board.json", "memory/deployment.md", "release-notes/2026-W43.md", "README.md"} {
		store, ctx := openStore(t)
		got := finishStateChange(t, store, ctx, model.ConfigRepo(), path)
		if got.AutoMerge || linkOf(got, model.LinkHumanOnlyHold) == "" || linkOf(got, model.LinkSecurityReviewTask) == "" {
			t.Fatalf("config repo change to %s was not held for human Submit by default: automerge=%v links=%+v",
				path, got.AutoMerge, got.Links)
		}
	}
}

// Paths matched by auto_merge_paths in settings/config-policy.yml on the base
// branch (main) are exempt from the default config-repo hold.
func TestStateRepoAutoMergePathsOptInFromBaseBranch(t *testing.T) {
	t.Parallel()
	basePolicy := map[string]string{
		trustboundary.ConfigPolicyPath: "auto_merge_paths:\n  - memory/**\n  - release-notes/**\n  - tables/board.json\n",
	}

	for _, exemptPath := range []string{"memory/deployment.md", "release-notes/v2.5.md", "tables/board.json"} {
		store, ctx := openStore(t)
		got := finishStateChangeWithBaseFiles(t, store, ctx, model.ConfigRepo(), basePolicy, map[string]string{
			exemptPath: "updated\n",
		})
		if !got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) != "" || linkOf(got, model.LinkHumanOnlyHold) != "" {
			t.Fatalf("exempt path %s was held despite base-branch %s: automerge=%v links=%+v",
				exemptPath, trustboundary.ConfigPolicyPath, got.AutoMerge, got.Links)
		}
	}

	// Non-matching path is still held for human Submit.
	store, ctx := openStore(t)
	got := finishStateChangeWithBaseFiles(t, store, ctx, model.ConfigRepo(), basePolicy, map[string]string{
		"tables/custom_field.json": "updated\n",
	})
	if got.AutoMerge || linkOf(got, model.LinkHumanOnlyHold) == "" {
		t.Fatalf("non-exempt path tables/custom_field.json was not held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// settings/config-policy.yml is always evaluated from the base branch (main),
// never from the unmerged PR head, and is itself in NeverAutoMergePaths.
func TestStateRepoConfigPolicyOnPRHeadCannotExemptItself(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	// Base branch has NO settings/config-policy.yml; PR branch adds one
	// opting in tables/board.json (and settings/config-policy.yml) alongside
	// a change to tables/board.json.
	got := finishStateChangeWithBaseFiles(t, store, ctx, model.ConfigRepo(), nil, map[string]string{
		trustboundary.ConfigPolicyPath: "auto_merge_paths:\n  - settings/config-policy.yml\n  - tables/board.json\n",
		"tables/board.json":            "changed\n",
	})
	if got.AutoMerge || linkOf(got, model.LinkHumanOnlyHold) == "" {
		t.Fatalf("PR modifying %s on its own head escaped human hold: automerge=%v links=%+v",
			trustboundary.ConfigPolicyPath, got.AutoMerge, got.Links)
	}
}

// NeverAutoMergePaths is a hardcoded ceiling evaluated before auto_merge_paths:
// even if base branch settings/config-policy.yml lists a forbidden path (or "**"),
// changes to NeverAutoMergePaths still require a human Submit.
func TestStateRepoNeverAutoMergePathsCeiling(t *testing.T) {
	t.Parallel()
	basePolicy := map[string]string{
		trustboundary.ConfigPolicyPath: "auto_merge_paths:\n  - \"**\"\n  - settings/config-policy.yml\n  - tables/grain_config.json\n  - scripts/**\n",
	}
	for _, protectedPath := range []string{
		trustboundary.ConfigPolicyPath,
		"settings/capabilities.yml",
		"settings/new-task-form.yml",
		"settings/templates.yml",
		"settings/schedules.yml",
		"settings/suites.yml",
		"tables/grain_config.json",
		"tables/repo_config.json",
		"tables/api_scope_set.json",
		"tables/git_credential.json",
		"tables/repo_git_credential.json",
		"tables/agent_backend.json",
		"tables/console_session.json",
		"tables/repo_workflow.json",
		"scripts/setup.sh",
		"workflows/ci.yml",
		".github/workflows/ci.yml",
		".grain/workflows/ci.yml",
		"schema-version",
	} {
		store, ctx := openStore(t)
		got := finishStateChangeWithBaseFiles(t, store, ctx, model.ConfigRepo(), basePolicy, map[string]string{
			protectedPath: "modified\n",
		})
		if got.AutoMerge || linkOf(got, model.LinkHumanOnlyHold) == "" {
			t.Fatalf("NeverAutoMergePaths path %s auto-merged despite ceiling: automerge=%v links=%+v",
				protectedPath, got.AutoMerge, got.Links)
		}
	}
}

// The state repository's layout means nothing in any other repository.
func TestStatePathsInAnOrdinaryRepoAreNotHeld(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	store.SettingsRepo = func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "settings"} }
	got := finishStateChange(t, store, ctx, model.RepoRef{Owner: "acme", Name: "widgets"}, "tables/template_grant.json")
	if !got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) != "" {
		t.Fatalf("an ordinary repository's tables/ was held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// A deployment whose state lives in a repository of its own judges that
// one the same way.
func TestExternalStateRepoTemplateChangeIsHeldForAPerson(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings := model.RepoRef{Owner: "acme", Name: "settings"}
	store.SettingsRepo = func() model.RepoRef { return settings }
	got := finishStateChange(t, store, ctx, settings, "settings/templates.yml")
	if linkOf(got, model.LinkSecurityReviewTask) == "" || got.AutoMerge {
		t.Fatalf("a template change to the external state repository merges itself: automerge=%v links=%+v",
			got.AutoMerge, got.Links)
	}
}
