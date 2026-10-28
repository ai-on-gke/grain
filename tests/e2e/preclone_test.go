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

// TestDispatchPreClonesTheRepoSoTheAgentNeverHasTo drives a real
// orchestrator.RunDispatch, with a real gitproxy in front of a real bare
// upstream repo and pkg/mcp's real exec-backed sandbox tools, against a
// scripted agent that does no cloning of its own at all: it cds straight
// into ./work and pushes. Nothing below the MCP layer is mocked, so the
// only way this passes is if RunDispatch itself cloned the task's target
// through the proxy and left the task's branch checked out before the
// agent's first turn.
//
// Why it matters: a sandbox starts empty (orchestrator's own package doc
// comment), and until prepareCheckout the prompt never said so, never
// said to clone, and never named the proxy URL that is the only address
// the sandbox can reach the repo through. A live run's first attempt did
// exactly what that leaves an agent room to do -- ran git in the empty
// directory, got "not a git repository", and gave up -- and the task was
// only carried by the redispatch after it.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// preClonedPushScript is pushScript with the clone removed -- the whole
// point: an agent that assumes the checkout is already there, on the
// branch it was told to push, with a remote it never had to be told.
func preClonedPushScript(branch, taskID string) []antigravity.Step {
	cmd := "cd " + orchestrator.CheckoutDir + " && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed " + branch),
	}
}

func TestDispatchPreClonesTheRepoSoTheAgentNeverHasTo(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.newRepo("acme", "widgets")

	task := fileIssue(w, "iss-preclone", human("alice"), model.RepoRef{Owner: "acme", Name: "widgets"})
	branch := model.BranchName(task.ID)

	dispatches, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("Cycle dispatched %+v, want exactly one", dispatches)
	}
	d := dispatches[0]

	full, err := w.store.GetTask(w.ctx, task.ID)
	if err != nil || full == nil {
		t.Fatalf("GetTask(%s): %v (nil=%v)", task.ID, err, full == nil)
	}

	root := w.prepareSandbox(d)
	if entries, err := os.ReadDir(filepath.Join(root, orchestrator.CheckoutDir)); err == nil {
		t.Fatalf("the sandbox already holds a checkout before the run: %v", entries)
	}

	// GitRemoteBase is the one line a deployment adds (pkg/cli/daemon.go
	// passes its own proxy URL): with it, RunDispatch prepares the
	// checkout; without it, the sandbox stays as empty as it ever was,
	// which is what every other test in this package still gets.
	cfg := orchestrator.Config{GitRemoteBase: w.proxyURL}
	fw := antigravity.NewForTest(antigravity.Steps(preClonedPushScript(branch, task.ID)...))
	result, err := orchestrator.RunDispatch(w.ctx, w.store, fw, cfg, *full, d,
		mcp.NewSandboxTools(root), root, "", nil, baseTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if !pushedOK(result) {
		t.Fatalf("the agent's push did not go through cleanly: %+v", result.ToolCalls)
	}

	if !w.branchExists("acme", "widgets", branch) {
		t.Fatalf("%s never landed upstream", branch)
	}
	if got := w.log1("acme", "widgets", branch, "%s"); got != "agent commit for "+task.ID {
		t.Fatalf("pushed branch tip = %q, want the agent's commit", got)
	}
	// The commit sits on top of the repo's own history rather than on a
	// fresh root: the clone came from the upstream through the proxy, not
	// from an empty `git init` the agent could have made itself.
	if !w.branchContains("acme", "widgets", branch, "main") {
		t.Fatalf("%s does not build on main -- the checkout was not a clone of the target repo", branch)
	}

	// The prompt has to agree with what was prepared, or the agent is
	// being told to clone something that is already there.
	if prompt := orchestrator.BuildPrompt(*full, orchestrator.CheckoutDir, "", false, orchestrator.DefaultMaxRunRuntime, orchestrator.History{}, nil); !strings.Contains(prompt, "./"+orchestrator.CheckoutDir) {
		t.Fatalf("prompt never mentions the prepared checkout: %q", prompt)
	}
}

// A task closed before its dispatch reaches RunDispatch never has its
// sandbox touched at all -- bwsalmon/agents#346's rule, which the clone
// has to observe too now that it is the run's own first touch of that
// sandbox (see close_while_live_test.go for the agent half of it).
func TestDispatchDoesNotCloneForATaskClosedBeforeItRan(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.newRepo("acme", "widgets")

	task := fileIssue(w, "iss-closed", human("alice"), model.RepoRef{Owner: "acme", Name: "widgets"})
	dispatches, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("Cycle dispatched %+v, want exactly one", dispatches)
	}
	full, err := w.store.GetTask(w.ctx, task.ID)
	if err != nil || full == nil {
		t.Fatalf("GetTask(%s): %v (nil=%v)", task.ID, err, full == nil)
	}

	closedAt := baseTime.Add(30 * time.Second)
	if err := w.store.Observe(w.ctx, model.Observation{TaskID: task.ID, ClosedAt: &closedAt}); err != nil {
		t.Fatal(err)
	}

	root := w.prepareSandbox(dispatches[0])
	cfg := orchestrator.Config{GitRemoteBase: w.proxyURL}
	fw := antigravity.NewForTest(antigravity.Steps(preClonedPushScript(model.BranchName(task.ID), task.ID)...))
	if _, err := orchestrator.RunDispatch(w.ctx, w.store, fw, cfg, *full, dispatches[0],
		mcp.NewSandboxTools(root), root, "", nil, baseTime.Add(time.Minute)); err == nil {
		t.Fatal("RunDispatch reported success for a task closed before its run started")
	}
	if _, err := os.Stat(filepath.Join(root, orchestrator.CheckoutDir)); !os.IsNotExist(err) {
		t.Fatalf("a closed task's run cloned into its sandbox anyway (err=%v)", err)
	}
}
