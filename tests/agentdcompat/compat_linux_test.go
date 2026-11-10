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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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

	t.Run("logins status", func(t *testing.T) {
		if peer.Has(hostd.FeatureLogins) {
			deadline := time.Now().Add(10 * time.Second)
			for !ctl.current().Status().LoginsKnown {
				if time.Now().After(deadline) {
					t.Fatalf("daemon never marked empty logins known for host with FeatureLogins: %+v", ctl.current().Status())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if got := ctl.current().Status().Logins; len(got) != 0 {
				t.Fatalf("status logins = %q, want none", got)
			}
		} else {
			// Give learnLogins time to complete its Hello stream against an older host.
			time.Sleep(100 * time.Millisecond)
			if st := ctl.current().Status(); st.LoginsKnown {
				t.Fatalf("daemon marked logins known for an older host without FeatureLogins: %+v", st)
			}
		}
	})

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

		// Symlink staging: refused when the login file does not exist or is
		// not a login; allowed via LoginTargets on hosts with logins_map and
		// via CodexAuth on older hosts.
		codexLogin := filepath.Join(home, ".codex", "auth.json")
		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageSymlink, Path: filepath.Join(dir, "missing-auth.json"), Target: codexLogin}); err == nil {
			t.Error("symlink to a missing login file was not refused")
		}
		if err := os.MkdirAll(filepath.Dir(codexLogin), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(codexLogin, []byte(`{"compat":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(filepath.Dir(codexLogin))

		h, err := c.Hello(ctx)
		if err != nil {
			t.Fatalf("hello with codex login: %v", err)
		}
		var target string
		if h.Has(hostd.FeatureLoginsMap) {
			target = h.LoginTargets[hostd.LoginCodex]
		} else {
			target = h.CodexAuth
		}
		if target == "" {
			t.Fatalf("hello %+v reported no codex login target", h)
		}
		stagedAuth := filepath.Join(dir, "auth.json")
		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageSymlink, Path: stagedAuth, Target: target}); err != nil {
			t.Fatalf("symlink to codex login (%s): %v", target, err)
		}
		if info, err := os.Lstat(stagedAuth); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("staged auth link Lstat = %v, %v; want symlink", info, err)
		}
		if data, err := os.ReadFile(stagedAuth); err != nil || string(data) != `{"compat":true}` {
			t.Fatalf("reading staged auth link = %q, %v", data, err)
		}
		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageSymlink, Path: filepath.Join(dir, "bad-link"), Target: tokenFile}); err == nil {
			t.Error("symlink to a non-login file was not refused")
		}

		// Hosts advertising logins_map accept top-level logins: in the
		// allowlist and allow StageSymlink to target only what it names.
		if h.Has(hostd.FeatureLoginsMap) {
			origAllow, err := os.ReadFile(allowlist)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.WriteFile(allowlist, origAllow, 0o644) }()

			customLogin := filepath.Join(home, ".custom-cli", "creds.json")
			if err := os.MkdirAll(filepath.Dir(customLogin), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(customLogin, []byte(`{"custom":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(filepath.Dir(customLogin))

			mappedAllow := string(origAllow) + "logins:\n  custom: ~/.custom-cli/creds.json\n"
			if err := os.WriteFile(allowlist, []byte(mappedAllow), 0o644); err != nil {
				t.Fatal(err)
			}
			hm, err := c.Hello(ctx)
			if err != nil {
				t.Fatalf("hello with logins: map: %v", err)
			}
			if !reflect.DeepEqual(hm.Logins, []string{"custom"}) {
				t.Fatalf("mapped Hello.Logins = %q, want [\"custom\"]", hm.Logins)
			}
			customTarget := hm.LoginTargets["custom"]
			if customTarget == "" {
				t.Fatalf("mapped Hello.LoginTargets = %v, want custom target", hm.LoginTargets)
			}
			stagedCustom := filepath.Join(dir, "custom.json")
			if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageSymlink, Path: stagedCustom, Target: customTarget}); err != nil {
				t.Fatalf("symlink to mapped custom login: %v", err)
			}
			if data, err := os.ReadFile(stagedCustom); err != nil || string(data) != `{"custom":true}` {
				t.Fatalf("reading staged custom link = %q, %v", data, err)
			}
			if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageSymlink, Path: filepath.Join(dir, "unmapped-codex.json"), Target: target}); err == nil {
				t.Error("symlink to unmapped codex login was not refused when logins: map omitted it")
			}
		}

		if _, err := c.Stage(ctx, hostd.StageOp{Op: hostd.StageRemoveAll, Path: dir}); err != nil {
			t.Fatalf("remove_all: %v", err)
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
		// An idle agent may exit on s.Drain() before its mux flushes frameOK.
		drainErr := c.Drain(ctx)
		if drainErr != nil && !errors.Is(drainErr, io.EOF) {
			t.Fatalf("drain: %v", drainErr)
		}
		select {
		case err := <-a.exited:
			a.exited <- err // for the cleanup
			if err != nil {
				t.Fatalf("a drained agent with nothing running exits cleanly: %v", err)
			}
		case <-time.After(30 * time.Second):
			if drainErr != nil {
				t.Fatalf("drain: %v", drainErr)
			}
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
