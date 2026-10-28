package orchestrator_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A schedule firing an interactive template files an interactive task
// (model.Template.Interactive): nobody is watching when it fires, so the
// task's first chat parks it until somebody replies, the same as any
// other interactive task. The firing is still approved -- the schedule
// was the approval -- and keeps the template's network access.
func TestReconcileScheduleFiresAnInteractiveTemplateAsAnInteractiveTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	tmpl := model.Template{
		ID:            "template-1",
		Name:          "GKE cluster",
		Title:         "Set up a GKE cluster for grain",
		Body:          "Ask which project, then set it up.",
		Interactive:   true,
		NetworkAccess: model.NetworkOpen,
	}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	templateID := tmpl.ID
	sched := model.Schedule{
		ID:         "sched-1",
		TemplateID: &templateID,
		Target:     model.RepoRef{Owner: "acme", Name: "widgets"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
		Enabled:    true,
		NextRunAt:  baseTime.Add(-time.Minute),
		CreatedAt:  baseTime,
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}

	if err := runScheduleOnly(t, store, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks, want 1", len(filed))
	}
	task := filed[0]
	if !task.Interactive {
		t.Error("the filed task is not interactive")
	}
	if task.Approval == nil {
		t.Error("the filed task is not approved")
	}
	if task.NetworkAccess != model.NetworkOpen {
		t.Errorf("NetworkAccess = %q, want the template's %q", task.NetworkAccess, model.NetworkOpen)
	}
}
