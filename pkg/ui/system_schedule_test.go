package ui_test

// The schedule grain seeds for itself, from the UI's side: a deployment
// may say when it runs and whether it runs, and may not say what it
// does. What it does ships with grain, and the row is only a row.
//
// The seeding itself is orchestrator's (reconcileSystemSchedules); these
// write the row the way that pass does and then drive the API against
// it.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// seedSystemSchedule is the row orchestrator's own seeding pass writes,
// written here directly: pkg/ui cannot import pkg/orchestrator (a
// presentation-layer package importing core dispatch logic runs the
// wrong way), and what these tests are about is the row, not how it got
// there.
func seedSystemSchedule(t *testing.T, store *model.Store, ctx context.Context) model.Schedule {
	t.Helper()
	id := model.InfraReviewTemplateID
	tmpl := model.SystemTemplate(id, model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatalf("this build ships no template for %s", id)
	}
	sched := model.Schedule{
		ID:         id,
		TemplateID: &id,
		Title:      tmpl.Title,
		Body:       tmpl.Body,
		Grants:     tmpl.Grants,
		Reads:      tmpl.Reads,
		// The row caches the system template's network like every other
		// content column
		// (orchestrator.applySystemContent).
		NetworkAccess: tmpl.NetworkAccess,
		Recurrence:    model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 3 * 60},
		Overlap:       model.OverlapSkip,
		Enabled:       true,
		NextRunAt:     baseTime.Add(11 * time.Hour),
		CreatedAt:     baseTime,
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("seeding the system schedule: %v", err)
	}
	return sched
}

func TestASystemScheduleListsUnderTheTemplateThisBuildShips(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	seedSystemSchedule(t, store, ctx)

	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("%d schedules, want the seeded one", len(list))
	}
	got := list[0]
	if got.TemplateID != model.InfraReviewTemplateID {
		t.Errorf("templateId = %q, want the system template's own id", got.TemplateID)
	}
	// The name comes from this build rather than from a `template` row,
	// which there is none of -- a schedules list that could not name what
	// it fires would show a job nobody could identify.
	if got.TemplateName != "Infrastructure review" {
		t.Errorf("templateName = %q, want the name this build gives it", got.TemplateName)
	}
	if got.Repo != "" {
		t.Errorf("repo = %q, want none rather than the %q a zero repo spells", got.Repo, "/")
	}
	// And it says out loud that it is one of grain's, so a pane can draw
	// the row's content as grain's own rather than as a prompt somebody
	// here is invited to rewrite. What it says is a cache of a template
	// that ships in code (orchestrator.resyncSystemSchedule keeps it
	// honest); editing it changes nothing about what fires, and finding
	// that out from a 400 is finding it out too late.
	if !got.System {
		t.Error("system = false for the schedule grain seeds for itself; nothing downstream can tell it apart")
	}

	ordinary, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Nightly dependency bump", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating an ordinary schedule: %v", err)
	}
	if ordinary.System {
		t.Error("system = true for a schedule somebody here wrote")
	}
}

func TestASystemSchedulesCadenceOverlapAndSwitchAreTheDeploymentsOwn(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched := seedSystemSchedule(t, store, ctx)

	off := false
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		Recurrence: &ui.Recurrence{Kind: "weekly", Weekday: "sunday", TimeOfDay: "04:00"},
		Overlap:    strPtr("allow"),
		Enabled:    &off,
		// The three fields the schedule overlay resends on every save,
		// unchanged. A guard that refused them on presence alone would
		// leave a deployment unable to edit the cadence at all from the
		// one pane that edits cadences.
		TemplateID: strPtr(sched.ID),
		Repo:       strPtr(""),
		Base:       strPtr(""),
	})
	if err != nil {
		t.Fatalf("editing a system schedule's cadence: %v", err)
	}
	if updated.Recurrence.Kind != "weekly" || updated.Recurrence.Weekday != "sunday" {
		t.Errorf("recurrence = %+v, want the weekly cadence this deployment chose", updated.Recurrence)
	}
	if updated.Overlap != "allow" {
		t.Errorf("overlap = %q, want allow", updated.Overlap)
	}
	if updated.Enabled {
		t.Error("the schedule is still enabled; disabling it is the switch a deployment has")
	}
}

func TestASystemSchedulesContentIsNotTheDeploymentsToChange(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched := seedSystemSchedule(t, store, ctx)

	yes := true
	network := model.NetworkNone
	if sched.NetworkAccess == network {
		network = model.NetworkOpen
	}
	for name, req := range map[string]ui.UpdateScheduleRequest{
		"its title":        {Title: strPtr("Something else")},
		"its prompt":       {Description: strPtr("write down whatever you like")},
		"its capabilities": {Capabilities: &[]string{"self-repair"}},
		"its reads":        {Reads: &[]string{"acme/widgets"}},
		"its target":       {Repo: strPtr("acme/widgets")},
		"its auto-merge":   {AutoMerge: &yes},
		"its network":      {NetworkAccess: strPtr(string(network))},
		"what it fires":    {TemplateID: strPtr("")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.UpdateSchedule(ctx, sched.ID, req)
			if err == nil {
				t.Fatalf("editing %s was allowed; what one of grain's own jobs does ships with grain", name)
			}
			var invalid *ui.ValidationError
			if !errors.As(err, &invalid) {
				t.Errorf("error = %v (%T), want a validation error the API answers 400 to", err, err)
			}
			if !strings.Contains(err.Error(), "how often it runs") {
				t.Errorf("error = %q, want it to say what the deployment *can* change", err)
			}
		})
	}

	// And nothing was quietly written on the way to refusing.
	after, err := store.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if after.Title != sched.Title || after.Body != sched.Body {
		t.Error("a refused edit changed the row anyway")
	}
}

// Nobody builds a second schedule out of one of grain's own templates:
// grain seeds the one schedule that fires each, and a second would run
// the same standing job twice.
func TestGrainsOwnTemplatesCannotBeScheduledByHand(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: model.InfraReviewTemplateID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err == nil {
		t.Fatal("creating a second schedule for a system template was allowed")
	}
	if !strings.Contains(err.Error(), "grain seeds the schedule that fires it") {
		t.Errorf("error = %q, want it to say why not", err)
	}

	// And an ordinary schedule cannot be repointed at one either.
	ordinary, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Nightly dependency bump", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating an ordinary schedule: %v", err)
	}
	if _, err := c.UpdateSchedule(ctx, ordinary.ID, ui.UpdateScheduleRequest{
		TemplateID: strPtr(model.InfraReviewTemplateID),
	}); err == nil {
		t.Fatal("repointing an ordinary schedule at a system template was allowed")
	}
	if after, err := store.GetSchedule(ctx, ordinary.ID); err != nil || after.TemplateID != nil {
		t.Errorf("the ordinary schedule now fires %v (err %v)", after.TemplateID, err)
	}
}

func TestASystemScheduleIsDisabledRatherThanDeleted(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched := seedSystemSchedule(t, store, ctx)

	err := c.DeleteSchedule(ctx, sched.ID)
	if err == nil {
		t.Fatal("deleting a system schedule was allowed; the seeding pass would bring it straight back")
	}
	if !strings.Contains(err.Error(), "Disable it instead") {
		t.Errorf("error = %q, want it to name the switch that actually means it", err)
	}
	if still, err := store.GetSchedule(ctx, sched.ID); err != nil || still == nil {
		t.Errorf("the row is gone after a refused delete (err %v)", err)
	}

	// An ordinary schedule is still deleted, refusal or no refusal.
	ordinary, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Nightly dependency bump", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating an ordinary schedule: %v", err)
	}
	if err := c.DeleteSchedule(ctx, ordinary.ID); err != nil {
		t.Fatalf("deleting an ordinary schedule: %v", err)
	}
}

// "Run now" on a system schedule fires this build's template, filled in
// with the state repository this deployment was wired with -- the one
// thing about a system template that differs per deployment, and the one
// this UI would silently drop if nobody had passed it down.
func TestRunNowOnASystemScheduleFiresWithThisDeploymentsStateRepository(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	stateRepo := model.RepoRef{Owner: "bwsalmon", Name: "grain-home"}
	c.Config.StateRepoRef = func() model.RepoRef { return stateRepo }
	sched := seedSystemSchedule(t, store, ctx)

	run, err := c.RunScheduleNow(ctx, sched.ID)
	if err != nil {
		t.Fatalf("RunScheduleNow: %v", err)
	}
	if run.TaskID == "" {
		t.Fatal("running a system schedule by hand filed nothing")
	}
	task, err := store.GetTask(ctx, run.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(task.Reads) != 1 || task.Reads[0] != stateRepo {
		t.Errorf("reads = %v, want %s -- the infrastructure review reads its state repository", task.Reads, stateRepo)
	}
	if task.Target != nil {
		t.Errorf("target = %s, want none: what it produces is a report, not a branch", task.Target)
	}
}
