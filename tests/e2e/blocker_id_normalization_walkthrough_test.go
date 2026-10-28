package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestBlockerIDNormalizationWalkthrough is docs/test-plans/2026-10-01.md
// §8 as a test: a scripted run's propose_task names its dependencies the
// ways agents actually write them -- "grain/task-1", "#2", a UI URL --
// and supersedes "grain/task-4", and the proposal it files has to be
// blocked on exactly 1-3 (with 4 closed), stay blocked through approval
// until all three close, and then be dispatched.
//
// It runs once per store backend because the half of the change that
// decides whether anything is blocked at all is the task_blocked view
// (schema.go), which each backend builds from its own dialect: a
// dependency on a task that does not exist must stay an open blocker on
// both, rather than drop out of an inner join and dispatch.
func TestBlockerIDNormalizationWalkthrough(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		withStore(t, t.TempDir(), func(store *model.Store, ctx context.Context) {
			blockerIDNormalizationWalkthrough(t, store, ctx)
		})
	})
	t.Run("postgres", func(t *testing.T) {
		t.Parallel()
		store, _ := postgrestest.Open(t)
		blockerIDNormalizationWalkthrough(t, store, context.Background())
	})
}

func blockerIDNormalizationWalkthrough(t *testing.T, store *model.Store, ctx context.Context) {
	rig := newProposalRig(t)
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice")}, store)
	clock := baseTime
	// Every task goes through CreateTask rather than fileIssue: ids have
	// to come from the store's own allocator (task_sequence), or the
	// proposals the run files are handed ids already in use.
	file := func(title string, approved bool) string {
		t.Helper()
		created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: title, Repo: rig.target.String(), Approved: approved,
		})
		if err != nil {
			t.Fatalf("filing %q: %v", title, err)
		}
		return created.ID
	}

	// The parent is dispatched before its siblings exist, so the one
	// Cycle below cannot pick any of them instead.
	parentID := file("parent run", true)
	w := &world{t: t, store: store, ctx: ctx, roots: map[string]string{parentID + "-1": rig.credentialedRoot()}}
	dispatches, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) != 1 || dispatches[0].TaskID != parentID {
		t.Fatalf("Cycle dispatched %+v, want exactly %s", dispatches, parentID)
	}
	// Filed unapproved, so nothing but the proposal is ever dispatchable
	// below and the Cycle that proves it unblocked has only it to pick.
	var ids [4]string
	for i := range ids {
		ids[i] = file(fmt.Sprintf("dependency %d", i+1), false)
	}
	blockers := ids[:3]

	clock = clock.Add(time.Minute)
	result := w.runDispatch(dispatches[0], []antigravity.Step{
		toolCall("propose_task", map[string]any{
			"title":      "mixed-form proposal",
			"body":       "depends on 1-3 however an agent spells them",
			"depends_on": []any{"grain/task-" + ids[0], "#" + ids[1], "https://grain.example/tasks/" + ids[2]},
			"supersedes": "grain/task-" + ids[3],
		}),
		toolCall("propose_task", map[string]any{
			"title":      "proposal on a task that does not exist",
			"body":       "depends on nothing real",
			"depends_on": []any{"99999"},
		}),
		finalText("proposed two follow-ups"),
	}, clock)
	parent, err := store.GetTask(ctx, parentID)
	if err != nil || parent == nil {
		t.Fatalf("GetTask(%s): %v", parentID, err)
	}
	clock = clock.Add(time.Minute)
	if err := orchestrator.ProcessResult(ctx, store, rig.client, *parent, result, dispatches[0].RunID, clock); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposals := proposedBy(w, parentID)
	mixed, ok := proposals["mixed-form proposal"]
	if !ok {
		t.Fatalf("the mixed-form proposal was never filed: %v", proposals)
	}
	got := dependsOn(mixed)
	slices.Sort(got)
	if !slices.Equal(got, sorted(blockers)) {
		t.Fatalf("mixed-form proposal depends on %v, want %v", got, blockers)
	}
	if !slices.Contains(mixed.Links, model.Link{Kind: model.LinkSupersedes, Target: ids[3]}) {
		t.Errorf("mixed-form proposal links %+v, want supersedes %s", mixed.Links, ids[3])
	}
	if state, _ := store.State(ctx, ids[3]); state != model.StateClosed {
		t.Errorf("task %s is %q after being superseded as grain/task-%[1]s, want closed", ids[3], state)
	}

	// The end-of-run relay does not make a link to a task that does not
	// exist (proposedDependencies): it files the proposal with the entry
	// written into its body for the approver.
	missing, ok := proposals["proposal on a task that does not exist"]
	if !ok {
		t.Fatalf("the proposal naming 99999 was never filed: %v", proposals)
	}
	if deps := dependsOn(missing); len(deps) != 0 || !strings.Contains(missing.Body, "99999") {
		t.Errorf("proposal naming 99999 has links %v and body %q, want no link and 99999 in the body",
			deps, missing.Body)
	}

	if err := client.Approve(ctx, mixed.ID); err != nil {
		t.Fatalf("approving %s: %v", mixed.ID, err)
	}
	assertBlocked(w, mixed.ID, true)
	for _, id := range blockers {
		assertBlocked(w, mixed.ID, true)
		if err := client.Close(ctx, id, ui.CloseOptions{}); err != nil {
			t.Fatalf("closing %s: %v", id, err)
		}
	}
	assertBlocked(w, mixed.ID, false)
	clock = clock.Add(time.Minute)
	ready, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(ready, func(d dispatch.Dispatch) bool { return d.TaskID == mixed.ID }) {
		t.Fatalf("Cycle dispatched %+v once 1-3 closed, want %s", ready, mixed.ID)
	}

	// A link to a task that does not exist -- which no filing path will
	// write any more, but a row from before them or a task removed from
	// under it can still leave -- is an open blocker, not a pass.
	dangling := file("blocked on a task that does not exist", true)
	tk, err := store.GetTask(ctx, dangling)
	if err != nil || tk == nil {
		t.Fatalf("GetTask(%s): %v", dangling, err)
	}
	tk.Links = append(tk.Links, model.Link{Kind: model.LinkDependsOn, Target: "99999"})
	if err := store.PutTask(ctx, *tk); err != nil {
		t.Fatal(err)
	}
	assertBlocked(w, dangling, true)
	if n, err := store.OpenBlockers(ctx, dangling); err != nil || n != 1 {
		t.Fatalf("OpenBlockers(%s) = %d, %v; want 1", dangling, n, err)
	}
	// A human can still take it off, spelling it the way they like.
	if err := client.SetDependency(ctx, dangling, "#99999", false); err != nil {
		t.Fatalf("detaching #99999: %v", err)
	}
	assertBlocked(w, dangling, false)

	// The API's own spellings: a UI URL, task-N and grain/task-N attach
	// the task they name, and an unknown task is refused outright.
	for _, ref := range []string{"https://grain.example/tasks/" + ids[0], "task-" + ids[1],
		"grain/task-" + ids[2], "#" + ids[1]} {
		if err := client.SetDependency(ctx, dangling, ref, true); err != nil {
			t.Fatalf("attaching %q: %v", ref, err)
		}
	}
	tk, err = store.GetTask(ctx, dangling)
	if err != nil || tk == nil {
		t.Fatalf("GetTask(%s): %v", dangling, err)
	}
	got = dependsOn(*tk)
	slices.Sort(got)
	if !slices.Equal(got, sorted(blockers)) {
		t.Errorf("task %s depends on %v after attaching by URL, task-N, grain/task-N and #N, want %v",
			dangling, got, blockers)
	}
	if err := client.SetDependency(ctx, dangling, "task-99999", true); err == nil {
		t.Error("attaching task-99999 succeeded, want it refused as an unknown task")
	}
}

func sorted(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
