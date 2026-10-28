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

package e2e

// The daemon-level half of b660114c6 (docs/test-plans/2026-10-11.md §7),
// which its unit tests leave unproved: a real daemon.Run, the same one
// `grain daemon` runs, driven over its own REST API by the CLI's own
// HTTPClient, with only the agent stubbed -- a shell script standing in
// for the claude CLI that holds a run open until it is killed. Stopping
// the daemon is cancelling Run's ctx, which is all SIGTERM does to it
// (pkg/cli/daemon.go's signal.NotifyContext).
//
// The walkthrough these came out of, with the timings a real binary
// showed, is docs/test-runs/2026-10-12-upgrade-drain-interrupted.md.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// daemon.Run builds its sandboxes from a backend flag; a test has no
// container runtime to give it, so it dispatches into plain directories,
// as cmd/grain's own tests and the browser suite's grain_e2e build do.
func init() {
	daemon.LocalSandboxes = func(dir string) (orchestrator.Sandboxes, error) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		return orchestrator.NewHostSandboxes(dir), nil
	}
}

// stubAgent is the claude CLI's stand-in: it records each start in
// <dir>/starts, makes one tool call, and then sleeps for however many
// seconds <dir>/sleep says before answering -- long enough, by default,
// that only the daemon stopping ends it.
type stubAgent struct{ dir string }

func newStubAgent(t *testing.T, sleep int) stubAgent {
	t.Helper()
	s := stubAgent{dir: t.TempDir()}
	script := `#!/bin/sh
echo start >> ` + filepath.Join(s.dir, "starts") + `
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"run_command","input":{"command":"true"}}]}}'
echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}}'
sleep "$(cat ` + filepath.Join(s.dir, "sleep") + `)"
echo '{"type":"result","result":"done"}'
`
	mustWriteFile(t, filepath.Join(s.dir, "claude"), script)
	if err := os.Chmod(filepath.Join(s.dir, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(s.dir, "sleep"), strconv.Itoa(sleep))
	mustWriteFile(t, filepath.Join(s.dir, "token"), "sk-ant-oat01-stub")
	return s
}

func (s stubAgent) starts() int {
	raw, _ := os.ReadFile(filepath.Join(s.dir, "starts"))
	return strings.Count(string(raw), "start")
}

// liveDaemon is one daemon.Run over a data directory that outlives it, so
// a second one started over the same directory is a restart.
type liveDaemon struct {
	base   string
	cancel context.CancelFunc
	done   chan error
}

func daemonConfig(t *testing.T, dataDir, sandboxDir string, agent stubAgent) daemon.Config {
	t.Helper()
	cfg := daemon.Config{
		DataDir: dataDir, SandboxDir: sandboxDir,
		UiAddr:     "127.0.0.1:" + strconv.Itoa(freeLoopbackPort(t)),
		ClaudePath: filepath.Join(agent.dir, "claude"),
		MaxWorkers: 1, PollInterval: time.Second,
	}
	// Named explicitly: grain has no default model of its own, and a
	// connection with none to run waits rather than guessing one.
	seedConnections(t, cfg, nil,
		[]model.AgentBackend{{ID: "claude", Framework: model.AgentFrameworkClaude, Models: []string{"claude-sonnet-5"}}},
		map[string]string{"claude": "sk-ant-oat01-stub"})
	return cfg
}

func startDaemon(t *testing.T, cfg daemon.Config) *liveDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &liveDaemon{base: "http://" + cfg.UiAddr, cancel: cancel, done: make(chan error, 1)}
	seedTestAccessCode(t, cfg.DataDir)
	go func() { d.done <- daemon.Run(ctx, cfg) }()
	t.Cleanup(func() { d.stop(t) })
	waitUntil(t, "the daemon to answer GET /api/sandboxes", 60*time.Second, func() bool {
		resp, err := accessGet(d.base + "/api/sandboxes")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return d
}

// stop is SIGTERM: cancel Run's ctx and wait for it to drain and return.
func (d *liveDaemon) stop(t *testing.T) {
	t.Helper()
	if d.cancel == nil {
		return
	}
	d.cancel()
	d.cancel = nil
	select {
	case err := <-d.done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("daemon.Run: %v", err)
		}
	case <-time.After(5 * time.Minute):
		t.Fatal("daemon.Run did not return within 5m of its ctx being cancelled")
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitUntil(t *testing.T, what string, limit time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", limit, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func readStore(t *testing.T, dataDir string) *model.Store {
	t.Helper()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening the daemon's store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store
}

func fileLongRun(t *testing.T, c *ui.HTTPClient) string {
	t.Helper()
	task, err := c.CreateTask(context.Background(), ui.CreateTaskRequest{
		Title: "hold a run open", Description: "the stub agent sleeps until stopped",
		Approved: true, NoRepo: true,
	})
	if err != nil {
		t.Fatalf("filing the task: %v", err)
	}
	return task.ID
}

// A daemon stopped under a live run records that run as interrupted --
// with its transcript and tool census, charging the task's failure streak
// and backoff nothing -- and the next start runs the task again at once.
// The CLI's client rides out the restart: a read and a comment sent while
// nothing is listening both succeed once it is back, and the comment
// lands exactly once.
func TestADaemonRestartInterruptsTheRunAndTheTaskResumes(t *testing.T) {
	agent := newStubAgent(t, 600)
	dataDir, sandboxDir := t.TempDir(), t.TempDir()
	cfg := daemonConfig(t, dataDir, sandboxDir, agent)

	first := startDaemon(t, cfg)
	c := ui.NewHTTPClient(first.base)
	id := fileLongRun(t, c)
	waitUntil(t, "the stub agent to start", 60*time.Second, func() bool { return agent.starts() == 1 })
	first.stop(t)

	store := readStore(t, dataDir)
	ctx := context.Background()
	runs, err := store.Runs(ctx, id)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs after the stop = %+v, %v; want the one", runs, err)
	}
	if runs[0].Outcome != model.InterruptedOutcome || runs[0].FinishedAt == nil {
		t.Fatalf("run after SIGTERM: outcome %q finished %v, want a finished %q run",
			runs[0].Outcome, runs[0].FinishedAt, model.InterruptedOutcome)
	}
	if streak, err := store.FailureStreak(ctx, id); err != nil {
		t.Fatal(err)
	} else if streak != nil && (streak.Count != 0 || streak.BackoffCount != 0) {
		t.Errorf("failure streak after an interrupted run = %+v, want nothing charged", streak)
	}
	if transcript, _, err := store.RunTranscript(ctx, id, 1); err != nil || !strings.Contains(transcript, "run_command") {
		t.Errorf("interrupted run's transcript = %q, %v; want it kept", transcript, err)
	}

	// The daemon is down: a read and a comment sent now are refused at the
	// dial, and retried until the next start is listening.
	type answer struct {
		what string
		err  error
	}
	answers := make(chan answer, 2)
	callCtx, cancelCalls := context.WithTimeout(ctx, 60*time.Second)
	defer cancelCalls()
	go func() {
		answers <- answer{"comment", c.AddComment(callCtx, id, "posted while the daemon restarted", nil)}
	}()
	go func() {
		_, err := c.Task(callCtx, id)
		answers <- answer{"read", err}
	}()
	time.Sleep(time.Second)
	cfg.UiAddr = strings.TrimPrefix(first.base, "http://")
	second := startDaemon(t, cfg)
	for range 2 {
		if a := <-answers; a.err != nil {
			t.Errorf("%s across the restart: %v", a.what, a.err)
		}
	}

	waitUntil(t, "the interrupted task to run again", 60*time.Second, func() bool { return agent.starts() == 2 })
	detail, err := ui.NewHTTPClient(second.base).GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var posted int
	for _, cm := range detail.Comments {
		if cm.Body == "posted while the daemon restarted" {
			posted++
		}
	}
	if posted != 1 {
		t.Errorf("the comment posted across the restart is on the task %d times, want exactly 1", posted)
	}
	if detail.FailedAttempts != 0 {
		t.Errorf("failedAttempts after an interrupted run = %d, want 0", detail.FailedAttempts)
	}
}

// upgradeSource is a source checkout -upgrade-src-dir can upgrade from
// with nothing real behind it: its `make container-build` installs a
// script that answers `schema-version` as this build does, and restart
// is a script that records the moment it was asked to restart.
func upgradeSource(t *testing.T) (srcDir, installPath, restartCmd, restarted string) {
	t.Helper()
	for _, tool := range []string{"git", "make"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	root := t.TempDir()
	built := filepath.Join(root, "built-grain")
	mustWriteFile(t, built, fmt.Sprintf("#!/bin/sh\necho %d\n", model.SchemaVersion))
	if err := os.Chmod(built, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(root, "upstream.git")
	run(t, root, "git", "init", "-q", "--bare", bare)
	srcDir = filepath.Join(root, "src")
	run(t, root, "git", "clone", "-q", bare, srcDir)
	run(t, srcDir, "git", "checkout", "-q", "-b", "candidate")
	mustWriteFile(t, filepath.Join(srcDir, "Makefile"), "container-build:\n\tmkdir -p bin && cp "+built+" bin/grain\n")
	mustWriteFile(t, filepath.Join(srcDir, ".gitignore"), "bin/\n")
	run(t, srcDir, "git", "add", ".")
	run(t, srcDir, "git", "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "stub build")
	run(t, srcDir, "git", "push", "-q", "origin", "candidate")

	restarted = filepath.Join(root, "restarted")
	restartCmd = filepath.Join(root, "restart")
	// Write the stamp beside its final name and rename it into place:
	// restartedAt polls for the file, and a plain `>` redirect creates it
	// empty before date has written anything, which the poll read as "".
	mustWriteFile(t, restartCmd, "#!/bin/sh\ndate +%s%N > "+restarted+".tmp && mv "+restarted+".tmp "+restarted+"\n")
	if err := os.Chmod(restartCmd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return srcDir, filepath.Join(root, "bin", "grain"), restartCmd, restarted
}

func scheduleUpgrade(t *testing.T, base string) time.Time {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/api/upgrade",
		bytes.NewBufferString(`{"branch":"candidate","afterTasksFinish":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	at := time.Now()
	resp, err := http.DefaultClient.Do(presentAccessCode(req))
	if err != nil {
		t.Fatalf("scheduling the upgrade: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("scheduling the upgrade: status %d, want 202", resp.StatusCode)
	}
	return at
}

func restartedAt(t *testing.T, path string) (time.Time, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	ns, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return time.Unix(0, ns), true
}

// With -upgrade-drain-window set, a scheduled upgrade waits for the run
// under way only as long as the window, then upgrades anyway; the restart
// that follows records the run it cut short as interrupted.
func TestAScheduledUpgradeStopsWaitingWhenItsDrainWindowRunsOut(t *testing.T) {
	srcDir, installPath, restartCmd, restarted := upgradeSource(t)
	agent := newStubAgent(t, 600)
	dataDir := t.TempDir()
	cfg := daemonConfig(t, dataDir, t.TempDir(), agent)
	cfg.UpgradeSrcDir, cfg.UpgradeInstallPath, cfg.UpgradeRestartCmd = srcDir, installPath, []string{restartCmd}
	const window = 3 * time.Second
	cfg.UpgradeDrainWindow = window

	d := startDaemon(t, cfg)
	id := fileLongRun(t, ui.NewHTTPClient(d.base))
	waitUntil(t, "the stub agent to start", 60*time.Second, func() bool { return agent.starts() == 1 })
	scheduled := scheduleUpgrade(t, d.base)

	var at time.Time
	waitUntil(t, "the upgrade to restart the daemon", 60*time.Second, func() bool {
		var ok bool
		at, ok = restartedAt(t, restarted)
		return ok
	})
	if waited := at.Sub(scheduled); waited < window {
		t.Errorf("the upgrade restarted %s after it was scheduled, inside its %s drain window, with a run still live", waited, window)
	}
	if n := agent.starts(); n != 1 {
		t.Errorf("the agent started %d times while the upgrade held the queue, want 1", n)
	}
	d.stop(t)

	runs, err := readStore(t, dataDir).Runs(context.Background(), id)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v; want the one", runs, err)
	}
	if runs[0].Outcome != model.InterruptedOutcome {
		t.Errorf("the run the upgrade's restart cut short = %q, want %q", runs[0].Outcome, model.InterruptedOutcome)
	}
}

// With no drain window (0), a scheduled upgrade waits for the run under
// way to finish on its own, however long that is, and only then upgrades.
func TestAScheduledUpgradeWithNoDrainWindowWaitsForTheRunToFinish(t *testing.T) {
	srcDir, installPath, restartCmd, restarted := upgradeSource(t)
	agent := newStubAgent(t, 4)
	dataDir := t.TempDir()
	cfg := daemonConfig(t, dataDir, t.TempDir(), agent)
	cfg.UpgradeSrcDir, cfg.UpgradeInstallPath, cfg.UpgradeRestartCmd = srcDir, installPath, []string{restartCmd}

	d := startDaemon(t, cfg)
	id := fileLongRun(t, ui.NewHTTPClient(d.base))
	waitUntil(t, "the stub agent to start", 60*time.Second, func() bool { return agent.starts() == 1 })
	scheduleUpgrade(t, d.base)

	var at time.Time
	waitUntil(t, "the upgrade to restart the daemon", 60*time.Second, func() bool {
		var ok bool
		at, ok = restartedAt(t, restarted)
		return ok
	})
	d.stop(t)

	runs, err := readStore(t, dataDir).Runs(context.Background(), id)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v; want the one", runs, err)
	}
	run := runs[0]
	if run.FinishedAt == nil || run.Outcome == model.InterruptedOutcome {
		t.Fatalf("run = outcome %q finished %v, want it to have finished on its own", run.Outcome, run.FinishedAt)
	}
	if at.Before(*run.FinishedAt) {
		t.Errorf("the upgrade restarted at %s, before the run it was waiting for finished at %s",
			at.UTC().Format(time.RFC3339Nano), run.FinishedAt.UTC().Format(time.RFC3339Nano))
	}
}
