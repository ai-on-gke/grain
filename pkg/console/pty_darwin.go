//go:build darwin

package console

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// slaveNameLen is how large a buffer TIOCPTYGNAME insists on. The length
// is part of the request number itself -- Darwin's sys/ttycom.h defines
// it as _IOC(IOC_OUT, 't', 83, 128) -- and the kernel copies out that
// many bytes whatever the caller actually allocated, so a buffer sized by
// hand that disagreed with the constant would be a scribbled-over stack
// rather than a short read. Taking the length back out of the encoding
// (bits 16..28, the ioctl parameter length) is what makes the two unable
// to disagree.
const slaveNameLen = (unix.TIOCPTYGNAME >> 16) & 0x1fff

// openPTY allocates a pseudo-terminal pair the Darwin way. The shape is
// pty_linux.go's -- open the multiplexer, make the slave openable, find
// out which slave it is, open it -- but not one of the ioctls is shared:
// Darwin has neither TIOCGPTN nor TIOCSPTLCK, and its /dev/ptmx hands out
// /dev/ttysNNN rather than /dev/pts/N, so the slave's name has to be read
// back from the kernel instead of being printed from a number.
//
// The three ioctls are written out here for the reason pty_linux.go gives
// for writing out its two: grantpt(3), unlockpt(3) and ptsname(3) are C
// library wrappers around exactly these calls, and reaching them would
// mean either cgo -- which this module does not build with
// (CGO_ENABLED=0, see pkg/model/sqlite) -- or a dependency taken on for
// three syscalls.
//
// syscall.Syscall rather than one of x/sys/unix's Ioctl* helpers because
// TIOCPTYGNAME is an ioctl that writes into a buffer of the caller's, and
// x/sys/unix exports no way to hand it one: its ioctlPtr is unexported,
// and IoctlSetPointerInt, the nearest thing, only ever passes a pointer
// to an int32. The uintptr(unsafe.Pointer(...)) conversions are written
// inside the Syscall argument lists on purpose -- that is the one form
// the unsafe.Pointer rules keep valid, because the compiler knows to hold
// the pointee down for the duration of the call. Hoisting one into a
// local or a helper would let the garbage collector move the buffer while
// the kernel is writing into it.
//
// The master (first return) is what grain reads the agent's output from
// and writes the operator's keystrokes to; the slave (second) is what the
// CLI gets as its stdin, stdout and stderr, and what makes it believe it
// is being run by a person.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	fd := master.Fd()
	// TIOCPTYGRANT is grantpt: it hands the slave device node to this
	// user at mode 0620. Skipping it leaves the node owned by root, and
	// the OpenFile below fails with EACCES for a daemon that is not.
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(unix.TIOCPTYGRANT), 0); e != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("granting the terminal /dev/ptmx allocated: %w", e)
	}
	// TIOCPTYUNLK is unlockpt: until it is done the slave device cannot
	// be opened at all.
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(unix.TIOCPTYUNLK), 0); e != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("unlocking the terminal /dev/ptmx allocated: %w", e)
	}
	var name [slaveNameLen]byte
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("asking /dev/ptmx which terminal it allocated: %w", e)
	}
	// What came back is a NUL-terminated path in a fixed-size buffer. A
	// buffer with no NUL anywhere in it is not one, and opening all
	// slaveNameLen bytes as a filename would report that as a puzzling
	// ENOENT on a name full of padding rather than as what it is.
	end := bytes.IndexByte(name[:], 0)
	if end < 0 {
		master.Close()
		return nil, nil, fmt.Errorf("/dev/ptmx named its terminal with no terminator: %q", name[:])
	}
	slave, err = os.OpenFile(string(name[:end]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
