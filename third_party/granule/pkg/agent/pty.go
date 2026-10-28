package agent

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// attachPTY gives the session a pseudo-terminal and wires the command to
// its slave side.
//
// The pty is opened by hand rather than with a library because this
// binary ships inside the guest image, where every dependency is another
// thing to vendor, build CGO-free and keep working on two distributions.
// The sequence is the ordinary Linux one: open the multiplexer, unlock
// the pair, ask for the slave's number, open it.
func (s *session) attachPTY(cols, rows uint16) error {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening /dev/ptmx: %w", err)
	}

	if err := unix.IoctlSetPointerInt(int(ptmx.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		ptmx.Close()
		return fmt.Errorf("unlocking the pty: %w", err)
	}
	n, err := unix.IoctlGetInt(int(ptmx.Fd()), unix.TIOCGPTN)
	if err != nil {
		ptmx.Close()
		return fmt.Errorf("naming the pty: %w", err)
	}
	// O_NOCTTY: this process must not pick the terminal up as its own.
	// The child claims it, below, via Setctty.
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptmx.Close()
		return fmt.Errorf("opening the pty's other end: %w", err)
	}

	s.pty = ptmx
	s.slave = slave
	s.stdin = ptmx
	s.stdout = ptmx
	s.stderr = nil // one terminal, one stream

	s.cmd.Stdin = slave
	s.cmd.Stdout = slave
	s.cmd.Stderr = slave
	s.cmd.SysProcAttr.Setsid = true
	s.cmd.SysProcAttr.Setctty = true

	if cols > 0 && rows > 0 {
		s.resize(cols, rows)
	}
	return nil
}

// closeSlave drops this process's handle on the terminal once the child
// holds one of its own, so that reads on the master end see the child's
// exit as EOF.
func (s *session) closeSlave() {
	if s.slave != nil {
		_ = s.slave.Close()
		s.slave = nil
	}
}

// resize applies a terminal size. It is a no-op without a pty, since a
// client is free to send SIGWINCH-driven updates for a session that
// never asked for one.
func (s *session) resize(cols, rows uint16) {
	if s.pty == nil {
		return
	}
	_ = unix.IoctlSetWinsize(int(s.pty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Col: cols,
		Row: rows,
	})
}
