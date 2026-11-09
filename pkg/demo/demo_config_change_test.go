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

package demo

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain demo -config-change` seeds two tasks parked on real proposal
// branches of a real grain/config, and the UI's own approve and reject
// work against it: approving merges exactly the proposed head into main
// and applies it, rejecting deletes the branch and hands the reason back
// (docs/test-runs/2026-11-08-task-intake-inbox-lifecycle-task-3611.md,
// step 36, where both answered 501).
func TestConfigChangeDemoApprovesAndRejectsAgainstARealConfigRepo(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	cfg := ui.Config{Actor: ui.DefaultActor("demo-operator")}
	if err := configureConfigChangeDemo(ctx, dir, db, store, &cfg); err != nil {
		t.Fatalf("configureConfigChangeDemo: %v", err)
	}
	if cfg.StateRepoRef == nil || !model.IsConfigRepo(cfg.StateRepoRef()) {
		t.Fatal("the demo does not name grain/config as its config repository")
	}
	client := ui.NewClient(cfg, store)
	type proposal struct {
		id      string
		pending model.PendingConfigChange
	}
	parked := map[string]proposal{}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs == nil || obs.PendingConfigChange == nil {
			continue
		}
		if obs.PendingQuestionCommentID == nil {
			t.Errorf("task %s holds a config change but is not parked on it", task.ID)
		}
		parked[task.Title] = proposal{task.ID, *obs.PendingConfigChange}
	}
	pollTask, ok := parked[ConfigChangeDemoPollTitle]
	if !ok {
		t.Fatalf("no task parked on the poll interval proposal: %v", parked)
	}
	promptTask, ok := parked[ConfigChangeDemoPromptTitle]
	if !ok {
		t.Fatalf("no task parked on the prompt extension proposal: %v", parked)
	}
	poll, prompt := pollTask.pending, promptTask.pending
	pollID, promptID := pollTask.id, promptTask.id
	for _, p := range []model.PendingConfigChange{poll, prompt} {
		if !p.Validation.OK || len(p.Files) != 1 || p.Files[0] != "tables/grain_config.json" || p.Diff == "" || len(p.HeadSHA) != 40 {
			t.Errorf("proposal = %+v, want a validated one-file diff at a real commit", p)
		}
	}
	if !strings.Contains(poll.Diff, `+    "poll_interval_ms": 30000`) {
		t.Errorf("poll interval diff = %q", poll.Diff)
	}

	// The base branch picker on these tasks lists grain/config's own
	// branches, as the daemon's does, rather than answering 404.
	branches, err := client.ListGitHubBranches(ctx, model.ConfigRepo())
	if err != nil {
		t.Fatalf("listing grain/config's branches: %v", err)
	}
	for _, want := range []string{"main", poll.Branch, prompt.Branch} {
		if !slices.Contains(branches, want) {
			t.Errorf("grain/config branches = %v, want %s among them", branches, want)
		}
	}

	repoDir := daemon.StateRepoDir(dir)
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	if err := client.ApproveConfigChange(ctx, pollID, ui.ConfigChangeDecision{HeadSHA: poll.HeadSHA}); err != nil {
		t.Fatalf("ApproveConfigChange: %v", err)
	}
	if parents := strings.Fields(git("rev-list", "--parents", "-n", "1", "main")); len(parents) != 3 || parents[2] != poll.HeadSHA {
		t.Errorf("main's tip parents = %v, want a merge of %s", parents, poll.HeadSHA)
	}
	if refs := git("for-each-ref", "--format=%(refname)", "refs/heads/"+poll.Branch); refs != "" {
		t.Errorf("approved branch survived: %s", refs)
	}
	if live, err := store.GetConfig(ctx); err != nil || live.PollInterval != 30*time.Second {
		t.Errorf("approved change not applied: poll interval %v, %v", live.PollInterval, err)
	}

	if err := client.RejectConfigChange(ctx, promptID, ui.ConfigChangeDecision{HeadSHA: prompt.HeadSHA, Reason: "Not every repo has unit tests"}); err != nil {
		t.Fatalf("RejectConfigChange: %v", err)
	}
	if refs := git("for-each-ref", "--format=%(refname)", "refs/heads/"+prompt.Branch); refs != "" {
		t.Errorf("rejected branch survived: %s", refs)
	}
	if live, err := store.GetConfig(ctx); err != nil || live.PromptExtension != "" {
		t.Errorf("a rejected change was applied: %q, %v", live.PromptExtension, err)
	}
	comments, err := store.Comments(ctx, promptID)
	if err != nil {
		t.Fatal(err)
	}
	if last := comments[len(comments)-1].Body; !strings.Contains(last, "Not every repo has unit tests") {
		t.Errorf("last comment = %q, want the rejection's reason", last)
	}
	for _, id := range []string{pollID, promptID} {
		if obs, err := store.GetObservation(ctx, id); err != nil || obs.PendingConfigChange != nil {
			t.Errorf("task %s still offers its change after the decision: %v", id, err)
		}
	}
}
