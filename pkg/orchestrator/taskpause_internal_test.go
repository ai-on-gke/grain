package orchestrator

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A paused change is out of the merge queue and any batch, and is back in
// it, where it was, the moment the pause is lifted: membership is read off
// the pause each cycle, and nothing about the entry's queue position
// (its OrderKey, MergeQueueEnteredAt) is touched by pausing.
func TestAPausedChangeLeavesTheMergeQueueAndReturnsOnResume(t *testing.T) {
	entered := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	e := queueEntry{
		task: model.Task{ID: "t1", AutoMerge: true, OrderKey: 3},
		obs:  &model.Observation{TaskID: "t1", MergeQueueEnteredAt: &entered},
	}
	if !isQueueMember(e) {
		t.Fatal("the unpaused change is not a queue member: this test's entry is wrong")
	}
	e.paused = true
	if isQueueMember(e) {
		t.Fatal("a paused change is still a merge-queue member")
	}
	e.paused = false
	if !isQueueMember(e) || e.obs.MergeQueueEnteredAt != &entered || e.task.OrderKey != 3 {
		t.Fatal("the resumed change did not return to the queue where it was")
	}
}

func TestPauseGraceOver(t *testing.T) {
	now := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	later := now.Add(time.Minute)
	for _, c := range []struct {
		p    model.TaskPause
		want bool
	}{
		{model.TaskPause{Immediate: true, GraceUntil: &later}, true},
		{model.TaskPause{GraceUntil: &later}, false},
		{model.TaskPause{GraceUntil: &now}, true},
		{model.TaskPause{}, true},
	} {
		if got := pauseGraceOver(c.p, now); got != c.want {
			t.Errorf("pauseGraceOver(%+v) = %v, want %v", c.p, got, c.want)
		}
	}
}

// A pause rewrites only a strike-bearing ending as interrupted: one already
// classified as earning no strike keeps what it says about the run.
func TestNoStrikeOutcome(t *testing.T) {
	for _, o := range []string{model.InterruptedOutcome, model.PausedOutcome, model.CapacityOutcome,
		model.EnvironmentOutcome, model.OOMOutcome} {
		if !noStrikeOutcome(o) {
			t.Errorf("noStrikeOutcome(%q) = false, want true", o)
		}
	}
	for _, o := range []string{"failed", "cancelled", "finish-failed"} {
		if noStrikeOutcome(o) {
			t.Errorf("noStrikeOutcome(%q) = true, want false", o)
		}
	}
}
