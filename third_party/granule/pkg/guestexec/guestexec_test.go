package guestexec

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

	"golang.org/x/sys/unix"

	"github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/execwire"
)

// fakeCHV stands in for cloud-hypervisor's hybrid-vsock endpoint: a unix
// socket that answers the CONNECT handshake and then hands the
// connection to the real guest-side agent.
//
// Using the real pkg/agent rather than a scripted responder is the
// point. What these tests are for is the seam between the two halves --
// the handshake, the framing, which stream carries what, where the exit
// code comes from -- and a fake on the far side would only ever agree
// with whatever this side happens to do. There is no VM here, so the
// commands run on the build machine; everything else is the production
// path.
type fakeCHV struct {
	socket string

	// refuse makes the endpoint answer the handshake the way
	// cloud-hypervisor does when nothing inside the guest is listening
	// on the port yet: by closing the connection.
	refuse bool

	// hangUpMidSession closes the connection after the first output
	// frame instead of letting the session finish, standing in for a VM
	// that died with a command still running.
	hangUpMidSession bool

	// olderAgent stands in for a guest image built before Request.Dir
	// and Request.Env existed: it runs the command line and nothing
	// else, and its response names no features -- which is what an agent
	// that ignored those fields looks like from the client's side.
	olderAgent bool

	// agentWithoutSignal answers the way an agent built before the
	// signal frame existed would: a response naming no features, and a
	// session that steps over frames it has no idea what to do with. It
	// stands in for a guest image and a granule binary from different
	// commits.
	agentWithoutSignal bool

	// noteBeforeExit answers with output, a note frame and then an exit
	// code, which is what an agent sends for a session whose output it
	// stopped waiting for (pkg/agent's outputGrace). Standing in
	// for it here rather than provoking a real one keeps this about the
	// client: the command exited, so the exit code is the answer, and
	// the note is not part of it.
	noteBeforeExit bool

	// recordRequest keeps the request the client opened the session with
	// and answers it without running anything, for the tests that are
	// about what this client asks for rather than what a guest does with
	// it.
	recordRequest bool

	mu  sync.Mutex
	req execwire.Request
}

func startFakeCHV(t *testing.T, f *fakeCHV) *fakeCHV {
	t.Helper()
	if f == nil {
		f = &fakeCHV{}
	}
	// Short path: a unix socket's address has a hard length limit
	// (~108 bytes) that a nested t.TempDir() under a long test name can
	// exceed, which fails as a confusing "invalid argument" on bind.
	dir, err := os.MkdirTemp("", "chv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f.socket = filepath.Join(dir, "vsock.sock")

	ln, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeCHV) serve(conn net.Conn) {
	defer conn.Close()

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "CONNECT ") {
		return
	}
	f.mu.Lock()
	refuse := f.refuse
	f.mu.Unlock()
	if refuse {
		return
	}
	if _, err := fmt.Fprintf(conn, "OK %d\n", 4242); err != nil {
		return
	}

	if f.hangUpMidSession {
		f.serveThenHangUp(conn, br)
		return
	}
	if f.olderAgent {
		f.serveAsOlderAgent(conn, br)
		return
	}
	if f.recordRequest {
		f.serveAndRecord(conn, br)
		return
	}
	if f.agentWithoutSignal {
		f.serveAsAnAgentWithoutSignal(conn, br)
		return
	}
	if f.noteBeforeExit {
		f.serveWithANote(conn, br)
		return
	}
	// br may hold bytes already read off the socket, so the agent has to
	// be given the buffered reader rather than the connection.
	_ = agent.Serve(context.Background(), bufConn{r: br, Conn: conn})
}

// bufConn reads through a buffer that has already consumed part of the
// stream, while writing straight to the connection.
type bufConn struct {
	r *bufio.Reader
	net.Conn
}

func (c bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// serveAsOlderAgent runs the command line, ignoring every optional field
// on the request, and answers with the featureless response an agent
// built before those fields would have sent.
func (f *fakeCHV) serveAsOlderAgent(conn net.Conn, br *bufio.Reader) {
	req, err := execwire.ReadRequest(br)
	if err != nil {
		return
	}
	out, err := exec.Command("/bin/sh", "-c", req.Line).Output()
	if err != nil {
		_ = execwire.WriteResponse(conn, execwire.Response{OK: false, Error: err.Error()})
		return
	}
	_ = execwire.WriteResponse(conn, execwire.Response{OK: true})
	_ = execwire.WriteFrame(conn, execwire.TypeStdout, out)
	_ = execwire.WriteFrame(conn, execwire.TypeExit, execwire.EncodeExit(0))
}

// serveAndRecord keeps the request and ends the session immediately,
// exiting zero. Nothing is run: the assertion is on what the client sent.
func (f *fakeCHV) serveAndRecord(conn net.Conn, br *bufio.Reader) {
	req, err := execwire.ReadRequest(br)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.req = req
	f.mu.Unlock()

	if err := execwire.WriteResponse(conn, execwire.Response{OK: true, Features: execwire.Features}); err != nil {
		return
	}
	_ = execwire.WriteFrame(conn, execwire.TypeExit, execwire.EncodeExit(0))
}

// request returns what serveAndRecord kept.
func (f *fakeCHV) request() execwire.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.req
}

// serveThenHangUp answers the request, writes one output frame, and
// drops the connection without ever sending an exit frame.
func (f *fakeCHV) serveThenHangUp(conn net.Conn, br *bufio.Reader) {
	if _, err := execwire.ReadRequest(br); err != nil {
		return
	}
	_ = execwire.WriteResponse(conn, execwire.Response{OK: true})
	_ = execwire.WriteFrame(conn, execwire.TypeStdout, []byte("partial output"))
}

// serveAsAnAgentWithoutSignal accepts a session, names no features, and
// then steps over every frame the client sends -- which is precisely
// what an agent that does not know TypeSignal would do with one.
func (f *fakeCHV) serveAsAnAgentWithoutSignal(conn net.Conn, br *bufio.Reader) {
	if _, err := execwire.ReadRequest(br); err != nil {
		return
	}
	if err := execwire.WriteResponse(conn, execwire.Response{OK: true}); err != nil {
		return
	}
	for {
		if _, _, err := execwire.ReadFrame(br); err != nil {
			return
		}
	}
}

// serveWithANote answers with one output frame, a note about the session
// itself, and the command's exit code.
func (f *fakeCHV) serveWithANote(conn net.Conn, br *bufio.Reader) {
	if _, err := execwire.ReadRequest(br); err != nil {
		return
	}
	if err := execwire.WriteResponse(conn, execwire.Response{OK: true, Features: execwire.Features}); err != nil {
		return
	}
	_ = execwire.WriteFrame(conn, execwire.TypeStdout, []byte("as far as it got"))
	_ = execwire.WriteFrame(conn, execwire.TypeNote, []byte("the session stopped waiting for output"))
	_ = execwire.WriteFrame(conn, execwire.TypeExit, execwire.EncodeExit(5))
}

// testGrace is the cancellation grace every test here runs with, in
// place of DefaultCancelGrace. It is deliberately far longer than
// anything these tests should need: the grace is a ceiling, so a
// generous one costs a passing test nothing (a session ends when the
// exit frame arrives, not when the grace does), while a tight one makes
// every cancellation test a race between the production constant and
// however long a loaded build machine takes to deliver a signal, run a
// shell trap and drain the output -- which is what made
// TestRun_CancellationInterruptsTheCommandAndStillCollectsItsAnswer fail
// intermittently under "go test -race" against several packages at once.
// A test that wants to assert on the grace itself sets its own.
const testGrace = 60 * time.Second

func testConfig(t *testing.T, f *fakeCHV) Config {
	t.Helper()
	return Config{Socket: f.socket, User: currentAccount(t), ConnectTimeout: 2 * time.Second, CancelGrace: testGrace}
}

// currentAccount names the account this test process runs as. The agent
// switches to whichever account a request names, and switching to a
// different one needs privileges a test runner does not have -- so a
// test can only ask for the one it already has. The production default
// (root) is what the agent gets in a guest, where it runs as root.
func currentAccount(t *testing.T) string {
	t.Helper()
	f, err := os.Open("/etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	uid := strconv.Itoa(os.Getuid())
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) >= 7 && fields[2] == uid {
			return fields[0]
		}
	}
	t.Fatalf("no account in /etc/passwd for uid %s", uid)
	return ""
}

func TestFromEnv_DefaultsNeedNothingSetPerVM(t *testing.T) {
	// The SSH transport required GRANULE_EXEC_ADDR, which every backend
	// had to compute and set. Needing nothing is the point of the
	// replacement, so it is worth asserting rather than assuming.
	t.Setenv(envSocket, "")
	t.Setenv(envUser, "")
	t.Setenv(envConnectTimeout, "")
	t.Setenv(envCancelGrace, "")
	t.Setenv(envReadyFile, "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv with nothing set = %v, want a usable default config", err)
	}
	// The guest-declared check is opt-in: nothing set means readiness
	// keeps meaning "the agent answered", for every guest image that
	// knows nothing about it.
	if cfg.ReadyFile != "" {
		t.Errorf("ReadyFile = %q, want it unset unless configured", cfg.ReadyFile)
	}
	if cfg.Socket != DefaultSocket {
		t.Errorf("Socket = %q, want %q", cfg.Socket, DefaultSocket)
	}
	if cfg.User != defaultUser {
		t.Errorf("User = %q, want %q", cfg.User, defaultUser)
	}
	if cfg.ConnectTimeout != defaultConnectTimeout {
		t.Errorf("ConnectTimeout = %v, want %v", cfg.ConnectTimeout, defaultConnectTimeout)
	}
	if cfg.CancelGrace != DefaultCancelGrace {
		t.Errorf("CancelGrace = %v, want %v", cfg.CancelGrace, DefaultCancelGrace)
	}
}

func TestFromEnv_Overrides(t *testing.T) {
	t.Setenv(envSocket, "/tmp/other.sock")
	t.Setenv(envUser, "debian")
	t.Setenv(envConnectTimeout, "90s")
	t.Setenv(envCancelGrace, "45s")
	t.Setenv(envReadyFile, "/run/granule/ready")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Socket != "/tmp/other.sock" || cfg.User != "debian" || cfg.ConnectTimeout != 90*time.Second {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.CancelGrace != 45*time.Second {
		t.Fatalf("CancelGrace = %v, want the value %s named", cfg.CancelGrace, envCancelGrace)
	}
	if cfg.ReadyFile != "/run/granule/ready" {
		t.Fatalf("ReadyFile = %q, want the value %s named", cfg.ReadyFile, envReadyFile)
	}
}

func TestFromEnv_InvalidConnectTimeout(t *testing.T) {
	t.Setenv(envConnectTimeout, "not-a-duration")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv = nil, want an error naming the bad duration")
	}
}

func TestFromEnv_InvalidCancelGrace(t *testing.T) {
	t.Setenv(envCancelGrace, "not-a-duration")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv = nil, want an error naming the bad duration")
	}
}

func TestRun_ExecutesGivenCommandAndReportsExitCode(t *testing.T) {
	f := startFakeCHV(t, nil)

	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), testConfig(t, f), []string{"echo", "hello world"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, stderr.String())
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	// Shell-quoted and joined, so an argument with a space stays one
	// argument rather than becoming two.
	if stdout.String() != "hello world\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "hello world\n")
	}
}

// A command's non-zero exit is the command's answer, not a failure of
// "granule exec" -- the caller reads it from the code, and an error here
// would make every failing command look like a broken transport.
func TestRun_NonZeroExitIsNotAnError(t *testing.T) {
	f := startFakeCHV(t, nil)

	code, err := RunLine(context.Background(), testConfig(t, f), "exit 17", nil, nil, nil)
	if err != nil {
		t.Fatalf("RunLine returned an error for a non-zero exit: %v", err)
	}
	if code != 17 {
		t.Errorf("exit code = %d, want 17", code)
	}
}

// The two streams stay apart end to end. grain's sandbox tools read them
// separately, and merging them is exactly the corruption the guest's old
// SSH console wrapper caused.
func TestRun_KeepsStdoutAndStderrApart(t *testing.T) {
	f := startFakeCHV(t, nil)

	var stdout, stderr bytes.Buffer
	if _, err := RunLine(context.Background(), testConfig(t, f), "echo out; echo err >&2", nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "out\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if stderr.String() != "err\n" {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunLine_SendsLineVerbatim(t *testing.T) {
	f := startFakeCHV(t, nil)

	// A line RunLine must not re-quote: the guest's shell has to see the
	// pipe and the variable, not a single literal argument.
	var stdout bytes.Buffer
	if _, err := RunLine(context.Background(), testConfig(t, f), "echo one two three | wc -w", nil, &stdout, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != "3" {
		t.Fatalf("stdout = %q, want the pipeline to have run guest-side", stdout.String())
	}
}

// The terminal is on this side of the connection, so this side is the
// only one that can say what kind it is. Nothing in the guest can see
// it, and an agent left to its own environment puts TERM=dumb in a
// session that really is a terminal -- which is every curses program on
// the far side refusing to draw (see pkg/agent).
func TestRunLine_TheClientsTerminalTypeTravelsWithThePTYRequest(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{recordRequest: true})
	t.Setenv("TERM", "xterm-256color")

	_, tty := openPTY(t)
	if _, err := RunLine(context.Background(), testConfig(t, f), "true", tty, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	req := f.request()
	if !req.TTY {
		t.Fatal("a terminal on stdin did not ask the guest for a pty")
	}
	if req.Term != "xterm-256color" {
		t.Errorf("Term = %q, want the terminal type this client is running under", req.Term)
	}
}

// A session with no terminal names none: "dumb" is the right answer for
// it, and it is the agent's to supply rather than something this end
// asserts on behalf of a terminal that is not there.
func TestRunLine_ASessionWithoutAPTYNamesNoTerminalType(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{recordRequest: true})
	t.Setenv("TERM", "xterm-256color")

	if _, err := RunLine(context.Background(), testConfig(t, f), "true", strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	req := f.request()
	if req.TTY {
		t.Fatal("a pipe on stdin asked the guest for a pty")
	}
	if req.Term != "" {
		t.Errorf("Term = %q, want nothing sent for a session with no terminal", req.Term)
	}
}

// openPTY opens a pseudo-terminal pair, so a test can hand RunLine a
// stdin that really is one. Opened by hand for the same reason
// pkg/agent's pty.go does it: there is no pty library in this tree,
// and the sequence is four calls.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty available on this machine: %v", err)
	}
	t.Cleanup(func() { ptmx.Close() })

	if err := unix.IoctlSetPointerInt(int(ptmx.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlocking the pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(ptmx.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("naming the pty: %v", err)
	}
	// O_NOCTTY: the test process must not adopt this terminal as its own.
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("opening the pty's other end: %v", err)
	}
	t.Cleanup(func() { slave.Close() })
	return ptmx, slave
}

func TestRun_ForwardsStdin(t *testing.T) {
	f := startFakeCHV(t, nil)

	var stdout bytes.Buffer
	code, err := Run(context.Background(), testConfig(t, f), []string{"cat"}, strings.NewReader("from the caller\n"), &stdout, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d: cat never saw end of input", code)
	}
	if stdout.String() != "from the caller\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// A guest still booting has no agent listening yet, and
// cloud-hypervisor answers by closing the connection. That is the
// ordinary case the retry loop exists for, so it must be bounded by
// ConnectTimeout rather than either giving up at once or retrying
// forever.
func TestRun_RetriesUntilTheTimeoutWhenNothingIsListeningInTheGuest(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{refuse: true})

	cfg := testConfig(t, f)
	cfg.ConnectTimeout = 750 * time.Millisecond

	start := time.Now()
	_, err := Run(context.Background(), cfg, []string{"true"}, nil, nil, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Run = nil, want a timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want it to name the timeout", err)
	}
	if elapsed < cfg.ConnectTimeout {
		t.Errorf("gave up after %v, before the %v timeout: it is not retrying", elapsed, cfg.ConnectTimeout)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v to give up on a %v timeout", elapsed, cfg.ConnectTimeout)
	}
}

// A VM that dies mid-command must not look like a command that
// succeeded. The exit frame is the only thing that ends a session, so a
// stream that stops without one is an error rather than exit 0.
func TestRun_AGuestThatVanishesMidCommandIsAnError(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{hangUpMidSession: true})

	var stdout bytes.Buffer
	code, err := RunLine(context.Background(), testConfig(t, f), "does not matter", nil, &stdout, nil)
	if err == nil {
		t.Fatalf("Run = nil (code %d), want an error for a session with no exit status", code)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0 alongside the error", code)
	}
	if !strings.Contains(err.Error(), "exit status") {
		t.Errorf("error = %v, want it to say no exit status arrived", err)
	}
}

// A note is the agent talking about the session rather than the command
// talking: it says the output stops short of what the command wrote (see
// pkg/agent's outputGrace), and the exit code beside it is still
// the command's own. So it must not become the command's output, and it
// must not turn a finished session into an error either -- it goes to
// this process's log, where a caller reading the guest's answer
// programmatically will not mistake it for part of one.
func TestRun_ANoteIsNotTheCommandsOutputAndDoesNotFailTheSession(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{noteBeforeExit: true})

	var stdout, stderr bytes.Buffer
	code, err := RunLine(context.Background(), testConfig(t, f), "does not matter", nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunLine = %v, want the session to have ended with the command's status", err)
	}
	if code != 5 {
		t.Errorf("exit = %d, want the status the agent sent alongside the note", code)
	}
	if stdout.String() != "as far as it got" {
		t.Errorf("stdout = %q, want only what the command wrote", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want the note kept out of the command's own streams", stderr.String())
	}
}

// A socket that is not there at all is the other shape of "the VM is not
// ready", and has to be retried rather than failing on the first dial:
// cloud-hypervisor creates the socket when it starts, which is after the
// container does.
func TestRun_AMissingSocketIsRetriedAndThenReported(t *testing.T) {
	cfg := Config{Socket: filepath.Join(t.TempDir(), "never-created.sock"), ConnectTimeout: 300 * time.Millisecond}

	_, err := Run(context.Background(), cfg, []string{"true"}, nil, nil, nil)
	if err == nil {
		t.Fatal("Run = nil, want an error")
	}
	if !strings.Contains(err.Error(), "never-created.sock") {
		t.Errorf("error = %v, want it to name the socket it could not reach", err)
	}
}

// Cancelling the context ends the session rather than leaving the caller
// waiting on a command that may never finish.
func TestRun_ContextCancellationEndsTheSession(t *testing.T) {
	f := startFakeCHV(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunLine(ctx, testConfig(t, f), "sleep 30", nil, nil, nil)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling the context did not end the session")
	}
}

// Cancelling is not the same as walking away. The client asks the guest
// to interrupt the command and keeps the session open long enough to
// carry the answer, so a cancelled call still gets the command's own
// last words and its real exit code rather than a dropped connection.
func TestRun_CancellationInterruptsTheCommandAndStillCollectsItsAnswer(t *testing.T) {
	f := startFakeCHV(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &trigger{want: "ready", hit: make(chan struct{})}
	go func() {
		<-out.hit
		cancel()
	}()

	code, err := RunLine(ctx, testConfig(t, f),
		"trap 'echo interrupted; exit 7' INT; echo ready; while :; do sleep 1; done",
		nil, out, nil)
	if err != nil {
		t.Fatalf("RunLine = %v, want the session to have ended cleanly with an exit code", err)
	}
	if code != 7 {
		t.Errorf("exit = %d, want 7 from the command's own INT handler", code)
	}
	if !strings.Contains(out.String(), "interrupted") {
		t.Errorf("output = %q, want what the command printed on its way out", out.String())
	}
}

// Against an agent too old to know the signal frame, cancelling falls
// straight back to what a client could always do -- close the connection
// -- rather than spending cancelGrace waiting for an answer that is
// never coming.
func TestRun_CancellationDoesNotWaitOnAnAgentThatCannotBeSignalled(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{agentWithoutSignal: true})

	cfg := testConfig(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := RunLine(ctx, cfg, "does not matter", nil, nil, nil); err == nil {
		t.Fatal("RunLine = nil, want an error for a session with no exit status")
	}
	if elapsed := time.Since(start); elapsed >= cfg.CancelGrace {
		t.Errorf("took %v to give up, want less than the %v grace an agent that can be signalled gets", elapsed, cfg.CancelGrace)
	}
}

// The grace is what bounds a cancelled session against a command that
// will not take the hint: the client asks, waits that long for an answer
// that is not coming, and closes the connection. Config.CancelGrace is
// what a caller with a guest slower than the default reaches for, so it
// has to be the thing actually being waited on -- assert both ends of
// it, since a grace that was ignored would show up either as giving up
// early or as waiting out the much larger default.
func TestRun_ACancelledSessionWaitsTheConfiguredGraceForACommandThatIgnoresIt(t *testing.T) {
	f := startFakeCHV(t, nil)

	cfg := testConfig(t, f)
	cfg.CancelGrace = 300 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &trigger{want: "ready", hit: make(chan struct{})}
	// The clock starts at the cancellation, not at the call: what is
	// being measured is the grace, not the dial and the shell startup in
	// front of it. Taken just before cancelling rather than just after,
	// so the window measured here can only be wider than the one the
	// client actually times, never narrower. Carried over a channel so
	// the read below is ordered after the write rather than racing it.
	cancelled := make(chan time.Time, 1)
	go func() {
		<-out.hit
		cancelled <- time.Now()
		cancel()
	}()

	// A shell that ignores SIGINT outright, so nothing but the closing
	// of the connection can end this session.
	_, err := RunLine(ctx, cfg, "trap '' INT; echo ready; while :; do sleep 1; done", nil, out, nil)
	if err == nil {
		t.Fatal("RunLine = nil, want an error for a session closed without an exit status")
	}
	elapsed := time.Since(<-cancelled)
	if elapsed < cfg.CancelGrace {
		t.Errorf("gave up after %v, want at least the %v grace the command had to answer in", elapsed, cfg.CancelGrace)
	}
	// Half the default is far more room than the configured 300ms needs
	// on any machine, and still nowhere near what a grace that had been
	// ignored -- and so left to DefaultCancelGrace -- would take.
	if ceiling := DefaultCancelGrace / 2; elapsed >= ceiling {
		t.Errorf("took %v, want the configured %v grace to have bounded it rather than the %v default", elapsed, cfg.CancelGrace, DefaultCancelGrace)
	}
}

// trigger is an io.Writer that closes hit the first time everything
// written to it contains want, so a test can act on a command reaching a
// known point rather than on a sleep.
type trigger struct {
	want string
	hit  chan struct{}

	mu   sync.Mutex
	seen strings.Builder
}

func (w *trigger) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen.Write(p)
	if strings.Contains(w.seen.String(), w.want) {
		select {
		case <-w.hit:
		default:
			close(w.hit)
		}
	}
	return len(p), nil
}

func (w *trigger) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen.String()
}

// TestReady_AnswersWhenTheGuestRunsACommand is the whole of "granule
// ready": no output, no session, just the fact that the guest ran
// something.
func TestReady_AnswersWhenTheGuestRunsACommand(t *testing.T) {
	f := startFakeCHV(t, nil)

	if err := Ready(context.Background(), testConfig(t, f)); err != nil {
		t.Fatalf("Ready = %v, want nil against a guest that answers", err)
	}
}

// TestReady_Sub50msDetection verifies that Ready detects guest readiness
// within sub-50ms when the guest becomes ready shortly after the first attempt,
// using the 25ms initial retry interval instead of a 500ms delay.
func TestReady_Sub50msDetection(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{refuse: true})

	cfg := testConfig(t, f)
	cfg.ConnectTimeout = 2 * time.Second

	time.AfterFunc(15*time.Millisecond, func() {
		f.mu.Lock()
		f.refuse = false
		f.mu.Unlock()
	})

	start := time.Now()
	if err := Ready(context.Background(), cfg); err != nil {
		t.Fatalf("Ready = %v, want nil", err)
	}
	elapsed := time.Since(start)
	if elapsed < 15*time.Millisecond {
		t.Errorf("Ready returned after %v, before becoming ready at 15ms", elapsed)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("Ready took %v to detect readiness, want sub-50ms (initial retry interval is 25ms)", elapsed)
	}
}

// A guest still booting refuses the vsock port, which is the ordinary
// case the retry exists for: Ready has to keep asking until its timeout
// rather than reporting the first refusal as a verdict.
func TestReady_RetriesAGuestThatIsStillBooting(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{refuse: true})

	cfg := testConfig(t, f)
	cfg.ConnectTimeout = 750 * time.Millisecond

	start := time.Now()
	err := Ready(context.Background(), cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Ready = nil, want a timeout")
	}
	if elapsed < cfg.ConnectTimeout {
		t.Errorf("gave up after %v, before the %v timeout: it is not retrying", elapsed, cfg.ConnectTimeout)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v to give up on a %v timeout", elapsed, cfg.ConnectTimeout)
	}
}

// A zero timeout is what a container readiness probe passes: one
// attempt, answered now, because whatever runs the probe does the
// retrying itself.
func TestReady_ZeroTimeoutIsASingleAttempt(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{refuse: true})

	cfg := testConfig(t, f)
	cfg.ConnectTimeout = 0

	start := time.Now()
	err := Ready(context.Background(), cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Ready = nil, want the refusal reported")
	}
	if elapsed > retryInterval {
		t.Errorf("took %v for a single attempt: it retried", elapsed)
	}
}

// A guest whose agent answers but cannot actually run the command is
// not ready either -- an agent up before the account it has to switch to
// exists looks exactly like this -- so the probe has to be a command
// that ran, not merely a connection that was accepted.
func TestReady_AGuestThatCannotRunTheProbeIsNotReady(t *testing.T) {
	f := startFakeCHV(t, nil)

	cfg := testConfig(t, f)
	cfg.ConnectTimeout = 0
	cfg.User = "no-such-account-here"

	if err := Ready(context.Background(), cfg); err == nil {
		t.Fatal("Ready = nil for a guest that could not run the probe, want an error")
	}
}

// TestReady_WaitsForTheGuestsOwnDeclaration is the second condition:
// with a ReadyFile configured, a guest whose agent answers is not ready
// until the guest itself has created that file. The file appearing
// partway through the wait is the ordinary shape of it -- a guest that
// touches it once its workload is serving -- and the point is that Ready
// returns then and not at the first answered command.
func TestReady_WaitsForTheGuestsOwnDeclaration(t *testing.T) {
	f := startFakeCHV(t, nil)
	declared := filepath.Join(t.TempDir(), "ready")

	cfg := testConfig(t, f)
	cfg.ReadyFile = declared
	cfg.ConnectTimeout = 10 * time.Second

	// The agent answers from the first attempt, so anything that made
	// Ready return before this fires would return with the file absent.
	timer := time.AfterFunc(750*time.Millisecond, func() {
		if err := os.WriteFile(declared, nil, 0o644); err != nil {
			t.Errorf("declaring readiness: %v", err)
		}
	})
	defer timer.Stop()

	start := time.Now()
	if err := Ready(context.Background(), cfg); err != nil {
		t.Fatalf("Ready = %v, want nil once the guest declared itself ready", err)
	}
	if elapsed := time.Since(start); elapsed < 750*time.Millisecond {
		t.Errorf("Ready returned after %v, before the file existed: it is not waiting for the declaration", elapsed)
	}
}

// The failure this has to read differently from every other one: the VM
// booted and its agent answers, and it is the guest's own declaration
// that is outstanding. A caller told "the guest did not answer" would go
// and read a console that has nothing wrong in it.
func TestReady_AnUndeclaredGuestSaysSoRatherThanLookingLikeABoot(t *testing.T) {
	f := startFakeCHV(t, nil)
	declared := filepath.Join(t.TempDir(), "never")

	cfg := testConfig(t, f)
	cfg.ReadyFile = declared
	cfg.ConnectTimeout = 750 * time.Millisecond

	err := Ready(context.Background(), cfg)
	if err == nil {
		t.Fatal("Ready = nil against a guest that never declared itself ready")
	}
	if !errors.Is(err, ErrNotDeclared) {
		t.Errorf("Ready = %v, want it to wrap ErrNotDeclared so callers can tell this from an unreachable guest", err)
	}
	if !strings.Contains(err.Error(), declared) {
		t.Errorf("Ready = %v, want the message to name %s", err, declared)
	}
	if !strings.Contains(err.Error(), "reachable") {
		t.Errorf("Ready = %v, want the message to say the guest was reachable", err)
	}
}

// A guest that never declares itself ready must fail rather than hang,
// which for the single-attempt form a readiness probe uses means
// answering now, with the same distinguishable error.
func TestReady_ZeroTimeoutStillDistinguishesTheUndeclaredGuest(t *testing.T) {
	f := startFakeCHV(t, nil)

	cfg := testConfig(t, f)
	cfg.ReadyFile = filepath.Join(t.TempDir(), "never")
	cfg.ConnectTimeout = 0

	err := Ready(context.Background(), cfg)
	if !errors.Is(err, ErrNotDeclared) {
		t.Fatalf("Ready = %v, want ErrNotDeclared", err)
	}
}

// The opt-in half of the design, asserted rather than assumed: with no
// ReadyFile configured, nothing about the guest's filesystem is asked
// about at all, so a guest image that has never heard of any of this is
// ready exactly when it always was.
func TestReady_WithoutAReadyFileNothingExtraIsRequired(t *testing.T) {
	f := startFakeCHV(t, nil)

	cfg := testConfig(t, f)
	cfg.ConnectTimeout = 0

	if err := Ready(context.Background(), cfg); err != nil {
		t.Fatalf("Ready = %v, want nil: an unconfigured guest must still be ready when its agent answers", err)
	}
}

// A ReadyFile whose path needs quoting still asks about the file the
// caller named: the check is a shell command line, and a path with a
// space in it would otherwise become two arguments to "test".
func TestReady_ReadyFilePathIsShellQuoted(t *testing.T) {
	f := startFakeCHV(t, nil)
	declared := filepath.Join(t.TempDir(), "a path with spaces")
	if err := os.WriteFile(declared, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, f)
	cfg.ReadyFile = declared
	cfg.ConnectTimeout = 0

	if err := Ready(context.Background(), cfg); err != nil {
		t.Fatalf("Ready = %v, want nil for a declared guest whose ready file needs quoting", err)
	}
}

func TestShellJoin(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"ls"}, "'ls'"},
		{[]string{"echo", "hello world"}, "'echo' 'hello world'"},
		{[]string{"echo", "it's"}, `'echo' 'it'\''s'`},
	}
	for _, c := range cases {
		if got := shellJoin(c.args); got != c.want {
			t.Errorf("shellJoin(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestShellCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "no args requests interactive shell", args: nil, want: ""},
		{name: "plain -c", args: []string{"-c", "echo hi"}, want: "echo hi"},
		{name: "fused flags ending in c", args: []string{"-ec", "echo hi"}, want: "echo hi"},
		{name: "command containing shell metacharacters passed through verbatim", args: []string{"-c", "ls -la $HOME | wc -l"}, want: "ls -la $HOME | wc -l"},
		{name: "-c with no command is an error", args: []string{"-c"}, wantErr: true},
		{name: "positional args after -c's command are unsupported", args: []string{"-c", "echo hi", "extra"}, wantErr: true},
		{name: "a script file argument is unsupported", args: []string{"script.sh"}, wantErr: true},
		{name: "--login is unsupported", args: []string{"--login"}, wantErr: true},
		{name: "bare -", args: []string{"-"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ShellCommandLine(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ShellCommandLine(%q) error = nil, want an error", c.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("ShellCommandLine(%q) error = %v", c.args, err)
			}
			if got != c.want {
				t.Errorf("ShellCommandLine(%q) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

// Config.Dir and Config.Env are what "granule exec -w/-e" set, and what
// they buy is a command that does not have to carry "cd ... &&" and an
// "export" of its own.
func TestRun_WorkingDirectoryAndEnvironmentReachTheCommand(t *testing.T) {
	f := startFakeCHV(t, nil)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, f)
	cfg.Dir = dir
	cfg.Env = []string{"GOFLAGS=-mod=vendor"}

	var stdout bytes.Buffer
	if _, err := RunLine(context.Background(), cfg, `pwd; echo "$GOFLAGS"`, nil, &stdout, nil); err != nil {
		t.Fatal(err)
	}
	want := dir + "\n-mod=vendor\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

// The mismatch this protocol cannot afford to be quiet about: an agent
// that predates the fields runs the command anyway, in the wrong
// directory, and says OK. The client has to notice from the features the
// response does not name, and say so.
func TestRun_AnAgentThatIgnoredTheWorkingDirectoryIsNotTakenAtItsWord(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{olderAgent: true})

	cfg := testConfig(t, f)
	cfg.Dir = t.TempDir()

	_, err := RunLine(context.Background(), cfg, "pwd", nil, nil, nil)
	if err == nil {
		t.Fatal("RunLine accepted a session that silently dropped the working directory")
	}
	if !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("error = %v, want one naming the guest agent as too old", err)
	}
}

// And a request that asked for nothing optional is still served by that
// same older agent: every added field is "omitempty" so that a client
// not using one stays compatible with a guest image that does not have
// it.
func TestRun_AnOlderAgentStillServesAPlainCommand(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{olderAgent: true})

	var stdout bytes.Buffer
	if _, err := RunLine(context.Background(), testConfig(t, f), "echo plain", nil, &stdout, nil); err != nil {
		t.Fatalf("RunLine against an older agent: %v", err)
	}
	if stdout.String() != "plain\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestParseArgs_FlagsThenCommand(t *testing.T) {
	opts, err := ParseArgs([]string{"-w", "/src", "-e", "A=1", "--env", "B=2", "--", "go", "build", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Workdir != "/src" {
		t.Errorf("workdir = %q", opts.Workdir)
	}
	if strings.Join(opts.Env, ",") != "A=1,B=2" {
		t.Errorf("env = %v, want both entries in the order given", opts.Env)
	}
	if strings.Join(opts.Command, " ") != "go build ./..." {
		t.Errorf("command = %v", opts.Command)
	}
}

// The invocation every doc, script and README line uses has to keep
// meaning exactly what it did before this mode had any flags at all.
func TestParseArgs_TheDocumentedInvocationIsUnchanged(t *testing.T) {
	opts, err := ParseArgs([]string{"--", "sh", "-c", "exit 42"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(opts.Command, "|") != "sh|-c|exit 42" {
		t.Fatalf("command = %v, want the command's own flags left alone", opts.Command)
	}
	if opts.Workdir != "" || len(opts.Env) != 0 {
		t.Fatalf("opts = %+v, want nothing set", opts)
	}

	// No "--" either: flags stop at the first thing that is not one, so
	// a command's own arguments are still its own.
	opts, err = ParseArgs([]string{"uname", "-a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(opts.Command, "|") != "uname|-a" {
		t.Fatalf("command = %v", opts.Command)
	}

	// And no arguments at all is still the interactive login shell.
	opts, err = ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Command) != 0 {
		t.Fatalf("command = %v, want none", opts.Command)
	}
}

func TestParseArgs_RejectsWhatWouldGoWrongLater(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		// docker exec reads a bare name from the client's environment;
		// this container's environment is not one anybody set up, so a
		// variable that quietly did not arrive is the likelier outcome.
		{"a bare variable name", []string{"-e", "GOFLAGS", "--", "true"}, "KEY=value"},
		{"an empty key", []string{"-e", "=1", "--", "true"}, "KEY=value"},
		{"a relative working directory", []string{"-w", "src", "--", "true"}, "absolute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseArgs(tc.args)
			if err == nil {
				t.Fatalf("ParseArgs(%q) = nil, want an error", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), Usage) {
				t.Error("the error does not carry the usage a caller needs to fix it")
			}
		})
	}
}

// "granule exec -h" is a question, and cmd/granule-shim answers it with Usage
// on stdout rather than as a failure.
func TestParseArgs_HelpIsNotAnError(t *testing.T) {
	_, err := ParseArgs([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("ParseArgs(-h) = %v, want flag.ErrHelp", err)
	}
}

// A container exec sends its command as argv, unjoined: the container
// need not have a shell to split a line, and a container runtime's exec
// never asks for one.
func TestRun_AContainerExecSendsArgvRatherThanALine(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{recordRequest: true})

	cfg := testConfig(t, f)
	cfg.Container = true
	cfg.User = ""
	if _, err := Run(context.Background(), cfg, []string{"echo", "a b"}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	req := f.request()
	if !req.Container {
		t.Fatal("the request did not ask for the container")
	}
	if req.Line != "" || strings.Join(req.Argv, "|") != "echo|a b" {
		t.Errorf("line = %q, argv = %q, want the command as argv and no line", req.Line, req.Argv)
	}
}

// An agent too old to know Container would run the command in the guest
// instead, which is the wrong machine's answer in the right format.
func TestRun_AnAgentThatIgnoredContainerIsNotTakenAtItsWord(t *testing.T) {
	f := startFakeCHV(t, &fakeCHV{olderAgent: true})

	cfg := testConfig(t, f)
	cfg.Container = true
	_, err := RunLine(context.Background(), cfg, "true", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), execwire.FeatureContainer) {
		t.Fatalf("error = %v, want one naming the missing %s feature", err, execwire.FeatureContainer)
	}
}

func TestParseArgs_Container(t *testing.T) {
	opts, err := ParseArgs([]string{"--container", "-u", "1000", "--", "ls", "-l"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Container || opts.User != "1000" || strings.Join(opts.Command, " ") != "ls -l" {
		t.Errorf("opts = %+v", opts)
	}
	if opts, err := ParseArgs([]string{"--", "ls"}); err != nil || opts.Container {
		t.Errorf("without --container: opts = %+v, err = %v", opts, err)
	}
}
