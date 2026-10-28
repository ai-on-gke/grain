//go:build linux

package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// serveShHostd starts a grain hostd whose allowlist offers sh as a cli.
func serveShHostd(t *testing.T) hostd.Client {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)
	// Not t.TempDir: hostd's socket path has to fit in a sockaddr_un.
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	if err := os.WriteFile(allow, []byte(fmt.Sprintf("clis:\n  sh:\n    path: %s\n", sh)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Socket:          filepath.Join(dir, "run", "hostd.sock"),
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		DisconnectGrace: 500 * time.Millisecond,
		Env:             []string{"PATH=" + os.Getenv("PATH")},
		Logf:            t.Logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	c := hostd.Client{Socket: srv.Socket, Build: "test"}
	for i := 0; ; i++ {
		if _, err := c.Hello(context.Background()); err == nil {
			return c
		} else if i > 100 {
			t.Fatalf("hostd never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// syncBuf is a strings.Builder safe to read while it is written.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The acceptance test's daemon half for a run driven through grain hostd
// (the container install): an upgrade flushes the run part-way through
// its second attempt, and the next daemon adopts the process from the
// persisted, non-zero cursor. A hostd spool is not on the daemon's disk,
// so the earlier attempt is split off the live transcript by the stream
// Attach returns: the framework is given that attempt, the whole of the
// one in flight, and the transcript has no hole and no duplicate.
func TestResumeAdoptedHostdRunKeepsItsEarlierAttempts(t *testing.T) {
	t.Parallel()
	client := serveShHostd(t)
	store, ctx := openStore(t)
	_, gh := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t", model.RepoRef{Owner: "acme", Name: "widgets"})
	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t-1", Attempt: 1}
	startRun(t, ctx, store, d, time.Now().UTC().Add(-time.Minute))

	sandboxDir := t.TempDir()
	if _, err := orchestrator.NewHostSandboxes(sandboxDir).Acquire(ctx, d.RunID, orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	// The previous daemon: the attempt in flight writes a line, an
	// upgrade flushes it, and it goes on running in hostd to write the
	// rest. (hostd's allowlist takes no path in an argument, so the wait
	// is a sleep rather than a file to watch for.)
	cursors := map[string]int64{}
	var mu sync.Mutex
	previous := &runhost.Hostd{Client: client, CursorEvery: time.Millisecond,
		OnCursor: func(ctx context.Context, run string, n int64) {
			mu.Lock()
			cursors[run] = n
			mu.Unlock()
			_ = store.SetHostdCursor(ctx, run, n, time.Now().UTC())
		}}
	var tee syncBuf
	runCtx := runhost.WithRun(ctx, runhost.Run{Task: task.ID, Run: d.RunID, Deadline: time.Now().Add(time.Hour)})
	detached := make(chan error, 1)
	go func() {
		script := `echo line1; sleep 2; echo line2`
		_, err := previous.Host(agent.LocalHost{}, "sh").Run(runCtx, agent.Command{Args: []string{"-c", script}}, &tee)
		detached <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); tee.String() != "line1\n"; {
		if time.Now().After(deadline) {
			t.Fatalf("the CLI never wrote its first line: %q", tee.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := previous.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-detached; !errors.Is(err, agent.ErrDetached) {
		t.Fatalf("Run after the flush = %v, want ErrDetached", err)
	}
	mu.Lock()
	cursor := cursors[d.RunID]
	mu.Unlock()
	if cursor != int64(len("line1\n")) {
		t.Fatalf("persisted cursor %d, want the line already teed", cursor)
	}
	// The live transcript: an attempt the run retried, then what the
	// previous daemon teed of this one.
	transcripts := t.TempDir()
	if err := os.WriteFile(filepath.Join(transcripts, d.RunID), []byte("attempt1\n"+tee.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// The next daemon adopts it.
	sandboxes := orchestrator.NewHostSandboxes(sandboxDir)
	var adopted []orchestrator.AdoptedRun
	if _, err := orchestrator.RecoverHostd(ctx, store, gh, client, "test", time.Now().UTC(),
		func(a runhost.Adoption, c int64) bool {
			sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, a.Claim.Run)
			if err != nil {
				t.Errorf("readopting the sandbox: %v", err)
				return false
			}
			adopted = append(adopted, orchestrator.AdoptedRun{Adoption: a, Cursor: c, Sandbox: sb})
			return true
		}); err != nil {
		t.Fatal(err)
	}
	if len(adopted) != 1 || adopted[0].Cursor != cursor {
		t.Fatalf("adopted %+v, want run t-1 at cursor %d", adopted, cursor)
	}

	fw := parsingFramework{parsed: make(chan string, 1), earlier: make(chan string, 1), result: toolResult(agent.ToolCall{
		Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"}})}
	deps := orchestrator.Deps{Store: store, Client: gh, Sandboxes: sandboxes,
		Framework: orchestrator.StaticFramework(fw),
		Config:    orchestrator.Config{TranscriptDir: transcripts}}
	var live []byte
	next := &runhost.Hostd{Client: client, CursorEvery: time.Millisecond,
		OnCursor: func(context.Context, string, int64) {
			live, _ = os.ReadFile(filepath.Join(transcripts, d.RunID))
		}}
	if err := orchestrator.ResumeAdoptedRun(ctx, deps, next, adopted[0]); err != nil {
		t.Fatalf("ResumeAdoptedRun: %v", err)
	}
	if got := <-fw.parsed; got != "line1\nline2\n" {
		t.Fatalf("the framework parsed %q, want the attempt's whole stream", got)
	}
	select {
	case got := <-fw.earlier:
		if got != "attempt1\n" {
			t.Fatalf("the framework was given earlier attempts %q, want the retried attempt's stream", got)
		}
	default:
		t.Fatal("the framework was not given the run's earlier attempts")
	}
	if string(live) != "attempt1\nline1\nline2\n" {
		t.Fatalf("live transcript = %q, want the previous daemon's half and the rest, with no hole and no duplicate", live)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil || len(runs) != 1 || runs[0].FinishedAt == nil {
		t.Fatalf("runs = %+v (%v), want the one run finished", runs, err)
	}
	if runs[0].Outcome == model.InterruptedOutcome || runs[0].Outcome == "failed" {
		t.Fatalf("outcome = %q (%s), want the run's own ending", runs[0].Outcome, runs[0].Detail)
	}
}
