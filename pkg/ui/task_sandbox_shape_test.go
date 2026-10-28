package ui_test

// grain/task-33: a per-task sandbox shape on a deployment whose sandbox
// backend has none of its own (ui.Config.SandboxShapeIgnored) is refused
// at the moment somebody states it, rather than filed, queued,
// dispatched and then refused by orchestrator.HostSandboxes.Acquire in a
// run's log ("a host-directory sandbox has no shape of its own"). The
// new-task form already disables the three fields where the flag is set
// (grain/task-26); these are the callers that are not that form.

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateTaskRefusesSandboxShapeWhereTheBackendHasNone(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	c.Config.SandboxShapeIgnored = true

	for name, req := range map[string]ui.CreateTaskRequest{
		"cpus":   {Title: "t", SandboxCPUs: 4},
		"memory": {Title: "t", SandboxMemoryMB: 8192},
		"disk":   {Title: "t", SandboxDiskGB: 40},
		"all three": {
			Title: "t", SandboxCPUs: 4, SandboxMemoryMB: 8192, SandboxDiskGB: 40,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.CreateTask(ctx, req)
			var ve *ui.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
			// The refusal has to say what was refused, since it is all the
			// person filing the task sees -- the shape they asked for and
			// what to do instead.
			if !strings.Contains(err.Error(), "no shape of their own") ||
				!strings.Contains(err.Error(), "sandboxCpus") {
				t.Fatalf("error = %q, want it to name the backend and the fields", err)
			}
		})
	}

	// Nothing was filed by any of those: the whole point of refusing here
	// rather than at sandbox setup is that no task is left behind to die
	// later.
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("stored %d tasks, want none: a refused shape must file nothing", len(tasks))
	}
}

// The flag refuses a *shape*, not a task: filing without one is the
// ordinary case on such a deployment and must keep working.
func TestCreateTaskAllowsNoSandboxShapeWhereTheBackendHasNone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.SandboxShapeIgnored = true

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "t", Approved: true})
	if err != nil {
		t.Fatalf("creating a task with no sandbox shape: %v", err)
	}
	if task.SandboxCPUs != 0 || task.SandboxMemoryMB != 0 || task.SandboxDiskGB != 0 {
		t.Fatalf("sandbox shape = %d/%d/%d, want 0/0/0",
			task.SandboxCPUs, task.SandboxMemoryMB, task.SandboxDiskGB)
	}
}

// The same rule on PATCH: an edit cannot introduce a shape the backend
// would refuse either. Clearing one is always allowed, one field at a
// time -- otherwise a task filed before a deployment moved onto a
// backend with no shape could never be edited back into something that
// runs (refuseIgnoredSandboxShape's own doc comment).
func TestUpdateTaskSandboxShapeWhereTheBackendHasNone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	// Filed while the deployment still had a shaped backend, which is how
	// a stored override gets onto such a task in the first place.
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true,
		SandboxCPUs: 4, SandboxMemoryMB: 8192, SandboxDiskGB: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Config.SandboxShapeIgnored = true

	cpus := 2
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{SandboxCPUs: &cpus}); err != nil {
		var ve *ui.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("error = %v, want a ValidationError", err)
		}
	} else {
		t.Fatal("setting a sandbox shape was accepted, want a ValidationError")
	}

	zero := 0
	cleared, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{SandboxCPUs: &zero})
	if err != nil {
		t.Fatalf("clearing sandboxCpus alone: %v", err)
	}
	if cleared.SandboxCPUs != 0 || cleared.SandboxMemoryMB != 8192 {
		t.Fatalf("sandbox shape = %d/%d/%d, want 0/8192/40: clearing one field leaves the others",
			cleared.SandboxCPUs, cleared.SandboxMemoryMB, cleared.SandboxDiskGB)
	}
	cleared, err = c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{
		SandboxMemoryMB: &zero, SandboxDiskGB: &zero,
	})
	if err != nil {
		t.Fatalf("clearing the rest of the shape: %v", err)
	}
	if cleared.SandboxMemoryMB != 0 || cleared.SandboxDiskGB != 0 {
		t.Fatalf("sandbox shape = %d/%d/%d, want 0/0/0",
			cleared.SandboxCPUs, cleared.SandboxMemoryMB, cleared.SandboxDiskGB)
	}
}

// A deployment whose backend does have a shape is untouched: the flag is
// the only thing that refuses, and it defaults to off (a UI with no
// backend to speak for says nothing -- ui.Config.SandboxShapeIgnored).
func TestCreateTaskKeepsSandboxShapeWhereTheBackendHasOne(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, SandboxCPUs: 4, SandboxMemoryMB: 8192,
	})
	if err != nil {
		t.Fatalf("creating a task with a sandbox shape: %v", err)
	}
	if task.SandboxCPUs != 4 || task.SandboxMemoryMB != 8192 {
		t.Fatalf("sandbox shape = %d/%d, want 4/8192", task.SandboxCPUs, task.SandboxMemoryMB)
	}
}

// And over HTTP, which is the surface an ui.HTTPClient caller and any
// script actually reach: a 400 with the reason in it, rather than a 201
// and a run that dies later.
func TestCreateTaskOverHTTPRefusesSandboxShapeWhereTheBackendHasNone(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Config.SandboxShapeIgnored = true
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"fix it","approved":true,"sandboxCpus":4}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "no shape of their own") {
		t.Fatalf("body = %s, want the refusal's own reason", rec.Body)
	}
}
