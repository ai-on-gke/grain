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

package gitproxy

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// heldValue is a stored func for RetireMigratedCopy that holds value.
func heldValue(value string) func() ([]byte, bool, error) {
	return func() ([]byte, bool, error) { return []byte(value), true, nil }
}

// retireWithin runs RetireMigratedCopy and fails the test if it has not
// returned within a few seconds, as a start that blocks on a copy would.
func retireWithin(t *testing.T, copyPath string, stored func() ([]byte, bool, error)) (bool, error) {
	t.Helper()
	type result struct {
		gone bool
		err  error
	}
	done := make(chan result, 1)
	go func() {
		gone, err := RetireMigratedCopy(copyPath, "github-credential-bot/token", stored, nil)
		done <- result{gone, err}
	}()
	select {
	case r := <-done:
		return r.gone, r.err
	case <-time.After(5 * time.Second):
		t.Fatalf("RetireMigratedCopy(%s) had not returned after 5s", copyPath)
		return false, nil
	}
}

func TestARollbackCopyThatIsASymlinkIsNeitherReadThroughNorDeleted(t *testing.T) {
	dir, _ := storeBackedDir(t)
	// The link points outside the data directory at a file that holds
	// exactly the store's value, so reading through it would "verify" it.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, "ghp_bot\n")
	copyPath := filepath.Join(dir, "bot.token"+MigratedSuffix)
	if err := os.Symlink(outside, copyPath); err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	gone, err := retireWithin(t, copyPath, heldValue("ghp_bot\n"))
	if gone || err == nil {
		t.Errorf("RetireMigratedCopy on a symlink = %v, %v; want it kept with an error", gone, err)
	}
	if _, lerr := os.Lstat(copyPath); lerr != nil {
		t.Errorf("the symlink was removed: %v", lerr)
	}
	if b, rerr := os.ReadFile(outside); rerr != nil || string(b) != "ghp_bot\n" {
		t.Errorf("the symlink's target changed: %q, %v", b, rerr)
	}
	out := logs.String()
	if strings.Contains(out, "holds the same value") {
		t.Errorf("the copy was compared through its symlink:\n%s", out)
	}
	if !strings.Contains(out, "not a regular file") {
		t.Errorf("keeping the symlink was not logged:\n%s", out)
	}
}

func TestARollbackCopyThatIsAFIFODoesNotBlockTheStart(t *testing.T) {
	dir, _ := storeBackedDir(t)
	copyPath := filepath.Join(dir, "bot.token"+MigratedSuffix)
	if err := syscall.Mkfifo(copyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	captureLog(t)
	if gone, err := retireWithin(t, copyPath, heldValue("ghp_bot\n")); gone || err == nil {
		t.Errorf("RetireMigratedCopy on a FIFO = %v, %v; want it kept with an error", gone, err)
	}
}

func TestARollbackCopyThatIsASymlinkToADeviceIsNotRead(t *testing.T) {
	dir, _ := storeBackedDir(t)
	copyPath := filepath.Join(dir, "bot.token"+MigratedSuffix)
	if err := os.Symlink("/dev/zero", copyPath); err != nil {
		t.Fatal(err)
	}
	captureLog(t)
	if gone, err := retireWithin(t, copyPath, heldValue("ghp_bot\n")); gone || err == nil {
		t.Errorf("RetireMigratedCopy on a link to /dev/zero = %v, %v; want it kept with an error", gone, err)
	}
}

func TestAWorldReadableRollbackCopyIsStillDeleted(t *testing.T) {
	dir, _ := storeBackedDir(t)
	copyPath := filepath.Join(dir, "bot.token"+MigratedSuffix)
	writeFile(t, copyPath, "ghp_bot\n")
	if err := os.Chmod(copyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	captureLog(t)
	if gone, err := retireWithin(t, copyPath, heldValue("ghp_bot\n")); !gone || err != nil {
		t.Errorf("RetireMigratedCopy on a 0644 copy = %v, %v; want it deleted", gone, err)
	}
}

func TestARollbackCopyThatCannotBeDeletedIsLoggedAndKept(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root deletes from a read-only directory")
	}
	dir, _ := storeBackedDir(t)
	copyPath := filepath.Join(dir, "bot.token"+MigratedSuffix)
	writeFile(t, copyPath, "ghp_bot\n")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	logs := captureLog(t)
	if gone, err := retireWithin(t, copyPath, heldValue("ghp_bot\n")); gone || err == nil {
		t.Errorf("RetireMigratedCopy in a read-only directory = %v, %v; want it kept with an error", gone, err)
	}
	if !exists(copyPath) {
		t.Error("the copy is gone from a read-only directory")
	}
	if !strings.Contains(logs.String(), "deleting the rollback copy "+copyPath) {
		t.Errorf("the failed deletion was not logged:\n%s", logs.String())
	}
}
