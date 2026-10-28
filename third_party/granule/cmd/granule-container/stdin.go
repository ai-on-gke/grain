package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/workloadio"
)

// stdio is how the container's process is wired to the host, beyond the
// output pipes: its stdin, and the terminal when it runs on one.
type stdio struct {
	// afterStart drops this process's copies of what the container now
	// holds, and hands broker the workload's input.
	afterStart func()
	// done is closed once the terminal's output has all been copied; nil
	// without a terminal, whose output goes through the output pipes.
	done <-chan struct{}
	// close ends the input once the container has exited, so that
	// nothing the host sends later waits on a process that is gone.
	close func()
}

// wireStdio gives cmd its stdin -- and, for spec.Tty, its terminal --
// according to spec (see pkg/workloadio's "Stdin"):
//
//   - neither OpenStdin nor Tty: /dev/null, as a detached container reads;
//   - OpenStdin: a pipe the host's stdin is written to, whose end is the
//     host closing its end;
//   - Tty: a pseudo-terminal for all three streams, whose output goes to
//     out and whose size the host sets, and whose input is the host's if
//     OpenStdin is set too.
//
// broker has to have been told what to offer (OfferInput) before it
// served anything; this is what it then delivers to.
func wireStdio(cmd *exec.Cmd, spec containerspec.Spec, cred containerspec.Credential, broker *workloadio.Broker, out io.Writer) (stdio, error) {
	switch {
	case spec.Tty:
		return wireTerminal(cmd, spec, cred, broker, out)
	case spec.OpenStdin:
		r, w, err := os.Pipe()
		if err != nil {
			return stdio{}, fmt.Errorf("making a pipe for the container's stdin: %w", err)
		}
		cmd.Stdin = r
		in := &pipeInput{w: w}
		return stdio{
			afterStart: func() {
				r.Close()
				broker.SetInput(in)
			},
			close: func() { in.CloseWrite() },
		}, nil
	default:
		var null *os.File
		if f, err := os.Open(os.DevNull); err == nil {
			null = f
			cmd.Stdin = f
		}
		closeNull := func() {
			if null != nil {
				null.Close()
			}
		}
		return stdio{afterStart: closeNull, close: func() {}}, nil
	}
}

func wireTerminal(cmd *exec.Cmd, spec containerspec.Spec, cred containerspec.Credential, broker *workloadio.Broker, out io.Writer) (stdio, error) {
	master, slave, err := openTerminal()
	if err != nil {
		return stdio{}, err
	}
	if cred.Set {
		// The image's user owns its terminal, as it would after a login:
		// a process that reopens it by name (/proc/self/fd/0, ttyname)
		// has to be allowed to.
		_ = os.Chown(slave.Name(), int(cred.UID), int(cred.GID))
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A session of its own with the terminal as its controlling one, so
	// that ^C and the shell's job control mean what they mean under
	// `docker run -t`. The session leader leads its process group too,
	// which is what stopping the container signals; Setpgid would fail
	// on a session leader, so it gives way here.
	cmd.SysProcAttr.Setpgid = false
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Ends with EIO once every holder of the other end has closed it,
		// which is the terminal's end-of-file.
		io.Copy(out, master)
	}()
	in := &terminalInput{master: master, stdin: spec.OpenStdin}
	return stdio{
		afterStart: func() {
			slave.Close()
			broker.SetInput(in)
		},
		done:  done,
		close: func() { in.closeMaster() },
	}, nil
}

// openTerminal opens a pseudo-terminal pair -- see pkg/agent's
// attachPTY, which does the same for granule exec -t.
func openTerminal() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening /dev/ptmx for the container's terminal: %w", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlocking the container's terminal: %w", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("naming the container's terminal: %w", err)
	}
	// O_NOCTTY: the terminal is the container's, not this process's.
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("opening the container's terminal: %w", err)
	}
	return master, slave, nil
}

// pipeInput is a stdin that is a pipe: the host's end-of-file is closing
// it.
type pipeInput struct {
	w    *os.File
	once sync.Once
}

func (p *pipeInput) Write(b []byte) (int, error) { return p.w.Write(b) }

func (p *pipeInput) CloseWrite() error {
	var err error
	p.once.Do(func() { err = p.w.Close() })
	return err
}

func (p *pipeInput) Resize(cols, rows uint16) {}

// terminalInput is a stdin that is a terminal. A terminal cannot be
// half-closed -- its input and its output are one device, and closing
// it would take the container's output with it -- so the host's
// end-of-file is delivered the way a person at the keyboard delivers
// it: the terminal's EOF character (^D), which a reader in canonical
// mode sees as end-of-file. After an unfinished line it takes two, the
// first only ending the line. A program that has put the terminal in
// raw mode reads its own input and has no end-of-file to be given;
// nothing is sent to it.
type terminalInput struct {
	master *os.File
	stdin  bool // the container's stdin is open to the host

	mu       sync.Mutex
	closed   bool
	lastByte byte
}

func (t *terminalInput) Write(b []byte) (int, error) {
	if !t.stdin {
		return len(b), nil
	}
	n, err := t.master.Write(b)
	if n > 0 {
		t.mu.Lock()
		t.lastByte = b[n-1]
		t.mu.Unlock()
	}
	return n, err
}

func (t *terminalInput) CloseWrite() error {
	if !t.stdin {
		return nil
	}
	t.mu.Lock()
	last := t.lastByte
	t.mu.Unlock()
	termios, err := unix.IoctlGetTermios(int(t.master.Fd()), unix.TCGETS)
	if err != nil {
		return err
	}
	if termios.Lflag&unix.ICANON == 0 {
		return nil
	}
	eof := []byte{termios.Cc[unix.VEOF]}
	if last != 0 && last != '\n' && last != termios.Cc[unix.VEOF] {
		eof = append(eof, eof[0])
	}
	_, err = t.master.Write(eof)
	return err
}

func (t *terminalInput) Resize(cols, rows uint16) {
	_ = unix.IoctlSetWinsize(int(t.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows})
}

// closeMaster closes the terminal once the container has exited and its
// output has been copied (or given up on).
func (t *terminalInput) closeMaster() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.closed = true
		t.master.Close()
	}
}
