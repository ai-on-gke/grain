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

package hostd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// testServer is a hostd serving an allowlist with one CLI entry, "sh",
// that runs /bin/sh with whatever argv the test sends. Paths it is handed
// must sit under dir, so a script takes them as "$1", not in its text.
type testServer struct {
	srv    *Server
	client Client
	dir    string
	logs   *logBuf
	cancel context.CancelFunc
	done   chan error
}

type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.b, format+"\n", args...)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func startServer(t *testing.T, extra string, configure func(*Server)) *testServer {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)
	// A unix socket path is limited to ~108 bytes, which t.TempDir can
	// exceed.
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	body := fmt.Sprintf("clis:\n  sh:\n    path: %s\n    paths_under: [%[2]s]\n    cwd_under: [%[2]s]\n    env: [GREETING]\n    pass_env: [HOSTD_TEST_PASSED]\n", sh, dir) + extra
	if err := os.WriteFile(allow, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	logs := &logBuf{}
	ts := &testServer{
		srv: &Server{
			Socket:          filepath.Join(dir, "run", "hostd.sock"),
			Allowlist:       allow,
			AllowlistOwners: []uint32{uint32(os.Getuid())},
			StateDir:        filepath.Join(dir, "state"),
			DisconnectGrace: 500 * time.Millisecond,
			Env:             []string{"PATH=" + os.Getenv("PATH"), "HOSTD_TEST_PASSED=yes", "HOSTD_TEST_SECRET=no"},
			Logf:            logs.logf,
		},
		dir:  dir,
		logs: logs,
		done: make(chan error, 1),
	}
	if configure != nil {
		configure(ts.srv)
	}
	ts.client = Client{Socket: ts.srv.Socket}
	ctx, cancel := context.WithCancel(context.Background())
	ts.cancel = cancel
	go func() { ts.done <- ts.srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ts.done:
		case <-time.After(30 * time.Second):
			t.Error("Serve did not return")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ts.srv.Socket); err == nil {
			break
		}
		select {
		case err := <-ts.done:
			t.Fatalf("Serve: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("hostd never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ts
}

// sh runs script with args as its "$1" onwards.
func (ts *testServer) sh(ctx context.Context, script string, stdin string, args ...string) (Exit, string, string, error) {
	var out, errb bytes.Buffer
	exit, err := ts.client.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: append([]string{"-c", script, "sh"}, args...)},
		strings.NewReader(stdin), &out, &errb)
	return exit, out.String(), errb.String(), err
}

func TestServeSocketIsOwnerOnly(t *testing.T) {
	ts := startServer(t, "", nil)
	info, err := os.Stat(ts.srv.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %s, want 0600", perm)
	}
	if info, _ := os.Stat(filepath.Dir(ts.srv.Socket)); info.Mode().Perm() != 0o700 {
		t.Errorf("socket dir mode = %s, want 0700", info.Mode().Perm())
	}
}

func TestRunStreamsStdioAndExitStatus(t *testing.T) {
	ts := startServer(t, "", nil)
	exit, out, errOut, err := ts.sh(context.Background(),
		`cat; echo "to stderr" >&2; echo "passed=$HOSTD_TEST_PASSED secret=$HOSTD_TEST_SECRET"; exit 3`, "hello from stdin\n")
	if err != nil {
		t.Fatal(err)
	}
	if exit.Code != 3 {
		t.Errorf("exit = %+v, want code 3", exit)
	}
	if want := "hello from stdin\npassed=yes secret=\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	if errOut != "to stderr\n" {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestRunAppliesTheRequestedEnvAndWorkingDir(t *testing.T) {
	ts := startServer(t, "", nil)
	var out bytes.Buffer
	_, err := ts.client.Run(context.Background(), Request{Kind: KindCLI, Name: "sh",
		Args: []string{"-c", `echo "$GREETING"; pwd`}, Env: []string{"GREETING=hi"}, Dir: ts.dir}, nil, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(ts.dir)
	if out.String() != "hi\n"+real+"\n" && out.String() != "hi\n"+ts.dir+"\n" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestRunRefusesAndLogsWhatTheAllowlistDoesNotAccept(t *testing.T) {
	ts := startServer(t, "", nil)
	for _, req := range []Request{
		{Kind: KindCLI, Name: "bash"},
		{Kind: KindCLI, Name: "sh", Env: []string{"LD_PRELOAD=/tmp/x.so"}},
		{Kind: KindCLI, Name: "sh", Args: []string{"/etc/passwd"}},
		{Kind: KindCLI, Name: "sh", Dir: "/etc"},
	} {
		_, err := ts.client.Run(context.Background(), req, nil, nil, nil)
		var r *RefusedError
		if !errors.As(err, &r) {
			t.Errorf("%+v: err = %v, want a refusal", req, err)
		}
	}
	if n := strings.Count(ts.logs.String(), "hostd: refused:"); n != 4 {
		t.Errorf("logged %d refusals, want 4:\n%s", n, ts.logs.String())
	}
	if strings.Contains(ts.logs.String(), "starting") {
		t.Errorf("a refused request was started:\n%s", ts.logs.String())
	}
}

func TestOffersListsTheAllowlist(t *testing.T) {
	ts := startServer(t, "capabilities:\n  cap:\n    command: /bin/true\n", nil)
	offers, err := ts.client.Offers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 2 || offers[0].Kind != KindCapability || offers[1].Name != "sh" {
		t.Errorf("offers = %+v", offers)
	}
}

func TestAnAllowlistEditAppliesToTheNextRequest(t *testing.T) {
	ts := startServer(t, "", nil)
	if err := os.WriteFile(ts.srv.Allowlist, []byte("clis: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := ts.sh(context.Background(), "true", "")
	var r *RefusedError
	if !errors.As(err, &r) {
		t.Errorf("err = %v, want the removed entry refused", err)
	}
}

func TestRunRefusesAPeerRunningAsAnotherUser(t *testing.T) {
	// Exercised by making hostd believe it runs as someone else: the
	// kernel-reported peer uid is the test's own.
	ts := startServer(t, "", func(s *Server) { s.uid = uint32(os.Getuid()) + 1 })
	_, _, _, err := ts.sh(context.Background(), "echo ran", "")
	if err == nil {
		t.Fatal("a connection from another uid ran something")
	}
	if !strings.Contains(ts.logs.String(), "only uid") {
		t.Errorf("the refused peer was not logged:\n%s", ts.logs.String())
	}
}

// pidAlive reports whether pid still exists and is not a zombie.
func pidAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || !strings.HasPrefix(s[i+1:], " Z")
}

func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for pidAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d is still running after %s", pid, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// readPID waits for the file a script wrote its background child's pid to.
func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCancelKillsTheWholeProcessGroup(t *testing.T) {
	ts := startServer(t, "", nil)
	pidFile := filepath.Join(ts.dir, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		readPID(t, pidFile)
		cancel()
	}()
	start := time.Now()
	exit, _, _, err := ts.sh(ctx, `cd "$1" || exit; sleep 60 & echo $! > "$2"; wait`, "", ts.dir, filepath.Base(pidFile))
	if time.Since(start) > 20*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
	if err == nil && exit.Signal != "KILL" {
		t.Errorf("exit = %+v, %v; want killed", exit, err)
	}
	waitGone(t, readPID(t, pidFile), 10*time.Second)
}

func TestHostdEnforcesTheDeadlineItself(t *testing.T) {
	ts := startServer(t, "", nil)
	var out bytes.Buffer
	start := time.Now()
	// The request's deadline, not the client's context, is what ends it:
	// hostd enforces it even with nobody asking.
	exit, err := ts.client.Run(context.Background(), Request{Kind: KindCLI, Name: "sh",
		Args: []string{"-c", "echo started; sleep 60"}, Deadline: time.Now().Add(500 * time.Millisecond)}, nil, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !exit.Deadline || time.Since(start) > 20*time.Second {
		t.Errorf("exit = %+v after %s, want killed at its deadline", exit, time.Since(start))
	}
	if out.String() != "started\n" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestTheEntrysMaxDurationCapsALaterDeadline(t *testing.T) {
	sh, _ := exec.LookPath("sh")
	sh, _ = filepath.EvalSymlinks(sh)
	ts := startServer(t, fmt.Sprintf("  short:\n    path: %s\n    max_duration: 300ms\n", sh), nil)
	// The extra entry lands under clis: as it follows sh's own fields.
	exit, err := ts.client.Run(context.Background(), Request{Kind: KindCLI, Name: "short",
		Args: []string{"-c", "sleep 60"}, Deadline: time.Now().Add(time.Hour)}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !exit.Deadline {
		t.Errorf("exit = %+v, want killed at max_duration", exit)
	}
}

// The semantics agent.LocalHost has (host_waitdelay_test.go): a process
// that is gone while something it started still holds its output must not
// hold the run until that descendant exits too.
func TestRunReturnsOnceTheProcessIsGoneEvenIfAChildHoldsItsOutput(t *testing.T) {
	ts := startServer(t, "", nil)
	for _, tc := range []struct {
		name, script string
		wantCode     int
	}{
		{"killed", "echo started; sleep 60 & kill -9 $$", -1},
		{"exited", "echo started; sleep 60 & exit 0", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			exit, out, _, err := ts.sh(context.Background(), tc.script, "")
			if elapsed := time.Since(start); elapsed > 30*time.Second {
				t.Fatalf("Run took %s, want it bounded by WaitDelay", elapsed)
			}
			if err != nil || exit.Code != tc.wantCode {
				t.Errorf("exit = %+v, %v; want code %d", exit, err, tc.wantCode)
			}
			if !strings.Contains(out, "started") {
				t.Errorf("stdout = %q", out)
			}
		})
	}
}

func TestADisconnectedDaemonsProcessIsKilledAfterTheGracePeriod(t *testing.T) {
	ts := startServer(t, "", nil)
	pidFile := filepath.Join(ts.dir, "orphan.pid")
	conn, err := net.Dial("unix", ts.srv.Socket)
	if err != nil {
		t.Fatal(err)
	}
	// "trap '' HUP" and ignoring stdin: a process that would happily
	// outlive the daemon if hostd let it.
	if err := writeJSON(conn, frameRequest, Request{Op: OpStart, Kind: KindCLI, Name: "sh",
		Args: []string{"-c", `cd "$1" && echo $$ > "$2"; exec sleep 60`, "sh", ts.dir, filepath.Base(pidFile)}}); err != nil {
		t.Fatal(err)
	}
	if typ, _, err := readFrame(conn); err != nil || typ != frameStarted {
		t.Fatalf("first frame = %q, %v", typ, err)
	}
	pid := readPID(t, pidFile)
	conn.Close()
	waitGone(t, pid, 10*time.Second)
	if !strings.Contains(ts.logs.String(), "went away") {
		t.Errorf("the disconnect was not logged:\n%s", ts.logs.String())
	}
}

func TestAStartingHostdReapsWhatTheLastOneLeft(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	// A process group a previous hostd recorded and never cleaned up.
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { cmd.Wait(); close(waited) }()
	defer cmd.Process.Kill()
	ts := startServer(t, "", func(s *Server) {
		if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		start, err := procStartTime(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(s.StateDir, strconv.Itoa(cmd.Process.Pid)), []byte(start), 0o600)
		// A record whose pid is now somebody else's process is left alone.
		os.WriteFile(filepath.Join(s.StateDir, strconv.Itoa(os.Getpid())), []byte("1"), 0o600)
	})
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the recorded process group was not reaped")
	}
	if entries, _ := os.ReadDir(ts.srv.StateDir); len(entries) != 0 {
		t.Errorf("state dir still holds %d records", len(entries))
	}
}

// endless is stdin that never runs out, for a process that never reads it.
type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// waitLogged waits for hostd to have logged substr.
func waitLogged(t *testing.T, ts *testServer, substr string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(ts.logs.String(), substr) {
		if time.Now().After(deadline) {
			t.Fatalf("hostd never logged %q:\n%s", substr, ts.logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A process that never reads its stdin, with the daemon still sending it,
// must not stop hostd from seeing the KILL that cancelling sends: before
// stdin stopped blocking hostd's control loop, it ran on to max_duration.
func TestCancelKillsAProcessThatNeverReadsItsStdin(t *testing.T) {
	ts := startServer(t, "", nil)
	pidFile := filepath.Join(ts.dir, "deaf.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		exit Exit
		err  error
	}
	ran := make(chan result, 1)
	go func() {
		exit, err := ts.client.Run(ctx, Request{Kind: KindCLI, Name: "sh",
			Args: []string{"-c", `cd "$1" && echo $$ > "$2"; exec sleep 60`, "sh", ts.dir, filepath.Base(pidFile)}}, endless{}, nil, nil)
		ran <- result{exit, err}
	}()
	pid := readPID(t, pidFile)
	waitLogged(t, ts, "is not reading its stdin")
	start := time.Now()
	cancel()
	var r result
	select {
	case r = <-ran:
	case <-time.After(WaitDelay + 10*time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if r.err != nil || r.exit.Signal != "KILL" {
		t.Errorf("exit = %+v, %v; want hostd's report of the KILL", r.exit, r.err)
	}
	waitGone(t, pid, WaitDelay)
	if elapsed := time.Since(start); elapsed > WaitDelay+2*time.Second {
		t.Errorf("cancel took %s, want it within WaitDelay", elapsed)
	}
}

// The same process, with the daemon gone instead: the disconnect must
// still be seen, so DisconnectGrace starts and ends it.
func TestADisconnectReapsAProcessThatNeverReadsItsStdin(t *testing.T) {
	ts := startServer(t, "", nil)
	pidFile := filepath.Join(ts.dir, "deaf.pid")
	conn, err := net.Dial("unix", ts.srv.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeJSON(conn, frameRequest, Request{Op: OpStart, Kind: KindCLI, Name: "sh",
		Args: []string{"-c", `cd "$1" && echo $$ > "$2"; exec sleep 60`, "sh", ts.dir, filepath.Base(pidFile)}}); err != nil {
		t.Fatal(err)
	}
	if typ, _, err := readFrame(conn); err != nil || typ != frameStarted {
		t.Fatalf("first frame = %q, %v", typ, err)
	}
	pid := readPID(t, pidFile)
	chunk := bytes.Repeat([]byte("x"), maxFrame)
	for !strings.Contains(ts.logs.String(), "is not reading its stdin") {
		if err := writeFrame(conn, frameStdin, chunk); err != nil {
			t.Fatalf("hostd stopped reading the connection: %v", err)
		}
	}
	// One more, which hostd must drop rather than wait on.
	if err := writeFrame(conn, frameStdin, chunk); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	start := time.Now()
	waitGone(t, pid, ts.srv.DisconnectGrace+5*time.Second)
	t.Logf("gone %s after the disconnect (grace %s)", time.Since(start), ts.srv.DisconnectGrace)
	if !strings.Contains(ts.logs.String(), "went away") {
		t.Errorf("the disconnect was not logged:\n%s", ts.logs.String())
	}
}

func TestSocketOwnerAttributesOnlyHostdsOwnProcessTrees(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 to hold a socket in a child process")
	}
	ts := startServer(t, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A socket held by this test process itself -- same uid as hostd,
	// but not in any process tree hostd started -- must not be
	// attributed by hostd.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ownInode := socketInodeOf(t, ln)
	if pid, err := ts.client.SocketOwner(ctx, ownInode); err == nil {
		t.Fatalf("SocketOwner attributed a socket outside hostd's process trees to pid %d", pid)
	}

	// Start a process through hostd whose child opens a TCP listener and
	// writes its socket inode and pid: hostd attributes the socket to
	// that child while it runs, and refuses once it exits.
	pyScript := filepath.Join(ts.dir, "listen.py")
	pyBody := `import os, socket, sys, time
s = socket.socket()
s.bind(("127.0.0.1", 0))
s.listen(1)
inode = os.readlink(f"/proc/self/fd/{s.fileno()}")
with open(sys.argv[1], "w") as f:
    f.write(f"{os.getpid()} {inode}\n")
time.sleep(60)
`
	if err := os.WriteFile(pyScript, []byte(pyBody), 0o600); err != nil {
		t.Fatal(err)
	}
	readyFile := filepath.Join(ts.dir, "listener.ready")
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	started := make(chan Started, 1)
	client := ts.client
	client.OnStarted = func(st Started) { started <- st }
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.Run(runCtx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", `cd "$1" && python3 "$2" "$3"`, "sh", ts.dir, pyScript, filepath.Base(readyFile)}}, nil, nil, nil)
	}()
	st := <-started

	var childPID int
	var childInode uint64
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(readyFile); err == nil && strings.HasSuffix(string(data), "\n") {
			var link string
			if _, err := fmt.Sscanf(string(data), "%d %s", &childPID, &link); err == nil {
				link = strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
				childInode, _ = strconv.ParseUint(link, 10, 64)
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("child listener never wrote its inode")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == st.PID || childInode == 0 {
		t.Fatalf("child pid = %d (root %d), inode = %d", childPID, st.PID, childInode)
	}

	gotPID, err := ts.client.SocketOwner(ctx, childInode)
	if err != nil || gotPID != childPID {
		t.Fatalf("SocketOwner(%d) = %d, %v; want child pid %d", childInode, gotPID, err, childPID)
	}
	// Still cannot attribute this test process's own socket while a
	// hostd process is running.
	if pid, err := ts.client.SocketOwner(ctx, ownInode); err == nil {
		t.Fatalf("SocketOwner attributed a non-hostd socket to pid %d", pid)
	}

	stopRun()
	<-done
	waitGone(t, childPID, 10*time.Second)
	if pid, err := ts.client.SocketOwner(ctx, childInode); err == nil {
		t.Fatalf("after process exit, SocketOwner(%d) = %d, want error", childInode, pid)
	}
}

func socketInodeOf(t *testing.T, ln net.Listener) uint64 {
	t.Helper()
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
	inode, err := strconv.ParseUint(s, 10, 64)
	if err != nil || inode == 0 {
		t.Fatalf("readlink = %q: %v", link, err)
	}
	return inode
}
