// Newcomers join the merge queue at the back (grain/task-545): a task
// filed at the front of the backlog -- a port, an at_front or newest-first
// task -- takes an OrderKey below everything in the table, and must not
// take position one in the merge queue the cycle its pull request opens.
package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

func TestATaskFiledAtTheFrontJoinsTheMergeQueueBehindItsMembers(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	runner := newFakeCI()

	// t1 is already in the queue, its CI still running.
	waiting, waitingBranch := queuedTaskOnBase(t, ctx, store, fix.sim, fix.client, "t1", fix.repo, "main")
	waiting = grantCI(t, ctx, store, waiting)
	status := startRequiredCI(t, ctx, store, fix.client, runner, waiting, fix.repo, waitingBranch)
	setMergeable(fix.sim, true)
	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))
	if state, note := mergeQueueStateOf(t, ctx, store, "t1"); state != "waiting_ci" {
		t.Fatalf("t1 says %q (%q), want waiting_ci", state, note)
	}

	// t2 is filed at the front of the backlog, the way newPortTask files a
	// port (Store.OrderKeyForNewTask(ctx, true)), and is completed with a
	// green pull request at once, the way a mechanical port is.
	front, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	port, portBranch := queuedTaskOnBase(t, ctx, store, fix.sim, fix.client, "t2", fix.repo, "main")
	port.OrderKey = front
	if err := store.PutTask(ctx, port); err != nil {
		t.Fatal(err)
	}
	port = grantCI(t, ctx, store, port)
	greenGrainCI(t, ctx, store, fix.client, runner, port, fix.repo, portBranch)
	setMergeable(fix.sim, true)

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(2*time.Minute))

	if mergedBranch(fix.sim, portBranch) {
		t.Fatal("the task filed at the front merged ahead of a change already waiting in the queue")
	}
	state, note := mergeQueueStateOf(t, ctx, store, "t2")
	if state != "waiting_queue" || !strings.Contains(note, "behind task t1") {
		t.Fatalf("t2 says %q (%q), want waiting_queue behind task t1", state, note)
	}
	// And the backlog shows it there, so the next cycle -- and a person
	// reading the list -- sees the same order.
	t1, err := store.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	t2, err := store.GetTask(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if t2.OrderKey <= t1.OrderKey {
		t.Fatalf("t2's order key %v is not behind t1's %v", t2.OrderKey, t1.OrderKey)
	}

	// Once t1 is green it merges first, and t2 follows on a later cycle.
	runner.finish(status.RunID, ci.ConclusionSuccess)
	if err := runCIReconciler(t, ctx, store, fix.client, runner); err != nil {
		t.Fatal(err)
	}
	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(3*time.Minute))
	if !mergedBranch(fix.sim, waitingBranch) {
		state, note := mergeQueueStateOf(t, ctx, store, "t1")
		t.Fatalf("t1 did not merge once green (%q: %q)", state, note)
	}
}
