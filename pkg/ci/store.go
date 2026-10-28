package ci

import (
	"context"
)

// BaseTransitionCacheEvictor is implemented by CI caches that support invalidating
// or purging negative (failing) cached verdicts tied to base transitions.
type BaseTransitionCacheEvictor interface {
	// PurgeNegativeEntries removes cached failures from this cache. If baseCommit is non-empty,
	// entries with ConclusionFailure recorded against exactly baseCommit are removed; failures
	// against any other base are left for the engine to discard at lookup, when the run's own
	// base differs from theirs. If baseCommit is empty, all entries with ConclusionFailure are
	// removed. Passing entries are never affected.
	PurgeNegativeEntries(ctx context.Context, baseCommit string) (int, error)
}

// EvictBaseTransitionFailures purges cached negative verdicts tied to a base commit
// from the cache when the base branch advances, avoiding false negative replays while
// keeping unaffected passing jobs cached.
func EvictBaseTransitionFailures(ctx context.Context, cache Cache, baseCommit string) (int, error) {
	if cache == nil {
		return 0, nil
	}
	if evictor, ok := cache.(BaseTransitionCacheEvictor); ok {
		return evictor.PurgeNegativeEntries(ctx, baseCommit)
	}
	return 0, nil
}
