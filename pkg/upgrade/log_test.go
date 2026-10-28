package upgrade

// Where this package's own log output goes while its tests run.
//
// pkg/upgrade says everything it has to say about an upgrade through
// log.Printf: progress.logf for each transition, and one line for every
// line the commands an upgrade runs print (output.go's lineLogger). In a
// test binary that lands on stderr attached to nothing, and `go test`
// passes it through as it arrives, interleaved with its own report --
// so a failure arrives as a `--- FAIL:` line followed by however many
// hundred lines every *later* test in the package went on to log, and
// the two are not connected by anything a reader can follow.
//
// That is not hypothetical. The `go` check failed once on this
// package (grain/task-257, commit 2dff17e) and passed on the commits
// either side of it with identical code in here. What CI kept of that
// run was a tail of upgrade log lines belonging to no test in
// particular, with the `--- FAIL:` line already above the point the log
// was cut off -- so the one fact needed to fix it, which test had
// failed, was not in the evidence at all.
//
// So the whole package's log is routed at whichever test is running,
// through t.Log. `go test` holds those lines and prints them under the
// name of the test that produced them, and only for a test that fails:
// a run that goes green says nothing at all, and a run that does not
// says which test, with that test's own upgrade log under it and
// nobody else's.

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
)

// testLog is that route. One package-level value, because log.SetOutput
// is package-level too -- no test here runs in parallel, so at most one
// of them owns it at a time.
var testLog testJournal

// TestMain points the log at testLog for the whole binary. Nothing puts
// it back: os.Stderr is where testLog sends anything logged with no test
// to attribute it to, which is the same place those lines went before.
//
// It also has the last word, which is the other half of keeping a
// failure here readable -- see reportFailures.
func TestMain(m *testing.M) {
	log.SetOutput(&testLog)
	// And no date and time in front of each line: `go test` stamps
	// nothing else it prints, t.Log adds the file and line of the write,
	// and the wall clock of a log line is not what someone reading a
	// failed test is short of.
	log.SetFlags(0)
	code := m.Run()
	testLog.reportFailures(os.Stderr)
	os.Exit(code)
}

// testJournal is the io.Writer behind log.Printf while these tests run:
// it hands each line to the test that claimed it, and keeps the lines so
// that a test asserting on what an upgrade said can read them back.
type testJournal struct {
	mu sync.Mutex
	// t is the test the log currently belongs to, or nil between tests.
	// It is read and written under mu, which is what keeps a log line
	// arriving from an upgrade's own goroutine from reaching a t whose
	// test has already finished -- t.Log after that panics.
	t  *testing.T
	sb strings.Builder
	// failed is the name of every test that claimed the log and then
	// failed, in the order they ran -- reportFailures' whole content.
	failed []string
}

func (j *testJournal) Write(b []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.t == nil {
		// Logged from outside any test: between two of them, or from a
		// goroutine still unwinding after the test that started it
		// returned. Said plainly rather than silently, because a line
		// here is a line the next failure will not have with it.
		return os.Stderr.Write(append([]byte("[upgrade log with no test to attach it to] "), b...))
	}
	j.sb.Write(b)
	j.t.Log(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

// String is everything logged since the current test claimed the log --
// for the one test here that asserts on what an upgrade said rather than
// only needing it attributed (hold_test.go's refused hold).
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

// release gives the log back at the end of t, and notes t on the way out
// if it failed.
func (j *testJournal) release(t *testing.T) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.t = nil
	// t.Failed() is complete by now for the same cleanup-ordering reason
	// logToTest's own comment gives: this cleanup is registered first and
	// so runs last, after newUpgrader's wait for the upgrade to finish
	// has had its chance to report one.
	if t.Failed() {
		j.failed = append(j.failed, t.Name())
	}
}

// reportFailures writes the names of the tests that failed, as the last
// thing this binary prints.
//
// It is for a reader who has a *tail* of a CI log and not the log, which
// is what a failure here has twice now come back as. `go test` keeps
// each package's output together and closes it with one FAIL line naming
// the package, so the package is the part that survives being cut; the
// `--- FAIL: TestX` line does not, because everything that test logged
// (this file's whole reason to exist) is printed underneath it and pushes
// it out of view, and the "ok" lines of every package that finishes after
// this one push it further. A line here lands immediately before
// `FAIL github.com/bwsalmon/grain/pkg/upgrade`: if a reader can see that
// this package is what failed, they can see which test did.
//
// Only tests that claimed the log are in it -- every test that runs an
// upgrade, via newUpgrader, and those are exactly the ones whose own
// output buries their `--- FAIL:` line. A test that logs nothing needs
// no help being found.
func (j *testJournal) reportFailures(w io.Writer) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.failed) == 0 {
		return
	}
	fmt.Fprintf(w, "pkg/upgrade: the test(s) that failed, in the order they ran: %s\n",
		strings.Join(j.failed, ", "))
}

// logToTest routes this package's log at t for the rest of the test, and
// hands back what it collects.
//
// Called by newUpgrader, so every test that actually runs an upgrade is
// covered without saying so; a test that logs without one (it built a
// progress itself, or its Start was refused before it got going) calls
// this itself. Calling it twice for the same test is a no-op beyond
// returning the journal again, so a test that wants the lines can ask
// for them whether or not newUpgrader already asked.
//
// The cleanup it registers is what gives the log back, and it has to run
// *after* the wait for the upgrade to finish that newUpgrader registers
// -- cleanups run last-registered-first, so this call has to come first,
// which is why newUpgrader opens with it.
func logToTest(t *testing.T) *testJournal {
	t.Helper()
	if testLog.claim(t) {
		t.Cleanup(func() { testLog.release(t) })
	}
	return &testLog
}
