// Package agent is the guest-side half of "granule exec": it runs one
// command per connection and speaks pkg/execwire back over it.
//
// It is deliberately separate from cmd/granule-agent, which is only the
// vsock listener around it, so that everything below can be tested
// against an ordinary net.Pipe on a build machine -- there is no VM in
// `go test`, and a session handler that could only be exercised inside
// one would be a session handler nobody exercises.
//
// # Reproducing what sshd did
//
// This replaces an sshd, and the environment a command runs in is part
// of what it replaces. sshd runs a non-interactive command as
// `<shell> -c <line>` with no profile read at all, handing it a fixed
// default PATH; the guest images built on this rely on that in ways that
// look nothing like a shell setting when they break. grain's own guest
// puts the Go and Node toolchains in /usr/local/bin specifically because
// that directory is in sshd's default PATH and nothing it ships reads a
// profile (see grain's scripts/kontur/guest-setup.sh). So loginEnv below
// reproduces that environment rather than inventing a cleaner one, and
// an interactive session (an empty command line) gets a login shell,
// which is the other thing sshd did.
//
// A request can move away from both of those, and that is the point of
// having them be a base rather than the whole answer: Request.Dir picks
// the working directory (the account's home otherwise, as before) and
// Request.Env is overlaid on loginEnv key by key. So a caller that wants
// its own toolchain on PATH says so per command instead of installing
// into /usr/local/bin to land inside the fixed one, and the parts of the
// environment that describe the *account* stay true unless that caller
// deliberately overrides them.
//
// The terminal type is the one part of that environment this end cannot
// know. sshd forwarded the caller's own TERM; a pty allocated in here has
// no idea what is on the other side of it, and the agent's environment in
// a guest is whatever init handed it, which is nothing -- so a real
// terminal used to run under TERM=dumb, and vim, less and htop all
// behaved as if there were nothing to draw on. execwire.Request.Term
// carries it now, and terminalType below says what happens when a client
// does not send one.
//
// Supplementary groups are part of the same debt. A guest account is
// routinely in a group that grants something -- `docker`, most of all --
// and a session that set only uid and gid would leave every `docker`
// command failing on the socket's permissions, with nothing about the
// error naming the group it lost.
//
// # Ending a command
//
// A session ends for one of three reasons, and only the first is the
// command's own doing: it exits; the client asks for it to be signalled
// (execwire.TypeSignal); or the session's context is cancelled, which is
// what a client dropping the connection and what cmd/granule-agent
// closing a connection down both come to. The last two go through
// terminate, which SIGTERMs and then SIGKILLs, and both reach the
// command's whole process group rather than the shell at the top of it.
//
// The group matters more than it looks. Every session runs
// `<shell> -c <line>`, and a line that starts anything of its own -- a
// build, a test runner, a server -- leaves the shell waiting on a child
// that a signal to the shell alone never touches. start therefore puts
// the command in a process group of its own, so that signalling the
// negative pid means this session and nothing else in the guest.
//
// None of this existed before: an abandoned session closed the guest
// command's stdin and left everything else running, which in a
// long-lived guest is one leaked process per timed-out call, with
// nothing that reports them.
//
// # When the command exits before its output does
//
// "The command exits" and "the command's output ends" read like one
// event and are not. The output is a pipe, and a command that starts
// something of its own without redirecting that child's output leaves
// the writing end open in a process its own exit says nothing about:
// `granule exec -- 'myserver &'` exits immediately, and the pipe stays
// open for as long as the server lives. A session that drained the
// output to its end before reporting the exit -- which is what this did,
// because os/exec's own pipes are closed by Wait and reaping first
// truncates a command's last words -- therefore did not end at all, and
// its client waited on an exit frame that could not arrive.
//
// Both halves are kept. attachPipes gives the session pipes it owns, so
// nothing Wait does can truncate the output and the command can be
// reaped while the drain is still running; pump then bounds the drain by
// outputGrace once the command has been reaped, and tells the client
// what happened -- an execwire.TypeNote frame saying the output stops
// short, then the command's real exit code.
//
// What it does not do is signal anything. The process holding the output
// open is one a caller deliberately backgrounded, the command that
// started it exited on its own, and there is still a client reading, so
// none of what makes an abandoned session's process group worth killing
// is true here. It is left running -- exactly as the same process is
// left running when the caller redirects its output and the session ends
// without ever noticing it.
package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
)

// terminateGrace is how long a command has between the SIGTERM that asks
// it to stop and the SIGKILL that stops it -- long enough for a shell to
// run a trap and a build to write out what it was doing, short enough
// that a guest whose client has already gone is not holding the process
// for a noticeable time. Nothing is waiting on the difference: by the
// time either signal is sent, the session's output has nowhere to go.
const terminateGrace = 5 * time.Second

// outputGrace is how long a session goes on draining output after the
// command itself has exited.
//
// Ordinarily nothing waits here at all: the command held the writing end
// of the pipes, they closed as it died, and the drain has already
// finished. The bound is for the session where something else holds
// them -- a process the command started and left running with the
// session's stdout still attached, which is what
// `granule exec -- 'myserver &'` produces whenever the caller does not
// redirect the child's output. That descriptor stays open for as long as
// that process lives, so a drain that waits for it is a client waiting
// for an exit frame that cannot arrive until somebody stops a server.
//
// The price of bounding it is a client too slow to accept the last of
// the output inside the window: its tail is dropped. That is worth
// paying only because the client is told (cutNote) rather than left to
// read a short session as a complete one. Five seconds, then -- long
// enough that reaching it means a client that has stopped reading rather
// than one merely behind, and short enough that the case above, which is
// otherwise unbounded, costs a caller nothing worth measuring.
const outputGrace = 5 * time.Second

// Serve handles exactly one exec session on conn and returns when the
// command has finished and its exit frame has been written.
//
// A failure to *start* the command is reported in the opening Response
// and returns nil: the session was served correctly, it is the command
// that could not run. Only a broken connection or a malformed request is
// an error here.
//
// ctx bounds the command, not just this function: cancelling it ends
// whatever the session started, so a caller that gives Serve a context
// per connection (cmd/granule-agent does) leaves nothing behind in the
// guest when the connection goes.
func Serve(ctx context.Context, conn io.ReadWriter) error {
	br := bufio.NewReader(conn)

	req, err := execwire.ReadRequest(br)
	if err != nil {
		if errors.Is(err, execwire.ErrUnknownField) {
			// A request this build cannot honour in full, rather than a
			// connection that broke: the client is owed the reason, and
			// gets it the same way a command that could not start does.
			_ = execwire.WriteResponse(conn, execwire.Response{OK: false, Error: err.Error(), Features: execwire.Features})
			return nil
		}
		return fmt.Errorf("reading the request: %w", err)
	}

	if req.Forward {
		return serveForward(ctx, br, conn, req)
	}

	sess, err := start(ctx, req)
	if err != nil {
		// Best effort: if the connection is gone too, the caller has
		// already lost the answer anyway.
		_ = execwire.WriteResponse(conn, execwire.Response{OK: false, Error: err.Error(), Features: execwire.Features})
		return nil
	}
	// Cancelling ends the command; closing the files hands back the
	// descriptors. The second half is not something a session used to
	// have to do: os/exec closed the output pipes itself as part of
	// Wait, and does not close pipes it did not make (see attachPipes),
	// so without this every call would leak two descriptors in an agent
	// that goes on running for the life of the guest.
	defer func() {
		sess.cancel()
		sess.closeFiles()
	}()

	// Every response carries the feature list, refusal or not: it is how
	// a client that asked for Dir or Env finds out whether this agent is
	// new enough to have honoured them, and how one that may want to
	// signal the command finds out whether the frame will be acted on
	// (see execwire's package comment).
	if err := execwire.WriteResponse(conn, execwire.Response{OK: true, Features: execwire.Features}); err != nil {
		// The command started and its client is already gone. Cancel
		// ends it, and reaping it here rather than returning straight
		// away is what keeps this from leaving a zombie behind in a
		// guest whose init may never collect one.
		sess.cancel()
		_ = sess.reap()
		return fmt.Errorf("writing the response: %w", err)
	}

	return sess.pump(br, conn)
}

// session is one running command and the file descriptors wired to it.
type session struct {
	cmd *exec.Cmd

	// stdin is what the client's stdin frames are written to, and
	// stdout/stderr what is read back. With a pty all three are the same
	// file: a terminal has one stream, and closing it once is what ends
	// the session.
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	pty   *os.File // nil unless the request asked for one
	slave *os.File // the child's end, closed once the child holds it

	// childOut are the writing ends of the output pipes -- the child's
	// descriptors, which this process closes its own copies of as soon
	// as the child has them. Empty for a pty session, where the slave
	// above is the same thing.
	childOut []*os.File

	// cancel ends the command. It is the cancel half of the context the
	// command was started with, so calling it goes through os/exec's own
	// cancellation and arrives at terminate below.
	cancel context.CancelFunc

	// mu guards writes to the connection: stdout and stderr are pumped
	// by separate goroutines into one stream of frames.
	mu sync.Mutex

	// killMu guards the SIGKILL waiting behind a SIGTERM, and the flag
	// that says the command has been reaped. Both are needed to stop
	// that timer firing at a pid the kernel has since given to
	// something else.
	killMu    sync.Mutex
	killTimer *time.Timer
	reaped    bool
}

// launch is what a request resolves to before anything is started: the
// program and its arguments, where and with what environment it runs,
// and as whom. Chroot, when set, is the root everything else is resolved
// inside -- Argv[0] and Dir are paths after the chroot, not before it.
type launch struct {
	Argv       []string
	Dir        string
	Env        []string
	Credential *syscall.Credential
	Chroot     string

	// Who names the account in errors about starting the command.
	Who string
}

// guestLaunch is a session in the guest itself: an account in the
// guest's own passwd, its login shell, and the environment sshd would
// have given it.
func guestLaunch(req execwire.Request) (launch, error) {
	if len(req.Argv) > 0 {
		return launch{}, errors.New("a request's argv is only honoured together with container: a command in the guest runs through the account's shell, from its line")
	}
	acct, err := lookupAccount(req.User)
	if err != nil {
		return launch{}, err
	}

	argv := []string{acct.commandShell(), "-c", req.Line}
	if strings.TrimSpace(req.Line) == "" {
		// No command: an interactive login shell, the same fallback
		// sshd makes for a session with no command.
		argv = []string{acct.Shell, "-l"}
	}

	dir, err := workingDir(req.Dir, acct)
	if err != nil {
		return launch{}, err
	}
	env, err := sessionEnv(acct, terminalType(req), req.Env)
	if err != nil {
		return launch{}, err
	}
	return launch{Argv: argv, Dir: dir, Env: env, Credential: acct.credential(), Who: acct.Name}, nil
}

func start(ctx context.Context, req execwire.Request) (*session, error) {
	var (
		l   launch
		err error
	)
	if req.Container {
		l, err = containerLaunch(containerConfig, req)
	} else {
		l, err = guestLaunch(req)
	}
	if err != nil {
		return nil, err
	}
	argv := l.Argv

	s := &session{}
	ctx, s.cancel = context.WithCancel(ctx)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if l.Chroot != "" {
		// exec.Command looks a bare name up on the agent's own PATH;
		// a container's program is already resolved, inside its root,
		// to the path it has after the chroot -- which is the only path
		// that may be exec'd.
		cmd.Path = argv[0]
		cmd.Err = nil
	}
	cmd.Dir = l.Dir
	cmd.Env = l.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Chroot: l.Chroot}
	// os/exec's own cancellation is a SIGKILL to the one process it
	// started. Replace it with terminate, which asks the whole process
	// group first and only insists afterwards.
	cmd.Cancel = s.terminate
	if l.Credential != nil {
		cmd.SysProcAttr.Credential = l.Credential
	}

	s.cmd = cmd
	if req.TTY {
		// attachPTY asks for a session of its own (Setsid), which
		// already makes the child a process group leader -- asking for
		// Setpgid as well would be a setpgid(2) on a session leader,
		// which fails with EPERM before the command ever runs.
		if err := s.attachPTY(req.Cols, req.Rows); err != nil {
			s.cancel()
			return nil, err
		}
	} else {
		// A process group of its own, so that signalling this session
		// reaches what `<shell> -c` started and not just the shell.
		cmd.SysProcAttr.Setpgid = true
		if err := s.attachPipes(); err != nil {
			s.cancel()
			return nil, err
		}
	}

	if err := cmd.Start(); err != nil {
		s.closeFiles()
		s.cancel()
		if req.Dir != "" && errors.Is(err, fs.ErrPermission) {
			// The chdir happens in the forked child, after it has
			// dropped to the account's credentials, and the errno that
			// comes back names the binary rather than the directory --
			// "fork/exec /bin/sh: permission denied" for a directory
			// that root can stat and this account cannot enter. Say
			// which of the two it probably was.
			return nil, fmt.Errorf("starting %s in %s: %w (is %s reachable by %s? the agent can see it, the session runs as the account)", argv[0], cmd.Dir, err, cmd.Dir, l.Who)
		}
		return nil, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	// The child holds its own ends now -- the pty's slave side, or the
	// writing ends of the output pipes. Closing this process's copies
	// here is what makes a read on the other end finish when the child
	// does, rather than wait on a descriptor this process itself keeps
	// open. (A descriptor some *other* process keeps open is the case
	// pump has to bound; this one is simply not to be created.)
	s.closeSlave()
	s.closeChildOut()
	return s, nil
}

// attachPipes wires the command to pipes this session owns outright,
// rather than to the ones StdoutPipe and StderrPipe hand back.
//
// The difference is who closes the reading end, and it decides what
// order a session is allowed to end in. os/exec closes the pipes it made
// itself as part of Wait, so a session built on those has exactly one
// safe order -- drain to EOF, and only then reap -- because reaping
// first pulls the output out from under the drain and truncates whatever
// the command wrote just before exiting. Owning them here means Wait
// touches nothing this session reads from, so the command can be reaped
// while the drain is still going, which is what lets pump put a bound on
// the drain instead of waiting on it forever.
func (s *session) attachPipes() error {
	var err error
	if s.stdin, err = s.cmd.StdinPipe(); err != nil {
		return fmt.Errorf("wiring stdin: %w", err)
	}
	if s.stdout, err = s.outputPipe(&s.cmd.Stdout); err != nil {
		return fmt.Errorf("wiring stdout: %w", err)
	}
	if s.stderr, err = s.outputPipe(&s.cmd.Stderr); err != nil {
		return fmt.Errorf("wiring stderr: %w", err)
	}
	return nil
}

// outputPipe points one of the command's output streams at a new pipe
// and returns the reading end, recording the writing end as one for the
// child to inherit and for this process to drop (see closeChildOut).
//
// An *os.File assigned to cmd.Stdout becomes that descriptor in the
// child directly: os/exec neither copies it through a goroutine of its
// own nor takes on closing it. Both halves of that are the point -- see
// attachPipes.
func (s *session) outputPipe(dst *io.Writer) (io.ReadCloser, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	*dst = w
	s.childOut = append(s.childOut, w)
	return r, nil
}

// pump moves bytes in both directions until the command exits and its
// output has been drained, then writes the exit frame.
func (s *session) pump(br *bufio.Reader, w io.Writer) error {
	var wg sync.WaitGroup

	// Output. With a pty, stderr is nil: everything is on the one
	// stream, exactly as it is over SSH with a pty.
	wg.Add(1)
	go func() { defer wg.Done(); s.copyOut(w, s.stdout, execwire.TypeStdout) }()
	if s.stderr != nil {
		wg.Add(1)
		go func() { defer wg.Done(); s.copyOut(w, s.stderr, execwire.TypeStderr) }()
	}
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()

	// Input. This one is not waited on: a client that never closes
	// stdin (an interactive session, or any caller that leaves the pipe
	// open) would otherwise hold the session open past the command's own
	// exit. It ends on its own when the connection closes.
	go s.copyIn(br)

	// Reaping and draining run side by side, because they are not the
	// same event and a session needs both. Wait returns when the command
	// exits; the drain ends when the last holder of the writing end
	// closes it, and the command is only usually the last holder.
	//
	// Neither can simply be done first. Reaping first was never an
	// option -- it is Wait that would close the pipes if os/exec had
	// made them, and even with pipes this session owns (attachPipes) the
	// exit frame would then be free to overtake the output it concludes.
	// Draining first is what this used to do, and it is what leaves a
	// session hanging on a process the command started and left running
	// with the session's stdout attached. So both are started, and the
	// drain is the one that gets a bound.
	//
	// exec.Cmd.WaitDelay is not that bound: it applies to the I/O
	// copying Wait itself does, and this session does its own precisely
	// so that Wait is not in the path.
	reaped := make(chan error, 1)
	go func() { reaped <- s.reap() }()

	var err error
	cut := false
	select {
	case <-drained:
		// The output ended first, which is the ordinary case: the
		// command held the writing end and it closed as the command
		// died. All that is left is the command's own status, and it is
		// worth waiting for however long it takes -- a session whose
		// output has closed while the command runs on (one that
		// redirected its own stdout, say) is still a running command.
		err = <-reaped
	case err = <-reaped:
		// The command is gone and its output is not, so something the
		// command started is holding it open. Give that a bounded while
		// to end on its own, and then stop waiting for it.
		select {
		case <-drained:
		case <-time.After(outputGrace):
			// Nothing is signalled here, deliberately. What holds the
			// output open is a process the caller backgrounded on
			// purpose, the command that started it exited cleanly, and
			// its client is still on the other end -- none of the
			// conditions that make an abandoned session's process group
			// worth killing (see the package comment). Killing it would
			// also make "&" mean two different things depending on
			// whether the caller redirected the child's output, since a
			// backgrounded process that does redirect closes this pipe
			// and is never noticed here at all.
			//
			// reap has already returned, so the cancel Serve defers
			// finds the command reaped and signals nothing either -- see
			// signalGroupLocked.
			cut = true
			s.closeOutputs()
			<-drained
		}
	}

	code := exitCode(err)
	if cut {
		// Said before the exit frame and separately from it: the status
		// is the command's real one, because the command really did
		// exit, and this is the only thing that can tell the client the
		// output around it stops short of what was written.
		if err := execwire.WriteFrame(w, execwire.TypeNote, []byte(cutNote(code))); err != nil {
			return err
		}
	}
	return execwire.WriteFrame(w, execwire.TypeExit, execwire.EncodeExit(code))
}

// cutNote is what a client is told when outputGrace runs out. The fix is
// in the caller's own command line rather than anywhere in the guest, so
// this names the shape of command that causes it.
func cutNote(code int) string {
	return fmt.Sprintf("granule-agent: the command exited with status %d, but something it started still had the session's output open, so the session stopped waiting for it after %s: any output after this point was not sent. A process meant to outlive the command should have its output redirected -- \"myserver >/dev/null 2>&1 &\" -- which lets the session end when the command does.", code, outputGrace)
}

func (s *session) copyOut(w io.Writer, r io.Reader, typ byte) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.mu.Lock()
			werr := execwire.WriteFrame(w, typ, buf[:n])
			s.mu.Unlock()
			if werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *session) copyIn(br *bufio.Reader) {
	for {
		typ, payload, err := execwire.ReadFrame(br)
		if err != nil {
			// The client is gone, so nothing is going to read this
			// command's output or its exit code. Close this end
			// unconditionally -- including the pty, where endStdin
			// deliberately would not -- so a command blocked on a read
			// ends rather than waiting on a client that has left, and
			// cancel the session, which ends the command itself.
			//
			// Closing stdin used to be the whole of this, and it is not
			// enough: a command that never reads stdin -- almost all of
			// them -- carried on running in the guest with nobody
			// waiting for it and nothing left to notice it.
			s.closeStdin()
			s.cancel()
			return
		}
		switch typ {
		case execwire.TypeStdin:
			if _, err := s.stdin.Write(payload); err != nil {
				return
			}
		case execwire.TypeStdinClose:
			s.endStdin()
		case execwire.TypeWinsize:
			cols, rows, err := execwire.DecodeWinsize(payload)
			if err == nil {
				s.resize(cols, rows)
			}
		case execwire.TypeSignal:
			// The client interrupting the command, rather than
			// abandoning it: the session stays open afterwards, so
			// whatever the command prints on its way out still reaches
			// the client, and its exit still arrives as an exit frame.
			//
			// No permission check, because there is nothing to check
			// against: whoever can open this session can already run
			// arbitrary commands as this account, and could send the
			// same signal with a `kill` of their own.
			sig, err := execwire.DecodeSignal(payload)
			if err == nil {
				_ = s.signalGroup(syscall.Signal(sig))
			}
		}
	}
}

// endStdin handles the client saying "no more input".
//
// Without a pty that is a close, and a command blocked on a read sees
// EOF. With one it must not be: stdin, stdout and stderr are the same
// terminal, so closing it would end the session rather than the input,
// and take the command's own output with it. A terminal signals
// end-of-input in band instead, which is the client's business to send
// (^D) and not something to synthesize here.
func (s *session) endStdin() {
	if s.pty != nil {
		return
	}
	s.closeStdin()
}

func (s *session) closeStdin() {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
}

func (s *session) closeFiles() {
	s.closeStdin()
	s.closeOutputs()
	s.closeChildOut()
}

// closeOutputs takes this end of the command's output away, which is
// what makes a drain that is blocked on somebody else's descriptor
// return -- there is nothing else that can, since the read it is sitting
// in ends when the writing end closes and the whole difficulty is that
// this session does not own the process holding it.
//
// It is also the moment whatever still holds the other end loses its
// reader, so a background process that goes on writing to it gets EPIPE
// and the SIGPIPE with it -- the same end a process whose ssh session
// has been closed comes to. That is a consequence worth knowing rather
// than a choice: the alternative is a descriptor and a goroutine per
// session kept alive in the guest for as long as that process lives.
func (s *session) closeOutputs() {
	if s.stdout != nil {
		_ = s.stdout.Close()
	}
	if s.stderr != nil {
		_ = s.stderr.Close()
	}
}

// closeChildOut drops this process's copies of the writing ends of the
// output pipes, once the child holds its own. It is closeSlave's
// counterpart for a session without a pty, and it matters for the same
// reason: a copy left open here would keep every read on the other end
// waiting on this process rather than on the command.
func (s *session) closeChildOut() {
	for _, f := range s.childOut {
		_ = f.Close()
	}
	s.childOut = nil
}

// signalGroup sends sig to the command's process group.
//
// The group, not the process: start gives every session a group of its
// own precisely so that this negative pid means "the command and
// everything it started" and reaches, say, the compiler a `sh -c 'make'`
// is sitting waiting on.
func (s *session) signalGroup(sig syscall.Signal) error {
	s.killMu.Lock()
	defer s.killMu.Unlock()
	return s.signalGroupLocked(sig)
}

// signalGroupLocked is signalGroup with killMu already held, which is
// what makes the check against reaped mean anything: once the command
// has been waited for, its pid is the kernel's to hand out again, and a
// signal frame that arrives a moment late must go nowhere rather than to
// whatever now holds that number.
func (s *session) signalGroupLocked(sig syscall.Signal) error {
	if s.cmd == nil || s.cmd.Process == nil || s.reaped {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-s.cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		// Either the command is already gone, or -- in the moment
		// between the fork and the child's own setpgid -- the group does
		// not exist yet. Signalling the process directly covers the
		// second without having to tell them apart, and reports
		// os.ErrProcessDone for the first.
		return s.cmd.Process.Signal(sig)
	}
	return err
}

// terminate ends the command: SIGTERM to its process group now, SIGKILL
// to it after terminateGrace if it is still there. It is what the
// session's context cancellation runs (see start's cmd.Cancel).
//
// SIGTERM first rather than a straight kill, because the command is
// frequently a shell with a trap, a test runner with children of its own
// or a build holding a lock, and each of those has cleanup that is worth
// the wait to a guest that goes on living afterwards. SIGKILL after it,
// because "asked politely and never checked" is how the abandoned
// processes this exists to stop would come back.
func (s *session) terminate() error {
	s.killMu.Lock()
	defer s.killMu.Unlock()

	err := s.signalGroupLocked(syscall.SIGTERM)
	if errors.Is(err, os.ErrProcessDone) {
		// Reported rather than swallowed: os/exec reads this exact error
		// as "there was nothing to cancel", and hands Wait's own result
		// back to the caller instead of the context's error.
		return err
	}
	if s.killTimer != nil {
		return err
	}
	s.killTimer = time.AfterFunc(terminateGrace, func() {
		_ = s.signalGroup(syscall.SIGKILL)
	})
	return err
}

// reap waits for the command and calls off any SIGKILL still pending
// against it, so that nothing is left aimed at a pid the kernel is free
// to reuse the moment this returns.
func (s *session) reap() error {
	err := s.cmd.Wait()

	s.killMu.Lock()
	defer s.killMu.Unlock()
	s.reaped = true
	if s.killTimer != nil {
		s.killTimer.Stop()
		s.killTimer = nil
	}
	return err
}

// exitCode turns Wait's error into the number to report. A command
// killed by a signal has no exit status of its own, and -1 is what
// os/exec already reports for it.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	// Wait failed for a reason that is not the command's own exit --
	// 127 is the shell's own "could not run it", which is the closest
	// true thing to say.
	return 127
}

// account is the parts of a guest account a session needs. os/user
// answers most of it, but not the login shell, and not the groups --
// hence the two small readers below rather than a straight
// user.Lookup.
type account struct {
	Name   string
	UID    int
	GID    int
	Home   string
	Shell  string
	Groups []uint32

	// self is set when the request names the account this process is
	// already running as, in which case no credential is applied: the
	// agent runs as root in the guest, so this is only ever true in
	// tests and when a caller asks for root explicitly.
	self bool
}

func (a *account) credential() *syscall.Credential {
	if a.self {
		return nil
	}
	return &syscall.Credential{Uid: uint32(a.UID), Gid: uint32(a.GID), Groups: a.Groups}
}

func loginEnv(a *account, term string) []string {
	return []string{
		"PATH=" + defaultPATH(a.UID),
		"HOME=" + a.Home,
		"USER=" + a.Name,
		"LOGNAME=" + a.Name,
		"SHELL=" + a.Shell,
		"TERM=" + term,
	}
}

// defaultTTYTerm is what a pty session gets when its client did not say
// what kind of terminal it has. Something has to be picked -- a real
// terminal with TERM=dumb in it is one every curses program refuses to
// draw on -- and plain "xterm" is the conservative guess: it is the
// terminfo entry a guest has if it has any at all, and it claims nothing
// this end cannot see for itself.
const defaultTTYTerm = "xterm"

// terminalType picks the TERM a session's environment gets.
//
// The client's own comes first, because it is the only end that knows:
// the terminal is on its screen, not in the guest, which is exactly what
// sshd forwarded and what Request.Term restores. Then the agent's own
// environment, which is all this used to consult -- in a guest that is
// whatever init handed granule-agent, i.e. nothing, which is how a real
// terminal ended up running vim under TERM=dumb.
//
// The last resort splits on whether there is a terminal at all: "dumb"
// for a session without one, where stopping curses programs from drawing
// is precisely right and always was, and defaultTTYTerm for a session
// that has a pty and a client too old to name a type for it.
func terminalType(req execwire.Request) string {
	if req.Term != "" {
		return req.Term
	}
	if t := os.Getenv("TERM"); t != "" {
		return t
	}
	if req.TTY {
		return defaultTTYTerm
	}
	return "dumb"
}

// sessionEnv overlays a request's own "KEY=value" entries onto the login
// environment, rather than replacing it.
//
// Overlay and not replacement because the parts of loginEnv that come
// from the account -- HOME, USER, LOGNAME, SHELL -- have to keep
// agreeing with the account the command actually runs as. A caller
// asking for GOFLAGS is not asking to be moved out of its home
// directory, and a caller that really does want a different HOME can say
// so, since a later entry wins over the base.
//
// The keys are applied in the order given, and a repeated key takes its
// last value, which is what a shell's own "A=1 A=2 cmd" does. That
// includes TERM: a caller who sends one as an entry of its own overrides
// the terminal type the session was opened with.
func sessionEnv(a *account, term string, extra []string) ([]string, error) {
	env := loginEnv(a, term)
	for _, e := range extra {
		key, _, ok := strings.Cut(e, "=")
		if !ok || key == "" {
			// Not something to pass through and let the command puzzle
			// over: an entry with no "=" is a caller mistake, and the
			// only place that can name it is here.
			return nil, fmt.Errorf("environment entry %q is not in KEY=value form", e)
		}
		env = setEnv(env, key, e)
	}
	return env, nil
}

// setEnv replaces the entry for key in env with entry, or appends it.
func setEnv(env []string, key, entry string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = entry
			return env
		}
	}
	return append(env, entry)
}

// workingDir resolves the directory a session starts in: the request's
// own, when it names one, and otherwise the account's home the way every
// session before this field existed started.
//
// A directory that is not there is refused before the command runs,
// because the alternative is worse than it looks: the chdir happens in
// the forked child, so the failure comes back from Start as
// "fork/exec /bin/sh: no such file or directory" -- which reads as a
// guest with no shell rather than as a caller who asked for a directory
// the guest does not have.
//
// This stat runs as the agent (root), so it answers "is it there", not
// "can this account get into it". The second question is only truly
// answered by the chdir itself, which is why Start's error is annotated
// too rather than trusted to be self-explanatory.
func workingDir(dir string, a *account) (string, error) {
	if dir == "" {
		return a.Home, nil
	}
	if !filepath.IsAbs(dir) {
		// A relative path would be resolved against the agent's own
		// working directory -- whatever init left it at -- which is not
		// something a caller can reason about, and not what "relative"
		// would suggest either.
		return "", fmt.Errorf("working directory %q must be an absolute path", dir)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("working directory %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("working directory %s is not a directory", dir)
	}
	return dir, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
