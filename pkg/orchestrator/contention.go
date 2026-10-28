package orchestrator

import (
	"context"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// noteContention records one thing the merge queue had to do over again
// because something else moved (model.MergeQueueContentionEvent), which
// is what the merges report's contention counters are read from.
//
// Telemetry rather than state: nothing the queue decides reads it back,
// so a write that fails is logged and the cycle goes on -- a missed count
// is a smaller harm than a merge held over a measurement.
func noteContention(ctx context.Context, store *model.Store, repo model.RepoRef, taskID, kind, reason string, now time.Time) {
	if err := store.RecordMergeQueueContention(ctx, model.MergeQueueContentionEvent{
		Repo: repo, TaskID: taskID, Kind: kind, Reason: reason, At: now,
	}); err != nil {
		log.Printf("orchestrator: recording merge queue %s (%s) for %s %s: %v", kind, reason, repo, taskID, err)
	}
}

// noteRebase records the queue merging a pull request's base into its
// head, and the suite that merge sends the change back through: two
// events, since a rebase is both -- a new tree, and the verdict about the
// old one thrown away.
func noteRebase(ctx context.Context, store *model.Store, ref model.PullRequestRef, taskID, reason string, now time.Time) {
	noteContention(ctx, store, ref.Repo, taskID, model.MergeQueueRebase, reason, now)
	noteContention(ctx, store, ref.Repo, taskID, model.MergeQueueRetest, model.MergeQueueAfterRebase, now)
}
