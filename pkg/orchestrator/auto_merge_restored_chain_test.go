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
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// The other half of TestDeclinedAutoMergeOutlastsHoldReleaseCarryAndRestartUntilSubmit
// (releases playbook §0, gap 1): a change filed to merge itself is held
// for a security review, the daemon restarts -- over the same database,
// or onto a fresh one from a state-repo dump -- the review comes back
// clean, and the change merges with nobody's Submit. Its events say the
// hold turned auto-merge off and its release turned it back on.
func TestFiledAutoMergeComesBackWhenItsSecurityHoldClearsAcrossARestart(t *testing.T) {
	for _, restart := range []string{"reopen", "state-repo import"} {
		t.Run(restart, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			store, db := openStoreInDir(t, dir+"/db")
			ctx := context.Background()

			sim, client := newSim(t, "acme", "widgets", "main")
			task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
			if err := orchestrator.ProcessResult(ctx, store, client, task,
				toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"}), "t1-1", baseTime); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}
			held := reloaded(t, ctx, store, task.ID)
			reviewID := linkOf(held, model.LinkSecurityReviewTask)
			if reviewID == "" || held.AutoMerge {
				t.Fatalf("a trust-boundary push was not held: autoMerge=%v links=%+v", held.AutoMerge, held.Links)
			}

			switch restart {
			case "reopen":
				db.Close()
				store, db = openStoreInDir(t, dir+"/db")
			case "state-repo import":
				dump := dir + "/dump"
				if err := staterepo.Export(ctx, db, dump); err != nil {
					t.Fatalf("exporting: %v", err)
				}
				db.Close()
				var fresh *sql.DB
				store, fresh = openStoreInDir(t, dir+"/fresh")
				if err := staterepo.Import(ctx, fresh, dump); err != nil {
					t.Fatalf("importing: %v", err)
				}
				db = fresh
			}
			defer db.Close()
			if got := reloaded(t, ctx, store, task.ID); got.AutoMerge || linkOf(got, model.LinkSecurityHold) == "" {
				t.Fatalf("after the %s: autoMerge=%v links=%+v, want still held", restart, got.AutoMerge, got.Links)
			}

			completeReview(t, ctx, store, reviewID)
			if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			branch := model.BranchName(task.ID)
			setMergeable(sim, true)
			sim.CheckRuns[branch] = greenCheck
			if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
				t.Fatalf("SyncPullRequests: %v", err)
			}
			if !mergedBranch(sim, branch) {
				got := reloaded(t, ctx, store, task.ID)
				t.Fatalf("a change filed to merge itself waited after a clean review: autoMerge=%v links=%+v",
					got.AutoMerge, got.Links)
			}

			events, err := store.TaskEvents(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			var off, on bool
			for _, e := range events {
				switch e.Kind {
				case model.TaskEventAutoMergeOff:
					off = off || strings.Contains(e.Detail, "holdForTrustBoundary")
				case model.TaskEventAutoMergeOn:
					on = on || (off && strings.Contains(e.Detail, "settleSecurityHold"))
				case model.TaskEventSubmitted:
					t.Fatalf("submitted: %+v", e)
				}
			}
			if !off || !on {
				t.Fatalf("events = %+v, want the hold's auto-merge-off and its release's auto-merge-on", events)
			}
		})
	}
}

// A review that left findings ends the hold for a person: the change is
// not handed its auto-merge back when the hold's links go, and a gating
// trigger settling afterwards does not hand it back either.
func TestAHoldEndedByFindingsWaitsForAPerson(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, task, _ := heldTask(t, ctx, store)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		model.AwaitPersonForAutoMerge(tk, "review r1 left findings")
		tk.Links = withoutKinds(tk.Links, model.LinkSecurityHold, model.LinkTrustBoundaryCleared)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || !model.AutoMergeAwaitsPerson(got) {
		t.Fatalf("autoMerge=%v links=%+v, want off and waiting for a person", got.AutoMerge, got.Links)
	}
}

func withoutKinds(links []model.Link, kinds ...model.LinkKind) []model.Link {
	var out []model.Link
	for _, l := range links {
		keep := true
		for _, k := range kinds {
			keep = keep && l.Kind != k
		}
		if keep {
			out = append(out, l)
		}
	}
	return out
}

// A change held for a person only because one head touched a path only a
// human may release is classified again when a rerun pushes a head that
// no longer touches it, and merges itself as it was filed to. Before, a
// human-only hold was never classified again (holdForTrustBoundary read
// it as auto-merge somebody had turned off), so the change waited for a
// Submit nobody had been asked for.
func TestAHumanOnlyHoldClearsWhenTheHeadNoLongerNeedsAHuman(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", ".grain/quarantine.yml", true)
	if err := orchestrator.ProcessResult(ctx, store, client, task,
		toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"}), "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := reloaded(t, ctx, store, task.ID); got.AutoMerge || linkOf(got, model.LinkHumanOnlyHold) == "" {
		t.Fatalf("not held for a human: autoMerge=%v links=%+v", got.AutoMerge, got.Links)
	}

	branch := model.BranchName(task.ID)
	removeFile(t, sim.BareRepo, branch, ".grain/quarantine.yml")
	pushFile(t, sim.BareRepo, branch, "docs/notes.md", "a docs fix\n")
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID),
		toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"}), "t1-2", baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkHumanOnlyHold) != "" {
		t.Fatalf("a head off the trust boundary is still held: autoMerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("did not merge: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
}
