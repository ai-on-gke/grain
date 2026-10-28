package model_test

// A schedule file may run one of the repository's own workflows on a
// branch it names (`workflow:` with `base:`, grain/task-3159) -- the same
// suite a workflow's cron runs on the default branch, run on a release
// candidate as well. It fires down the same path the cron does: the
// firing records a run owed against that branch, and the review is filed
// on that branch and says so in its title, so two mornings' reviews of
// one workflow can be told apart without opening either.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestAWorkflowScheduleOnABranchRunsAndIsReviewedThere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	templateID := model.WorkflowReviewTemplateID("nightly")
	sched := model.Schedule{
		ID:         model.RepoDeclaredID(model.DeclaredSchedule, nightlyRepo, "nightly-candidate"),
		Title:      `Review last night's "nightly" workflow run on candidate`,
		Target:     nightlyRepo,
		Base:       "candidate",
		TemplateID: &templateID,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 14*60 + 11},
		Overlap:    model.OverlapSkip,
		Enabled:    true,
		NextRunAt:  firstNight,
		CreatedAt:  firstNight.Add(-24 * time.Hour),

		AutoApproveProposals: true,
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("writing the schedule: %v", err)
	}

	firing := fireNightly(t, store, ctx, sched.ID, firstNight)
	if !firing.WorkflowRun || firing.TaskID != "" {
		t.Fatalf("firing = %+v, want a recorded workflow run and no task filed yet", firing)
	}
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("reading the firing: %v, %v", row, err)
	}
	if row.Stem != "nightly" || row.Branch != "candidate" || row.Repo != nightlyRepo {
		t.Fatalf("firing recorded %+v, want the nightly workflow owed on candidate", *row)
	}

	row.Conclusion = "success"
	reviewID, filed, err := store.FileWorkflowReview(ctx, sched, *row, model.RepoRef{}, firstNight.Add(3*time.Hour))
	if err != nil || !filed {
		t.Fatalf("filing the review: filed %v, %v", filed, err)
	}
	review, err := store.GetTask(ctx, reviewID)
	if err != nil || review == nil {
		t.Fatalf("reading the review: %v, %v", review, err)
	}
	if want := `Review last night's "nightly" workflow run on candidate`; review.Title != want {
		t.Errorf("review title %q, want %q", review.Title, want)
	}
	if review.Base != "candidate" {
		t.Errorf("review base %q, want candidate -- the reviewer reads the tree the run ran", review.Base)
	}
	if review.Target == nil || *review.Target != nightlyRepo {
		t.Errorf("review target %v, want %v", review.Target, nightlyRepo)
	}
}

// The default branch's own review keeps the title it has always had: a
// cron names no branch, so there is nothing to add to it.
func TestAWorkflowReviewWithNoBaseIsNotRetitled(t *testing.T) {
	templateID := model.WorkflowReviewTemplateID("nightly")
	tmpl := model.SystemTemplate(templateID, model.RepoRef{})
	if tmpl == nil {
		t.Fatalf("no system template %s", templateID)
	}
	for base, want := range map[string]string{
		"":          `Review last night's "nightly" workflow run`,
		"candidate": `Review last night's "nightly" workflow run on candidate`,
	} {
		comp := model.ComposeScheduleTask(model.Schedule{
			Target:     nightlyRepo,
			Base:       base,
			TemplateID: &templateID,
		}, tmpl)
		if comp.Title != want {
			t.Errorf("base %q: title %q, want %q", base, comp.Title, want)
		}
	}
}
