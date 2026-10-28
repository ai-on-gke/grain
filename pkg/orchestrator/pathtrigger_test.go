package orchestrator_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

var triggerRepo = model.RepoRef{Owner: "acme", Name: "widgets"}

// declareTriggers stores what a declaration sync of triggerRepo's base
// would: one template per stem, and the triggers, at baseCommit.
func declareTriggers(t *testing.T, ctx context.Context, store *model.Store, baseCommit string,
	templates map[string]string, triggers ...model.PathTrigger) {
	t.Helper()
	var decl model.RepoDeclarations
	for stem, title := range templates {
		target := triggerRepo
		decl.Templates = append(decl.Templates, model.DeclaredTemplateFile{
			Path: model.TemplateDeclarationDir + "/" + stem + ".yml",
			Template: model.Template{
				ID: model.RepoDeclaredID(model.DeclaredTemplate, triggerRepo, stem), Name: stem,
				Title: title, Body: "Plan {{group}} from {{from_sha}} to {{to_sha}} on {{branch}} ({{trigger}}):\n{{paths}}",
				Target: &target,
			},
		})
	}
	for i := range triggers {
		if triggers[i].Path == "" {
			triggers[i].Path = model.TriggerDeclarationDir + "/" + triggers[i].Name + ".yml"
		}
		if triggers[i].Per == "" {
			triggers[i].Per = model.TriggerPerChange
		}
	}
	decl.Triggers = triggers
	if _, err := store.SyncRepoDeclarations(ctx, triggerRepo, baseCommit, decl, baseTime, time.UTC); err != nil {
		t.Fatalf("syncing declarations: %v", err)
	}
}

func templateID(stem string) string {
	return model.RepoDeclaredID(model.DeclaredTemplate, triggerRepo, stem)
}

// designTrigger is the design-doc planner's shape: merges to the default
// branch under docs/design, one task per design folder, plans ignored.
func designTrigger() model.PathTrigger {
	return model.PathTrigger{
		Name: "design", On: model.TriggerOnMerge, Paths: []string{"docs/design/**"},
		Ignore: []string{"**/plan.yml"}, Template: templateID("planner"), Per: model.TriggerPerFolder,
	}
}

// triggered is every task a trigger filed, open or not, by its tags.
func triggered(t *testing.T, ctx context.Context, store *model.Store) []model.Task {
	t.Helper()
	all, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, task := range all {
		if slices.Contains(task.Tags, model.PathTriggerMarkerTag) {
			out = append(out, task)
		}
	}
	return out
}

func openTriggered(t *testing.T, ctx context.Context, store *model.Store) []model.Task {
	t.Helper()
	var out []model.Task
	for _, task := range triggered(t, ctx, store) {
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			out = append(out, task)
		}
	}
	return out
}

func reconcileTriggers(t *testing.T, ctx context.Context, store *model.Store, client github.Client, at time.Time) {
	t.Helper()
	if err := orchestrator.ReconcilePathTriggers(ctx, orchestrator.Deps{Store: store, Client: client}, at); err != nil {
		t.Fatalf("reconciling path triggers: %v", err)
	}
}

// mergeSetup is a repository whose triggers have already seen main once,
// which is where they start watching.
func mergeSetup(t *testing.T, triggers ...model.PathTrigger) (context.Context, *model.Store, *githubsim.Sim, *github.RESTClient) {
	t.Helper()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
		map[string]string{"planner": "Plan {{group}}"}, triggers...)
	reconcileTriggers(t, ctx, store, client, baseTime)
	if got := triggered(t, ctx, store); len(got) != 0 {
		t.Fatalf("the first look at a branch fired %d task(s); it should only start watching", len(got))
	}
	return ctx, store, sim, client
}

func TestMergeTriggerFilesOneTaskForADesignDocMerge(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client := mergeSetup(t, designTrigger())
	from := branchSHA(t, sim.BareRepo, "main")
	pushFile(t, sim.BareRepo, "main", "docs/design/foo/README.md", "# foo\n")
	to := branchSHA(t, sim.BareRepo, "main")

	reconcileTriggers(t, ctx, store, client, baseTime.Add(2*time.Minute))

	got := triggered(t, ctx, store)
	if len(got) != 1 {
		t.Fatalf("filed %d tasks, want exactly one: %+v", len(got), got)
	}
	task := got[0]
	if task.Title != "Plan docs/design/foo" {
		t.Errorf("title = %q, want the group substituted", task.Title)
	}
	for _, want := range []string{
		"Plan docs/design/foo from " + from + " to " + to + " on main (design):\ndocs/design/foo/README.md",
		"- from_sha: `" + from + "`", "- to_sha: `" + to + "`", "- branch: `main`", "- group: `docs/design/foo`",
	} {
		if !strings.Contains(task.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, task.Body)
		}
	}
	if task.Target == nil || !task.Target.Same(triggerRepo) || task.Base != "main" {
		t.Errorf("task is not filed against the repository's branch: target=%v base=%q", task.Target, task.Base)
	}
	if task.Approval != nil {
		t.Error("a trigger without auto-approve filed an approved task")
	}
	if !slices.Contains(task.Tags, model.PathTriggerFromTag(from)) || model.PathTriggerFrom(task.Tags) != from {
		t.Errorf("tags do not record the from sha: %v", task.Tags)
	}
	if !slices.ContainsFunc(task.Tags, func(tag string) bool { return strings.HasPrefix(tag, "path-trigger-rule:") }) {
		t.Errorf("tags do not record the base commit the rule came from: %v", task.Tags)
	}

	// Nothing has moved: nothing more is filed, however often it looks.
	reconcileTriggers(t, ctx, store, client, baseTime.Add(5*time.Minute))
	if got := triggered(t, ctx, store); len(got) != 1 {
		t.Fatalf("a second look at the same head filed again: %d tasks", len(got))
	}
}

func TestMergeTriggerDiffsABurstOnceAndARestartNeitherRefiresNorSkips(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client := mergeSetup(t, designTrigger())
	from := branchSHA(t, sim.BareRepo, "main")
	// Four merges between two looks: two design folders, a plan that is
	// ignored, and a file nothing watches.
	pushFile(t, sim.BareRepo, "main", "docs/design/foo/README.md", "# foo\n")
	pushFile(t, sim.BareRepo, "main", "docs/design/bar/README.md", "# bar\n")
	pushFile(t, sim.BareRepo, "main", "docs/design/baz/plan.yml", "plan: true\n")
	pushFile(t, sim.BareRepo, "main", "pkg/x.go", "package x\n")
	to := branchSHA(t, sim.BareRepo, "main")

	reconcileTriggers(t, ctx, store, client, baseTime.Add(2*time.Minute))
	got := triggered(t, ctx, store)
	var groups []string
	for _, task := range got {
		if model.PathTriggerFrom(task.Tags) != from || !strings.Contains(task.Body, "- to_sha: `"+to+"`") {
			t.Errorf("task %s does not cover the whole burst once:\n%s", task.ID, task.Body)
		}
		groups = append(groups, task.Title)
	}
	slices.Sort(groups)
	if want := []string{"Plan docs/design/bar", "Plan docs/design/foo"}; !slices.Equal(groups, want) {
		t.Fatalf("filed %v, want %v", groups, want)
	}

	// A restart: a fresh process with the same store. The cursor says
	// the burst is filed, so nothing re-fires...
	cursor, err := store.GetPathTriggerCursor(ctx, triggerRepo, "main")
	if err != nil || cursor != to {
		t.Fatalf("cursor = %q (%v), want %s", cursor, err, to)
	}
	reconcileTriggers(t, ctx, store, client, baseTime.Add(time.Hour))
	if n := len(triggered(t, ctx, store)); n != 2 {
		t.Fatalf("a restart re-fired: %d tasks", n)
	}
	// ...and a merge that lands while it is down is still diffed from
	// where the last one left off.
	pushFile(t, sim.BareRepo, "main", "docs/design/qux/README.md", "# qux\n")
	// The declaration sync that every merge also sets off runs first.
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"),
		map[string]string{"planner": "Plan {{group}}"}, designTrigger())
	reconcileTriggers(t, ctx, store, client, baseTime.Add(2*time.Hour))
	got = triggered(t, ctx, store)
	if len(got) != 3 {
		t.Fatalf("the merge after a restart was skipped: %d tasks", len(got))
	}
}

func TestMergeTriggerCoalescesPerGroup(t *testing.T) {
	t.Parallel()
	ctx, store, sim, client := mergeSetup(t, designTrigger())
	from := branchSHA(t, sim.BareRepo, "main")

	pushFile(t, sim.BareRepo, "main", "docs/design/foo/a.md", "a\n")
	reconcileTriggers(t, ctx, store, client, baseTime.Add(2*time.Minute))
	first := openTriggered(t, ctx, store)
	if len(first) != 1 {
		t.Fatalf("want one task after the first merge, got %d", len(first))
	}

	// The first has never been dispatched: a second merge to the same
	// folder supersedes it, keeping its from_sha and both files.
	pushFile(t, sim.BareRepo, "main", "docs/design/foo/b.md", "b\n")
	reconcileTriggers(t, ctx, store, client, baseTime.Add(4*time.Minute))
	open := openTriggered(t, ctx, store)
	if len(open) != 1 || open[0].ID == first[0].ID {
		t.Fatalf("want the first task superseded by one new one, open = %+v", open)
	}
	second := open[0]
	if model.PathTriggerFrom(second.Tags) != from {
		t.Errorf("the superseding task lost the earliest from_sha: %v", second.Tags)
	}
	if !strings.Contains(second.Body, "docs/design/foo/a.md") || !strings.Contains(second.Body, "docs/design/foo/b.md") {
		t.Errorf("the superseding task does not cover both merges:\n%s", second.Body)
	}
	if linkOf(second, model.LinkSupersedes) != first[0].ID {
		t.Errorf("no supersedes link: %+v", second.Links)
	}

	// Now the second has a pull request open: it cannot be superseded,
	// so the third depends on it -- never two runnable tasks per key.
	if err := store.UpdateTask(ctx, second.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#41"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pushFile(t, sim.BareRepo, "main", "docs/design/foo/c.md", "c\n")
	reconcileTriggers(t, ctx, store, client, baseTime.Add(6*time.Minute))
	open = openTriggered(t, ctx, store)
	if len(open) != 2 {
		t.Fatalf("want the running task and one waiting on it, got %d open", len(open))
	}
	var third model.Task
	for _, task := range open {
		if task.ID != second.ID {
			third = task
		}
	}
	if linkOf(third, model.LinkDependsOn) != second.ID {
		t.Errorf("the third task does not wait on the running one: %+v", third.Links)
	}

	// And a fourth merge supersedes the waiting one, still waiting.
	pushFile(t, sim.BareRepo, "main", "docs/design/foo/d.md", "d\n")
	reconcileTriggers(t, ctx, store, client, baseTime.Add(8*time.Minute))
	open = openTriggered(t, ctx, store)
	if len(open) != 2 {
		t.Fatalf("want still two open, got %d", len(open))
	}
	for _, task := range open {
		if task.ID == second.ID {
			continue
		}
		if task.ID == third.ID || linkOf(task, model.LinkDependsOn) != second.ID {
			t.Errorf("the waiting task was not superseded by one that still waits: %+v", task)
		}
	}
}

func TestARepositoryWithNoTriggersBehavesAsToday(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	declareTriggers(t, ctx, store, branchSHA(t, sim.BareRepo, "main"), map[string]string{"planner": "Plan"})
	reconcileTriggers(t, ctx, store, client, baseTime)
	pushFile(t, sim.BareRepo, "main", "docs/design/foo/README.md", "# foo\n")
	reconcileTriggers(t, ctx, store, client, baseTime.Add(2*time.Minute))
	if got := triggered(t, ctx, store); len(got) != 0 {
		t.Fatalf("filed %d tasks with no triggers declared", len(got))
	}
	if c, _ := store.GetPathTriggerCursor(ctx, triggerRepo, "main"); c != "" {
		t.Errorf("a repository with no triggers grew a cursor: %s", c)
	}

	// And a finished change in it is untouched by the pull-request half.
	task := finishTouching(t, ctx, store, sim, "t1", "docs/design/foo/README.md", true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkPathTriggersRead) != "" || linkOf(got, model.LinkPathTriggerHold) != "" {
		t.Fatalf("a repository with no triggers touched the change: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

func TestMergeTriggerNewStoreAtReusedAddressNotThrottledByEarlierStore(t *testing.T) {
	t.Parallel()
	store1, ctx := openStore(t)
	sim1, client1 := newSim(t, "acme", "widgets", "main")
	declareTriggers(t, ctx, store1, branchSHA(t, sim1.BareRepo, "main"),
		map[string]string{"planner": "Plan {{group}}"}, designTrigger())
	reconcileTriggers(t, ctx, store1, client1, baseTime)
	reconcileTriggers(t, ctx, store1, client1, baseTime.Add(4*time.Minute))

	addrBefore := fmt.Sprintf("%p", store1)
	idBefore := store1.ID()

	// Open a fresh Store and overwrite store1's struct allocation in place to
	// deterministically simulate the Go garbage collector reclaiming store1 and
	// allocating store2 at the exact same heap address (%p).
	store2, _ := openStore(t)
	if store2.ID() == idBefore {
		t.Fatalf("new store was assigned the same ID %d as the previous store", store2.ID())
	}
	reflect.ValueOf(store1).Elem().Set(reflect.ValueOf(store2).Elem())
	if got := fmt.Sprintf("%p", store1); got != addrBefore {
		t.Fatalf("pointer address changed from %s to %s", addrBefore, got)
	}

	sim2, client2 := newSim(t, "acme", "widgets", "main")
	declareTriggers(t, ctx, store1, branchSHA(t, sim2.BareRepo, "main"),
		map[string]string{"planner": "Plan {{group}}"}, designTrigger())
	reconcileTriggers(t, ctx, store1, client2, baseTime)
	pushFile(t, sim2.BareRepo, "main", "docs/design/foo/README.md", "# foo\n")
	reconcileTriggers(t, ctx, store1, client2, baseTime.Add(4*time.Minute))

	if got := triggered(t, ctx, store1); len(got) != 1 {
		t.Fatalf("filed %d tasks on new Store at reused address %s, want 1: %+v", len(got), addrBefore, got)
	}
}
