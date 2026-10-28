// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
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
