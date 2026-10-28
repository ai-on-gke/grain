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

// TestARedispatchIsToldWhatTheAttemptBeforeItDid drives two real
// orchestrator.RunDispatch calls at the same task, with a real gitproxy
// in front of a real bare upstream repo, and reads the prompt the second
// one was actually given out of the store. The first attempt pushes a
// commit and its run row is finished with an outcome; the second has to
// open on a checkout of that branch and a prompt that says both what
// happened and what is on it.
//
// Why it matters: a redispatch used to get the task, the conversation,
// its attachments and a checkout continuing the previous attempt's
// branch -- and no account whatever of the attempt that made those
// commits (docs/agent-ergonomics.md, finding 8). The cheapest thing that
// cost was re-doing a diagnosis grain had already paid for; the dearest
// was re-attempting exactly the thing that ran out of wall clock. Both
// halves of the fix need the two things only a real dispatch has
// together: the store's own task_run rows, and a checkout to read
// `git log` out of.
package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestARedispatchIsToldWhatTheAttemptBeforeItDid(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.newRepo("acme", "widgets")

	task := fileIssue(w, "iss-redispatch", human("alice"), model.RepoRef{Owner: "acme", Name: "widgets"})
	branch := model.BranchName(task.ID)
	cfg := orchestrator.Config{GitRemoteBase: w.proxyURL}

	dispatches, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("Cycle dispatched %+v, want exactly one", dispatches)
	}
	first := dispatches[0]

	full, err := w.store.GetTask(w.ctx, task.ID)
	if err != nil || full == nil {
		t.Fatalf("GetTask(%s): %v (nil=%v)", task.ID, err, full == nil)
	}

	// Attempt 1: a real run that clones, commits and pushes, and whose
	// outcome and tool census RunDispatch itself writes to task_run
	// (outcomeOf) on the way out.
	root := w.prepareSandbox(first)
	fw := antigravity.NewForTest(antigravity.Steps(preClonedPushScript(branch, task.ID)...))
	result, err := orchestrator.RunDispatch(w.ctx, w.store, fw, cfg, *full, first,
		mcp.NewSandboxTools(root), root, "", nil, baseTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("RunDispatch (attempt 1): %v", err)
	}
	if !pushedOK(result) {
		t.Fatalf("the first attempt's push did not go through cleanly: %+v", result.ToolCalls)
	}

	// Attempt 2, started the way dispatch.Cycle would start it -- the row
	// before the run, so that RunDispatch's own history read has to leave
	// it out of its own prompt.
	second := dispatch.Dispatch{TaskID: task.ID, RunID: first.RunID + "-2", Attempt: 2}
	if err := w.store.StartRun(w.ctx, model.Run{
		ID: second.RunID, TaskID: second.TaskID, Attempt: second.Attempt,
		StartedAt: baseTime.Add(time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatalf("starting the second attempt: %v", err)
	}
	secondRoot := w.prepareSandbox(second)
	quiet := antigravity.NewForTest(antigravity.Steps(
		toolCall("run_command", map[string]any{"command": "true"}),
		finalText("read the branch"),
	))
	if _, err := orchestrator.RunDispatch(w.ctx, w.store, quiet, cfg, *full, second,
		mcp.NewSandboxTools(secondRoot), secondRoot, "", nil, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunDispatch (attempt 2): %v", err)
	}

	// Read back what that run was really told, rather than what this test
	// believes it composed: SetRunPrompt is written from the same string
	// handed to framework.Run.
	prompt, found, err := w.store.RunPrompt(w.ctx, task.ID, 2)
	if err != nil || !found {
		t.Fatalf("RunPrompt(%s, 2): %v (found=%v)", task.ID, err, found)
	}

	// How attempt 1 ended, out of task_run.
	for _, want := range []string{"you are attempt 2", "attempt 1", "succeeded", "tool call"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("attempt 2's prompt does not mention %q:\n%s", want, prompt)
		}
	}
	// And what it left on the branch, out of the checkout: the commit the
	// scripted agent above actually pushed, named by subject.
	if want := "agent commit for " + task.ID; !strings.Contains(prompt, want) {
		t.Errorf("attempt 2's prompt does not name the commit attempt 1 pushed (%q):\n%s", want, prompt)
	}
	if !strings.Contains(prompt, branch) {
		t.Errorf("attempt 2's prompt does not name the branch those commits are on:\n%s", prompt)
	}
	// Not the base's own history: the range is <base>..HEAD, so the
	// repo's seed commit is not something the earlier attempt did.
	if strings.Contains(prompt, "initial commit") {
		t.Errorf("attempt 2's prompt credits the earlier attempt with the base's own commits:\n%s", prompt)
	}
}
