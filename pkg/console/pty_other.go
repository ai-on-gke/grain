//go:build !linux && !darwin

package console

import (
	"errors"
	"os"
	"os/exec"
)

// errNoPTY is every platform but Linux and macOS. grain is deployed as
// one container on one Linux VM (scripts/setup.sh, terraform/gcp) and
// developed on macOS laptops, so the PTY half of this package is written
// against those two platforms' own ioctls (pty_linux.go, pty_darwin.go)
// and this file is what keeps the rest of it -- the Manager, the ring,
// the wrapper -- compiling anywhere else.
//
// A deployment that ever wants console sessions elsewhere writes the
// three functions below for that platform; nothing above them is
// platform-specific.
var errNoPTY = errors.New("console: interactive sessions need a pseudo-terminal, which this platform's build of grain does not provide")

func openPTY() (master, slave *os.File, err error) { return nil, nil, errNoPTY }

func attachTTY(cmd *exec.Cmd) {}

func resizePTY(master *os.File, rows, cols int) error { return errNoPTY }
