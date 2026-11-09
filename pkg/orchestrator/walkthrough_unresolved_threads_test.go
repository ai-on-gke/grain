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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The unresolved-conversation loop of docs/test-plans/2026-11-08.md §5
// item 4 (walked in docs/test-runs/2026-11-08-releases-qualification-mergequeue.md),
// cycle by cycle: the merge queue and the review-thread relay both run on
// every cycle, so a refusal for an unresolved conversation must cost one
// send-back between them -- not a send-back plus a relay run -- while the
// pull request carries an active thread, an outdated one (line null on a
// superseded commit) and one from grain's own draft review. The run then
// lists and resolves the threads through the same client calls `grain pr
// threads` / `grain pr resolve` reach (list_review_threads and
// resolve_review_thread), and the queue merges on its own.
func TestWalkthroughUnresolvedConversationLoopSendsBackOnceAndMergesOnResolve(t *testing.T) {
	sim, client, store, task, branch, syncPRs := unresolvedConversationQueue(t, "walk-convo")
	withDraftFinding(t, sim, store, task, branch)
	ctx := t.Context()
	cycle := func(at time.Time) {
		t.Helper()
		_ = syncPRs(at) // a refusal is reported as an error; the state is what is checked
		if err := orchestrator.SyncReviewThreads(ctx, store, client, at); err != nil {
			t.Fatalf("SyncReviewThreads: %v", err)
		}
	}

	cycle(baseTime)
	if s := stateOf(t, ctx, store, task.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q: the task goes back for the threads", s, model.StateQueued)
	}
	if obs := mergeQueueStatus(t, ctx, store, task.ID); obs.MergeQueueBlockedAt != nil {
		t.Fatalf("handed to a person on the first refusal: %q", obs.MergeQueueNote)
	}
	// Later cycles while the run is queued add nothing.
	cycle(baseTime.Add(time.Minute))
	cycle(baseTime.Add(2 * time.Minute))
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("want exactly one comment sending the task back (no second relay run), got %d:\n%s",
			len(bodies), strings.Join(bodies, "\n---\n"))
	}
	for _, want := range []string{"thread-234726", "(outdated)", "thread-234727", "scripts/run.sh:14"} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the send-back lacks %q:\n%s", want, bodies[0])
		}
	}
	if strings.Contains(bodies[0], "thread-234728") || strings.Contains(bodies[0], "owner question") {
		t.Errorf("grain's own review thread was sent to the run:\n%s", bodies[0])
	}

	// Turn 2: what `grain pr threads --unresolved` shows the run, then
	// `grain pr resolve --thread-id` on each external one.
	number := prNumber(t, sim, branch)
	threads, err := client.ListReviewThreads("acme", "walk-convo", number)
	if err != nil {
		t.Fatal(err)
	}
	open := map[string]bool{}
	for _, th := range threads {
		if !th.Resolved {
			open[th.ID] = th.Outdated
		}
	}
	if outdated, ok := open["thread-234726"]; !ok || !outdated {
		t.Fatalf("the outdated thread is not listed as open and outdated: %+v", threads)
	}
	if outdated, ok := open["thread-234727"]; !ok || outdated {
		t.Fatalf("the active thread is not listed as open and current: %+v", threads)
	}
	for _, id := range []string{"thread-234726", "thread-234727"} {
		if err := client.ResolveReviewThread("acme", "walk-convo", id); err != nil {
			t.Fatalf("resolving %s: %v", id, err)
		}
	}
	// grain's own draft-review thread is the owner's to answer; GitHub's
	// rule still counts it, so the owner resolves it as they answer.
	sim.ResolvedThreads["thread-234728"] = true
	done := baseTime.Add(20 * time.Minute)
	if err := store.ObserveField(ctx, task.ID, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	cycle(done.Add(time.Minute))
	if !mergedHead(sim, branch) {
		t.Fatalf("not merged once every thread was resolved: %q", mergeQueueStatus(t, ctx, store, task.ID).MergeQueueNote)
	}
	if got := len(commentBodies(t, ctx, store, task.ID)); got != 1 {
		t.Errorf("comments after the merge = %d, want still 1", got)
	}
}

// The same loop where the sent-back run leaves a thread open: the next
// refusal is handed to a person (merge-blocked), and further cycles neither
// send the task back again nor keep asking GitHub to merge.
func TestWalkthroughUnresolvedConversationLeftOpenIsHandedOffWithoutLooping(t *testing.T) {
	sim, client, store, task, branch, syncPRs := unresolvedConversationQueue(t, "walk-convo-left")
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
	if err := client.ResolveReviewThread("acme", "walk-convo-left", "thread-234727"); err != nil {
		t.Fatal(err)
	}
	done := baseTime.Add(20 * time.Minute)
	if err := store.ObserveField(ctx, task.ID, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	cycle(done.Add(time.Minute))
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("the thread the run left open was not handed to a person")
	}
	calls := mergeCalls(sim)
	comments := len(commentBodies(t, ctx, store, task.ID))
	for i := 2; i <= 6; i++ {
		cycle(done.Add(time.Duration(i) * time.Minute))
	}
	if s := stateOf(t, ctx, store, task.ID); s == model.StateQueued {
		t.Fatal("the task was sent back a second time")
	}
	if got := mergeCalls(sim); got != calls {
		t.Errorf("merge asked for %d more time(s) after the hand-off", got-calls)
	}
	if got := len(commentBodies(t, ctx, store, task.ID)); got != comments {
		t.Errorf("%d more comment(s) after the hand-off", got-comments)
	}
	if mergedHead(sim, branch) {
		t.Fatal("merged with a thread unresolved")
	}
}
