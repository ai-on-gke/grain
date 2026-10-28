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

package runhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

func TestLabelsRoundTrip(t *testing.T) {
	want := Labels{Deployment: "grain", Task: "42", Run: "r-1", Generation: "v1.2.3", Protocol: Protocol,
		Deadline: time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC), Spool: "/var/lib/grain/data/runhosts/run-r-1-x"}
	got, err := ParseLabels(want.Map())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip: got %+v, want %+v", got, want)
	}
	if _, err := ParseLabels(map[string]string{"other": "x"}); err == nil {
		t.Fatal("a container with no protocol label parsed as a run host")
	}
	garbled := want.Map()
	garbled[LabelProtocol] = "two"
	got, err = ParseLabels(garbled)
	if err != nil || got.Protocol != 0 || Supported(got.Protocol) {
		t.Fatalf("an unreadable protocol label must list as unsupported protocol 0, got %+v, %v", got, err)
	}
}

func TestSupportedIsNMinusOne(t *testing.T) {
	if !Supported(Protocol) {
		t.Fatal("this build must adopt its own protocol")
	}
	if Supported(Protocol + 1) {
		t.Fatal("a rolled-back build must refuse the next protocol by name, not misread it")
	}
	if Supported(0) {
		t.Fatal("protocol 0 is the unreadable label")
	}
}

func TestSpecRefusesSingleFileMounts(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "n", Image: "i", Command: []string{"grain"}, Labels: Labels{Run: "r", Deployment: "d"}}
	spec.Mounts = []Mount{{Path: dir}}
	if err := spec.Validate(isDir); err != nil {
		t.Fatalf("a directory mount was refused: %v", err)
	}
	spec.Mounts = []Mount{{Path: file}}
	if err := spec.Validate(isDir); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("a single-file mount must be refused, got %v", err)
	}
}

func TestDockerCreateArgs(t *testing.T) {
	spec := Spec{
		Name: "grain-runhost-run-7-abc", Image: "ghcr.io/x/grain:v1", User: "1000:1000",
		Labels:  Labels{Deployment: "grain", Run: "7", Task: "3", Protocol: Protocol},
		Mounts:  []Mount{{Path: "/var/lib/grain/data/agent-home"}, {Path: "/opt/ro", ReadOnly: true}},
		Command: []string{"/usr/local/bin/grain", "runhost", "exec", "-spool", "/s"},
	}
	args := Docker{}.createArgs(spec)
	line := strings.Join(args, " ")
	for _, want := range []string{
		"run --detach --name grain-runhost-run-7-abc --network host --entrypoint /usr/local/bin/grain --restart no --user 1000:1000",
		"--label " + LabelRun + "=7",
		"--volume /var/lib/grain/data/agent-home:/var/lib/grain/data/agent-home ",
		"--volume /opt/ro:/opt/ro:ro",
		"ghcr.io/x/grain:v1 runhost exec -spool /s",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("docker run argv %q lacks %q", line, want)
		}
	}
	if strings.Contains(line, "--env") {
		t.Errorf("docker run argv %q sets an environment nobody asked for", line)
	}
}

// TestDockerCreateArgsSetsHome: a run host runs as the daemon's uid,
// which has no passwd entry in the image, so without HOME its CLI gets
// "/" and cannot write its config or session store (the daemon
// walkthrough, docs/test-runs/2026-10-14-run-hosts-daemon.md).
func TestDockerCreateArgsSetsHome(t *testing.T) {
	spec := Spec{
		Name: "grain-runhost-run-7-abc", Image: "ghcr.io/x/grain:v1",
		Labels:  Labels{Deployment: "grain", Run: "7", Task: "3", Protocol: Protocol},
		Command: []string{"/usr/local/bin/grain", "runhost", "exec", "-spool", "/s"},
	}
	line := strings.Join(Docker{Env: map[string]string{"HOME": "/var/lib/grain/data/agent-home"}}.createArgs(spec), " ")
	if want := "--env HOME=/var/lib/grain/data/agent-home ghcr.io/x/grain:v1 "; !strings.Contains(line, want) {
		t.Fatalf("docker run argv %q lacks %q before the image", line, want)
	}
}

// TestHostCreateTempIsUnderStaging is the daemon walkthrough's finding:
// claude's --mcp-config went to the daemon's os.TempDir, which no run
// host mounts, and the real CLI refused to start ("MCP config file not
// found"). A file created with no directory named must be under
// StagingRoot, which every run host mounts at the same path.
func TestHostCreateTempIsUnderStaging(t *testing.T) {
	h := newTestHost(t, newFakeDriver())
	path, err := h.CreateTemp(context.Background(), "", "grain-claude-mcp-config-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != h.StagingRoot {
		t.Fatalf("CreateTemp put %s outside the staging root %s, where a run host cannot read it", path, h.StagingRoot)
	}
	other := t.TempDir()
	if path, err := h.CreateTemp(context.Background(), other, "x-*"); err != nil || filepath.Dir(path) != other {
		t.Fatalf("CreateTemp(%s) = %s, %v; a directory asked for is kept", other, path, err)
	}
}

func TestDockerListReadsInspect(t *testing.T) {
	l := Labels{Deployment: "grain", Run: "9", Task: "1", Protocol: Protocol, Spool: "/s"}
	m := l.Map()
	var calls [][]string
	d := Docker{run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		switch args[0] {
		case "ps":
			return []byte("abc\ndef\n"), nil
		case "inspect":
			var b strings.Builder
			b.WriteString(`[{"Name":"/grain-runhost-a","State":{"Running":true},"Config":{"Labels":{`)
			first := true
			for k, v := range m {
				if !first {
					b.WriteString(",")
				}
				first = false
				b.WriteString(`"` + k + `":"` + v + `"`)
			}
			b.WriteString(`}}},{"Name":"/stranger","State":{"Running":true},"Config":{"Labels":{"x":"y"}}}]`)
			return []byte(b.String()), nil
		}
		return nil, nil
	}}
	infos, err := d.List(context.Background(), "grain")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "grain-runhost-a" || !infos[0].Running || infos[0].Labels != l {
		t.Fatalf("List = %+v", infos)
	}
	if !slices.Contains(calls[0], "label="+LabelDeployment+"=grain") {
		t.Fatalf("List must filter by deployment: %v", calls[0])
	}
}

// TestSpoolCursorHasNoHoleAndNoDuplicate is the transcript half of the
// acceptance test: two readers in turn, the second resuming from the
// cursor the first stopped at, see each byte exactly once.
func TestSpoolCursorHasNoHoleAndNoDuplicate(t *testing.T) {
	dir := t.TempDir()
	out, err := os.OpenFile(filepath.Join(dir, spoolStdout), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var seen bytes.Buffer
	var cursor int64
	for i := range 10 {
		out.WriteString(strings.Repeat(string(rune('a'+i)), i+1) + "\n")
		if i%3 == 0 { // a daemon reads now and then, and is replaced
			data, next, err := ReadStdout(dir, cursor)
			if err != nil {
				t.Fatal(err)
			}
			seen.Write(data)
			cursor = next
		}
	}
	data, _, err := ReadStdout(dir, cursor)
	if err != nil {
		t.Fatal(err)
	}
	seen.Write(data)
	whole, _ := os.ReadFile(filepath.Join(dir, spoolStdout))
	if seen.String() != string(whole) {
		t.Fatalf("tailed %q, spool holds %q", seen.String(), whole)
	}
}

func TestExecSpoolsOutputAndRemovesEnv(t *testing.T) {
	dir := t.TempDir()
	err := WriteSpool(dir, Invocation{Path: "/bin/sh", Args: []string{"-c", `read x; echo "out $x $SECRET"; echo err >&2; exit 3`}},
		[]string{"SECRET=s3cr3t"}, "in\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := Exec(context.Background(), dir, time.Time{}); err == nil {
		t.Fatal("Exec of a failing command returned nil")
	}
	data, _, _ := ReadStdout(dir, 0)
	if string(data) != "out in s3cr3t\n" {
		t.Fatalf("stdout = %q", data)
	}
	e, err := ReadExit(dir)
	if err != nil || e == nil || e.Code != 3 {
		t.Fatalf("exit = %+v, %v", e, err)
	}
	if ReadStderrTail(dir, 100) != "err" {
		t.Fatalf("stderr = %q", ReadStderrTail(dir, 100))
	}
	if _, err := os.Stat(filepath.Join(dir, spoolEnv)); !os.IsNotExist(err) {
		t.Fatalf("the env file must be gone once read, stat err %v", err)
	}
}

func TestExecEnforcesItsDeadline(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSpool(dir, Invocation{Path: "/bin/sleep", Args: []string{"30"}}, nil, ""); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = Exec(context.Background(), dir, time.Now().Add(200*time.Millisecond))
	if time.Since(start) > 10*time.Second {
		t.Fatal("the run host outlived its deadline label")
	}
	e, _ := ReadExit(dir)
	if e == nil || !strings.Contains(e.Error, "deadline") {
		t.Fatalf("exit = %+v, want a deadline reason", e)
	}
}

// fakeDriver stands in for docker: Create runs the entrypoint (Exec) in a
// goroutine, the way a container would run it, and that goroutine outlives
// whatever called Create -- as a run host outlives a daemon.
type fakeDriver struct {
	mu      sync.Mutex
	hosts   map[string]Info
	cancels map[string]context.CancelFunc
	done    map[string]chan struct{}
	specs   []Spec
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{hosts: map[string]Info{}, cancels: map[string]context.CancelFunc{}, done: map[string]chan struct{}{}}
}

func (f *fakeDriver) Create(_ context.Context, spec Spec) error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.hosts[spec.Name] = Info{Name: spec.Name, Labels: spec.Labels, Running: true}
	f.cancels[spec.Name] = cancel
	f.done[spec.Name] = done
	f.mu.Unlock()
	go func() {
		defer close(done)
		_ = Exec(ctx, spec.Labels.Spool, spec.Labels.Deadline)
		f.mu.Lock()
		if i, ok := f.hosts[spec.Name]; ok {
			i.Running = false
			f.hosts[spec.Name] = i
		}
		f.mu.Unlock()
	}()
	return nil
}

func (f *fakeDriver) List(_ context.Context, deployment string) ([]Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Info
	for _, i := range f.hosts {
		if i.Labels.Deployment == deployment {
			out = append(out, i)
		}
	}
	return out, nil
}

func (f *fakeDriver) Destroy(_ context.Context, name string) error {
	f.mu.Lock()
	cancel, done := f.cancels[name], f.done[name]
	delete(f.hosts, name)
	f.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	return nil
}

func newTestHost(t *testing.T, d Driver) *Host {
	root := t.TempDir()
	return &Host{
		LocalHost:   agent.LocalHost{Logf: t.Logf},
		Driver:      d,
		Deployment:  "test",
		Generation:  "v-old",
		Image:       "grain:test",
		Grain:       "/usr/local/bin/grain",
		SpoolRoot:   filepath.Join(root, "spool"),
		StagingRoot: filepath.Join(root, "staging"),
		Poll:        10 * time.Millisecond,
		CursorEvery: time.Nanosecond,
	}
}

func TestHostRunsARunInARunHost(t *testing.T) {
	d := newFakeDriver()
	h := newTestHost(t, d)
	var cursors []int64
	h.OnCursor = func(_ context.Context, run string, c int64) { cursors = append(cursors, c) }
	ctx := WithRun(context.Background(), Run{Task: "5", Run: "r5", Deadline: time.Now().Add(time.Hour)})
	var tee bytes.Buffer
	out, err := h.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", "echo one; sleep 0.05; echo two"}}, &tee)
	if err != nil {
		t.Fatal(err)
	}
	if out != "one\ntwo\n" || tee.String() != out {
		t.Fatalf("stdout %q, tee %q", out, tee.String())
	}
	if len(cursors) == 0 || cursors[len(cursors)-1] != int64(len(out)) {
		t.Fatalf("cursor never reached the end of the spool: %v", cursors)
	}
	if len(d.specs) != 1 || d.specs[0].Labels.Task != "5" || d.specs[0].Labels.Deadline.IsZero() {
		t.Fatalf("specs = %+v", d.specs)
	}
	if infos, _ := d.List(context.Background(), "test"); len(infos) != 0 {
		t.Fatalf("a finished run host was left behind: %+v", infos)
	}
}

func TestHostRunWithoutARunIsLocal(t *testing.T) {
	d := newFakeDriver()
	h := newTestHost(t, d)
	out, err := h.Run(context.Background(), agent.Command{Path: "/bin/echo", Args: []string{"probe"}}, nil)
	if err != nil || out != "probe\n" || len(d.specs) != 0 {
		t.Fatalf("out %q err %v specs %d", out, err, len(d.specs))
	}
}

func TestHostReportsAFailedCLI(t *testing.T) {
	h := newTestHost(t, newFakeDriver())
	ctx := WithRun(context.Background(), Run{Task: "5", Run: "r6"})
	out, err := h.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", "echo partial; echo boom >&2; exit 1"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") || out != "partial\n" {
		t.Fatalf("out %q err %v", out, err)
	}
}

// TestDetachPersistsTheCursorAtTheStopWhenItHasNotMoved is the daemon
// walkthrough's finding (docs/test-runs/2026-10-14-run-hosts-daemon.md):
// a run quiet since its last output -- a long tool call -- was detached
// without its cursor being written again, so the row's updated_at stayed
// at that output, and the next daemon credited the whole silence before
// the stop as restart gap. The detach must report the cursor even when it
// has not moved, so its time is the stop.
func TestDetachPersistsTheCursorAtTheStopWhenItHasNotMoved(t *testing.T) {
	d := newFakeDriver()
	h := newTestHost(t, d)
	type call struct {
		cursor int64
		at     time.Time
	}
	var mu sync.Mutex
	var calls []call
	h.OnCursor = func(_ context.Context, _ string, c int64) {
		mu.Lock()
		calls = append(calls, call{c, time.Now()})
		mu.Unlock()
	}
	h.Detach = func(context.Context) bool { return true }
	ctx, stop := context.WithCancel(WithRun(context.Background(), Run{Task: "9", Run: "r9"}))
	errc := make(chan error, 1)
	go func() {
		_, err := h.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", "echo one; sleep 30"}}, nil)
		errc <- err
	}()
	t.Cleanup(func() {
		infos, _ := d.List(context.Background(), "test")
		for _, i := range infos {
			_ = d.Destroy(context.Background(), i.Name)
		}
	})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		read := len(calls) > 0 && calls[len(calls)-1].cursor == int64(len("one\n"))
		mu.Unlock()
		if read {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tail never persisted the cursor past the first line")
		}
	}
	time.Sleep(200 * time.Millisecond) // quiet: the cursor does not move
	stoppedAt := time.Now()
	stop()
	if err := <-errc; err != ErrDetached {
		t.Fatalf("Run = %v, want ErrDetached", err)
	}
	mu.Lock()
	defer mu.Unlock()
	last := calls[len(calls)-1]
	if last.cursor != int64(len("one\n")) || last.at.Before(stoppedAt) {
		t.Fatalf("last cursor report = %d at %s, want %d at or after the stop (%s): the next daemon "+
			"would credit the quiet time before the stop as restart gap", last.cursor, last.at, len("one\n"), stoppedAt)
	}
}

// TestRunHostSurvivesTheDaemon is the acceptance test's shape without
// docker: the first daemon is stopped mid-run and detaches; its run host
// keeps writing; a second daemon adopts it from the persisted cursor, and
// the transcript the two of them tee'd has no hole and no duplicate.
func TestRunHostSurvivesTheDaemon(t *testing.T) {
	d := newFakeDriver()
	first := newTestHost(t, d)
	var persisted int64
	var mu sync.Mutex
	first.OnCursor = func(_ context.Context, _ string, c int64) { mu.Lock(); persisted = c; mu.Unlock() }
	first.Detach = func(ctx context.Context) bool { return true }

	ctx, stop := context.WithCancel(WithRun(context.Background(), Run{Task: "7", Run: "r7"}))
	script := `for i in 1 2 3 4 5 6 7 8 9 10; do echo line$i; sleep 0.03; done`
	var firstTee bytes.Buffer
	errc := make(chan error, 1)
	go func() {
		_, err := first.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", script}}, &firstTee)
		errc <- err
	}()
	time.Sleep(100 * time.Millisecond)
	stop() // the daemon is stopped for an upgrade
	if err := <-errc; err != ErrDetached {
		t.Fatalf("first daemon's Run = %v, want ErrDetached", err)
	}

	// The next daemon starts: list, decide, adopt.
	second := newTestHost(t, d)
	second.SpoolRoot, second.StagingRoot = first.SpoolRoot, first.StagingRoot
	infos, _ := d.List(context.Background(), "test")
	mu.Lock()
	cursor := persisted
	mu.Unlock()
	plan := Decide([]Claim{{Run: "r7", Task: "7", LastSeen: time.Now().Add(-time.Second)}}, infos,
		func(i Info) SpoolState { e, _ := ReadExit(i.Labels.Spool); return SpoolState{Exited: e != nil} }, time.Now())
	if len(plan.Adopt) != 1 || len(plan.Release) != 0 || len(plan.Finish) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Adopt[0].Gap < time.Second {
		t.Fatalf("gap %v not credited", plan.Adopt[0].Gap)
	}
	var secondTee bytes.Buffer
	out, err := second.Attach(context.Background(), plan.Adopt[0].Host, cursor, &secondTee)
	if err != nil {
		t.Fatal(err)
	}
	if firstTee.String()+secondTee.String() != out {
		t.Fatalf("transcript has a hole or a duplicate:\nfirst  %q\nsecond %q\nwhole  %q", firstTee.String(), secondTee.String(), out)
	}
	if strings.Count(out, "\n") != 10 {
		t.Fatalf("the run did not finish across the restart: %q", out)
	}
}

// A run cancelled for any reason but the daemon stopping is destroyed,
// and Attach hands back what its CLI wrote before that, as LocalHost does
// for a killed child: a run cut off by its deadline may already have
// pushed. One the daemon detaches from returns agent.ErrDetached, which a
// Framework recognises without importing this package.
func TestAttachReturnsTheSpoolOfACancelledRun(t *testing.T) {
	if !errors.Is(ErrDetached, agent.ErrDetached) {
		t.Fatal("runhost.ErrDetached is not agent.ErrDetached")
	}
	d := newFakeDriver()
	h := newTestHost(t, d)
	h.Detach = func(context.Context) bool { return false }
	ctx, cancel := context.WithCancelCause(WithRun(context.Background(), Run{Task: "8", Run: "r8"}))
	stopped := errors.New("the run's deadline")
	go func() { time.Sleep(150 * time.Millisecond); cancel(stopped) }()
	out, err := h.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", "echo pushed; sleep 30"}}, nil)
	if !errors.Is(err, stopped) {
		t.Fatalf("err = %v, want the cancellation's cause", err)
	}
	if out != "pushed\n" {
		t.Fatalf("stdout = %q, want what the CLI wrote before it was stopped", out)
	}
	if infos, _ := d.List(context.Background(), "test"); len(infos) != 0 {
		t.Fatalf("a cancelled run's run host was left behind: %+v", infos)
	}
}

// An upgrade flushes before it restarts the daemon (Host.Flush, piece 6
// of docs/non-disruptive-upgrade.md): every tail lets go with its cursor
// persisted while the daemon's own context is still live, the run host
// keeps working, and the next daemon picks it up from that cursor with no
// hole and no duplicate. A run starting to tail mid-flush lets go too.
func TestFlushDetachesEveryTailBeforeTheRestart(t *testing.T) {
	d := newFakeDriver()
	first := newTestHost(t, d)
	var mu sync.Mutex
	persisted := map[string]int64{}
	first.OnCursor = func(_ context.Context, run string, c int64) { mu.Lock(); persisted[run] = c; mu.Unlock() }

	ctx := WithRun(context.Background(), Run{Task: "9", Run: "r9"})
	script := `for i in 1 2 3 4 5 6 7 8 9 10; do echo line$i; sleep 0.05; done`
	var firstTee bytes.Buffer
	errc := make(chan error, 1)
	go func() {
		_, err := first.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", script}}, &firstTee)
		errc <- err
	}()
	time.Sleep(150 * time.Millisecond)
	fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runs, err := first.Flush(fctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0] != "r9" {
		t.Fatalf("Flush detached %v, want [r9]", runs)
	}
	if err := <-errc; !errors.Is(err, ErrDetached) {
		t.Fatalf("Run after Flush = %v, want ErrDetached", err)
	}
	mu.Lock()
	cursor := persisted["r9"]
	mu.Unlock()
	if cursor != int64(firstTee.Len()) {
		t.Fatalf("persisted cursor %d, but the tail had written %d bytes", cursor, firstTee.Len())
	}

	// A run whose tail starts while the flush stands detaches at once.
	late, lateErr := first.Run(WithRun(context.Background(), Run{Task: "10", Run: "r10"}),
		agent.Command{Path: "/bin/sh", Args: []string{"-c", "sleep 0.2; echo late"}}, nil)
	if !errors.Is(lateErr, ErrDetached) || late != "" {
		t.Fatalf("a tail started mid-flush = %q, %v; want ErrDetached", late, lateErr)
	}

	second := newTestHost(t, d)
	second.SpoolRoot, second.StagingRoot = first.SpoolRoot, first.StagingRoot
	infos, _ := d.List(context.Background(), "test")
	var host Info
	for _, i := range infos {
		if i.Labels.Run == "r9" {
			host = i
		}
	}
	if host.Name == "" {
		t.Fatalf("the flushed run's run host is gone: %+v", infos)
	}
	var secondTee bytes.Buffer
	out, err := second.Attach(context.Background(), host, cursor, &secondTee)
	if err != nil {
		t.Fatal(err)
	}
	if firstTee.String()+secondTee.String() != out || strings.Count(out, "\n") != 10 {
		t.Fatalf("transcript has a hole or a duplicate:\nfirst  %q\nsecond %q\nwhole  %q", firstTee.String(), secondTee.String(), out)
	}

	// Resume: the restart did not happen, so this daemon tails again.
	first.Resume()
	out, err = first.Run(WithRun(context.Background(), Run{Task: "11", Run: "r11"}),
		agent.Command{Path: "/bin/sh", Args: []string{"-c", "echo again"}}, nil)
	if err != nil || out != "again\n" {
		t.Fatalf("Run after Resume = %q, %v", out, err)
	}
}

// A run host stages locally, so it says IsLocal, but only a container on
// the host network reaches the daemon's loopback, where a capability
// grant's MCP proxy listens; a run pod is refused those servers rather
// than handed URLs nothing in it answers.
func TestOnlyAHostNetworkRunHostSharesTheDaemonsLoopback(t *testing.T) {
	servers := []agent.CapabilityMCPServer{{Name: "self-debug", URL: "http://127.0.0.1:1/x/mcp", Tools: []string{"t"}}}
	for _, c := range []struct {
		driver Driver
		shares bool
	}{
		{Docker{}, true},
		{Docker{Network: "host"}, true},
		{Docker{Network: "bridge"}, false},
		{Kubernetes{}, false},
	} {
		h := &Host{Driver: c.driver}
		if got := h.SharesHostLoopback(); got != c.shares {
			t.Errorf("%#v: SharesHostLoopback = %v, want %v", c.driver, got, c.shares)
		}
		_, _, err := agent.CarryCapabilityServers(h, servers)
		if (err == nil) != c.shares {
			t.Errorf("%#v: CarryCapabilityServers err = %v, want refused = %v", c.driver, err, !c.shares)
		}
	}
}
