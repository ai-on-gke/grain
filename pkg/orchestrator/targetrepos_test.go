package orchestrator_test

// The half of the target-repo allowlist that lives on the dispatch side
// (orchestrator.reconcileTargetRepos, grain/task-404).
//
// Everything that writes a repo grain would work in asks
// Config.TargetRepos on the way in -- pkg/ui's own
// TestUpdateTaskRefusesRetargetOffTheTargetRepoList and
// TestReplyDoesNotUnparkATaskStillOffTheTargetRepoList are that boundary
// -- and none of it can speak for the list moving underneath work that
// is already stored. A task queued while its repo was allowed, a task a
// schedule fired, a suite pass, a qualification plan: each of them
// reaches a narrowed list already approved and already dispatchable, and
// the git proxy authorizes their sandboxes against the task's own Target
// rather than against the list (pkg/gitproxy/authorize.go). So these
// tests run real cycles against a real store and assert on what was
// dispatched, since "would it be refused" is not the question -- nothing
// was refusing anything here.
//
// Tasks are filed straight into the store rather than through ui.Client,
// deliberately: that is exactly the shape of the three fire paths above
// (model.Store.fireTaskSchedule and its two neighbours file an already
// approved task with no allowlist check of their own), and it is the
// case a park at filing cannot cover.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// restrictTo seeds the deployment's stored configuration with a target
// repo list -- what a cycle then reads back out of grain_config
// (RunCycle's own refresh), rather than a value handed to Deps by a test
// and never stored anywhere.
func restrictTo(t *testing.T, ctx context.Context, store *model.Store, repos ...string) {
	t.Helper()
	if err := store.SetTargetRepos(ctx, repos); err != nil {
		t.Fatal(err)
	}
}

// settingsClient is the deployment's own long-lived ui.Client -- one per
// test, as a daemon has one per process. That matters here: the client
// answers CreateTask and AddComment from its in-process copy of the list
// (ui.Client's own setTargetRepos), which is only in step with the store
// because UpdateSettings on that same client is what wrote both. A test
// that built a fresh client per gesture would be asking a list nobody
// had ever set.
func settingsClient(store *model.Store, repos ...string) *ui.Client {
	return ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice")}, store)
}

// narrowTo is an operator editing that list under Settings, through
// ui.Client itself rather than a hand-written config row, so that what
// these tests do to the list is exactly what the pane does.
func narrowTo(t *testing.T, ctx context.Context, client *ui.Client, repos ...string) {
	t.Helper()
	if _, err := client.UpdateTeam(ctx, model.DefaultTeamID, ui.UpdateTeamRequest{TargetRepos: &repos}); err != nil {
		t.Fatalf("narrowing the target repo list: %v", err)
	}
}

// reply is a human replying on a parked task -- the one gesture that
// lifts a park, and the one that re-asks the list on its way through
// (ui.Client.AddComment).
func reply(t *testing.T, ctx context.Context, client *ui.Client, taskID, body string) {
	t.Helper()
	if err := client.AddComment(ctx, taskID, body, nil); err != nil {
		t.Fatalf("replying on %s: %v", taskID, err)
	}
}

// The case the whole thing is for: a task queued against a repo that was
// on the list when it was filed, with the repo taken off the list while
// it sat there. The cycle that would have dispatched it parks it
// instead, and says in the task's own conversation what happened and
// what the operator can do about it.
//
// The task in a repo that is still on the list is the control, and it is
// the half that would be easy to break: a pass that parked the queue
// would be worse than one that parked nothing.
func TestNarrowingTheTargetRepoListParksQueuedWorkRatherThanDispatchingIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	restrictTo(t, ctx, store, "acme/widgets", "acme/gadgets")
	filedTask(t, ctx, store, "onlist", widgets)
	filedTask(t, ctx, store, "stranded", gadgets)

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)

	narrowTo(t, ctx, settingsClient(store, "acme/widgets", "acme/gadgets"), "acme/widgets")
	cycle(t, ctx, deps)

	stateIs(t, store, "stranded", model.StateAwaitingReply)
	notDispatched(t, store, "stranded", "after its repo came off the target repo list")

	// What the operator is told: which repo, and the list as it now
	// stands, since somebody looking at a parked task has not necessarily
	// seen the settings change that parked it.
	bodies := commentBodies(t, ctx, store, "stranded")
	if len(bodies) != 1 {
		t.Fatalf("comments on the parked task = %d, want the one explaining the park: %q", len(bodies), bodies)
	}
	for _, want := range []string{"acme/gadgets", "acme/widgets", "target repo list"} {
		if !strings.Contains(bodies[0], want) {
			t.Fatalf("park comment = %q, want %q in it", bodies[0], want)
		}
	}

	// And the queue goes on running: only the stranded task was parked.
	enteredRun(t, fw, "onlist-1")
	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

// The park is not a way of throwing the task away: putting the repo back
// on the list and replying is what lifts it, and a reply on its own is
// not (ui.Client's own re-park, asked of the list as it stands). The
// task keeps its target throughout -- nothing here retargets work
// somebody filed on purpose.
func TestTheDispatchTimeParkLiftsWhenTheRepoGoesBackOnTheList(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	restrictTo(t, ctx, store, "acme/widgets", "acme/gadgets")
	filedTask(t, ctx, store, "t1", gadgets)

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)
	client := settingsClient(store, "acme/widgets", "acme/gadgets")

	narrowTo(t, ctx, client, "acme/widgets")
	cycle(t, ctx, deps)
	stateIs(t, store, "t1", model.StateAwaitingReply)

	// A reply that skipped the settings change leaves it exactly where it
	// was, and a cycle after that must not pick it up either.
	reply(t, ctx, client, "t1", "please run this")
	stateIs(t, store, "t1", model.StateAwaitingReply)
	cycle(t, ctx, deps)
	notDispatched(t, store, "t1", "after a reply that did not widen the list")

	// Both acts, in the order the comment asks for them.
	narrowTo(t, ctx, client, "acme/widgets", "acme/gadgets")
	reply(t, ctx, client, "t1", "put it back under Settings")
	stateIs(t, store, "t1", model.StateQueued)

	task, err := store.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Target == nil || !task.Target.Same(gadgets) {
		t.Fatalf("target = %v, want %s: a park must not retarget the task", task.Target, gadgets)
	}

	cycle(t, ctx, deps)
	enteredRun(t, fw, "t1-1")
	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

// A deployment that restricts nothing -- Config.TargetRepos' own
// "unrestricted" zero value, and the shape most deployments are in --
// has no list for this to enforce, and a pass that read an empty list as
// "no repo is allowed" would park every task in such a deployment on its
// first tick.
func TestAnUnrestrictedDeploymentParksNothingAtDispatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	restrictTo(t, ctx, store) // a stored config row, with no repos named in it
	filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)

	cycle(t, ctx, deps)
	enteredRun(t, fw, "t1-1")
	if bodies := commentBodies(t, ctx, store, "t1"); len(bodies) != 0 {
		t.Fatalf("comments = %q, want none: an empty list restricts nothing", bodies)
	}

	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

// A task with no repo at all (ui.CreateTaskRequest.NoRepo) has nothing
// to clone and nothing to push, so the list has nothing to say about it
// -- the same reading ui.Client.AddComment already takes of a nil target
// before re-asking this question of a reply. Parking these would take
// every repo-less task in a restricted deployment out of the queue.
func TestATaskWithNoRepoIsNotParkedByTheTargetRepoList(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	restrictTo(t, ctx, store, "acme/widgets")
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	task := model.Task{
		ID: "norepo", Intent: model.IntentImplement, Title: "think about something", Body: "no repo",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)
	cycle(t, ctx, deps)

	if got, err := store.State(ctx, "norepo"); err != nil {
		t.Fatal(err)
	} else if got == model.StateAwaitingReply {
		t.Fatal("a task with no repo was parked on the target repo list, which says nothing about it")
	}
	if bodies := commentBodies(t, ctx, store, "norepo"); len(bodies) != 0 {
		t.Fatalf("comments = %q, want none", bodies)
	}

	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

// The pass parks a task once, not once per tick: parking takes it out of
// task_ready, so the ticks that follow have nothing to look at. A pass
// that re-parked would bury the explanation under a copy of itself every
// poll interval for as long as nobody noticed.
func TestTheTargetRepoParkIsWrittenOnceRatherThanEveryTick(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	restrictTo(t, ctx, store, "acme/widgets")
	filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "gadgets"})

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)
	for i := range 3 {
		cycleAt(t, ctx, deps, baseTime.Add(time.Duration(i)*time.Minute))
	}

	if bodies := commentBodies(t, ctx, store, "t1"); len(bodies) != 1 {
		t.Fatalf("comments after three cycles = %d, want the one park: %q", len(bodies), bodies)
	}
	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

// --- schedules firing into a repo the list stopped naming (grain/task-430) ---

// The park above is the last look before dispatch, and it is not the
// only thing a narrowed list should do to a schedule: a schedule fires
// on its own cadence forever, so leaving it to file a task the park then
// catches means a parked task per cadence, each carrying grain's own
// comment, in a deployment whose operator was already told what the
// narrowing stranded at the save that did it (model.StrandedWork).
//
// So the firing itself is suppressed while its repo is off the list
// (model.Store.suppressOffTargetRepos): nothing filed, nothing parked,
// nothing to reply to. The schedule is untouched otherwise -- still
// enabled, still pointed where it was pointed -- which is why the second
// half of this test is the half that matters: putting the repo back
// starts the chore again by itself, with nobody having to remember which
// schedules to re-enable.
func TestAScheduleStopsFiringWhileItsRepoIsOffTheTargetRepoList(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	restrictTo(t, ctx, store, "acme/widgets", "acme/gadgets")
	// filedSchedule (schedule_test.go) targets acme/widgets, which is the
	// control here: a pass that stopped every schedule would be worse
	// than one that stopped none.
	filedSchedule(t, store, "onlist", baseTime.Add(-time.Minute))
	stranded := filedSchedule(t, store, "stranded", baseTime.Add(-time.Minute))
	stranded.Target = gadgets
	if err := store.PutSchedule(ctx, stranded); err != nil {
		t.Fatal(err)
	}
	client := settingsClient(store, "acme/widgets", "acme/gadgets")
	narrowTo(t, ctx, client, "acme/widgets")

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)
	// Three of this schedule's own occurrences, a day apart, since the
	// question is what a cadence does over time rather than what one
	// firing does.
	for i := range 3 {
		cycleAt(t, ctx, deps, baseTime.Add(time.Duration(i)*24*time.Hour))
	}

	if filed := scheduledTasksTagged(t, store, "schedule:stranded"); len(filed) != 0 {
		t.Fatalf("filed %d tasks into a repo off the target repo list, want none: %+v", len(filed), filed)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:onlist"); len(filed) == 0 {
		t.Fatal("the schedule whose repo is still on the list filed nothing")
	}
	// Nothing filed into that repo at all, by anything: which is also why
	// there is nothing parked and no comment for anyone to read, since a
	// park is what a filed firing would have collected.
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		if tk.Target != nil && tk.Target.Same(gadgets) {
			t.Errorf("task %s targets %s, which the list no longer names", tk.ID, gadgets)
		}
	}

	// The cadence moved on rather than sticking on the occurrence it did
	// not fire -- otherwise every tick would ask the same question of a
	// list only a human can widen -- and "last run" still says what it
	// said, because nothing ran.
	after, err := store.GetSchedule(ctx, "stranded")
	if err != nil {
		t.Fatal(err)
	}
	if !after.NextRunAt.After(baseTime.Add(48 * time.Hour)) {
		t.Errorf("nextRunAt = %v, want it past the last suppressed occurrence", after.NextRunAt)
	}
	if after.LastRunAt != nil {
		t.Errorf("lastRunAt = %v, want nil: no firing happened", after.LastRunAt)
	}
	if !after.Enabled {
		t.Error("want the schedule still enabled: a narrowed list must not pause a chore an operator would then have to remember to resume")
	}
	if !after.Target.Same(gadgets) {
		t.Errorf("target = %s, want it left at %s", after.Target, gadgets)
	}

	// And the half that makes this reversible: the repo goes back on the
	// list and the very next occurrence fires, with nobody touching the
	// schedule.
	narrowTo(t, ctx, client, "acme/widgets", "acme/gadgets")
	cycleAt(t, ctx, deps, baseTime.Add(72*time.Hour))
	filed := scheduledTasksTagged(t, store, "schedule:stranded")
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks after the repo went back on the list, want 1", len(filed))
	}
	if filed[0].Target == nil || !filed[0].Target.Same(gadgets) {
		t.Errorf("target = %v, want %s", filed[0].Target, gadgets)
	}

	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

// The repo a firing is suppressed on is the repo that firing would
// actually have been dispatched into, which is not always the one the
// schedule's own Target column names: a template bound to a repo of its
// own beats it (model.Template.FiringTarget), and the schedule's copy of
// that binding is only resynced by a firing -- so a template rebound
// since the last one leaves the schedule naming yesterday's repo.
//
// Reading the schedule's own column would suppress this firing forever
// over a repo it was never going to use, which is the failure worth
// having a test for: silently stopping work is exactly what this must
// not do.
func TestAScheduleFiresWhereItsBoundTemplateSaysRatherThanWhereItsColumnDoes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	restrictTo(t, ctx, store, "acme/widgets")
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tmpl-1", Name: "Nightly", Title: "Bump dependencies",
		Target: &widgets, CreatedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	sched := filedSchedule(t, store, "rebound", baseTime.Add(-time.Minute))
	sched.TemplateID = ptrTo("tmpl-1")
	// The stale column: where this schedule fired before the template was
	// rebound, and a repo the list no longer names.
	sched.Target = model.RepoRef{Owner: "acme", Name: "gadgets"}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)
	cycle(t, ctx, deps)

	filed := scheduledTasksTagged(t, store, "schedule:rebound")
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks, want the one firing into the repo the template binds", len(filed))
	}
	if filed[0].Target == nil || !filed[0].Target.Same(widgets) {
		t.Errorf("target = %v, want %s", filed[0].Target, widgets)
	}

	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

func ptrTo[T any](v T) *T { return &v }
