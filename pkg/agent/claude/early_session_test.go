package claude

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// stallingRunner writes its first lines the way a live claude does --
// system/init first, carrying the session id -- and then hangs, as a run
// mid-turn does, until its context is cancelled.
type stallingRunner struct {
	lines   string
	stalled chan struct{}
}

func (r *stallingRunner) Run(ctx context.Context, _ []string, _ string, _ []string, tee io.Writer) (string, error) {
	if tee != nil {
		io.WriteString(tee, r.lines)
	}
	close(r.stalled)
	<-ctx.Done()
	return r.lines, ctx.Err()
}

// The session claude names on its first event is reported before the run
// returns -- so a daemon stopped mid-turn, which cancels the run and may
// never get as far as reading its Result, has already been told which
// conversation to resume.
func TestRunReportsItsSessionBeforeItIsCancelled(t *testing.T) {
	r := &stallingRunner{lines: stream(systemInit("sess-early"), assistantText("reading the repo")), stalled: make(chan struct{})}
	f := newFramework(r, "mcpserver-path")

	var mu sync.Mutex
	var reported []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-r.stalled
		mu.Lock()
		got := append([]string(nil), reported...)
		mu.Unlock()
		if len(got) != 1 || got[0] != "sess-early" {
			t.Errorf("reported while the run was still going = %q, want [sess-early]", got)
		}
		cancel()
	}()
	_, err := f.Run(ctx, agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(),
		OnResumableSession: func(s string) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, s)
		},
	})
	if err == nil {
		t.Fatal("Run err = nil, want the cancellation")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 1 || reported[0] != "sess-early" {
		t.Errorf("reported = %q, want exactly [sess-early]: once, as soon as it was named", reported)
	}
}
