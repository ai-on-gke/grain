package cli

// defer_until_test.go: what `grain defer -until` accepts (task 386).
//
// The flag exists so that "do not start this before the 14th" is
// something a person can set rather than write into a task's body, and
// the two spellings below are what somebody actually types: a bare date
// for the common case, and a full timestamp for the caller who means a
// particular instant. A bare date is the start of that day where the
// person typing it is, which is the only reading that matches the words
// -- so these run against time.Local rather than pinning a zone.

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestDeferUntilTakesADateOrATimestamp(t *testing.T) {
	t.Parallel()
	// No flag at all is the deferral grain has always made: aside until
	// somebody undeferrs it.
	opts, err := deferOptions("")
	if err != nil || opts.Until != nil {
		t.Fatalf("deferOptions(\"\") = (%+v, %v), want no wake time and no error", opts, err)
	}

	opts, err = deferOptions("2026-09-14")
	if err != nil {
		t.Fatalf("a bare date: %v", err)
	}
	want := time.Date(2026, 9, 14, 0, 0, 0, 0, time.Local)
	if opts.Until == nil || !opts.Until.Equal(want) {
		t.Fatalf("deferOptions(\"2026-09-14\") = %v, want the start of that day locally (%v)", opts.Until, want)
	}

	opts, err = deferOptions("2026-09-14T09:00:00+01:00")
	if err != nil {
		t.Fatalf("an RFC 3339 time: %v", err)
	}
	want = time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	if opts.Until == nil || !opts.Until.Equal(want) {
		t.Fatalf("deferOptions with an offset = %v, want the instant it names (%v)", opts.Until, want)
	}

	// Anything else is refused here rather than sent to a daemon that
	// would refuse it in less helpful words -- and the message says both
	// spellings, since the whole point of failing early is that the next
	// attempt works.
	_, err = deferOptions("next tuesday")
	if err == nil {
		t.Fatal("deferOptions(\"next tuesday\") = no error, want one")
	}
	for _, want := range []string{"2026-09-14", "RFC 3339"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
}

// And the wake time is readable again afterwards. `grain get` printed a
// bare "deferred" whatever a task was deferred until, so the one fact
// worth knowing about a task put aside on a date was in -json and
// nowhere else.
func TestTaskBlockShowsTheWakeTime(t *testing.T) {
	t.Parallel()
	until := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	block := taskBlock(ui.Task{ID: "7", Title: "wait for the release", State: "deferred", DeferredUntil: &until})
	if !strings.Contains(block, "deferred until: 2026-09-14T00:00:00Z") {
		t.Errorf("`grain get` on a deferred task says nothing about when it wakes:\n%s", block)
	}

	// A deferral nobody put a date on says nothing extra: there is no
	// wake time to report, and a blank line saying so would be noise on
	// every other task.
	block = taskBlock(ui.Task{ID: "7", Title: "wait", State: "deferred"})
	if strings.Contains(block, "deferred until") {
		t.Errorf("a deferral with no wake time still printed one:\n%s", block)
	}
}

func TestTaskBlockShowsActivityAndWaitReason(t *testing.T) {
	t.Parallel()
	wait := "waiting: 1 worker runs are live of the 1 this cycle allows"
	block := taskBlock(ui.Task{ID: "9", Title: "queued behind capacity", State: "queued", Activity: wait, ActivityBySetup: true})
	if !strings.Contains(block, "activity:   "+wait+"\n") {
		t.Errorf("`grain get` on a waiting queued task omits its wait reason:\n%s", block)
	}

	block = taskBlock(ui.Task{ID: "9", Title: "idle queued task", State: "queued"})
	if strings.Contains(block, "activity:") {
		t.Errorf("a task with no activity still printed an activity line:\n%s", block)
	}
}

func TestTaskBlockShowsPausedStatus(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	grace := at.Add(2 * time.Minute)
	block := taskBlock(ui.Task{
		ID:    "3",
		Title: "running work",
		State: "running",
		Paused: &ui.TaskPaused{
			At:         at,
			By:         "alice",
			FromState:  "running",
			GraceUntil: &grace,
		},
	})
	want := "paused:     by alice at 2026-10-26T15:00:00Z (was running), grace until 2026-10-26T15:02:00Z\n"
	if !strings.Contains(block, want) {
		t.Errorf("taskBlock on a gracefully paused running task missing %q:\n%s", want, block)
	}

	block = taskBlock(ui.Task{
		ID:    "3",
		Title: "running work",
		State: "running",
		Paused: &ui.TaskPaused{
			At:        at,
			By:        "alice",
			FromState: "running",
			Immediate: true,
		},
	})
	wantImmediate := "paused:     by alice at 2026-10-26T15:00:00Z (was running), immediate\n"
	if !strings.Contains(block, wantImmediate) {
		t.Errorf("taskBlock on an immediately paused running task missing %q:\n%s", wantImmediate, block)
	}

	block = taskBlock(ui.Task{ID: "3", Title: "unpaused work", State: "queued"})
	if strings.Contains(block, "paused:") {
		t.Errorf("taskBlock on an unpaused task printed a paused line:\n%s", block)
	}
}
