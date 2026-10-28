package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// A red the base-branch reconciler confirms wakes the pipeline steward in
// the same cycle, rather than leaving it for the next half-hourly firing
// -- and the daemon files no repair of its own first: what to do about
// the red is the steward's to decide.
func TestAConfirmedRedBaseBranchWakesThePipelineSteward(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	id := model.PipelineStewardTemplateID
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: id, TemplateID: &id,
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 30},
		Overlap:    model.OverlapSkip, Enabled: true,
		NextRunAt: baseTime.Add(29 * time.Minute), CreatedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	// A failure with no jobs to re-run is confirmed as it stands, in one
	// cycle (TestAFailureWithNoJobsToRerunIsConfirmedAsItStands).
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, Conclusion: model.BaseBranchCIFailure,
		Detail: "grain CI could not run: .grain/workflows/tests.yml: yaml: line 4: did not find expected key",
	}); err != nil {
		t.Fatal(err)
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || !row.Confirmed() || row.RepairTaskID != "" {
		t.Fatalf("main = %+v (%v), want a confirmed red with no repair filed by the daemon", row, err)
	}
	steward, err := store.OpenTaskWithTag(ctx, model.ScheduleFiringTag(id))
	if err != nil {
		t.Fatal(err)
	}
	if steward == nil {
		t.Fatal("a confirmed red did not wake the pipeline steward")
	}
	comments, err := store.Comments(ctx, steward.ID)
	if err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, c := range comments {
		said = said || strings.Contains(c.Body, "acme/widgets main is confirmed red")
	}
	if !said {
		t.Errorf("the pipeline steward was not told which branch is red: %+v", comments)
	}
	if sched, _ := store.GetSchedule(ctx, id); !sched.NextRunAt.Equal(baseTime.Add(29 * time.Minute)) {
		t.Errorf("next run moved to %s; a wake on red is an extra firing", sched.NextRunAt)
	}
}
