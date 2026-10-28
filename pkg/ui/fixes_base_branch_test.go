package ui_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grain/task-3451: a task filed with "This fixes a red <base>" carries the
// branch's repair tag, the same one grain's own repair carries, plus the
// mark that says a person set it.
func TestCreateTaskMarksATaskAsFixingItsBaseBranch(t *testing.T) {
	c, store, ctx := testClient(t)
	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "De flake tests", Description: "please", Approved: true, Base: "main",
		FixesBaseBranch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.FixesBaseBranch || created.GrainBaseRepair {
		t.Fatalf("created task reads fixesBaseBranch=%v grainBaseRepair=%v, want true and false",
			created.FixesBaseBranch, created.GrainBaseRepair)
	}
	task, err := store.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if !slices.Contains(task.Tags, model.BaseRepairBranchTag(repo, "main")) ||
		!slices.Contains(task.Tags, model.MarkedBaseRepairTag) {
		t.Fatalf("tags %v, want the branch repair tag and the person's mark", task.Tags)
	}

	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Think", Description: "please", NoRepo: true, FixesBaseBranch: true,
	}); err == nil {
		t.Fatal("a task with no repo was marked as fixing a base branch")
	}
}

// Marking an existing task leaves its dependencies as they are (grain no
// longer links work to wait on a red branch's repair, so there is no wait
// of grain's to drop) and follows an edited base; grain's own repair
// cannot be unmarked.
func TestUpdateTaskMarkingKeepsItsDependencies(t *testing.T) {
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	file := func(title string, tags ...string) string {
		t.Helper()
		created, err := c.CreateTask(ctx, ui.CreateTaskRequest{
			Title: title, Description: "please", Approved: true, Base: "main",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(tags) > 0 {
			if err := store.UpdateTask(ctx, created.ID, func(task *model.Task) error {
				task.Tags = append(task.Tags, tags...)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		return created.ID
	}
	repair := file("Fix grain CI on acme/widgets main", model.BaseRepairBranchTag(repo, "main"))
	other := file("something else")
	vet := file("fix main's go vet break")
	if err := store.UpdateTask(ctx, vet, func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: other})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	on := true
	if _, err := c.UpdateTask(ctx, vet, ui.UpdateTaskRequest{FixesBaseBranch: &on}); err != nil {
		t.Fatal(err)
	}
	task, _ := store.GetTask(ctx, vet)
	var targets []string
	for _, l := range task.Links {
		if l.Kind == model.LinkDependsOn {
			targets = append(targets, l.Target)
		}
	}
	if !slices.Equal(targets, []string{other}) {
		t.Fatalf("depends on %v after marking, want %s kept", targets, other)
	}
	if !model.FixesBaseBranch(*task) {
		t.Fatalf("tags %v after marking, want it marked", task.Tags)
	}

	base := "release-1"
	if _, err := c.UpdateTask(ctx, vet, ui.UpdateTaskRequest{Base: &base}); err != nil {
		t.Fatal(err)
	}
	task, _ = store.GetTask(ctx, vet)
	if !slices.Contains(task.Tags, model.BaseRepairBranchTag(repo, "release-1")) ||
		slices.Contains(task.Tags, model.BaseRepairBranchTag(repo, "main")) {
		t.Fatalf("tags %v after moving the base, want the mark moved to release-1", task.Tags)
	}

	off := false
	if _, err := c.UpdateTask(ctx, repair, ui.UpdateTaskRequest{FixesBaseBranch: &off}); err == nil {
		t.Fatal("grain's own repair was unmarked")
	}
	if _, err := c.UpdateTask(ctx, vet, ui.UpdateTaskRequest{FixesBaseBranch: &off}); err != nil {
		t.Fatal(err)
	}
	if task, _ = store.GetTask(ctx, vet); model.FixesBaseBranch(*task) {
		t.Fatalf("tags %v after unmarking", task.Tags)
	}
}

// docs/test-runs/2026-10-13-fixes-red-base-branch.md, finding 2: editing
// the base of grain's own repair of main used to re-tag it as grain's
// repair of the new base, so a green main no longer found it to close and
// the work grain had linked to wait on it stayed blocked. A person's mark
// follows an edited base; grain's own repair keeps the branch it was
// filed for.
func TestEditingTheBaseOfGrainsOwnRepairKeepsItsBranchTag(t *testing.T) {
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Fix grain CI on acme/widgets main", Description: "please", Approved: true, Base: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	commitTag := model.GrainBaseRepairCommitTag(repo, "main", "bad0000")
	if err := store.UpdateTask(ctx, created.ID, func(task *model.Task) error {
		task.Tags = append(task.Tags, model.BaseRepairBranchTag(repo, "main"), commitTag)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	base := "release-1"
	if _, err := c.UpdateTask(ctx, created.ID, ui.UpdateTaskRequest{Base: &base}); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := c.UpdateTask(ctx, created.ID, ui.UpdateTaskRequest{FixesBaseBranch: &on}); err != nil {
		t.Fatal(err)
	}
	task, _ := store.GetTask(ctx, created.ID)
	if !slices.Contains(task.Tags, model.BaseRepairBranchTag(repo, "main")) ||
		slices.Contains(task.Tags, model.BaseRepairBranchTag(repo, "release-1")) ||
		!slices.Contains(task.Tags, commitTag) || !model.GrainBaseRepair(*task) {
		t.Fatalf("tags %v after moving grain's repair of main to release-1, want main's repair tag kept", task.Tags)
	}
}
