package model_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A park (wait_for_task, resume_later) is the agent's own suspension:
// recorded as parked with suspend_kind requested, so it neither counts
// towards a failure streak nor ends one -- where it used to end one, as
// a success.
func TestAParkIsARequestedSuspension(t *testing.T) {
	store, ctx := open(t)
	task := model.Task{
		ID: "parker", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective,
		ResumeOnDeadline: true,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ResumeOnDeadline {
		t.Fatal("resume_on_deadline did not survive a write and a read")
	}
	for i, outcome := range []string{"failed", "failed", model.ParkedOutcome} {
		id := task.ID + "-" + string(rune('1'+i))
		at := now.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: task.ID, Attempt: i + 1, StartedAt: at},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, at.Add(time.Minute), outcome, ""); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := runs[len(runs)-1]; last.SuspendKind != model.SuspendRequested {
		t.Fatalf("a parked run has suspend_kind %q, want requested", last.SuspendKind)
	}
	streak, err := store.FailureStreak(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 2 || streak.LastOutcome != model.ParkedOutcome {
		t.Fatalf("streak = %+v, want the two failures behind the park, still read as parked", streak)
	}
	if model.ClassifyOutcome(model.ParkedOutcome) != model.OutcomeSuspended {
		t.Fatal("parked does not classify as a suspension")
	}
}

func TestDeadlineResumesCountsTheTrailingRun(t *testing.T) {
	done := now
	run := func(kind model.SuspensionKind, outcome string) model.Run {
		return model.Run{FinishedAt: &done, SuspendKind: kind, Outcome: outcome}
	}
	runs := []model.Run{
		run(model.SuspendDeadline, model.SuspendedOutcome),
		run("", "failed"),
		run(model.SuspendDeadline, model.SuspendedOutcome),
		run(model.SuspendUsageLimit, model.PausedOutcome),
		run(model.SuspendDeadline, model.SuspendedOutcome),
	}
	if n := model.DeadlineResumes(runs); n != 2 {
		t.Fatalf("DeadlineResumes = %d, want 2 (the failure ends the run, the usage limit is passed over)", n)
	}
}

func TestResumeOnRoundTrips(t *testing.T) {
	at := now.Add(time.Hour)
	r := model.ResumeOn{At: &at, CI: model.ResumeOnMyBranch, Message: true}
	back, err := model.ParseResumeOn(r.JSON())
	if err != nil {
		t.Fatal(err)
	}
	if back.CI != r.CI || !back.Message || back.At == nil || !back.At.Equal(at) {
		t.Fatalf("round trip = %+v, want %+v", back, r)
	}
	if (model.ResumeOn{}).JSON() != "" || !(model.ResumeOn{}).Empty() {
		t.Fatal("the empty ResumeOn is not stored as NULL")
	}
	want := "parked: resume_later until " + at.Format(time.RFC3339) +
		" has passed, or grain CI (my-branch) has a verdict, or anybody posts to this task: waiting on review"
	if got := model.RequestedDetail("waiting on review", r); got != want {
		t.Fatalf("RequestedDetail = %q, want %q", got, want)
	}
}

// A resume_later on ci or message holds its task deferred until the
// condition arrives: a post to the task, or a CI verdict on its branch
// that came after it stopped.
func TestResumeLaterWakesOnAMessageAndOnCI(t *testing.T) {
	store, ctx := open(t)
	for _, id := range []string{"on-message", "on-ci"} {
		task := model.Task{
			ID: id, Intent: model.IntentImplement, Title: "t",
			Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{ID: id + "-1", TaskID: id, Attempt: 1, StartedAt: now},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		// What ui.Client.ResumeLater does while the run is live.
		deferredAt := now.Add(time.Minute)
		if err := store.SetDeferred(ctx, id, &deferredAt, nil, deferredAt); err != nil {
			t.Fatal(err)
		}
		// What RunDispatch then processResultFrom do: the run finishes at
		// T+2m, and processResultFrom calls Suspend with the cycle's start
		// time (now).
		if err := store.FinishRun(ctx, id+"-1", now.Add(2*time.Minute), "succeeded", ""); err != nil {
			t.Fatal(err)
		}
		when := model.ResumeOn{Message: true}
		if id == "on-ci" {
			when = model.ResumeOn{CI: model.ResumeOnMyBranch}
		}
		if err := store.Suspend(ctx, model.Suspension{RunID: id + "-1", Kind: model.SuspendRequested,
			Detail: model.RequestedDetail("r", when), ResumeOn: when.JSON(), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	deferred := func(id string) bool {
		t.Helper()
		task, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return task.DeferredAt != nil
	}

	if woke, err := store.WakeOnMessage(ctx, "on-ci", now.Add(time.Hour)); err != nil || woke {
		t.Fatalf("a post woke a task waiting on CI (%v, %v)", woke, err)
	}
	if woke, err := store.WakeOnMessage(ctx, "on-message", now.Add(time.Hour)); err != nil || !woke || deferred("on-message") {
		t.Fatalf("a post did not wake the task waiting on one (%v, %v)", woke, err)
	}

	ciTask, err := store.GetTask(ctx, "on-ci")
	if err != nil {
		t.Fatal(err)
	}
	due, err := store.DueResumeOnCI(ctx)
	if err != nil || len(due) != 0 {
		t.Fatalf("due before any CI run = %v (%v)", due, err)
	}
	// A verdict from before the stop is not the one it waits for; one
	// arriving after the deferral is, even while the run is still winding
	// down.
	for i, finished := range []time.Time{now, now.Add(90 * time.Second)} {
		if err := store.PutCIRun(ctx, model.CIRun{ID: "ci-" + string(rune('a'+i)), Key: "k" + string(rune('a'+i)),
			Repo: "acme/widgets", Ref: ciTask.BranchName(), StartedAt: finished.Add(-time.Minute),
			FinishedAt: finished, Conclusion: "success"}); err != nil {
			t.Fatal(err)
		}
		due, err := store.DueResumeOnCI(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if want := i == 1; (len(due) == 1 && due[0] == "on-ci") != want {
			t.Fatalf("after a verdict at %v, due = %v, want on-ci due: %v", finished, due, want)
		}
	}
}
