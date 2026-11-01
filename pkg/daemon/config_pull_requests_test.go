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
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// rehearsedChange is the edit a refused write of sql would hand back.
func rehearsedChange(t *testing.T, db *sql.DB, sqlText string) []staterepo.FileChange {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	changes, writeErr, err := staterepo.Rehearse(ctx, tx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlText)
		return err
	})
	if writeErr != nil || err != nil || len(changes) == 0 {
		t.Fatalf("rehearsing %q: %v %v %+v", sqlText, writeErr, err, changes)
	}
	return changes
}

func TestOpenConfigPullRequestCommitsAValidatedHeldChange(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := model.DefaultConfig()
	cfg.MaxWorkers = 2
	// A deployment's own template, as NewDeploymentConfig gives every
	// new one: the pull request's task must still name the branch its
	// commit is on rather than the template's expansion.
	cfg.TaskBranchTemplate = model.DefaultTaskBranchTemplate
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
	settingsFile := filepath.Join(repo.Dir(), "tables", "grain_config.json")
	before, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatal(err)
	}

	changes := rehearsedChange(t, db, `UPDATE grain_config SET prompt_extension = 'be kind'`)
	pr, err := manager.OpenConfigPullRequest(ctx, ui.ConfigPullRequestRequest{Changes: changes, User: "alice"})
	if err != nil {
		t.Fatalf("OpenConfigPullRequest: %v", err)
	}
	if pr.Branch != model.BranchName(pr.TaskID) || pr.Number == 0 {
		t.Fatalf("pull request = %+v", pr)
	}
	shown, err := localGit(repo.Dir(), "show", pr.Branch+":tables/grain_config.json")
	if err != nil || !strings.Contains(shown, `"prompt_extension": "be kind"`) {
		t.Fatalf("the branch does not carry the change: %v\n%s", err, shown)
	}
	// Neither the working tree nor the deployment's own row moved.
	if after, _ := os.ReadFile(settingsFile); string(after) != string(before) {
		t.Fatal("opening the pull request wrote the working tree")
	}
	if live, err := store.GetConfig(ctx); err != nil || live.PromptExtension != "" {
		t.Fatalf("opening the pull request wrote the live row: %+v %v", live, err)
	}
	task, err := store.GetTask(ctx, pr.TaskID)
	if err != nil || task == nil || task.Target == nil || !model.IsConfigRepo(*task.Target) || task.AutoMerge {
		t.Fatalf("task = %+v, %v", task, err)
	}
	obs, err := store.GetObservation(ctx, pr.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state := model.StateOf(*task, obs, false, 0); state != model.StateAwaitingSubmit {
		t.Fatalf("the pull request's task is %s, want held for a person's submit", state)
	}
	// And the sync reads it as open: a task naming another branch had
	// its pull request read as closed unmerged on the next tick, before
	// anybody could submit it (2026-10-28 test plan §5 walkthrough).
	if task.BranchName() != pr.Branch {
		t.Fatalf("the task names branch %q, but the change was committed to %q", task.BranchName(), pr.Branch)
	}
	prClient := &StateRepoPullRequestClient{Manager: manager, Store: store}
	target := model.ConfigRepo()
	detail, err := prClient.GetPullRequest(target.Owner, target.Name, pr.Number)
	if err != nil {
		t.Fatalf("reading the internal pull request: %v", err)
	}
	if detail.State != "open" || detail.Merged || detail.HeadRef != pr.Branch {
		t.Fatalf("the internal pull request reads %+v, want open with head %s", detail, pr.Branch)
	}

	// A change that would lock dispatch is refused before anything is
	// committed.
	locking := rehearsedChange(t, db, `UPDATE grain_config SET max_workers = 0`)
	_, err = manager.OpenConfigPullRequest(ctx, ui.ConfigPullRequestRequest{Changes: locking, User: "alice"})
	var invalid *ui.ConfigPullRequestInvalid
	if !errors.As(err, &invalid) || !errors.Is(err, staterepo.ErrDispatchLocked) {
		t.Fatalf("a dispatch-locking change: err = %v", err)
	}
	if invalid.Stale {
		t.Fatal("a dispatch-locking change was called stale: making it again would not help")
	}

	// A change refused before grain/config moved under it no longer
	// applies: it is stale, which a person answers by making the change
	// again rather than by fixing what it sets.
	moved := strings.Replace(string(before), `"prompt_extension": ""`, `"prompt_extension": "moved"`, 1)
	if moved == string(before) {
		t.Fatalf("grain_config.json has no empty prompt_extension to move:\n%s", before)
	}
	if err := os.WriteFile(settingsFile, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo.Dir(), "-c", "user.name=t", "-c", "user.email=t@example.com",
		"commit", "-qam", "somebody else's change").CombinedOutput(); err != nil {
		t.Fatalf("moving main: %v\n%s", err, out)
	}
	_, err = manager.OpenConfigPullRequest(ctx, ui.ConfigPullRequestRequest{Changes: changes, User: "alice"})
	if !errors.As(err, &invalid) || !invalid.Stale {
		t.Fatalf("a change that no longer applies to main: err = %v, want a stale ConfigPullRequestInvalid", err)
	}
}
