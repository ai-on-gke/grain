package sqlite_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// TestCurrentEnvironmentFailure is the host-wide reading of expired corp
// credentials: nothing before any run ends that way, one finding counting
// the whole spell while it lasts -- across tasks, since it is the host's
// and not theirs -- and nothing again once any run ends another way.
func TestCurrentEnvironmentFailure(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for _, id := range []string{"a1b2", "c3d4"} {
		if err := store.PutTask(ctx, task(id, true)); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	finish := func(taskID, outcome, detail string) time.Time {
		t.Helper()
		n++
		id := fmt.Sprintf("r%d", n)
		at := now.Add(time.Duration(n) * time.Hour)
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: taskID, Sandbox: "s1", Attempt: n, StartedAt: at,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, at.Add(time.Minute), outcome, detail); err != nil {
			t.Fatal(err)
		}
		return at.Add(time.Minute)
	}
	read := func() *model.EnvironmentFailure {
		t.Helper()
		f, err := store.CurrentEnvironmentFailure(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	if f := read(); f != nil {
		t.Fatalf("before any run: %+v, want nil", f)
	}
	finish("a1b2", "failed", "exit status 1")
	if f := read(); f != nil {
		t.Fatalf("after an ordinary failure: %+v, want nil", f)
	}
	const expired = "host credentials expired: run `renew-host-creds` as alice on build-1.example.test, then retry"
	first := finish("a1b2", model.EnvironmentOutcome, expired)
	last := finish("c3d4", model.EnvironmentOutcome, expired)
	f := read()
	if f == nil {
		t.Fatal("during the spell: nil, want a finding")
	}
	if f.Runs != 2 || f.Detail != expired || f.TaskID != "c3d4" ||
		!f.Since.Equal(first) || !f.Last.Equal(last) {
		t.Errorf("during the spell: %+v, want 2 runs from %s to %s, last on c3d4", f, first, last)
	}
	finish("a1b2", "succeeded", "")
	if f := read(); f != nil {
		t.Errorf("after renewing, a run that got past its tools: %+v, want nil", f)
	}
}
