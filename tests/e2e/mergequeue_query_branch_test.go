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

package e2e

// query_merge_queue {branch: "main"} against a real queue: a pull request
// on its repository's default branch -- a task with no Base, the
// ordinary task -- is queued by a real orchestrator cycle, and the real
// `grain mcpserver` subprocess reads it back through a real pkg/ui
// server, resolving the default branch over the -github-host credential
// ladder. Before 8c0895b34 the report printed an empty "Branch:" for it
// and a "main" filter dropped it (F10 in
// docs/test-runs/2026-10-05-merge-rules.md); the unit test beside the
// reader (pkg/daemon/mcpserver_mergequeue_test.go) stubs the daemon's
// API, which this does not. Section 8 of docs/test-plans/2026-10-06.md.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestQueryMergeQueueListsADefaultBranchPullRequestUnderItsBranch(t *testing.T) {
	t.Parallel()
	bin := buildGrainBinary(t)
	w := newWorld(t)
	const owner, repoName = "acme", "widgets"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	synced := &syncedSim{sim: sim}
	deps := orchestrator.Deps{Store: w.store, Client: github.NewClient(synced, nil), Sandboxes: worldSandboxes{w}, MaxWorkers: 1}

	const taskID = "mq1"
	task := model.Task{
		ID: taskID, Intent: model.IntentImplement, Title: "queued on the default branch",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("alice")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &baseTime,
	}
	if err := w.store.PutTask(w.ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(taskID)
	clock := baseTime

	deps.Framework = scriptedFramework(cleanPushScript(w.remote(owner, repoName), branch, taskID))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (push): %v", err)
	}
	if synced.pullRequestCount() != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}

	// A check still running on the head keeps it in the queue rather
	// than merged, which is the only state a queue report has to show.
	head := strings.TrimSpace(runOutput(t, w.upstreamDir, "git", "-C", bare, "rev-parse", branch))
	synced.mu.Lock()
	sim.CheckRuns[head] = []github.CheckRun{{Name: "build", Status: "in_progress"}}
	synced.mu.Unlock()
	for i := 0; i < 2; i++ {
		clock = clock.Add(time.Minute)
		if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle (queue, tick %d): %v", i, err)
		}
	}
	obs, err := w.store.GetObservation(w.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueState == "" {
		t.Fatalf("observation = %+v, want %s in the merge queue", obs, taskID)
	}
	stored, err := w.store.GetTask(w.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Base != "" {
		t.Fatalf("task Base = %q, want empty: the case under test is a default-branch task", stored.Base)
	}

	daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
	}), w.store))
	host := githubHostServer(t, synced, w.upstreamDir)
	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "ci"}`)
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "ci.token"), "ghp-not-a-real-token-for-a-sim\n")

	p := startMCPServer(t, bin,
		"-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", taskID,
		"-data-dir", dataDir, "-github-host", host, "-github-insecure-http")
	query := func(t *testing.T, args map[string]any) string {
		t.Helper()
		// The full view: what is asserted below is its per-change
		// "Repo: | Branch: | State:" line, which the compact default
		// writes on one line of its own.
		args["view"] = "full"
		res, err := p.CallTool(context.Background(), "query_merge_queue", args)
		if err != nil {
			t.Fatalf("tools/call query_merge_queue %v: %v\nstderr:\n%s", args, err, p.stderr.String())
		}
		if res.IsError {
			t.Fatalf("query_merge_queue %v answered with an error: %q\nstderr:\n%s", args, res.Text(), p.stderr.String())
		}
		return res.Text()
	}

	listed := "(Task " + taskID + ")"
	onMain := "Repo: acme/widgets | Branch: main | State: `" + obs.MergeQueueState + "`"
	t.Run("branch main lists it, on main", func(t *testing.T) {
		text := query(t, map[string]any{"branch": "main"})
		if !strings.Contains(text, listed) || !strings.Contains(text, onMain) {
			t.Fatalf("query_merge_queue {branch: main} = \n%s\nwant %s listed with %q", text, taskID, onMain)
		}
	})
	t.Run("no filter names its branch too", func(t *testing.T) {
		text := query(t, map[string]any{})
		if !strings.Contains(text, onMain) {
			t.Fatalf("query_merge_queue {} = \n%s\nwant %q", text, onMain)
		}
	})
	t.Run("another branch leaves it out", func(t *testing.T) {
		text := query(t, map[string]any{"branch": "release"})
		if strings.Contains(text, listed) {
			t.Fatalf("query_merge_queue {branch: release} = \n%s\nwant %s left out", text, taskID)
		}
	})
}
