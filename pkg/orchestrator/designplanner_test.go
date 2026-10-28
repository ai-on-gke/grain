package orchestrator_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/repodecl"
)

// The design-doc planner (docs/design-docs.md) is configuration, not
// code: a trigger, `design-docs.yml`, and this repository's own
// `.grain/templates/design-planner.yml`. The trigger is turned off in this
// repository for now, and its body is kept in
// pkg/designdoc/testdata/design-docs.yml until it is restored. These tests
// fire those two files, read from this checkout, through the path-trigger
// machinery, so a change to either is tested the way a deployment will run
// it.

// declareDesignPlanner syncs the design-docs trigger and the template it
// files, as triggerRepo's declarations at base.
func declareDesignPlanner(t *testing.T, ctx context.Context, store *model.Store, base string) {
	t.Helper()
	files := fstest.MapFS{}
	for p, src := range map[string]string{
		".grain/triggers/design-docs.yml":     "../designdoc/testdata/design-docs.yml",
		".grain/templates/design-planner.yml": "../../.grain/templates/design-planner.yml",
	} {
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		files[p] = &fstest.MapFile{Data: body}
	}
	declared, err := repodecl.Load(files, triggerRepo)
	if err != nil {
		t.Fatalf("the design-docs trigger and design-planner template do not load: %v", err)
	}
	var decl model.RepoDeclarations
	for _, tr := range declared.Triggers {
		if tr.Name == "design-docs" {
			decl.Triggers = append(decl.Triggers, tr)
		}
	}
	for _, f := range declared.Templates {
		if f.Template.ID == templateID("design-planner") {
			decl.Templates = append(decl.Templates, f)
		}
	}
	if len(decl.Triggers) != 1 || len(decl.Templates) != 1 {
		t.Fatalf("want the design-docs trigger and the design-planner template, got %d trigger(s) and %d template(s)",
			len(decl.Triggers), len(decl.Templates))
	}
	if _, err := store.SyncRepoDeclarations(ctx, triggerRepo, base, decl, baseTime, time.UTC); err != nil {
		t.Fatalf("syncing declarations: %v", err)
	}
}

// While the trigger is off, this repository still declares the planner's
// template, so a planner can be filed by hand from it, and declares no
// design-docs trigger.
func TestThisRepositoryKeepsThePlannerTemplateWithoutItsTrigger(t *testing.T) {
	t.Parallel()
	declared, err := repodecl.Load(os.DirFS("../.."), triggerRepo)
	if err != nil {
		t.Fatalf("this repository's .grain does not load: %v", err)
	}
	for _, tr := range declared.Triggers {
		if tr.Name == "design-docs" {
			t.Errorf("%s is declared, but the design-doc planner trigger is turned off (docs/design-docs.md)", tr.Path)
		}
	}
	found := false
	for _, f := range declared.Templates {
		if f.Template.ID == templateID("design-planner") {
			found = true
		}
	}
	if !found {
		t.Error("this repository no longer declares the design-planner template")
	}
}

var leftoverPlaceholder = regexp.MustCompile(`\{\{\s*[a-z0-9][a-z0-9_-]*\s*\}\}`)

func TestDesignDocMergeFilesOneDesignPlannerTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	declareDesignPlanner(t, ctx, store, branchSHA(t, sim.BareRepo, "main"))
	reconcileTriggers(t, ctx, store, client, baseTime)

	// A merge that edits the doc files exactly one planner for its folder.
	from := branchSHA(t, sim.BareRepo, "main")
	pushFile(t, sim.BareRepo, "main", "docs/design/sample/README.md", "# Sample\n\n### Do a thing {#D-1}\n")
	to := branchSHA(t, sim.BareRepo, "main")
	sim.MergedPullRequestForCommit = map[string]int{to: 77}
	reconcileTriggers(t, ctx, store, client, baseTime.Add(4*time.Minute))

	got := triggered(t, ctx, store)
	if len(got) != 1 {
		t.Fatalf("filed %d tasks, want one design planner: %+v", len(got), got)
	}
	task := got[0]
	if want := "Plan design doc changes: docs/design/sample"; task.Title != want {
		t.Errorf("title = %q, want %q", task.Title, want)
	}
	if task.Approval != nil {
		t.Error("the design-docs trigger is not auto-approve, but its task was filed approved")
	}
	if task.Base != "main" || task.Target == nil || !task.Target.Same(triggerRepo) {
		t.Errorf("filed against %v@%q, want %s@main", task.Target, task.Base, triggerRepo)
	}
	for _, want := range []string{
		"git diff " + from + ".." + to + " -- docs/design/sample",
		"`git show " + to + ":<doc>`",
		"```\ndocs/design/sample/README.md\n```",
		"Range: `" + from + ".." + to + "` on `main`",
		"Design-Item: <doc>#<ID>",
		"Design-Rev: " + to,
	} {
		if !strings.Contains(task.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, task.Body)
		}
	}
	// The design PR is the planner's origin, and so of all it files.
	if want := model.GitHubOrigin(triggerRepo, 77); task.WorkOrigin != want {
		t.Errorf("origin = %q, want the merged design PR %q", task.WorkOrigin, want)
	}
	if strings.Contains(task.Body, "plan.yml") {
		t.Errorf("the planner still writes a ledger:\n%s", task.Body)
	}
	if m := leftoverPlaceholder.FindAllString(task.Body+task.Title, -1); len(m) > 0 {
		t.Errorf("the planner template uses variables the trigger does not supply: %v", m)
	}
}
