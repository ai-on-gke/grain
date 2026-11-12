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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// Grain/task-4808: a head that moves without changing the change -- an
// empty commit, a clean merge of the base -- keeps its gating reviews'
// results instead of filing new ones (samechange.go).

// pushEmptyCommit pushes a "rerun CI" commit that changes nothing.
func pushEmptyCommit(t *testing.T, bare, branch string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	run(t, wd, "git", "commit", "-q", "--allow-empty", "-m", "rerun CI")
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// mergeBaseResolving merges base into branch and pushes the merge. A conflict
// is resolved by writing resolve's contents and committing them.
func mergeBaseResolving(t *testing.T, bare, branch, base string, resolve map[string]string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	merge := exec.Command("git", "merge", "--no-ff", "-q", "-m", "Merge "+base, "origin/"+base)
	merge.Dir = wd
	out, err := merge.CombinedOutput()
	if err != nil && len(resolve) == 0 {
		t.Fatalf("merging %s into %s: %v\n%s", base, branch, err, out)
	}
	if err == nil && len(resolve) > 0 {
		t.Fatalf("merging %s into %s did not conflict", base, branch)
	}
	if len(resolve) > 0 {
		for path, content := range resolve {
			if err := os.WriteFile(filepath.Join(wd, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			run(t, wd, "git", "add", path)
		}
		run(t, wd, "git", "commit", "-q", "--no-edit")
	}
	run(t, wd, "git", "push", "-q", "origin", branch)
}

func carriedNotes(t *testing.T, ctx context.Context, store *model.Store, id string) []string {
	t.Helper()
	cs, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		if strings.Contains(c.Body, "without changing the change; reviews carried from") {
			out = append(out, c.Body)
		}
	}
	return out
}

// passedGate finishes a gated change touching config/secrets/prod.yml and
// lets its one review pass, so the change merges itself again.
func passedGate(t *testing.T) (context.Context, *model.Store, string, orchestratorFixture) {
	t.Helper()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("every-pr", model.TriggerHumanOnFindings, "config")},
		"config/secrets/prod.yml")
	first := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	completeReview(t, ctx, store, first)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(20*time.Minute))
	if got := reloaded(t, ctx, store, task.ID); !got.AutoMerge || model.HasPendingReviewHold(got) {
		t.Fatalf("the first review did not pass: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	return ctx, store, first, orchestratorFixture{sim: sim.BareRepo, client: client, task: task}
}

type orchestratorFixture struct {
	sim    string
	client *github.RESTClient
	task   model.Task
}

func assertCarried(t *testing.T, ctx context.Context, store *model.Store, f orchestratorFixture, first, head string) {
	t.Helper()
	got := reloaded(t, ctx, store, f.task.ID)
	if ids := distinctGatingIDs(got); len(ids) != 1 || ids[0] != first {
		t.Fatalf("the moved head's gate is %v, want the first review %s carried", ids, first)
	}
	if open := openReviews(t, ctx, store, f.task.ID); len(open) != 1 || open[0].ID != first {
		var ids []string
		for _, r := range open {
			ids = append(ids, r.ID)
		}
		t.Fatalf("reviews of the change: %v, want only %s", ids, first)
	}
	if linkOf(got, model.LinkPathTriggersRead) != head {
		t.Errorf("the moved head %s was not recorded as judged: links=%+v", head, got.Links)
	}
	if !got.AutoMerge || model.HasPendingReviewHold(got) {
		t.Errorf("the passed gate did not carry: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if notes := carriedNotes(t, ctx, store, f.task.ID); len(notes) != 1 || !strings.Contains(notes[0], head[:12]) {
		t.Errorf("the task does not say once that its reviews carried to %s: %q", head, notes)
	}
}

func TestAnEmptyCommitCarriesTheGatingReviews(t *testing.T) {
	t.Parallel()
	ctx, store, first, f := passedGate(t)
	branch := model.BranchName(f.task.ID)

	pushEmptyCommit(t, f.sim, branch)
	head := judgeHead(t, ctx, store, f.client, f.sim, f.task, baseTime.Add(time.Hour))
	assertCarried(t, ctx, store, f, first, head)

	// Judging the same head again says nothing more.
	judgeHead(t, ctx, store, f.client, f.sim, f.task, baseTime.Add(2*time.Hour))
	if notes := carriedNotes(t, ctx, store, f.task.ID); len(notes) != 1 {
		t.Errorf("re-judging the carried head noted it again: %q", notes)
	}
}

func TestACleanBaseMergeCarriesTheGatingReviews(t *testing.T) {
	t.Parallel()
	ctx, store, first, f := passedGate(t)
	branch := model.BranchName(f.task.ID)

	pushFileToBranch(t, f.sim, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	mergeBaseResolving(t, f.sim, branch, "main", nil)
	head := judgeHead(t, ctx, store, f.client, f.sim, f.task, baseTime.Add(time.Hour))
	assertCarried(t, ctx, store, f, first, head)
}

func TestAConflictResolvingBaseMergeIsReviewedAgain(t *testing.T) {
	t.Parallel()
	ctx, store, first, f := passedGate(t)
	branch := model.BranchName(f.task.ID)

	pushFileToBranch(t, f.sim, "main", "config/secrets/prod.yml", "main's own", "main writes the same file")
	mergeBaseResolving(t, f.sim, branch, "main", map[string]string{"config/secrets/prod.yml": "resolved\n"})
	judgeHead(t, ctx, store, f.client, f.sim, f.task, baseTime.Add(time.Hour))

	got := reloaded(t, ctx, store, f.task.ID)
	if ids := distinctGatingIDs(got); len(ids) != 1 || ids[0] == first {
		t.Fatalf("a merge that resolved a conflict was not reviewed again: gate %v (first %s)", ids, first)
	}
	if got.AutoMerge || !model.HasPendingReviewHold(got) {
		t.Errorf("the re-reviewed head is not held for its review: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if notes := carriedNotes(t, ctx, store, f.task.ID); len(notes) != 0 {
		t.Errorf("a changed change was noted as carried: %q", notes)
	}
}

func TestARealChangeIsReviewedAgainFromWhatChanged(t *testing.T) {
	t.Parallel()
	ctx, store, first, f := passedGate(t)
	branch := model.BranchName(f.task.ID)
	firstHead := branchSHA(t, f.sim, branch)

	// An empty commit first: carried, and the next review still reads
	// only what changed since the head the first review read.
	pushEmptyCommit(t, f.sim, branch)
	judgeHead(t, ctx, store, f.client, f.sim, f.task, baseTime.Add(time.Hour))
	pushFile(t, f.sim, branch, "config/secrets/staging.yml", "new\n")
	head := judgeHead(t, ctx, store, f.client, f.sim, f.task, baseTime.Add(2*time.Hour))

	ids := distinctGatingIDs(reloaded(t, ctx, store, f.task.ID))
	if len(ids) != 1 || ids[0] == first {
		t.Fatalf("a real change was not reviewed again: gate %v (first %s)", ids, first)
	}
	review := reloaded(t, ctx, store, ids[0])
	_, delta, _ := strings.Cut(review.Body, "This is an incremental review.")
	if delta == "" || !strings.Contains(delta, firstHead) || !strings.Contains(delta, head) {
		t.Fatalf("the re-review is not incremental from the reviewed head %s:\n%s", firstHead, review.Body)
	}
	if !strings.Contains(delta, "`config/secrets/staging.yml`") || strings.Contains(delta, "`config/secrets/prod.yml`") {
		t.Errorf("the incremental review lists the wrong files:\n%s", delta)
	}
}

func TestCarriedTriggerFindingsStillBlock(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("every-pr", model.TriggerHumanOnFindings, "config")},
		"config/secrets/prod.yml")
	firstID := distinctGatingIDs(reloaded(t, ctx, store, task.ID))[0]
	res := toolResult(reviewFinding("plaintext token", "config/secrets/prod.yml", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, firstID), res, firstID+"-1",
		baseTime.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	completeReview(t, ctx, store, firstID)
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(20*time.Minute))
	if got := reloaded(t, ctx, store, task.ID); got.AutoMerge || !model.HasReviewFindings(got) {
		t.Fatalf("findings did not block: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}

	branch := model.BranchName(task.ID)
	pushEmptyCommit(t, sim.BareRepo, branch)
	judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(time.Hour))
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	mergeBaseResolving(t, sim.BareRepo, branch, "main", nil)
	judgeHead(t, ctx, store, client, sim.BareRepo, task, baseTime.Add(2*time.Hour))
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(3*time.Hour))

	got := reloaded(t, ctx, store, task.ID)
	if ids := distinctGatingIDs(got); len(ids) != 1 || ids[0] != firstID {
		t.Fatalf("the moved head filed a new review: gate %v (first %s)", ids, firstID)
	}
	if got.AutoMerge || !model.HasReviewFindings(got) {
		t.Fatalf("carried findings stopped blocking: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

func TestAnEmptyCommitCarriesSecurityReviewFindings(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/ui/server.go")
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	review := reloaded(t, ctx, store, reviewID)
	res := toolResult(reviewFinding("this route skips auth", "pkg/ui/server.go", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, review, res, reviewID+"-1", baseTime.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The task's agent runs again and pushes only an empty commit.
	pushEmptyCommit(t, sim.BareRepo, branch)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), result, "t1-2",
		baseTime.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	got := reloaded(t, ctx, store, task.ID)
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews, want the first carried", len(reviews))
	}
	if got.AutoMerge || !model.HasReviewFindings(got) || linkOf(got, model.LinkSecurityReviewTask) != reviewID {
		t.Fatalf("the security review's findings did not carry: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if notes := carriedNotes(t, ctx, store, task.ID); len(notes) != 1 {
		t.Errorf("the task does not say once that its reviews carried: %q", notes)
	}
}

// A path only a human may release keeps its security review across an
// empty commit, but a person's Submit is still asked for at the new head.
func TestAnEmptyCommitCarriesAHumanOnlySecurityReviewButNotTheSubmit(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, ".grain/quarantine.yml")
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	humanSubmit(t, ctx, store, task.ID, prHeadOf(t, client, reloaded(t, ctx, store, task.ID)))

	pushEmptyCommit(t, sim.BareRepo, branch)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), result, "t1-2",
		baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	got := reloaded(t, ctx, store, task.ID)
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 || linkOf(got, model.LinkSecurityReviewTask) != reviewID {
		t.Fatalf("filed %d security reviews (%s now), want %s carried", len(reviews), linkOf(got, model.LinkSecurityReviewTask), reviewID)
	}
	head := branchSHA(t, sim.BareRepo, branch)
	if !strings.HasSuffix(linkOf(got, model.LinkTrustBoundaryCleared), "@"+head) {
		t.Errorf("the classification did not move to %s: links=%+v", head, got.Links)
	}
	if got.AutoMerge || linkOf(got, model.LinkHumanOnlyHold) == "" || linkOf(got, model.LinkHumanSubmitted) != "" {
		t.Fatalf("the Submit of the old head carried: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// A `template: none` gate files no review, so nothing on the task names it:
// one the base's rules added since the passed gate must still hold the
// change for a person rather than letting the carry wave it through.
func TestAPersonOnlyGateTheBaseAddedIsNotCarriedPast(t *testing.T) {
	t.Parallel()
	ctx, store, _, f := passedGate(t)
	branch := model.BranchName(f.task.ID)

	owners := gateTrigger("owners", model.TriggerHumanOnFindings, "config")
	owners.Template = model.TemplateNone
	declareTriggers(t, ctx, store, branchSHA(t, f.sim, "main"),
		map[string]string{"sensitive": "Check {{trigger}} on {{pr}}", "planner": "Plan"},
		gateTrigger("every-pr", model.TriggerHumanOnFindings, "config"), owners)
	pushEmptyCommit(t, f.sim, branch)
	judgeHead(t, ctx, store, f.client, f.sim, f.task, baseTime.Add(time.Hour))

	got := reloaded(t, ctx, store, f.task.ID)
	if got.AutoMerge || linkOf(got, model.LinkPathTriggerHold) == "" {
		t.Fatalf("a person-only gate the base added was carried past: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if notes := carriedNotes(t, ctx, store, f.task.ID); len(notes) != 0 {
		t.Errorf("a head the base's rules gate afresh was noted as carried: %q", notes)
	}
}
