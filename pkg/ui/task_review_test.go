package ui_test

// grain/task-284: the review attached to a task -- the template a second
// agent is filed from once the task's own work is done. Tested against a
// real embedded store, the reasoning schedules_test.go's own doc comment
// gives.

import (
	"errors"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

func TestCreateTaskAttachesAReviewTemplate(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "Bug hunt", Title: "Review it"})
	if err != nil {
		t.Fatal(err)
	}

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Add pagination", Approved: true, ReviewTemplateID: tmpl.ID,
	})
	if err != nil {
		t.Fatalf("creating a task with a review: %v", err)
	}
	if task.ReviewTemplateID != tmpl.ID {
		t.Fatalf("reviewTemplateId = %q, want %q", task.ReviewTemplateID, tmpl.ID)
	}
	// The name rides along so a pane can say which review is attached
	// without holding the whole template list to look an id up in.
	if task.ReviewTemplateName != "Bug hunt" {
		t.Fatalf("reviewTemplateName = %q, want %q", task.ReviewTemplateName, "Bug hunt")
	}

	stored, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReviewTemplateID != tmpl.ID {
		t.Fatalf("stored ReviewTemplateID = %q, want %q", stored.ReviewTemplateID, tmpl.ID)
	}

	// And through the list shape too, which resolves every task's name
	// from one read of the template list.
	list, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ReviewTemplateName != "Bug hunt" {
		t.Fatalf("listed task = %+v, want the review's own name on it", list)
	}
}

// A typo here costs the review entirely -- the task's pull request waits
// on one that can never be filed -- and it would not surface until hours
// later, so it is refused at the moment somebody chooses it.
func TestCreateTaskRejectsAnUnknownReviewTemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "x", ReviewTemplateID: "nope"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestUpdateTaskAttachesAndDetachesAReview(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "Bug hunt", Title: "Review it"})
	if err != nil {
		t.Fatal(err)
	}
	task := create(t, c, ctx)

	attached, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{ReviewTemplateID: &tmpl.ID})
	if err != nil {
		t.Fatalf("attaching a review: %v", err)
	}
	if attached.ReviewTemplateID != tmpl.ID {
		t.Fatalf("reviewTemplateId = %q, want %q", attached.ReviewTemplateID, tmpl.ID)
	}

	// "" is a real edit here, not an omitted field: it detaches the
	// review, so the task merges without waiting for one.
	none := ""
	detached, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{ReviewTemplateID: &none})
	if err != nil {
		t.Fatalf("detaching the review: %v", err)
	}
	if detached.ReviewTemplateID != "" {
		t.Fatalf("reviewTemplateId = %q, want it detached", detached.ReviewTemplateID)
	}
}

func TestUpdateTaskRejectsAnUnknownReviewTemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)
	missing := "nope"
	_, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{ReviewTemplateID: &missing})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// A review task nests under the task it reviews the way an automatic
// fix nests under the one it repairs (Stacked), and says which of the
// two it is (Review) -- both are stacked on another task's branch, and
// "a second agent read this change" is not "the merge queue patched a
// red build".
func TestTaskReadsAReviewTaskAsStackedAndAsAReview(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	reviewer := model.Principal{Kind: model.PrincipalAutomation, ID: "review"}
	if err := store.PutTask(ctx, model.Task{
		ID: "9", Intent: model.IntentImplement, Title: "Review the change",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: reviewer},
			Reason:      model.ReasonReview,
		},
		Approval: &model.Attribution{Actor: reviewer},
		Target:   &repo,
		Binding:  model.BindingDirective,
		Base:     model.BranchName("8"),
		Links:    []model.Link{{Kind: model.LinkProposedBy, Target: "8"}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := c.Task(ctx, "9")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stacked || !got.Review {
		t.Fatalf("stacked = %v, review = %v, want both true", got.Stacked, got.Review)
	}
	if got.GeneratedFrom != "8" {
		t.Fatalf("generatedFrom = %q, want the task it reviews", got.GeneratedFrom)
	}
}

// Deleting a template out from under a task that still names it would
// leave that task's pull request waiting on a review nothing can ever
// file -- the same reasoning that already keeps a template a schedule,
// plan or suite points at from being deleted.
func TestDeleteTemplateRefusesWhileAnOpenTaskNamesItAsItsReview(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "Bug hunt", Title: "Review it"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Add pagination", Approved: true, ReviewTemplateID: tmpl.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = c.DeleteTemplate(ctx, tmpl.ID)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError refusing the delete", err)
	}

	// Once that task is closed it will never be reviewed again, so the
	// template is deletable -- otherwise one use, months ago, would make
	// it undeletable forever.
	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTemplate(ctx, tmpl.ID); err != nil {
		t.Fatalf("deleting a template only a closed task named: %v", err)
	}
}

func TestCreateTaskUsesRepoDefaultReviewTemplate(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	reviewTmplID := model.DefaultReviewTemplateID(repo)
	if err := store.PutTemplate(ctx, model.Template{
		ID:    reviewTmplID,
		Name:  "Bug hunter",
		Title: "Review the code",
		Body:  "Read the diff and fix bugs.",
	}); err != nil {
		t.Fatal(err)
	}

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Add pagination", Repo: "acme/widgets", Approved: true,
	})
	if err != nil {
		t.Fatalf("creating a task: %v", err)
	}
	if task.ReviewTemplateID != reviewTmplID {
		t.Fatalf("reviewTemplateId = %q, want %q", task.ReviewTemplateID, reviewTmplID)
	}
	if task.ReviewTemplateName != "Bug hunter" {
		t.Fatalf("reviewTemplateName = %q, want %q", task.ReviewTemplateName, "Bug hunter")
	}
}

func TestCreateTaskExplicitReviewOverridesRepoDefault(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	defaultID := model.DefaultReviewTemplateID(repo)
	if err := store.PutTemplate(ctx, model.Template{
		ID:    defaultID,
		Name:  "Default reviewer",
		Title: "Default review",
	}); err != nil {
		t.Fatal(err)
	}

	customTmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "Custom Review", Title: "Custom"})
	if err != nil {
		t.Fatal(err)
	}

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Add pagination", Repo: "acme/widgets", Approved: true, ReviewTemplateID: customTmpl.ID,
	})
	if err != nil {
		t.Fatalf("creating a task: %v", err)
	}
	if task.ReviewTemplateID != customTmpl.ID {
		t.Fatalf("reviewTemplateId = %q, want %q", task.ReviewTemplateID, customTmpl.ID)
	}
}
