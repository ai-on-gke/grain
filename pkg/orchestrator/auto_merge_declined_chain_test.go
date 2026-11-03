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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// docs/test-plans/2026-11-03.md §3 item 3, walked as one chain through
// grain's own machinery (docs/test-runs/2026-11-03-operator-attribution.md):
// a person turns auto-merge off on a change a security hold is holding;
// the clean review releases the hold; a later push that changes no
// trust-boundary patch carries the clearance; the daemon restarts over
// the same database. Through all of it the change stays unmerged with
// auto-merge off, each refusal an auto-merge-kept-off naming its writer,
// until the person's Submit records submitted and auto-merge-on as
// theirs, and the next cycle merges it.
func TestDeclinedAutoMergeOutlastsHoldReleaseCarryAndRestartUntilSubmit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, db := openStoreInDir(t, dir)
	ctx := context.Background()
	alice := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP}
	asAlice := ui.WithPrincipal(ctx, alice)

	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
	if err := orchestrator.ProcessResult(ctx, store, client, task,
		toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"}), "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	if reviewID == "" {
		t.Fatal("a trust-boundary push was not held for review")
	}

	uiClient := func(s *model.Store) *ui.Client {
		c := ui.NewClient(ui.Config{Actor: ui.DefaultActor("owner@example.com")}, s)
		c.Now = func() time.Time { return baseTime.Add(4 * time.Hour) }
		return c
	}
	off := false
	if _, err := uiClient(store).UpdateTask(asAlice, task.ID, ui.UpdateTaskRequest{AutoMerge: &off}); err != nil {
		t.Fatal(err)
	}

	// The clean review releases the hold, and leaves auto-merge off.
	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := reloaded(t, ctx, store, task.ID); got.AutoMerge || !model.AutoMergeDeclined(got) ||
		linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("after the release: autoMerge=%v declined=%v links=%+v", got.AutoMerge, model.AutoMergeDeclined(got), got.Links)
	}

	// A push that changes no trust-boundary patch carries the clearance,
	// and still merges nothing.
	branch := model.BranchName(task.ID)
	pushFile(t, sim.BareRepo, branch, "docs/notes.md", "a docs fix\n")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a change whose auto-merge a person turned off merged after its clearance was carried")
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews, want the clearance carried", len(reviews))
	}

	// Restart: a new store over the same database file.
	db.Close()
	store, db = openStoreInDir(t, dir)
	defer db.Close()
	if got := reloaded(t, ctx, store, task.ID); got.AutoMerge || !model.AutoMergeDeclined(got) {
		t.Fatalf("after a restart: autoMerge=%v declined=%v", got.AutoMerge, model.AutoMergeDeclined(got))
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(3*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests after restart: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("merged after a restart without anybody's Submit")
	}

	// Every auto-merge write since the setup filed it on was alice's off
	// -- recorded although the hold had already turned it off -- or grain
	// keeping it off and saying which of its functions tried. (The
	// release itself does not try: a declined change is one only a person
	// releases, securityhold.go's heldForHuman.)
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawOff bool
	for _, e := range events {
		switch {
		case !sawOff && e.Kind != model.TaskEventAutoMergeOff:
			continue // the setup's own write, before alice's PATCH
		case e.Kind == model.TaskEventAutoMergeOn:
			t.Fatalf("auto-merge turned on before Submit: %+v", e)
		case e.Kind == model.TaskEventAutoMergeOff:
			sawOff = sawOff || (e.By.Same(alice) && strings.HasPrefix(e.Detail, "ui.(*Client).UpdateTask "))
		case e.Kind == model.TaskEventAutoMergeKeptOff:
			if e.Detail == "" || e.By.Kind != model.PrincipalAutomation {
				t.Errorf("kept-off event names no writer: %+v", e)
			}
		}
	}
	if !sawOff {
		t.Fatalf("events = %+v, want alice's auto-merge-off", events)
	}

	// Submit is the person's, and the next cycle merges it.
	if err := uiClient(store).Submit(asAlice, task.ID); err != nil {
		t.Fatal(err)
	}
	events, err = store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var submitted, on bool
	for _, e := range events {
		submitted = submitted || (e.Kind == model.TaskEventSubmitted && e.By.Same(alice) && e.By.Source == model.SourceIAP)
		on = on || (e.Kind == model.TaskEventAutoMergeOn && e.By.Same(alice) && e.Reason == "submitted")
	}
	if !submitted || !on {
		t.Fatalf("events = %+v, want submitted and auto-merge-on by alice", events)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(5*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests after Submit: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("a submitted change did not merge: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
}
