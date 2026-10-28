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

package engine

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/workflows"
)

// Gate is how many job sandboxes may exist at once across everything one
// Engine is running -- the deployment-wide bound, as against
// Engine.Concurrency, which bounds one run.
//
// Acquire blocks until a job may hold a sandbox and returns the release
// that gives it back. An error -- only ever ctx's -- means this job never
// got one and must not run; release is nil then, and nothing is owed.
// release is safe to call more than once and must be called exactly once
// per successful Acquire, which the engine does with defer.
type Gate interface {
	Acquire(ctx context.Context, req GateRequest) (release func(), err error)
}

// GateRequest is one job's ask of a Gate: what its sandbox will commit,
// what the job is worth against everything else queueing, and where to
// report a wait.
type GateRequest struct {
	// Want is what this job's sandbox will commit -- its shape, resolved
	// to the machine it actually takes (grain/task-173). A gate that only
	// counts ignores it; the deployment's own also holds it against a
	// memory and vCPU budget, which is why the shape is passed rather
	// than looked up. The zero commitment means "nothing to charge", and
	// is what a caller with no shape in hand passes.
	Want workflows.Commitment
	// Priority is what this job is worth against the others waiting here,
	// asked afresh on every attempt this job makes. nil means
	// PriorityNormal, which is what every job at a gate nobody ranks gets
	// and leaves the order to arrival alone.
	Priority func() workflows.Priority
	// Wait, when it is not nil, is called each time the reason this job is
	// still queueing changes -- "waiting for a free sandbox", "waiting
	// for memory", "waiting behind higher-priority work" -- so that a job
	// held back is visible as held back rather than merely slow to start.
	// It is never called on a job that is admitted immediately, and it is
	// called with the zero Wait when the job is finally admitted.
	Wait func(workflows.Wait)
	// Assigned, when it is not nil, is called once with the id this
	// attempt's charge is filed under in the deployment's ledger the
	// moment it joins the queue, before it is known whether admission
	// ever succeeds.
	Assigned func(id string)
	// Adopted is true when this job's sandbox is already running on the host
	// from before a daemon restart: the gate must immediately record its slot
	// and ledger charge without blocking behind the queue.
	Adopted bool
}

// DefaultGatePoll is how often a waiting job re-asks a gate whose limit it
// has not been able to meet.
const DefaultGatePoll = time.Second

// DefaultAgeBoost is how long a waiter has to queue to gain one step of
// rank -- the anti-starvation floor under SharedGate's ordering.
const DefaultAgeBoost = 2 * time.Minute

// DefaultReserveAfter is how long the budget has to have been refusing a
// waiter before a gate starts holding capacity for it rather than admitting
// smaller jobs past it.
const DefaultReserveAfter = time.Minute

// runGate is the per-run concurrency gate Engine uses to bound how many
// sandboxes a single run holds at once (Engine.Concurrency). Jobs of the
// same run are admitted in arrival order.
type runGate struct {
	limit   int
	mu      sync.Mutex
	held    int
	joined  uint64
	waiters map[*runWaiter]struct{}
	wake    chan struct{}
}

type runWaiter struct {
	seq uint64
}

func (g *runGate) Acquire(ctx context.Context, req GateRequest) (func(), error) {
	w := g.join()
	defer g.leave(w)
	if req.Adopted {
		g.mu.Lock()
		g.held++
		rel := g.releaser()
		g.mu.Unlock()
		return rel, nil
	}
	waited, last := false, workflows.Wait{}
	for {
		wake := g.subscribe()
		release, why, ok := g.take(w)
		if ok {
			if waited && req.Wait != nil {
				req.Wait(workflows.Wait{})
			}
			return release, nil
		}
		if why != last {
			waited, last = true, why
			if req.Wait != nil {
				req.Wait(why)
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		case <-time.After(DefaultGatePoll):
		}
	}
}

func (g *runGate) join() *runWaiter {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waiters == nil {
		g.waiters = map[*runWaiter]struct{}{}
	}
	g.joined++
	w := &runWaiter{seq: g.joined}
	g.waiters[w] = struct{}{}
	return w
}

func (g *runGate) leave(w *runWaiter) {
	g.mu.Lock()
	_, waiting := g.waiters[w]
	delete(g.waiters, w)
	wake := g.wake
	if waiting {
		g.wake = nil
	}
	g.mu.Unlock()
	if waiting && wake != nil {
		close(wake)
	}
}

func (g *runGate) take(w *runWaiter) (release func(), why workflows.Wait, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held >= g.limit {
		return nil, workflows.Wait{
			Cause: workflows.WaitForRun,
			Reason: "for one of the " + plural(g.limit, "sandbox", "sandboxes") + " this run may hold at once; " +
				plural(g.held, "job of it holds", "jobs of it hold") + " one right now",
		}, false
	}
	ahead := 0
	for o := range g.waiters {
		if o != w && o.seq < w.seq {
			ahead++
		}
	}
	if ahead > 0 {
		return nil, workflows.Wait{
			Cause: workflows.WaitForRun,
			Reason: "for one of the " + plural(g.limit, "sandbox", "sandboxes") + " this run may hold at once; " +
				plural(ahead, "job of it is", "jobs of it are") + " in line ahead of it",
		}, false
	}
	g.held++
	return g.releaser(), workflows.Wait{}, true
}

func (g *runGate) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.held--
			wake := g.wake
			g.wake = nil
			g.mu.Unlock()
			if wake != nil {
				close(wake)
			}
		})
	}
}

func (g *runGate) subscribe() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.wake == nil {
		g.wake = make(chan struct{})
	}
	return g.wake
}

func plural(n int, one, many string) string {
	word := many
	if n == 1 {
		word = one
	}
	return strconv.Itoa(n) + " " + word
}
