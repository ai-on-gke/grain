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
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// EnsureBuiltins adds to the allowlist at path every entry of builtins it
// has no capabilities entry for (PlanEnsure), and nothing else. It is run
// as root on the host, never by the daemon: by grain-hostd.service before
// each start (so a hostd on a new binary brings the entries its build
// registers with it), by the upgrade step that drains hostd onto a new
// binary, and by scripts/setup.sh. hostd re-reads the file on every
// request, so an added entry is offered at once and no running process is
// touched.
//
// It never follows a symlink, and it replaces the file atomically -- a
// neighbouring temporary file, renamed over it -- with the file's own
// owner and mode, so the root-owned file hostd insists on stays one.
func EnsureBuiltins(path, command string, builtins []BuiltinEntry) (EnsureResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return EnsureResult{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return EnsureResult{}, fmt.Errorf("hostd allowlist %s %w", path, errSymlink)
	}
	if !info.Mode().IsRegular() {
		return EnsureResult{}, fmt.Errorf("hostd allowlist %s is not a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return EnsureResult{}, err
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return EnsureResult{}, err
	}
	if !os.SameFile(info, opened) {
		f.Close()
		return EnsureResult{}, fmt.Errorf("hostd allowlist %s changed while it was read; try again", path)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return EnsureResult{}, err
	}
	out, res, err := PlanEnsure(data, command, builtins)
	if err != nil || len(res.Added) == 0 {
		return res, err
	}
	st, ok := opened.Sys().(*syscall.Stat_t)
	if !ok {
		return EnsureResult{}, fmt.Errorf("hostd allowlist %s: cannot read its owner", path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return EnsureResult{}, err
	}
	done := false
	defer func() {
		if !done {
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return EnsureResult{}, err
	}
	if err := tmp.Chown(int(st.Uid), int(st.Gid)); err != nil {
		tmp.Close()
		return EnsureResult{}, err
	}
	if err := tmp.Chmod(opened.Mode().Perm()); err != nil {
		tmp.Close()
		return EnsureResult{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return EnsureResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return EnsureResult{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return EnsureResult{}, err
	}
	done = true
	return res, nil
}
