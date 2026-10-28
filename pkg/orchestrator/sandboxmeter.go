package orchestrator

// Counting the sandboxes this deployment builds, on the way past
// (grain/task-315).
//
// The counters themselves are rows (model.SandboxUsage, and its package
// comment for what they mean and why they are durable). This is the
// other half: the one place that says *when* a sandbox is counted, so
// that an agent run's sandbox and a CI job's are counted alike by two
// callers that otherwise have nothing to do with each other -- RunCycle
// here, and cmd/grain's CI sandbox adapter, which wraps the same backend.
//
// It is deliberately not a Sandboxes decorator, which is the shape this
// wants to be. A sandbox is reached through half a dozen optional
// interfaces (rootedSandbox, SandboxPlacer, vmNamedSandbox,
// SandboxShell), each found by type assertion, and a wrapper that
// forgets to forward one of them does not fail to compile -- it silently
// takes a capability away from every run, which is exactly how
// bwsalmon/agents#643 left credential placement broken on one backend.
// So the backend's own value is handed back untouched and the meter is a
// pair of calls the caller makes: acquire through Acquire, and call the
// func it returns where the sandbox is released.

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// MeterFor builds a meter counting into a store, or one that counts
// nothing where there is no store to count into.
//
// It exists so that both call sites get the typed-nil rule right in one
// place: a nil *model.Store assigned to SandboxUsageRecorder is an
// interface that is not nil and panics on the first count, which is the
// one way a meter could take a deployment down.
func MeterFor(store *model.Store) SandboxMeter {
	if store == nil {
		return SandboxMeter{}
	}
	return SandboxMeter{Usage: store}
}

// SandboxUsageRecorder is the durable counter a sandbox's use is added
// to -- *model.Store's own RecordSandboxStart/RecordSandboxFinish, and
// an interface here so that this package's tests can count into
// something cheaper.
//
// Both methods take the moment rather than reading a clock, so a test
// can state a sandbox's life exactly and so that the two halves of one
// sandbox's measurement come from one clock.
type SandboxUsageRecorder interface {
	RecordSandboxStart(ctx context.Context, kind string, created bool, at time.Time) error
	RecordSandboxFinish(ctx context.Context, kind string, lived time.Duration, at time.Time) error
}

// SandboxMeter counts what it acquires, into Usage.
//
// The zero value counts nothing and acquires normally, which is what a
// caller with no store behind it gets: measurement must never be the
// reason a run cannot have a sandbox. Every failure to record is logged
// and swallowed for the same reason -- a deployment that cannot write a
// counter is still a deployment that should dispatch.
type SandboxMeter struct {
	// Usage is where the counts go. nil counts nothing.
	Usage SandboxUsageRecorder
	// Now is the clock both halves of a measurement are taken from. nil
	// is time.Now.
	Now func() time.Time
	// Log is where a failed count is reported. nil is log.Printf.
	Log func(format string, args ...any)
}

// Acquire builds a sandbox through sandboxes and counts it, handing back
// the sandbox the backend made -- the backend's own value, not a wrapper
// (see this file's own note on why).
//
// kind is model.SandboxKindAgent or model.SandboxKindCI, passed by the
// caller rather than guessed from the name: both call sites know which
// of the two they are, and a rule that read it back off the name would
// be a second place for CI's naming convention to be encoded.
//
// The second return is the sandbox's life, and must be called where the
// sandbox is released -- once; further calls do nothing. A sandbox that
// is never released, because the process holding it stopped, is counted
// as created and never as finished, which is the undercount
// model.SandboxUsage.Unaccounted names.
//
// A failed acquire is counted too, as attempted and not created: it
// spent whatever the backend spent before giving up, and dropping it
// would make a deployment whose sandboxes keep failing to start look
// cheaper than one whose sandboxes work.
func (m SandboxMeter) Acquire(ctx context.Context, sandboxes Sandboxes, kind, name string, shape Shape) (Sandbox, func(context.Context), error) {
	started := m.now()
	sandbox, err := sandboxes.Acquire(ctx, name, shape)
	// Except a pod the cluster-fit check held back before creating it
	// (admitPod): nothing was spent on that one, and a CI job waiting for
	// room asks again every few seconds.
	if heldBack(err) {
		return nil, func(context.Context) {}, err
	}
	// Counted on a context detached from ctx's cancellation: an acquire
	// cut short by it -- a daemon shutting down, a task closed while its
	// VM booted -- is one of the failed acquires the paragraph above says
	// are counted, and a write on the already-cancelled ctx would fail and
	// drop it. The release half counts on whatever ctx its caller passes,
	// which runOne detaches for the same reason.
	m.record("starting", func(rec SandboxUsageRecorder) error {
		return rec.RecordSandboxStart(context.WithoutCancel(ctx), kind, err == nil, started)
	})
	if err != nil {
		return nil, func(context.Context) {}, err
	}
	var once sync.Once
	return sandbox, func(releaseCtx context.Context) {
		once.Do(func() {
			ended := m.now()
			m.record("finishing", func(rec SandboxUsageRecorder) error {
				return rec.RecordSandboxFinish(releaseCtx, kind, ended.Sub(started), ended)
			})
		})
	}, nil
}

// Serve builds a serving sandbox through serving and counts it, handing
// back the ServingSandbox the backend made and the release func that
// records its finish.
func (m SandboxMeter) Serve(ctx context.Context, serving ServingSandboxes, kind string, spec ServeSpec) (ServingSandbox, func(context.Context), error) {
	started := m.now()
	sandbox, err := serving.Serve(ctx, spec)
	m.record("starting", func(rec SandboxUsageRecorder) error {
		return rec.RecordSandboxStart(context.WithoutCancel(ctx), kind, err == nil, started)
	})
	if err != nil {
		return nil, func(context.Context) {}, err
	}
	var once sync.Once
	return sandbox, func(releaseCtx context.Context) {
		once.Do(func() {
			ended := m.now()
			m.record("finishing", func(rec SandboxUsageRecorder) error {
				return rec.RecordSandboxFinish(releaseCtx, kind, ended.Sub(started), ended)
			})
		})
	}, nil
}

func (m SandboxMeter) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}

// record runs one count against Usage, if there is one, and says so in
// the log if it fails. What it cannot do is return the error: nothing
// upstream of a counter should change its mind about a run because the
// counting went wrong.
func (m SandboxMeter) record(what string, fn func(SandboxUsageRecorder) error) {
	if m.Usage == nil {
		return
	}
	if err := fn(m.Usage); err != nil {
		logf := m.Log
		if logf == nil {
			logf = log.Printf
		}
		logf("orchestrator: counting a %s sandbox: %v", what, err)
	}
}
