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
	"testing"
	"time"
)

// The three states anything measures a patience against, and the instant
// each one is measured from. Everything else answers nil, which is what
// a reader turns into "say nothing" rather than into "stuck".

func TestStateSinceIsTheApprovalForATaskThatHasNeverRun(t *testing.T) {
	approved := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	tk := task(true)
	tk.ApprovedAt = &approved

	times := StateTimesFrom(tk, nil, nil)
	if got := times.Since(StateQueued); got == nil || !got.Equal(approved) {
		t.Fatalf("queued since %v, want %v", got, approved)
	}
}

func TestStateSinceIsTheLastAttemptsEndingForATaskThatQueuedAgain(t *testing.T) {
	approved := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	started := approved.Add(time.Minute)
	finished := started.Add(time.Hour)
	tk := task(true)
	tk.ApprovedAt = &approved

	// Deliberately not in Store.Runs' order: StateTimesFrom promises
	// nothing about the order it is handed.
	runs := []Run{
		{StartedAt: started.Add(2 * time.Hour), FinishedAt: &finished},
		{StartedAt: started, FinishedAt: timeRef(started.Add(time.Minute))},
	}
	times := StateTimesFrom(tk, nil, runs)
	if got := times.Since(StateQueued); got == nil || !got.Equal(finished) {
		t.Fatalf("queued since %v, want the last attempt's ending %v", got, finished)
	}
}

func TestStateSinceIsTheRetryForATaskSomebodyHasJustPutBack(t *testing.T) {
	// The case that would otherwise flag a row for the one thing that is
	// not standing still: a task whose runs stopped days ago, back in the
	// queue because a person pressed Retry a minute ago.
	approved := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	finished := approved.Add(time.Hour)
	retried := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	tk := task(true)
	tk.ApprovedAt = &approved

	times := StateTimesFrom(tk, &Observation{RetryRequestedAt: &retried},
		[]Run{{StartedAt: approved, FinishedAt: &finished}})
	if got := times.Since(StateQueued); got == nil || !got.Equal(retried) {
		t.Fatalf("queued since %v, want the retry %v", got, retried)
	}

	// And the same for "Run now", which is the other way a person puts a
	// task back in front of dispatch.
	ranNow := retried.Add(time.Hour)
	times = StateTimesFrom(tk, &Observation{RetryRequestedAt: &retried, RunNowAt: &ranNow},
		[]Run{{StartedAt: approved, FinishedAt: &finished}})
	if got := times.Since(StateQueued); got == nil || !got.Equal(ranNow) {
		t.Fatalf("queued since %v, want the run-now request %v", got, ranNow)
	}
}

func TestStateSinceIsTheUndeferralForATaskPickedBackUp(t *testing.T) {
	// The case this measurement was blind to until the undeferral was
	// stamped: a task approved in January, put aside, and picked back up
	// this morning. Measured from its approval it reads as eight months
	// of standing still, and gets flagged for it on the first read after
	// it comes back -- the one thing that is not standing still.
	approved := time.Date(2026, 1, 9, 9, 0, 0, 0, time.UTC)
	back := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	tk := task(true)
	tk.ApprovedAt = &approved
	tk.UndeferredAt = &back

	// With no observation at all, which is the ordinary shape of a task
	// nobody has retried: the undeferral is the whole of RequeuedAt.
	times := StateTimesFrom(tk, nil, nil)
	if got := times.Since(StateQueued); got == nil || !got.Equal(back) {
		t.Fatalf("queued since %v, want the undeferral %v", got, back)
	}

	// And alongside an older retry, where the later of the two is the
	// wait that is actually going on.
	retried := approved.Add(time.Hour)
	times = StateTimesFrom(tk, &Observation{RetryRequestedAt: &retried}, nil)
	if got := times.Since(StateQueued); got == nil || !got.Equal(back) {
		t.Fatalf("queued since %v, want the undeferral %v", got, back)
	}

	// A run that finished after the task came back still outranks it --
	// each of the three is a moment the task re-entered the queue, and
	// the most recent one is the answer.
	finished := back.Add(time.Hour)
	times = StateTimesFrom(tk, nil, []Run{{StartedAt: back, FinishedAt: &finished}})
	if got := times.Since(StateQueued); got == nil || !got.Equal(finished) {
		t.Fatalf("queued since %v, want the attempt that ended after the undeferral %v", got, finished)
	}
}

func TestStateSinceIsTheLiveRunsOwnStartNotTheFirstAttempts(t *testing.T) {
	// A third attempt measured from the first one's start would read as
	// hours older than the run that is actually happening.
	first := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	firstEnd := first.Add(time.Hour)
	live := firstEnd.Add(time.Hour)
	tk := task(true)

	times := StateTimesFrom(tk, nil, []Run{
		{StartedAt: first, FinishedAt: &firstEnd},
		{StartedAt: live},
	})
	if got := times.Since(StateRunning); got == nil || !got.Equal(live) {
		t.Fatalf("running since %v, want the live run's own start %v", got, live)
	}
}

func TestStateSinceIsTheCompletionForAChangeOnTheMergeQueue(t *testing.T) {
	completed := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	times := StateTimesFrom(landedTask(), &Observation{CompletedAt: &completed}, nil)
	if got := times.Since(StateCompleted); got == nil || !got.Equal(completed) {
		t.Fatalf("completed since %v, want %v", got, completed)
	}
}

func TestStateSinceDeclinesToGuessForTheStatesAPersonOwns(t *testing.T) {
	// Not a gap: nothing is driving these, so how long one has stood is
	// not evidence of anything, and nothing carries a patience for them.
	at := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	times := StateTimes{
		ApprovedAt:        &at,
		RequeuedAt:        &at,
		LiveRunStartedAt:  &at,
		LastRunFinishedAt: &at,
		CompletedAt:       &at,
	}
	for _, state := range []State{StateProposed, StateAwaitingReply, StateAwaitingSubmit,
		StateFailed, StateDeferred, StateClosed} {
		if got := times.Since(state); got != nil {
			t.Errorf("%s since %v, want nothing", state, got)
		}
	}
}

func TestStateSinceMeasuresADeferralFromItsWakeTime(t *testing.T) {
	// A deferral with a wake time is the orchestrator's to end, and its
	// wait on that began when the wake time came -- not when somebody put
	// the task aside, which may have been a month earlier.
	deferred := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	until := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	tk := task(true)
	tk.DeferredAt, tk.DeferredUntil = &deferred, &until
	if got := StateTimesFrom(tk, nil, nil).Since(StateDeferred); got == nil || !got.Equal(until) {
		t.Fatalf("deferred since %v, want the wake time %v", got, until)
	}
	// One without a wake time is a person's, and says nothing.
	tk.DeferredUntil = nil
	if got := StateTimesFrom(tk, nil, nil).Since(StateDeferred); got != nil {
		t.Fatalf("a deferral with no wake time since %v, want nothing", got)
	}
}

func TestStateSinceIsNothingWhenTheRecordCannotSay(t *testing.T) {
	// A task approved before Task.ApprovedAt existed, and a state whose
	// own instant is simply not there. nil is the answer, and a reader
	// measuring a patience against it says nothing rather than measuring
	// against the zero time and calling everything stuck.
	times := StateTimesFrom(task(true), nil, nil)
	for _, state := range []State{StateQueued, StateRunning, StateCompleted} {
		if got := times.Since(state); got != nil {
			t.Errorf("%s since %v, want nothing", state, got)
		}
	}
}

func timeRef(t time.Time) *time.Time { return &t }
