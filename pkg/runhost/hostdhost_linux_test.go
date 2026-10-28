//go:build linux

package runhost

import (
	"bytes"
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

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/hostd"
)

// serveHostd starts a grain hostd whose allowlist offers sh as a cli.
func serveHostd(t *testing.T) hostd.Client {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)
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

// cursors records what OnCursor was told, per run.
type cursors struct {
	mu   sync.Mutex
	last map[string]int64
}

func (c *cursors) on(_ context.Context, run string, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[string]int64{}
	}
	c.last[run] = n
}

func (c *cursors) get(run string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.last[run]
	return n, ok
}

// lockedBuf is a bytes.Buffer safe to read while it is written.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func steadyLines(n int) (script, want string) {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	return fmt.Sprintf(`i=0; while [ $i -lt %d ]; do echo line$i; i=$((i+1)); sleep 0.05; done`, n), b.String()
}

// A run's host CLI starts detached with the run's id, task and deadline;
// an upgrade's Flush lets go of it with its cursor persisted; and the
// next daemon's Attach from that cursor finishes the transcript with
// nothing lost and nothing repeated, and returns the whole stdout.
func TestHostdRunIsFlushedAndReattachedFromItsCursor(t *testing.T) {
	client := serveHostd(t)
	var first cursors
	d := &Hostd{Client: client, OnCursor: first.on, CursorEvery: time.Millisecond}
	script, want := steadyLines(40)
	budget := time.Now().Add(time.Hour)
	ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-1", Deadline: budget})
	var tee lockedBuf
	done := make(chan error, 1)
	go func() {
		_, err := d.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", script}}, &tee)
		done <- err
	}()
	for !strings.Contains(tee.String(), "line5\n") {
		time.Sleep(10 * time.Millisecond)
	}
	runs, err := d.Flush(context.Background())
	if err != nil || strings.Join(runs, ",") != "run-1" {
		t.Fatalf("Flush = %v, %v", runs, err)
	}
	if err := <-done; !errors.Is(err, agent.ErrDetached) {
		t.Fatalf("Run after the flush = %v, want ErrDetached", err)
	}
	cursor, ok := first.get("run-1")
	if !ok || cursor != int64(len(tee.String())) {
		t.Fatalf("persisted cursor %d (%v), want what was teed: %d", cursor, ok, len(tee.String()))
	}

	procs, err := client.Processes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	infos := HostdInfos(procs, hostd.ThisHello("test"))
	if len(infos) != 1 || infos[0].Labels.Run != "run-1" || infos[0].Labels.Task != "42" || !infos[0].Running {
		t.Fatalf("listing after the flush = %+v", infos)
	}
	if got, want := procs[0].Deadline, budget.Add(DefaultAllowance); got.Sub(want).Abs() > time.Second {
		t.Fatalf("process deadline %v, want the budget plus the allowance %v", got, want)
	}

	var second cursors
	next := &Hostd{Client: client, OnCursor: second.on}
	var rest bytes.Buffer
	whole, err := next.Attach(context.Background(), infos[0], cursor, &rest)
	if err != nil {
		t.Fatal(err)
	}
	if tee.String()+rest.String() != want {
		t.Fatalf("transcript across the restart =\n%q\nwant\n%q", tee.String()+rest.String(), want)
	}
	if whole != want {
		t.Fatalf("whole stdout = %q", whole)
	}
	if n, _ := second.get("run-1"); n != int64(len(want)) {
		t.Fatalf("final cursor %d, want %d", n, len(want))
	}
}

// A draining hostd's refusal reaches the framework as ErrHostdDraining,
// and a call that is not a run is a plain start.
func TestHostdDrainingRefusalAndPlainStarts(t *testing.T) {
	client := serveHostd(t)
	d := &Hostd{Client: client}
	host := d.Host(agent.LocalHost{}, "sh")
	out, err := host.Run(context.Background(), agent.Command{Args: []string{"-c", "echo probe"}}, nil)
	if err != nil || out != "probe\n" {
		t.Fatalf("plain start = %q, %v", out, err)
	}
	// A process still running holds the drain open: with none, hostd
	// would exit at once and the start below would find no socket.
	holding := make(chan hostd.Started, 1)
	holder := client
	holder.Detach = func(context.Context) bool { return true }
	holder.OnStarted = func(st hostd.Started) { holding <- st }
	hctx, stopHolder := context.WithCancel(context.Background())
	go func() {
		_, _ = holder.Run(hctx, hostd.Request{Kind: hostd.KindCLI, Name: "sh", Args: []string{"-c", "sleep 30"},
			Detach: true, Run: "holder", Deadline: time.Now().Add(time.Minute)}, nil, nil, nil)
	}()
	held := <-holding
	t.Cleanup(func() {
		stopHolder()
		_ = client.Cancel(context.Background(), held.ID)
	})
	if err := client.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-2"})
	_, err = host.Run(ctx, agent.Command{Args: []string{"-c", "echo no"}}, nil)
	if !errors.Is(err, agent.ErrHostdDraining) {
		t.Fatalf("start on a draining hostd = %v, want ErrHostdDraining", err)
	}
}
