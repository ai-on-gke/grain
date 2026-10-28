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

package granule

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSandboxAccessSpecFilesStayPrivate pins what keeps a spec's bearer
// token to this account: the file is 0600 in a 0700 directory, a
// directory left wider is tightened, and a spec another account could
// have written (a wide file, a symlink, a writable directory) is refused
// rather than trusted.
func TestSandboxAccessSpecFilesStayPrivate(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "access")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pod.json")
	if err := WriteSandboxAccessSpec(path, SandboxAccessSpec{PodName: "pod", Token: "secret"}); err != nil {
		t.Fatalf("WriteSandboxAccessSpec: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v (%v), want 0700", fi.Mode(), err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v (%v), want 0600", fi.Mode(), err)
	}
	spec, err := ReadSandboxAccessSpec(path)
	if err != nil || spec.Token != "secret" {
		t.Fatalf("ReadSandboxAccessSpec = %+v, %v", spec, err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSandboxAccessSpec(path); err == nil {
		t.Error("ReadSandboxAccessSpec accepted a 0644 spec")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSandboxAccessSpec(link); err == nil {
		t.Error("ReadSandboxAccessSpec followed a symlink")
	}

	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSandboxAccessSpec(path); err == nil {
		t.Error("ReadSandboxAccessSpec accepted a spec in a world-writable directory")
	}
}

// TestSandboxAccessSpecRefusesAnotherAccountsFiles pins the ownership
// half of the check: a daemon running as root passes every mode check
// and Chmod, so a 0700 directory or 0600 spec another account planted
// (the default directory is under the shared os.TempDir) must still be
// refused rather than handing the controller someone else's address and
// CA.
func TestSandboxAccessSpecRefusesAnotherAccountsFiles(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to chown a file to another account")
	}
	const nobody = 65534
	dir := filepath.Join(t.TempDir(), "access")
	path := filepath.Join(dir, "pod.json")
	if err := WriteSandboxAccessSpec(path, SandboxAccessSpec{PodName: "pod", Token: "secret"}); err != nil {
		t.Fatalf("WriteSandboxAccessSpec: %v", err)
	}

	if err := os.Lchown(path, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSandboxAccessSpec(path); err == nil {
		t.Error("ReadSandboxAccessSpec accepted a spec another account owns")
	}
	if err := os.Lchown(path, 0, 0); err != nil {
		t.Fatal(err)
	}

	if err := os.Lchown(dir, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSandboxAccessSpec(path); err == nil {
		t.Error("ReadSandboxAccessSpec accepted a spec in a directory another account owns")
	}
	if err := WriteSandboxAccessSpec(path, SandboxAccessSpec{PodName: "pod"}); err == nil {
		t.Error("WriteSandboxAccessSpec wrote into a directory another account owns")
	}
}
