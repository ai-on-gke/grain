package forge

import (
	"errors"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
)

// defaultUnreadableDeadline is how long reads of one pull request have to
// go on failing before the merge queue will say on that task's row that
// it cannot read the pull request (orchestrator's unreadable state).
//
// It exists because GitHub is flaky at a rate that is entirely ordinary
// and entirely invisible from inside one read. This deployment's own
// daemon log for 16-17 September 2026 carries a 5xx roughly every few
// hours, spread over eight different pull requests and over both of the
// reads Observe makes: 502 "Server Error", 500 with no body at all, 502
// as GitHub's HTML error page, 504 "We couldn't respond to your request
// in time", 503 "upstream connect error ... connection refused". Every
// one of them was answered by the next cycle ten seconds later and every
// one of the changes merged. What they cost was a sentence on somebody's
// row saying grain could not read their pull request, which is a fault
// report about a deployment that was working.
//
// So the run of failures is timed, exactly as a refused merge and a
// stalled check are, and only a run that outlasts this is a fault
// anybody is shown. Two minutes is about twelve cycles at the ten-second
// reconcile this deployment runs: far longer than any flake observed,
// and short enough that a credential which really has lost its access is
// on the row before the person who broke it has finished breaking it.
//
// A constant rather than a setting, on the terms defaultCheckStallDeadline
// and defaultMergeRefusalDeadline set out, with SetUnreadableDeadline as
// the escape hatch.
const defaultUnreadableDeadline = 2 * time.Minute

// ReadFailure is what this process knows about one unbroken run of reads
// of one pull request that did not come back with an answer: what went
// wrong, how many times in a row, since when, and whether the fault is
// one that resolves by itself.
//
// It is the third of this package's clocks and deliberately the same
// shape as MergeRefusal, down to carrying its own Deadline. What it
// gives the caller is the difference between "one GET failed" and "grain
// has not been able to read this pull request for two minutes", which is
// the whole of the difference between a row that should say nothing and
// a row that should say something is wrong.
//
// Keyed by pull request with no head sha, unlike the other two: a read
// that failed is one that never learned what the head commit is, so
// there is nothing a push could invalidate. A successful read ends the
// run (ForgetReadFailures, called by Observe), which is what keeps this
// measuring one unbroken run rather than a tally of every flake since
// the daemon started.
type ReadFailure struct {
	// Reason is what went wrong, already written for somebody to read
	// (github.Message): "GitHub answered 502: Server Error" rather than
	// the status with the whole escaped JSON body after it.
	//
	// Rendered here rather than carried raw and rendered by the caller,
	// which is the opposite of what MergeRefusal.Reason does, because the
	// only consumer of this record is a row a person is looking at --
	// github.Message's own doc comment draws that line, and the untrimmed
	// error still reaches this process's log regardless, since the read
	// error is returned to the cycle unchanged.
	//
	// It is the *latest* failure's reason, not the first: a run can
	// change its mind (a 502 while a repository is being renamed, then a
	// 404 once it has been), and what is wrong now is what a person
	// looking now needs.
	Reason string
	// Transient is whether the latest failure is one that resolves on its
	// own -- GitHub 5xx, a throttle, or a call that never reached GitHub
	// at all. See TransientRead, and the two sentences the orchestrator
	// writes off the back of this.
	Transient bool
	// Count is how many reads in a row have failed. One is a flake, a
	// dozen is a fault; it is also what keeps a deployment whose cycle is
	// slower than the deadline from declaring a fault on a single read.
	Count       int
	First, Last time.Time
	// Deadline is the window Persistent measures against, carried on the
	// record for the reason MergeRefusal.Deadline is.
	Deadline time.Duration
}

// Persistent reports whether this run of failed reads has gone on long
// enough to be a fault rather than a flake -- the point at which the
// queue stops expecting the next cycle to read the pull request fine.
//
// Both halves are required. The clock is the substance of it, and the
// count is there because nothing guarantees the caller's cycle is faster
// than the deadline: one read that failed three minutes ago and has not
// been retried since is not a run of anything, and a deployment whose
// reconcile interval is raised must not start reporting every single
// flake as a fault.
//
// Written as a comparison against the deadline rather than as
// now.Sub(First) >= Deadline, so a clock that jumped backwards reads as
// "not persistent yet": the direction everything else timed here errs in.
func (f ReadFailure) Persistent(now time.Time) bool {
	if f.Deadline <= 0 || f.First.IsZero() || f.Count < 2 {
		return false
	}
	return !now.Before(f.First.Add(f.Deadline))
}

// TransientRead reports whether a failed read of the forge is one that
// resolves without anybody doing anything.
//
// Three things are transient, and they are the three that are not GitHub
// answering the question:
//
//   - a 5xx (github.IsServerError), which is GitHub having a bad minute;
//   - a throttle (github.IsRateLimited), including this deployment's own
//     gate declining to spend a call it has been told will fail, which
//     ends by construction at a named instant;
//   - anything that is not an answer from GitHub at all -- a refused
//     connection, a DNS failure, a timeout, a proxy in the way.
//
// Everything else is GitHub having answered, and having answered "no":
// 404 on a repository this credential can no longer see, 403 on a
// permission it does not hold, 401 on a token that has been revoked.
// Those do not clear on their own and the person reading the row is the
// one who has to do something about them.
//
// The default for an unrecognised failure is *terminal*, which is the
// opposite of the safe direction the predicates in pkg/github take, and
// deliberately so: nothing here latches or switches anything off, the
// only consequence is which of two sentences a row carries after the
// deadline has already passed, and a lasting fault described as "GitHub
// is having trouble, grain is retrying" is the sentence that wastes
// somebody's afternoon.
func TransientRead(err error) bool {
	if err == nil {
		return false
	}
	if github.IsServerError(err) || github.IsRateLimited(err) {
		return true
	}
	var answered *github.Error
	return !errors.As(err, &answered)
}

// readFailures is the bookkeeping behind ReadFailure, in memory for the
// reason refusals and sightings are: persisting it would mean a schema
// column, and losing it costs one more window of reads the queue is
// making anyway. A process that restarts starts the clock again, which
// errs toward saying nothing -- the direction this whole record exists
// to err in.
type readFailures struct {
	mu       sync.Mutex
	deadline time.Duration
	seen     map[string]ReadFailure
}

var failedReads = &readFailures{deadline: defaultUnreadableDeadline, seen: map[string]ReadFailure{}}

// RecordReadFailure records that a read of ref did not answer, and
// returns the whole run of failures that one belongs to.
//
// Called by Observe for both of the reads it makes, since a pull request
// whose check runs cannot be read is as unreadable as one whose own
// endpoint cannot be, and a run of failures that alternated between the
// two would otherwise never be counted as a run at all.
func RecordReadFailure(ref Ref, cause error, now time.Time) ReadFailure {
	failedReads.mu.Lock()
	defer failedReads.mu.Unlock()
	key := ref.String()
	f, ok := failedReads.seen[key]
	if !ok {
		f = ReadFailure{First: now}
	}
	f.Reason, f.Transient = github.Message(cause), TransientRead(cause)
	f.Last, f.Deadline = now, failedReads.deadline
	f.Count++
	failedReads.seen[key] = f
	return f
}

// LastReadFailure returns the run of failed reads on record for ref, if
// this process has one. A caller that has just failed a read has the
// record already (RecordReadFailure returns it); this is for one asking
// about a pull request it did not read this cycle.
func LastReadFailure(ref Ref) (ReadFailure, bool) {
	failedReads.mu.Lock()
	defer failedReads.mu.Unlock()
	f, ok := failedReads.seen[ref.String()]
	return f, ok
}

// ForgetReadFailures drops ref's record, so the next failed read starts a
// fresh run. Observe calls it on every read that answered, which is what
// makes the count a run of consecutive failures rather than a tally: two
// flakes a day apart are two runs of one, and neither is a fault.
func ForgetReadFailures(ref Ref) {
	failedReads.mu.Lock()
	defer failedReads.mu.Unlock()
	delete(failedReads.seen, ref.String())
}

// SetUnreadableDeadline replaces the window described on
// defaultUnreadableDeadline and returns the function that puts the old
// one back -- the same shape SetCheckStallDeadline and
// SetMergeRefusalDeadline have, used the same way, including clearing the
// records taken under the old window.
//
// Zero switches the report off entirely: reads then fail forever without
// anything reaching the row, which is neither what this package wants nor
// what it did before the record existed, so it is for a test that wants
// to pin exactly that and not for a deployment.
func SetUnreadableDeadline(d time.Duration) func() {
	failedReads.mu.Lock()
	prev := failedReads.deadline
	failedReads.deadline = d
	failedReads.seen = map[string]ReadFailure{}
	failedReads.mu.Unlock()
	return func() { SetUnreadableDeadline(prev) }
}
