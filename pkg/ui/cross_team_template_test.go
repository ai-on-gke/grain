package ui_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestRunNowNoteNamesTheCaller: the note run-now leaves is the only
// record of who asked, so it names the request's own principal -- not
// the deployment's configured actor, which is who it named until
// grain/task-1573 found every signed-in member's run-now attributed to
// the daemon's own user.
func TestRunNowNoteNamesTheCaller(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)
	asBob := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "bob"})
	if err := c.RunNow(asBob, task.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Author != "bob" {
		t.Fatalf("run-now note = %+v, want one note by bob", detail.Comments)
	}
}
