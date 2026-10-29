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

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// docs/test-plans/2026-10-28.md §8 step 3, on the security-hold
// walkthrough's harness (real orchestrator, real git server, githubsim, a
// scripted agent): a change on a path only a human may release is held,
// its security review pushes a fix to the held change's own branch, and
// the hold stays until a human presses Submit -- which then lands the
// change with the reviewer's fix in it. The report is
// docs/test-runs/2026-10-28-merge-queue-security-review-submit.md.
func TestHumanOnlyHoldKeepsAnInPlaceReviewFixUntilSubmit(t *testing.T) {
	t.Parallel()
	w := newHoldWorld(t, false)
	// The daemon's UI reads the pull request's head at the press
	// (pkg/daemon/ui_server.go), so Submit records the head the human saw.
	w.ui.Config.GitHubPullRequests = github.NewClient(w.sim, nil)
	task := w.create("refresh the lint baseline", true, nil)
	branch := model.BranchName(task.ID)
	w.workThenReview(task.ID, "echo '{\"x\":1}' > .grain/baselines/lint.json", func(review model.Task) []antigravity.Step {
		cmd := "git clone " + w.remote + " work && cd work && git checkout " + review.BranchName() +
			" && echo '{\"x\":2}' > .grain/baselines/lint.json && git commit -qam 'review fix' && git push origin " + review.BranchName()
		return []antigravity.Step{toolCall("run_command", map[string]any{"command": cmd}), finalText("fixed the baseline")}
	})
	reviewID := w.heldOn(task, ".grain/baselines/lint.json", false)
	if got := w.task(reviewID).BranchName(); got != branch {
		t.Fatalf("the security review works on %q, want the held change's own branch %q", got, branch)
	}
	held := w.head(branch)
	w.notMergedWhileHeld(task, 6)
	if st := w.state(reviewID); st != model.StateCompleted && st != model.StateClosed {
		t.Fatalf("the review did not finish (state %s), so this proved nothing", st)
	}
	if w.head(branch) == held {
		t.Fatal("the review's fix never reached the held change's branch")
	}
	if w.branchExists(model.BranchName(reviewID)) || linkTarget(w.task(reviewID), model.LinkFixes) != "" {
		t.Error("the review pushed a branch or opened a pull request of its own")
	}
	if got := w.task(task.ID); got.AutoMerge {
		t.Fatalf("a finished review gave a human-only change auto-merge back: links=%+v", got.Links)
	}

	if err := w.ui.Submit(w.ctx, task.ID); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !w.cycles(6, func() bool { w.markAllClean(); return w.merged(branch) }) {
		t.Fatalf("Submit did not land the held change: automerge=%v links=%+v comments=%s",
			w.task(task.ID).AutoMerge, w.task(task.ID).Links, w.comments(task.ID))
	}
	landed, err := exec.Command("git", "--git-dir", w.bare, "show", "main:.grain/baselines/lint.json").Output()
	if err != nil || !strings.Contains(string(landed), `"x":2`) {
		t.Errorf("the change merged without the review's fix (%v):\n%s", err, landed)
	}
}
