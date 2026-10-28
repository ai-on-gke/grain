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

// The shell transport a caller that is not an agent runs commands over
// (shell.go), against the backend a test can actually have: a host
// directory.
//
// What these pin is the three ways this differs from the run_command
// tool beside it -- a bound the caller chose rather than a fixed ten
// minutes, the whole output rather than a few tens of kilobytes, and a
// non-zero exit reported as an answer rather than an error -- because
// each of them is a property grain's own CI depends on and none of them
// is visible in a job that passes quickly.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/procgroup"
)

// acquireShell builds a host sandbox and returns its shell, and the
// directory that sandbox is, so a test can put a file where the script
// it runs will look for one.
func acquireShell(t *testing.T) (orchestrator.SandboxShell, string) {
	t.Helper()
	sandbox, err := orchestrator.NewHostSandboxes(t.TempDir()).Acquire(context.Background(), "shell-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("acquiring a host sandbox: %v", err)
	}
	t.Cleanup(func() { sandbox.Release(context.Background()) })
	shell, ok := sandbox.(orchestrator.SandboxShell)
	if !ok {
		t.Fatal("a host sandbox should have a shell")
	}
	root, ok := sandbox.(interface{ Root() (string, error) })
	if !ok {
		t.Fatal("a host sandbox should be a directory")
	}
	dir, err := root.Root()
	if err != nil {
		t.Fatal(err)
	}
	return shell, dir
}

func TestAHostSandboxsShellRunsAScriptWhereItWasAskedTo(t *testing.T) {
	t.Parallel()
	shell, root := acquireShell(t)
	if err := os.MkdirAll(filepath.Join(root, "work", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "work", "sub", "marker"), []byte("here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := shell.RunShell(context.Background(), orchestrator.ShellRequest{
		Script: "cat marker\necho \"$GREETING to $NAME\"",
		Dir:    "work/sub",
		Env:    map[string]string{"GREETING": "hello", "NAME": "the sandbox"},
	})
	if err != nil {
		t.Fatalf("running a script: %v", err)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0: %s", out.ExitCode, out.Output)
	}
	if !strings.Contains(out.Output, "here") {
		t.Errorf("the script did not run in the directory it was given:\n%s", out.Output)
	}
	if !strings.Contains(out.Output, "hello to the sandbox") {
		t.Errorf("the script did not see its environment:\n%s", out.Output)
	}
}

// A failing line ends the script, which is what `set -e` buys a
// multi-line CI step and what GitHub Actions' own default shell does.
func TestAHostSandboxsShellStopsAtTheFirstFailingLine(t *testing.T) {
	t.Parallel()
	shell, _ := acquireShell(t)
	out, err := shell.RunShell(context.Background(), orchestrator.ShellRequest{
		Script: "echo first\nfalse\necho unreachable",
	})
	if err != nil {
		t.Fatalf("a script that exited non-zero is an answer, not a transport failure: %v", err)
	}
	if out.ExitCode == 0 {
		t.Errorf("exit code = 0, want the failure `false` reported")
	}
	if strings.Contains(out.Output, "unreachable") {
		t.Errorf("the script kept going after a failing line:\n%s", out.Output)
	}
}

// The whole log, uncut. This is the difference from run_command that
// costs the most when it is got wrong: mcp.maxToolResultBytes is 16KB,
// and a failing test suite's output is the thing somebody opened the job
// to read.
func TestAHostSandboxsShellKeepsTheWholeOutput(t *testing.T) {
	t.Parallel()
	shell, _ := acquireShell(t)
	const lines = 20000
	out, err := shell.RunShell(context.Background(), orchestrator.ShellRequest{
		Script: "seq 1 " + strconv.Itoa(lines),
	})
	if err != nil {
		t.Fatalf("running a script: %v", err)
	}
	if !strings.Contains(out.Output, "\n"+strconv.Itoa(lines)+"\n") && !strings.HasSuffix(out.Output, strconv.Itoa(lines)+"\n") {
		t.Errorf("the tail of a long output was cut: %d bytes, ending %q",
			len(out.Output), tail(out.Output, 40))
	}
	if len(out.Output) < 64<<10 {
		t.Errorf("output = %d bytes, want the whole of a log that does not fit a tool result", len(out.Output))
	}
}

// followWriter is somebody watching a script run: it records what it has
// been given so far and lets a test wait for a particular line to arrive,
// while the script that is printing it is still going.
type followWriter struct {
	mu   sync.Mutex
	seen strings.Builder
	got  chan struct{}
}

func (w *followWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen.Write(p)
	select {
	case w.got <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (w *followWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen.String()
}

// The whole point of ShellRequest.Output: a CI step is minutes of shell,
// and what somebody watching it wants is the line it is on now. So the
// first line has to arrive while the script is still running, not with the
// result -- which is what this waits for before the script is allowed to
// end.
func TestAHostSandboxsShellHandsOverOutputWhileTheScriptIsStillRunning(t *testing.T) {
	t.Parallel()
	shell, root := acquireShell(t)
	gate := filepath.Join(root, "carry-on")
	follower := &followWriter{got: make(chan struct{}, 1)}

	done := make(chan orchestrator.ShellResult, 1)
	go func() {
		out, err := shell.RunShell(context.Background(), orchestrator.ShellRequest{
			// Prints, then waits for the test to have seen it, then
			// prints again: the second line cannot exist until the first
			// has been followed.
			Script: "echo first\nwhile [ ! -e " + strconv.Quote(gate) + " ]; do sleep 0.05; done\necho second",
			Output: follower,
		})
		if err != nil {
			t.Errorf("running a script: %v", err)
		}
		done <- out
	}()

	deadline := time.After(20 * time.Second)
	for !strings.Contains(follower.text(), "first") {
		select {
		case <-follower.got:
		case <-deadline:
			t.Fatalf("no output arrived while the script was running: got %q", follower.text())
		}
	}
	if strings.Contains(follower.text(), "second") {
		t.Fatalf("the script got past its gate without the test opening it: %q", follower.text())
	}
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case out := <-done:
		if out.ExitCode != 0 {
			t.Fatalf("exit code = %d: %s", out.ExitCode, out.Output)
		}
		// Followed and returned are the same text: a reader who watched
		// the whole thing is not handed a different transcript at the end.
		if follower.text() != out.Output {
			t.Errorf("followed %q, returned %q", follower.text(), out.Output)
		}
		if !strings.Contains(out.Output, "second") {
			t.Errorf("the result is still the whole output: %q", out.Output)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the script never finished")
	}
}

// The caller's own bound, applied to the command rather than to the
// call: a step that hangs ends, and ends as a failure rather than as a
// transport error, since a command that ran out of its own time is an
// answer about the commit.
func TestAHostSandboxsShellHonoursTheBoundItWasGiven(t *testing.T) {
	t.Parallel()
	shell, _ := acquireShell(t)
	started := time.Now()
	out, err := shell.RunShell(context.Background(), orchestrator.ShellRequest{
		Script:  "echo starting\nsleep 30\necho done",
		Timeout: 500 * time.Millisecond,
	})
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Errorf("the bound was not applied: the script ran for %s", elapsed)
	}
	if err == nil && out.ExitCode == 0 {
		t.Errorf("a script killed by its own bound should not look like a success: %+v", out)
	}
	if strings.Contains(out.Output, "done") {
		t.Errorf("the script outlived its bound:\n%s", out.Output)
	}
}

// The environment a step gets is grain's decision rather than the
// daemon's accident: a curated few of this process's variables, and
// nothing else.
//
// The script a host shell runs is a CI step's, and a CI step's script
// comes out of a workflow file every agent grain dispatches can rewrite
// -- so `env` in a step used to be a read of whatever an operator's unit
// file exported into the daemon. The names below are the contract
// (hostShellPassthrough plus the two hostShellEnv sets itself, plus the
// four bash sets for itself); a name arriving here that is not on it
// means the daemon's environment is reaching a step again.
func TestAHostSandboxsShellGivesAScriptOnlyTheEnvironmentGrainMeantIt(t *testing.T) {
	// Something only the daemon holds, spelled like the thing this is
	// really about.
	t.Setenv("GRAIN_TEST_DAEMON_ONLY_TOKEN", "ghs_thedaemonsownsecret0000000000")
	// And the cache the CI e2e test's host arm passes in this way on
	// purpose, which has to survive the curation.
	t.Setenv("GOMODCACHE", "/daemon/gomodcache")

	shell, root := acquireShell(t)
	out, err := shell.RunShell(context.Background(), orchestrator.ShellRequest{
		Script: "env | sed 's/=.*//' | sort\necho \"home=$HOME\"\necho \"modcache=$GOMODCACHE\"\n" +
			"echo \"nosystem=$GIT_CONFIG_NOSYSTEM\"",
		Env: map[string]string{"FROM_THE_CALLER": "yes"},
	})
	if err != nil {
		t.Fatalf("running a script: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", out.ExitCode, out.Output)
	}

	allowed := map[string]bool{
		// What RunShell composes.
		"HOME": true, "PATH": true, "LANG": true, "LC_ALL": true,
		"GOMODCACHE": true, "GOCACHE": true, "npm_config_cache": true,
		// Not a passthrough but a value grain sets, so that git in the
		// sandbox ignores the host's system config -- a host credential
		// helper there would be handed the sandbox's proxy token.
		"GIT_CONFIG_NOSYSTEM": true,
		// Likewise grain's: a repository a script creates is in the files
		// ref format, which the daemon's git can read back (pkg/gitrefs).
		"GIT_DEFAULT_REF_FORMAT": true,
		// What the caller asked for, in this request.
		"FROM_THE_CALLER": true,
		// What bash sets for itself, which nobody passed it.
		"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true,
	}
	names, _, _ := strings.Cut(out.Output, "\nhome=")
	for _, name := range strings.Split(names, "\n") {
		if name = strings.TrimSpace(name); name == "" || allowed[name] {
			continue
		}
		t.Errorf("a script saw %s, which is the daemon's environment rather than the step's", name)
	}
	if strings.Contains(out.Output, "GRAIN_TEST_DAEMON_ONLY_TOKEN") {
		t.Error("a variable the daemon holds reached a script run in a sandbox")
	}
	// A PATH, so a step can find the tools this machine has -- the whole
	// premise of a host-directory backend.
	if !strings.Contains(out.Output, "\nPATH\n") {
		t.Errorf("a script was given no PATH:\n%s", out.Output)
	}
	if !strings.Contains(out.Output, "home="+root) {
		t.Errorf("HOME is not the sandbox:\n%s", out.Output)
	}
	if !strings.Contains(out.Output, "modcache=/daemon/gomodcache") {
		t.Errorf("the module cache the daemon was given did not reach the step:\n%s", out.Output)
	}
	// The other half of pinning HOME: with the host's system git config
	// still being read, a credential helper configured there would run
	// beside the one grain writes into the sandbox and be handed the
	// sandbox's proxy token to store (see
	// TestAHostSandboxsShellEnvironmentIgnoresTheHostsSystemGitConfig).
	if !strings.Contains(out.Output, "nosystem=1") {
		t.Errorf("a step's git would still read the host's system config:\n%s", out.Output)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func TestHostSandboxResetsChildOOMScoreAdjToPositive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("oom_score_adj is Linux-specific")
	}
	h := orchestrator.NewHostSandboxes(t.TempDir())
	sb, err := h.Acquire(context.Background(), "t-oom", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })

	sh := sb.(orchestrator.SandboxShell)
	res, err := sh.RunShell(context.Background(), orchestrator.ShellRequest{
		Script: "sleep 0.05\ncat /proc/self/oom_score_adj\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strconv.Itoa(procgroup.ChildOOMScoreAdj)
	if got := strings.TrimSpace(res.Output); got != want {
		t.Errorf("RunShell oom_score_adj = %q, want %q", got, want)
	}

	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var runCmd func(context.Context, map[string]any) string
	for _, tool := range tools {
		if tool.Name == "run_command" {
			h := tool.Handler
			runCmd = func(ctx context.Context, args map[string]any) string {
				r := h(ctx, args)
				if r.IsError {
					t.Fatalf("run_command failed: %s", r.Text)
				}
				return r.Text
			}
		}
	}
	if runCmd == nil {
		t.Fatal("HostSandbox missing run_command tool")
	}
	out := runCmd(context.Background(), map[string]any{
		"command": "sleep 0.05 && cat /proc/self/oom_score_adj",
	})
	if !strings.Contains(out, "\n"+want+"\n") {
		t.Errorf("HostSandbox run_command output = %q, want oom_score_adj %q", out, want)
	}
}

func TestAHostSandboxsShellDetachesAndReattachesByStepID(t *testing.T) {
	t.Parallel()
	h := orchestrator.NewHostSandboxes(t.TempDir())
	sb, err := h.Acquire(context.Background(), "ci-step-reattach", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("acquiring sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sb.Release(context.Background()) })

	shell := sb.(orchestrator.SandboxShell)
	root, err := sb.(interface{ Root() (string, error) }).Root()
	if err != nil {
		t.Fatal(err)
	}

	gate := filepath.Join(root, "continue-step")
	runsFile := filepath.Join(root, "runs")
	req := orchestrator.ShellRequest{
		StepID: "a0-s0",
		Script: "echo run >> " + strconv.Quote(runsFile) + "\n" +
			"echo before-restart\n" +
			"while [ ! -e " + strconv.Quote(gate) + " ]; do sleep 0.05; done\n" +
			"echo after-restart\n" +
			"exit 7\n",
	}

	firstFollower := &followWriter{got: make(chan struct{}, 1)}
	firstReq := req
	firstReq.Output = firstFollower
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()

	firstDone := make(chan error, 1)
	go func() {
		_, err := shell.RunShell(firstCtx, firstReq)
		firstDone <- err
	}()

	deadline := time.After(15 * time.Second)
	for !strings.Contains(firstFollower.text(), "before-restart") {
		select {
		case <-firstFollower.got:
		case <-deadline:
			t.Fatalf("step did not print before-restart: %q", firstFollower.text())
		}
	}

	// Simulate the daemon detaching on upgrade: cancel the first RunShell
	// call while the inner step continues running inside the sandbox.
	cancelFirst()
	if err := <-firstDone; err == nil {
		t.Fatal("expected cancelled attach call to return a transport error")
	}

	if ok, err := orchestrator.HasStepRunner(context.Background(), sb, "a0-s0"); err != nil || !ok {
		t.Fatalf("HasStepRunner(a0-s0) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := orchestrator.HasStepRunner(context.Background(), sb, "a0-s9"); err != nil || ok {
		t.Fatalf("HasStepRunner(a0-s9) = %v, %v; want false, nil", ok, err)
	}

	// Allow the inner step to finish while no caller is attached, then
	// re-attach with the same StepID on a readopted sandbox.
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	readopted, err := h.Readopt(context.Background(), "ci-step-reattach")
	if err != nil {
		t.Fatalf("readopting sandbox: %v", err)
	}
	secondFollower := &followWriter{got: make(chan struct{}, 1)}
	secondReq := req
	secondReq.Output = secondFollower
	res, err := readopted.(orchestrator.SandboxShell).RunShell(context.Background(), secondReq)
	if err != nil {
		t.Fatalf("re-attached RunShell failed: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7 (output=%q)", res.ExitCode, res.Output)
	}
	wantOut := "before-restart\nafter-restart\n"
	if res.Output != wantOut {
		t.Errorf("re-attached output = %q, want %q", res.Output, wantOut)
	}
	if secondFollower.text() != wantOut {
		t.Errorf("re-attached stream = %q, want %q", secondFollower.text(), wantOut)
	}
	runsBytes, err := os.ReadFile(runsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(runsBytes), "run\n"); got != 1 {
		t.Errorf("inner script ran %d times across re-attach, want 1", got)
	}
}
