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

//go:build linux || darwin

package console

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// This file is the half of the PTY that Linux and Darwin genuinely share.
//
// openPTY is per-platform and lives in pty_linux.go and pty_darwin.go:
// the ioctls that allocate a pair have no overlap at all between the two.
// These two do, byte for byte -- both are BSD interfaces that Linux
// adopted unchanged -- and they were duplicated for one release before
// this file existed. That is a bad shape for attachTTY in particular: it
// is the controlling-terminal handoff, the reasoning below is subtle, and
// a correction made to one copy would silently not reach the other.

// attachTTY makes the child a session of its own with the PTY as its
// controlling terminal -- which is what a ^C typed into the session
// signals, and what stops the CLI from reaching for the daemon's own
// terminal (there is none) instead.
//
// Setsid also puts the child in a brand new process group whose id is its
// pid, so procgroup.Kill's "signal the negative pid" still reaches
// everything the CLI forks. Setpgid is deliberately not set alongside it:
// a session leader cannot then setpgid itself, and asking for both is an
// exec that fails before the CLI ever runs.
//
// Ctty is 0 because Start hands the slave to the child as its stdin,
// stdout and stderr: with Setctty, Ctty names a file descriptor in the
// *child*, not in this process.
func attachTTY(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0
}

// resizePTY tells the terminal how large the window showing it is. A
// fresh PTY reports 0x0, and a TUI given that draws itself into a corner
// or not at all, so a client sends its size the moment it attaches.
func resizePTY(master *os.File, rows, cols int) error {
	err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	})
	if err != nil {
		return fmt.Errorf("console: resizing the session to %dx%d: %w", cols, rows, err)
	}
	return nil
}
