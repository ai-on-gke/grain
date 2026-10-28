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

package console

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openPTY allocates a pseudo-terminal pair the Linux way: open the
// multiplexer, ask it which slave it just handed out, unlock that slave
// and open it. There is no libc call to borrow here (grantpt/unlockpt are
// C library wrappers around exactly these two ioctls) and no dependency
// worth adding for four syscalls, which is why this is written out rather
// than pulled in -- see the module's own "pure Go, no cgo" rule in
// pkg/model/sqlite.
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
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("asking /dev/ptmx which terminal it allocated: %w", err)
	}
	// TIOCSPTLCK with a zero argument is unlockpt: until it is done the
	// slave device cannot be opened at all.
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlocking /dev/pts/%d: %w", n, err)
	}
	name := fmt.Sprintf("/dev/pts/%d", n)
	slave, err = os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
