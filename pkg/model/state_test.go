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

package model

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// State is computed twice on purpose — once as a SQL view so the store
// makes "state is never written" structural, and once in Go so code
// holding a Task needs no database. Two implementations of one rule is a
// drift risk, and this file is what pays for it.

var (
	thenState = regexp.MustCompile(`THEN '(\w+)'`)
	elseState = regexp.MustCompile(`ELSE '(\w+)'`)
)

func stateView() string {
	for _, v := range Views {
		if v.Name == "task_state" {
			return v.DDL
		}
	}
	panic("no task_state view")
}

// viewPrecedence is the state strings in task_state's CASE, in the order
// the CASE tests them — which is the view's precedence.
func viewPrecedence() []string {
	var out []string
	for _, m := range thenState.FindAllStringSubmatch(stateView(), -1) {
		out = append(out, m[1])
	}
	for _, m := range elseState.FindAllStringSubmatch(stateView(), -1) {
		out = append(out, m[1])
	}
	return out
}

// goPrecedence is the same precedence, discovered by turning each
// condition on from the most significant down and seeing what StateOf
// returns.
func goPrecedence() []string {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	id := int64(5)
	approved := task(true)
	unapproved := task(false)
	// The one condition that is not an Observation field: an unsubmitted
	// pull request on the task itself (StateAwaitingSubmit, which splits
	// the same completed_at the two branches below test).
	unsubmitted := task(true)
	unsubmitted.Links = []Link{{Kind: LinkFixes, Target: "acme/widgets#1"}}
	// The same completed_at split a third way. 'awaiting_ack' is a
	// finished task with no pull request at all, which is what a bare
	// task(true) already is, so 'completed' is the one that needs saying
	// out loud: a pull request somebody has submitted.
	landed := landedTask()
	// The other one: a deferral, also on the task itself. Approved, so
	// that the row proves deferring outranks 'queued' rather than only
	// restating the 'proposed' row below it.
	deferred := task(true)
	deferred.DeferredAt = &now
	got := []State{
		StateOf(approved, &Observation{ClosedAt: &now}, true, 0),
		StateOf(unsubmitted, &Observation{CompletedAt: &now}, true, 0),
		StateOf(approved, &Observation{CompletedAt: &now}, true, 0),
		StateOf(landed, &Observation{CompletedAt: &now}, true, 0),
		StateOf(approved, &Observation{PendingQuestionCommentID: &id}, true, 0),
		StateOf(approved, nil, true, 0),
		StateOf(approved, nil, false, MaxConsecutiveFailures),
		StateOf(deferred, nil, false, 0),
		StateOf(unapproved, nil, false, 0),
		StateOf(approved, nil, false, 0),
	}
	out := make([]string, len(got))
	for i, s := range got {
		out[i] = string(s)
	}
	return out
}

func task(approved bool) Task {
	t := Task{
		ID: "a1b2", Intent: IntentImplement, Title: "t",
		Origin: Origin{
			Attribution: Attribution{Actor: Principal{PrincipalHuman, "someone"}},
			Reason:      ReasonDirect,
		},
		Binding: BindingDirective,
	}
	if approved {
		t.Approval = &Attribution{Actor: Principal{PrincipalHuman, "someone"}}
	}
	return t
}

// landedTask is an approved task whose run opened a pull request that
// somebody has submitted -- the one shape that still reads 'completed'
// now that the post-run life is split three ways. Every other completed
// task here has either an unsubmitted pull request ('awaiting_submit')
// or none at all ('awaiting_ack').
func landedTask() Task {
	t := task(true)
	t.AutoMerge = true
	t.Links = []Link{{Kind: LinkFixes, Target: "acme/widgets#1"}}
	return t
}

func TestViewAndGoDerivationAgreeOnPrecedence(t *testing.T) {
	// If somebody reorders one CASE branch and not the other, this is
	// what says so. The failure is otherwise a task in the wrong state
	// only when two conditions happen to be true at once.
	if got, want := viewPrecedence(), goPrecedence(); !reflect.DeepEqual(got, want) {
		t.Fatalf("view precedence %v, Go precedence %v", got, want)
	}
}

func TestEveryStateIsReachable(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range goPrecedence() {
		seen[s] = true
	}
	for _, s := range []State{StateProposed, StateQueued, StateRunning,
		StateAwaitingReply, StateFailed, StateAwaitingSubmit,
		StateCompleted, StateDeferred, StateClosed} {
		if !seen[string(s)] {
			t.Errorf("state %q is not reachable from the derivation", s)
		}
	}
}

// TestAwaitsSubmitNeedsAPullRequestNobodySubmitted pins the two halves
// of the condition that splits the post-run life in two: a task with
// nothing to submit and a task already submitted are both waiting on
// nobody, and neither should be told a human owes it a click.
func TestAwaitsSubmitNeedsAPullRequestNobodySubmitted(t *testing.T) {
	withPR := func(autoMerge bool) Task {
		tk := task(true)
		tk.AutoMerge = autoMerge
		tk.Links = []Link{{Kind: LinkFixes, Target: "acme/widgets#1"}}
		return tk
	}
	if !AwaitsSubmit(withPR(false)) {
		t.Error("an unsubmitted pull request is exactly what awaits a Submit click")
	}
	if AwaitsSubmit(withPR(true)) {
		t.Error("auto-merge is what Submit sets, so a submitted task awaits nobody")
	}
	// An analyze task that answered in a comment: finished, not parked.
	// Its other links are no substitute -- only a fixes-link is a pull
	// request.
	noPR := task(true)
	noPR.Links = []Link{{Kind: LinkProposedBy, Target: "c3d4"}, {Kind: LinkDependsOn, Target: "e5f6"}}
	if AwaitsSubmit(noPR) {
		t.Error("a task with no pull request has nothing to submit")
	}
}

// TestSubmittingMovesATaskOffAwaitingSubmit is the whole point of the
// state: it is the one post-run state a task cannot leave on its own,
// and Submit (which sets AutoMerge, ui.Client.Submit) is what lets it.
func TestSubmittingMovesATaskOffAwaitingSubmit(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	tk := task(true)
	tk.Links = []Link{{Kind: LinkFixes, Target: "acme/widgets#1"}}
	obs := &Observation{CompletedAt: &now}

	if got := StateOf(tk, obs, false, 0); got != StateAwaitingSubmit {
		t.Fatalf("StateOf on an unsubmitted pull request = %q, want %q", got, StateAwaitingSubmit)
	}
	tk.AutoMerge = true
	if got := StateOf(tk, obs, false, 0); got != StateCompleted {
		t.Fatalf("StateOf once submitted = %q, want %q", got, StateCompleted)
	}
	// Closing still outranks it, the same as it outranks 'completed':
	// there is nothing left to submit on a task somebody closed.
	tk.AutoMerge = false
	if got := StateOf(tk, &Observation{CompletedAt: &now, ClosedAt: &now}, false, 0); got != StateClosed {
		t.Fatalf("StateOf on a closed task = %q, want %q", got, StateClosed)
	}
}

// TestAwaitsAckIsAFinishedTaskWithNothingToLand pins the split this
// state exists for: the badge on a task that produced no pull request
// used to say "Queued for merge", about a task that had no pull request
// to queue.
func TestAwaitsAckIsAFinishedTaskWithNothingToLand(t *testing.T) {
	// An investigation that answered in a comment. Its other links are
	// no evidence either way -- only a fixes-link is a pull request.
	answered := task(true)
	answered.Links = []Link{{Kind: LinkProposedBy, Target: "c3d4"}}
	if !AwaitsAck(answered) {
		t.Error("a finished task with no pull request has nothing to land and an answer to read")
	}
	if AwaitsAck(landedTask()) {
		t.Error("a task with a pull request is waiting on the merge, not on a reader")
	}
	// A review grain filed itself delivers its findings on the change
	// under review, so the wait it creates belongs to that task and not to
	// this one. orchestrator.SyncReviews files one as an implement task
	// with ReasonReview, so the origin is the only thing that catches it.
	filed := task(true)
	filed.Origin.Reason = ReasonReview
	if AwaitsAck(filed) {
		t.Error("a review grain filed itself is read on the change under review")
	}
	// And the half of that exclusion this state took back: a task that
	// merely declares itself a review is not one of grain's, has no
	// change under review to deliver findings to, and so has them relayed
	// into its own conversation -- which is an answer on this task and
	// nowhere else, the exact thing this state is for. It used to read
	// 'completed' and so be badged "Queued for merge" with no pull
	// request to merge.
	declared := task(true)
	declared.Intent = IntentReview
	if !AwaitsAck(declared) {
		t.Error("a task that declared itself a review answered on itself and is waiting to be read")
	}
}

// TestAcknowledgingIsTheOnlyWayOffAwaitingAck is the same point
// TestSubmittingMovesATaskOffAwaitingSubmit makes about its own state:
// nothing grain does moves a task off this one, because there is no pull
// request for anything to happen to. Closing it is the whole of the
// answer.
func TestAcknowledgingIsTheOnlyWayOffAwaitingAck(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	tk := task(true)
	obs := &Observation{CompletedAt: &now}

	if got := StateOf(tk, obs, false, 0); got != StateAwaitingReply {
		t.Fatalf("StateOf on a task that answered in a comment = %q, want %q", got, StateAwaitingReply)
	}
	// AutoMerge is not an answer to it: Submit's own flag says something
	// about a pull request, and there is none here.
	tk.AutoMerge = true
	if got := StateOf(tk, obs, false, 0); got != StateAwaitingReply {
		t.Fatalf("StateOf with auto-merge set and nothing to merge = %q, want %q", got, StateAwaitingReply)
	}
	if got := StateOf(tk, &Observation{CompletedAt: &now, ClosedAt: &now}, false, 0); got != StateClosed {
		t.Fatalf("StateOf once acknowledged = %q, want %q", got, StateClosed)
	}
}

// TestTransitionsNamesTheCompletionMomentAwaitingAck is the third arm of
// the rule its awaiting_submit neighbour below covers: one completion
// moment, entered under whichever name StateOf gives it now, so the
// timeline's last entry and the badge above it never disagree.
func TestTransitionsNamesTheCompletionMomentAwaitingAck(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	finished := created.Add(time.Hour)
	tk := task(true)
	tk.CreatedAt, tk.ApprovedAt = &created, &created
	obs := &Observation{CompletedAt: &finished}

	got := Transitions(tk, obs, nil, nil, nil)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateAwaitingReply, At: finished},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}
}

// TestTransitionsNamesTheCompletionMomentAwaitingSubmit keeps the
// timeline's last entry and the state badge above it saying the same
// thing -- the record has no separate "somebody submitted it" timestamp
// to hang a second entry on.
func TestTransitionsNamesTheCompletionMomentAwaitingSubmit(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	finished := created.Add(time.Hour)
	tk := task(true)
	tk.CreatedAt, tk.ApprovedAt = &created, &created
	tk.Links = []Link{{Kind: LinkFixes, Target: "acme/widgets#1"}}
	obs := &Observation{CompletedAt: &finished}

	got := Transitions(tk, obs, nil, nil, nil)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateAwaitingSubmit, At: finished},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}

	tk.AutoMerge = true
	got = Transitions(tk, obs, nil, nil, nil)
	want[2] = Transition{State: StateCompleted, At: finished}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions once submitted = %+v, want %+v", got, want)
	}
}

// TestDeferringLeavesApprovalAlone is the reason DeferredAt is a column
// of its own rather than a second way of clearing approval: a task comes
// back from a deferral into the state it went in with, so nobody has to
// re-approve work they already approved.
func TestDeferringLeavesApprovalAlone(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		task  Task
		after State
	}{
		{"queued", task(true), StateQueued},
		{"proposed", task(false), StateProposed},
	} {
		tk := tc.task
		tk.DeferredAt = &now
		if got := StateOf(tk, nil, false, 0); got != StateDeferred {
			t.Errorf("a deferred %s task reads %q, want %q", tc.name, got, StateDeferred)
		}
		tk.DeferredAt = nil
		if got := StateOf(tk, nil, false, 0); got != tc.after {
			t.Errorf("undeferring a %s task leaves it %q, want %q", tc.name, got, tc.after)
		}
	}
}

// TestTransitionsNamesTheDeferralOnlyWhileItLasts is Transitions' own
// half of the same rule the awaiting_reply test below covers: the record
// holds the deferral a task is in, not the start of the ones it has come
// back from, so the timeline says so exactly while it is true. The
// *return* it made is a different matter and is stamped -- see the test
// after this one.
func TestTransitionsNamesTheDeferralOnlyWhileItLasts(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	deferred := created.Add(time.Hour)
	tk := task(true)
	tk.CreatedAt, tk.ApprovedAt = &created, &created
	tk.DeferredAt = &deferred

	got := Transitions(tk, nil, nil, nil, nil)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateDeferred, At: deferred},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}

	tk.DeferredAt = nil
	for _, tr := range Transitions(tk, nil, nil, nil, nil) {
		if tr.State == StateDeferred {
			t.Fatalf("a task picked back up should leave no deferred transition: %+v", tr)
		}
	}
}

// The other half of a deferral that has ended: the moment the task came
// back (Task.UndeferredAt), which is the part of it the record does keep.
//
// It goes on the timeline under the state the task returned to, which is
// whichever of 'proposed' and 'queued' its own approval implies --
// undeferring is not an approval and not a withdrawal of one.
func TestTransitionsNamesTheReturnFromADeferral(t *testing.T) {
	created := time.Date(2026, 1, 9, 10, 0, 0, 0, time.UTC)
	back := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

	tk := task(true)
	tk.CreatedAt, tk.ApprovedAt = &created, &created
	tk.UndeferredAt = &back
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateQueued, At: back},
	}
	if got := Transitions(tk, nil, nil, nil, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}

	// A proposal nobody had approved comes back a proposal, the way
	// StateOf reads it.
	proposal := task(false)
	proposal.CreatedAt = &created
	proposal.UndeferredAt = &back
	want = []Transition{
		{State: StateProposed, At: created},
		{State: StateProposed, At: back},
	}
	if got := Transitions(proposal, nil, nil, nil, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions for a returned proposal = %+v, want %+v", got, want)
	}

	// Approved after it came back -- Run now on a deferred proposal does
	// exactly this -- so the return is still a return to 'proposed' and
	// the approval that followed is its own entry.
	late := back.Add(time.Hour)
	approvedLate := task(true)
	approvedLate.CreatedAt, approvedLate.ApprovedAt = &created, &late
	approvedLate.UndeferredAt = &back
	want = []Transition{
		{State: StateProposed, At: created},
		{State: StateProposed, At: back},
		{State: StateQueued, At: late},
	}
	if got := Transitions(approvedLate, nil, nil, nil, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions for a task approved after it came back = %+v, want %+v", got, want)
	}

	// And a task deferred again after that return keeps both: the
	// deferral it is in now, and the coming back it did before it.
	againAt := back.Add(2 * time.Hour)
	again := task(true)
	again.CreatedAt, again.ApprovedAt = &created, &created
	again.UndeferredAt, again.DeferredAt = &back, &againAt
	want = []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateQueued, At: back},
		{State: StateDeferred, At: againAt},
	}
	if got := Transitions(again, nil, nil, nil, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions for a task deferred a second time = %+v, want %+v", got, want)
	}
}

func TestBlockedIsNotAState(t *testing.T) {
	// Blocked is an annotation on queued, derived from links, which is
	// why it lives in its own view.
	for _, s := range viewPrecedence() {
		if s == "blocked" {
			t.Fatal("blocked leaked into the state vocabulary")
		}
	}
}

func TestStateIsAViewAndNotAColumn(t *testing.T) {
	// The invariant the schema exists to make structural: there is no
	// column to write, so no finish path can write one.
	for _, table := range Tables {
		if strings.Contains(table, "CREATE TABLE IF NOT EXISTS `task` ") {
			if strings.Contains(table, "`state`") || strings.Contains(table, "state_since") {
				t.Fatal("task table grew a state column")
			}
		}
	}
}

func TestDeclarationAndObservationAreSeparateTables(t *testing.T) {
	// They answer to different records, and keeping them apart is what
	// would let a declaration change be branched and reviewed while
	// observations keep landing on the trunk.
	var taskTable string
	for _, table := range Tables {
		if strings.Contains(table, "CREATE TABLE IF NOT EXISTS `task` ") {
			taskTable = table
		}
	}
	for _, observed := range []string{"closed_at", "completed_at", "baseline_comment_id", "retry_requested_at"} {
		if strings.Contains(taskTable, observed) {
			t.Errorf("observation column %q leaked into the task table", observed)
		}
	}
}

func TestLinkKindsThatBlock(t *testing.T) {
	// merge-with gates the merge, not the run, which is what lets the
	// members of a coordinated cross-repo change be worked in parallel.
	for kind, want := range map[LinkKind]bool{
		LinkDependsOn: true, LinkChildOf: true,
		LinkMergeWith: false, LinkFixes: false,
		LinkAddresses: false, LinkProposedBy: false,
		// continues is provenance between a closed console session and
		// the one that replaced it. A replacement that blocked on the
		// conversation it replaces would never be dispatched, which is
		// the session dead the moment somebody split it.
		LinkContinues:  false,
		LinkSupersedes: false, LinkSupersededBy: false,
		LinkGoogleChat: false,
		LinkReverts:    false, LinkRevertedBy: false,
	} {
		if got := kind.Blocks(); got != want {
			t.Errorf("%s.Blocks() = %v, want %v", kind, got, want)
		}
	}
}

func TestLandingStateIsDecidedByTheActorNotTheReason(t *testing.T) {
	human := Principal{PrincipalHuman, "someone"}
	bot := Principal{PrincipalAutomation, "grain"}
	// Every reason, with a human actor: queued. With automation: not.
	// A sixth reason cannot queue itself because reasons are not read.
	for _, r := range []OriginReason{ReasonDirect, ReasonSchedule, ReasonFix,
		ReasonReview, ReasonProposal} {
		if !LandsQueued(Origin{Attribution{Actor: human}, r}) {
			t.Errorf("human + %s should land queued", r)
		}
		if LandsQueued(Origin{Attribution{Actor: bot, OnBehalfOf: &human}, r}) {
			t.Errorf("automation + %s must not land queued, even on a human's behalf", r)
		}
	}
}

func TestTransitionsCoversProposedThroughCompleted(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	approved := created.Add(time.Minute)
	started := approved.Add(time.Minute)
	finished := started.Add(10 * time.Minute)

	// A task whose work is landing, so that the entry this test is about
	// is the one 'completed' actually means -- which of the three
	// post-run states a completion reads as is
	// TestTransitionsNamesTheCompletionMoment*'s own subject.
	tk := landedTask()
	tk.CreatedAt, tk.ApprovedAt = &created, &approved
	obs := &Observation{CompletedAt: &finished}
	runs := []Run{{StartedAt: started, FinishedAt: &finished}}

	got := Transitions(tk, obs, runs, nil, nil)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: approved},
		{State: StateRunning, At: started},
		{State: StateCompleted, At: finished},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}
}

func TestTransitionsRequeuesBetweenAttemptsButNotAfterTheLastOne(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	firstStart := created.Add(time.Minute)
	firstEnd := firstStart.Add(time.Minute)
	secondStart := firstEnd.Add(time.Minute)
	secondEnd := secondStart.Add(time.Minute)

	tk := landedTask()
	tk.CreatedAt, tk.ApprovedAt = &created, &firstStart
	obs := &Observation{CompletedAt: &secondEnd}
	runs := []Run{
		{StartedAt: firstStart, FinishedAt: &firstEnd},
		{StartedAt: secondStart, FinishedAt: &secondEnd},
	}

	got := Transitions(tk, obs, runs, nil, nil)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: firstStart},
		{State: StateRunning, At: firstStart},
		// The requeue between the two attempts, but nothing after the
		// second's own finish -- that instant is StateCompleted instead.
		{State: StateQueued, At: firstEnd, Inferred: true},
		{State: StateRunning, At: secondStart},
		{State: StateCompleted, At: secondEnd},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}
}

// TestTransitionsPlacesARequeueAtItsRecordedMomentWhenThereIsOne: a
// Retry pressed between two attempts is when the task went back in line,
// so the requeue sits there rather than at the earlier attempt's finish.
func TestTransitionsPlacesARequeueAtItsRecordedMomentWhenThereIsOne(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	firstStart := created.Add(time.Minute)
	firstEnd := firstStart.Add(time.Minute)
	retried := firstEnd.Add(time.Hour)
	secondStart := retried.Add(time.Minute)

	tk := task(true)
	tk.CreatedAt, tk.ApprovedAt = &created, &created
	obs := &Observation{RetryRequestedAt: &retried}
	runs := []Run{
		{StartedAt: firstStart, FinishedAt: &firstEnd},
		{StartedAt: secondStart},
	}

	got := Transitions(tk, obs, runs, nil, nil)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateRunning, At: firstStart},
		{State: StateQueued, At: retried},
		{State: StateRunning, At: secondStart},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}
}

// TestTransitionsMarksARequeueItHasNoMomentFor: a task queued again
// after its last attempt -- a question answered, say -- has no recorded
// requeue time, so the entry stands at that attempt's finish and says it
// is inferred. An undeferral in the gap is its own entry instead.
func TestTransitionsMarksARequeueItHasNoMomentFor(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	started := created.Add(time.Minute)
	finished := started.Add(time.Minute)

	tk := task(true)
	tk.CreatedAt, tk.ApprovedAt = &created, &created
	runs := []Run{{StartedAt: started, FinishedAt: &finished}}

	got := Transitions(tk, &Observation{}, runs, nil, nil)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateRunning, At: started},
		{State: StateQueued, At: finished, Inferred: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}

	back := finished.Add(time.Hour)
	tk.UndeferredAt = &back
	got = Transitions(tk, &Observation{}, runs, nil, nil)
	want[3] = Transition{State: StateQueued, At: back}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions after an undeferral = %+v, want %+v", got, want)
	}
}

func TestTransitionsShowsFailedOnceTheStreakCapsOut(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	tk := task(true)
	tk.CreatedAt, tk.ApprovedAt = &created, &created
	lastFinished := created.Add(time.Hour)
	streak := &FailureStreak{Count: MaxConsecutiveFailures, LastFinishedAt: lastFinished, LastOutcome: "failed"}

	got := Transitions(tk, nil, nil, streak, nil)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateFailed, At: lastFinished},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}
}

// TestTransitionsHidesFailedOnceTheTaskHasCompleted covers
// bwsalmon/agents#502: a run salvaged into a pull request after erroring
// keeps its own outcome "failed" forever (orchestrator.salvagePushedBranch
// never corrects it), so task_streak's count can sit at or above
// MaxConsecutiveFailures for a task that has, in every other respect,
// completed. StateOf already masks that streak the moment
// obs.CompletedAt is set; Transitions has to apply the same precedence
// when reconstructing history, or a completed task's timeline shows a
// bogus "Failed" entry right before "Completed", forever.
func TestTransitionsHidesFailedOnceTheTaskHasCompleted(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	tk := landedTask()
	tk.CreatedAt, tk.ApprovedAt = &created, &created
	lastFinished := created.Add(time.Hour)
	streak := &FailureStreak{Count: MaxConsecutiveFailures, LastFinishedAt: lastFinished, LastOutcome: "failed"}
	obs := &Observation{CompletedAt: &lastFinished}

	got := Transitions(tk, obs, nil, streak, nil)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateCompleted, At: lastFinished},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}
}

func TestTransitionsShowsAPendingQuestionOnlyWhileOutstanding(t *testing.T) {
	created := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	asked := created.Add(time.Hour)
	tk := task(true)
	tk.CreatedAt, tk.ApprovedAt = &created, &created
	obs := &Observation{PendingQuestionCommentID: new(int64)}
	runs := []Run{{StartedAt: created.Add(time.Minute), FinishedAt: &asked}}

	got := Transitions(tk, obs, runs, nil, &asked)
	want := []Transition{
		{State: StateProposed, At: created},
		{State: StateQueued, At: created},
		{State: StateRunning, At: created.Add(time.Minute)},
		{State: StateAwaitingReply, At: asked},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Transitions = %+v, want %+v", got, want)
	}

	// Once answered, the caller passes no askedAt at all -- the period is
	// gone from the record, not merely unlabelled.
	obs.PendingQuestionCommentID = nil
	got = Transitions(tk, obs, runs, nil, nil)
	for _, tr := range got {
		if tr.State == StateAwaitingReply {
			t.Fatalf("an answered question should leave no awaiting_reply transition: %+v", got)
		}
	}
}

func TestIsBlockedIgnoresNonBlockingLinks(t *testing.T) {
	tk := task(true)
	tk.Links = []Link{
		{Kind: LinkProposedBy, Target: "open-but-irrelevant"},
		{Kind: LinkMergeWith, Target: "also-open"},
		{Kind: LinkExternalPR, Target: "acme/widgets#42"},
		{Kind: LinkGitHubEvent, Target: "comment:555"},
	}
	if IsBlocked(tk, map[string]bool{}) {
		t.Error("provenance and merge-group links must not block dispatch")
	}
	tk.Links = append(tk.Links, Link{Kind: LinkDependsOn, Target: "c3d4"})
	if !IsBlocked(tk, map[string]bool{}) {
		t.Error("an open depends-on must block")
	}
	if IsBlocked(tk, map[string]bool{"c3d4": true}) {
		t.Error("a closed depends-on must not block")
	}
}

func TestLeaseExpiry(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	issued := now.Add(-30 * time.Hour)
	l := Lease{IssuedAt: issued}
	if l.Expired(now, 0) {
		t.Error("a lease with no expiry and no backstop is not expired")
	}
	// The backstop exists because materialisation has a window that
	// cannot be closed: a failure between minting and recording leaks a
	// credential nothing knows to revoke.
	if !l.Expired(now, 24*time.Hour) {
		t.Error("the 24h backstop should have caught a 30h-old lease")
	}
	soon := now.Add(time.Hour)
	if (Lease{IssuedAt: issued, ExpiresAt: &soon}).Expired(now, 0) {
		t.Error("a lease expiring later is not expired yet")
	}
}

// TestNothingNeededEndingsReadTheSameInGoAndTheView pins what the two
// nothing_needed endings ProcessResult writes read as. "close" stamps
// closed_at beside completed_at, and closed outranks every post-run state
// so the task never shows in awaiting_ack; "ack" stamps completed_at
// alone, which is awaiting_ack exactly as a closing comment is. The view
// half of the parity is the same precedence TestViewAndGoDerivationAgree
// OnPrecedence already pins, and pkg/model/sqlite's
// TestNothingNeededActionRoundTripsAndClosesThroughTheView reads it back
// through the real task_state view.
func TestNothingNeededEndingsReadTheSameInGoAndTheView(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	closed := task(true)
	closed.NothingNeededAction = NothingNeededClose
	if got := StateOf(closed, &Observation{CompletedAt: &now, ClosedAt: &now}, false, 0); got != StateClosed {
		t.Errorf("close: state = %q, want closed", got)
	}
	acked := task(true)
	acked.NothingNeededAction = NothingNeededAck
	if got := StateOf(acked, &Observation{CompletedAt: &now}, false, 0); got != StateAwaitingReply {
		t.Errorf("ack: state = %q, want awaiting_reply", got)
	}
	if got := NothingNeededAction("").Resolved(); got != NothingNeededClose {
		t.Errorf(`"".Resolved() = %q, want close`, got)
	}
	for _, s := range []string{"", "close", "ack"} {
		if _, err := ParseNothingNeededAction(s); err != nil {
			t.Errorf("ParseNothingNeededAction(%q): %v", s, err)
		}
	}
	if _, err := ParseNothingNeededAction("delete"); err == nil {
		t.Error(`ParseNothingNeededAction("delete") accepted`)
	}
}
