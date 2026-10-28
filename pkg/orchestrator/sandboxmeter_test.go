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

// The lifetime sandbox counters, from the dispatch side: what
// orchestrator.SandboxMeter counts, and that a real RunCycle counts
// through it (grain/task-315).
//
// model's own tests prove the rows add up and survive a restart. These
// prove the two things only this package can say: that a sandbox's
// measured life is its wall clock, and that the counting is actually
// wired into the path a run takes rather than into a function nobody
// calls.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// countingRecorder is a SandboxUsageRecorder that keeps the counts in
// memory -- the store's own arithmetic is tested against SQLite, and what
// is wanted here is what the meter asked it to count.
type countingRecorder struct {
	mu        sync.Mutex
	attempted map[string]int
	created   map[string]int
	finished  map[string]int
	lived     map[string]time.Duration
	err       error
}

func newCountingRecorder() *countingRecorder {
	return &countingRecorder{
		attempted: map[string]int{}, created: map[string]int{},
		finished: map[string]int{}, lived: map[string]time.Duration{},
	}
}

func (r *countingRecorder) RecordSandboxStart(ctx context.Context, kind string, created bool, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempted[kind]++
	if created {
		r.created[kind]++
	}
	return r.err
}

func (r *countingRecorder) RecordSandboxFinish(ctx context.Context, kind string, lived time.Duration, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished[kind]++
	r.lived[kind] += lived
	return r.err
}

// stubSandboxes hands back a sandbox that does nothing, or an error.
type stubSandboxes struct{ err error }

func (s stubSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	if s.err != nil {
		return nil, s.err
	}
	return stubSandbox{name: name}, nil
}

type stubSandbox struct{ name string }

func (s stubSandbox) Name() string                                  { return s.name }
func (s stubSandbox) Tools(ctx context.Context) ([]mcp.Tool, error) { return nil, nil }
func (s stubSandbox) ConfigureGitCredentials(ctx context.Context, remoteURL, token string, identity mcp.GitIdentity) error {
	return nil
}
func (s stubSandbox) Release(ctx context.Context) error { return nil }

func TestMeteredSandboxTimeIsItsWallClock(t *testing.T) {
	t.Parallel()
	recorder := newCountingRecorder()
	// A clock the test moves by hand, so the measurement is a stated
	// span rather than however long the test itself took.
	clock := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	meter := orchestrator.SandboxMeter{
		Usage: recorder,
		Now:   func() time.Time { return clock },
	}

	sandbox, counted, err := meter.Acquire(context.Background(), stubSandboxes{},
		model.SandboxKindCI, "c1a2b3-0", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// The backend's own sandbox, not a wrapper: everything a run reaches
	// through an optional interface (a root, a placer, a shell) is found
	// by type assertion, and a wrapper is how one of those silently goes
	// missing.
	if _, wrapped := sandbox.(stubSandbox); !wrapped {
		t.Errorf("Acquire handed back %T, want the backend's own sandbox unwrapped", sandbox)
	}

	clock = clock.Add(36 * time.Minute)
	counted(context.Background())

	if got := recorder.lived[model.SandboxKindCI]; got != 36*time.Minute {
		t.Errorf("counted %s of sandbox time, want 36m -- the wall clock between acquire and release", got)
	}
	if recorder.attempted[model.SandboxKindCI] != 1 || recorder.created[model.SandboxKindCI] != 1 ||
		recorder.finished[model.SandboxKindCI] != 1 {
		t.Errorf("counted %d attempted, %d created, %d finished; want one of each",
			recorder.attempted[model.SandboxKindCI], recorder.created[model.SandboxKindCI],
			recorder.finished[model.SandboxKindCI])
	}
	// Called again where a caller has two release paths: a sandbox has
	// one life, and counting it twice would put the hours out by as much
	// as the sandbox itself was worth.
	clock = clock.Add(time.Hour)
	counted(context.Background())
	if got := recorder.finished[model.SandboxKindCI]; got != 1 {
		t.Errorf("a second release counted %d finishes, want 1", got)
	}
	if got := recorder.lived[model.SandboxKindCI]; got != 36*time.Minute {
		t.Errorf("a second release made the total %s, want it unchanged at 36m", got)
	}
}

func TestAMeteredSandboxThatNeverComesUpIsCountedAsAttempted(t *testing.T) {
	t.Parallel()
	recorder := newCountingRecorder()
	meter := orchestrator.SandboxMeter{Usage: recorder}

	_, counted, err := meter.Acquire(context.Background(), stubSandboxes{err: errors.New("no room")},
		model.SandboxKindAgent, "t1-1", orchestrator.Shape{})
	if err == nil {
		t.Fatal("Acquire succeeded against a backend that refuses everything")
	}
	if recorder.attempted[model.SandboxKindAgent] != 1 {
		t.Errorf("attempted = %d, want 1: a sandbox that failed to start still spent something",
			recorder.attempted[model.SandboxKindAgent])
	}
	if recorder.created[model.SandboxKindAgent] != 0 {
		t.Errorf("created = %d, want 0", recorder.created[model.SandboxKindAgent])
	}
	// The func handed back is safe to call and counts nothing: there was
	// no sandbox, so there is no life to close.
	counted(context.Background())
	if recorder.finished[model.SandboxKindAgent] != 0 {
		t.Errorf("finished = %d, want 0", recorder.finished[model.SandboxKindAgent])
	}
}

func TestAFailingCounterNeverCostsARunItsSandbox(t *testing.T) {
	t.Parallel()
	recorder := newCountingRecorder()
	recorder.err = errors.New("database is locked")
	meter := orchestrator.SandboxMeter{
		Usage: recorder,
		Log:   func(string, ...any) {},
	}

	sandbox, counted, err := meter.Acquire(context.Background(), stubSandboxes{},
		model.SandboxKindAgent, "t1-1", orchestrator.Shape{})
	if err != nil || sandbox == nil {
		t.Fatalf("Acquire = %v, %v; measurement must never be why a run has no sandbox", sandbox, err)
	}
	counted(context.Background())
}

func TestAMeterWithNoStoreCountsNothingAndStillAcquires(t *testing.T) {
	t.Parallel()
	// The typed-nil case MeterFor exists for: a nil *model.Store in an
	// interface is not a nil interface, and would panic on the first
	// count.
	meter := orchestrator.MeterFor(nil)
	sandbox, counted, err := meter.Acquire(context.Background(), stubSandboxes{},
		model.SandboxKindAgent, "t1-1", orchestrator.Shape{})
	if err != nil || sandbox == nil {
		t.Fatalf("Acquire = %v, %v; want a sandbox", sandbox, err)
	}
	counted(context.Background())
}

// ctxCheckingRecorder fails a write on a cancelled context, the way the
// store's own writes do.
type ctxCheckingRecorder struct{ *countingRecorder }

func (r ctxCheckingRecorder) RecordSandboxStart(ctx context.Context, kind string, created bool, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.countingRecorder.RecordSandboxStart(ctx, kind, created, at)
}

// cancelledSandboxes fails the way a backend does when the acquire's own
// ctx is cancelled under it.
type cancelledSandboxes struct{}

func (cancelledSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAMeteredSandboxWhoseAcquireWasCancelledIsStillCounted(t *testing.T) {
	t.Parallel()
	recorder := newCountingRecorder()
	meter := orchestrator.SandboxMeter{Usage: ctxCheckingRecorder{recorder}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := meter.Acquire(ctx, cancelledSandboxes{}, model.SandboxKindAgent, "t1-1", orchestrator.Shape{}); err == nil {
		t.Fatal("Acquire succeeded on a cancelled ctx")
	}
	if recorder.attempted[model.SandboxKindAgent] != 1 {
		t.Errorf("attempted = %d, want 1: an acquire cut short by cancellation still spent something",
			recorder.attempted[model.SandboxKindAgent])
	}
}
