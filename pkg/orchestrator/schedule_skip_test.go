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

package orchestrator_test

// The record a skipped firing leaves behind (model.ScheduleSkip,
// grain/task-3560), walked through the release cut's own night: a daily
// schedule comes due while its previous firing is still open, is held,
// and fires once as soon as that firing closes.

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// skipsOf is sched's skip history, newest first.
func skipsOf(t *testing.T, store *model.Store, scheduleID string) []model.ScheduleSkip {
	t.Helper()
	skips, err := store.ScheduleSkips(t.Context(), scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	return skips
}

// firstFiringOpen fires sched once at baseTime and returns the task it
// filed, left open, with the schedule's next occurrence 24h on.
func firstFiringOpen(t *testing.T, store *model.Store, sched model.Schedule) model.Task {
	t.Helper()
	if err := runScheduleOnly(t, store, baseTime); err != nil {
		t.Fatalf("first RunCycle: %v", err)
	}
	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks on the first cycle, want 1", len(filed))
	}
	return filed[0]
}

func TestScheduleSkipIsRecordedAndFiresOnceWhenTheBlockerCloses(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := filedSchedule(t, store, "sched-1", baseTime.Add(-time.Minute))
	blocker := firstFiringOpen(t, store, sched)
	if got := skipsOf(t, store, sched.ID); len(got) != 0 {
		t.Fatalf("a firing that fired recorded skips: %+v", got)
	}

	due := baseTime.Add(-time.Minute).Add(24 * time.Hour)
	// The occurrence comes due with the previous firing still open, and
	// the poll goes on re-asking for nine minutes.
	for _, at := range []time.Duration{0, time.Minute, 5 * time.Minute, 8 * time.Minute} {
		if err := runScheduleOnly(t, store, due.Add(at)); err != nil {
			t.Fatalf("RunCycle at +%v: %v", at, err)
		}
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 1 {
		t.Fatalf("filed %d tasks while the first was open, want still 1", len(filed))
	}
	skips := skipsOf(t, store, sched.ID)
	if len(skips) != 1 {
		t.Fatalf("recorded %d skips over four cycles of one occurrence, want 1: %+v", len(skips), skips)
	}
	skip := skips[0]
	if skip.Reason != model.SuppressedByOpenTask || skip.TaskID != blocker.ID ||
		skip.Outcome != model.ScheduleSkipHeld || !skip.Occurrence.Equal(due) || !skip.At.Equal(due) {
		t.Fatalf("skip = %+v, want held on task %s for the %v occurrence", skip, blocker.ID, due)
	}
	want := "skipped at " + due.UTC().Format("2006-01-02 15:04 MST") + ": #" + blocker.ID +
		" from the previous firing is still open (queued)"
	if got := skip.Summary(); got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}

	// The blocker closes nine minutes late; the next poll fires the held
	// occurrence, once.
	closed := due.Add(9 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: blocker.ID, ClosedAt: &closed}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Duration{9 * time.Minute, 10 * time.Minute} {
		if err := runScheduleOnly(t, store, due.Add(at)); err != nil {
			t.Fatalf("RunCycle at +%v: %v", at, err)
		}
	}
	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 2 {
		t.Fatalf("filed %d tasks once the blocker closed, want 2", len(filed))
	}
	var late string
	for _, tk := range filed {
		if tk.ID != blocker.ID {
			late = tk.ID
		}
	}
	skip = skipsOf(t, store, sched.ID)[0]
	if skip.Outcome != model.ScheduleSkipFired || skip.FiredTaskID != late || skip.Folded != 0 ||
		skip.ResolvedAt == nil || !skip.ResolvedAt.Equal(closed) {
		t.Errorf("skip after the late firing = %+v, want fired as %s at %v", skip, late, closed)
	}
	updated, err := store.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := due.Add(24 * time.Hour); !updated.NextRunAt.Equal(want) {
		t.Errorf("nextRunAt = %v, want the regular cadence %v", updated.NextRunAt, want)
	}
}

// A held occurrence the next regular one catches up with: the two
// collapse into one record and, when the blocker closes, one firing --
// the late one stands in for both, and says how many it absorbed.
func TestScheduleSkipsHeldPastTheNextOccurrenceCollapseIntoOneFiring(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := filedSchedule(t, store, "sched-1", baseTime.Add(-time.Minute))
	blocker := firstFiringOpen(t, store, sched)

	due := baseTime.Add(-time.Minute).Add(24 * time.Hour)
	for _, at := range []time.Duration{0, 24 * time.Hour, 25 * time.Hour} {
		if err := runScheduleOnly(t, store, due.Add(at)); err != nil {
			t.Fatalf("RunCycle at +%v: %v", at, err)
		}
	}
	if skips := skipsOf(t, store, sched.ID); len(skips) != 1 {
		t.Fatalf("recorded %d skips across two held occurrences, want 1: %+v", len(skips), skips)
	}

	closed := due.Add(26 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: blocker.ID, ClosedAt: &closed}); err != nil {
		t.Fatal(err)
	}
	if err := runScheduleOnly(t, store, closed); err != nil {
		t.Fatal(err)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 2 {
		t.Fatalf("filed %d tasks, want 2: one late firing for both held occurrences", len(filed))
	}
	skip := skipsOf(t, store, sched.ID)[0]
	if skip.Outcome != model.ScheduleSkipFired || skip.Folded != 1 {
		t.Errorf("skip = %+v, want fired with one occurrence folded in", skip)
	}
	updated, err := store.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := due.Add(48 * time.Hour); !updated.NextRunAt.Equal(want) {
		t.Errorf("nextRunAt = %v, want %v", updated.NextRunAt, want)
	}
}

// A paused schedule's due occurrence is recorded too, once.
func TestScheduleSkipRecordsAPausedSchedule(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	sched := filedSchedule(t, store, "sched-1", baseTime.Add(-time.Minute))
	if err := store.UpdateSchedule(t.Context(), sched.ID, func(s *model.Schedule) error {
		s.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Duration{0, time.Hour} {
		if err := runScheduleOnly(t, store, baseTime.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	skips := skipsOf(t, store, sched.ID)
	if len(skips) != 1 || skips[0].Reason != model.SkippedPaused ||
		!strings.Contains(skips[0].Summary(), "the schedule is paused") {
		t.Fatalf("skips = %+v, want one paused skip", skips)
	}
}

// A held occurrence the cadence is moved past -- here a paused schedule
// retimed by its declaring repository's sync, which goes straight to the
// store -- never fires, so the next on-time firing must not settle it as
// "fired late": it was dropped (2026-10-15 §4 walkthrough).
func TestScheduleSkipStrandedByARetimeIsDroppedNotFired(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	sched := filedSchedule(t, store, "sched-1", baseTime.Add(-time.Minute))
	if err := store.UpdateSchedule(t.Context(), sched.ID, func(s *model.Schedule) error {
		s.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runScheduleOnly(t, store, baseTime); err != nil {
		t.Fatal(err)
	}
	retimed := baseTime.Add(2 * time.Hour)
	if err := store.UpdateSchedule(t.Context(), sched.ID, func(s *model.Schedule) error {
		s.Enabled, s.NextRunAt = true, retimed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runScheduleOnly(t, store, retimed); err != nil {
		t.Fatal(err)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 1 {
		t.Fatalf("filed %d tasks, want the one on-time firing", len(filed))
	}
	skips := skipsOf(t, store, sched.ID)
	if len(skips) != 1 || skips[0].Outcome != model.ScheduleSkipDropped || skips[0].FiredTaskID != "" ||
		skips[0].ResolvedAt == nil || !skips[0].ResolvedAt.Equal(retimed) {
		t.Fatalf("skips = %+v, want the paused occurrence dropped at %v", skips, retimed)
	}
}

// OverlapAllow fires over an open firing, so nothing is skipped and
// nothing recorded.
func TestScheduleSkipNotRecordedWhenOverlapIsAllowed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := filedSchedule(t, store, "sched-1", baseTime.Add(-time.Minute))
	sched.Overlap = model.OverlapAllow
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	firstFiringOpen(t, store, sched)
	if err := runScheduleOnly(t, store, baseTime.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 2 {
		t.Fatalf("filed %d tasks, want 2", len(filed))
	}
	if skips := skipsOf(t, store, sched.ID); len(skips) != 0 {
		t.Fatalf("recorded skips under OverlapAllow: %+v", skips)
	}
}

// heldNotesOn is the comments the schedule itself wrote on taskID.
func heldNotesOn(t *testing.T, store *model.Store, taskID string) []model.Comment {
	t.Helper()
	comments, err := store.Comments(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Comment
	for _, c := range comments {
		if c.Author.Actor == model.SchedulePrincipal && strings.Contains(c.Body, "is held on this task") {
			out = append(out, c)
		}
	}
	return out
}

// A held firing says so on its blocker once that blocker waits on a
// person (grain/task-3748) -- the release cut held on #3201 while #3201
// waited on a Submit -- and says it once, however many cycles re-skip it.
func TestScheduleSkipNotesTheBlockerOnceItWaitsOnAPerson(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := filedSchedule(t, store, "sched-1", baseTime.Add(-time.Minute))
	blocker := firstFiringOpen(t, store, sched)

	due := baseTime.Add(-time.Minute).Add(24 * time.Hour)
	if err := runScheduleOnly(t, store, due); err != nil {
		t.Fatal(err)
	}
	// Queued is not waiting on anybody: nothing to say yet.
	if notes := heldNotesOn(t, store, blocker.ID); len(notes) != 0 {
		t.Fatalf("noted a held firing on a queued blocker: %+v", notes)
	}

	// The blocker asks a question and waits for a reply.
	question, err := store.AddComment(ctx, model.Comment{TaskID: blocker.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: blocker.ID}}, Body: "which one?", CreatedAt: due})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: blocker.ID, PendingQuestionCommentID: &question}); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, blocker.ID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("blocker state = %q (%v), want %q", st, err, model.StateAwaitingReply)
	}
	for _, at := range []time.Duration{time.Minute, 5 * time.Minute, 25 * time.Hour} {
		if err := runScheduleOnly(t, store, due.Add(at)); err != nil {
			t.Fatalf("RunCycle at +%v: %v", at, err)
		}
	}
	notes := heldNotesOn(t, store, blocker.ID)
	if len(notes) != 1 {
		t.Fatalf("wrote %d notes on the blocker over three cycles, want 1: %+v", len(notes), notes)
	}
	for _, want := range []string{sched.ID, due.UTC().Format("2006-01-02 15:04 MST"), "waiting on a reply"} {
		if !strings.Contains(notes[0].Body, want) {
			t.Errorf("note %q does not mention %q", notes[0].Body, want)
		}
	}
	if skip := skipsOf(t, store, sched.ID)[0]; skip.NotedTaskID != blocker.ID {
		t.Errorf("skip = %+v, want it to record the note on %s", skip, blocker.ID)
	}
}

// A firing that fails is written on the schedule as a held skip saying
// how, rather than returned as the cycle's error every ten seconds
// (grain/task-4537: two days of one log line per cycle per schedule, and
// nothing on the schedules page). Once the cause is fixed, the next cycle
// fires it once and settles the skip.
func TestAFailingFiringIsRecordedOnTheScheduleNotReturnedEveryCycle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := filedSchedule(t, store, "sched-1", baseTime.Add(-time.Minute))
	missing := "suite-missing"
	sched.SuiteID = &missing
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Duration{0, 10 * time.Second, time.Hour} {
		if err := runScheduleOnly(t, store, baseTime.Add(at)); err != nil {
			t.Fatalf("RunCycle at +%v: %v, want the failure recorded rather than returned", at, err)
		}
	}
	skips := skipsOf(t, store, sched.ID)
	if len(skips) != 1 {
		t.Fatalf("skips = %+v, want one for the one occurrence", skips)
	}
	if k := skips[0]; k.Reason != model.SkippedFiringFailed || k.Outcome != model.ScheduleSkipHeld ||
		!strings.Contains(k.Summary(), "firing failed: suite suite-missing no longer exists") {
		t.Fatalf("skip = %+v (%q)", k, k.Summary())
	}

	// Fixed: the schedule fires an ordinary task again, once.
	sched.SuiteID = nil
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	if err := runScheduleOnly(t, store, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle after the fix: %v", err)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 1 {
		t.Fatalf("filed %d tasks, want 1", len(filed))
	}
	if k := skipsOf(t, store, sched.ID)[0]; k.Outcome != model.ScheduleSkipFired {
		t.Errorf("skip after the fix = %+v, want fired late", k)
	}
}
