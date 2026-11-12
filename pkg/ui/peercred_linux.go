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

package ui

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"syscall"
)

// ownedByThisAccount reports whether fi, a directory, is owned by the
// account this process runs as (restrictAPISocketDir).
func ownedByThisAccount(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// hasAccessACL reports whether dir carries an extended access ACL (one a
// setfacl -m left), whose mask its group mode bits then are
// (restrictAPISocketDir). A filesystem without ACLs has none.
func hasAccessACL(dir string) bool {
	n, err := syscall.Getxattr(dir, "system.posix_acl_access", nil)
	return err == nil && n > 0
}

// clearOwningGroupACL drops every permission dir's access ACL gives its
// owning group (the g:: entry), which the mask restrictAPISocketDir keeps
// at --x would otherwise pass: a directory that was 0750 when the
// operator ran setfacl -m u:<account>:x keeps g::r-x, and so let the
// whole group reach the socket the 0700 it goes to without an ACL keeps
// it from. Named entries (u:<account>, g:<group>) are the operator's
// grants and are left alone.
func clearOwningGroupACL(dir string) error {
	const attr = "system.posix_acl_access"
	b := make([]byte, 4096)
	n, err := syscall.Getxattr(dir, attr, b)
	if err != nil {
		return fmt.Errorf("reading the ACL of %s: %w", dir, err)
	}
	b = b[:n]
	if len(b) < 4 || (len(b)-4)%8 != 0 {
		return fmt.Errorf("the ACL of %s is not one this daemon can read (%d bytes)", dir, len(b))
	}
	changed := false
	for off := 4; off < len(b); off += 8 {
		// Each entry is tag u16, perm u16, id u32, little-endian;
		// ACL_GROUP_OBJ is tag 0x04.
		if binary.LittleEndian.Uint16(b[off:]) == 0x04 && binary.LittleEndian.Uint16(b[off+2:]) != 0 {
			binary.LittleEndian.PutUint16(b[off+2:], 0)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := syscall.Setxattr(dir, attr, b, 0); err != nil {
		return fmt.Errorf("restricting the owning group's ACL entry on %s: %w", dir, err)
	}
	return nil
}

// peerUID is the uid of the process at the other end of c, a unix socket
// connection, as the kernel recorded it when that process connected
// (SO_PEERCRED).
func peerUID(c net.Conn) (uint32, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("a %T is not a unix socket connection", c)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, fmt.Errorf("reading SO_PEERCRED: %w", credErr)
	}
	if cred == nil {
		return 0, errors.New("SO_PEERCRED named no peer")
	}
	return cred.Uid, nil
}
