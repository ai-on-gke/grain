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

package change

import (
	"fmt"
	"sync"
	"time"
)

// Stage S6 (google3-cls.md §7, G3-7): the daemon syncing a CL to head on
// its own, with no agent. Each automatic trigger fires at most once per
// CL snapshot, never while the task has a run going, and a sync that
// failed is not tried again automatically until the CL is uploaded anew
// (NoteUpload) -- so a sync that keeps failing cannot loop. A person's
// Sync button is none of these: it always asks.

// The reasons the daemon syncs a CL.
const (
	SyncNeedsSync  = "needs_sync" // a Submit or presubmit said the CL needs a sync
	SyncBehind     = "behind"     // health: behind head
	SyncConflicted = "conflicted" // health: conflicts with head
	SyncStale      = "stale"      // an awaiting_submit snapshot older than SyncAfter
	SyncButton     = "button"     // a person pressed Sync
)

// DefaultSyncAfter is how old an awaiting_submit CL's snapshot may grow
// before the daemon syncs it, where the provider does not say
// (google3.sync_after).
const DefaultSyncAfter = 24 * time.Hour

// SyncPolicy is a Provider that says how old an awaiting_submit snapshot
// may grow before it is synced; zero turns the timer off.
type SyncPolicy interface {
	SyncAfter() time.Duration
}

// SyncAfterOf is p's stale-snapshot interval.
func SyncAfterOf(p Provider) time.Duration {
	if sp, ok := p.(SyncPolicy); ok {
		return sp.SyncAfter()
	}
	return DefaultSyncAfter
}

// SyncPlanner remembers which automatic syncs it has made. Memory only:
// a restart may sync each CL once more, which is the cost of a sync and
// no more.
type SyncPlanner struct {
	mu sync.Mutex
	// tried is target@snapshot for every snapshot an automatic sync was
	// made on.
	tried map[string]bool
	// failed is a target whose last automatic sync failed, until its next
	// upload.
	failed map[string]bool
	// wanted is a target a Submit or presubmit said needs a sync.
	wanted map[string]bool
}

// Planner is the daemon's SyncPlanner.
var Planner = NewSyncPlanner()

// NewSyncPlanner is an empty SyncPlanner.
func NewSyncPlanner() *SyncPlanner {
	return &SyncPlanner{tried: map[string]bool{}, failed: map[string]bool{}, wanted: map[string]bool{}}
}

func snapKey(target string, snapshot int) string { return fmt.Sprintf("%s@%d", target, snapshot) }

// WantSync records that target needs a sync: a Submit or presubmit said
// so. The next Due for it answers SyncNeedsSync.
func (p *SyncPlanner) WantSync(target string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wanted[target] = true
}

// Due is the automatic sync obs calls for, if any: "" when none is due,
// one has already been made on this snapshot, the last one failed, or
// running is true. awaitingSubmit is whether the task is waiting on a
// Submit, which the stale-snapshot timer alone needs.
func (p *SyncPlanner) Due(obs Observation, running, awaitingSubmit bool, syncAfter time.Duration, now time.Time) string {
	if obs.Target == "" || running || obs.Status == StatusSubmitted || obs.Status == StatusAbandoned {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed[obs.Target] || p.tried[snapKey(obs.Target, obs.Snapshot)] {
		return ""
	}
	switch {
	case p.wanted[obs.Target]:
		return SyncNeedsSync
	case obs.Health == HealthBehind:
		return SyncBehind
	case obs.Health == HealthConflicted:
		return SyncConflicted
	case awaitingSubmit && syncAfter > 0 && !obs.SnapshotAt.IsZero() && now.Sub(obs.SnapshotAt) >= syncAfter:
		return SyncStale
	}
	return ""
}

// Synced records an automatic sync made on obs's snapshot, and whether
// it failed.
func (p *SyncPlanner) Synced(target string, snapshot int, failed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tried[snapKey(target, snapshot)] = true
	delete(p.wanted, target)
	if failed {
		p.failed[target] = true
	}
}

// NoteUpload records a new upload of target: a failed sync may be tried
// again on what the upload made.
func (p *SyncPlanner) NoteUpload(target string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.failed, target)
}
