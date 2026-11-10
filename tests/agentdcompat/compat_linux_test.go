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

//go:build linux

// Package agentdcompat drives this tree's daemon side of the host agent
// contract (pkg/hostd's Remote and Client, exactly what pkg/daemon uses)
// against grain-agentd binaries built from earlier releases, so a change
// that would strand an installed host agent fails CI rather than an
// operator's machine (design/design/agentd-stable-contract.design.md,
// "Guard rails").
//
// The binaries come from $GRAIN_AGENTD_COMPAT_BINARIES, a comma-separated
// list of label=path; scripts/agentd-compat.sh builds them from release
// tags and runs this. With the variable unset the test skips, so a plain
// `go test ./...` stays hermetic.
package agentdcompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/hookpolicy"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// BinariesEnv names the agentd binaries to test, as label=path pairs.
const BinariesEnv = "GRAIN_AGENTD_COMPAT_BINARIES"

func binaries(t *testing.T) map[string]string {
	t.Helper()
	spec := strings.TrimSpace(os.Getenv(BinariesEnv))
	if spec == "" {
		t.Skipf("$%s is unset: run scripts/agentd-compat.sh to build released grain-agentd binaries and test them", BinariesEnv)
	}
	out := map[string]string{}
	for _, kv := range strings.Split(spec, ",") {
		label, path, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || label == "" || path == "" {
			t.Fatalf("$%s: %q is not label=path", BinariesEnv, kv)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("$%s: %s: %v", BinariesEnv, label, err)
		}
		out[label] = path
	}
	return out
}

func TestReleasedAgentdsWorkWithThisDaemon(t *testing.T) {
	for label, bin := range binaries(t) {
		t.Run(label, func(t *testing.T) { exercise(t, bin) })
	}
}

// controller is this tree's daemon end: a Remote behind the route a host
// agent dials, which a test can restart -- a new Remote, every session cut
// -- the way a daemon upgrade does.
type controller struct {
	token string
	http  *httptest.Server

	mu     sync.Mutex
	remote *hostd.Remote
	conns  []net.Conn
	wg     sync.WaitGroup
}

func startController(t *testing.T, token string) *controller {
	t.Helper()
	c := &controller{token: token}
	c.remote = c.newRemote(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+hostd.RemotePath, func(w http.ResponseWriter, r *http.Request) {
		c.wg.Add(1)
		defer c.wg.Done()
		c.current().ServeHTTP(w, r)
	})
	c.http = httptest.NewUnstartedServer(mux)
	c.http.Config.ConnState = func(conn net.Conn, st http.ConnState) {
		if st == http.StateHijacked {
			c.mu.Lock()
			c.conns = append(c.conns, conn)
			c.mu.Unlock()
		}
	}
	c.http.Start()
	t.Cleanup(func() {
		c.http.Close()
		c.cut()
		c.wg.Wait()
	})
	return c
}

func (c *controller) newRemote(t *testing.T) *hostd.Remote {
	var mu sync.Mutex
	done := false
	t.Cleanup(func() { mu.Lock(); done = true; mu.Unlock() })
	return &hostd.Remote{Token: c.token, Logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if !done {
			t.Logf("daemon: "+format, args...)
		}
	}}
}

func (c *controller) current() *hostd.Remote {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remote
}

func (c *controller) cut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.conns {
		_ = conn.Close()
	}
	c.conns = nil
}

// restart replaces the daemon: a fresh Remote that knows nothing of the
// last one's sessions, and every connection to the old one cut.
func (c *controller) restart(t *testing.T) {
	c.mu.Lock()
	c.remote = c.newRemote(t)
	c.mu.Unlock()
	c.cut()
}

func (c *controller) client() hostd.Client {
	return hostd.Client{Dial: func(ctx context.Context) (net.Conn, error) { return c.current().Dial(ctx) }}
}

func (c *controller) waitConnected(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.current().WaitConnected(ctx); err != nil {
		t.Fatalf("the host agent never dialed in: %v", err)
	}
}

// fakeCLI is the one allowlisted "agent CLI": it says its arguments,
// echoes stdin, or waits for a file to appear, so every path of a start
// is observable without a real agent.
const fakeCLI = `#!/bin/sh
echo "args:$*"
echo "env:$COMPAT_MARK"
case "$1" in
echo) cat ;;
wait) while [ ! -e "$2/go" ]; do sleep 0.05; done; echo "released" ;;
esac
echo "to-stderr" >&2
exit 3
`

type agent struct {
	cmd    *exec.Cmd
	exited chan error
	log    *bytes.Buffer
}

func exercise(t *testing.T, bin string) {
	// A short root: the agent's unix socket must fit in sun_path.
	root, err := os.MkdirTemp("", "agc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "h")
	stage := filepath.Join(home, ".grain", "hostd-stage")
	for _, d := range []string{home, filepath.Join(root, "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(root, "fake-cli")
	if err := os.WriteFile(cli, []byte(fakeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	allowlist := filepath.Join(root, "agentd.yaml")
	if err := os.WriteFile(allowlist, []byte(fmt.Sprintf(
		"clis:\n  fake:\n    path: %s\n    paths_under: [%s]\n    cwd_under: [%s]\n    env: [COMPAT_*]\n",
		cli, stage, stage)), 0o644); err != nil {
		t.Fatal(err)
	}
	const token = "compat-test-token"
	tokenFile := filepath.Join(root, "token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctl := startController(t, token)
	a := &agent{exited: make(chan error, 1), log: &bytes.Buffer{}}
	a.cmd = exec.Command(bin,
		"-server", ctl.http.URL,
		"-token-file", tokenFile,
		"-key-file", filepath.Join(root, "no-key"),
		"-allowlist", allowlist,
		"-socket", filepath.Join(root, "s.sock"),
		"-state-dir", filepath.Join(root, "state"),
	)
	a.cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "USER=" + os.Getenv("USER")}
	a.cmd.Stdout, a.cmd.Stderr = lockedWriter(a.log), lockedWriter(a.log)
	if err := a.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { a.exited <- a.cmd.Wait() }()
	t.Cleanup(func() {
		_ = a.cmd.Process.Kill()
		<-a.exited
		if t.Failed() {
			t.Logf("grain-agentd's log:\n%s", logText(a.log))
		}
	})

	ctl.waitConnected(t)
	c := ctl.client()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Hello: the versions are compatible, the agent is remote, and every
	// feature this daemon relies on is there (inferred from the protocol
	// for a build that predates Hello.Features).
	peer, err := c.Hello(ctx)
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	if err := hostd.Compatible(hostd.ThisHello("compat"), peer); err != nil {
		t.Fatalf("this daemon refuses the agent: %v", err)
	}
	for _, f := range []hostd.Feature{hostd.FeatureStart, hostd.FeatureDetach, hostd.FeatureDrain, hostd.FeatureStage, hostd.FeatureListen} {
		if !peer.Has(f) {
			t.Errorf("hello %+v lacks feature %q, which every accepted agent has", peer, f)
		}
	}
	if !peer.Remote || peer.StageRoot == "" {
		t.Fatalf("hello %+v: a dialed-in agent says remote and its stage root", peer)
	}
	stageRoot := peer.StageRoot

	t.Run("list", func(t *testing.T) {
		offers, err := c.Offers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(offers) != 1 || offers[0].Name != "fake" || offers[0].Kind != hostd.KindCLI {
			t.Fatalf("offers = %+v, want the one fake cli", offers)
		}
	})

	t.Run("start", func(t *testing.T) {
		var out, errb bytes.Buffer
		exit, err := c.Run(ctx, hostd.Request{Kind: hostd.KindCLI, Name: "fake", Args: []string{"echo", "one"},
			Env: []string{"COMPAT_MARK=m1"}, Dir: stageRoot}, strings.NewReader("from-stdin\n"), &out, &errb)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if exit.Code != 3 {
			t.Errorf("exit = %+v, want code 3", exit)
		}
		for _, want := range []string{"args:echo one", "env:m1", "from-stdin"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("stdout %q lacks %q", out.String(), want)
			}
		}
		if !strings.Contains(errb.String(), "to-stderr") {
			t.Errorf("stderr %q lacks to-stderr", errb.String())
		}
	})

	t.Run("refusal", func(t *testing.T) {
		_, err := c.Run(ctx, hostd.Request{Kind: hostd.KindCLI, Name: "fake", Env: []string{"NOT_ALLOWED=1"}}, nil, io.Discard, io.Discard)
		var r *hostd.RefusedError
		if !errors.As(err, &r) {
			t.Fatalf("an env name the entry does not accept: err = %v, want a refusal", err)
		}
	})

	t.Run("stage", func(t *testing.T) {
		dir := filepath.Join(stageRoot, "compat")
		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageMkdirAll, Path: dir, Perm: 0o700}); err != nil {
			t.Fatalf("mkdir_all: %v", err)
		}
		file := filepath.Join(dir, "config.json")
		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageWriteFile, Path: file, Data: []byte(`{"staged":true}`), Perm: 0o600}); err != nil {
			t.Fatalf("write_file: %v", err)
		}
		got, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageReadTail, Path: file, Limit: 1024})
		if err != nil || string(got.Data) != `{"staged":true}` {
			t.Fatalf("read_tail = %q, %v", got.Data, err)
		}
		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageWriteFile, Path: filepath.Join(root, "escape"), Data: []byte("x")}); err == nil {
			t.Error("write_file outside the stage root was not refused")
		}
		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageRemoveAll, Path: dir}); err != nil {
			t.Fatalf("remove_all: %v", err)
		}
	})

	t.Run("hooks", func(t *testing.T) {
		if peer.Grain == "" {
			t.Fatalf("hello %+v has empty Grain binary path", peer)
		}
		runHook := func(args []string, stdin string) string {
			t.Helper()
			cmd := exec.CommandContext(ctx, peer.Grain, args...)
			cmd.Stdin = strings.NewReader(stdin)
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if err := cmd.Run(); err != nil {
				t.Fatalf("%s %v failed: %v (stderr: %s)", peer.Grain, args, err, errOut.String())
			}
			if strings.Contains(errOut.String(), "ignoring unrecognized arguments") || strings.Contains(errOut.String(), "flag provided but not defined") {
				t.Fatalf("%s %v rejected its arguments: %s", peer.Grain, args, errOut.String())
			}
			return strings.TrimSpace(out.String())
		}

		// Legacy / fallback contract (used for hosts without FeatureHookPolicy).
		if got := runHook([]string{"agy-tool-hook"}, `{"toolCall":{"name":"mcp_grain-sandbox_run_command"}}`); got != "" {
			t.Errorf("fallback agy-tool-hook denied run_command: %s", got)
		}
		if got := runHook([]string{"agy-tool-hook"}, `{"toolCall":{"name":"run_command"}}`); !strings.Contains(got, `"decision":"deny"`) {
			t.Errorf("fallback agy-tool-hook allowed native run_command: %s", got)
		}
		if got := runHook([]string{"claude-stop-hook"}, `{"hook_event_name":"Stop","last_assistant_message":"Done."}`); !strings.Contains(got, `"decision":"block"`) {
			t.Errorf("fallback claude-stop-hook allowed unfinished background stop: %s", got)
		}

		if peer.Has(hostd.FeatureHookPolicy) {
			hookDir := filepath.Join(stageRoot, "hook-policy-compat")
			if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageMkdirAll, Path: hookDir, Perm: 0o700}); err != nil {
				t.Fatal(err)
			}
			policyPath := filepath.Join(hookDir, hookpolicy.Filename)
			recordPath := filepath.Join(hookDir, "host-calls.jsonl")
			transcriptPath := filepath.Join(hookDir, "session.jsonl")
			raw, err := hookpolicy.Marshal(hookpolicy.File{
				Version: hookpolicy.Version,
				Tool: &hookpolicy.ToolPolicy{
					AllowedTools: []string{"mcp_grain-sandbox_future_compat"},
					HostToolGrants: []hookpolicy.HostToolGrant{
						{Capability: "compat#lookup", Tools: []string{"compat_lookup"}},
					},
					RecordPath:        recordPath,
					TaskID:            "1",
					RunID:             "r1",
					DefaultDenySuffix: "denied by staged compat policy.",
				},
				Stop: &hookpolicy.StopPolicy{
					ToolPrefixes:             []string{"mcp__grain-sandbox__"},
					BackgroundContinueReason: "compat background nudge",
					ClosingTools:             []string{"compat_finish"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageWriteFile, Path: policyPath, Data: raw, Perm: 0o600}); err != nil {
				t.Fatal(err)
			}
			transcript := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"mcp__grain-sandbox__compat_finish","input":{}}]}}` + "\n" +
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok","is_error":false}]}}` + "\n"
			if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageWriteFile, Path: transcriptPath, Data: []byte(transcript), Perm: 0o600}); err != nil {
				t.Fatal(err)
			}

			// Fire both hooks while the daemon is cut, proving policy evaluation
			// is purely local on the host and never calls back to the controller.
			ctl.restart(t)
			if got := runHook([]string{"agy-tool-hook", "-policy", policyPath}, `{"toolCall":{"name":"mcp_grain-sandbox_future_compat"}}`); got != "" {
				t.Errorf("staged agy-tool-hook denied future_compat: %s", got)
			}
			if got := runHook([]string{"agy-tool-hook", "-policy", policyPath}, `{"toolCall":{"name":"compat_lookup","args":{"query":"q"}}}`); got != "" {
				t.Errorf("staged agy-tool-hook denied granted compat_lookup: %s", got)
			}
			if got := runHook([]string{"agy-tool-hook", "-policy", policyPath}, `{"toolCall":{"name":"mcp_grain-sandbox_run_command"}}`); !strings.Contains(got, "denied by staged compat policy.") {
				t.Errorf("staged agy-tool-hook consulted compiled-in roster: %s", got)
			}
			if got := runHook([]string{"claude-stop-hook", "-policy", policyPath}, `{"hook_event_name":"Stop","last_assistant_message":"Done."}`); !strings.Contains(got, "compat background nudge") {
				t.Errorf("staged claude-stop-hook did not block with staged nudge: %s", got)
			}
			if got := runHook([]string{"claude-stop-hook", "-policy", policyPath}, fmt.Sprintf(`{"hook_event_name":"Stop","transcript_path":%q}`, transcriptPath)); got != "" {
				t.Errorf("staged claude-stop-hook blocked compat_finish: %s", got)
			}

			ctl.waitConnected(t)
			c = ctl.client()
			if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageRemoveAll, Path: hookDir}); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("listen", func(t *testing.T) {
		addr, release, err := ctl.current().Carry(ctx, func(conn net.Conn) {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		})
		if err != nil {
			t.Fatalf("carry: %v", err)
		}
		defer release()
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatalf("dialing the agent's carried listener %s: %v", addr, err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write([]byte("ping\n")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 5)
		if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping\n" {
			t.Fatalf("carried echo = %q, %v", buf, err)
		}
	})

	t.Run("detached start, reattach after a daemon restart", func(t *testing.T) {
		gate := filepath.Join(stageRoot, "gate")
		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageMkdirAll, Path: gate, Perm: 0o700}); err != nil {
			t.Fatal(err)
		}
		started := make(chan hostd.Started, 1)
		dc := c
		dc.OnStarted = func(s hostd.Started) { started <- s }
		dc.Detach = func(context.Context) bool { return true }
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		var first bytes.Buffer
		go func() {
			defer close(done)
			_, _ = dc.Run(runCtx, hostd.Request{Kind: hostd.KindCLI, Name: "fake", Args: []string{"wait", gate},
				Dir: stageRoot, Detach: true, Run: "run-1", Task: "task-1"}, nil, lockedWriter(&first), io.Discard)
		}()
		var st hostd.Started
		select {
		case st = <-started:
		case <-time.After(30 * time.Second):
			t.Fatal("the detached start never started")
		}
		if st.ID == "" {
			t.Fatalf("started %+v: a detached start names its process", st)
		}
		// The daemon goes away mid-run, as an upgrade takes it.
		stop()
		<-done
		ctl.restart(t)
		ctl.waitConnected(t)
		c = ctl.client()

		procs, err := c.Processes(ctx)
		if err != nil {
			t.Fatalf("processes after the restart: %v", err)
		}
		var found *hostd.Process
		for i := range procs {
			if procs[i].ID == st.ID {
				found = &procs[i]
			}
		}
		if found == nil || !found.Detached || found.Run != "run-1" || found.Task != "task-1" {
			t.Fatalf("processes = %+v: want detached %s for run-1/task-1", procs, st.ID)
		}
		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageWriteFile, Path: filepath.Join(gate, "go"), Data: []byte("1"), Perm: 0o600}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		exit, cur, err := c.Attach(ctx, st.ID, hostd.Cursor{}, &out, io.Discard)
		if err != nil {
			t.Fatalf("attach: %v", err)
		}
		if exit.Code != 3 || !strings.Contains(out.String(), "args:wait") || !strings.Contains(out.String(), "released") {
			t.Fatalf("attach from the start: exit %+v, stdout %q", exit, out.String())
		}
		if cur.Stdout != int64(out.Len()) {
			t.Errorf("cursor %+v does not match the %d bytes read", cur, out.Len())
		}
	})

	t.Run("cancel", func(t *testing.T) {
		started := make(chan hostd.Started, 1)
		dc := c
		dc.OnStarted = func(s hostd.Started) { started <- s }
		dc.Detach = func(context.Context) bool { return true }
		runCtx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			_, _ = dc.Run(runCtx, hostd.Request{Kind: hostd.KindCLI, Name: "fake", Args: []string{"wait", filepath.Join(stageRoot)},
				Dir: stageRoot, Detach: true}, nil, io.Discard, io.Discard)
		}()
		st := <-started
		stop()
		if err := c.Cancel(ctx, st.ID); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		waitFor(t, "the cancelled process to leave the listing", func() bool {
			procs, err := c.Processes(ctx)
			if err != nil {
				return false
			}
			for _, p := range procs {
				if p.ID == st.ID && !p.Exited {
					return false
				}
			}
			return true
		})
	})

	t.Run("drain", func(t *testing.T) {
		if err := c.Drain(ctx); err != nil {
			t.Fatalf("drain: %v", err)
		}
		select {
		case err := <-a.exited:
			a.exited <- err // for the cleanup
			if err != nil {
				t.Fatalf("a drained agent with nothing running exits cleanly: %v", err)
			}
		case <-time.After(30 * time.Second):
			// Still up: it must at least say it is draining and refuse starts.
			h, err := c.Hello(ctx)
			if err != nil || !h.Draining {
				t.Fatalf("after OpDrain the agent neither exited nor says draining: %+v, %v", h, err)
			}
			_, err = c.Run(ctx, hostd.Request{Kind: hostd.KindCLI, Name: "fake"}, nil, io.Discard, io.Discard)
			if !hostd.IsDraining(err) {
				t.Fatalf("a draining agent took a start: %v", err)
			}
		}
	})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

var logMu sync.Mutex

type locked struct{ w io.Writer }

func (l locked) Write(p []byte) (int, error) {
	logMu.Lock()
	defer logMu.Unlock()
	return l.w.Write(p)
}

func lockedWriter(w io.Writer) io.Writer { return locked{w} }

func logText(b *bytes.Buffer) string {
	logMu.Lock()
	defer logMu.Unlock()
	return b.String()
}
