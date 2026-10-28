package postgres_test

// The lifetime sandbox counters, against a real database
// (model.SandboxUsage, grain/task-315).
//
// Every claim here is about a counter rather than about a report, and
// the one that matters most is the first: the whole reason these rows
// exist rather than a few integers on the daemon is that the daemon they
// would have lived on restarted 52 times in eight minutes on the day
// this was asked for. A counter that cannot survive that answers "how
// many sandboxes have we used" with "however many since the last crash",
// which is the answer that was already available and already useless.

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
)

// openStoreIn is openStore against a directory the caller owns, so a
// test can close the store and open a second one over the same database
// -- which is what a daemon restart is, as far as these rows are
// concerned.
func openStoreIn(t *testing.T, dir string) (*model.Store, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store, ctx
}

func usageOf(t *testing.T, store *model.Store, ctx context.Context, kind string) model.SandboxUsage {
	t.Helper()
	usages, err := store.SandboxUsages(ctx)
	if err != nil {
		t.Fatalf("SandboxUsages: %v", err)
	}
	for _, u := range usages {
		if u.Kind == kind {
			return u
		}
	}
	t.Fatalf("no %s row among %+v", kind, usages)
	return model.SandboxUsage{}
}

func TestSandboxCountersSurviveARestartAndCarryOn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, ctx := openStoreIn(t, dir)

	// Two sandboxes counted by the process that started them, both
	// finished.
	for i := 0; i < 2; i++ {
		at := now.Add(time.Duration(i) * time.Hour)
		if err := store.RecordSandboxStart(ctx, model.SandboxKindAgent, true, at); err != nil {
			t.Fatalf("counting a sandbox: %v", err)
		}
		if err := store.RecordSandboxFinish(ctx, model.SandboxKindAgent, 30*time.Minute, at.Add(30*time.Minute)); err != nil {
			t.Fatalf("counting a finished sandbox: %v", err)
		}
	}

	// The daemon stops and another one starts over the same database.
	restarted, ctx := openStoreIn(t, dir)
	if err := restarted.RecordSandboxStart(ctx, model.SandboxKindAgent, true, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("counting a sandbox after the restart: %v", err)
	}
	if err := restarted.RecordSandboxFinish(ctx, model.SandboxKindAgent, time.Hour, now.Add(3*time.Hour)); err != nil {
		t.Fatalf("counting a finished sandbox after the restart: %v", err)
	}

	got := usageOf(t, restarted, ctx, model.SandboxKindAgent)
	if got.Attempted != 3 || got.Created != 3 || got.Finished != 3 {
		t.Errorf("after a restart: attempted=%d created=%d finished=%d, want 3/3/3 -- "+
			"a counter that starts again at zero is the thing these rows exist to prevent",
			got.Attempted, got.Created, got.Finished)
	}
	if want := 2 * time.Hour; got.Duration != want {
		t.Errorf("sandbox time after a restart = %s, want %s (the two half-hours before it plus the hour after)",
			got.Duration, want)
	}
	// And the date the figures are "since" is the first process's, not
	// the second's: a deployment that restarted this morning has not
	// started counting this morning.
	if !got.Since.Equal(now) {
		t.Errorf("since = %s, want %s -- a restart must not move the date the counters are quoted from",
			got.Since, now)
	}
}

func TestASandboxThatNeverStartsIsCountedOnce(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	// One that failed to come up, then the retry that worked. The retry
	// is a second sandbox and is counted as one; the failure is not
	// dropped, and it is not counted twice because it was retried.
	if err := store.RecordSandboxStart(ctx, model.SandboxKindCI, false, now); err != nil {
		t.Fatalf("counting a failed sandbox: %v", err)
	}
	if err := store.RecordSandboxStart(ctx, model.SandboxKindCI, true, now.Add(time.Minute)); err != nil {
		t.Fatalf("counting the retry: %v", err)
	}
	if err := store.RecordSandboxFinish(ctx, model.SandboxKindCI, 4*time.Minute, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("counting the retry's end: %v", err)
	}

	got := usageOf(t, store, ctx, model.SandboxKindCI)
	if got.Attempted != 2 {
		t.Errorf("attempted = %d, want 2 (the one that failed and the one that worked)", got.Attempted)
	}
	if got.Created != 1 {
		t.Errorf("created = %d, want 1: a sandbox that never came up is not a sandbox that ran", got.Created)
	}
	if got.Failed() != 1 {
		t.Errorf("failed = %d, want 1", got.Failed())
	}
	if got.Duration != 4*time.Minute {
		t.Errorf("duration = %s, want 4m: nothing that failed to start may contribute time", got.Duration)
	}
	if got.Unaccounted() != 0 {
		t.Errorf("unaccounted = %d, want 0: every sandbox that came up here was seen to stop", got.Unaccounted())
	}
}

func TestSandboxTimeIsPerKindAndAddsUp(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	// An agent run of 97 minutes, and two CI jobs -- one long, one short
	// -- which is the comparison the hours exist to make: the same count
	// of sandboxes, very different machines.
	if err := store.RecordSandboxStart(ctx, model.SandboxKindAgent, true, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSandboxFinish(ctx, model.SandboxKindAgent, 97*time.Minute, now.Add(97*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, lived := range []time.Duration{36 * time.Minute, 40 * time.Second} {
		if err := store.RecordSandboxStart(ctx, model.SandboxKindCI, true, now); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordSandboxFinish(ctx, model.SandboxKindCI, lived, now.Add(lived)); err != nil {
			t.Fatal(err)
		}
	}

	usages, err := store.SandboxUsages(ctx)
	if err != nil {
		t.Fatalf("SandboxUsages: %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("read %d kinds, want 2", len(usages))
	}
	// Kind order, so two readings of one deployment read the same way.
	if usages[0].Kind != model.SandboxKindAgent || usages[1].Kind != model.SandboxKindCI {
		t.Fatalf("kinds came back as %q, %q; want agent then ci", usages[0].Kind, usages[1].Kind)
	}
	if want := 97 * time.Minute; usages[0].Duration != want {
		t.Errorf("agent time = %s, want %s", usages[0].Duration, want)
	}
	if want := 36*time.Minute + 40*time.Second; usages[1].Duration != want {
		t.Errorf("CI time = %s, want %s", usages[1].Duration, want)
	}
	if hours := usages[0].Hours(); hours < 1.61 || hours > 1.62 {
		t.Errorf("agent hours = %v, want about 1.617 (97 minutes)", hours)
	}
}

func TestCountingAnUnknownSandboxKindIsRefused(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	// A typo at a call site has to be an error on that write rather than
	// a third row nobody adds up.
	if err := store.RecordSandboxStart(ctx, "agents", true, now); err == nil {
		t.Error("counting a sandbox of an unknown kind was accepted")
	}
	if err := store.RecordSandboxFinish(ctx, "", time.Minute, now); err == nil {
		t.Error("finishing a sandbox of an unknown kind was accepted")
	}
	usages, err := store.SandboxUsages(ctx)
	if err != nil {
		t.Fatalf("SandboxUsages: %v", err)
	}
	if len(usages) != 0 {
		t.Errorf("refused writes still left %+v behind", usages)
	}
}

func TestASandboxNeverSeenToStopIsReadableRatherThanMissing(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	// Two came up; one was released and one was not -- the shape a crash
	// or an upgrade shutdown leaves behind. The second's time is missing
	// from the total, and the size of that gap is exactly what
	// Unaccounted says.
	for i := 0; i < 2; i++ {
		if err := store.RecordSandboxStart(ctx, model.SandboxKindAgent, true, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordSandboxFinish(ctx, model.SandboxKindAgent, 20*time.Minute, now.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}

	got := usageOf(t, store, ctx, model.SandboxKindAgent)
	if got.Unaccounted() != 1 {
		t.Errorf("unaccounted = %d, want 1", got.Unaccounted())
	}
	if got.Duration != 20*time.Minute {
		t.Errorf("duration = %s, want 20m -- nothing may be guessed for the sandbox nobody saw stop", got.Duration)
	}
}
