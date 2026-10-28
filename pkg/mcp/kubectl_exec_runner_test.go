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

package mcp

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/kubectlgate"
	granuleagent "github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/guestexec"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

func TestKubectlExecArgsNameTheNamespacePodAndContainer(t *testing.T) {
	r := &KubectlExecRunner{Namespace: "grain", Pod: "grain-sandbox-7-1", Container: "sandbox"}
	got := strings.Join(r.execArgs([]string{"sh", "-c", "echo hi"}, false), " ")
	want := "-n grain exec -c sandbox grain-sandbox-7-1 -- sh -c " + statusTrailerScript + " grain-exec sh -c echo hi"
	if got != want {
		t.Errorf("execArgs = %q, want %q", got, want)
	}
}

func TestKubectlExecArgsPassKubeconfigWhenSet(t *testing.T) {
	r := &KubectlExecRunner{
		Kubeconfig: "/var/lib/grain/kubeconfig",
		Namespace:  "grain",
		Pod:        "grain-sandbox-7-1",
		Container:  "sandbox",
	}
	got := strings.Join(r.execArgs([]string{"sh", "-c", "echo hi"}, false), " ")
	want := "--kubeconfig /var/lib/grain/kubeconfig -n grain exec -c sandbox grain-sandbox-7-1 -- sh -c " + statusTrailerScript + " grain-exec sh -c echo hi"
	if got != want {
		t.Errorf("execArgs = %q, want %q", got, want)
	}
}

func TestKubectlExecArgsAddInteractiveOnlyForStdin(t *testing.T) {
	r := &KubectlExecRunner{Namespace: "grain", Pod: "p"}
	// Without stdin to pipe, -i would give the command a stdin nothing
	// ever writes to instead of the closed one it should see.
	if got := r.execArgs([]string{"true"}, false); contains(got, "-i") {
		t.Errorf("execArgs without stdin = %v, want no -i", got)
	}
	if got := r.execArgs([]string{"true"}, true); !contains(got, "-i") {
		t.Errorf("execArgs with stdin = %v, want -i", got)
	}
	// Never -t: a TTY merges stderr into stdout and translates newlines,
	// and every caller here wants the two streams apart.
	if got := r.execArgs([]string{"true"}, true); contains(got, "-t") {
		t.Errorf("execArgs = %v, want no -t", got)
	}
}

func TestKubectlExecArgsOmitTheContainerWhenUnnamed(t *testing.T) {
	r := &KubectlExecRunner{Namespace: "grain", Pod: "p"}
	// kubectl's own flags, before the "--": the shell after it has a -c
	// of its own.
	got := r.execArgs([]string{"true"}, false)
	for i, a := range got {
		if a == "--" {
			got = got[:i]
			break
		}
	}
	if contains(got, "-c") {
		t.Errorf("kubectl flags = %v, want no -c when no container is named", got)
	}
}

func TestKubectlExecPassesArgvThroughUnquoted(t *testing.T) {
	// argv is the command, not a string to be shell-quoted into one:
	// quoting here as well would double-quote every argument.
	r := &KubectlExecRunner{Namespace: "grain", Pod: "p"}
	args := r.execArgs([]string{"sh", "-c", "a b 'c'"}, false)
	if args[len(args)-1] != "a b 'c'" {
		t.Errorf("last arg = %q, want it verbatim", args[len(args)-1])
	}
}

func TestExecFailedBeforeContainerRecognizesKubectlsOwnFailures(t *testing.T) {
	// Both of these exit 1, exactly as a failing command in the pod
	// does, so the stream is the only thing that tells them apart.
	for _, stderr := range []string{
		"Error from server (NotFound): pods \"grain-sandbox-7-1\" not found\n",
		"error: unable to upgrade connection: container not found\n",
		"Error from server (Forbidden): pods \"p\" is forbidden\n",
		"Unable to connect to the server: dial tcp 136.70.201.174:443: connect: connection timed out\n",
		"E1025 07:40:01.123456     1 websocket.go:297] Unhandled Error\n" +
			"error: Post \"https://136.70.201.174/api/v1/namespaces/grain/pods/p/exec?command=sh&container=sandbox&stderr=true&stdout=true\": dial tcp 136.70.201.174:443: connect: connection timed out\n",
	} {
		if !execFailedBeforeContainer(stderr) {
			t.Errorf("execFailedBeforeContainer(%q) = false, want true", stderr)
		}
	}
	// A command's own stderr is not kubectl's.
	for _, stderr := range []string{"make: *** [all] Error 1\n", "", "go: cannot find module\n"} {
		if execFailedBeforeContainer(stderr) {
			t.Errorf("execFailedBeforeContainer(%q) = true, want false", stderr)
		}
	}
}

// TestKubectlExecRunnerReportsRealExitStatus runs against a stand-in
// kubectl, which is enough to prove the one contract remoteRunner's
// callers depend on: a command that ran gets its own status back, and
// one that never ran gets -1.
func TestKubectlExecRunnerReportsRealExitStatus(t *testing.T) {
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\n" +
		// The command is everything after the "--".
		"case \"$*\" in\n" +
		"  *'grain-exec fail'*) echo 'went wrong' >&2; exit 3 ;;\n" +
		"  *'grain-exec missing'*) echo 'Error from server (NotFound): pods \"p\" not found' >&2; exit 1 ;;\n" +
		"esac\n" +
		"echo ok\n" + fakeStatusTrailerOK
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}

	stdout, _, code := r.Run(context.Background(), []string{"true"}, "")
	if code != 0 || strings.TrimSpace(stdout) != "ok" {
		t.Errorf("Run of a working command = (%q, %d), want (ok, 0)", stdout, code)
	}
	if _, _, code := r.Run(context.Background(), []string{"fail"}, ""); code != 3 {
		t.Errorf("exit code = %d, want the pod command's own 3", code)
	}
	// A pod that is not there did not run the command at all, which is
	// -1 rather than a status the command chose.
	if _, _, code := r.Run(context.Background(), []string{"missing"}, ""); code != -1 {
		t.Errorf("exit code against a missing pod = %d, want -1", code)
	}
}

// TestKubectlExecArgsPutTheTrailerBeyondThrough is why Through is a
// field: the shell that reports the command's status has to run where
// the command does, inside the guest, so that a guest session torn down
// under `granule exec` cannot be answered for by a shell in the pod.
func TestKubectlExecArgsPutTheTrailerBeyondThrough(t *testing.T) {
	r := &KubectlExecRunner{Namespace: "grain", Pod: "p", Through: []string{"granule", "exec", "--"}}
	got := strings.Join(r.execArgs([]string{"uname", "-a"}, false), " ")
	want := "-n grain exec p -- granule exec -- sh -c " + statusTrailerScript + " grain-exec uname -a"
	if got != want {
		t.Errorf("execArgs = %q, want %q", got, want)
	}
}

// passThroughKubectl is a kubectl stand-in that runs the command it was
// handed -- everything after its "--" -- on this machine, so the real
// statusTrailerScript runs as it would in a pod.
func passThroughKubectl(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	script := "#!/bin/sh\n" +
		"while [ \"$1\" != -- ]; do shift; done\n" +
		"shift\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	return path
}

func TestKubectlExecRunnerTakesTheStatusFromTheTrailer(t *testing.T) {
	r := &KubectlExecRunner{Kubectl: passThroughKubectl(t), Namespace: "grain", Pod: "p"}
	stdout, stderr, code := r.Run(context.Background(), []string{"sh", "-c", "echo out; printf 'no newline' >&2; exit 3"}, "")
	if code != 3 || stdout != "out\n" {
		t.Errorf("Run = (%q, %d), want (out, 3)", stdout, code)
	}
	// The trailer is taken back out, leaving the command's stderr
	// exactly as it wrote it.
	if stderr != "no newline" {
		t.Errorf("stderr = %q, want the command's own %q", stderr, "no newline")
	}
	stdout, stderr, code = r.Run(context.Background(), []string{"cat"}, "piped in")
	if code != 0 || stdout != "piped in" || stderr != "" {
		t.Errorf("Run of cat = (%q, %q, %d), want stdin through and exit 0", stdout, stderr, code)
	}
	var live bytes.Buffer
	_, stderr, code = r.RunStream(context.Background(), []string{"sh", "-c", "echo warn >&2"}, "", &live)
	if code != 0 || stderr != "warn\n" || live.String() != "warn\n" {
		t.Errorf("RunStream = (%q, %d), live %q; want the trailer in neither", stderr, code, live.String())
	}
}

// cutOffKubectl is a kubectl stand-in for a session torn down mid-command:
// it prints what the command had printed so far, then exits as kubectl
// does when the exec's stream closes with no status on it -- 0, unless
// exit says otherwise -- with no trailer, because the command never got
// as far as finishing.
func cutOffKubectl(t *testing.T, stderr string, exit int) (path string, count func() int) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	script := "#!/bin/sh\n" +
		"echo x >> " + counter + "\n" +
		"echo 'go build ./cmd/grain'\n" +
		"printf '%s' '" + stderr + "' >&2\n" +
		"exit " + strconv.Itoa(exit) + "\n"
	path = filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	return path, func() int {
		data, _ := os.ReadFile(counter)
		return strings.Count(string(data), "x")
	}
}

func TestKubectlExecRunnerReportsASessionCutOffMidCommandAsAFailure(t *testing.T) {
	kubectl, _ := cutOffKubectl(t, "compiling\n", 0)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	var live bytes.Buffer
	stdout, stderr, code := r.RunStream(context.Background(), []string{"make", "build"}, "", &live)
	if code != -1 {
		t.Fatalf("exit code = %d, want -1: kubectl's 0 came with no status from the command", code)
	}
	if stdout != "go build ./cmd/grain\n" || !strings.HasPrefix(stderr, "compiling\n") {
		t.Errorf("Run = (%q, %q), want the partial output kept", stdout, stderr)
	}
	if !strings.Contains(stderr, SessionCutOffNotice) {
		t.Errorf("stderr = %q, want it to say the session was cut off", stderr)
	}
	if !strings.Contains(live.String(), "compiling") {
		t.Errorf("live = %q, want the partial output followed live", live.String())
	}
}

func TestKubectlExecRunnerReportsKubectlLosingTheStreamAsAFailure(t *testing.T) {
	// kubectl's own complaint after the command had started: its 1 is
	// not the command's.
	kubectl, _ := cutOffKubectl(t, "compiling\nerror: error reading from error stream: connection reset by peer\n", 1)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	if _, stderr, code := r.Run(context.Background(), []string{"make", "build"}, ""); code != -1 || !strings.Contains(stderr, SessionCutOffNotice) {
		t.Errorf("Run = (%q, %d), want -1 and the cut-off notice", stderr, code)
	}
}

func TestKubectlExecRunnerDoesNotRetryASessionCutOffMidCommand(t *testing.T) {
	// Even when what the command had printed reads like the dial
	// timeout Run does retry: the command started, and may have done
	// half of what it does.
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	kubectl, count := cutOffKubectl(t, apiServerDialTimeout+"\n", 0)
	r := &KubectlExecRunner{Kubectl: kubectl, Namespace: "grain", Pod: "p"}
	if _, _, code := r.Run(context.Background(), []string{"curl", "http://10.0.0.1"}, ""); code != -1 {
		t.Errorf("exit code = %d, want -1", code)
	}
	if n := count(); n != 1 {
		t.Errorf("kubectl ran %d times, want 1", n)
	}
}

func TestKubectlExecRunnerReportsAGuestSessionCutOffUnderThrough(t *testing.T) {
	// `granule exec` ending with nothing to say for the guest's command
	// (here: printing part of it and exiting 0) is a cut-off session
	// too, which only holds because the trailer runs beyond Through.
	dir := t.TempDir()
	granule := filepath.Join(dir, "granule")
	if err := os.WriteFile(granule, []byte("#!/bin/sh\necho partial\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &KubectlExecRunner{Kubectl: passThroughKubectl(t), Namespace: "grain", Pod: "p", Through: []string{granule, "exec", "--"}}
	var live bytes.Buffer
	stdout, stderr, code := r.RunStream(context.Background(), []string{"make", "build"}, "", &live)
	if code != -1 || stdout != "partial\n" || !strings.Contains(stderr, SessionCutOffNotice) {
		t.Errorf("RunStream = (%q, %q, %d), want -1 and the cut-off notice", stdout, stderr, code)
	}
	if !strings.Contains(live.String(), SessionCutOffNotice) {
		t.Errorf("RunStream live output = %q, want the cut-off notice streamed to the live writer", live.String())
	}
}

func TestStatusTrailerFindsATrailerArrivingInPieces(t *testing.T) {
	var got bytes.Buffer
	tr := &statusTrailer{to: &got}
	for _, chunk := range []string{"a\x1e", "b\n", "warn\n\x1egrain-ex", "ec-stat", "us=4", "2\n"} {
		if _, err := tr.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	tr.flush()
	if !tr.seen || tr.status != 42 {
		t.Errorf("trailer = (%v, %d), want (true, 42)", tr.seen, tr.status)
	}
	// A stray record separator is ordinary output, passed on.
	if got.String() != "a\x1eb\nwarn\n" {
		t.Errorf("passed on %q, want %q", got.String(), "a\x1eb\nwarn\n")
	}
}

func TestStatusTrailerPassesOnAnUnfinishedTrailer(t *testing.T) {
	var got bytes.Buffer
	tr := &statusTrailer{to: &got}
	tr.Write([]byte("out\x1egrain-exec-sta"))
	tr.flush()
	if tr.seen || got.String() != "out\x1egrain-exec-sta" {
		t.Errorf("trailer seen %v, passed on %q; want no trailer and every byte back", tr.seen, got.String())
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestContainerExecRunnerUser(t *testing.T) {
	r := &ContainerExecRunner{Container: "c"}
	if got := strings.Join(r.execArgs([]string{"id"}, true), " "); got != "exec -i c id" {
		t.Errorf("execArgs = %q, want no -u when User is empty", got)
	}
	r.User = "0"
	if got := strings.Join(r.execArgs([]string{"id"}, true), " "); got != "exec -i -u 0 c id" {
		t.Errorf("execArgs = %q, want -u 0 before the container", got)
	}
}

func TestKubectlDialArgsIncludeThroughWithoutStatusTrailer(t *testing.T) {
	r := &KubectlExecRunner{
		Namespace: "grain",
		Pod:       "grain-granule-7-1",
		Container: "sandbox",
		Through:   []string{"granule", "exec", "--"},
	}
	got := strings.Join(r.dialArgs([]string{"sh", "-c", "exec grain-connect 127.0.0.1:4280"}), " ")
	want := "-n grain exec -i -c sandbox grain-granule-7-1 -- granule exec -- sh -c exec grain-connect 127.0.0.1:4280"
	if got != want {
		t.Errorf("dialArgs = %q, want %q", got, want)
	}
}

func TestContainerExecRunnerStreamsBothStreamsToAFollower(t *testing.T) {
	writeLocalDockerShim(t)
	r := &ContainerExecRunner{Container: "c"}
	var followed strings.Builder
	stdout, stderr, code := r.RunStream(context.Background(), []string{"sh", "-c", "echo out; echo err >&2"}, "", &followed)
	if code != 0 || stdout != "out\n" || stderr != "err\n" {
		t.Fatalf("RunStream = (%q, %q, %d), want (\"out\\n\", \"err\\n\", 0)", stdout, stderr, code)
	}
	if !strings.Contains(followed.String(), "out\n") || !strings.Contains(followed.String(), "err\n") {
		t.Errorf("followed = %q, want both stdout and stderr", followed.String())
	}
}

func startInProcessGranuleServer(t *testing.T, podName string) (addr string, spec granule.SandboxAccessSpec, dropActiveConns func()) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	ca, err := granule.NewSandboxCA()
	if err != nil {
		t.Fatalf("NewSandboxCA: %v", err)
	}
	certPEM, keyPEM, err := ca.IssueSandboxCert(podName, podName)
	if err != nil {
		t.Fatalf("IssueSandboxCert: %v", err)
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	token, err := granule.MintToken()
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := sandbox.NewServer(sandbox.ServerConfig{
		SandboxID: podName,
		Token:     token,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{tlsCert},
		},
		Guest: guestexec.Config{
			User: u.Username,
		},
		DialGuest: func(dialCtx context.Context) (net.Conn, error) {
			if err := dialCtx.Err(); err != nil {
				return nil, err
			}
			c1, c2 := net.Pipe()
			go func() {
				defer c2.Close()
				_ = granuleagent.Serve(ctx, c2)
			}()
			return c1, nil
		},
	})

	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = rawLn.Close() })

	var mu sync.Mutex
	var conns []net.Conn
	trackLn := &trackingListener{
		Listener: rawLn,
		onAccept: func(c net.Conn) {
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		},
	}
	go func() {
		_ = srv.Serve(ctx, trackLn)
	}()

	tcpAddr := rawLn.Addr().(*net.TCPAddr)
	spec = granule.SandboxAccessSpec{
		Mode:      granule.AccessModeDirect,
		PodName:   podName,
		SandboxID: podName,
		Token:     token,
		CACertPEM: string(ca.CAPEM()),
		Port:      tcpAddr.Port,
		PodIP:     "127.0.0.1",
	}
	drop := func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
		conns = nil
	}
	return rawLn.Addr().String(), spec, drop
}

type trackingListener struct {
	net.Listener
	onAccept func(net.Conn)
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil && l.onAccept != nil {
		l.onAccept(c)
	}
	return c, err
}

func TestGranuleSandboxRunnerRunStreamAndPutFiles(t *testing.T) {
	_, spec, _ := startInProcessGranuleServer(t, "grain-granule-42-1")
	u, _ := user.Current()
	runner, err := NewGranuleSandboxRunnerFromSpec(spec, nil, nil)
	if err != nil {
		t.Fatalf("NewGranuleSandboxRunnerFromSpec: %v", err)
	}
	runner.User = u.Username
	t.Cleanup(func() { _ = runner.Close() })

	ctx := context.Background()
	info, err := runner.Health(ctx)
	if err != nil || !info.Ready {
		t.Fatalf("Health = (%+v, %v), want Ready=true", info, err)
	}

	var followed strings.Builder
	stdout, stderr, code := runner.RunStream(ctx, []string{"sh", "-c", "echo hello-out; echo hello-err >&2; exit 7"}, "", &followed)
	if code != 7 || stdout != "hello-out\n" || stderr != "hello-err\n" {
		t.Fatalf("RunStream = (%q, %q, %d), want (hello-out, hello-err, 7)", stdout, stderr, code)
	}
	if !strings.Contains(followed.String(), "hello-out\n") || !strings.Contains(followed.String(), "hello-err\n") {
		t.Errorf("followed = %q, want both streams", followed.String())
	}

	dir := t.TempDir()
	f1 := filepath.Join(dir, "nested", "a.txt")
	f2 := filepath.Join(dir, "nested", "b.sh")
	if err := PlaceFilesOverSSH(ctx, runner, []FilePlacement{
		{Path: f1, Content: []byte("alpha"), Mode: "0600"},
		{Path: f2, Content: []byte("#!/bin/sh\necho beta\n"), Mode: "0755"},
	}); err != nil {
		t.Fatalf("PlaceFilesOverSSH: %v", err)
	}
	got1, err := os.ReadFile(f1)
	if err != nil || string(got1) != "alpha" {
		t.Fatalf("ReadFile(%s) = (%q, %v), want alpha", f1, got1, err)
	}
	out2, stderr2, code2 := runner.Run(ctx, []string{f2}, "")
	if code2 != 0 || out2 != "beta\n" {
		t.Fatalf("running placed script: code=%d stdout=%q stderr=%q", code2, out2, stderr2)
	}

	writeTool := sshWriteFileTool(runner, dir)
	f3 := filepath.Join(dir, "tool-written.txt")
	res := writeTool.Handler(ctx, map[string]any{
		"file_path": f3,
		"content":   "written-via-putfiles",
	})
	if res.IsError {
		t.Fatalf("write_file returned error: %s", res.Text)
	}
	got3, err := os.ReadFile(f3)
	if err != nil || string(got3) != "written-via-putfiles" {
		t.Fatalf("ReadFile(%s) = (%q, %v), want written-via-putfiles", f3, got3, err)
	}
}

func TestGranuleSandboxRunnerReattachesAfterConnectionDrop(t *testing.T) {
	_, spec, dropConns := startInProcessGranuleServer(t, "grain-granule-99-1")
	u, _ := user.Current()
	runner, err := NewGranuleSandboxRunnerFromSpec(spec, nil, nil)
	if err != nil {
		t.Fatalf("NewGranuleSandboxRunnerFromSpec: %v", err)
	}
	runner.User = u.Username
	t.Cleanup(func() { _ = runner.Close() })

	// Drop the TCP connection as soon as the first line arrives on the live stream.
	dropped := make(chan struct{})
	var once sync.Once
	live := writerFunc(func(p []byte) (int, error) {
		if bytes.Contains(p, []byte("part-1")) {
			once.Do(func() {
				close(dropped)
				go dropConns()
			})
		}
		return len(p), nil
	})

	stdout, stderr, code := runner.RunStream(
		context.Background(),
		[]string{"sh", "-c", "echo part-1; sleep 0.25; echo part-2; exit 3"},
		"",
		live,
	)
	select {
	case <-dropped:
	default:
		t.Fatal("expected live stream to trigger connection drop")
	}
	if code != 3 || stdout != "part-1\npart-2\n" {
		t.Fatalf("after Reattach: code=%d stdout=%q stderr=%q, want code=3 stdout=\"part-1\\npart-2\\n\"", code, stdout, stderr)
	}
}
