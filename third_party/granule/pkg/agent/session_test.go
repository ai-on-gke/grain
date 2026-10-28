package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/forward"
)

// result is what a client sees for one session.
type result struct {
	resp   execwire.Response
	stdout string
	stderr string
	code   int
}

// currentAccount is the name of the account this test process is running
// as, read the same way the agent reads it.
//
// Tests ask for this account rather than for root, because switching to
// a different one needs privileges a test runner does not have: asking
// for root as an ordinary user makes the agent try to setuid(0) and fail
// with EPERM before the command ever runs. The production agent runs as
// root in the guest, where every account is reachable; a test can only
// exercise the account it already has.
func currentAccount(t *testing.T) string {
	t.Helper()
	f, err := os.Open(passwdPath)
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
	t.Fatalf("no account in %s for uid %s", passwdPath, uid)
	return ""
}

// run drives one Serve over an in-memory connection, the way "granule
// exec" drives one over vsock. stdinFrames are sent before reading any
// output, which is enough for every command here.
func run(t *testing.T, req execwire.Request, stdin string) result {
	t.Helper()
	if req.User == "" {
		req.User = currentAccount(t)
	}

	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		done <- Serve(context.Background(), server)
	}()
	defer client.Close()

	if err := execwire.WriteRequest(client, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(client)
	resp, err := execwire.ReadResponse(br)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		// Reported here rather than left to whatever the caller was
		// asserting about output: a refused start otherwise reads as a
		// command that ran and printed nothing, which is what made the
		// first CI failure on this package look like six unrelated ones.
		if !expectRefusal(req) {
			t.Fatalf("the agent refused to start the command: %s", resp.Error)
		}
		return result{resp: resp}
	}

	if stdin != "" {
		if err := execwire.WriteFrame(client, execwire.TypeStdin, []byte(stdin)); err != nil {
			t.Fatal(err)
		}
	}
	if err := execwire.WriteFrame(client, execwire.TypeStdinClose, nil); err != nil {
		t.Fatal(err)
	}

	res := result{resp: resp}
	var out, errb bytes.Buffer
	for {
		typ, payload, err := execwire.ReadFrame(br)
		if err != nil {
			t.Fatalf("reading frames: %v (stdout so far %q)", err, out.String())
		}
		switch typ {
		case execwire.TypeStdout:
			out.Write(payload)
		case execwire.TypeStderr:
			errb.Write(payload)
		case execwire.TypeNote:
			// None of the commands driven through run has any business
			// producing one: a note means the session stopped waiting
			// for output rather than draining it to the end (see
			// outputGrace), so one here is output somebody may have
			// lost.
			t.Errorf("the session sent a note: %s", payload)
		case execwire.TypeExit:
			res.code, err = execwire.DecodeExit(payload)
			if err != nil {
				t.Fatal(err)
			}
			res.stdout, res.stderr = out.String(), errb.String()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Serve: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Serve did not return after the exit frame")
			}
			return res
		}
	}
}

// expectRefusal reports whether a request is one of the few that are
// meant to be refused before anything runs.
func expectRefusal(req execwire.Request) bool {
	return req.User == "nosuchaccount"
}

// live is a session a test drives frame by frame, rather than through
// run above. Everything about ending a command early -- interrupting it,
// cancelling it, walking away from it -- has to happen while the command
// is still running, which run's send-everything-then-read shape cannot
// express.
type live struct {
	t      *testing.T
	client net.Conn
	br     *bufio.Reader
	done   chan error

	// notes are the TypeNote frames the agent sent, in order: what it
	// had to say about the session itself rather than anything the
	// command wrote.
	notes []string
}

func startSession(ctx context.Context, t *testing.T, req execwire.Request) *live {
	t.Helper()
	if req.User == "" {
		req.User = currentAccount(t)
	}

	client, server := net.Pipe()
	l := &live{t: t, client: client, br: bufio.NewReader(client), done: make(chan error, 1)}
	go func() {
		defer server.Close()
		l.done <- Serve(ctx, server)
	}()
	t.Cleanup(func() { client.Close() })

	if err := execwire.WriteRequest(client, req); err != nil {
		t.Fatal(err)
	}
	resp, err := execwire.ReadResponse(l.br)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("the agent refused to start the command: %s", resp.Error)
	}
	if !resp.Supports(execwire.FeatureSignal) {
		t.Errorf("response features = %v, want %s among them", resp.Features, execwire.FeatureSignal)
	}
	return l
}

// awaitOutput reads frames until the session's output contains want,
// which is how a test knows the command has got as far as the thing it
// is about to be interrupted in the middle of.
func (l *live) awaitOutput(want string) {
	l.t.Helper()
	var seen strings.Builder
	for {
		typ, payload, err := execwire.ReadFrame(l.br)
		if err != nil {
			l.t.Fatalf("waiting for %q: %v (output so far %q)", want, err, seen.String())
		}
		switch typ {
		case execwire.TypeStdout, execwire.TypeStderr:
			seen.Write(payload)
			if strings.Contains(seen.String(), want) {
				return
			}
		case execwire.TypeNote:
			l.notes = append(l.notes, string(payload))
		case execwire.TypeExit:
			l.t.Fatalf("the command exited before printing %q (output %q)", want, seen.String())
		}
	}
}

// awaitExit reads the rest of the session and returns the exit code and
// everything printed on the way to it.
func (l *live) awaitExit() (int, string) {
	l.t.Helper()
	var out strings.Builder
	for {
		typ, payload, err := execwire.ReadFrame(l.br)
		if err != nil {
			l.t.Fatalf("reading frames: %v (output so far %q)", err, out.String())
		}
		switch typ {
		case execwire.TypeStdout, execwire.TypeStderr:
			out.Write(payload)
		case execwire.TypeNote:
			l.notes = append(l.notes, string(payload))
		case execwire.TypeExit:
			code, err := execwire.DecodeExit(payload)
			if err != nil {
				l.t.Fatal(err)
			}
			select {
			case err := <-l.done:
				if err != nil {
					l.t.Fatalf("Serve: %v", err)
				}
			case <-time.After(5 * time.Second):
				l.t.Fatal("Serve did not return after the exit frame")
			}
			return code, out.String()
		}
	}
}

// backgroundChild is a command line that starts a child of its own,
// writes its pid to pidFile and then waits for it -- the shape a
// `<shell> -c` session takes whenever the line it was given runs
// anything, and the one where signalling only the shell leaves something
// behind. It prints "started" once the pid file is written.
func backgroundChild(pidFile string) string {
	return fmt.Sprintf("sleep 300 & echo $! >%s; echo started; wait", pidFile)
}

// heldOutput is the same background child without the wait: the command
// starts something, does not redirect its output, and exits at once,
// leaving the writing end of the session's stdout and stderr open in a
// process that will outlive it by minutes. It is what
// `granule exec -- 'myserver &'` is, and the session it produces is the
// one that used to hang until somebody stopped the server.
//
// It prints on its way out as well as on its way in, because what the
// command wrote before exiting still has to arrive: the bound on the
// drain must not cost the output the command actually produced.
func heldOutput(pidFile string) string {
	return fmt.Sprintf("sleep 300 & echo $! >%s; echo started; echo the-last-thing-it-said >&2; exit 5", pidFile)
}

// killLater kills pid when the test ends, so that a `sleep 300` this
// package deliberately leaves running does not outlive the run by five
// minutes.
func killLater(t *testing.T, pid int) {
	t.Helper()
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		t.Fatalf("pid file %s holds %q", path, b)
	}
	return pid
}

// processAlive reports whether pid names a process that is still
// running.
//
// Not kill(pid, 0): that succeeds for a process which has exited and is
// waiting to be reaped, and a child whose parent shell was killed first
// is reparented to whatever init this test runs under, which may be in
// no hurry to collect it. The state field in /proc tells a zombie from a
// living process, and is the only thing that answers the question these
// tests are actually asking.
func processAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// "pid (comm) state ...", and comm may itself contain spaces and
	// parentheses, so the state is the field after the last ')'.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return false
	}
	return b[i+2] != 'Z'
}

func awaitProcessGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d was still running %s after the session ended", pid, within)
}

// A client that goes away has to take the command with it. Closing the
// guest process's stdin was all this used to do, which for the great
// majority of commands -- anything that does not read stdin -- meant a
// process left running in the guest with nobody waiting for it and
// nothing to notice it. In a long-lived sandbox that is one leak per
// timed-out call.
func TestDroppingTheConnectionEndsTheCommandAndWhatItStarted(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	l := startSession(context.Background(), t, execwire.Request{Line: backgroundChild(pidFile)})
	l.awaitOutput("started")
	child := readPID(t, pidFile)

	l.client.Close()

	awaitProcessGone(t, child, 10*time.Second)
	select {
	case <-l.done:
		// Whatever Serve says here is about the connection, not the
		// session: it ends by failing to write an exit frame to a client
		// that has gone. What matters is that it returns at all, and
		// that the command above is gone with it.
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the client went away")
	}
}

// Cancelling the session's context is what cmd/granule-agent does when a
// connection ends, and it asks before it insists: a command gets a
// SIGTERM it can act on, and only a command that sits through
// terminateGrace is killed outright.
func TestCancellingTheContextAsksTheCommandToStopBeforeKillingIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := startSession(ctx, t, execwire.Request{
		Line: "trap 'echo caught-term; exit 9' TERM; echo ready; while :; do sleep 1; done",
	})
	l.awaitOutput("ready")

	cancel()

	code, out := l.awaitExit()
	if !strings.Contains(out, "caught-term") {
		t.Errorf("output = %q, want the command's own SIGTERM handler to have run", out)
	}
	if code != 9 {
		t.Errorf("exit = %d, want 9 from the trap rather than a signal death", code)
	}
}

// A signal frame is the other half: the client wants the command
// interrupted but the session kept, so that what the command prints on
// its way out still arrives and its exit still comes back as an exit
// code rather than a dropped connection.
func TestASignalFrameInterruptsTheCommandWithoutEndingTheSession(t *testing.T) {
	l := startSession(context.Background(), t, execwire.Request{
		Line: "trap 'echo interrupted; exit 7' INT; echo ready; while :; do sleep 1; done",
	})
	l.awaitOutput("ready")

	if err := execwire.WriteFrame(l.client, execwire.TypeSignal, execwire.EncodeSignal(int(syscall.SIGINT))); err != nil {
		t.Fatal(err)
	}

	code, out := l.awaitExit()
	if !strings.Contains(out, "interrupted") {
		t.Errorf("output = %q, want what the command printed on its way out", out)
	}
	if code != 7 {
		t.Errorf("exit = %d, want 7 from the command's INT handler", code)
	}
}

// And it reaches the whole process group, not the shell at the top of
// it. A `<shell> -c` line that started something and is waiting on it is
// the ordinary case -- a build, a test run, a server -- and a signal
// delivered only to the shell would leave that child running.
//
// SIGTERM rather than SIGINT here because of a shell rule rather than
// anything about the transport: a non-interactive shell with no job
// control sets SIGINT to ignored in the background jobs it starts, so a
// SIGINT would prove nothing about a `sleep 300 &`.
func TestASignalFrameReachesEverythingTheCommandStarted(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	l := startSession(context.Background(), t, execwire.Request{
		Line: "trap 'echo interrupted; exit 7' TERM; " + backgroundChild(pidFile),
	})
	l.awaitOutput("started")
	child := readPID(t, pidFile)

	if err := execwire.WriteFrame(l.client, execwire.TypeSignal, execwire.EncodeSignal(int(syscall.SIGTERM))); err != nil {
		t.Fatal(err)
	}

	awaitProcessGone(t, child, 5*time.Second)
	if code, _ := l.awaitExit(); code != 7 {
		t.Errorf("exit = %d, want 7 from the command's TERM handler", code)
	}
}

// A signal number the protocol does not allow is dropped, and dropping
// it costs the session nothing: the command is still running and still
// reading its stdin afterwards. Signal 0 is the one to try, since kill(2)
// reads it as an existence check rather than a signal.
func TestASignalFrameOutsideTheAllowedRangeIsIgnored(t *testing.T) {
	l := startSession(context.Background(), t, execwire.Request{Line: "cat"})

	if err := execwire.WriteFrame(l.client, execwire.TypeSignal, execwire.EncodeSignal(0)); err != nil {
		t.Fatal(err)
	}
	if err := execwire.WriteFrame(l.client, execwire.TypeStdin, []byte("still here\n")); err != nil {
		t.Fatal(err)
	}
	l.awaitOutput("still here")

	if err := execwire.WriteFrame(l.client, execwire.TypeStdinClose, nil); err != nil {
		t.Fatal(err)
	}
	if code, _ := l.awaitExit(); code != 0 {
		t.Errorf("exit = %d, want the command to have finished normally", code)
	}
}

// A command that exits is over, whatever else still holds the pipe it
// was writing to. Draining to the end of the output before reporting the
// exit is right for the command's own last words and wrong for a process
// it left running: the writing end stays open for as long as that
// process lives, so the client waits on an exit frame that cannot arrive
// until somebody stops a server. Here the session ends on the command's
// exit instead, outputGrace after it.
func TestASessionEndsWhenTheCommandDoesEvenIfSomethingStillHoldsItsOutput(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	l := startSession(context.Background(), t, execwire.Request{Line: heldOutput(pidFile)})

	// A bound on the test, not on the session: without one, a session
	// that goes back to waiting on the background process fails as a
	// package-wide timeout ten minutes later, naming nothing.
	if err := l.client.SetReadDeadline(time.Now().Add(outputGrace + 30*time.Second)); err != nil {
		t.Fatal(err)
	}

	// Everything is read through awaitExit rather than waited for with
	// awaitOutput first, because the assertion below is about output
	// that must survive the session ending, and awaitOutput drops
	// whatever it read past the string it was waiting for.
	code, out := l.awaitExit()
	killLater(t, readPID(t, pidFile))

	if code != 5 {
		t.Errorf("exit = %d, want the command's own status: it exited, whatever is still holding its output", code)
	}
	if !strings.Contains(out, "started") || !strings.Contains(out, "the-last-thing-it-said") {
		t.Errorf("output = %q, want everything the command wrote, including its last line before exiting", out)
	}
}

// And the client is told, because the exit code alone cannot say it: the
// command's status is real, and the output around it stops short of what
// was written to the pipe.
func TestASessionThatStopsWaitingForOutputSaysSo(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	l := startSession(context.Background(), t, execwire.Request{Line: heldOutput(pidFile)})
	if err := l.client.SetReadDeadline(time.Now().Add(outputGrace + 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	l.awaitExit()
	killLater(t, readPID(t, pidFile))

	if len(l.notes) != 1 {
		t.Fatalf("notes = %q, want exactly one saying the output was cut short", l.notes)
	}
	if !strings.Contains(l.notes[0], "output open") || !strings.Contains(l.notes[0], "status 5") {
		t.Errorf("note = %q, want it to name the command's status and say why the output stops", l.notes[0])
	}
}

// What it must not do is kill the process holding the output. That
// process is there because the caller asked for it, the command that
// started it exited on its own, and a client is still reading -- none of
// what makes an abandoned session's process group worth signalling. The
// same command with its output redirected leaves the process running
// (below), and "&" cannot mean two different things depending on a
// redirection.
func TestASessionThatStopsWaitingLeavesTheBackgroundProcessAlone(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	l := startSession(context.Background(), t, execwire.Request{Line: heldOutput(pidFile)})
	if err := l.client.SetReadDeadline(time.Now().Add(outputGrace + 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	l.awaitExit()

	child := readPID(t, pidFile)
	killLater(t, child)
	if !processAlive(child) {
		t.Error("the backgrounded process was killed along with the session it outlived")
	}
}

// The other half of the same case, and the one that already worked: a
// background process whose output goes somewhere else closes the
// session's pipes when the command exits, so the session ends at once --
// no wait for the bound, nothing to tell the client, and the process
// still running.
func TestABackgroundProcessThatRedirectsItsOutputEndsTheSessionAtOnce(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	l := startSession(context.Background(), t, execwire.Request{
		Line: fmt.Sprintf("sleep 300 >/dev/null 2>&1 & echo $! >%s; echo started; exit 5", pidFile),
	})
	l.awaitOutput("started")
	child := readPID(t, pidFile)
	killLater(t, child)

	started := time.Now()
	code, _ := l.awaitExit()
	if took := time.Since(started); took >= outputGrace {
		t.Errorf("the session took %s to end, which is the bound in pump: a redirected background process should end it as soon as the command exits", took)
	}
	if code != 5 {
		t.Errorf("exit = %d, want the command's own status", code)
	}
	if len(l.notes) != 0 {
		t.Errorf("notes = %q, want none: nothing was cut short", l.notes)
	}
	if !processAlive(child) {
		t.Error("the backgrounded process was killed with the session")
	}
}

func TestACommandsOutputAndExitCodeComeBack(t *testing.T) {
	res := run(t, execwire.Request{Line: "echo hello"}, "")
	if res.stdout != "hello\n" || res.code != 0 {
		t.Fatalf("stdout %q, exit %d", res.stdout, res.code)
	}

	res = run(t, execwire.Request{Line: "exit 42"}, "")
	if res.code != 42 {
		t.Fatalf("exit %d, want 42", res.code)
	}
}

// The two output streams stay apart. This is the whole reason the
// protocol has two frame types rather than one, and grain's sandbox
// tools read them separately -- a merged stream is what the SSH console
// wrapper used to do wrong, and what GUEST_CONSOLE_WRAP=0 exists to
// avoid.
func TestStdoutAndStderrStaySeparate(t *testing.T) {
	res := run(t, execwire.Request{Line: "echo out; echo err >&2"}, "")
	if res.stdout != "out\n" {
		t.Errorf("stdout = %q, want just the stdout line", res.stdout)
	}
	if res.stderr != "err\n" {
		t.Errorf("stderr = %q, want just the stderr line", res.stderr)
	}
}

// A command's own output must survive its exit. os/exec closes the pipes
// as part of Wait, so an implementation that reaped before draining
// loses whatever was written just before exiting -- which is exactly
// where a failing command puts its error message.
func TestOutputWrittenJustBeforeExitIsNotLost(t *testing.T) {
	res := run(t, execwire.Request{Line: "echo the-last-thing-it-said >&2; exit 3"}, "")
	if res.code != 3 {
		t.Fatalf("exit %d, want 3", res.code)
	}
	if !strings.Contains(res.stderr, "the-last-thing-it-said") {
		t.Fatalf("stderr = %q, want the message written immediately before exit", res.stderr)
	}
}

// A session owns its output pipes now rather than borrowing os/exec's
// (attachPipes), and what it owns it has to close: Wait used to do it,
// and does not for pipes it did not make. The agent runs for the life of
// the guest, so two descriptors left behind per call is a guest that
// eventually cannot open anything.
func TestASessionLeavesNoDescriptorsBehind(t *testing.T) {
	openFDs := func() int {
		names, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skipf("no /proc/self/fd to count descriptors in: %v", err)
		}
		return len(names)
	}

	// One session first, so that whatever the runtime opens once -- an
	// epoll descriptor, a timer -- is already open when the count below
	// is taken.
	run(t, execwire.Request{Line: "true"}, "")
	before := openFDs()
	for i := 0; i < 10; i++ {
		run(t, execwire.Request{Line: "echo hello"}, "")
	}

	// A little slack, since the runtime is free to open something of its
	// own in between; a session that kept its pipes would be twenty
	// descriptors past this.
	if after := openFDs(); after > before+2 {
		t.Errorf("%d descriptors open after ten sessions, %d before them", after, before)
	}
}

func TestStdinReachesTheCommand(t *testing.T) {
	res := run(t, execwire.Request{Line: "cat"}, "round trip\n")
	if res.stdout != "round trip\n" {
		t.Fatalf("stdout = %q", res.stdout)
	}
	if res.code != 0 {
		t.Fatalf("exit %d: a command reading stdin never saw EOF", res.code)
	}
}

// Failing to start and failing once started are different answers.
// Collapsing them would report a guest with no such account as a command
// that ran and exited non-zero.
func TestAnAccountThatDoesNotExistFailsBeforeTheCommandRuns(t *testing.T) {
	res := run(t, execwire.Request{Line: "echo unreachable", User: "nosuchaccount"}, "")
	if res.resp.OK {
		t.Fatal("a request naming an unknown account was accepted")
	}
	if !strings.Contains(res.resp.Error, "nosuchaccount") {
		t.Errorf("error %q does not name the account", res.resp.Error)
	}
}

// With a pty the session has one stream, and it is a terminal. Both
// halves matter: interactive tools test isatty, and a caller that got
// stderr frames from a pty session would be seeing something SSH never
// produced either.
func TestATTYSessionIsATerminalOnOneStream(t *testing.T) {
	res := run(t, execwire.Request{Line: "test -t 1 && echo is-a-tty; echo to-stderr >&2", TTY: true, Cols: 80, Rows: 24}, "")
	if !strings.Contains(res.stdout, "is-a-tty") {
		t.Fatalf("stdout = %q, want the command to have seen a terminal", res.stdout)
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want everything on the one stream a terminal has", res.stderr)
	}
	if !strings.Contains(res.stdout, "to-stderr") {
		t.Errorf("stdout = %q, want the stderr line merged into it", res.stdout)
	}
}

func TestTheRequestedTerminalSizeIsApplied(t *testing.T) {
	res := run(t, execwire.Request{Line: "stty size", TTY: true, Cols: 120, Rows: 40}, "")
	// stty prints "rows cols". Skipped rather than failed where stty is
	// absent: the size is set by ioctl either way, and this asserts it
	// only where something can read it back.
	if strings.TrimSpace(res.stdout) == "" {
		t.Skip("no stty on this machine to read the size back with")
	}
	if got := strings.TrimSpace(res.stdout); got != "40 120" {
		t.Fatalf("stty size = %q, want \"40 120\"", got)
	}
}

// The terminal type comes from the client, because the client is the
// only end that has a terminal. Before it did, a pty session ran under
// the agent's own TERM -- nothing, in a guest -- and every curses program
// on the far side was told there was nothing to draw on.
func TestTheClientsTerminalTypeReachesTheCommand(t *testing.T) {
	// The agent's own environment must not be able to supply the answer,
	// or this would pass on a machine that happens to export TERM.
	t.Setenv("TERM", "")

	res := run(t, execwire.Request{Line: `echo "$TERM"`, TTY: true, Cols: 80, Rows: 24, Term: "xterm-256color"}, "")
	if got := strings.TrimSpace(res.stdout); got != "xterm-256color" {
		t.Fatalf("TERM = %q, want the terminal type the request named", got)
	}
}

// A session with no terminal is one where "dumb" is the right answer and
// always was: there is nothing to draw on, and saying so is what stops a
// curses program from trying.
func TestASessionWithNoTerminalStillGetsDumb(t *testing.T) {
	t.Setenv("TERM", "")

	res := run(t, execwire.Request{Line: `echo "$TERM"`}, "")
	if got := strings.TrimSpace(res.stdout); got != "dumb" {
		t.Fatalf("TERM = %q, want \"dumb\" for a session with no pty", got)
	}
}

// A pty from a client too old to name a terminal type still has a
// terminal on the other end of it, so "dumb" is the one answer it must
// not get.
func TestAPTYWhoseClientNamedNoTerminalTypeGetsAUsableOne(t *testing.T) {
	t.Setenv("TERM", "")

	res := run(t, execwire.Request{Line: `echo "$TERM"`, TTY: true, Cols: 80, Rows: 24}, "")
	if got := strings.TrimSpace(res.stdout); got != defaultTTYTerm {
		t.Fatalf("TERM = %q, want %q for a pty session whose client sent no terminal type", got, defaultTTYTerm)
	}
}

// TERM is part of the login environment like any other entry, so a
// caller that sends one of its own overrides the session's -- the same
// rule the request's -e follows for PATH and HOME.
func TestARequestedTERMOverridesTheSessionsOwn(t *testing.T) {
	res := run(t, execwire.Request{
		Line: `echo "$TERM"`,
		TTY:  true, Cols: 80, Rows: 24,
		Term: "xterm-256color",
		Env:  []string{"TERM=vt100"},
	}, "")
	if got := strings.TrimSpace(res.stdout); got != "vt100" {
		t.Fatalf("TERM = %q, want the value the request's environment set", got)
	}
}

// The environment is sshd's, not this process's, because guest images
// built on granule depend on it -- grain puts its Go and Node toolchains
// in /usr/local/bin precisely because that is on this PATH and no
// profile is read.
func TestTheCommandGetsTheSameEnvironmentSSHDGaveIt(t *testing.T) {
	res := run(t, execwire.Request{Line: "echo \"$PATH\"; echo \"$HOME\"; echo \"$USER\""}, "")
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q", res.stdout)
	}
	if want := defaultPATH(os.Getuid()); lines[0] != want {
		t.Errorf("PATH = %q, want the login default %q", lines[0], want)
	}
	if lines[2] == "" {
		t.Error("USER is empty")
	}
}

// An empty command line is an interactive login shell, the same fallback
// sshd makes. Asserted by the shell reading its startup files, which a
// `-c` invocation does not do.
func TestAnEmptyCommandLineAsksForALoginShell(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_ = Serve(context.Background(), server)
	}()
	defer client.Close()

	if err := execwire.WriteRequest(client, execwire.Request{User: currentAccount(t)}); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(client)
	resp, err := execwire.ReadResponse(br)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("an empty command line was refused: %s", resp.Error)
	}
	// A login shell with stdin closed exits on its own; all that is
	// asserted here is that it started and ended rather than erroring.
	if err := execwire.WriteFrame(client, execwire.TypeStdinClose, nil); err != nil {
		t.Fatal(err)
	}
	for {
		typ, payload, err := execwire.ReadFrame(br)
		if err != nil {
			t.Fatalf("the login shell never reported an exit: %v", err)
		}
		if typ == execwire.TypeExit {
			if _, err := execwire.DecodeExit(payload); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}

// The account files are the guest's, and the fields that matter are the
// two os/user does not answer: the login shell and the supplementary
// groups.
func TestAccountLookupReadsTheShellAndTheSupplementaryGroups(t *testing.T) {
	dir := t.TempDir()
	passwd := filepath.Join(dir, "passwd")
	group := filepath.Join(dir, "group")
	// Any uid but this process's own: lookupAccount reads no groups for
	// the account it is already running as (see account.self), so a
	// fixture that happened to name the uid running the tests -- 1000,
	// the first account on a Debian host, is the obvious one to write
	// here -- would assert nothing on that machine and fail there alone.
	uid := 1000
	if os.Getuid() == uid {
		uid = 1001
	}
	if err := os.WriteFile(passwd, []byte(fmt.Sprintf(
		"root:x:0:0:root:/root:/bin/bash\n"+
			"debian:x:%d:%d:,,,:/home/debian:/bin/bash\n", uid, uid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(group, []byte(
		"docker:x:998:debian\n"+
			"sudo:x:27:debian\n"+
			"nobody:x:65534:\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldPasswd, oldGroup := passwdPath, groupPath
	passwdPath, groupPath = passwd, group
	defer func() { passwdPath, groupPath = oldPasswd, oldGroup }()

	a, err := lookupAccount("debian")
	if err != nil {
		t.Fatal(err)
	}
	if a.UID != uid || a.GID != uid || a.Shell != "/bin/bash" {
		t.Fatalf("account = %+v", a)
	}
	// The primary gid first, then every group naming it. Losing the
	// docker group is the failure this guards: every `docker` command in
	// the guest would fail on the socket's permissions, saying nothing
	// about a group.
	want := map[uint32]bool{uint32(uid): true, 998: true, 27: true}
	if len(a.Groups) != len(want) {
		t.Fatalf("groups = %v, want the primary plus docker and sudo", a.Groups)
	}
	for _, g := range a.Groups {
		if !want[g] {
			t.Errorf("unexpected group %d in %v", g, a.Groups)
		}
	}

	if _, err := lookupAccount("ghost"); err == nil {
		t.Error("an account not in the file was resolved anyway")
	}

	// Lookup by numeric string matching an existing user
	numAcct, err := lookupAccount(fmt.Sprintf("%d", uid))
	if err != nil {
		t.Fatalf("lookupAccount(%d) = %v", uid, err)
	}
	if numAcct.UID != uid || numAcct.Name != "debian" {
		t.Fatalf("lookupAccount(%d) = %+v, want debian", uid, numAcct)
	}

	// Lookup by numeric string for a non-existent user should synthesize account
	synthAcct, err := lookupAccount("12345")
	if err != nil {
		t.Fatalf("lookupAccount(12345) = %v", err)
	}
	if synthAcct.UID != 12345 || synthAcct.GID != 12345 || synthAcct.Shell != "/bin/sh" {
		t.Fatalf("lookupAccount(12345) = %+v, want synthesized account", synthAcct)
	}

	// Numbers the credential cannot represent are refused rather than
	// truncated or read as "unchanged", both of which come out as root.
	for _, name := range []string{"4294967295", "4294967296", "18446744073709551616"} {
		if a, err := lookupAccount(name); err == nil {
			t.Errorf("lookupAccount(%s) = %+v, want it refused", name, a)
		}
	}
}

// The credential is the production path no test can actually exercise
// -- switching to another account needs privileges a test runner does
// not have -- so what is built for one is asserted directly instead.
// The groups are the part worth pinning: a session that set only uid and
// gid would leave every `docker` command in the guest failing on the
// socket's permissions, with nothing in the error naming the group it
// lost.
func TestAnotherAccountGetsACredentialCarryingItsGroups(t *testing.T) {
	a := &account{Name: "debian", UID: 1000, GID: 1000, Groups: []uint32{1000, 998}}
	cred := a.credential()
	if cred == nil {
		t.Fatal("no credential for an account this process is not running as")
	}
	if cred.Uid != 1000 || cred.Gid != 1000 {
		t.Errorf("credential = %+v, want uid/gid 1000", cred)
	}
	if len(cred.Groups) != 2 || cred.Groups[0] != 1000 || cred.Groups[1] != 998 {
		t.Errorf("credential groups = %v, want the account's own", cred.Groups)
	}
}

// The account the request names is routinely the one the agent is
// already running as: root, in the guest, where the agent runs as root.
// No credential is applied for it -- a setuid to the uid the process
// already has -- and that is also what lets every session test above run
// a real command without privileges. Asserted here because it is the
// case the test above has to steer around: without it, a group lookup
// that had been skipped and one that was broken would look the same.
func TestTheAccountTheAgentAlreadyRunsAsGetsNoCredential(t *testing.T) {
	dir := t.TempDir()
	passwd := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwd, []byte(
		fmt.Sprintf("itself:x:%d:%d::%s:/bin/sh\n", os.Getuid(), os.Getgid(), dir)), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPasswd := passwdPath
	passwdPath = passwd
	defer func() { passwdPath = oldPasswd }()

	a, err := lookupAccount("itself")
	if err != nil {
		t.Fatal(err)
	}
	if !a.self {
		t.Fatalf("account %+v was not recognized as this process's own", a)
	}
	if cred := a.credential(); cred != nil {
		t.Errorf("credential = %+v for the account the agent already runs as; a setuid to the uid it has would fail for an unprivileged agent", cred)
	}
}

// A home directory named in /etc/passwd but absent from the filesystem
// would fail every command on chdir, which reads as the command being
// broken rather than the account being half-created.
func TestAMissingHomeDirectoryFallsBackRatherThanFailingEveryCommand(t *testing.T) {
	dir := t.TempDir()
	passwd := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwd, []byte("ghosthome:x:0:0::/home/does-not-exist:/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPasswd := passwdPath
	passwdPath = passwd
	defer func() { passwdPath = oldPasswd }()

	a, err := lookupAccount("ghosthome")
	if err != nil {
		t.Fatal(err)
	}
	if a.Home != "/" {
		t.Fatalf("home = %q, want / when the named directory is not there", a.Home)
	}
}

// serveOne drives one Serve and returns the opening response, for the
// requests that are meant to be refused before anything runs. run above
// treats a refusal as a test failure, which is right for every request
// that should have started.
func serveOne(t *testing.T, req execwire.Request) execwire.Response {
	t.Helper()
	if req.User == "" {
		req.User = currentAccount(t)
	}

	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		done <- Serve(context.Background(), server)
	}()
	defer client.Close()

	if err := execwire.WriteRequest(client, req); err != nil {
		t.Fatal(err)
	}
	resp, err := execwire.ReadResponse(bufio.NewReader(client))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// The point of the field: a caller says where to run, instead of writing
// "cd /src && ..." into the command line and quoting it correctly.
func TestARequestedWorkingDirectoryIsWhereTheCommandRuns(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := run(t, execwire.Request{Line: "pwd", Dir: dir}, "")
	if got := strings.TrimSpace(res.stdout); got != dir {
		t.Fatalf("pwd = %q, want the requested %q", got, dir)
	}
}

// And without one, nothing moves: a session still starts in the
// account's home directory, which is where every session started before
// this field existed.
func TestWithoutOneTheSessionStillStartsInTheAccountsHome(t *testing.T) {
	acct, err := lookupAccount(currentAccount(t))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(acct.Home)
	if err != nil {
		t.Fatal(err)
	}
	res := run(t, execwire.Request{Line: "pwd"}, "")
	if got := strings.TrimSpace(res.stdout); got != want {
		t.Fatalf("pwd = %q, want the account's home %q", got, want)
	}
}

// A directory that is not there has to be refused with a message naming
// it. Left to the chdir in the forked child, the same mistake comes back
// from Start as "fork/exec /bin/sh: no such file or directory", which
// reads as a guest with no shell.
func TestAWorkingDirectoryThatIsNotThereIsRefusedByName(t *testing.T) {
	resp := serveOne(t, execwire.Request{Line: "echo unreachable", Dir: "/no/such/place"})
	if resp.OK {
		t.Fatal("a request naming a directory that does not exist was accepted")
	}
	if !strings.Contains(resp.Error, "/no/such/place") {
		t.Errorf("error %q does not name the directory", resp.Error)
	}
	if strings.Contains(resp.Error, "/bin/sh") {
		t.Errorf("error %q blames the shell for the caller's directory", resp.Error)
	}
}

func TestAWorkingDirectoryThatIsNotADirectoryIsRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	resp := serveOne(t, execwire.Request{Line: "echo unreachable", Dir: file})
	if resp.OK {
		t.Fatal("a request naming a regular file as its working directory was accepted")
	}
	if !strings.Contains(resp.Error, file) {
		t.Errorf("error %q does not name the path", resp.Error)
	}
}

// A relative path would be resolved against whatever the agent's own
// working directory happens to be, which is not something a caller can
// reason about -- so it is refused rather than resolved.
func TestARelativeWorkingDirectoryIsRefused(t *testing.T) {
	resp := serveOne(t, execwire.Request{Line: "pwd", Dir: "src"})
	if resp.OK {
		t.Fatal("a relative working directory was accepted")
	}
	if !strings.Contains(resp.Error, "absolute") {
		t.Errorf("error %q does not say what is wrong with it", resp.Error)
	}
}

// Overlaid, not substituted: the entries a caller sends are added to the
// login environment, and the rest of it -- the PATH guest images are
// built against, and the HOME/USER that describe the account -- is still
// there.
func TestARequestedEnvironmentIsOverlaidOnTheLoginEnvironment(t *testing.T) {
	res := run(t, execwire.Request{
		Line: `echo "$GOFLAGS"; echo "$PATH"; echo "$HOME"`,
		Env:  []string{"GOFLAGS=-mod=vendor"},
	}, "")
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q", res.stdout)
	}
	if lines[0] != "-mod=vendor" {
		t.Errorf("GOFLAGS = %q, want the value the request set", lines[0])
	}
	if want := defaultPATH(os.Getuid()); lines[1] != want {
		t.Errorf("PATH = %q, want the login default %q left alone", lines[1], want)
	}
	if lines[2] == "" {
		t.Error("HOME is empty: the login environment was replaced rather than added to")
	}
}

// Deliberately overriding one of those defaults is allowed, and the last
// entry for a key wins, the way "A=1 A=2 cmd" does in a shell.
func TestAnEntryCanOverrideTheLoginDefaultAndTheLastOneWins(t *testing.T) {
	res := run(t, execwire.Request{
		Line: `echo "$PATH"; echo "$HOME"`,
		Env:  []string{"PATH=/first/bin", "HOME=/somewhere/else", "PATH=/second/bin"},
	}, "")
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q", res.stdout)
	}
	if lines[0] != "/second/bin" {
		t.Errorf("PATH = %q, want the last value given", lines[0])
	}
	if lines[1] != "/somewhere/else" {
		t.Errorf("HOME = %q, want the override the request asked for", lines[1])
	}
}

// An entry with no "=" is a caller mistake, and this is the only place
// that can say so: passed through, it would reach the command as
// nothing at all.
func TestAnEnvironmentEntryThatIsNotKeyValueIsRefused(t *testing.T) {
	resp := serveOne(t, execwire.Request{Line: "echo unreachable", Env: []string{"GOFLAGS"}})
	if resp.OK {
		t.Fatal("an environment entry with no value was accepted")
	}
	if !strings.Contains(resp.Error, "GOFLAGS") {
		t.Errorf("error %q does not name the entry", resp.Error)
	}
}

// The capability line: every response says what this agent implements,
// refusal or not, because that is how a client finds out whether the
// Dir/Env it sent were honoured or silently dropped by an older guest.
func TestEveryResponseNamesTheFeaturesThisAgentImplements(t *testing.T) {
	res := run(t, execwire.Request{Line: "true"}, "")
	if !res.resp.Supports(execwire.FeatureDirEnv) {
		t.Errorf("a successful response reported features %v", res.resp.Features)
	}
	resp := serveOne(t, execwire.Request{Line: "echo unreachable", Dir: "/no/such/place"})
	if !resp.Supports(execwire.FeatureDirEnv) {
		t.Errorf("a refusal reported features %v; a client cannot tell a refusal from an old agent", resp.Features)
	}
}

// The other direction of the same problem: a client newer than this
// agent gets an answer naming the field, rather than a connection that
// closes without a word.
func TestARequestNamingAFieldThisAgentDoesNotKnowIsAnswered(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		done <- Serve(context.Background(), server)
	}()
	defer client.Close()

	if _, err := client.Write([]byte(`{"line":"echo hi","teleport":"/src"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	resp, err := execwire.ReadResponse(bufio.NewReader(client))
	if err != nil {
		t.Fatalf("the agent hung up on a request it could not honour: %v", err)
	}
	if resp.OK {
		t.Fatal("a request carrying an unknown field was accepted")
	}
	if !strings.Contains(resp.Error, "teleport") {
		t.Errorf("error %q does not name the field", resp.Error)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after refusing the request")
	}
}

// A service account's login shell refuses a login, which is what it is
// for -- and a command run as that account is not a login. Without this
// every `granule exec -u redis` answered "This account is currently not
// available", including the ones a Granulefile's `USER` above a `RUN` in
// a VM stage compiles to.
func TestACommandForAnAccountThatCannotLogInStillRuns(t *testing.T) {
	for _, shell := range []string{"/usr/sbin/nologin", "/sbin/nologin", "/bin/false"} {
		a := &account{Name: "redis", Shell: shell}
		if got := a.commandShell(); got != "/bin/sh" {
			t.Errorf("commandShell() for %s = %q, want /bin/sh", shell, got)
		}
		if a.Shell != shell {
			t.Errorf("Shell = %q, want the account's own %q left alone for an interactive session", a.Shell, shell)
		}
	}
}

// An ordinary account's shell is its own: a command runs under the shell
// the guest gave the account, which is what decides what its rc files
// and its PATH are.
func TestACommandForAnOrdinaryAccountRunsUnderItsOwnShell(t *testing.T) {
	a := &account{Name: "debian", Shell: "/bin/bash"}
	if got := a.commandShell(); got != "/bin/bash" {
		t.Errorf("commandShell() = %q, want /bin/bash", got)
	}
}

func TestForwardSessionTunnelsGuestPortToHost(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendLn.Close()
	go func() {
		conn, err := backendLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	guestPort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	specStr := fmt.Sprintf("127.0.0.1:%d:%s", guestPort, backendLn.Addr().String())
	spec, err := forward.ParseSpec(forward.RemoteToLocal, specStr)
	if err != nil {
		t.Fatal(err)
	}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- Serve(ctx, server)
	}()

	req := execwire.Request{
		Forward:      true,
		ForwardSpecs: []string{specStr},
	}
	if err := execwire.WriteRequest(client, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(client)
	resp, err := execwire.ReadResponse(br)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("Serve refused forward: %s", resp.Error)
	}

	clientDone := make(chan error, 1)
	go func() {
		clientDone <- forward.RunClient(ctx, br, client, []forward.Spec{spec}, nil)
	}()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", guestPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dialing forwarded guest port: %v", err)
	}
	defer conn.Close()

	const msg = "hello over agent forward"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != msg {
		t.Fatalf("got %q, want %q", got, msg)
	}

	cancel()
	_ = client.Close()
	_ = server.Close()
	<-clientDone
	<-serverDone
}

func TestForwardSessionRefusesInvalidSpecs(t *testing.T) {
	if resp := serveOne(t, execwire.Request{Forward: true, ForwardSpecs: []string{"not-a-spec"}}); resp.OK {
		t.Fatal("expected refusal for invalid forward spec")
	}
}
