package ui_test

// POST /api/tasks/{id}/activity: the hop a dispatched run's own mcpserver
// makes to say what it is doing while it is still doing it (the
// update_status tool, pkg/mcp). Unlike the two hops beside it there is
// nothing behind this one to stand a fake in for -- the write lands in
// the store the daemon is already holding -- so these tests drive a real
// store, start a real run on it, and assert on what a task then reads
// back as.

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// running puts a live run on a task, which is the only state in which a
// status means anything.
func running(t *testing.T, store *model.Store, ctx context.Context, taskID string) {
	t.Helper()
	if err := store.StartRun(ctx, model.Run{
		ID: "run-" + taskID, TaskID: taskID, Sandbox: "sandbox-" + taskID,
		Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run on %s: %v", taskID, err)
	}
}

// The whole point: a run says what it is doing, and the task the list
// renders says it back -- with the moment it was said, since a phrase
// with no age cannot be told from a stale one.
func TestSetTaskActivityShowsOnTheTask(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	activity, err := client.SetTaskActivity(ctx, task.ID, "waiting for CI on the second push")
	if err != nil {
		t.Fatalf("SetTaskActivity: %v", err)
	}
	if !activity.Live || activity.Note != "waiting for CI on the second push" || activity.At == nil {
		t.Fatalf("activity = %+v, want it recorded against a live run", activity)
	}

	got, err := client.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Activity != "waiting for CI on the second push" || got.ActivityAt == nil {
		t.Errorf("task = %+v, want the synopsis and its time on the task", got)
	}

	// And on the list, which is where it is actually read: one query
	// covers every task, so a task with a synopsis has to come back with
	// it there too rather than only from its own page.
	list, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Activity != "waiting for CI on the second push" {
		t.Errorf("ListTasks = %+v, want the synopsis on the listed task", list)
	}

	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Activity != "waiting for CI on the second push" {
		t.Errorf("GetTask = %+v, want the synopsis on the detail shape too", detail.Task)
	}
}

// It is a synopsis, not a log: each one replaces the last.
func TestSetTaskActivityReplacesTheLastOne(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	if _, err := client.SetTaskActivity(ctx, task.ID, "reading the dispatch path"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetTaskActivity(ctx, task.ID, "running the test suite"); err != nil {
		t.Fatal(err)
	}
	got, err := client.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Activity != "running the test suite" {
		t.Errorf("activity = %q, want the most recent one", got.Activity)
	}
}

// The note is shown on one line, so it is stored as one -- a caller that
// sends several gets them joined rather than the second one silently cut
// off.
func TestSetTaskActivityFlattensTheNote(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	activity, err := client.SetTaskActivity(ctx, task.ID, "  running the tests\n\nfor pkg/ui ")
	if err != nil {
		t.Fatal(err)
	}
	if activity.Note != "running the tests for pkg/ui" {
		t.Errorf("note = %q, want it flattened onto one line", activity.Note)
	}
}

// Two refusals, both the caller's own mistake and so both a 400 rather
// than a 500 (ValidationError): nothing to show, and too much to show.
func TestSetTaskActivityRefusesNotesItCannotShow(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	for _, tt := range []struct {
		name string
		note string
		want string
	}{
		{"empty", "   \n ", "cannot be empty"},
		{"too long", strings.Repeat("x", ui.MaxActivityLength+1), "at most"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := client.SetTaskActivity(ctx, task.ID, tt.note); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
	// Nothing was written by either attempt.
	if got, err := client.Task(ctx, task.ID); err != nil || got.Activity != "" {
		t.Errorf("task = %+v (%v), want no synopsis recorded", got, err)
	}
}

// A task whose run is over records nothing and says so, rather than
// failing: the call raced the end of the run, which is nobody's mistake.
// The task stops showing a synopsis at the same moment, since what it
// said is no longer what it is doing.
func TestSetTaskActivityAfterTheRunIsOver(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)
	if _, err := client.SetTaskActivity(ctx, task.ID, "waiting for CI"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "run-"+task.ID, baseTime.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	activity, err := client.SetTaskActivity(ctx, task.ID, "still going")
	if err != nil {
		t.Fatalf("SetTaskActivity after the run ended: %v", err)
	}
	if activity.Live {
		t.Error("activity.Live = true, want false: there was no run to record against")
	}
	if got, err := client.Task(ctx, task.ID); err != nil || got.Activity != "" {
		t.Errorf("task = %+v (%v), want no synopsis once the run is over", got, err)
	}
}

// A run's setup has a status too, and it is grain's own: the dispatch
// path writes through the same store while the sandbox is being built
// and the repo cloned (orchestrator.setupNotes), and a reader is told
// which of the two voices they are hearing. The row answers it -- a
// phrase standing before the agent ever started is grain's -- so it must
// reach every shape a person reads a task through, not just the list.
func TestActivityWrittenBeforeTheAgentStartedReadsAsGrainsOwn(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	if _, err := store.SetTaskActivity(ctx, task.ID, "cloning acme/widgets", baseTime); err != nil {
		t.Fatal(err)
	}

	got, err := client.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Activity != "cloning acme/widgets" || !got.ActivityBySetup {
		t.Errorf("task = %+v, want the setup's own phrase, marked as grain's", got)
	}
	list, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].ActivityBySetup {
		t.Errorf("ListTasks = %+v, want the same attribution on the listed task", list)
	}

	// Once the agent has started, everything on the row is the agent's:
	// grain clears its own last phrase in the same breath (RunDispatch),
	// so anything standing here is something the run wrote.
	if err := store.SetRunAgentStarted(ctx, "run-"+task.ID, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetTaskActivity(ctx, task.ID, "reading pkg/orchestrator"); err != nil {
		t.Fatal(err)
	}
	got, err = client.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Activity != "reading pkg/orchestrator" || got.ActivityBySetup {
		t.Errorf("task = %+v, want the agent's own phrase, unmarked", got)
	}
}

// A task id nothing was filed under is a 404, the same as every other
// route that names one -- so a misconfigured -task on an mcpserver reads
// as the mistake it is rather than as a status that went nowhere.
func TestSetTaskActivityForAnUnknownTask(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)

	_, err := client.SetTaskActivity(ctx, "nope", "waiting for CI")
	var nf *ui.NotFoundError
	if err == nil || !errors.As(err, &nf) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
}

// The same round trip an mcpserver actually makes: over HTTP, through
// the route, into the store and back onto the task.
func TestHTTPClientSetTaskActivityRoundTrip(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)
	remote := accessCodeClient(client, srv.URL)

	activity, err := remote.SetTaskActivity(ctx, task.ID, "waiting for CI on the second push")
	if err != nil {
		t.Fatalf("SetTaskActivity over HTTP: %v", err)
	}
	if !activity.Live || activity.Note != "waiting for CI on the second push" {
		t.Fatalf("activity = %+v, want the note recorded against the live run", activity)
	}

	tasks, err := remote.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Activity != "waiting for CI on the second push" {
		t.Fatalf("ListTasks = %+v, want the synopsis carried across the wire", tasks)
	}
}

// A refusal survives the wire with its message intact -- that message is
// what the agent reads and acts on.
func TestHTTPClientSetTaskActivityReportsARefusal(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)

	_, err := accessCodeClient(client, srv.URL).SetTaskActivity(ctx, task.ID,
		strings.Repeat("x", ui.MaxActivityLength+1))
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("err = %v, want the daemon's own refusal", err)
	}
}
