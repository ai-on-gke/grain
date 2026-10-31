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

//go:build unix

package hostd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// checkOwnedBy refuses an allowlist that anyone but owners could have
// written: it must be a regular file (not a symlink to one), owned by one
// of owners, and writable by neither its group nor anyone else.
func checkOwnedBy(path string, owners []uint32) error {
	return checkFileOwnedBy("hostd allowlist", path, owners)
}

// checkFileOwnedBy is checkOwnedBy for any file, what naming it in the
// error.
func checkFileOwnedBy(what, path string, owners []uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %s is not a regular file", what, path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		// chmod go-w rather than a mode: what is checked is the allowlist
		// and hostd's own binary, and 0644 would leave the binary
		// unexecutable.
		return fmt.Errorf("%s %s is writable by group or others (mode %s); remove that with chmod go-w %s", what, path, info.Mode().Perm(), path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s %s: cannot read its owner", what, path)
	}
	if !slices.Contains(owners, st.Uid) {
		return fmt.Errorf("%s %s is owned by uid %d; only %v may own it", what, path, st.Uid, owners)
	}
	return nil
}

// socketDirRemoved reports whether the directory socket sits in has been
// removed while this process still has it: a bind mount of a directory
// deleted on the host stays, with no links, in the mount namespace that
// mounted it.
func socketDirRemoved(socket string) bool {
	info, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 0
}
