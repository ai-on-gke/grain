// Package procgroup makes exec.CommandContext's cancellation reach a
// command's whole process tree, not just the one process it directly
// forked.
//
// exec.CommandContext's default Cancel only ever signals cmd.Process
// itself (os/exec's own doc comment: "Cancel ... calls cmd.Process.Kill()").
// That is not enough for bwsalmon/agents#346: killing `claude` on cancel
// leaves its own child mcpserver process (and anything run_command's own
// `bash -c` spawned under it) running as an orphan, since neither inherits
// the cancelled ctx -- the same gap for a local run_command's own `bash -c`
// forking a background pipeline. Prepare puts cmd in its own process
// group and overrides Cancel to signal the whole group instead, so
// cancelling the ctx a command was built with kills everything that
// command's own tree ever forked, the same "kill -TERM -$pgid" shape a
// process supervisor uses for exactly this reason.
package procgroup

import "os/exec"

// Prepare puts cmd in its own process group and arms its Cancel so that
// ctx being cancelled (cmd must have been built with exec.CommandContext)
// kills that whole group, not just cmd.Process. Call it before cmd.Start.
func Prepare(cmd *exec.Cmd) {
	prepare(cmd)
}

// Kill signals cmd's whole process group, for a caller that ends a
// command itself rather than by cancelling a context -- pkg/console's
// sessions, which are ended by somebody closing them and so have no ctx
// to cancel. It is Prepare's Cancel by another name, and it is subject to
// the same requirement: the command must have been started in a process
// group of its own, whether by Prepare or by a Setsid of its own.
//
// A command that was never started, or that has already been reaped, is
// not an error -- both are "there is nothing left to kill", which is what
// a caller closing a session that has just exited is looking at.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	return kill(cmd.Process.Pid)
}

// KillPID signals the process group led by pid (falling back to pid itself),
// for a detached process whose PID was recorded on disk rather than held in an
// exec.Cmd.
func KillPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	return kill(pid)
}
