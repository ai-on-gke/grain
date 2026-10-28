package mcp

import "syscall"

// syscallAlive reports nil while pid is a process this test can signal.
// Signal 0 checks for existence without delivering anything.
func syscallAlive(pid int) error { return syscall.Kill(pid, 0) }
