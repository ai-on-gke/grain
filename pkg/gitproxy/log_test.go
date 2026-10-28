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
	"log"
	"os"
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
	os.Exit(m.Run())
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
