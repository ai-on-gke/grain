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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// docs/test-plans/2026-11-09.md §7 item 5, walked in
// docs/test-runs/2026-11-09-external-agent-rails.md: after an
// unresolved-thread refusal is handed to a person, a dozen more queue
// cycles ask GitHub to merge nothing (the 2026-11-08 test ran five); once
// the person resolves the last thread, the queue picks the task back up
// on its own and asks GitHub to merge exactly once more, and it merges.
func TestWalkthroughHandOffAsksNothingForTwelveCyclesThenMergesOnceOnResolve(t *testing.T) {
	sim, client, store, task, branch, syncPRs := unresolvedConversationQueue(t, "walk-handoff")
	ctx := t.Context()
	cycle := func(at time.Time) {
		t.Helper()
		_ = syncPRs(at)
		if err := orchestrator.SyncReviewThreads(ctx, store, client, at); err != nil {
			t.Fatalf("SyncReviewThreads: %v", err)
		}
	}
	cycle(baseTime)
	if s := stateOf(t, ctx, store, task.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q", s, model.StateQueued)
	}
	if err := client.ResolveReviewThread("acme", "walk-handoff", "thread-234727"); err != nil {
		t.Fatal(err)
	}
	done := baseTime.Add(20 * time.Minute)
	if err := store.ObserveField(ctx, task.ID, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	cycle(done.Add(time.Minute))
	if obs := mergeQueueStatus(t, ctx, store, task.ID); obs.MergeQueueBlockedAt == nil {
		t.Fatal("the thread the run left open was not handed to a person")
	}
	// Two asks before the hand-off: the refusal that sent the task back,
	// and the one asked once its run finished, which is handed off.
	handedOff := mergeCalls(sim)
	if handedOff != 2 {
		t.Fatalf("merge calls before the hand-off = %d, want 2", handedOff)
	}
	at := done.Add(time.Minute)
	for i := 0; i < 12; i++ {
		at = at.Add(time.Minute)
		cycle(at)
	}
	if got := mergeCalls(sim); got != handedOff {
		t.Fatalf("merge asked for %d more time(s) over 12 cycles after the hand-off", got-handedOff)
	}
	if mergedHead(sim, branch) {
		t.Fatal("merged with a thread unresolved")
	}

	// The person resolves what is left; the queue merges on its own by
	// the next refusal poll (mergeRefusalPollInterval, 2m).
	if err := client.ResolveReviewThread("acme", "walk-handoff", "thread-234726"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		at = at.Add(time.Minute)
		cycle(at)
	}
	if !mergedHead(sim, branch) {
		t.Fatalf("not merged once the last thread was resolved: %q", mergeQueueStatus(t, ctx, store, task.ID).MergeQueueNote)
	}
	if got := mergeCalls(sim) - handedOff; got != 1 {
		t.Errorf("merge calls after the resolve = %d, want exactly 1", got)
	}
}
