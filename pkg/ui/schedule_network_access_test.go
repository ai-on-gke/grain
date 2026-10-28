package ui_test

// grain/task-427: a schedule carrying its own inline content can say how
// much network the tasks it files get, the way a template already could.
//
// task_network_access_test.go beside this one is the same setting one
// layer down, and the difference between the two is deliberate and
// tested here: a task is refused "none" on a deployment whose sandboxes
// are directories on the host, because it is about to be dispatched into
// one; a schedule is not, for the reason a template is not -- it is a
// standing declaration that outlives this deployment's backend, and its
// firing is where that question gets asked.

import (
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateScheduleCarriesItsOwnNetworkAccess(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Sweep the record", Repo: "acme/widgets", Recurrence: everyDay,
		NetworkAccess: string(model.NetworkNone),
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	if sched.NetworkAccess != string(model.NetworkNone) {
		t.Errorf("the response says networkAccess %q, want %q", sched.NetworkAccess, model.NetworkNone)
	}
	stored, err := store.GetSchedule(ctx, sched.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NetworkAccess != model.NetworkNone {
		t.Errorf("the stored schedule says %q, want %q", stored.NetworkAccess, model.NetworkNone)
	}

	// And what it composes: the whole point of the field is that the
	// task this schedule files carries it, said by the schedule rather
	// than left to the deployment (docs/composition.md's table).
	comps, err := store.ScheduleComposition(ctx, *stored, model.RepoRef{})
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if len(comps) != 1 {
		t.Fatalf("composed %d tasks, want 1", len(comps))
	}
	if comps[0].NetworkAccess != model.NetworkNone {
		t.Errorf("composed networkAccess = %q, want %q", comps[0].NetworkAccess, model.NetworkNone)
	}
	if comps[0].Sources.NetworkAccess != model.LayerSchedule {
		t.Errorf("network decided by %q, want the schedule", comps[0].Sources.NetworkAccess)
	}
}

func TestCreateScheduleRefusesAWordThatNamesNoNetwork(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay, NetworkAccess: "airgapped",
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if !strings.Contains(err.Error(), string(model.NetworkNone)) {
		t.Errorf("error = %q, want it to name the vocabulary that was on offer", err)
	}
	list, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("stored %d schedules, want none", len(list))
	}
}

// A schedule is offered "none" whatever this deployment's sandboxes are,
// unlike a task filed with it (Client.refuseIgnoredNetworkAccess) --
// CreateScheduleRequest.NetworkAccess's own doc comment has the why.
func TestCreateScheduleAllowsNoNetworkWhereTheBackendHasNone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxShapeIgnored = true

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
		NetworkAccess: string(model.NetworkNone),
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	if sched.NetworkAccess != string(model.NetworkNone) {
		t.Errorf("networkAccess = %q, want %q", sched.NetworkAccess, model.NetworkNone)
	}
}

func TestUpdateScheduleEditsAndClearsTheNetworkAccess(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}

	none := string(model.NetworkNone)
	if _, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{NetworkAccess: &none}); err != nil {
		t.Fatalf("setting: %v", err)
	}
	stored, err := store.GetSchedule(ctx, sched.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NetworkAccess != model.NetworkNone {
		t.Fatalf("stored networkAccess = %q, want %q", stored.NetworkAccess, model.NetworkNone)
	}

	// "" is a value here, not "leave alone": back to the deployment's
	// own answer, the same reading UpdateTaskRequest gives it.
	cleared := ""
	if _, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{NetworkAccess: &cleared}); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	stored, err = store.GetSchedule(ctx, sched.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NetworkAccess != "" {
		t.Fatalf("stored networkAccess = %q after clearing, want empty", stored.NetworkAccess)
	}

	bogus := "airgapped"
	if _, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{NetworkAccess: &bogus}); err == nil {
		t.Error("want a word that names no network refused on the way in")
	}
}

// Content, so a template attached to a schedule decides it and the
// schedule's own column becomes a display cache of the template's --
// exactly as the title and the capabilities beside it already do.
func TestUpdateScheduleAttachToATemplateTakesItsNetworkAccess(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
		NetworkAccess: string(model.NetworkOpen),
	})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Memory sweep", Title: "Sweep", NetworkAccess: string(model.NetworkNone),
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{TemplateID: &tmpl.ID})
	if err != nil {
		t.Fatalf("attaching: %v", err)
	}
	if updated.NetworkAccess != string(model.NetworkNone) {
		t.Errorf("networkAccess = %q, want the template's %q", updated.NetworkAccess, model.NetworkNone)
	}
	stored, err := store.GetSchedule(ctx, sched.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	comps, err := store.ScheduleComposition(ctx, *stored, model.RepoRef{})
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if comps[0].NetworkAccess != model.NetworkNone ||
		comps[0].Sources.NetworkAccess != model.LayerTemplate {
		t.Errorf("composed network = %q from %q, want %q from the template",
			comps[0].NetworkAccess, comps[0].Sources.NetworkAccess, model.NetworkNone)
	}
}
