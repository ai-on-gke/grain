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

package protocol

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// MakeDumpable restores the calling process's dumpable attribute
// (PR_SET_DUMPABLE = 1). Exec'ing /usr/local/bin/grain, which carries the
// file capability cap_net_bind_service (Dockerfile), leaves the process
// non-dumpable: the kernel makes /proc/<pid>/fd root-owned 0500, so an
// unprivileged process of the same uid -- the daemon's port relay or grain
// hostd -- cannot read its fds to check that a grant port's socket belongs
// to it (§5.3.2).
func MakeDumpable() {
	_, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0)
}

// DropCapabilities clears every capability of a process that is not root,
// which can only have come from exec'ing the file-capability grain. A
// capability server binds no privileged port, and a process whose
// permitted set is not a subset of another same-uid process's is closed to
// it under ptrace's access check, dumpable or not: /proc/<pid>/fd/<n>
// would not readlink for the daemon's port relay or the installer's
// host-script probe, which could not then see that the server holds a
// grant's port.
func DropCapabilities() error {
	if os.Geteuid() == 0 {
		return nil
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	// capset sets the calling thread's capabilities alone, and the
	// thread ptrace checks is the leader, so every thread clears its own.
	// A cgo build cannot (ENOTSUP); the release image's grain is not one.
	_, _, errno := syscall.AllThreadsSyscall(syscall.SYS_CAPSET,
		uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0)
	if errno == syscall.ENOTSUP {
		return unix.Capset(&hdr, &data[0])
	}
	if errno != 0 {
		return errno
	}
	return nil
}
