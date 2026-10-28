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

//go:build !windows

package procgroup

import (
	"os/exec"
	"syscall"
)

// prepare sets Setpgid so cmd's process becomes the leader of a new
// process group that anything it forks (a shell's own pipeline, `claude`
// forking its own mcpserver child) inherits, then overrides Cancel to
// SIGKILL the negative pid -- POSIX's own "signal the whole group" idiom
// -- instead of exec.CommandContext's default of killing cmd.Process
// alone.
func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// kill signals the whole group the negative-pid way, falling back to the
// process alone where there is no group to signal (a caller that never
// asked for one, or a process that has already been reaped -- ESRCH).
func kill(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil || err == syscall.ESRCH {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}
