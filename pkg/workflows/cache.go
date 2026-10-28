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

package workflows

import (
	"context"
	"time"
)

// Cache is where a finished job's result is remembered, so the same
// work is not done twice. DirCache is the implementation a deployment
// gets; this is the seam a test replaces and a deployment with somewhere
// better to put entries would implement instead.
//
// It is two methods because that is all a content-addressed cache needs
// to be: a key is a promise that the work is identical (Fingerprint), so
// there is nothing to invalidate, nothing to expire on a schedule and
// no dependency graph to walk. An implementation decides for itself how
// long to keep an entry and how large to let itself get; nothing here
// asks.
//
// The contract in three lines:
//
//   - Lookup returns ok=false for anything it does not have, including
//     anything it has decided to stop trusting. An error is a cache that
//     is *broken*, and the engine treats it as a miss and runs the job:
//     a cache must never be able to fail a build.
//   - Save is best effort for the same reason. The engine logs a failure
//     and carries on.
//   - Only a terminal, self-contained conclusion is ever saved: success
//     or failure, never skipped, cancelled or pending. A skipped job's
//     result is a fact about the run it was in, not about the job.
type Cache interface {
	Lookup(ctx context.Context, key Key) (JobRun, bool, error)
	Save(ctx context.Context, key Key, result JobRun) error
}

// InspectableCache is a Cache an operator can also see and empty. It is
// separate from Cache, rather than two more methods on it, because
// Cache is the contract the *engine* needs and the engine needs neither
// of these: a cache that cannot say how big it is still answers
// correctly, and a test's map does not want a directory walk written for
// it. Whatever serves an operator (pkg/ui's /api/ci/cache, and `grain ci
// cache` behind it) asks for this and says "not this cache" when it does
// not get it.
//
// Clearing is safe by construction and needs no ceremony: an entry is a
// promise that identical work was already done (Fingerprint), never a
// fact only the cache holds, so the worst an empty cache costs is the
// re-runs it was saving.
type InspectableCache interface {
	Cache
	// Stats is what this cache is holding and what it is allowed to
	// hold. An error means it could not be read at all.
	Stats(ctx context.Context) (CacheStats, error)
	// Clear empties it, returning how many entries it removed. A cache
	// it could only partly empty is an error carrying the count it did
	// manage.
	Clear(ctx context.Context) (int, error)
}

// CacheStats is one cache as an operator sees it: what it holds, how
// well it is working, and the bounds it is being kept inside.
//
// It is deliberately about disk and hit rate rather than about
// correctness. Nothing here can be wrong in a way that fails a build --
// the point of showing it is the two questions the daemon's journal
// answered only in passing: is this cache actually saving anything, and
// is it about to fill the data disk.
type CacheStats struct {
	// Dir is where the entries live, for an operator being told where to
	// look. Empty for a cache that is not a directory.
	Dir string
	// Entries and Bytes are what is in it now.
	Entries int
	Bytes   int64
	// Oldest and Newest are the age span of what is held -- Oldest is
	// what the next sweep will drop first, and Newest is the last time
	// anything was worth caching at all. Zero for an empty cache.
	Oldest time.Time
	Newest time.Time
	// LastSweep is when the bounds were last enforced, zero for a cache
	// that has not swept since this daemon started. Sweeps are cheap and
	// occasional (sweepInterval), so "a while ago" here is ordinary: it
	// means nothing has been saved recently, not that the cache is
	// unattended.
	LastSweep time.Time
	// Hits and Misses count Lookups since this daemon started, which is
	// as far back as they go: they live in memory, and a restart is a
	// cache that is still full and a counter that is empty. A miss is
	// every answer that was not a served entry, including the entries
	// this cache decided to stop trusting.
	Hits   int64
	Misses int64
	// The bounds themselves, so a reader can tell "256MB of cache" from
	// "256MB of cache, which is all it may have".
	MaxAge        time.Duration
	MaxBytes      int64
	MaxEntries    int
	MaxEntryBytes int64
}

// Cacheable reports whether a finished job's result is one a cache may
// keep: it ran to a real conclusion, and that conclusion is a fact
// about the job rather than about the run it happened to be in.
//
// A job that failed to get a sandbox at all is deliberately excluded:
// that is a fact about the deployment on the day, and caching it would
// make one bad afternoon permanent for a commit. It now says so in its
// own conclusion (ConclusionErrored) rather than only in having an Error
// and no steps, so it falls out of the switch below without a special
// case -- the Error-and-no-steps guard on a failure stays for the job
// whose sandbox died mid-run, which is a failure by the same rule.
func Cacheable(result JobRun) bool {
	switch result.Conclusion {
	case ConclusionSuccess:
		return true
	case ConclusionFailure:
		return len(result.Steps) > 0 && result.Error == ""
	default:
		return false
	}
}
