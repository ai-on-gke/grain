package model_test

// A firing of a workflow schedule is a run and a report, and it is not
// finished until the report has been read -- so an open review
// suppresses the next firing (model.OverlapSkip, which the translation
// writes and nothing can change). The review is an ordinary agent task,
// though, and one of those can stay open indefinitely: parked on a
// question, waiting on a human, sitting in a state nobody closes. While
// it did, every later firing was suppressed and NextRunAt was left in
// the past, so the poll re-asked and re-skipped for ever and the suite
// stopped running altogether -- silently, and with the review that would
// have noticed being the very thing suppressed (grain/task-493).
//
// These are the tests for the bound on that wait: the review holds the
// cadence for a few of the schedule's own occurrences and then stops
// holding it, and the firing that goes ahead says on the abandoned
// review that it has.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

var nightlyRepo = model.RepoRef{Owner: "acme", Name: "widgets"}

// firstNight is 02:11 on a Monday, the hour this deployment's own
// nightly fires at.
var firstNight = time.Date(2026, 9, 21, 2, 11, 0, 0, time.UTC)

// nightlySchedule is a schedule translated out of a workflow's own
// `on: schedule` cron: daily, skipping an overlapping firing, and
// pointed at this build's own review template rather than at a row.
func nightlySchedule(t *testing.T, store *model.Store, ctx context.Context) model.Schedule {
	t.Helper()
	templateID := model.WorkflowReviewTemplateID("nightly")
	sched := model.Schedule{
		ID:         "sched-nightly",
		Title:      `Review last night's "nightly" workflow run`,
		Target:     nightlyRepo,
		TemplateID: &templateID,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 2*60 + 11},
		Enabled:    true,
		NextRunAt:  firstNight,
		CreatedAt:  firstNight.Add(-24 * time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("writing the schedule: %v", err)
	}
	return sched
}

// fireNightly fires the schedule as the cadence does, re-read from the
// store first: a firing moves NextRunAt, and the next one has to fire
// the row as it now stands rather than a stale copy.
func fireNightly(t *testing.T, store *model.Store, ctx context.Context, id string,
	now time.Time) model.ScheduleFiring {

	t.Helper()
	sched, err := store.GetSchedule(ctx, id)
	if err != nil || sched == nil {
		t.Fatalf("reading schedule %s: %v, %v", id, sched, err)
	}
	firing, err := store.FireSchedule(ctx, *sched, now, time.UTC, model.RepoRef{})
	if err != nil {
		t.Fatalf("firing %s at %s: %v", id, now, err)
	}
	return firing
}

// fileReview stands in for the cycles that start the run, collect its
// verdict and file the review (orchestrator's scheduledworkflow.go):
// the firing's row is marked reviewed and an open task carrying the
// schedule's firing tag appears, which is the state this bound is about.
func fileReview(t *testing.T, store *model.Store, ctx context.Context, sched model.Schedule,
	filedAt time.Time) string {

	t.Helper()
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("reading the firing of %s: %v, %v", sched.ID, row, err)
	}
	id, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatalf("allocating a task id: %v", err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  sched.Title,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.SchedulePrincipal},
			Reason:      model.ReasonSchedule,
		},
		Approval:  &model.Attribution{Actor: model.SchedulePrincipal},
		Target:    &nightlyRepo,
		Tags:      []string{model.ScheduleFiringTag(sched.ID)},
		CreatedAt: &filedAt,
	}); err != nil {
		t.Fatalf("filing the review task: %v", err)
	}
	row.ReviewTaskID = id
	row.Conclusion = "success"
	finished := filedAt
	row.FinishedAt = &finished
	if err := store.PutScheduledWorkflowRun(ctx, *row); err != nil {
		t.Fatalf("recording the review: %v", err)
	}
	return id
}

func commentsOn(t *testing.T, store *model.Store, ctx context.Context, taskID string) []model.Comment {
	t.Helper()
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		t.Fatalf("reading %s's conversation: %v", taskID, err)
	}
	return comments
}

// The rule itself, unchanged: a review somebody is reading suppresses
// tonight's firing, and the caller is told which task is holding it.
func TestAWorkflowScheduleSkipsWhileItsReviewIsBeingRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sched := nightlySchedule(t, store, ctx)

	if firing := fireNightly(t, store, ctx, sched.ID, firstNight); !firing.WorkflowRun {
		t.Fatalf("first firing = %+v, want a recorded workflow run", firing)
	}
	review := fileReview(t, store, ctx, sched, firstNight.Add(3*time.Hour))

	firing := fireNightly(t, store, ctx, sched.ID, firstNight.Add(24*time.Hour))
	if !firing.Skipped {
		t.Errorf("second firing = %+v, want it skipped while the review is open", firing)
	}
	if firing.OpenReview != review {
		t.Errorf("openReview = %q, want the review task %s that is holding the cadence", firing.OpenReview, review)
	}
	if got := commentsOn(t, store, ctx, review); len(got) != 0 {
		t.Errorf("review has %d comments, want a schedule that is merely waiting to say nothing", len(got))
	}
	// Nothing was recorded and nothing moved: the firing is delayed, not
	// dropped, exactly as it always was.
	after, err := store.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("re-reading the schedule: %v", err)
	}
	if !after.NextRunAt.Equal(firstNight.Add(24 * time.Hour)) {
		t.Errorf("nextRunAt = %s, want the suppressed occurrence left where it was", after.NextRunAt)
	}
}

// And the bound: a review nobody has closed several cadences later is
// no longer counted as a firing in flight, so the schedule fires again
// rather than stopping for good.
func TestAWorkflowScheduleFiresOverAReviewNobodyCloses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sched := nightlySchedule(t, store, ctx)

	fireNightly(t, store, ctx, sched.ID, firstNight)
	filedAt := firstNight.Add(3 * time.Hour)
	review := fileReview(t, store, ctx, sched, filedAt)

	// Three more nights with nobody reading it. The first of those is
	// held (above); by the last the review has stopped holding anything.
	for night := 1; night <= 3; night++ {
		firing := fireNightly(t, store, ctx, sched.ID, firstNight.Add(time.Duration(night)*24*time.Hour))
		if night < 3 {
			if !firing.Skipped {
				t.Fatalf("firing on night %d = %+v, want the review still holding the cadence", night, firing)
			}
			continue
		}
		if firing.Skipped {
			t.Fatalf("firing on night %d = %+v, want the schedule to fire over an abandoned review",
				night, firing)
		}
		if !firing.WorkflowRun {
			t.Errorf("firing on night %d = %+v, want a recorded workflow run", night, firing)
		}
		if firing.OpenReview != review {
			t.Errorf("openReview = %q, want the abandoned review %s named", firing.OpenReview, review)
		}
	}

	// The firing really happened: there is a fresh unreviewed row for
	// the reconciler to start a run for, and the cadence has moved on.
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("GetScheduledWorkflowRun = %v, %v, want tonight's firing recorded", row, err)
	}
	if row.Settled() || !row.FiredAt.Equal(firstNight.Add(72*time.Hour)) {
		t.Errorf("row = %+v, want an unreviewed firing of the third night", row)
	}
	after, err := store.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("re-reading the schedule: %v", err)
	}
	if !after.NextRunAt.After(firstNight.Add(72 * time.Hour)) {
		t.Errorf("nextRunAt = %s, want the cadence moved past the firing that just happened", after.NextRunAt)
	}

	// And it is reported rather than merely worked around: the
	// abandoned review says so in its own conversation, and is left
	// open, because deciding a report is stale is not deciding it was
	// unimportant.
	comments := commentsOn(t, store, ctx, review)
	if len(comments) != 1 {
		t.Fatalf("review has %d comments, want the one saying grain fired anyway", len(comments))
	}
	if comments[0].Author.Actor != model.SchedulePrincipal {
		t.Errorf("note author = %+v, want the schedule itself", comments[0].Author.Actor)
	}
	for _, want := range []string{"stopped waiting", "nightly", "still open"} {
		if !strings.Contains(comments[0].Body, want) {
			t.Errorf("note does not say %q: %s", want, comments[0].Body)
		}
	}
	state, err := store.State(ctx, review)
	if err != nil {
		t.Fatalf("reading the review's state: %v", err)
	}
	if state == model.StateClosed {
		t.Error("the abandoned review was closed; grain fires over one, it does not tidy it away")
	}
}

// A review closed before the hold runs out leaves no note behind: the
// cadence picks up the moment the report has been read, which is what
// the overlap rule is for in the first place.
func TestAWorkflowScheduleFiresQuietlyOnceItsReviewIsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sched := nightlySchedule(t, store, ctx)

	fireNightly(t, store, ctx, sched.ID, firstNight)
	review := fileReview(t, store, ctx, sched, firstNight.Add(3*time.Hour))
	closed := firstNight.Add(8 * time.Hour)
	if err := store.ObserveField(ctx, review, closed, func(o *model.Observation) {
		o.ClosedAt = &closed
	}); err != nil {
		t.Fatalf("closing the review: %v", err)
	}

	firing := fireNightly(t, store, ctx, sched.ID, firstNight.Add(24*time.Hour))
	if firing.Skipped || !firing.WorkflowRun {
		t.Fatalf("firing = %+v, want the next night to fire normally", firing)
	}
	if firing.OpenReview != "" {
		t.Errorf("openReview = %q, want nothing reported for a review that was read", firing.OpenReview)
	}
	if got := commentsOn(t, store, ctx, review); len(got) != 0 {
		t.Errorf("closed review has %d comments, want none", len(got))
	}
}
