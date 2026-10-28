package forge

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
)

var readStart = time.Date(2026, 9, 17, 4, 11, 0, 0, time.UTC)

func githubError(status int, body string) error {
	return &github.Error{Status: status, Body: []byte(body)}
}

// The five failures below are, verbatim, the shapes this deployment's
// daemon log carried over 16-17 September 2026 -- every one of which put
// "grain could not read this pull request" on somebody's row, and every
// one of which was answered fine ten seconds later.
func TestTheFailuresGitHubActuallyFlakesWithReadAsTransient(t *testing.T) {
	for _, flake := range []struct {
		name string
		err  error
	}{
		{"502 with a message", githubError(502, `{"message":"Server Error"}`)},
		{"500 with no body at all", githubError(500, "")},
		{"502 as GitHub's HTML error page", githubError(502, "<html><body>error</body></html>")},
		{"504", githubError(504, `{"message":"We couldn't respond to your request in time."}`)},
		{"503 from the proxy in front", githubError(503, "upstream connect error ... connection refused")},
		{"a call that never reached GitHub", errors.New("dial tcp: connection refused")},
		{"a wrapped 500", fmt.Errorf("forge: reading acme/widgets#1: %w", githubError(500, ""))},
		{"this deployment's own throttle", &github.RateLimitError{Until: readStart, Reason: "secondary rate limit"}},
	} {
		if !TransientRead(flake.err) {
			t.Errorf("TransientRead(%s) = false, want true: %v", flake.name, flake.err)
		}
	}
}

// The other half of the split, and the reason the state exists at all: a
// credential that has lost its access, or a pull request somebody
// deleted, is a fault a person has to act on and must not be described as
// GitHub having a bad minute.
func TestARefusalGitHubMeantReadsAsTerminal(t *testing.T) {
	for _, refusal := range []struct {
		name string
		err  error
	}{
		{"404 on a repository the credential cannot see", githubError(404, `{"message":"Not Found"}`)},
		{"403 on a permission it does not hold", githubError(403, `{"message":"Resource not accessible by integration"}`)},
		{"401 on a token that was revoked", githubError(401, `{"message":"Bad credentials"}`)},
		{"422", githubError(422, `{"message":"Validation Failed"}`)},
	} {
		if TransientRead(refusal.err) {
			t.Errorf("TransientRead(%s) = true, want false: %v", refusal.name, refusal.err)
		}
	}
	if TransientRead(nil) {
		t.Error("TransientRead(nil) = true: no failure is not a transient failure")
	}
}

// One failed read is a flake and says nothing, however long the clock
// shows: the count and the duration are both required, so that a
// deployment whose reconcile interval is longer than the deadline does
// not start reporting every single 502 as a fault.
func TestOneFailedReadIsNeverPersistent(t *testing.T) {
	ref := Ref{Owner: "acme", Repo: "flaky", Number: 1}
	t.Cleanup(func() { ForgetReadFailures(ref) })

	first := RecordReadFailure(ref, githubError(502, `{"message":"Server Error"}`), readStart)
	if first.Count != 1 {
		t.Fatalf("Count = %d, want 1", first.Count)
	}
	if first.Persistent(readStart.Add(time.Hour)) {
		t.Fatal("a single failed read an hour old reads as a persistent fault")
	}

	second := RecordReadFailure(ref, githubError(502, `{"message":"Server Error"}`), readStart.Add(10*time.Second))
	if second.Count != 2 || !second.First.Equal(readStart) {
		t.Fatalf("second = %+v, want the run of two counted from the first", second)
	}
	if second.Persistent(readStart.Add(10 * time.Second)) {
		t.Fatal("ten seconds of failed reads reads as a persistent fault")
	}
	if !second.Persistent(readStart.Add(3 * time.Minute)) {
		t.Fatal("three minutes of failed reads does not read as a persistent fault")
	}
}

// A read that answered ends the run, which is what makes the count a run
// of consecutive failures rather than a tally: two flakes a day apart are
// two runs of one, and neither is a fault.
func TestASuccessfulReadEndsTheRun(t *testing.T) {
	ref := Ref{Owner: "acme", Repo: "intermittent", Number: 7}
	t.Cleanup(func() { ForgetReadFailures(ref) })

	RecordReadFailure(ref, githubError(500, ""), readStart)
	RecordReadFailure(ref, githubError(500, ""), readStart.Add(10*time.Second))
	ForgetReadFailures(ref)
	if f, ok := LastReadFailure(ref); ok {
		t.Fatalf("LastReadFailure = %+v after a read that answered, want no record", f)
	}

	later := RecordReadFailure(ref, githubError(500, ""), readStart.Add(6*time.Hour))
	if later.Count != 1 || !later.First.Equal(readStart.Add(6*time.Hour)) {
		t.Fatalf("later = %+v, want a fresh run of one timed from itself", later)
	}
	if later.Persistent(readStart.Add(7 * time.Hour)) {
		t.Fatal("a flake six hours after two others reads as one unbroken fault")
	}
}

// The run reports the latest failure rather than the first, because a
// fault can change its mind: GitHub 5xx while a repository is being
// renamed, then a 404 once it has been. What a person looking now needs
// is what is wrong now.
func TestTheRunCarriesTheLatestFailure(t *testing.T) {
	ref := Ref{Owner: "acme", Repo: "renamed", Number: 3}
	t.Cleanup(func() { ForgetReadFailures(ref) })

	RecordReadFailure(ref, githubError(502, `{"message":"Server Error"}`), readStart)
	after := RecordReadFailure(ref, githubError(404, `{"message":"Not Found"}`), readStart.Add(time.Minute))
	if after.Transient {
		t.Errorf("run = %+v, want the 404 it is failing with now, not the 502 it started with", after)
	}
	if after.Count != 2 || !after.First.Equal(readStart) {
		t.Errorf("run = %+v, want the clock still timed from the first failure", after)
	}
}

// Nothing may be reported under a deadline the record was not kept under
// -- the property MergeRefusal.Deadline exists for, kept here for the
// same reason.
func TestSetUnreadableDeadlineClearsWhatWasKeptUnderTheOldOne(t *testing.T) {
	ref := Ref{Owner: "acme", Repo: "rewound", Number: 9}
	t.Cleanup(func() { ForgetReadFailures(ref) })

	RecordReadFailure(ref, githubError(500, ""), readStart)
	restore := SetUnreadableDeadline(time.Second)
	if f, ok := LastReadFailure(ref); ok {
		t.Fatalf("LastReadFailure = %+v, want the old window's records dropped", f)
	}
	f := RecordReadFailure(ref, githubError(500, ""), readStart)
	if f.Deadline != time.Second {
		t.Fatalf("Deadline = %s, want the window in force", f.Deadline)
	}
	restore()
	if f := RecordReadFailure(ref, githubError(500, ""), readStart); f.Deadline != defaultUnreadableDeadline {
		t.Fatalf("Deadline = %s after restoring, want %s", f.Deadline, defaultUnreadableDeadline)
	}
}

// A clock that jumped backwards reads as "not persistent yet", the
// direction everything timed in this package errs in.
func TestAClockThatWentBackwardsReportsNothing(t *testing.T) {
	ref := Ref{Owner: "acme", Repo: "backwards", Number: 2}
	t.Cleanup(func() { ForgetReadFailures(ref) })

	RecordReadFailure(ref, githubError(500, ""), readStart)
	f := RecordReadFailure(ref, githubError(500, ""), readStart.Add(10*time.Second))
	if f.Persistent(readStart.Add(-time.Hour)) {
		t.Fatal("a clock that went backwards declared a fault")
	}
}
