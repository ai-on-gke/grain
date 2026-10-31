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

package daemon

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// reapInterval is how often reconcile calls reapCapabilities. The resource
// age threshold itself is dynamic: reapGracePeriod behind the oldest live
// agent or CI run.
const reapInterval = time.Hour

// reapGracePeriod keeps the shared trailing edge behind live-work timestamps
// and an idle deployment's clock. It absorbs timestamp skew, persistence
// ordering, and eventually consistent cloud listings without making leaked
// resources wait for a fixed 24-hour TTL.
const ReapGracePeriod = 10 * time.Minute

// reapBoundary is computed once per sweep and shared by every provider. With
// live work it trails the oldest start; while idle it trails now, allowing
// prompt cleanup without risking a resource created concurrently with the
// census.
func ReapBoundary(ctx context.Context, store *model.Store, now time.Time) (time.Time, error) {
	if store == nil {
		return time.Time{}, errors.New("cannot compute a reaper boundary without a store")
	}
	oldest, live, err := store.OldestLiveWorkStartedAt(ctx)
	if err != nil {
		return time.Time{}, err
	}
	edge := now
	if live && oldest.Before(edge) {
		edge = oldest
	}
	return edge.Add(-ReapGracePeriod), nil
}

// reapCapabilitiesBefore calls every registered Reaper with an explicit
// boundary timestamp. Providers return partial deletion errors alongside
// successes; both are logged so cleanup cannot silently remove nothing.
func ReapCapabilitiesBefore(ctx context.Context, registry *model.CapabilityRegistry, creds model.CredentialResolver, before time.Time) {
	if registry == nil {
		return
	}
	for _, p := range registry.Providers() {
		reaper, ok := p.(model.Reaper)
		if !ok {
			continue
		}
		deleted, err := reaper.Reap(ctx, creds, before)
		if len(deleted) > 0 {
			log.Printf("grain daemon: reaped %d resource(s) created before %s for capability %q: %v",
				len(deleted), before.Format(time.RFC3339), p.Spec().Name, deleted)
		}
		if err != nil {
			log.Printf("grain daemon: reaping capability %q before %s: %v",
				p.Spec().Name, before.Format(time.RFC3339), err)
		}
	}
}

// reapCapabilities calls every registered Reaper with one boundary derived
// from all live agent and CI work. A census failure fails closed and skips the
// whole sweep.
func reapCapabilities(ctx context.Context, store *model.Store, registry *model.CapabilityRegistry, creds model.CredentialResolver, now time.Time) {
	if registry == nil {
		return
	}
	before, err := ReapBoundary(ctx, store, now)
	if err != nil {
		log.Printf("grain daemon: computing capability reap boundary: %v", err)
		return
	}
	ReapCapabilitiesBefore(ctx, registry, creds, before)
}

// reconcile calls orchestrator.RunCycle every poll interval, and
// reapCapabilities every reapInterval, until ctx is done, logging (never
// panicking on) whatever either returns -- one bad cycle must not take
// the whole daemon down, since the next tick gets another chance at
// whatever failed. RunCycle ticks are not overlapped: reconcile waits for
// one to return before the next interval starts, so a slow GitHub poll
// simply delays the next dispatch rather than racing it. What a tick no
// longer waits for is the runs it starts: deps.Runs (set in runDaemon)
// makes RunCycle hand each dispatch to a goroutine and return, so a tick
// takes as long as the cycle's decisions rather than as long as its
// agents -- see orchestrator.InFlight for what the old wait cost, which
// was every other task's dispatch for as long as any one run lasted. A
// reap running concurrently with a RunCycle tick is fine either way; the
// two touch disjoint state (a reap only ever deletes a resource no live Lease
// still names an outstanding run against). Ported from pkg/orchestrate's
// own Reconciler.Run (bwsalmon/agents#254) when that package merged into
// pkg/orchestrator, which -- being "a library, not a binary" (its own
// doc comment) -- has no timer loop of its own.
//
// Each tick measures itself into deps.CycleTimes (the package-level
// cycleTimes ring, which GET /api/metrics reads back): "waits for one to
// return before the next interval starts" is exactly why a tick's own
// duration is part of how long a queued task waits, and nothing outside
// a test could see it.
//
// The interval, and everything else in live, is re-read from the store
// once per tick rather than fixed for the life of the process: this loop
// is the deployment's own heartbeat, so it is also the natural place to
// notice a setting has changed and hand the change to whatever it
// configures (liveConfig.refresh). deps is the loop's own copy, mutated
// in place by that refresh -- each cycle and each dispatch goroutine
// takes its own copy of it, so nothing already running sees it change
// underneath.
func reconcile(ctx context.Context, deps orchestrator.Deps, live *LiveConfig) {
	if deps.Ledger != nil && deps.Ledger.Wake == nil {
		deps.Ledger.Wake = wakeCycle
	}
	ReconcileWith(ctx, deps, live, cycleWakes, IapSyncTriggers)
}

// minWakeInterval is the shortest gap reconcileWith leaves between two
// woken cycles. It is not a rate limit on replies -- every wake is
// honoured -- but on how closely two of them may be honoured: a wake
// arriving sooner than this after the last cycle started is served by a
// cycle scheduled at the end of it, so a hundred comments in a second
// cost one extra cycle rather than a hundred.
//
// A second, because that is well under the wait it is shortening (the
// shipped -poll-interval is 30s) and well over what a cycle costs on
// this deployment's own measurements -- so the bound never shows up as
// latency a person notices, and does bound the loop for a store where a
// tick has grown expensive.
const minWakeInterval = 50 * time.Millisecond

// reconcileWith is reconcile with the wake channel as an argument, so a
// test can drive it. See reconcile and cycleWakes.
func ReconcileWith(ctx context.Context, deps orchestrator.Deps, live *LiveConfig, wakes <-chan struct{}, iapSyncs <-chan struct{}) {
	var depsMu sync.Mutex
	snapshotDeps := func() orchestrator.Deps {
		depsMu.Lock()
		defer depsMu.Unlock()
		return deps
	}
	var lastTick time.Time
	tick := func() {
		lastTick = time.Now()
		if err := orchestrator.RunCycle(ctx, snapshotDeps(), time.Now().UTC()); err != nil {
			log.Printf("grain daemon: reconcile cycle: %v", err)
		}
	}
	reap := func() {
		d := snapshotDeps()
		reapCapabilities(ctx, d.Store, d.Config.Capabilities, d.Config.Credentials, time.Now().UTC())
	}
	syncIAP := func() {
		// ReconcileIAP itself returns at once when no IAP target is
		// configured, after clearing the status of any that was.
		d := snapshotDeps()
		if err := ReconcileIAP(ctx, d.Store, live.Current()); err != nil {
			log.Printf("grain daemon: IAP IAM reconciliation: %v", err)
		}
	}
	reap()
	tick()
	syncIAP()
	// The interval is positive by construction, here and at the Reset
	// below: checkConfig refuses a flag that is not, and neither
	// loadConfig nor liveConfig ever adopts a stored value a ticker could
	// not be built from (runnableStoredConfig). time.NewTicker's own panic
	// on a non-positive one is left exactly where it is rather than
	// guarded here -- a loop that paced itself by a number nobody checked
	// would be worse than one that stops -- but nothing this deployment
	// can be configured with should reach it any more (grain/task-316).
	ticker := time.NewTicker(live.Current().PollInterval)
	defer ticker.Stop()
	reapTicker := time.NewTicker(reapInterval)
	defer reapTicker.Stop()

	loopWakes := wakes
	var wakeWG sync.WaitGroup
	defer wakeWG.Wait()
	if deps.Runs != nil && wakes != nil {
		forwarded := make(chan struct{}, 1)
		loopWakes = forwarded
		wakeWG.Add(1)
		go func() {
			defer wakeWG.Done()
			var lastFastWake time.Time
			fastTimer := time.NewTimer(time.Hour)
			if !fastTimer.Stop() {
				<-fastTimer.C
			}
			defer fastTimer.Stop()
			fastPending := false
			inFlightRetry := false
			runFast := func(allowInFlightRetry bool) {
				lastFastWake = time.Now()
				d := snapshotDeps()
				hadInFlight := d.Runs != nil && d.Runs.Len() > 0
				if err := orchestrator.RunDispatchCycle(ctx, d, time.Now().UTC()); err != nil {
					log.Printf("grain daemon: woken dispatch cycle: %v", err)
				}
				if allowInFlightRetry && hadInFlight && !fastPending {
					fastPending = true
					inFlightRetry = true
					fastTimer.Reset(minWakeInterval)
				}
				select {
				case forwarded <- struct{}{}:
				default:
				}
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-wakes:
					if fastPending {
						inFlightRetry = false
						continue
					}
					wait := max(0, minWakeInterval-time.Since(lastFastWake))
					if wait == 0 {
						runFast(true)
					} else {
						fastPending = true
						inFlightRetry = false
						fastTimer.Reset(wait)
					}
				case <-fastTimer.C:
					allowRetry := !inFlightRetry
					fastPending = false
					inFlightRetry = false
					runFast(allowRetry)
				}
			}
		}()
	}

	// woken fires when a wake that had to be spaced out comes due;
	// wakePending says one is already scheduled, which is what makes a
	// burst of wakes cost a single cycle. Stopped rather than running, so
	// that a loop nobody ever wakes behaves exactly as it did before.
	woken := time.NewTimer(time.Hour)
	if !woken.Stop() {
		<-woken.C
	}
	defer woken.Stop()
	wakePending := false
	cycle := func() {
		// Before the cycle, so a settings change made since the last
		// tick is in effect for the dispatches this one decides on
		// rather than for the one after it.
		depsMu.Lock()
		changed := live.Refresh(ctx, &deps)
		depsMu.Unlock()
		if changed {
			ticker.Reset(live.Current().PollInterval)
		}
		tick()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cycle()
		case <-loopWakes:
			if wakePending {
				// A cycle is already coming for an earlier wake, and it
				// will read the store as it stands when it runs -- which
				// includes whatever this wake was about.
				continue
			}
			wakePending = true
			wait := max(0, minWakeInterval-time.Since(lastTick))
			if deps.Runs != nil {
				// RunDispatchCycle has already dispatched ready work;
				// give newly spawned runs a brief head start on the
				// store before running the full background cycle.
				wait = max(wait, 500*time.Millisecond)
			}
			woken.Reset(wait)
		case <-woken.C:
			wakePending = false
			// The periodic tick is pushed out a whole interval by a woken
			// one rather than left to fire straight after it: what
			// -poll-interval asks for is a cycle at least that often, and
			// one has just run.
			ticker.Reset(live.Current().PollInterval)
			cycle()
		case <-reapTicker.C:
			reap()
		case <-iapSyncs:
			syncIAP()
		}
	}
}
