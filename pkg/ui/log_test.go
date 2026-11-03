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

// Where this package's own log output goes while its tests run.
//
// The same routing pkg/upgrade, pkg/orchestrator and pkg/staterepo have,
// and for the same reason: what this package says through log.Printf --
// and what the things it drives say, an upgrade started from the API
// narrating every one of its steps -- lands in a test binary on stderr
// attached to no test at all. `go test` hands that back interleaved with
// its own report, so a `--- FAIL:` line ends up separated from the run
// that produced it by whatever every *later* test went on to log. See
// pkg/upgrade/log_test.go for the red check that made the case.
//
// So the whole binary's log is routed at whichever tests are running,
// through t.Log. `go test` holds those lines and prints them under the
// name of the test that produced them, and only for a test that fails.
//
// This file is in package ui rather than ui_test because log.SetOutput
// is process-wide and one router has to serve both halves of this
// package's tests. LogToTest is the external half's way in, and that is
// where nearly every test here lives.

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
)

// testLog is that route. One package-level value, because log.SetOutput
// is package-level too, and every test in this package runs in parallel
// -- so several of them hold it at once and it has to be a register of
// claims rather than a single current one.
var testLog testJournal

// TestMain points the log at testLog for the whole binary. Nothing puts
// it back: os.Stderr is where testLog sends anything logged with no test
// to attribute it to, which is the same place those lines went before.
func TestMain(m *testing.M) {
	serveCapabilityIfForked()
	log.SetOutput(&testLog)
	// And no date and time in front of each line: `go test` stamps
	// nothing else it prints, t.Log adds the file and line of the write,
	// and the wall clock of a log line is not what someone reading a
	// failed test is short of.
	log.SetFlags(0)
	os.Exit(m.Run())
}

// testJournal is the io.Writer behind log.Printf while these tests run:
// it hands each line to the tests that have claimed it, and keeps the
// lines so that a test asserting on what was said can read them back.
//
// "Tests", plural, is the whole difference from the single-claimant
// version this had while nothing here ran in parallel. log.Printf
// carries no hint of which test's goroutine reached it, so once several
// tests are in flight at once a line genuinely cannot be attributed to
// one of them: there is nothing in a process-wide logger to attribute it
// with. What is still worth having is that a failing test has the line
// *near* it, so a line logged while n tests hold the log is given to all
// n of them, marked as possibly belonging to a neighbour. The
// alternative -- back to stderr the moment a second test starts, which
// is every line in a parallel package -- is the detached, interleaved
// output this file exists to stop.
type testJournal struct {
	mu sync.Mutex
	// claims is the tests currently holding the log, in claim order, or
	// empty between tests. It is read and written under mu, which is what
	// keeps a line logged from an upgrade's own goroutine from reaching a
	// t whose test has already finished -- t.Log after that panics, and a
	// released claim is off this list before the cleanup that released it
	// returns.
	claims []*testClaim
}

// testClaim is one test's hold on the log, and the lines written while it
// held it. A test that asserts on what was said reads them back through
// String.
type testClaim struct {
	journal *testJournal
	t       *testing.T
	sb      strings.Builder // guarded by journal.mu
}

func (j *testJournal) Write(b []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.claims) == 0 {
		// Logged from outside any test: between two of them, or from a
		// goroutine still unwinding after the test that started it
		// returned. Said plainly rather than silently, because a line
		// here is a line the next failure will not have with it.
		return os.Stderr.Write(append([]byte("[ui log with no test to attach it to] "), b...))
	}
	line := strings.TrimRight(string(b), "\n")
	for _, c := range j.claims {
		c.sb.Write(b)
		if len(j.claims) == 1 {
			c.t.Log(line)
			continue
		}
		// Any one of these tests could have logged it and the logger
		// does not say which, so each is told as much rather than being
		// handed a line as if it were certainly its own.
		c.t.Log(fmt.Sprintf("[ui log, from one of the %d tests running in parallel] %s", len(j.claims), line))
	}
	return len(b), nil
}

// String is everything logged since the claiming test claimed the log,
// for a test that asserts on what was said rather than only needing it
// attributed. While other tests hold the log too it collects their lines
// as well, for the reason Write's own comment gives -- so an assertion
// that a line is *absent* is not one this can support.
func (c *testClaim) String() string {
	c.journal.mu.Lock()
	defer c.journal.mu.Unlock()
	return c.sb.String()
}

// claim is t's hold on the log, made if it does not already have one --
// the second return says which, so that only the call that made it
// registers the release.
func (j *testJournal) claim(t *testing.T) (*testClaim, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range j.claims {
		if c.t == t {
			return c, false
		}
	}
	c := &testClaim{journal: j, t: t}
	j.claims = append(j.claims, c)
	return c, true
}

func (j *testJournal) release(c *testClaim) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i, held := range j.claims {
		if held == c {
			j.claims = append(j.claims[:i], j.claims[i+1:]...)
			return
		}
	}
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
// claim again, so a test that wants to read the lines can ask for them
// whether or not a helper it called already claimed them.
func logToTest(t *testing.T) *testClaim {
	t.Helper()
	c, fresh := testLog.claim(t)
	if fresh {
		t.Cleanup(func() { testLog.release(c) })
	}
	return c
}

// LogToTest is logToTest for ui_test, whose testClient claims the log
// for nearly every test in this package. Nothing there reads the lines
// back, so it hands nothing back.
func LogToTest(t *testing.T) {
	t.Helper()
	logToTest(t)
}

func LogBufferForTest(t *testing.T) interface{ String() string } {
	t.Helper()
	return logToTest(t)
}
