package ui_test

// grain/task-269: a task's own network-access override, from the API's
// side -- what is accepted, what is refused, and where it is refused.
//
// The shape of this follows task_sandbox_shape_test.go beside it,
// because it is the same problem: a value only a VM backend can honour,
// stated by somebody filing a task on a deployment that may not have
// one. The answer is the same too -- refuse it here, where the person
// stating it is, rather than at sandbox setup where only the run's log
// would say so.

import (
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateTaskRefusesAWordThatNamesNoNetwork(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "t", NetworkAccess: "airgapped"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if !strings.Contains(err.Error(), string(model.NetworkNone)) {
		t.Errorf("error = %q, want it to name the vocabulary that was on offer", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("stored %d tasks, want none", len(tasks))
	}
}

func TestCreateTaskCarriesTheNetworkOverrideOntoTheTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "read the repo and write memory", NetworkAccess: string(model.NetworkNone),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.NetworkAccess != string(model.NetworkNone) {
		t.Errorf("the response says networkAccess %q, want %q", task.NetworkAccess, model.NetworkNone)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NetworkAccess != model.NetworkNone {
		t.Errorf("the stored task says %q, want %q", stored.NetworkAccess, model.NetworkNone)
	}
}

// "none" on a deployment whose sandboxes are directories on this host is
// refused, for the reason a per-task shape is: the task would be filed,
// queued, dispatched and then fail at sandbox setup
// (orchestrator.HostSandboxes.Acquire), and the person who asked for it
// would hear about it in a run's log.
func TestCreateTaskRefusesNoNetworkWhereTheBackendHasNone(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	c.Config.SandboxShapeIgnored = true

	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "t", NetworkAccess: string(model.NetworkNone)})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if !strings.Contains(err.Error(), "networkAccess") {
		t.Errorf("error = %q, want it to name the field somebody has to change", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("stored %d tasks, want none: a refused override must file nothing", len(tasks))
	}
}

// ...and "open" is not refused there, because it is what such a sandbox
// has: the backend narrows the vocabulary rather than closing the field.
func TestCreateTaskAllowsTheOrdinaryNetworkWhereTheBackendHasNone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxShapeIgnored = true

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, NetworkAccess: string(model.NetworkOpen),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.NetworkAccess != string(model.NetworkOpen) {
		t.Errorf("networkAccess = %q, want %q", task.NetworkAccess, model.NetworkOpen)
	}
}

// An edit can set it, and can clear it back to "use the deployment's
// own" -- including on a deployment whose backend could not honour the
// value the task is carrying, which is the one edit that must always be
// possible.
func TestUpdateTaskEditsTheNetworkOverride(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	none := string(model.NetworkNone)
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{NetworkAccess: &none}); err != nil {
		t.Fatalf("setting networkAccess: %v", err)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NetworkAccess != model.NetworkNone {
		t.Fatalf("stored networkAccess = %q, want %q", stored.NetworkAccess, model.NetworkNone)
	}

	c.Config.SandboxShapeIgnored = true
	cleared := ""
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{NetworkAccess: &cleared}); err != nil {
		t.Fatalf("clearing networkAccess on a backend that cannot honour it: %v", err)
	}
	stored, err = store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NetworkAccess != "" {
		t.Fatalf("stored networkAccess = %q after clearing, want empty", stored.NetworkAccess)
	}
}

// The deployment-wide setting round-trips, and is reported resolved: a
// pane shows the network sandboxes actually get, so an unset setting
// reads as "open" rather than as a blank that could mean anything.
func TestSettingsReportTheNetworkSandboxesActuallyGet(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.NetworkAccess != string(model.NetworkOpen) {
		t.Errorf("NetworkAccess = %q for a deployment that has never chosen, want %q",
			got.NetworkAccess, model.NetworkOpen)
	}
	if len(got.NetworkAccesses) != len(model.NetworkAccesses()) {
		t.Errorf("NetworkAccesses = %v, want grain's own vocabulary", got.NetworkAccesses)
	}

	none := string(model.NetworkNone)
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{NetworkAccess: &none})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if saved.NetworkAccess != none {
		t.Errorf("NetworkAccess = %q after saving %q", saved.NetworkAccess, none)
	}

	bad := "airgapped"
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{NetworkAccess: &bad}); err == nil {
		t.Error("UpdateSettings accepted a word that names no network")
	}
}
