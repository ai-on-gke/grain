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

package gitproxy

// Where this package's own log output goes while its tests run.
//
// The same routing pkg/upgrade, pkg/orchestrator and pkg/staterepo have,
// and for the same reason: what this package says through log.Printf --
// an installation token it could not mint, a credentials file it could
// not re-read, a repo pattern spelled twice -- lands in a test binary on
// stderr attached to no test at all. `go test` hands that back
// interleaved with its own report, so a `--- FAIL:` line ends up
// separated from the run that produced it by whatever every *later* test
// went on to log. See pkg/upgrade/log_test.go for the red check that
// made the case.
//
// So the whole binary's log is routed at whichever test is running,
// through t.Log. `go test` holds those lines and prints them under the
// name of the test that produced them, and only for a test that fails.
//
// Nearly every test here is in this package rather than gitproxy_test;
// LogToTest is the live tests' way in.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// testLog is that route. One package-level value, because log.SetOutput
// is package-level too -- no test in either half of this package runs in
// parallel, so at most one of them owns it at a time.
var testLog testJournal

// TestMain points the log at testLog for the whole binary. Nothing puts
// it back: os.Stderr is where testLog sends anything logged with no test
// to attribute it to, which is the same place those lines went before.
//
// It also snapshots the real $HOME/.git-credentials before running the
// suite and fails if any test modified it (for example, by running `git`
// against a `http://user:tok@127.0.0.1:...` URL without pointing HOME at
// t.TempDir(), which lets the sandbox's `credential.helper = store`
// append the test's loopback token to the real ~/.git-credentials).
func TestMain(m *testing.M) {
	if fakeDepotGGHAsked() {
		// This binary run as commandforge_depot_test.go's fake `forgecli`.
		os.Exit(fakeDepotGGHMain())
	}
	if hostGitServerAsked() {
		// This binary run by grain hostd as hostgit_test.go's host-git
		// capability server.
		os.Exit(hostGitServerMain())
	}
	log.SetOutput(&testLog)
	// And no date and time in front of each line: `go test` stamps
	// nothing else it prints, t.Log adds the file and line of the write,
	// and the wall clock of a log line is not what someone reading a
	// failed test is short of.
	log.SetFlags(0)

	credsSnap, err := snapshotGitCredentials(os.Getenv("HOME"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "snapshot ~/.git-credentials: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := credsSnap.verifyAndRestore(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

type gitCredentialsSnapshot struct {
	path    string
	content []byte
	existed bool
}

func snapshotGitCredentials(home string) (gitCredentialsSnapshot, error) {
	if home == "" {
		return gitCredentialsSnapshot{}, nil
	}
	path := filepath.Join(home, ".git-credentials")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return gitCredentialsSnapshot{path: path, existed: false}, nil
	}
	if err != nil {
		return gitCredentialsSnapshot{}, err
	}
	return gitCredentialsSnapshot{path: path, content: data, existed: true}, nil
}

func (s gitCredentialsSnapshot) verifyAndRestore() error {
	if s.path == "" {
		return nil
	}
	after, err := os.ReadFile(s.path)
	afterExisted := true
	if os.IsNotExist(err) {
		afterExisted = false
		err = nil
	}
	if err != nil {
		return fmt.Errorf("reading %s after tests: %w", s.path, err)
	}
	if s.existed == afterExisted && bytes.Equal(s.content, after) {
		return nil
	}
	if s.existed {
		_ = os.WriteFile(s.path, s.content, 0o600)
	} else {
		_ = os.Remove(s.path)
	}
	return fmt.Errorf("%s changed during pkg/gitproxy tests (before: %d bytes, after: %d bytes; a test ran git against a credentialed URL without t.Setenv(\"HOME\", t.TempDir()))",
		s.path, len(s.content), len(after))
}

func TestGitCredentialsSnapshotDetectsAndRestoresChanges(t *testing.T) {
	t.Run("existing file modified", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".git-credentials")
		original := []byte("http://sandbox:tok@127.0.0.1%3a9728\n")
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		snap, err := snapshotGitCredentials(home)
		if err != nil {
			t.Fatal(err)
		}
		if err := snap.verifyAndRestore(); err != nil {
			t.Fatalf("unchanged file failed verification: %v", err)
		}

		polluted := append([]byte("http://x:tok0@127.0.0.1%3a40117\n"), original...)
		if err := os.WriteFile(path, polluted, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := snap.verifyAndRestore(); err == nil {
			t.Fatal("expected verifyAndRestore to fail when .git-credentials was modified")
		}
		restored, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(restored, original) {
			t.Fatalf("restored .git-credentials = %q, want %q", restored, original)
		}
	})

	t.Run("absent file created", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".git-credentials")
		snap, err := snapshotGitCredentials(home)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("http://x:tok0@127.0.0.1%3a40117\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := snap.verifyAndRestore(); err == nil {
			t.Fatal("expected verifyAndRestore to fail when .git-credentials was created")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed on restore, got err=%v", path, err)
		}
	})
}

// testJournal is the io.Writer behind log.Printf while these tests run:
// it hands each line to the test that claimed it, and keeps the lines so
// that a test asserting on what the proxy said can read them back.
type testJournal struct {
	mu sync.Mutex
	// t is the test the log currently belongs to, or nil between tests.
	// It is read and written under mu, which is what keeps a line logged
	// from one of the proxy's own goroutines from reaching a t whose test
	// has already finished -- t.Log after that panics.
	t  *testing.T
	sb strings.Builder
}

func (j *testJournal) Write(b []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.t == nil {
		// Logged from outside any test: between two of them, or from a
		// goroutine still unwinding after the test that started it
		// returned. Said plainly rather than silently, because a line
		// here is a line the next failure will not have with it.
		return os.Stderr.Write(append([]byte("[gitproxy log with no test to attach it to] "), b...))
	}
	j.sb.Write(b)
	j.t.Log(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

// String is everything logged since the current test claimed the log,
// for a test that asserts on what the proxy said rather than only
// needing it attributed.
func (j *testJournal) String() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.sb.String()
}

func (j *testJournal) claim(t *testing.T) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.t == t {
		return false
	}
	j.t = t
	j.sb.Reset()
	return true
}

func (j *testJournal) release() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.t = nil
}

// logToTest routes this package's log at t for the rest of the test, and
// hands back what it collects.
//
// Call it before anything that registers a cleanup of its own which
// waits for a goroutine to finish: cleanups run last-registered-first,
// so the release registered here has to be the last one to run, or a
// line logged while that goroutine winds down arrives at a test that has
// already completed. Claiming first is what puts it last.
//
// Calling it twice for the same test is a no-op beyond returning the
// journal again, so a test that wants to read the lines can ask for them
// whether or not a helper it called already claimed them.
func logToTest(t *testing.T) *testJournal {
	t.Helper()
	if testLog.claim(t) {
		t.Cleanup(testLog.release)
	}
	return &testLog
}

// LogToTest is logToTest for gitproxy_test, where the live tests live.
// Nothing there reads the lines back, so it hands nothing back.
func LogToTest(t *testing.T) {
	t.Helper()
	logToTest(t)
}
