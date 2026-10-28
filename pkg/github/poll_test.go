package github

import (
	"testing"
	"time"
)

func newTestSchedule(low *bool) *PollSchedule {
	return &PollSchedule{low: func(time.Time) bool { return *low }}
}

// An idle poll doubles its interval each time nothing changes, up to
// the cap, and a change puts it straight back to the base cadence.
func TestIdlePollsBackOffAndResetOnChange(t *testing.T) {
	low := false
	p := newTestSchedule(&low)
	key := PollKey{Subject: PullRequestSubject("acme", "widgets", 7), Aspect: "review comments"}
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)

	var intervals []time.Duration
	for i := 0; i < 7; i++ {
		if !p.Due(key, PollNormal, false, now) {
			t.Fatalf("poll %d not due at the end of its interval", i)
		}
		p.Observe(key, "same", now)
		iv := p.Interval(key)
		intervals = append(intervals, iv)
		if p.Due(key, PollNormal, false, now.Add(iv-time.Second)) {
			t.Fatalf("poll %d due before its interval %s ran out", i, iv)
		}
		now = now.Add(iv)
	}
	want := []time.Duration{PollBase, 2 * PollBase, 4 * PollBase, 8 * PollBase, PollCap, PollCap, PollCap}
	for i := range want {
		if intervals[i] != want[i] {
			t.Fatalf("intervals = %v, want %v", intervals, want)
		}
	}

	p.Observe(key, "changed", now)
	if p.Interval(key) != PollBase {
		t.Errorf("interval after a change = %s, want %s", p.Interval(key), PollBase)
	}
}

// Anything grain does to a pull request -- or an explicit Reset, which
// is what an agent waiting on its checks asks for -- makes it due now.
func TestAGrainWriteMakesItsPullRequestDueAgain(t *testing.T) {
	saved := polls
	t.Cleanup(func() { polls = saved })
	low := false
	polls = newTestSchedule(&low)
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)
	pr := PollKey{Scope: "1", Subject: PullRequestSubject("Acme", "Widgets", 7), Aspect: "review comments"}
	other := PollKey{Scope: "1", Subject: PullRequestSubject("acme", "widgets", 8), Aspect: "review comments"}
	branch := PollKey{Scope: "1", Subject: BranchSubject("acme", "widgets", "grain/x/task-1"), Aspect: "path triggers"}
	elsewhere := PollKey{Scope: "1", Subject: PullRequestSubject("acme", "gadgets", 7), Aspect: "review comments"}
	idle := func() {
		for _, k := range []PollKey{pr, other, branch, elsewhere} {
			for i := 0; i < 4; i++ {
				polls.Observe(k, "same", now)
			}
		}
	}
	due := func(k PollKey) bool { return polls.Due(k, PollNormal, false, now.Add(time.Second)) }

	idle()
	noteWrite("POST", "/repos/acme/widgets/issues/7/comments")
	if !due(pr) || due(other) {
		t.Errorf("after a comment on #7: #7 due %v (want true), #8 due %v (want false)", due(pr), due(other))
	}

	idle()
	noteWrite("PATCH", "/repos/acme/widgets/git/refs/heads/grain/x/task-1")
	if !due(branch) || due(pr) {
		t.Errorf("after a branch update: branch due %v, #7 due %v", due(branch), due(pr))
	}

	// A merge moves every pull request's base, so the whole repository
	// is looked at again -- and only that repository.
	idle()
	noteWrite("PUT", "/repos/acme/widgets/pulls/7/merge")
	if !due(pr) || !due(other) || !due(branch) || due(elsewhere) {
		t.Errorf("after a merge: #7 %v #8 %v branch %v other repo %v", due(pr), due(other), due(branch), due(elsewhere))
	}

	idle()
	noteWrite("GET", "/repos/acme/widgets/pulls/7")
	if due(pr) {
		t.Error("a read reset the backoff")
	}
	polls.Reset(pr.Subject)
	if !due(pr) {
		t.Error("Reset did not make the pull request due")
	}
}

// Something active is read every cycle, exactly as before -- a pull
// request with CI in flight sees no change in behaviour from any of this
// while the budget is fine.
func TestAnActivePollIsAlwaysDue(t *testing.T) {
	low := false
	p := newTestSchedule(&low)
	key := PollKey{Subject: PullRequestSubject("acme", "widgets", 7), Aspect: "checks"}
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		if !p.Due(key, PollNormal, true, now) {
			t.Fatalf("an active poll was not due on cycle %d", i)
		}
		p.Observe(key, "same", now)
		now = now.Add(PollBase)
	}
}

// Under a low budget the merge queue's head keeps being read every
// cycle, everything else ordinary drops to the cap, and background
// polling stops until the budget recovers.
func TestLowBudgetKeepsTheMergeQueueHeadAndCutsTheRest(t *testing.T) {
	low := true
	p := newTestSchedule(&low)
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)
	head := PollKey{Subject: PullRequestSubject("acme", "widgets", 1), Aspect: "merge queue head"}
	active := PollKey{Subject: PullRequestSubject("acme", "widgets", 2), Aspect: "checks"}
	stale := PollKey{Subject: BranchSubject("acme", "widgets", "old"), Aspect: "path triggers"}

	headReads, activeReads, staleReads := 0, 0, 0
	for i := 0; i < 20; i++ { // ten minutes of thirty-second cycles
		if p.Due(head, PollCritical, true, now) {
			headReads++
			p.Observe(head, "same", now)
		}
		if p.Due(active, PollNormal, true, now) {
			activeReads++
			p.Observe(active, "same", now)
		}
		if p.Due(stale, PollBackground, false, now) {
			staleReads++
			p.Observe(stale, "same", now)
		}
		now = now.Add(PollBase)
	}
	if headReads != 20 {
		t.Errorf("merge queue head read %d times in 20 cycles, want every one", headReads)
	}
	if activeReads != 2 {
		t.Errorf("ordinary poll read %d times in ten minutes under a low budget, want 2 (once, then at the cap)", activeReads)
	}
	if staleReads != 0 {
		t.Errorf("background poll read %d times under a low budget, want none", staleReads)
	}

	low = false
	if !p.Due(stale, PollBackground, false, now) {
		t.Error("background polling did not resume once the budget recovered")
	}
}

func TestForgottenPollsAreDropped(t *testing.T) {
	low := false
	p := newTestSchedule(&low)
	now := time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)
	gone := PollKey{Subject: PullRequestSubject("acme", "widgets", 1), Aspect: "checks"}
	p.Due(gone, PollNormal, false, now)
	p.Observe(gone, "x", now)
	p.Due(PollKey{Subject: "other"}, PollNormal, false, now.Add(pollForget+time.Minute))
	if _, ok := p.entries[gone]; ok {
		t.Error("an entry nobody asked about for an hour is still held")
	}
}
