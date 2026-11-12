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

package ui_test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// grantSearchACL sets on dir the access ACL setfacl -m u:<uid>:x leaves on
// a 0700 directory -- u::rwx, u:<uid>:--x, g::---, m::--x, o::--- -- in
// the kernel's posix_acl_xattr encoding, which also sets the group mode
// bits to the mask. It skips the test on a filesystem without ACLs.
func grantSearchACL(t *testing.T, dir string, uid uint32) {
	t.Helper()
	const undefined = 0xffffffff
	entries := []struct {
		tag, perm uint16
		id        uint32
	}{
		{0x01, 7, undefined}, // ACL_USER_OBJ
		{0x02, 1, uid},       // ACL_USER
		{0x04, 0, undefined}, // ACL_GROUP_OBJ
		{0x10, 1, undefined}, // ACL_MASK
		{0x20, 0, undefined}, // ACL_OTHER
	}
	b := binary.LittleEndian.AppendUint32(nil, 2) // POSIX_ACL_XATTR_VERSION
	for _, e := range entries {
		b = binary.LittleEndian.AppendUint16(b, e.tag)
		b = binary.LittleEndian.AppendUint16(b, e.perm)
		b = binary.LittleEndian.AppendUint32(b, e.id)
	}
	if err := syscall.Setxattr(dir, "system.posix_acl_access", b, 0); err != nil {
		if errors.Is(err, syscall.ENOTSUP) {
			t.Skipf("%s has no ACL support: %v", dir, err)
		}
		t.Fatalf("setting an ACL on %s: %v", dir, err)
	}
}

// A restart must not undo the operator's setfacl -m u:<account>:x on the
// socket's directory: chmod 0700 zeroes the ACL mask, which lives in the
// group bits (docs/test-runs/2026-11-11-caller-identity.md, F5).
func TestAPISocketKeepsAnACLGrantAcrossARestart(t *testing.T) {
	dir, err := os.MkdirTemp("", "gsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := ui.APISocketPath(dir)
	ln, err := ui.ListenAPISocket(path)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	sockDir := filepath.Dir(path)
	grantSearchACL(t, sockDir, uint32(os.Getuid()))
	if fi, err := os.Stat(sockDir); err != nil || fi.Mode().Perm() != 0o710 {
		t.Fatalf("after the ACL the socket directory is %v (%v), want 0710", fi.Mode(), err)
	}
	ln, err = ui.ListenAPISocket(path)
	if err != nil {
		t.Fatalf("restarting on a directory with granted search: %v", err)
	}
	ln.Close()
	if fi, err := os.Stat(sockDir); err != nil || fi.Mode().Perm() != 0o710 {
		t.Errorf("after a restart the socket directory is %v (%v), want 0710 kept so the granted search survives", fi.Mode(), err)
	}
}
