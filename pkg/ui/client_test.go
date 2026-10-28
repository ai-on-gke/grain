package ui_test

// pkg/ui against a real embedded SQLite store, the same discipline every
// other package's own tests hold to (model/simulate_test.go: "Nothing
// here is a fake standing in for the store"). These used to run against
// an in-memory github.Client stand-in, because a task was a GitHub issue;
// there is no GitHub in this package at all now.

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

var baseTime = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

// admitUser once let user past an access gate that counted team members
// and system admins. Every person is the owner now, so it does nothing.
func admitUser(t *testing.T, store *model.Store, user string) {
	t.Helper()
}

func testClient(t *testing.T) (*ui.Client, *model.Store, context.Context) {
	t.Helper()
	// Nearly every test here starts with a client, testServer included,
	// so this is where they claim the log -- first, so that the release
	// it registers runs after the cleanup below and after anything that
	// waits for an upgrade's goroutine to finish. See log_test.go.
	ui.LogToTest(t)
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime }
	return client, store, ctx
}

// setDefaultTeamTargetRepos stores repos as the deployment's target repos.
func setDefaultTeamTargetRepos(t *testing.T, store *model.Store, ctx context.Context, repos []string) {
	t.Helper()
	// The target repos are a column of the settings row since schema 24.
	// A row written here for the first time carries a poll interval, so
	// it reads as a seeded row rather than a connections-only one
	// (model.Config.ConnectionsOnly).
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		if cfg.PollInterval == 0 {
			cfg.PollInterval = model.DefaultPollInterval
		}
		cfg.TargetRepos = repos
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// create is the common setup: an approved task, so it reads queued. It
// states no opinion about where in the backlog it goes, so it joins
// whichever end the deployment currently remembers -- createAt below is
// the same task with that choice made.
func create(t *testing.T, c *ui.Client, ctx context.Context) ui.Task {
	t.Helper()
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the thing", Description: "please", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating a task: %v", err)
	}
	return task
}

// createAt is create with grain/task-202's own choice stated: atFront
// true files the task ahead of everything already queued, false at the
// end behind it, and either way the deployment remembers it for the next
// task filed without one (ui.CreateTaskRequest.AtFront).
func createAt(t *testing.T, c *ui.Client, ctx context.Context, atFront bool) ui.Task {
	t.Helper()
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the thing", Description: "please", Approved: true, AtFront: &atFront,
	})
	if err != nil {
		t.Fatalf("creating a task at the %s of the backlog: %v", map[bool]string{true: "front", false: "end"}[atFront], err)
	}
	return task
}

// The point of the whole inversion: a task exists after one call, with no
// GitHub issue anywhere behind it.
func TestCreateTaskFilesStraightIntoTheStore(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task := create(t, c, ctx)
	if task.ID == "" {
		t.Fatal("created task has no id")
	}
	if task.State != model.StateQueued {
		t.Fatalf("state = %q, want queued: an approved task is dispatchable at once", task.State)
	}
	if task.Repo != "acme/widgets" {
		t.Fatalf("repo = %q, want the configured default target", task.Repo)
	}

	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Fatal("task is not in the store")
	}
	if stored.Origin.Attribution.Actor.ID != "alice" {
		t.Fatalf("origin actor = %q, want the configured actor", stored.Origin.Attribution.Actor.ID)
	}

	// Dispatchable means dispatchable: task_ready is the view
	// dispatch.Cycle drains, and nothing polled anything to get here.
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0] != task.ID {
		t.Fatalf("ready = %v, want just the new task", ready)
	}
}

func TestCreateTaskPreservesCreator(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:   "test creator",
		Creator: "bob",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.Creator != "bob" {
		t.Fatalf("task.Creator = %q, want bob", task.Creator)
	}

	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Creator != "bob" {
		t.Fatalf("stored.Creator = %q, want bob", stored.Creator)
	}

	readBack, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readBack.Creator != "bob" {
		t.Fatalf("readBack.Creator = %q, want bob", readBack.Creator)
	}
}

func TestQueuedTaskShowsWhichGrantResourceIsHoldingItsAdmission(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: task.ID, Capability: "gemini-key", Resource: "Gemini API keys", WaitingAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].State != model.StateQueued ||
		tasks[0].Activity != "waiting for Gemini API keys (gemini-key grant)" ||
		!tasks[0].ActivityBySetup || tasks[0].ActivityAt == nil || !tasks[0].ActivityAt.Equal(baseTime) {
		t.Fatalf("queued task = %+v, want its pre-run grant wait", tasks)
	}
}

// A task whose last run's sandbox was OOM-killed waits for its retry with
// a row that says so, in grain's voice, in the list and in its own read;
// the next run starting clears it.
func TestQueuedTaskShowsItIsRetryingAfterAnOOMKill(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.StartRun(ctx, model.Run{ID: "r1", TaskID: task.ID, Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	ended := baseTime.Add(time.Minute)
	if err := store.FinishRun(ctx, "r1", ended, model.OOMOutcome, model.OOMDetailPrefix); err != nil {
		t.Fatal(err)
	}
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].State != model.StateQueued || tasks[0].Activity != "retrying after OOM kill" ||
		!tasks[0].ActivityBySetup || tasks[0].ActivityAt == nil || !tasks[0].ActivityAt.Equal(ended) {
		t.Fatalf("queued task = %+v, want it retrying after the OOM kill", tasks)
	}
	one, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.Activity != "retrying after OOM kill" {
		t.Errorf("Task(%s).Activity = %q, want the OOM retry", task.ID, one.Activity)
	}
	if err := store.StartRun(ctx, model.Run{ID: "r2", TaskID: task.ID, Attempt: 2, StartedAt: ended.Add(time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r2", ended.Add(2*time.Minute), "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	if tasks, err = c.ListTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if tasks[0].Activity == "retrying after OOM kill" {
		t.Errorf("after a later run the row still reads %q", tasks[0].Activity)
	}
}

// A wait for the team's own setup is already a whole sentence naming the
// Team settings pane to open, so the task shows it as written rather
// than as a grant (docs/test-runs/2026-10-04-connections-and-models.md, F7).
func TestQueuedTaskShowsWhatItsTeamHasToSetUp(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	const reason = `team "gamma" has no GitHub credential for acme/widgets -- a team admin sets one in Team settings -> Git identity`
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: task.ID, Capability: model.TeamSetupWaitCapability, Resource: reason, WaitingAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Activity != "waiting: "+reason || !detail.ActivityBySetup {
		t.Fatalf("detail activity = %q (by setup %v), want %q", detail.Activity, detail.ActivityBySetup, "waiting: "+reason)
	}
}

func TestQueuedTaskShowsQuotaWaitAndClearsOnStart(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	const reason = "sandbox CPU budget exhausted (8/8 vCPUs in use)"
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: task.ID, Capability: model.QuotaWaitCapability, Resource: reason, WaitingAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Activity != "waiting: "+reason || !tasks[0].ActivityBySetup {
		t.Fatalf("ListTasks activity = %+v, want %q with BySetup=true", tasks, "waiting: "+reason)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Activity != "waiting: "+reason || !detail.ActivityBySetup {
		t.Fatalf("GetTask activity = %q (by setup %v), want %q", detail.Activity, detail.ActivityBySetup, "waiting: "+reason)
	}

	if err := store.StartRun(ctx, model.Run{
		ID: task.ID + "-1", TaskID: task.ID, Sandbox: "sb-1", Attempt: 1, StartedAt: baseTime.Add(time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if wait, err := store.GrantWaitOf(ctx, task.ID); err != nil || wait != nil {
		t.Fatalf("GrantWaitOf after StartRun = %+v (%v), want nil", wait, err)
	}
}

// And the other half of that: a wait is a derived fact, so attaching the
// capability it named makes it false at once. Nothing dispatches here --
// no attempt starts and no cycle runs -- because the row is invalidated
// by the edit itself (model's invalidateStaleGrantWait), which is what a
// deployment with every worker busy depends on.
func TestAttachingTheMissingCapabilityStopsTheTaskSayingItIsWaiting(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: task.ID, Capability: "self-debug",
		Resource: "self-debug", WaitingAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Activity == "" {
		t.Fatal("detail says nothing while the task is parked for a missing grant")
	}

	if err := c.SetCapability(ctx, task.ID, "self-debug", true); err != nil {
		t.Fatalf("attaching self-debug: %v", err)
	}

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Activity != "" || tasks[0].ActivityAt != nil {
		t.Errorf("list activity after attaching self-debug = %+v, want none", tasks)
	}
	listed, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if listed.Activity != "" {
		t.Errorf("task activity after attaching self-debug = %q, want none", listed.Activity)
	}
	detail, err = c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Activity != "" {
		t.Errorf("detail activity after attaching self-debug = %q, want none", detail.Activity)
	}
}

func TestCreateTaskUnapprovedFilesAsAProposal(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "maybe do this"})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != model.StateProposed {
		t.Fatalf("state = %q, want proposed", task.State)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready = %v, want nothing: a proposal is not dispatchable", ready)
	}
}

// TestCreateTaskInteractiveDispatchesAtOnce is bwsalmon/agents#539's
// whole point: an interactive task is dispatchable the moment it is
// filed even though Approved was never set, because CreateTaskRequest.
// Interactive's own doc comment says a chat nobody has opened yet has
// nothing to show for itself.
func TestCreateTaskInteractiveDispatchesAtOnce(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "chat about this", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	if !task.Interactive {
		t.Fatal("task.Interactive = false, want true")
	}
	if task.State != model.StateQueued {
		t.Fatalf("state = %q, want queued: an interactive task is approved at once", task.State)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0] != task.ID {
		t.Fatalf("ready = %v, want just the new task", ready)
	}
}

// TestCreateTaskInteractiveJumpsTheQueue mirrors
// TestNewestFirstSettingMovesNewTasksToTheFrontOfTheQueue: an interactive
// task dispatches ahead of whatever is already queued, on top of
// whatever model.Config.NewestFirst says, since starting it is the whole
// reason it was filed.
func TestCreateTaskInteractiveJumpsTheQueue(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	first := create(t, c, ctx)
	second := create(t, c, ctx)
	chat, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "chat about this", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{chat.ID, first.ID, second.ID}
	if !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready = %v, want the interactive task dispatched first %v", ready, want)
	}
}

// A repo outside Config.TargetRepos is filed exactly as asked, but
// parked awaiting reply -- v1's "a task naming anything else is parked
// with a comment rather than dispatched" -- so it never reaches
// task_ready.
func TestCreateTaskOffTargetRepoListParksAwaitingReply(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "acme/gadgets"})

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix the other thing", Repo: "someone-else/unrelated", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating a task off the allowlist: %v", err)
	}
	if task.Repo != "someone-else/unrelated" {
		t.Fatalf("repo = %q, want the requested repo -- parking must not rewrite Target", task.Repo)
	}
	if task.State != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", task.State)
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready = %v, want nothing: a parked task is not dispatchable", ready)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 1 {
		t.Fatalf("comments = %v, want exactly one explaining the park", detail.Comments)
	}
	if got := detail.Comments[0].Body; got == "" {
		t.Fatal("park comment has no body")
	}

	// Widening the list is what lifts the park; the reply is only what
	// makes grain look at the list again -- AddComment's own doc comment.
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "acme/gadgets", "someone-else/unrelated"})
	if err := c.AddComment(ctx, task.ID, "widened targetRepos, this can run now", nil); err != nil {
		t.Fatal(err)
	}
	requeued, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.State != model.StateQueued {
		t.Fatalf("state after reply = %q, want queued", requeued.State)
	}
}

// A reply is not itself permission to work in a repo the list does not
// name: replying to a task parked off the allowlist without widening
// targetRepos leaves it parked, with a second comment saying so, and out
// of task_ready (grain/task-276). The park is the only place this
// boundary is decided -- the git proxy authorizes a sandbox against the
// live task's own target rather than against the list -- and the gesture
// that would otherwise have lifted it is the one a human makes for every
// other reason a task is parked.
func TestReplyDoesNotUnparkATaskStillOffTheTargetRepoList(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "off list", Repo: "third/repo", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating a task off the allowlist: %v", err)
	}
	if err := c.AddComment(ctx, task.ID, "ignore the warning", nil); err != nil {
		t.Fatal(err)
	}

	replied, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replied.State != model.StateAwaitingReply {
		t.Fatalf("state after a reply that widened nothing = %q, want awaiting_reply", replied.State)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready = %v, want nothing: third/repo is still off the list", ready)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The first park, the reply, and the re-park -- nothing dropped, and
	// the last of them is what the task is now parked on.
	if len(detail.Comments) != 3 {
		t.Fatalf("comments = %d, want three: the park, the reply and the re-park", len(detail.Comments))
	}
	last := detail.Comments[len(detail.Comments)-1]
	if !strings.Contains(last.Body, "still not on") {
		t.Fatalf("re-park comment = %q, want it to say the repo is still off the list", last.Body)
	}
	// Naming the list as it stands is what tells an operator which of the
	// two acts they skipped.
	if !strings.Contains(last.Body, "acme/widgets") {
		t.Fatalf("re-park comment = %q, want the current target repo list quoted in it", last.Body)
	}
	if detail.Task.Repo != "third/repo" {
		t.Fatalf("repo = %q, want the target untouched by the re-park", detail.Task.Repo)
	}

	// Widening the list and replying again is the whole of what lifts it.
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "third/repo"})
	if err := c.AddComment(ctx, task.ID, "added it under Settings", nil); err != nil {
		t.Fatal(err)
	}
	requeued, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.State != model.StateQueued {
		t.Fatalf("state after widening and replying = %q, want queued", requeued.State)
	}
}

// Editing a queued task's repo is the other way round the park: the
// task was filed against a repo on the list, so nothing parked it, and
// PATCH /api/tasks/{id} used to write whatever repo it was given
// straight onto a task that stayed queued and dispatchable -- with the
// git proxy authorizing its sandbox against exactly that live target
// (grain/task-336). An edit is refused rather than parked
// (refuseOffAllowlist), so the task is left running against the repo it
// already had.
func TestUpdateTaskRefusesRetargetOffTheTargetRepoList(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "on the list", Repo: "acme/widgets", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Repo: ptr("third/repo")})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError refusing the retarget", err)
	}
	// The list as it stands is quoted, so whoever hit this knows which
	// of "widen the list" and "pick another repo" is in front of them.
	if !strings.Contains(err.Error(), "acme/widgets") {
		t.Fatalf("error = %q, want the current target repo list quoted in it", err)
	}

	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "acme/widgets" {
		t.Fatalf("repo = %q, want the target untouched by a refused edit", got.Repo)
	}
	if got.State != model.StateQueued {
		t.Fatalf("state = %q, want queued: a refused edit must not park a task nobody objected to", got.State)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 {
		t.Fatalf("ready = %v, want the task still dispatchable against its own repo", ready)
	}

	// Widening the list is what lets the move through, exactly as it is
	// what lifts the park on a filing.
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "third/repo"})
	moved, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Repo: ptr("third/repo")})
	if err != nil {
		t.Fatalf("retargeting onto a repo now on the list: %v", err)
	}
	if moved.Repo != "third/repo" {
		t.Fatalf("repo = %q, want the edit applied", moved.Repo)
	}
}

// A task already parked off the list stays editable. The task pane saves
// the whole form, repo field included, so a request restating the repo
// the task already carries must not be read as a move onto it -- that is
// the one task somebody is most likely to be editing while it sits
// parked (grain/task-336).
func TestUpdateTaskRestatingAnOffListRepoStillEditsTheTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "off list", Repo: "third/repo", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != model.StateAwaitingReply {
		t.Fatalf("state = %q, want the filing parked", task.State)
	}

	// Spelled in another case on the way back in, since GitHub resolves
	// the two to one repository and RepoRef.Same is what decides whether
	// this edit moves anything.
	edited, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{
		Title: ptr("off list, described better"), Repo: ptr("Third/Repo"),
	})
	if err != nil {
		t.Fatalf("editing a task parked off the list: %v", err)
	}
	if edited.Title != "off list, described better" {
		t.Fatalf("title = %q, want the edit applied", edited.Title)
	}
	if edited.State != model.StateAwaitingReply {
		t.Fatalf("state = %q, want the park untouched by an unrelated edit", edited.State)
	}

	// Moving it on again, to a second repo the list does not name, is
	// still refused: the park is lifted by widening the list, not by
	// editing around it.
	_, err = c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Repo: ptr("fourth/repo")})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// The same re-check applies to a task parked on a run's own question
// while the list was narrowed underneath it: the reply answers the
// question, and the repo is still one this deployment may not work in,
// so the task parks on that instead of queueing.
func TestReplyToARunsQuestionReparksATargetSinceRemovedFromTheList(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "ask about it", Repo: "acme/widgets", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != model.StateQueued {
		t.Fatalf("state = %q, want queued: acme/widgets is on the list", task.State)
	}
	// What a run's ask_question leaves behind (orchestrator's own
	// relayComment): a comment, and the task pointed at it.
	commentID, err := c.Store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "which branch?", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store.ObserveField(ctx, task.ID, time.Now(), func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	}); err != nil {
		t.Fatal(err)
	}

	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/gadgets"})
	if err := c.AddComment(ctx, task.ID, "main", nil); err != nil {
		t.Fatal(err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply: acme/widgets is no longer a repo this deployment works in", got.State)
	}
}

// An empty Config.TargetRepos is v1's own "leave empty for a
// single-repo deployment": nothing is restricted.
func TestCreateTaskEmptyTargetRepoListAllowsAnyRepo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "fix it", Repo: "someone-else/unrelated", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != model.StateQueued {
		t.Fatalf("state = %q, want queued: an empty target repo list restricts nothing", task.State)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 {
		t.Fatalf("ready = %v, want just the new task", ready)
	}
}

func TestCreateTaskCarriesCapabilityGrants(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "needs a key", Approved: true, Capabilities: &[]string{"gemini-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Capabilities) != 1 || task.Capabilities[0] != "gemini-key" {
		t.Fatalf("capabilities = %v, want [gemini-key]", task.Capabilities)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Grants) != 1 || stored.Grants[0].Capability != "gemini-key" {
		t.Fatalf("grants = %+v, want one gemini-key grant", stored.Grants)
	}
}

func TestCreateTaskProposalAcceptsGrainCICapability(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	parent := create(t, c, ctx)
	proposal, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "proposed subtask",
		ProposedBy:   parent.ID,
		Capabilities: &[]string{"grain-ci", "gemini-key"},
	})
	if err != nil {
		t.Fatalf("CreateTask proposal: %v", err)
	}
	stored, err := store.GetTask(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasGemini := false
	for _, g := range stored.Grants {
		if g.Capability == "gemini-key" {
			hasGemini = true
		}
	}
	if !hasGemini {
		t.Fatalf("grants = %+v, want gemini-key grant", stored.Grants)
	}
}

// putDefaultCapabilities stores ids as this deployment's default
// capability set, the way an operator saving Settings would -- through a
// full model.Config, since PutConfig writes the row wholesale.
func putDefaultCapabilities(t *testing.T, ctx context.Context, store *model.Store, ids ...string) {
	t.Helper()
	cfg, _ := store.GetConfig(ctx)
	if cfg == nil {
		c := model.DefaultConfig()
		cfg = &c
	}
	cfg.DefaultCapabilities = ids
	if cfg.PollInterval == 0 {
		cfg.PollInterval = model.DefaultPollInterval
	}
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatalf("storing default capabilities %v: %v", ids, err)
	}
}

// grain/task-14: a deployment can say which capabilities every task
// filed on it starts out holding. The grant lands on the task itself, as
// GrantByDefault -- not as a deployment-level set read again at dispatch
// -- which is what makes it visible on the task and detachable from it.
func TestCreateTaskSeedsDeploymentDefaultCapabilities(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putDefaultCapabilities(t, ctx, store, "gcp-key")

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "needs a sandbox key", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(task.Capabilities, "gcp-key") {
		t.Fatalf("capabilities = %v, want gcp-key among them: the deployment defaults it", task.Capabilities)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Grants) != 1 || stored.Grants[0].Via != model.GrantByDefault {
		t.Fatalf("grants = %+v, want one gcp-key grant recorded as %q", stored.Grants, model.GrantByDefault)
	}
	// Detachable like any other grant: a default is a starting point, not
	// something the task is stuck with.
	if err := c.SetCapability(ctx, task.ID, "gcp-key", false); err != nil {
		t.Fatalf("detaching a defaulted capability: %v", err)
	}
	detached, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detached.Grants) != 0 {
		t.Fatalf("grants after detaching = %+v, want none", detached.Grants)
	}
}

// A request that names its own capabilities names all of them: the
// defaults seed a task nobody said anything about, and an empty-but-
// present list is somebody saying "none". The UI's own form always sends
// a list -- seeded from the defaults -- so unticking a box on it has to
// mean the task is filed without that capability.
func TestCreateTaskCapabilitiesOverrideDeploymentDefaults(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putDefaultCapabilities(t, ctx, store, "gcp-key")

	named, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "only this one", Approved: true, Capabilities: &[]string{"gemini-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(named.Capabilities, []string{"gemini-key"}) {
		t.Errorf("capabilities = %v, want [gemini-key] alone", named.Capabilities)
	}
	stored, err := store.GetTask(ctx, named.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Grants) != 1 || stored.Grants[0].Via != model.GrantByLabel {
		t.Errorf("grants = %+v, want one gemini-key grant recorded as %q", stored.Grants, model.GrantByLabel)
	}

	none, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "none at all", Approved: true, Capabilities: &[]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(none.Capabilities) != 0 {
		t.Errorf("capabilities = %v, want none: an empty list is a choice, not an absent one", none.Capabilities)
	}
}

// A stored default this build no longer offers is skipped, not fatal:
// UpdateSettings validates the set when it is saved, so an id with no
// row can only be one grain has retired since, and a settings row left
// behind by an upgrade must not become a deployment where no task can be
// filed at all ("scratch-repo" is exactly that -- task-10 renamed it to
// github-sandbox).
func TestCreateTaskSkipsRetiredDefaultCapability(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putDefaultCapabilities(t, ctx, store, "scratch-repo", "gemini-key")

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "still filable", Approved: true})
	if err != nil {
		t.Fatalf("filing a task on a deployment defaulting a retired capability: %v", err)
	}
	if !reflect.DeepEqual(task.Capabilities, []string{"gemini-key"}) {
		t.Fatalf("capabilities = %v, want [gemini-key]: the retired id is skipped, the rest still granted",
			task.Capabilities)
	}
}

// putRepoDefaultCapabilities stores ids as one repo's own default
// capability set, the way an operator saving the repos pane's own
// per-repo picker would.
func putRepoDefaultCapabilities(t *testing.T, ctx context.Context, store *model.Store, repo string, ids ...string) {
	t.Helper()
	parsed, err := model.ParseRepo(repo)
	if err != nil {
		t.Fatalf("parsing %q: %v", repo, err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: parsed, DefaultCapabilities: ids}); err != nil {
		t.Fatalf("storing repo default capabilities %v for %s: %v", ids, repo, err)
	}
}

// grain/task-24: a repo can default capabilities of its own, on top of
// whatever the deployment defaults. Union, deployment-wide first -- a
// repo adds and never subtracts -- and only for the repo the task
// actually targets.
func TestCreateTaskUnionsRepoDefaultCapabilities(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putDefaultCapabilities(t, ctx, store, "gemini-key")
	putRepoDefaultCapabilities(t, ctx, store, "acme/widgets", "gcp-key")

	here, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "on the repo that adds one", Approved: true, Repo: "acme/widgets",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Compared sorted: a task's capabilities come back in the order
	// Store.grantsOf reads them (by capability), not in the order they
	// were seeded.
	if !reflect.DeepEqual(here.Capabilities, []string{"gcp-key", "gemini-key"}) {
		t.Errorf("capabilities = %v, want both the deployment's gemini-key and the repo's gcp-key",
			here.Capabilities)
	}
	stored, err := store.GetTask(ctx, here.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range stored.Grants {
		if g.Via != model.GrantByDefault {
			t.Errorf("grant %+v: want every seeded grant recorded as %q", g, model.GrantByDefault)
		}
	}

	// A different repo gets the deployment's set alone -- the repo layer
	// is keyed on the target, not applied to everything once stored.
	elsewhere, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "on another repo", Approved: true, Repo: "acme/gadgets",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(elsewhere.Capabilities, []string{"gemini-key"}) {
		t.Errorf("capabilities = %v, want [gemini-key] alone on a repo with no defaults of its own",
			elsewhere.Capabilities)
	}
}

// A task filed with no repo named at all is filed against
// Config.DefaultTarget, and it is that repo's defaults it should get:
// the layer is keyed on the repo the task ends up targeting, not on
// whether the request happened to spell it out.
func TestCreateTaskUsesDefaultTargetRepoDefaultCapabilities(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putRepoDefaultCapabilities(t, ctx, store, "acme/widgets", "gcp-key")

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "no repo named", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(task.Capabilities, []string{"gcp-key"}) {
		t.Fatalf("capabilities = %v, want [gcp-key]: the default target repo's own defaults", task.Capabilities)
	}
}

// A repo-less task (NoRepo) has no repo to key the second layer on, so
// it gets the deployment's set and nothing else -- not the default
// target's, which it deliberately is not filed against.
func TestCreateTaskWithNoRepoTakesOnlyDeploymentDefaultCapabilities(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putDefaultCapabilities(t, ctx, store, "gemini-key")
	putRepoDefaultCapabilities(t, ctx, store, "acme/widgets", "gcp-key")

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "nothing to check out", Approved: true, NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(task.Capabilities, []string{"gemini-key"}) {
		t.Fatalf("capabilities = %v, want [gemini-key] alone", task.Capabilities)
	}
}

// The same skip-rather-than-fail rule the deployment-wide set has, on
// the repo layer: a repo row naming a capability this build retired must
// not become a repo no task can be filed against.
func TestCreateTaskSkipsRetiredRepoDefaultCapability(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putRepoDefaultCapabilities(t, ctx, store, "acme/widgets", "scratch-repo", "gemini-key")

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "still filable", Approved: true})
	if err != nil {
		t.Fatalf("filing a task against a repo defaulting a retired capability: %v", err)
	}
	if !reflect.DeepEqual(task.Capabilities, []string{"gemini-key"}) {
		t.Fatalf("capabilities = %v, want [gemini-key]", task.Capabilities)
	}
}

// A repo restating something the deployment already defaults grants it
// once, not twice -- the union is a set, and a duplicate grant would be
// two rows for one capability on the task.
func TestCreateTaskDeduplicatesOverlappingDefaultCapabilities(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putDefaultCapabilities(t, ctx, store, "gcp-key")
	putRepoDefaultCapabilities(t, ctx, store, "acme/widgets", "gcp-key")

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "one key only", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(task.Capabilities, []string{"gcp-key"}) {
		t.Fatalf("capabilities = %v, want [gcp-key] once", task.Capabilities)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Grants) != 1 {
		t.Fatalf("grants = %+v, want exactly one", stored.Grants)
	}
}

// grain/config is executable deployment configuration, so its tasks receive
// required Grain CI even when neither the deployment nor a stored repo row
// opts ordinary repositories into it.
func TestCreateConfigTaskGetsRequiredGrainCI(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "change deployment config", Approved: true, Repo: model.ConfigRepo().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(task.Capabilities, "grain-ci") {
		t.Fatalf("capabilities = %v, want mandatory grain-ci", task.Capabilities)
	}
}

// The browser always sends the capability picker as an explicit list, even
// when the operator never touched it. A repository requirement is a floor,
// not an optional default, so an empty picker cannot remove config validation.
func TestCreateConfigTaskKeepsRequiredGrainCIWithExplicitCapabilities(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "change deployment config", Approved: true,
		Repo: model.ConfigRepo().String(), Capabilities: &[]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(task.Capabilities, "grain-ci") {
		t.Fatalf("capabilities = %v, want mandatory grain-ci", task.Capabilities)
	}
}

// A proposal against the main configuration repository files into the
// default team, whichever team its proposer is on (single-tenant reset,
// stage 4c).
func TestMainConfigRepoProposalsFileIntoTheDefaultTeam(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	// Parent task belonging to team "alpha"
	parentTask := model.Task{
		ID:    "task-alpha",
		Title: "Alpha parent task",
	}
	if err := store.PutTask(ctx, parentTask); err != nil {
		t.Fatal(err)
	}

	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:      "Propose main config update",
		Repo:       model.ConfigRepo().String(),
		ProposedBy: "task-alpha",
	})
	if err != nil {
		t.Fatalf("proposing to the main config repo from an alpha task: %v", err)
	}

	// Parent task belonging to admin team ("default")
	adminParent := model.Task{
		ID:    "task-admin",
		Title: "Admin parent task",
	}
	if err := store.PutTask(ctx, adminParent); err != nil {
		t.Fatal(err)
	}

	// Agent proposing task for main config repo from admin team should succeed
	_, err = c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:      "Propose main config update from admin",
		Repo:       model.ConfigRepo().String(),
		ProposedBy: "task-admin",
	})
	if err != nil {
		t.Fatalf("expected success proposing to main config repo from admin team, got: %v", err)
	}
}

// A request that names its own set still names all of it: the per-repo
// layer seeds a task nobody said anything about, exactly like the
// deployment-wide one, and unticking a box on the new-task form has to
// mean the task is filed without that capability.
func TestCreateTaskCapabilitiesOverrideRepoDefaults(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putRepoDefaultCapabilities(t, ctx, store, "acme/widgets", "gcp-key")

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "not that one", Approved: true, Capabilities: &[]string{"gemini-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(task.Capabilities, []string{"gemini-key"}) {
		t.Fatalf("capabilities = %v, want [gemini-key] alone", task.Capabilities)
	}
}

// A follow-up proposed with its parent in depends_on carries a child-of
// and a depends-on link to the same task (the demo's task 11 does). It is
// blocked on that task once: BlockedBy is a list of tasks, not of links,
// and the timeline's and the board's "Blocked on" read it as one.
func TestBlockedByNamesATaskOnceWhateverLinksItTwice(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	parent := create(t, c, ctx)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "follow-up", Approved: true, DependsOn: []string{parent.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	st.Links = append(st.Links, model.Link{Kind: model.LinkChildOf, Target: parent.ID})
	if err := c.Store.PutTask(ctx, *st); err != nil {
		t.Fatal(err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Blocked || !reflect.DeepEqual(got.BlockedBy, []string{parent.ID}) {
		t.Fatalf("blocked = %v, blockedBy = %v, want true and [%s]", got.Blocked, got.BlockedBy, parent.ID)
	}
}

// A dependency declared at creation is both the definition (DependsOn)
// and, while the blocker is still open, the signal (Blocked/BlockedBy) --
// docs/data-model.md's "blocked is not a state, it is derived from
// links", re-derived here rather than pinned at creation.
func TestCreateTaskCarriesDependsOnAndBlockedSignal(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	blocker := create(t, c, ctx)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "needs the other thing first", Approved: true, DependsOn: []string{blocker.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.DependsOn) != 1 || task.DependsOn[0] != blocker.ID {
		t.Fatalf("dependsOn = %v, want [%s]", task.DependsOn, blocker.ID)
	}
	if !task.Blocked {
		t.Fatal("blocked = false, want true: its dependency has not closed")
	}
	if len(task.BlockedBy) != 1 || task.BlockedBy[0] != blocker.ID {
		t.Fatalf("blockedBy = %v, want [%s]", task.BlockedBy, blocker.ID)
	}

	// Approved and blocked is still not ready: task_ready must agree with
	// the JSON signal, not just IsBlocked's own unit tests.
	ready, err := c.Store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ready {
		if id == task.ID {
			t.Fatalf("ready = %v, want the blocked task excluded", ready)
		}
	}

	if err := c.Close(ctx, blocker.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Blocked {
		t.Fatalf("blocked = true after its dependency closed, want false")
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != blocker.ID {
		t.Fatalf("dependsOn after the blocker closed = %v, want it kept as the definition", got.DependsOn)
	}
}

func TestDependsOnNormalizesTaskIDReferences(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	blocker := create(t, c, ctx)
	// Create task with various non-canonical references to blocker
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:    "depends on formatted blocker",
		Approved: true,
		DependsOn: []string{
			"grain/task-" + blocker.ID,
			"#" + blocker.ID,
			"http://localhost:8080/tasks/" + blocker.ID,
			"https://grain.example.com/task/" + blocker.ID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.DependsOn) != 1 || task.DependsOn[0] != blocker.ID {
		t.Fatalf("dependsOn = %v, want normalized [%s]", task.DependsOn, blocker.ID)
	}
	if !task.Blocked {
		t.Fatal("blocked = false, want true: its dependency has not closed")
	}

	// SetDependency also normalizes on attach and detach
	task2 := create(t, c, ctx)
	if err := c.SetDependency(ctx, "grain/task-"+task2.ID, "grain/task-"+blocker.ID, true); err != nil {
		t.Fatalf("SetDependency with prefixed IDs failed: %v", err)
	}
	got, err := c.Task(ctx, task2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Blocked || len(got.DependsOn) != 1 || got.DependsOn[0] != blocker.ID {
		t.Fatalf("task after SetDependency = %+v, want blocked and depending on %s", got, blocker.ID)
	}

	if err := c.SetDependency(ctx, "#"+task2.ID, "#"+blocker.ID, false); err != nil {
		t.Fatalf("SetDependency detach with prefixed IDs failed: %v", err)
	}
	got, _ = c.Task(ctx, task2.ID)
	if got.Blocked || len(got.DependsOn) != 0 {
		t.Fatalf("task after detach = %+v, want unblocked with no dependencies", got)
	}

	// A GitHub URL ending in the blocker's number is not a reference to
	// it: it is refused as an unknown task rather than attached.
	pr := "https://github.com/acme/widgets/pull/" + blocker.ID
	if err := c.SetDependency(ctx, task2.ID, pr, true); err == nil {
		got, _ = c.Task(ctx, task2.ID)
		t.Fatalf("attaching %s succeeded (dependsOn %v), want it refused as naming no task",
			pr, got.DependsOn)
	}
}

// A person naming a task that never completes is told so and nothing is
// written: a blocking link is released by its target closing, a standing
// chat session's task never closes, and the task would sit queued and
// undispatchable forever (grain/task-487). The same refusal the picker
// already makes for a task that does not exist and for the task itself.
func TestDependingOnATaskThatNeverCompletesIsRefused(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	session := sessionTask("450")
	if err := store.PutTask(ctx, session); err != nil {
		t.Fatal(err)
	}

	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "after the chat", Approved: true, DependsOn: []string{session.ID},
	}); err == nil {
		t.Fatal("CreateTask accepted a dependency on a task that never completes")
	}

	// And the same through the picker, which attaches one after the fact.
	task := create(t, c, ctx)
	if err := c.SetDependency(ctx, task.ID, session.ID, true); err == nil {
		t.Fatal("SetDependency attached a dependency on a task that never completes")
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DependsOn) != 0 {
		t.Fatalf("dependsOn = %v, want nothing written", got.DependsOn)
	}

	// Closed, that same task is an ordinary dependency: it completed
	// exactly once, which is what a blocking link waits for. The refusal
	// is about a task nothing will ever finish, not about its kind.
	if err := c.Close(ctx, session.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDependency(ctx, task.ID, session.ID, true); err != nil {
		t.Fatalf("SetDependency on a closed session task: %v", err)
	}
}

// A dependency that would close a loop is refused with an error naming
// the chain, and nothing is written: neither task would ever be ready
// again. The loop that matters most in practice is onto a base-branch
// repair grain has already made the task wait on
// (docs/test-runs/2026-10-12-clearance-carry-red-base-links.md, F1). A
// loop through a closed task is no loop, since a closed task waits on
// nothing.
func TestADependencyThatWouldCloseALoopIsRefused(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	repair := create(t, c, ctx)
	middle := create(t, c, ctx)
	waiter := create(t, c, ctx)
	if err := c.SetDependency(ctx, waiter.ID, middle.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDependency(ctx, middle.ID, repair.ID, true); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{waiter.ID, middle.ID} {
		err := c.SetDependency(ctx, repair.ID, target, true)
		if err == nil {
			t.Fatalf("SetDependency(%s -> %s) closed a loop and was accepted", repair.ID, target)
		}
		if !strings.Contains(err.Error(), "loop") || !strings.Contains(err.Error(), "task "+target+" already waits on task "+repair.ID) {
			t.Errorf("refusal does not say what the loop is: %v", err)
		}
	}
	got, err := c.Task(ctx, repair.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DependsOn) != 0 {
		t.Fatalf("dependsOn = %v, want nothing written", got.DependsOn)
	}

	if err := c.Close(ctx, middle.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDependency(ctx, repair.ID, waiter.ID, true); err != nil {
		t.Fatalf("a chain through a closed task was refused as a loop: %v", err)
	}
}

// A proposal a run filed while it was still going is not failed over the
// same entry: there is nobody to tell, and losing the whole proposal
// over one argument is worse than filing it with the ordering written
// out in words (model.NeverEndingDependencyNote). This is the path the
// director actually files through, and the one that produced eight
// undispatchable tasks in a day.
func TestAProposalsDependencyOnANeverEndingTaskIsDroppedWithANote(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	session := sessionTask("450")
	if err := store.PutTask(ctx, session); err != nil {
		t.Fatal(err)
	}
	real := create(t, c, ctx)

	filed, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "split out of the chat", Description: "the work itself",
		DependsOn: []string{session.ID, real.ID}, ProposedBy: session.ID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if len(filed.DependsOn) != 1 || filed.DependsOn[0] != real.ID {
		t.Fatalf("dependsOn = %v, want just %s -- the session's own task never unblocks anything",
			filed.DependsOn, real.ID)
	}
	detail, err := c.GetTask(ctx, filed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.Description, session.ID) {
		t.Errorf("description = %q, want the dropped dependency named in it", detail.Description)
	}
	if !strings.Contains(detail.Description, "the work itself") {
		t.Errorf("description = %q, want the run's own body kept", detail.Description)
	}
}

// A read-only repo is stored and rendered as owner/name, and -- the
// design doc's "the single most important rule in this subsection" --
// naming one grants nothing: only Capabilities produce a Grant.
func TestCreateTaskCarriesReadOnlyRepos(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "needs a shared lib", Approved: true,
		Reads: []string{"acme/shared-lib", "acme/schema"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The store does not promise to preserve the order of a set with no
	// ordering column of its own (task_read's primary key is (task_id,
	// owner, name)), so this compares membership, not order.
	got := append([]string(nil), task.Reads...)
	sort.Strings(got)
	want := []string{"acme/schema", "acme/shared-lib"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reads = %v, want %v", task.Reads, want)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Grants) != 0 {
		t.Fatalf("grants = %+v, want none: a read-only repo must grant nothing", stored.Grants)
	}
}

// One repository named twice among the read-only repos is one
// read-only repo, however it was spelled the second time.
//
// Both halves used to reach task_read, whose (task, owner, name)
// primary key is exact: the repeated spelling failed the whole filing
// with a raw "UNIQUE constraint failed" out of SQLite -- a 500 for what
// a paste of somebody's notes produces -- and the differently-cased one
// stored a single repository as two, which BuildPrompt then offered the
// agent twice.
func TestCreateTaskKeepsOneEntryPerReadOnlyRepo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "needs a shared lib", Approved: true,
		Reads: []string{"acme/shared-lib", "acme/shared-lib", "ACME/Shared-Lib", "acme/schema"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), task.Reads...)
	sort.Strings(got)
	want := []string{"acme/schema", "acme/shared-lib"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reads = %v, want %v", task.Reads, want)
	}
}

// The same on the edit path, which parses its own set: a repo already
// in the box and typed again is not a second repo, and not a failed
// PATCH either -- the task pane re-sends the whole set on every change,
// so a refusal here would be an edit somebody could not complete.
func TestUpdateTaskKeepsOneEntryPerReadOnlyRepo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	reads := []string{"acme/shared-lib", "Acme/Shared-Lib"}
	got, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Reads: &reads})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Reads, []string{"acme/shared-lib"}) {
		t.Fatalf("reads = %v, want [acme/shared-lib]", got.Reads)
	}
}

// A task may name more than one repo it can land work in, each with the
// branch that work goes on there (model.WriteTarget) -- and, like a
// read-only repo, naming one grants nothing.
func TestCreateTaskCarriesExtraWriteTargets(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "grain and its config", Approved: true, Repo: "acme/widgets",
		Writes: []ui.WriteTarget{
			{Repo: "acme/widgets-state", Base: "deploy"},
			{Repo: "acme/docs"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, w := range task.Writes {
		got[w.Repo] = w.Base
	}
	want := map[string]string{"acme/widgets-state": "deploy", "acme/docs": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("writes = %+v, want %v", task.Writes, want)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Grants) != 0 {
		t.Fatalf("grants = %+v, want none: an extra write target must grant nothing", stored.Grants)
	}
	// The repo the task's own runs push to is unchanged by any of this.
	if task.Repo != "acme/widgets" {
		t.Fatalf("repo = %q, want the task's own target", task.Repo)
	}
}

// A task may declare multiple write targets on different branches for the same repo,
// as well as an additional branch for the repo it is filed against.
func TestCreateTaskAllowsMultipleBranchesPerRepo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "multi-branch task", Approved: true, Repo: "acme/widgets", Base: "main",
		Writes: []ui.WriteTarget{
			{Repo: "acme/widgets", Base: "release/1.0"},
			{Repo: "acme/widgets-state", Base: "deploy"},
			{Repo: "acme/widgets-state", Base: "staging"},
		},
	})
	if err != nil {
		t.Fatalf("CreateTask = %v, want success", err)
	}
	if len(task.Writes) != 3 {
		t.Fatalf("len(task.Writes) = %d, want 3", len(task.Writes))
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantWrites := []model.WriteTarget{
		{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Base: "release/1.0"},
		{Repo: model.RepoRef{Owner: "acme", Name: "widgets-state"}, Base: "deploy"},
		{Repo: model.RepoRef{Owner: "acme", Name: "widgets-state"}, Base: "staging"},
	}
	if !reflect.DeepEqual(stored.Writes, wantWrites) {
		t.Fatalf("stored.Writes = %+v, want %+v", stored.Writes, wantWrites)
	}
}

// A task's own target off the allowlist is filed and parked
// (parkOffAllowlist); an extra write target off it is refused outright,
// since it is not what the task is for and the proposal it would
// eventually produce is written straight to the store, past every check
// this package makes.
func TestCreateTaskRefusesAWriteTargetOffTheAllowlist(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "acme/gadgets"})

	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, Repo: "acme/widgets",
		Writes: []ui.WriteTarget{{Repo: "acme/elsewhere"}},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}

	// And one that is on the list is filed like any other.
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, Repo: "acme/widgets",
		Writes: []ui.WriteTarget{{Repo: "acme/gadgets", Base: "deploy"}},
	}); err != nil {
		t.Fatalf("filing a task whose write target is on the allowlist: %v", err)
	}
}

func TestCreateTaskValidates(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	for name, req := range map[string]ui.CreateTaskRequest{
		"empty title":        {Title: "  "},
		"unknown capability": {Title: "t", Capabilities: &[]string{"nope"}},
		"unparseable repo":   {Title: "t", Repo: "not-a-repo"},
		"unknown dependency": {Title: "t", DependsOn: []string{"404"}},
		"unparseable read":   {Title: "t", Reads: []string{"not-a-repo"}},
		"unparseable write":  {Title: "t", Writes: []ui.WriteTarget{{Repo: "not-a-repo"}}},
		// Both halves of a write target that says nothing new: the same
		// repo twice on the same branch, and the
		// task's own target on its own Base.
		"the same write repo twice on the same branch": {Title: "t", Writes: []ui.WriteTarget{
			{Repo: "acme/state", Base: "main"}, {Repo: "acme/state", Base: "main"}}},
		"a write repo that is the target on the same base": {Title: "t", Repo: "acme/widgets", Base: "deploy",
			Writes: []ui.WriteTarget{{Repo: "ACME/Widgets", Base: "deploy"}}},
		// And the read-only half of the same block: the repo a run is
		// filed against is cloned into its sandbox and pushed to, so it
		// cannot also be one the run may only read. Spelled in another
		// case here too, since RepoRef.Same is what has to catch it.
		"a read repo that is the target": {Title: "t", Repo: "acme/widgets",
			Reads: []string{"acme/schema", "ACME/Widgets"}},
		"negative sandbox cpus":    {Title: "t", SandboxCPUs: -1},
		"sandbox memory below 128": {Title: "t", SandboxMemoryMB: 64},
		"negative sandbox disk":    {Title: "t", SandboxDiskGB: -1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.CreateTask(ctx, req)
			var ve *ui.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
		})
	}
}

func TestCreateTaskNeedsATargetWhenThereIsNoDefault(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.DefaultTarget = nil

	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "nowhere to go"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// NoRepo (bwsalmon/agents#614) is the deliberate choice, distinct from a
// blank Repo: even with a configured DefaultTarget to fall back to, an
// explicit NoRepo files the task standalone rather than pinning it to
// the default.
func TestCreateTaskNoRepoFilesAStandaloneTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "standalone", NoRepo: true})
	if err != nil {
		t.Fatalf("creating a no-repo task: %v", err)
	}
	if task.Repo != "" {
		t.Fatalf("repo = %q, want none", task.Repo)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading the stored task: %v", err)
	}
	if stored.Target != nil {
		t.Fatalf("stored target = %+v, want nil", stored.Target)
	}
}

func TestCreateTaskRejectsRepoAndNoRepoTogether(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "confused", Repo: "acme/other", NoRepo: true})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestUpdateTaskChangesOnlyTheFieldsGiven(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	title := "renamed"
	got, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "renamed" {
		t.Fatalf("title = %q, want renamed", got.Title)
	}
	if got.Description != "please" {
		t.Fatalf("description = %q, want it untouched", got.Description)
	}
	if got.Repo != "acme/widgets" {
		t.Fatalf("repo = %q, want it untouched", got.Repo)
	}
}

// A running task's repo and base are the ones its run was dispatched
// against, so an edit moving either is refused until that run ends
// (grain/task-2310) -- while one restating them, as a whole-form save
// does, still lets the rest of the edit through.
func TestUpdateTaskRefusesRetargetingARunningTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "in flight", Repo: "acme/widgets", Base: "release/2", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	startRunOn(t, store, ctx, task.ID)

	for name, req := range map[string]ui.UpdateTaskRequest{
		"repo": {Repo: ptr("acme/other")},
		"base": {Base: ptr("main")},
	} {
		_, err := c.UpdateTask(ctx, task.ID, req)
		var ve *ui.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: error = %v, want a ValidationError refusing the edit", name, err)
		}
	}

	edited, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{
		Title: ptr("in flight, renamed"), Repo: ptr("acme/widgets"), Base: ptr("release/2"),
	})
	if err != nil {
		t.Fatalf("restating the repo and base of a running task: %v", err)
	}
	if edited.Title != "in flight, renamed" || edited.Repo != "acme/widgets" || edited.Base != "release/2" {
		t.Fatalf("task = %+v, want the title edited and the repo and base untouched", edited)
	}
}

func TestUpdateTaskEditsEveryField(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	repo, base, autoMerge := "other/repo", "release", true
	reads := []string{"acme/shared-lib"}
	got, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{
		Repo: &repo, Base: &base, AutoMerge: &autoMerge, Reads: &reads,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "other/repo" || got.Base != "release" || !got.AutoMerge {
		t.Fatalf("task = %+v, want the edited fields", got)
	}
	if !reflect.DeepEqual(got.Reads, reads) {
		t.Fatalf("reads = %v, want %v", got.Reads, reads)
	}
}

// bwsalmon/agents#534, grain/task-41: a task's own SandboxCPUs/
// SandboxMemoryMB/SandboxDiskGB override
// round-trips through CreateTask and UpdateTask the same as every other
// task field, and setting any of them back to 0 through UpdateTask clears
// that override (distinct from leaving the request field nil, which
// leaves it alone -- UpdateTaskRequest's own doc comment).
func TestTaskSandboxShapeOverrideRoundTrips(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Repo: "acme/widgets", Approved: true,
		SandboxCPUs: 4, SandboxMemoryMB: 8192, SandboxDiskGB: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SandboxCPUs != 4 || created.SandboxMemoryMB != 8192 || created.SandboxDiskGB != 40 {
		t.Fatalf("created task sandbox shape = %d/%d/%d, want 4/8192/40",
			created.SandboxCPUs, created.SandboxMemoryMB, created.SandboxDiskGB)
	}

	cpus, memoryMB, diskGB := 2, 4096, 20
	updated, err := c.UpdateTask(ctx, created.ID, ui.UpdateTaskRequest{
		SandboxCPUs: &cpus, SandboxMemoryMB: &memoryMB, SandboxDiskGB: &diskGB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SandboxCPUs != 2 || updated.SandboxMemoryMB != 4096 || updated.SandboxDiskGB != 20 {
		t.Fatalf("updated task sandbox shape = %d/%d/%d, want 2/4096/20",
			updated.SandboxCPUs, updated.SandboxMemoryMB, updated.SandboxDiskGB)
	}

	zero := 0
	cleared, err := c.UpdateTask(ctx, created.ID, ui.UpdateTaskRequest{
		SandboxCPUs: &zero, SandboxMemoryMB: &zero, SandboxDiskGB: &zero,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.SandboxCPUs != 0 || cleared.SandboxMemoryMB != 0 || cleared.SandboxDiskGB != 0 {
		t.Fatalf("cleared task sandbox shape = %d/%d/%d, want 0/0/0",
			cleared.SandboxCPUs, cleared.SandboxMemoryMB, cleared.SandboxDiskGB)
	}
}

// Reads has no attach/detach endpoint of its own (unlike Capabilities and
// DependsOn): a given Reads always replaces the whole set rather than
// adding to it.
func TestUpdateTaskReadsReplacesRatherThanAdds(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, Reads: []string{"acme/shared-lib"},
	})
	if err != nil {
		t.Fatal(err)
	}

	replacement := []string{"acme/schema"}
	got, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Reads: &replacement})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Reads, replacement) {
		t.Fatalf("reads = %v, want %v (replaced, not appended)", got.Reads, replacement)
	}

	cleared := []string{}
	got, err = c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Reads: &cleared})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Reads) != 0 {
		t.Fatalf("reads = %v, want none: an explicit empty slice clears the set", got.Reads)
	}
}

// Writes replaces the whole set the way Reads does, and for the same
// reason: an entry is a repo *and* a branch, so there is nothing to
// toggle one at a time.
func TestUpdateTaskWritesReplacesRatherThanAdds(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, Repo: "acme/widgets",
		Writes: []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	replacement := []ui.WriteTarget{{Repo: "acme/docs", Base: "trunk"}}
	got, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Writes: &replacement})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Writes, replacement) {
		t.Fatalf("writes = %+v, want %+v (replaced, not appended)", got.Writes, replacement)
	}

	cleared := []ui.WriteTarget{}
	got, err = c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Writes: &cleared})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Writes) != 0 {
		t.Fatalf("writes = %+v, want none: an explicit empty slice clears the set", got.Writes)
	}
}

// Moving a task's own repo onto one it already lists as an extra write
// target is the same contradiction as adding its target to that list,
// and is refused from either direction -- the check reads the task as
// the edit leaves it, not as it arrived.
func TestUpdateTaskRefusesATargetItAlreadyWritesTo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, Repo: "acme/widgets",
		Writes: []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := "acme/widgets-state"
	base := "deploy"
	_, err = c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Repo: &repo, Base: &base})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// The read-only repos are refused from either direction too: a task's
// own repo is where its run pushes, so it cannot be one the run may only
// read, however the two halves came to meet.
func TestUpdateTaskRefusesATargetItAlreadyReads(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "t", Approved: true, Repo: "acme/widgets",
		Reads: []string{"acme/schema"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Moving the task onto a repo it reads.
	repo := "acme/schema"
	_, err = c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Repo: &repo})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}

	// And adding the repo it is already filed against.
	reads := []string{"acme/schema", "acme/widgets"}
	_, err = c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Reads: &reads})
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// A task that already carries its own target among its reads is still
// editable. Nothing this package accepts can produce that any more, but
// the paths that write a task's reads straight to the store can (a
// template's or a schedule's own Reads, a backport carrying the
// original's), and a stale entry nobody typed must not freeze the rest
// of the task: an edit that does not mention the repos is not the place
// to complain about them, and taking the entry off has to work.
func TestUpdateTaskLeavesAStoredTargetAmongReadsAlone(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading the stored task: %v", err)
	}
	stored.Reads = []model.RepoRef{{Owner: "acme", Name: "schema"}, *stored.Target}
	if err := store.PutTask(ctx, *stored); err != nil {
		t.Fatal(err)
	}

	title := "renamed"
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Title: &title}); err != nil {
		t.Fatalf("editing the title of a task carrying its target among its reads: %v", err)
	}

	reads := []string{"acme/schema"}
	got, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Reads: &reads})
	if err != nil {
		t.Fatalf("taking the stale entry off: %v", err)
	}
	if !reflect.DeepEqual(got.Reads, reads) {
		t.Fatalf("reads = %v, want %v", got.Reads, reads)
	}
}

func TestUpdateTaskValidates(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	blank, bad := "  ", "not-a-repo"
	negativeCPUs, lowMemory, negativeDisk := -1, 64, -1
	for name, req := range map[string]ui.UpdateTaskRequest{
		"empty title": {Title: &blank},
		// Clearing the target is rejected rather than allowed: a task with
		// no target cannot be dispatched, and it is a real column now
		// rather than an optional directive line that could just be absent.
		"empty repo":                     {Repo: &blank},
		"unparseable repo":               {Repo: &bad},
		"unparseable read":               {Reads: &[]string{"not-a-repo"}},
		"a read repo that is the target": {Reads: &[]string{"acme/widgets"}},
		"unparseable write":              {Writes: &[]ui.WriteTarget{{Repo: "not-a-repo"}}},
		"negative sandbox cpus":          {SandboxCPUs: &negativeCPUs},
		"sandbox memory below 128":       {SandboxMemoryMB: &lowMemory},
		"negative sandbox disk":          {SandboxDiskGB: &negativeDisk},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.UpdateTask(ctx, task.ID, req)
			var ve *ui.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
		})
	}
}

// TestUpdateTaskNotesAnEditToTitleOrDescriptionAsAComment is bwsalmon/
// agents#523: a task's title and description are the two fields
// BuildPrompt actually hands a dispatched run, so an edit to either while
// a run is in flight has to reach it somehow -- and the mechanism that
// already exists for reaching a live run (orchestrator.addendaPoller) is
// "read the conversation," not "read the task's current row." Recording
// the edit as a Comment is what makes it visible there.
func TestUpdateTaskNotesAnEditToTitleOrDescriptionAsAComment(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	title := "rename the thing"
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Title: &title}); err != nil {
		t.Fatal(err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %+v, want exactly one noting the title edit", comments)
	}
	if got := comments[0]; got.Author.Actor.ID != "alice" || !strings.Contains(got.Body, title) {
		t.Fatalf("comment = %+v, want it attributed to alice and naming the new title", got)
	}

	description := "please, and hurry"
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Description: &description}); err != nil {
		t.Fatal(err)
	}
	comments, err = store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("comments = %+v, want a second one noting the description edit", comments)
	}
	if !strings.Contains(comments[1].Body, description) {
		t.Fatalf("comment = %+v, want it to include the new description", comments[1])
	}
}

// TestUpdateTaskQuotesAnEditedDescriptionVerbatim: the note quotes the
// new description last, so nothing may follow it -- a full stop after a
// description ending "done." read "done..", and after one ending in a
// fenced block it left the fence unclosed.
func TestUpdateTaskQuotesAnEditedDescriptionVerbatim(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	title := "rename the thing"
	description := "Run this.\n\n```sh\nmake test\n```"
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Title: &title, Description: &description}); err != nil {
		t.Fatal(err)
	}
	title = "rename it again"
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Title: &title}); err != nil {
		t.Fatal(err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("comments = %+v, want one per edit", comments)
	}
	want := "This task was just edited -- the title changed to \"rename the thing\"; and the description changed to:\n\n" + description
	if comments[0].Body != want {
		t.Fatalf("comment body = %q, want %q", comments[0].Body, want)
	}
	if want := "This task was just edited -- the title changed to \"rename it again\"."; comments[1].Body != want {
		t.Fatalf("comment body = %q, want %q", comments[1].Body, want)
	}
}

// TestUpdateTaskNotesNothingWhenTitleAndDescriptionAreUnchanged checks the
// other half of the same rule: a request that only touches other fields,
// or that names a title/description identical to what is already stored,
// adds no comment -- an edit form that always submits every field on save
// must not spam a task's conversation on every save that changed nothing.
func TestUpdateTaskNotesNothingWhenTitleAndDescriptionAreUnchanged(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	base := "release"
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Base: &base}); err != nil {
		t.Fatal(err)
	}
	sameTitle, sameDescription := task.Title, task.Description
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Title: &sameTitle, Description: &sameDescription}); err != nil {
		t.Fatal(err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Fatalf("comments = %+v, want none: nothing BuildPrompt reads ever changed", comments)
	}
}

func TestUpdateTaskNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	title := "x"
	_, err := c.UpdateTask(ctx, "404", ui.UpdateTaskRequest{Title: &title})
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %v, want a NotFoundError", err)
	}
}

func TestSetCapabilityAttachesAndDetaches(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := c.SetCapability(ctx, task.ID, "gemini-key", true); err != nil {
		t.Fatal(err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Capabilities) != 1 {
		t.Fatalf("capabilities = %v, want one", got.Capabilities)
	}

	// Attaching twice must not produce two grants.
	if err := c.SetCapability(ctx, task.ID, "gemini-key", true); err != nil {
		t.Fatal(err)
	}
	if got, _ = c.Task(ctx, task.ID); len(got.Capabilities) != 1 {
		t.Fatalf("capabilities after a second attach = %v, want still one", got.Capabilities)
	}

	if err := c.SetCapability(ctx, task.ID, "gemini-key", false); err != nil {
		t.Fatal(err)
	}
	if got, _ = c.Task(ctx, task.ID); len(got.Capabilities) != 0 {
		t.Fatalf("capabilities after detach = %v, want none", got.Capabilities)
	}
	// Detaching one that is not attached is a no-op, matching what
	// removing an absent label used to do.
	if err := c.SetCapability(ctx, task.ID, "gemini-key", false); err != nil {
		t.Fatalf("detaching an absent capability: %v", err)
	}
}

// gcp-key and github-sandbox each had a provider pkg/cli/daemon.go
// registered and no OfferedCapabilities row, so every attempt to attach
// one -- the only way a model.Grant is ever written -- was rejected as
// an unknown capability, and no sandbox on any deployment ever got
// gcpkey.SandboxKeyPath. Both routes a human has are covered here,
// since both validate against the same listing.
func TestGCPKeyAndGitHubSandboxCanBeGranted(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "mint me a key", Approved: true,
		Capabilities: &[]string{"gcp-key", "github-sandbox"},
	})
	if err != nil {
		t.Fatalf("creating a task granting gcp-key and github-sandbox: %v", err)
	}
	for _, want := range []string{"gcp-key", "github-sandbox"} {
		if !slices.Contains(task.Capabilities, want) {
			t.Errorf("capabilities = %v, want %s among them", task.Capabilities, want)
		}
	}

	plain := create(t, c, ctx)
	for _, id := range []string{"gcp-key", "github-sandbox"} {
		if err := c.SetCapability(ctx, plain.ID, id, true); err != nil {
			t.Errorf("attaching %s: %v", id, err)
		}
	}
}

// A task can be holding a grant this deployment no longer offers -- a
// renamed capability, "scratch-repo" being the one that was here, which
// fails the task's every dispatch at model.ResolveGrants. Detaching it
// has to work, or the only route out is the store itself. Attaching one
// is still refused: that is the check that keeps an unknown id from
// becoming a grant in the first place.
func TestSetCapabilityDetachesAnIDNoLongerOffered(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Grants = append(tk.Grants, model.Grant{Capability: "scratch-repo", Via: model.GrantByLabel})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.SetCapability(ctx, task.ID, "scratch-repo", false); err != nil {
		t.Fatalf("detaching a capability this deployment no longer offers: %v", err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Capabilities) != 0 {
		t.Fatalf("capabilities after detach = %v, want none", got.Capabilities)
	}

	err = c.SetCapability(ctx, task.ID, "scratch-repo", true)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("attaching an unknown capability: error = %v, want a ValidationError", err)
	}
}

func TestSetDependencyAttachesAndDetaches(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	blocker := create(t, c, ctx)
	task := create(t, c, ctx)

	if err := c.SetDependency(ctx, task.ID, blocker.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Blocked || len(got.DependsOn) != 1 || got.DependsOn[0] != blocker.ID {
		t.Fatalf("task = %+v, want blocked and depending on %s", got, blocker.ID)
	}

	// Attaching twice must not produce two links -- the link table's own
	// primary key already forbids the duplicate, but SetDependency's own
	// mutate closure checks first so a retry cannot even attempt it.
	if err := c.SetDependency(ctx, task.ID, blocker.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = c.Task(ctx, task.ID); len(got.DependsOn) != 1 {
		t.Fatalf("dependsOn after a second attach = %v, want still one", got.DependsOn)
	}

	if err := c.SetDependency(ctx, task.ID, blocker.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = c.Task(ctx, task.ID); got.Blocked || len(got.DependsOn) != 0 {
		t.Fatalf("task after detach = %+v, want unblocked with no dependencies", got)
	}
	// Detaching one that is not attached is a no-op, matching SetCapability.
	if err := c.SetDependency(ctx, task.ID, blocker.ID, false); err != nil {
		t.Fatalf("detaching an absent dependency: %v", err)
	}
}

func TestSetDependencyValidates(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	if _, err := c.Task(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	var ve *ui.ValidationError

	if err := c.SetDependency(ctx, task.ID, task.ID, true); !errors.As(err, &ve) {
		t.Fatalf("depending on itself: error = %v, want a ValidationError", err)
	}
	if err := c.SetDependency(ctx, task.ID, "404", true); !errors.As(err, &ve) {
		t.Fatalf("depending on an unknown task: error = %v, want a ValidationError", err)
	}
}

func TestApproveMakesAProposalDispatchable(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "maybe"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Approve(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.StateQueued {
		t.Fatalf("state = %q, want queued", got.State)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Approval == nil || stored.Approval.Actor.ID != "alice" {
		t.Fatalf("approval = %+v, want it recorded against the configured actor", stored.Approval)
	}
	// Approving again is a no-op rather than an error.
	if err := c.Approve(ctx, task.ID); err != nil {
		t.Fatalf("approving twice: %v", err)
	}
}

// The inverse of TestApproveMakesAProposalDispatchable: a queued task
// goes back to being a proposal, keeping everything else about it, and
// approving it a second time queues it again.
func TestWithdrawApprovalTakesAQueuedTaskBackOutOfTheQueue(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := c.WithdrawApproval(ctx, task.ID); err != nil {
		t.Fatalf("withdrawing approval: %v", err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.StateProposed {
		t.Fatalf("state = %q, want proposed", got.State)
	}
	if got.Title != task.Title {
		t.Fatalf("title = %q, want it untouched at %q", got.Title, task.Title)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Approval != nil || stored.ApprovedAt != nil {
		t.Fatalf("approval = %+v, approvedAt = %v, want both cleared", stored.Approval, stored.ApprovedAt)
	}

	// Withdrawing from a task that carries no approval is a no-op rather
	// than an error, mirroring Approve on an already-approved one.
	if err := c.WithdrawApproval(ctx, task.ID); err != nil {
		t.Fatalf("withdrawing twice: %v", err)
	}
	if err := c.Approve(ctx, task.ID); err != nil {
		t.Fatalf("re-approving: %v", err)
	}
	if got, err = c.Task(ctx, task.ID); err != nil || got.State != model.StateQueued {
		t.Fatalf("state after re-approval = %q (err %v), want queued", got.State, err)
	}
}

// The states where the approval has already been spent on work that
// happened: clearing it there would erase a real queue wait and stop
// nothing, so it is refused rather than quietly rewriting the record.
func TestWithdrawApprovalRefusesATaskThatHasStarted(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	var ve *ui.ValidationError

	running := create(t, c, ctx)
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: running.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := c.WithdrawApproval(ctx, running.ID); !errors.As(err, &ve) {
		t.Fatalf("withdrawing from a running task: error = %v, want a ValidationError", err)
	}

	completed := create(t, c, ctx)
	done := baseTime.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: completed.ID, CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if err := c.WithdrawApproval(ctx, completed.ID); !errors.As(err, &ve) {
		t.Fatalf("withdrawing from a completed task: error = %v, want a ValidationError", err)
	}
	stored, err := store.GetTask(ctx, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Approval == nil {
		t.Fatal("a refused withdrawal cleared the approval anyway")
	}

	var nf *ui.NotFoundError
	if err := c.WithdrawApproval(ctx, "404"); !errors.As(err, &nf) {
		t.Fatalf("withdrawing from an unknown task: error = %v, want a NotFoundError", err)
	}
}

// Deferring is not withdrawing approval: the task leaves the queue, and
// picking it back up puts it straight back on without anybody approving
// it a second time. A proposal deferred and picked back up is a proposal
// again, for the same reason.
func TestDeferAndUndeferReturnATaskToTheStateItLeft(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	queued := create(t, c, ctx)
	if err := c.Defer(ctx, queued.ID, ui.DeferOptions{}); err != nil {
		t.Fatalf("deferring a queued task: %v", err)
	}
	got, err := c.Task(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.StateDeferred {
		t.Fatalf("state = %q, want deferred", got.State)
	}
	stored, err := store.GetTask(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Approval == nil {
		t.Fatal("deferring cleared the approval, which is withdrawing's job and not this one's")
	}
	if stored.DeferredAt == nil || !stored.DeferredAt.Equal(baseTime) {
		t.Fatalf("deferredAt = %v, want the client's own clock at %v", stored.DeferredAt, baseTime)
	}
	// Nothing dispatchable while it is put aside.
	if ready, _ := store.Ready(ctx); len(ready) != 0 {
		t.Fatalf("ready while deferred = %v, want nothing", ready)
	}

	if err := c.Undefer(ctx, queued.ID); err != nil {
		t.Fatalf("undeferring: %v", err)
	}
	if got, err = c.Task(ctx, queued.ID); err != nil || got.State != model.StateQueued {
		t.Fatalf("state after undeferring = %q (err %v), want queued", got.State, err)
	}
	// Undeferring a task nobody deferred is a no-op, mirroring Approve on
	// an already-approved one.
	if err := c.Undefer(ctx, queued.ID); err != nil {
		t.Fatalf("undeferring twice: %v", err)
	}

	proposal, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "maybe"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Defer(ctx, proposal.ID, ui.DeferOptions{}); err != nil {
		t.Fatalf("deferring a proposal: %v", err)
	}
	// Deferring twice restamps rather than failing.
	if err := c.Defer(ctx, proposal.ID, ui.DeferOptions{}); err != nil {
		t.Fatalf("deferring twice: %v", err)
	}
	if err := c.Undefer(ctx, proposal.ID); err != nil {
		t.Fatal(err)
	}
	if got, err = c.Task(ctx, proposal.ID); err != nil || got.State != model.StateProposed {
		t.Fatalf("state after undeferring a proposal = %q (err %v), want proposed", got.State, err)
	}
}

// Deferring with a wake time on it (ui.DeferOptions.Until): the date is
// stored beside the deferral, deferring again is how it is changed or
// dropped, and picking the task up by hand takes it away -- so a task
// somebody undeferrs early does not defer itself again on the date it
// was going to wake.
//
// What the wake time does *not* change is the state: a task that says
// when it may start is plain 'deferred' until the orchestrator wakes it
// (orchestrator.reconcileWake, which is the other half of this).
func TestDeferringWithAWakeTimeStoresTheDateBesideTheDeferral(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task := create(t, c, ctx)
	until := baseTime.Add(7 * 24 * time.Hour)
	if err := c.Defer(ctx, task.ID, ui.DeferOptions{Until: &until}); err != nil {
		t.Fatalf("deferring until a date: %v", err)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DeferredUntil == nil || !stored.DeferredUntil.Equal(until) {
		t.Fatalf("deferredUntil = %v, want %v", stored.DeferredUntil, until)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.StateDeferred {
		t.Fatalf("state = %q, want deferred: a wake time is a note on a deferral, not a state", got.State)
	}
	if got.DeferredUntil == nil || !got.DeferredUntil.Equal(until) {
		t.Fatalf("the task pane is told it wakes at %v, want %v", got.DeferredUntil, until)
	}

	// Deferring again with no date is how a wake time is dropped without
	// picking the task up.
	if err := c.Defer(ctx, task.ID, ui.DeferOptions{}); err != nil {
		t.Fatalf("deferring again with no date: %v", err)
	}
	if stored, err = store.GetTask(ctx, task.ID); err != nil || stored.DeferredUntil != nil {
		t.Fatalf("deferredUntil after a dateless second deferral = %v (err %v), want nil", stored.DeferredUntil, err)
	}

	// And undeferring clears both halves.
	if err := c.Defer(ctx, task.ID, ui.DeferOptions{Until: &until}); err != nil {
		t.Fatal(err)
	}
	if err := c.Undefer(ctx, task.ID); err != nil {
		t.Fatalf("undeferring: %v", err)
	}
	stored, err = store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DeferredAt != nil || stored.DeferredUntil != nil {
		t.Fatalf("after undeferring the task is deferred at %v until %v, want neither",
			stored.DeferredAt, stored.DeferredUntil)
	}
}

// A wake time that has already passed is refused rather than obeyed: it
// would be undone by the next cycle, and somebody asking for one is
// working from a stale clock or has mistyped a date (DeferOptions.Until).
func TestDeferRefusesAWakeTimeThatHasPassed(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	var ve *ui.ValidationError

	task := create(t, c, ctx)
	past := baseTime.Add(-time.Hour)
	if err := c.Defer(ctx, task.ID, ui.DeferOptions{Until: &past}); !errors.As(err, &ve) {
		t.Fatalf("deferring until a moment that has passed: error = %v, want a ValidationError", err)
	}
	// Now itself is not in the future either: the task would wake on the
	// cycle that deferred it.
	if err := c.Defer(ctx, task.ID, ui.DeferOptions{Until: &baseTime}); !errors.As(err, &ve) {
		t.Fatalf("deferring until now: error = %v, want a ValidationError", err)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DeferredAt != nil {
		t.Fatal("a refused wake time put the task aside anyway")
	}
}

// The states deferring is refused on: a run in flight, and every task
// whose work has already happened. Nothing there is waiting on a dispatch
// that putting it aside could call off (Client.Defer).
func TestDeferRefusesATaskThatHasRun(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	var ve *ui.ValidationError

	running := create(t, c, ctx)
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: running.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Defer(ctx, running.ID, ui.DeferOptions{}); !errors.As(err, &ve) {
		t.Fatalf("deferring a running task: error = %v, want a ValidationError", err)
	}

	completed := create(t, c, ctx)
	done := baseTime.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: completed.ID, CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if err := c.Defer(ctx, completed.ID, ui.DeferOptions{}); !errors.As(err, &ve) {
		t.Fatalf("deferring a completed task: error = %v, want a ValidationError", err)
	}
	stored, err := store.GetTask(ctx, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DeferredAt != nil {
		t.Fatal("a refused deferral put the task aside anyway")
	}

	var nf *ui.NotFoundError
	if err := c.Defer(ctx, "404", ui.DeferOptions{}); !errors.As(err, &nf) {
		t.Fatalf("deferring an unknown task: error = %v, want a NotFoundError", err)
	}
	if err := c.Undefer(ctx, "404"); !errors.As(err, &nf) {
		t.Fatalf("undeferring an unknown task: error = %v, want a NotFoundError", err)
	}
}

func TestSubmitOptsIntoTheMergeQueue(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := c.Submit(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoMerge {
		t.Fatalf("autoMerge = %v, want true after Submit", got.AutoMerge)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.AutoMerge {
		t.Fatalf("stored autoMerge = %v, want true", stored.AutoMerge)
	}
	// Submitting an already-submitted task is a no-op rather than an error.
	if err := c.Submit(ctx, task.ID); err != nil {
		t.Fatalf("submitting twice: %v", err)
	}
}

func TestSubmitNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	err := c.Submit(ctx, "404")
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %v, want a NotFoundError", err)
	}
}

func TestAddCommentAppendsToTheConversation(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := c.AddComment(ctx, task.ID, "any progress?", nil); err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Body != "any progress?" {
		t.Fatalf("comments = %+v, want the one just posted", detail.Comments)
	}
	if detail.Comments[0].Author != "alice" {
		t.Fatalf("comment author = %q, want the configured actor", detail.Comments[0].Author)
	}
	if err := c.AddComment(ctx, task.ID, "   ", nil); err == nil {
		t.Fatal("an empty comment was accepted")
	}
}

// TestAddCommentAcceptsAttachmentsWithNoBody covers bwsalmon/agents#522's
// "attachable to follow-on comments" for the one case a bare body check
// alone would get wrong: a comment that carries only a file is not the
// same as one with nothing to say at all, so it must not be rejected the
// way an all-whitespace body with no attachment is just above.
func TestAddCommentAcceptsAttachmentsWithNoBody(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	upload := ui.AttachmentUpload{
		Filename: "screenshot.png", ContentType: "image/png",
		Content: base64.StdEncoding.EncodeToString([]byte("fake png bytes")),
	}
	if err := c.AddComment(ctx, task.ID, "", []ui.AttachmentUpload{upload}); err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 1 {
		t.Fatalf("comments = %+v, want exactly one", detail.Comments)
	}
	got := detail.Comments[0].Attachments
	if len(got) != 1 || got[0].Filename != "screenshot.png" || got[0].ContentType != "image/png" {
		t.Fatalf("comment attachments = %+v, want one screenshot.png (image/png)", got)
	}
	if got[0].Size != int64(len("fake png bytes")) {
		t.Fatalf("attachment size = %d, want %d", got[0].Size, len("fake png bytes"))
	}

	content, err := c.Attachment(ctx, task.ID, got[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(content.Content) != "fake png bytes" {
		t.Fatalf("attachment content = %q, want %q", content.Content, "fake png bytes")
	}
}

// TestCreateTaskStoresAttachments covers the other half of bwsalmon/
// agents#522: a file carried by the task's own body (CommentID nil),
// landing on TaskDetail.Attachments rather than any comment's.
func TestCreateTaskStoresAttachments(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Fix the thing", Repo: "acme/widgets", Approved: true,
		Attachments: []ui.AttachmentUpload{
			{Filename: "repro.zip", ContentType: "application/zip", Content: base64.StdEncoding.EncodeToString([]byte("PK\x03\x04fake"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attachments) != 1 || detail.Attachments[0].Filename != "repro.zip" {
		t.Fatalf("task attachments = %+v, want one repro.zip", detail.Attachments)
	}
	if len(detail.Comments) != 0 {
		t.Fatalf("a task-body attachment leaked into the conversation: %+v", detail.Comments)
	}
}

// TestCreateTaskRejectsAnOversizedAttachment proves the size limit is
// actually enforced, and that nothing is left behind when it rejects a
// request -- the task must not exist at all, not exist with some
// attachments missing.
func TestCreateTaskRejectsAnOversizedAttachment(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	oversized := base64.StdEncoding.EncodeToString(make([]byte, ui.MaxAttachmentSize+1))
	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Fix the thing", Repo: "acme/widgets",
		Attachments: []ui.AttachmentUpload{{Filename: "huge.bin", Content: oversized}},
	})
	if err == nil {
		t.Fatal("an oversized attachment was accepted")
	}
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v (%T), want a *ui.ValidationError", err, err)
	}
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("a task was left behind despite the rejected attachment: %+v", tasks)
	}
}

// GetTask surfaces every one of a task's runs, oldest first, so the UI
// can show attempts and their status in order (bwsalmon/agents#445)
// rather than just the failure streak's own count and most recent
// reason.
func TestGetTaskListsEveryAttemptOldestFirst(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1",
		Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", baseTime.Add(10*time.Minute), "failed", "build error"); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r2", TaskID: task.ID, Sandbox: "s1",
		Attempt: 2, StartedAt: baseTime.Add(time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempts = %+v, want 2", detail.Attempts)
	}
	first, second := detail.Attempts[0], detail.Attempts[1]
	if first.Number != 1 || first.Outcome != "failed" || first.Detail != "build error" || first.FinishedAt == nil {
		t.Fatalf("attempts[0] = %+v, want attempt 1, failed, build error, finished", first)
	}
	if second.Number != 2 || second.Outcome != "" || second.FinishedAt != nil {
		t.Fatalf("attempts[1] = %+v, want attempt 2, still running", second)
	}
}

// Each attempt carries the model and framework it actually ran on, as
// dispatch recorded them -- which, for a task left to the rotation, need
// not be the same from one attempt to the next -- and an attempt with
// none recorded carries none.
func TestGetTaskCarriesTheModelEachAttemptRanOn(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	for i, m := range []string{"gemini-3.1-pro", "gemini-3.1-flash", ""} {
		id := []string{"r1", "r2", "r3"}[i]
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: task.ID, Sandbox: id,
			Attempt: i + 1, StartedAt: baseTime.Add(time.Duration(i) * time.Hour),
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if m != "" {
			if err := store.SetRunModel(ctx, id, m, "antigravity"); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.FinishRun(ctx, id, baseTime.Add(time.Duration(i)*time.Hour+time.Minute), "failed", "x"); err != nil {
			t.Fatal(err)
		}
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 3 {
		t.Fatalf("attempts = %+v, want 3", detail.Attempts)
	}
	for i, want := range []string{"gemini-3.1-pro", "gemini-3.1-flash", ""} {
		a := detail.Attempts[i]
		wantFramework := "antigravity"
		if want == "" {
			wantFramework = ""
		}
		if a.Model != want || a.AgentFramework != wantFramework {
			t.Errorf("attempts[%d] ran on %q/%q, want %q/%q", i, a.Model, a.AgentFramework, want, wantFramework)
		}
	}
}

// An attempt that broke before its agent ever started carries the phrase
// grain stopped on -- "cloning acme/widgets" -- so the attempts timeline
// can say how far setup actually got. Store.TaskActivity reads live runs
// only, by design, so that phrase was written and never read by anybody:
// the person looking at a setup-failed attempt had its detail ("this
// run's sandbox could not be prepared: ...") and nothing else.
//
// An attempt whose agent did start carries nothing here, however loudly
// it was narrating itself when it stopped: what update_status last said
// is the agent's account of its own work, not a diagnosis of the attempt,
// and Attempt's own doc comment gives the reasoning.
func TestGetTaskCarriesTheSetupPhraseOfAnAttemptThatNeverReachedItsAgent(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1",
		Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTaskActivity(ctx, task.ID, "cloning acme/widgets", baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", baseTime.Add(2*time.Minute), "setup-failed",
		"this run's sandbox could not be prepared: no such repo"); err != nil {
		t.Fatal(err)
	}

	// The second attempt reached its agent, said something through
	// update_status, and then failed on its own account.
	if err := store.StartRun(ctx, model.Run{
		ID: "r2", TaskID: task.ID, Sandbox: "s2",
		Attempt: 2, StartedAt: baseTime.Add(time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunAgentStarted(ctx, "r2", baseTime.Add(time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTaskActivity(ctx, task.ID, "waiting for CI on the second push",
		baseTime.Add(time.Hour+2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r2", baseTime.Add(2*time.Hour), "failed",
		"exceeded max turns"); err != nil {
		t.Fatal(err)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempts = %+v, want 2", detail.Attempts)
	}
	brokeInSetup, reachedItsAgent := detail.Attempts[0], detail.Attempts[1]
	if brokeInSetup.SetupNote != "cloning acme/widgets" {
		t.Errorf("setup-failed attempt's SetupNote = %q, want the phrase it broke on", brokeInSetup.SetupNote)
	}
	if brokeInSetup.SetupNoteAt == nil || !brokeInSetup.SetupNoteAt.Equal(baseTime.Add(time.Minute)) {
		t.Errorf("SetupNoteAt = %v, want %v -- when grain said it", brokeInSetup.SetupNoteAt, baseTime.Add(time.Minute))
	}
	if reachedItsAgent.SetupNote != "" || reachedItsAgent.SetupNoteAt != nil {
		t.Errorf("attempt whose agent ran = %+v, want no setup note: those words are the agent's own",
			reachedItsAgent)
	}
}

// TestGetTaskHidesFailedAttemptsOnceTheTaskHasCompleted covers
// bwsalmon/agents#514, the sibling of the bug model.Transitions' own
// guard already fixed for bwsalmon/agents#502. orchestrator.
// salvagePushedBranch turns a pushed branch into a pull request (and the
// task into StateCompleted) even for a run whose own outcome stays
// "failed" forever -- an agent that commits, pushes, and then runs out of
// turns did the work; only the ending failed (orchestrator/cycle.go's own
// comment, orchestrator/finish_test.go's
// TestRunCycleOpensAPullRequestForABranchAFailedRunAlreadyPushed). That
// leaves task_streak sitting at 1 or more forever, since a "succeeded"
// outcome is the only thing that resets it. GetTask read that streak
// straight onto FailedAttempts/LastFailureAt/LastFailureReason with no
// regard for whether the task had since completed or closed -- unlike
// model.StateOf and model.Transitions, which both give obs.CompletedAt/
// ClosedAt precedence over the streak -- so a task's own detail view kept
// showing "1 consecutive failed attempt" (grain get's own "failed
// attempts: N in a row" line) long after the task plainly succeeded.
func TestGetTaskHidesFailedAttemptsOnceTheTaskHasCompleted(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1",
		Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", baseTime.Add(10*time.Minute), "failed", "exceeded max turns (2) without a final answer"); err != nil {
		t.Fatal(err)
	}
	// The pull request the salvage opened, submitted -- which is what
	// makes this completion read 'completed' rather than one of the two
	// post-run states that are waiting on a person (model.StateOf). The
	// state is beside the point of the test; the failure streak under it
	// is the point, and it is the same streak either way.
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
		tk.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completedAt := baseTime.Add(11 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &completedAt}); err != nil {
		t.Fatal(err)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != model.StateCompleted {
		t.Fatalf("state = %s, want completed", detail.State)
	}
	if detail.FailedAttempts != 0 || detail.LastFailureAt != nil || detail.LastFailureReason != "" {
		t.Fatalf("FailedAttempts = %d, LastFailureAt = %v, LastFailureReason = %q, want all zero on a completed task",
			detail.FailedAttempts, detail.LastFailureAt, detail.LastFailureReason)
	}
}

// TestRetryClearsAFailedTasksStreak covers bwsalmon/agents#403's own
// "Retry" button (Client.Retry, the UI's handleRetry): once a task has
// failed model.MaxConsecutiveFailures times in a row it reads StateFailed
// forever -- nothing else ever resets task_streak's own count -- until a
// human retries it. Retry itself only stamps Observation.RetryRequestedAt
// (Client.Retry's own doc comment); this proves that stamp actually
// carries all the way through Store.FailureStreak to State and back to a
// dispatchable task, the same round trip TestGetTaskHidesFailedAttempts
// OnceTheTaskHasCompleted proves for completion instead.
func TestRetryClearsAFailedTasksStreak(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	for i := 0; i < model.MaxConsecutiveFailures; i++ {
		id := "r" + strconv.Itoa(i+1)
		started := baseTime.Add(time.Duration(i) * time.Hour)
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: task.ID, Sandbox: "s1",
			Attempt: i + 1, StartedAt: started,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, started.Add(time.Minute), "failed", "boom"); err != nil {
			t.Fatal(err)
		}
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != model.StateFailed {
		t.Fatalf("state = %s, want failed after %d consecutive failures", detail.State, model.MaxConsecutiveFailures)
	}
	if detail.FailedAttempts != model.MaxConsecutiveFailures {
		t.Fatalf("FailedAttempts = %d, want %d", detail.FailedAttempts, model.MaxConsecutiveFailures)
	}

	retryAt := baseTime.Add(model.MaxConsecutiveFailures * time.Hour)
	c.Now = func() time.Time { return retryAt }
	if err := c.Retry(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	detail, err = c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != model.StateQueued {
		t.Fatalf("state = %s, want queued once retried: an approved task with no failures left is dispatchable", detail.State)
	}
	if detail.FailedAttempts != 0 || detail.LastFailureAt != nil || detail.LastFailureReason != "" {
		t.Fatalf("FailedAttempts = %d, LastFailureAt = %v, LastFailureReason = %q, want all zero once retried",
			detail.FailedAttempts, detail.LastFailureAt, detail.LastFailureReason)
	}

	// The failed attempts themselves are still on record -- retrying
	// forgives the streak, it does not rewrite history.
	if len(detail.Attempts) != model.MaxConsecutiveFailures {
		t.Fatalf("attempts = %d, want retrying to leave every past attempt on record", len(detail.Attempts))
	}
}

// TestRetryOnATaskWithNoFailureIsAHarmlessNoOp covers Client.Retry's own
// doc comment: calling it on a task that is not currently failed must not
// error or otherwise disturb its state.
func TestRetryOnATaskWithNoFailureIsAHarmlessNoOp(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := c.Retry(ctx, task.ID); err != nil {
		t.Fatalf("Retry on a never-run task: %v", err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != model.StateQueued {
		t.Fatalf("state = %s, want queued: retrying a task with nothing to retry must be a no-op", detail.State)
	}
}

// TestRetryOnAnUnknownTaskIsNotFound matches the same NotFoundError every
// other single-task Client method returns for an id nothing is behind
// (Close, Reopen, AddComment).
func TestRetryOnAnUnknownTaskIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	err := c.Retry(ctx, "nope")
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
}

// TestGetTaskListsPullRequestEventsOldestFirst covers bwsalmon/agents#493
// -- "show PR events in the task timeline" -- the same projection
// TestGetTaskListsEveryAttemptOldestFirst above already exercises for
// attempts, but for Observation's own PrOpenedAt/PrMergedAt/PrClosedAt.
func TestGetTaskListsPullRequestEventsOldestFirst(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	opened := baseTime
	merged := baseTime.Add(2 * time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, PrOpenedAt: &opened, PrMergedAt: &merged,
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.PullRequestEvents) != 2 {
		t.Fatalf("pull request events = %+v, want 2", detail.PullRequestEvents)
	}
	if got := detail.PullRequestEvents[0]; got.Kind != "opened" || !got.At.Equal(opened) {
		t.Fatalf("events[0] = %+v, want opened at %v", got, opened)
	}
	if got := detail.PullRequestEvents[1]; got.Kind != "merged" || !got.At.Equal(merged) {
		t.Fatalf("events[1] = %+v, want merged at %v", got, merged)
	}
}

func TestCreateAndUpdateTaskWithIssue(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Task with issue",
		Issue: 99,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.Issue != 99 {
		t.Fatalf("task.Issue = %d, want 99", task.Issue)
	}

	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.Issue != 99 {
		t.Fatalf("got.Issue = %d, want 99", got.Issue)
	}

	newIssue := 105
	updated, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{
		Issue: &newIssue,
	})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if updated.Issue != 105 {
		t.Fatalf("updated.Issue = %d, want 105", updated.Issue)
	}

	reread, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("reread Task: %v", err)
	}
	if reread.Issue != 105 {
		t.Fatalf("reread.Issue = %d, want 105", reread.Issue)
	}
}

// The suspended agent session reaches the task pane, which is the whole
// of what a person can do with it: the token opens the conversation
// grain will resume on the task's next run (model.SuspendedSession),
// and there is nowhere else to read it.
func TestGetTaskCarriesTheSuspendedSession(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	left := baseTime.Add(time.Hour)
	if err := store.SetSuspendedSession(ctx, task.ID, &model.SuspendedSession{
		Framework: model.AgentFrameworkClaude, Token: "sess-1", RunID: "r7", At: left,
	}, left); err != nil {
		t.Fatal(err)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := detail.SuspendedSession
	if got == nil {
		t.Fatal("the task detail carries no suspended session, so nothing on the pane says the next run will carry on")
	}
	if got.Token != "sess-1" || got.Framework != model.AgentFrameworkClaude || got.RunID != "r7" {
		t.Fatalf("suspended session = %+v, want the one run r7 left behind", got)
	}
	if !got.At.Equal(left) {
		t.Fatalf("suspended session At = %v, want %v -- how old the conversation is is most of what says whether it is still there", got.At, left)
	}
}

// And the ordinary case: a task nothing has suspended carries nothing,
// so the pane renders no section at all rather than an empty one.
func TestGetTaskHasNoSuspendedSessionForATaskThatHasNotRun(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.SuspendedSession != nil {
		t.Fatalf("suspended session = %+v, want none", detail.SuspendedSession)
	}
}

// TestGetTaskHasNoPullRequestEventsForATaskWithNoPullRequest is the empty
// case TestGetTaskListsPullRequestEventsOldestFirst's non-nil Observation
// does not cover: a task nobody has linked a pull request to yet (and so
// orchestrator.SyncPullRequests has never observed).
func TestGetTaskHasNoPullRequestEventsForATaskWithNoPullRequest(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.PullRequestEvents) != 0 {
		t.Fatalf("pull request events = %+v, want none", detail.PullRequestEvents)
	}
}

// AttemptTranscript is a single attempt's own recorded agent transcript,
// fetched on demand rather than carried on every Attempt GetTask already
// lists (bwsalmon/agents#446 -- "show attempt agent logs").
func TestAttemptTranscript(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1",
		Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", baseTime.Add(10*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunTranscript(ctx, "r1", "read the file, then pushed"); err != nil {
		t.Fatal(err)
	}

	got, err := c.AttemptTranscript(ctx, task.ID, 1)
	if err != nil || got != "read the file, then pushed" {
		t.Fatalf("AttemptTranscript = (%q, %v), want the transcript SetRunTranscript recorded", got, err)
	}

	if _, err := c.AttemptTranscript(ctx, task.ID, 2); err == nil {
		t.Fatal("expected an error for an attempt number with no run behind it")
	}
	if _, err := c.AttemptTranscript(ctx, "nonexistent", 1); err == nil {
		t.Fatal("expected an error for a nonexistent task")
	}
}

// fakeLiveTranscript is a ui.LiveTranscript a test can script with a
// canned response (or none at all) per run ID, without a real transcript
// file anywhere on disk.
type fakeLiveTranscript map[string]string

func (f fakeLiveTranscript) Tail(runID string) (string, bool, error) {
	text, ok := f[runID]
	return text, ok, nil
}

// TestAttemptTranscriptPrefersTheLiveTranscriptWhileARunIsStillGoing is
// bwsalmon/agents#467's whole point: an attempt with no FinishedAt yet
// reads its transcript-in-progress from Config.LiveTranscripts rather
// than waiting on Store.RunTranscript, which has nothing until
// orchestrator.RunDispatch's own SetRunTranscript call lands after the
// run is over.
func TestAttemptTranscriptPrefersTheLiveTranscriptWhileARunIsStillGoing(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	c.Config.LiveTranscripts = fakeLiveTranscript{"r1": "still working on it"}

	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1",
		Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	got, err := c.AttemptTranscript(ctx, task.ID, 1)
	if err != nil || got != "still working on it" {
		t.Fatalf("AttemptTranscript = (%q, %v), want the live transcript", got, err)
	}
}

// TestAttemptTranscriptFallsBackToTheStoreOnceALiveRunFinishes proves the
// other half: once FinishedAt is set, AttemptTranscript reads
// Store.RunTranscript even when Config.LiveTranscripts still has
// something recorded for that run ID -- the store, not a file that
// orchestrator.RunDispatch has by now already deleted, is authoritative
// for a finished attempt.
func TestAttemptTranscriptFallsBackToTheStoreOnceALiveRunFinishes(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	c.Config.LiveTranscripts = fakeLiveTranscript{"r1": "stale in-progress text"}

	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1",
		Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", baseTime.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunTranscript(ctx, "r1", "the real, finished story"); err != nil {
		t.Fatal(err)
	}

	got, err := c.AttemptTranscript(ctx, task.ID, 1)
	if err != nil || got != "the real, finished story" {
		t.Fatalf("AttemptTranscript = (%q, %v), want the stored transcript", got, err)
	}
}

// TestAttemptTranscriptFallsBackToTheStoreWhenLiveHasNothingYet covers a
// still-running attempt whose framework has not written anything to its
// live transcript file yet (or was never given one -- a Framework, for
// now): AttemptTranscript should fall back to Store.RunTranscript (empty,
// but not an error) rather than surface that as a failure.
func TestAttemptTranscriptFallsBackToTheStoreWhenLiveHasNothingYet(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	c.Config.LiveTranscripts = fakeLiveTranscript{}

	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1",
		Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	got, err := c.AttemptTranscript(ctx, task.ID, 1)
	if err != nil || got != "" {
		t.Fatalf("AttemptTranscript = (%q, %v), want (\"\", nil)", got, err)
	}
}

// Replying to a parked task resumes it. This used to take two separate
// acts -- post a comment AND re-apply the trigger label so the next poll
// would notice -- and forgetting the second left the task parked forever.
func TestReplyingToAParkedTaskResumesIt(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	agent := model.Principal{Kind: model.PrincipalAgent, ID: "run-1"}
	questionID, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &agent,
		},
		Body: "which endpoint?", CreatedAt: baseTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.PendingQuestionCommentID = &questionID
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Task(ctx, task.ID); got.State != model.StateAwaitingReply {
		t.Fatalf("state before the reply = %q, want awaiting_reply", got.State)
	}

	if err := c.AddComment(ctx, task.ID, "the second one", nil); err != nil {
		t.Fatal(err)
	}

	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.StateQueued {
		t.Fatalf("state after the reply = %q, want queued: replying is what resumes a parked task", got.State)
	}
	// And the relayed question still renders as grain speaking for the
	// agent rather than as grain's own.
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Comments[0].OnBehalfOf != "run-1" {
		t.Fatalf("relayed question = %+v, want it attributed on behalf of the run", detail.Comments[0])
	}
}

func TestCloseAndReopen(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Task(ctx, task.ID); got.State != model.StateClosed {
		t.Fatalf("state after close = %q, want closed", got.State)
	}
	if err := c.Reopen(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Task(ctx, task.ID); got.State != model.StateQueued {
		t.Fatalf("state after reopen = %q, want back to queued", got.State)
	}
}

func TestStop(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	canceller := &recordingTaskCanceller{}
	c.Config.TaskCanceller = canceller

	if err := c.Stop(ctx, task.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.State != model.StateAwaitingReply {
		t.Fatalf("state after stop = %q, want awaiting_reply", got.State)
	}
	if len(canceller.cancelled) != 1 || canceller.cancelled[0] != task.ID {
		t.Fatalf("cancelled = %+v, want [%s]", canceller.cancelled, task.ID)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil || len(comments) == 0 {
		t.Fatalf("comments: %v, len = %d", err, len(comments))
	}
	if comments[len(comments)-1].Body != "Turn stopped by user" {
		t.Errorf("last comment = %q, want 'Turn stopped by user'", comments[len(comments)-1].Body)
	}
}

// A stopped turn is "Asked" in the UI like a question is, so every view
// of the task has to say which of the two it is (Task.TurnStopped).
func TestStoppedTurnIsMarkedOnEveryView(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.TaskCanceller = &recordingTaskCanceller{}
	stopped := create(t, c, ctx)
	other := create(t, c, ctx)
	if err := c.Stop(ctx, stopped.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got, err := c.Task(ctx, stopped.ID); err != nil || !got.TurnStopped {
		t.Errorf("Task(stopped).TurnStopped = %v (err %v), want true", got.TurnStopped, err)
	}
	if got, err := c.GetTask(ctx, stopped.ID); err != nil || !got.TurnStopped {
		t.Errorf("GetTask(stopped).TurnStopped = %v (err %v), want true", got.TurnStopped, err)
	}
	list, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range list {
		want := task.ID == stopped.ID
		if task.ID != stopped.ID && task.ID != other.ID {
			continue
		}
		if task.TurnStopped != want {
			t.Errorf("ListTasks: %s TurnStopped = %v, want %v", task.ID, task.TurnStopped, want)
		}
	}

	// Answering it un-parks the task, and with it the mark.
	if err := c.AddComment(ctx, stopped.ID, "carry on", nil); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if got, _ := c.Task(ctx, stopped.ID); got.TurnStopped {
		t.Errorf("TurnStopped after a reply = true, want false (state %q)", got.State)
	}
}

func TestListTasksCarriesEveryTaskWithItsState(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	queued := create(t, c, ctx)
	proposed, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "maybe"})
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("listed %d tasks, want 2", len(tasks))
	}
	states := map[string]model.State{}
	for _, task := range tasks {
		states[task.ID] = task.State
	}
	if states[queued.ID] != model.StateQueued || states[proposed.ID] != model.StateProposed {
		t.Fatalf("states = %v, want each task's own", states)
	}
}

// TestListTasksIsDispatchOrder: the list a UI or CLI gets reads
// top-to-bottom in the order grain will work through it (grain/task-201).
// Three tasks created in order, with NewestFirst left at its default, are
// listed in that same order -- each one joined the end of the backlog, so
// the first one filed is both at the top and the next to run. This used
// to be the reverse (newest first), which put the task that would run
// next at the bottom of the list.
func TestListTasksIsDispatchOrder(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	first := create(t, c, ctx)
	second := create(t, c, ctx)
	third := create(t, c, ctx)

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{tasks[0].ID, tasks[1].ID, tasks[2].ID}
	want := []string{first.ID, second.ID, third.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListTasks order = %v, want dispatch order %v", got, want)
	}

	// The list is not merely in some fixed order: it is Ready's order,
	// which is the whole claim being made to whoever reads it.
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ready, got) {
		t.Fatalf("Ready = %v, want the listed order %v", ready, got)
	}
}

// TestNewestFirstSettingMovesNewTasksToTheFrontOfTheQueue is
// bwsalmon/agents#476's global switch, now that it is only about where
// new work joins the backlog (grain/task-201): with
// model.Config.NewestFirst set, a task created after two others is
// dispatched before them instead of after -- and, because the list is
// dispatch order, it is at the top of it for exactly that reason.
func TestNewestFirstSettingMovesNewTasksToTheFrontOfTheQueue(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	newestFirst := true
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{NewestFirst: &newestFirst}); err != nil {
		t.Fatal(err)
	}

	first := create(t, c, ctx)
	second := create(t, c, ctx)
	third := create(t, c, ctx)

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{tasks[0].ID, tasks[1].ID, tasks[2].ID}
	want := []string{third.ID, second.ID, first.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListTasks order under NewestFirst = %v, want newest first %v", got, want)
	}

	// And it is at the top because it runs first, not as a display
	// convention of its own: Ready agrees, task for task.
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready under NewestFirst = %v, want newest dispatched first %v", ready, want)
	}
}

// TestCreateTaskFilesAtTheEndTheRequestNames is grain/task-202's own
// half of the same choice: whoever files a task says which end of the
// backlog it joins on the request itself
// (ui.CreateTaskRequest.AtFront), rather than by going and flipping a
// deployment-wide setting first. The request wins over what is stored --
// here NewestFirst is off and the third task still runs next.
func TestCreateTaskFilesAtTheEndTheRequestNames(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	first := create(t, c, ctx)
	second := create(t, c, ctx)
	third := createAt(t, c, ctx, true)

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{third.ID, first.ID, second.ID}
	if !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready after filing one at the front = %v, want %v", ready, want)
	}
}

// TestCreateTaskRemembersWhichEndTheLastTaskJoined: the choice is
// sticky, which is the whole of grain/task-202 beyond the field itself.
// A task filed at the front stores that as the deployment's own default
// (model.Config.NewestFirst), so the next task filed with no opinion at
// all -- another form, the CLI, a second person -- joins the same end,
// and the Settings pane and the new-task form both read it back.
func TestCreateTaskRemembersWhichEndTheLastTaskJoined(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	first := create(t, c, ctx)
	second := createAt(t, c, ctx, true)
	// No opinion: it inherits what the task before it chose rather than
	// falling back to the end of the backlog.
	third := create(t, c, ctx)

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{third.ID, second.ID, first.ID}
	if !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready after remembering the front = %v, want %v", ready, want)
	}

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.NewestFirst {
		t.Fatalf("Settings.NewestFirst after filing a task at the front = false, want true")
	}

	// And back the other way, so it is a memory of the last filing
	// rather than a one-way switch.
	fourth := createAt(t, c, ctx, false)
	fifth := create(t, c, ctx)
	if ready, err = store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	want = []string{third.ID, second.ID, first.ID, fourth.ID, fifth.ID}
	if !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready after remembering the end again = %v, want %v", ready, want)
	}
}

func TestListTasksOrdersClosedTasksNewestFirst(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	open1 := create(t, c, ctx)
	open2 := create(t, c, ctx)

	c1 := create(t, c, ctx)
	t1 := baseTime.Add(1 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: c1.ID, ClosedAt: &t1}); err != nil {
		t.Fatal(err)
	}

	c2 := create(t, c, ctx)
	t2 := baseTime.Add(3 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: c2.ID, ClosedAt: &t2}); err != nil {
		t.Fatal(err)
	}

	c3 := create(t, c, ctx)
	t3 := baseTime.Add(2 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: c3.ID, ClosedAt: &t3}); err != nil {
		t.Fatal(err)
	}

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 5 {
		t.Fatalf("ListTasks = %d tasks, want 5", len(tasks))
	}

	got := []string{tasks[0].ID, tasks[1].ID, tasks[2].ID, tasks[3].ID, tasks[4].ID}
	want := []string{open1.ID, open2.ID, c2.ID, c3.ID, c1.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListTasks order = %v, want open in dispatch order and closed newest-first %v", got, want)
	}
}

// A task filed with no opinion changes nothing about where the next one
// goes: only a stated choice is remembered, so a scheduled or scripted
// filing cannot quietly reset what a human picked.
func TestCreateTaskWithNoOpinionLeavesTheRememberedEndAlone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	createAt(t, c, ctx, true)
	create(t, c, ctx)

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.NewestFirst {
		t.Fatalf("Settings.NewestFirst after a filing that stated nothing = false, want the remembered true")
	}
}

// TestReorderMovesATaskInTheBacklog exercises Client.Reorder end to end
// -- the endpoint TaskList.jsx's drag-and-drop drop handler calls -- and
// checks the move is visible through both ListTasks (the display) and
// Ready (the dispatch order), since both read the same OrderKey column.
func TestReorderMovesATaskInTheBacklog(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	first := create(t, c, ctx)
	second := create(t, c, ctx)
	third := create(t, c, ctx)

	// Backlog (dispatch) order today is oldest first: first, second, third.
	// Move third to the very front -- "just before the following job" when
	// dropped at the head of the list, the same wording the issue uses.
	if err := c.Reorder(ctx, ui.ReorderRequest{IDs: []string{third.ID}, BeforeID: first.ID}); err != nil {
		t.Fatal(err)
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{third.ID, first.ID, second.ID}; !reflect.DeepEqual(ready, want) {
		t.Fatalf("Ready after Reorder = %v, want %v", ready, want)
	}

	// And the list a UI shows is that same order, not its reverse: a task
	// dragged to the head of the list is the one that runs next, which is
	// the whole point of being able to drag it there.
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{tasks[0].ID, tasks[1].ID, tasks[2].ID}
	if want := []string{third.ID, first.ID, second.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListTasks after Reorder = %v, want %v", got, want)
	}
}

func TestReorderValidatesIDsIsNotEmpty(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	err := c.Reorder(ctx, ui.ReorderRequest{})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Reorder with no ids: error = %v, want a ValidationError", err)
	}
}

// AuthorKind is what lets the frontend tell a task somebody filed by
// hand from one grain or a dispatched run filed for itself -- Author
// alone cannot, since a login, a run ID and a deployment name are all
// just strings. state.js's lastBaseForRepo reads it to keep a
// system-generated task's base branch out of the human's own "Base
// branch" default.
func TestAuthorKindCarriesTheFilingPrincipalsKind(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	filed := create(t, c, ctx)
	if filed.AuthorKind != string(model.PrincipalHuman) {
		t.Fatalf("AuthorKind of a task filed through the client = %q, want human", filed.AuthorKind)
	}

	id, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scheduled := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  "nightly sweep",
		Body:   "filed by a schedule",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
			Reason:      model.ReasonSchedule,
		},
		CreatedAt: &baseTime,
	}
	if err := store.PutTask(ctx, scheduled); err != nil {
		t.Fatal(err)
	}

	got, err := c.Task(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthorKind != string(model.PrincipalAutomation) {
		t.Fatalf("AuthorKind of a scheduled task = %q, want automation", got.AuthorKind)
	}
}

// GeneratedFrom is read off the task's own LinkProposedBy link -- the
// same provenance relayProposedTasks (pkg/orchestrator/finish.go) sets
// automatically on every task a propose_task call files, surfaced here
// for the UI rather than left for a human to dig out of the store.
func TestGeneratedFromReadsOffTheProposedByLink(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	source := create(t, c, ctx)

	id, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proposal := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  "proposed child",
		Body:   "filed by the parent's own run",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
			Reason:      model.ReasonDirect,
		},
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: source.ID}},
		CreatedAt: &baseTime,
	}
	if err := store.PutTask(ctx, proposal); err != nil {
		t.Fatal(err)
	}

	got, err := c.Task(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.GeneratedFrom != source.ID {
		t.Fatalf("GeneratedFrom = %q, want %q", got.GeneratedFrom, source.ID)
	}

	if source.GeneratedFrom != "" {
		t.Fatalf("source task GeneratedFrom = %q, want empty: nothing proposed it", source.GeneratedFrom)
	}
}

// Stacked is true only for a task whose Origin.Reason is model.ReasonFix
// -- the merge queue's own automatic fix for another task's pull
// request (bwsalmon/agents#378) -- and false for an ordinary
// propose_task child, even though both carry a GeneratedFrom link. The
// frontend nests the former under the task named by GeneratedFrom and
// leaves the latter as a task of its own.
func TestStackedIsTrueOnlyForAFixTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	source := create(t, c, ctx)

	fixID, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixTask := model.Task{
		ID:     fixID,
		Intent: model.IntentImplement,
		Title:  "fix",
		Body:   "filed by the merge queue",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "merge-queue"}},
			Reason:      model.ReasonFix,
		},
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: source.ID}},
		CreatedAt: &baseTime,
	}
	if err := store.PutTask(ctx, fixTask); err != nil {
		t.Fatal(err)
	}

	proposalID, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proposal := model.Task{
		ID:     proposalID,
		Intent: model.IntentImplement,
		Title:  "proposed child",
		Body:   "filed by the parent's own run",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
			Reason:      model.ReasonDirect,
		},
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: source.ID}},
		CreatedAt: &baseTime,
	}
	if err := store.PutTask(ctx, proposal); err != nil {
		t.Fatal(err)
	}

	gotFix, err := c.Task(ctx, fixID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotFix.Stacked {
		t.Fatalf("fix task Stacked = false, want true")
	}

	gotProposal, err := c.Task(ctx, proposalID)
	if err != nil {
		t.Fatal(err)
	}
	if gotProposal.Stacked {
		t.Fatalf("proposed child Stacked = true, want false")
	}
}

// MergeQueueBlockedAt is bwsalmon/agents#494's own signal that a
// completed task's pull request needs a human, not another automatic
// merge-queue attempt -- ListTasks, Task and GetTask each project it
// off model.Observation through a different path (a bulk query, a
// single Store.GetObservation call, and one already fetched for
// Transitions), so all three are worth covering here rather than
// trusting they agree.
func TestTaskSurfacesMergeQueueBlockedAt(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeQueueBlockedAt != nil {
		t.Fatalf("MergeQueueBlockedAt = %v, want nil before the merge queue gives up", got.MergeQueueBlockedAt)
	}

	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, CompletedAt: &baseTime, MergeQueueBlockedAt: &baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	got, err = c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeQueueBlockedAt == nil || !got.MergeQueueBlockedAt.Equal(baseTime) {
		t.Fatalf("Task: MergeQueueBlockedAt = %v, want %v", got.MergeQueueBlockedAt, baseTime)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.MergeQueueBlockedAt == nil || !detail.MergeQueueBlockedAt.Equal(baseTime) {
		t.Fatalf("GetTask: MergeQueueBlockedAt = %v, want %v", detail.MergeQueueBlockedAt, baseTime)
	}

	list, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].MergeQueueBlockedAt == nil || !list[0].MergeQueueBlockedAt.Equal(baseTime) {
		t.Fatalf("ListTasks: MergeQueueBlockedAt = %v, want %v", list[0].MergeQueueBlockedAt, baseTime)
	}
}

// Repairing is the same shape of signal for the other thing the merge
// queue does to a task it is driving: sends it back to an agent to repair
// its own pull request branch (model.Observation.MergeQueueRepairAt).
// Such a task reads 'running' or 'queued' like any other attempt, so
// without this a list has no way to tell "still being written" from
// "written, merged nowhere, and now being unstuck" -- which is what the
// frontend colours the running mark on.
//
// The three projections take three different paths to it, exactly as
// above, and the bulk one (Store.MergeQueueRepairing) is a second
// implementation of model.Observation.RepairInFlight in SQL -- so all
// three are worth covering rather than trusting they agree.
func TestTaskSurfacesAMergeQueueRepairInFlight(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)

	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repairing {
		t.Fatal("Repairing = true for a task the merge queue has not repaired")
	}

	// The requeue: what orchestrator.requeueForRepair writes.
	askedAt := baseTime.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, MergeQueueRepairAt: &askedAt,
	}); err != nil {
		t.Fatal(err)
	}

	got, err = c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Repairing {
		t.Fatal("Task: Repairing = false while a repair is in flight")
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Repairing {
		t.Fatal("GetTask: Repairing = false while a repair is in flight")
	}
	list, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Repairing {
		t.Fatalf("ListTasks: Repairing = %v, want true", list)
	}

	// The repair run finishes: the task completes again, and it is an
	// ordinary completed task once more.
	finished := askedAt.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, MergeQueueRepairAt: &askedAt, CompletedAt: &finished,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repairing {
		t.Fatal("Task: Repairing = true after the repair completed")
	}
	list, err = c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Repairing {
		t.Fatalf("ListTasks: Repairing = %v after the repair completed, want false", list)
	}
}

func TestTaskNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.Task(ctx, "404")
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %v, want a NotFoundError", err)
	}
}

func TestGetSettingsIsUnconfiguredOnAFreshStore(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	got, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Configured {
		t.Fatalf("got %+v, want Configured false before anything has ever been saved", got)
	}
}

func firstSettings() ui.UpdateSettingsRequest {
	pollInterval, maxWorkers, host := "30s", 1, "github.com"
	return ui.UpdateSettingsRequest{
		PollInterval: &pollInterval, MaxMergesPerRepo: &maxWorkers,
		GitHubHost: &host,
	}
}

func TestUpdateSettingsFirstTimeRequiresTheCoreFields(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	full := firstSettings()
	if _, err := c.UpdateSettings(ctx, full); err != nil {
		t.Fatalf("saving with every core field given: %v", err)
	}

	c2, _, ctx2 := testClient(t)
	maxMerges := 1
	_, err := c2.UpdateSettings(ctx2, ui.UpdateSettingsRequest{MaxMergesPerRepo: &maxMerges})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("saving settings for the first time with pollInterval missing: error = %v, want a ValidationError", err)
	}
}

func TestUpdateSettingsThenGetRoundTrips(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	got, err := c.UpdateSettings(ctx, firstSettings())
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if !got.Configured || got.PollInterval != "30s" || got.GitHubHost != "github.com" {
		t.Fatalf("UpdateSettings returned %+v", got)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read, got) {
		t.Fatalf("GetSettings = %+v, want the just-written %+v", read, got)
	}

	// And it actually landed in the store UpdateSettings itself reads and
	// writes -- not some copy this package keeps of its own.
	stored, err := store.GetConfig(ctx)
	if err != nil || stored == nil {
		t.Fatalf("store.GetConfig: (%+v, %v)", stored, err)
	}
	if stored.GitHubHost != "github.com" {
		t.Fatalf("store's own config = %+v", stored)
	}
}

// grain/task-63: a first save that never mentions maxMergers stores
// model.DefaultConfig's own value for it rather than a zero nobody
// maxWorkers, maxMergers and maxCIJobs used to round-trip through this
// API, each with its own default and its own validation. They are not
// settings any more (grain/task-28): a count belongs to a team, and the
// only deployment-wide capacity control is the sandbox budget. A request
// that still carries one is accepted and changes nothing, so an older UI
// or a script written against the old API goes on saving what it can.
func TestUpdateSettingsIgnoresTheCountsThatAreNoLongerSettings(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	before, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The old field names, sent as raw JSON since the request type no
	// longer has them.
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{}); err != nil {
		t.Fatalf("saving with nothing to change: %v", err)
	}
	after, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.MaxWorkers != before.MaxWorkers || after.MaxMergers != before.MaxMergers ||
		after.MaxCIJobs != before.MaxCIJobs {
		t.Errorf("the legacy count columns moved: %d/%d/%d -> %d/%d/%d",
			before.MaxWorkers, before.MaxMergers, before.MaxCIJobs,
			after.MaxWorkers, after.MaxMergers, after.MaxCIJobs)
	}

	// MaxWorkers is configurable again (defaulting to 10) and round-trips through Settings.
	if before.MaxWorkers != model.DefaultMaxWorkers {
		t.Errorf("default MaxWorkers = %d, want %d", before.MaxWorkers, model.DefaultMaxWorkers)
	}
	workers := 6
	updated, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{MaxWorkers: &workers})
	if err != nil {
		t.Fatalf("updating MaxWorkers: %v", err)
	}
	if updated.MaxWorkers != 6 {
		t.Errorf("updated.MaxWorkers = %d, want 6", updated.MaxWorkers)
	}
}

// A later partial update changes only the fields given, leaving
// everything else -- including fields with no UI equivalent yet, like
// GCPProject -- exactly as they were, the same UpdateTaskRequest
// convention.
func TestUpdateSettingsChangesOnlyTheFieldsGiven(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	turns := 12
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{MaxAgentTurns: &turns})
	if err != nil {
		t.Fatalf("partial update: %v", err)
	}
	if got.MaxAgentTurns != 12 {
		t.Fatalf("maxAgentTurns = %d, want 12", got.MaxAgentTurns)
	}
	if got.PollInterval != "30s" || got.GitHubHost != "github.com" {
		t.Fatalf("partial update changed fields it was not given: %+v", got)
	}
}

// bwsalmon/agents#534, grain/task-41: SandboxCPUs/SandboxMemoryMB/
// SandboxDiskGB (the deployment-wide
// default sandbox shape) round-trip through UpdateSettings/GetSettings
// the same as every other store-backed field, and 0 -- the "unset, use
// grain's own default shape" zero value -- is valid, unlike
// MaxWorkers's own "must be at least 1".
func TestUpdateSettingsSandboxShapeRoundTrips(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	cpus, memoryMB, diskGB := 4, 8192, 40
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxCPUs: &cpus, SandboxMemoryMB: &memoryMB, SandboxDiskGB: &diskGB,
	})
	if err != nil {
		t.Fatalf("setting sandbox shape: %v", err)
	}
	if got.SandboxCPUs != 4 || got.SandboxMemoryMB != 8192 || got.SandboxDiskGB != 40 {
		t.Fatalf("sandboxCpus/sandboxMemoryMb/sandboxDiskGb = %d/%d/%d, want 4/8192/40",
			got.SandboxCPUs, got.SandboxMemoryMB, got.SandboxDiskGB)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.SandboxCPUs != 4 || read.SandboxMemoryMB != 8192 || read.SandboxDiskGB != 40 {
		t.Fatalf("GetSettings sandboxCpus/sandboxMemoryMb/sandboxDiskGb = %d/%d/%d, want 4/8192/40",
			read.SandboxCPUs, read.SandboxMemoryMB, read.SandboxDiskGB)
	}

	zero := 0
	got, err = c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxCPUs: &zero, SandboxMemoryMB: &zero, SandboxDiskGB: &zero,
	})
	if err != nil {
		t.Fatalf("clearing sandbox shape: %v", err)
	}
	if got.SandboxCPUs != 0 || got.SandboxMemoryMB != 0 || got.SandboxDiskGB != 0 {
		t.Fatalf("sandboxCpus/sandboxMemoryMb/sandboxDiskGb after clearing = %d/%d/%d, want 0/0/0",
			got.SandboxCPUs, got.SandboxMemoryMB, got.SandboxDiskGB)
	}
}

func TestUpdateSettingsValidates(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	bad := "not-a-duration"
	empty := ""
	negative := -1
	zeroMerges := 0
	negativeCPUs := -1
	lowMemory := 64
	negativeDisk := -1
	cases := map[string]ui.UpdateSettingsRequest{
		"unparseable poll interval": {PollInterval: &bad},
		"zero max workers":          {MaxWorkers: &zeroMerges},
		"zero max merges per repo":  {MaxMergesPerRepo: &zeroMerges},
		"blank github host":         {GitHubHost: &empty},
		"negative max agent turns":  {MaxAgentTurns: &negative},
		"negative sandbox cpus":     {SandboxCPUs: &negativeCPUs},
		"sandbox memory below 128":  {SandboxMemoryMB: &lowMemory},
		"negative sandbox disk":     {SandboxDiskGB: &negativeDisk},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := c.UpdateSettings(ctx, req)
			var ve *ui.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
		})
	}
}

func TestTaskGoogleChatSpaceRoundTrip(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:           "chat-linked task",
		GoogleChatSpace: "spaces/AAAAAAAAAAA",
		NoRepo:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.GoogleChatSpace != "spaces/AAAAAAAAAAA" {
		t.Fatalf("CreateTask: GoogleChatSpace = %q, want spaces/AAAAAAAAAAA", task.GoogleChatSpace)
	}

	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GoogleChatSpace != "spaces/AAAAAAAAAAA" {
		t.Fatalf("Task: GoogleChatSpace = %q, want spaces/AAAAAAAAAAA", got.GoogleChatSpace)
	}

	newSpace := "spaces/BBBBBBBBBBB"
	updated, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{
		GoogleChatSpace: &newSpace,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GoogleChatSpace != newSpace {
		t.Fatalf("UpdateTask: GoogleChatSpace = %q, want %q", updated.GoogleChatSpace, newSpace)
	}

	empty := ""
	cleared, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{
		GoogleChatSpace: &empty,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.GoogleChatSpace != "" {
		t.Fatalf("UpdateTask cleared: GoogleChatSpace = %q, want empty", cleared.GoogleChatSpace)
	}
}
