package ci

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// testClock is a clock a test moves by hand, read from whichever goroutine
// the engine happens to be on.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// streamingSandboxes is a backend whose steps print in pieces, so a test
// can watch a step that has not finished.
type streamingSandboxes struct {
	chunks []string
	// between is called before each chunk -- where a test moves its clock
	// past LiveOutputInterval so the next chunk is published.
	between func()
}

func (s *streamingSandboxes) Acquire(context.Context, SandboxRequest) (Sandbox, error) {
	return &streamingSandbox{owner: s}, nil
}

type streamingSandbox struct{ owner *streamingSandboxes }

func (s *streamingSandbox) Name() string { return "sandbox-1" }

func (s *streamingSandbox) Exec(ctx context.Context, cmd Command) (CommandResult, error) {
	return s.ExecStream(ctx, cmd, nil)
}

func (s *streamingSandbox) ExecStream(_ context.Context, _ Command, out func(string)) (CommandResult, error) {
	var whole strings.Builder
	for _, chunk := range s.owner.chunks {
		if s.owner.between != nil {
			s.owner.between()
		}
		whole.WriteString(chunk)
		if out != nil {
			out(chunk)
		}
	}
	return CommandResult{Output: whole.String()}, nil
}

func (s *streamingSandbox) Release(context.Context) error { return nil }

func TestEngineReportsWhatARunningStepHasPrintedSoFar(t *testing.T) {
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	sandboxes := &streamingSandboxes{
		chunks:  []string{"compiling\n", "testing\n", "done\n"},
		between: func() { clock.advance(LiveOutputInterval + time.Second) },
	}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - name: build\n        run: make\n",
	})

	var live []string
	run := (&Engine{Sandboxes: sandboxes, Now: clock.Now}).Run(context.Background(), "r1", plan, func(job JobRun) {
		if len(job.Steps) == 0 {
			return
		}
		step := job.Steps[len(job.Steps)-1]
		if step.Conclusion == "" && step.Output != "" {
			live = append(live, step.Output)
		}
	})
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q (%s)", run.Conclusion, run.Error)
	}
	if len(live) == 0 {
		t.Fatalf("a step that is still running should report what it has printed: got no live snapshot")
	}
	// Each publication is everything printed so far, so the last live one
	// is a prefix of the step's finished output rather than a fragment of
	// its own.
	final := jobByID(t, run, "one").Steps[0].Output
	if final != "compiling\ntesting\ndone\n" {
		t.Fatalf("the finished step still carries its whole output: got %q", final)
	}
	if last := live[len(live)-1]; !strings.HasPrefix(final, last) {
		t.Errorf("live output should grow into the final output: last live %q, final %q", last, final)
	}
	if !strings.Contains(live[0], "compiling") {
		t.Errorf("the first live snapshot should carry the step's first lines: got %q", live[0])
	}
}

func TestEngineDoesNotPublishARunningStepFasterThanItsInterval(t *testing.T) {
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	// A step that prints a hundred times without the clock moving: a
	// publication is a store write, so what a chatty step must not do is
	// turn one job into a hundred of them.
	chunks := make([]string, 100)
	for i := range chunks {
		chunks[i] = "line\n"
	}
	sandboxes := &streamingSandboxes{chunks: chunks}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: noisy\n",
	})

	published := 0
	(&Engine{Sandboxes: sandboxes, Now: clock.Now}).Run(context.Background(), "r1", plan, func(job JobRun) {
		if len(job.Steps) > 0 && job.Steps[len(job.Steps)-1].Output != "" && job.Steps[len(job.Steps)-1].Conclusion == "" {
			published++
		}
	})
	if published != 0 {
		t.Errorf("no interval passed, so nothing should have been published: got %d snapshots", published)
	}
}

func TestEngineStillRunsASandboxThatCannotStream(t *testing.T) {
	// The fallback the optional interface exists for: a backend with no
	// ExecStream reports its step once, when it is over, exactly as it did
	// before live output existed.
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		return CommandResult{Output: "ran " + cmd.Script}, nil
	}}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: echo hi\n",
	})

	live := 0
	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, func(job JobRun) {
		if len(job.Steps) > 0 && job.Steps[len(job.Steps)-1].Conclusion == "" && job.Steps[len(job.Steps)-1].Output != "" {
			live++
		}
	})
	if run.Conclusion != ConclusionSuccess || live != 0 {
		t.Errorf("a non-streaming backend still works and reports nothing live: %q, %d live snapshots", run.Conclusion, live)
	}
	if got := jobByID(t, run, "one").Steps[0].Output; got != "ran echo hi" {
		t.Errorf("step output: got %q", got)
	}
}
