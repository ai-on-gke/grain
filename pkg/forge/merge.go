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

package forge

import (
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// defaultMergeRefusalDeadline is how long the merge queue goes on asking
// GitHub to merge a pull request that reads clean and that GitHub keeps
// refusing, before it stops driving that pull request by itself and asks
// a person instead (orchestrator.escalateRefusedMerge).
//
// A refusal is not the same wait as an unfinished check, and it is
// deliberately a much shorter one than defaultCheckStallDeadline. The
// refusals that resolve on their own resolve within a cycle or two -- a
// head that moved between the read and the call (409), a mergeability
// GitHub had not finished computing -- while the ones that do not
// resolve never do: a branch protection wanting a review nobody gave, a
// required check GitHub has and grain's own verdict does not, a
// credential without write access to the base. Half an hour is long
// enough to ride out the first kind and short enough that the second
// kind reaches somebody while they still remember pushing the branch.
//
// A constant rather than a setting, on the terms defaultCheckStallDeadline
// sets out: it is not a number an operator has any ordinary reason to
// reach for, and SetMergeRefusalDeadline is the escape hatch until one
// does.
const defaultMergeRefusalDeadline = 30 * time.Minute

// MergeRefusal is what this process knows about one unbroken run of
// merges GitHub would not perform for one pull request: what it said, how
// many times it has said it, and when it started saying it.
//
// It exists because a refused merge is the one dead end the merge queue
// used to have no clock and no words for. Every other way a pull request
// can stop moving is timed and eventually handed to a person -- a
// conflict, a failing check, a check that never reports, a repair that
// never finishes -- but a merge GitHub simply will not do was retried
// every cycle forever, with the error going to this process's log and
// nothing at all to the task whose row went on saying "Merging". This is
// the record that makes that wait sayable (orchestrator's merge_failed
// state) and boundable (Stalled).
type MergeRefusal struct {
	// HeadSHA is the commit the refused merge was asked for. A push
	// starts the story again: a new commit is a new pull request as far
	// as merging it goes, exactly as it is for the two check clocks.
	HeadSHA string
	// Reason is what GitHub said, as the error reached the caller --
	// untrimmed and unabridged, since the caller renders it for a row or
	// for a comment and only it knows which.
	Reason string
	// Count is how many times in a row this commit's merge has been
	// refused, which is worth saying to a person: one refusal is a race,
	// forty is a rule.
	Count       int
	First, Last time.Time
	// Deadline is the window Stalled measures against, carried on the
	// record so a caller that wants to say how long it waited does not
	// have to ask a second time (and cannot ask under a different window
	// than the one the record was kept under).
	Deadline time.Duration
	// Kind is what GitHub refused for (github.ClassifyMergeRefusal), and
	// it decides what happens next: an approval is waited for, a rule or
	// a required check is handed to a person once, and only a refusal
	// nothing could place is retried -- MaxBlindMergeRetries times, with
	// backoff (RetryAt).
	Kind github.RefusalKind
	// Requirements is what GitHub's rules ask of the pull request, read
	// when the refusal was classified (github.MergeRequirements), and
	// Reviews the review state then (MergeRequirements.ApprovalSignature):
	// an approval wait re-arms when the reviews read differently.
	Requirements github.MergeRequirements
	Reviews      string
	// Escalated is the queue having handed this refusal to a person
	// (orchestrator.escalateRefusedMerge). A person clearing that block
	// (Retry merge) is a fresh start, not the next refusal of this run.
	Escalated bool
	// Polled is when the queue last read GitHub to see whether what this
	// refusal is waiting for has arrived (an approval, required checks
	// passing), so that a wait asks once every few minutes rather than
	// once a cycle.
	Polled time.Time
}

// MaxBlindMergeRetries is how many times in a row the queue asks GitHub
// for a merge it has been refused for a reason nobody could place, before
// it stops and hands the refusal to a person. A refusal that is a race --
// a head or base that moved under the call -- is over in one or two; one
// that is a rule never is, and used to be counted up to thirty-five.
const MaxBlindMergeRetries = 3

// blindRetryBackoff is the wait before the second blind retry; each one
// after waits twice as long as the one before.
const blindRetryBackoff = time.Minute

// RetryAt is the earliest the merge should be asked for again without
// anything having changed, and zero where it should not be asked for again
// at all on its own: a refusal that is a rule or a wanted approval is
// re-armed by what it is waiting for, never by a clock.
func (r MergeRefusal) RetryAt() time.Time {
	if r.Kind != "" && r.Kind != github.RefusalTransient {
		return time.Time{}
	}
	wait := blindRetryBackoff
	for i := 1; i < r.Count && wait < time.Hour; i++ {
		wait *= 2
	}
	return r.Last.Add(wait)
}

// Exhausted reports whether this run of refusals is one the queue should
// stop driving: a rule or required check at once, a refusal nobody could
// place after MaxBlindMergeRetries tries or once Stalled. An approval
// wait is never exhausted -- it is a wait, and it is shown as one.
func (r MergeRefusal) Exhausted(now time.Time) bool {
	switch r.Kind {
	case github.RefusalApproval:
		return false
	case github.RefusalRule, github.RefusalRequiredChecks:
		return true
	}
	return r.Count >= MaxBlindMergeRetries || r.Stalled(now)
}

// Stalled reports whether this run of refusals has gone on for longer
// than the deadline it was recorded under -- the point at which the queue
// stops expecting GitHub to change its mind.
//
// Written as a comparison against the deadline rather than as
// now.Sub(First) >= Deadline, so a clock that jumped backwards reads as
// "not stalled yet": the direction everything else timed here errs in.
func (r MergeRefusal) Stalled(now time.Time) bool {
	if r.Deadline <= 0 || r.First.IsZero() {
		return false
	}
	return !now.Before(r.First.Add(r.Deadline))
}

// refusals is the bookkeeping behind MergeRefusal, and it is in memory
// for the reason sightings is: persisting it would mean a schema column,
// and losing it costs one more window of the retries the queue is making
// anyway. It can never merge something early or give up on something
// sooner -- a process that restarts starts the clock again, which errs
// toward waiting.
//
// Keyed by pull request, with the head sha held alongside so that a push
// clears the record rather than inheriting the verdict of the commit
// before it.
type refusals struct {
	mu       sync.Mutex
	deadline time.Duration
	seen     map[string]MergeRefusal
}

var refusedMerges = &refusals{deadline: defaultMergeRefusalDeadline, seen: map[string]MergeRefusal{}}

// RecordMergeRefusal records that GitHub refused to merge ref at headSHA,
// and returns the whole run of refusals that one belongs to.
//
// A refusal for a different commit than the one on record replaces it
// rather than extending it, and so does the first refusal after a
// success (ForgetMergeRefusals is what a merge that went through calls).
func RecordMergeRefusal(ref Ref, headSHA, reason string, now time.Time) MergeRefusal {
	refusedMerges.mu.Lock()
	defer refusedMerges.mu.Unlock()
	key := ref.String()
	r, ok := refusedMerges.seen[key]
	if !ok || r.HeadSHA != headSHA {
		r = MergeRefusal{HeadSHA: headSHA, First: now}
	}
	r.Reason, r.Last, r.Deadline = reason, now, refusedMerges.deadline
	r.Count++
	refusedMerges.seen[key] = r
	return r
}

// ClassifyLastMergeRefusal records what the refusal on record for ref at
// headSHA was for, and returns the record as it now stands.
func ClassifyLastMergeRefusal(ref Ref, headSHA string, kind github.RefusalKind, req github.MergeRequirements) (MergeRefusal, bool) {
	refusedMerges.mu.Lock()
	defer refusedMerges.mu.Unlock()
	r, ok := refusedMerges.seen[ref.String()]
	if !ok || r.HeadSHA != headSHA {
		return MergeRefusal{}, false
	}
	r.Kind, r.Requirements, r.Reviews = kind, req, req.ApprovalSignature()
	refusedMerges.seen[ref.String()] = r
	return r, true
}

// NoteMergeRefusalPolled stamps the record's Polled (see there).
func NoteMergeRefusalPolled(ref Ref, now time.Time) {
	refusedMerges.mu.Lock()
	defer refusedMerges.mu.Unlock()
	if r, ok := refusedMerges.seen[ref.String()]; ok {
		r.Polled = now
		refusedMerges.seen[ref.String()] = r
	}
}

// PeekMergeRefusal is the record for ref whatever commit it is about --
// for a caller that has to decide something about the pull request
// before it has read which commit is its head (the merge queue's
// membership, which an approval wait takes an entry out of).
func PeekMergeRefusal(ref Ref) (MergeRefusal, bool) {
	refusedMerges.mu.Lock()
	defer refusedMerges.mu.Unlock()
	r, ok := refusedMerges.seen[ref.String()]
	return r, ok
}

// MarkMergeRefusalEscalated notes that the refusal on record for ref has
// been handed to a person (MergeRefusal.Escalated).
func MarkMergeRefusalEscalated(ref Ref) {
	refusedMerges.mu.Lock()
	defer refusedMerges.mu.Unlock()
	if r, ok := refusedMerges.seen[ref.String()]; ok {
		r.Escalated = true
		refusedMerges.seen[ref.String()] = r
	}
}

// LastMergeRefusal returns the run of refusals on record for ref at
// headSHA, if this process has one. It is what lets a cycle that has not
// yet tried to merge say why the last one did not take: the queue is
// about to make the same call again, and a row that said nothing until
// the call failed again would spend most of its life claiming to be
// merging.
func LastMergeRefusal(ref Ref, headSHA string) (MergeRefusal, bool) {
	refusedMerges.mu.Lock()
	defer refusedMerges.mu.Unlock()
	r, ok := refusedMerges.seen[ref.String()]
	if !ok || r.HeadSHA != headSHA {
		return MergeRefusal{}, false
	}
	return r, true
}

// ForgetMergeRefusals drops ref's record, so the next refusal starts a
// fresh run. Called when a merge goes through, when the pull request
// stops reading clean (whatever is wrong with it now is a different
// story, and the queue has other machinery for it), and when the pull
// request settles for good.
func ForgetMergeRefusals(ref Ref) {
	refusedMerges.mu.Lock()
	defer refusedMerges.mu.Unlock()
	delete(refusedMerges.seen, ref.String())
}

// SetMergeRefusalDeadline replaces the window described on
// defaultMergeRefusalDeadline and returns the function that puts the old
// one back -- the same shape SetCheckStallDeadline has, and used the same
// way, including clearing the records taken under the old window.
//
// Zero switches giving up off: a refused merge is then retried forever
// and nobody is asked, which is what this package did before the record
// existed.
func SetMergeRefusalDeadline(d time.Duration) func() {
	refusedMerges.mu.Lock()
	prev := refusedMerges.deadline
	refusedMerges.deadline = d
	refusedMerges.seen = map[string]MergeRefusal{}
	refusedMerges.mu.Unlock()
	return func() { SetMergeRefusalDeadline(prev) }
}
