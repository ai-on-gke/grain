package cli

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// `grain create -author` files a task as somebody other than the
// daemon's own actor, the way the global -as does, and -team puts it on
// their team: the local CLI's own requester and team metadata, which
// the local policy then reads like Silo's.
func TestCreateAuthorFilesTheTaskAsItsRequester(t *testing.T) {
	store, db, err := daemon.OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(accessCodeServer(ui.NewClient(ui.Config{SourceDB: db, Actor: ui.DefaultActor("alice")}, store)))
	t.Cleanup(ts.Close)
	client := accessCodeClient(ts.URL)

	out := &printer{}
	if err := cmdCreate(ctx, client, out, []string{"-title", "by bob", "-no-repo", "-author", "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdCreate(ctx, accessCodeClient(ts.URL), out, []string{"-title", "by the actor", "-no-repo"}); err != nil {
		t.Fatal(err)
	}
	tasks, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ui.Task{}
	for _, task := range tasks {
		got[task.Title] = task
	}
	if task := got["by bob"]; task.Creator != "bob" {
		t.Errorf("-author bob filed creator %q", task.Creator)
	}
	if task := got["by the actor"]; task.Creator != "alice" {
		t.Errorf("with no -author the task was filed by %q, want the daemon's actor", task.Creator)
	}
}
