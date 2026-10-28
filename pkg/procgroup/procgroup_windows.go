//go:build windows

package procgroup

import (
	"os"
	"os/exec"
)

// prepare leaves cmd's Cancel at exec.CommandContext's own default (kill
// cmd.Process alone) -- Windows has no POSIX process-group signal to send
// instead, and nothing in this codebase currently ships or tests a
// Windows build; a real fix here would use a Job Object.
func prepare(cmd *exec.Cmd) {}

// kill has no process group to reach on Windows, for prepare's own
// reason: there is nothing here that puts one together.
func kill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
