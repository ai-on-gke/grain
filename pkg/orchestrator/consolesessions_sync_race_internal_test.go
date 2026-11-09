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

package orchestrator

import (
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestConsoleSessionSyncDoesNotOverwriteATaskThatMoved pins the window
// between syncConsoleSessionTask's sessionStale check and its task write:
// an operator's edit (ui.UpdateConsoleSession) that reaches the session
// task after the sync read it must not be overwritten with grants the sync
// worked out from the task as it was. Before the write re-checked the task,
// the director e2e (ui/e2e/director.spec.js) lost a grain#health grant this
// way under CI load: the operator's snapshot then named it, so no later
// sync ever put it back.
func TestConsoleSessionSyncDoesNotOverwriteATaskThatMoved(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	deps := Deps{Store: store}
	if err := reconcileConsoleSessions(ctx, deps, standingNow); err != nil {
		t.Fatalf("reconcileConsoleSessions: %v", err)
	}
	id, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil || id == "" {
		t.Fatalf("finding director session task: id=%q err=%v", id, err)
	}
	read, err := store.GetTask(ctx, id)
	if err != nil || read == nil {
		t.Fatalf("GetTask(%s): %v", id, err)
	}
	session, ok, err := storedConsoleSession(ctx, store, model.DirectorSessionID)
	if err != nil || !ok {
		t.Fatalf("storedConsoleSession: ok=%v err=%v", ok, err)
	}

	// The sync's copy of the task is one it has something to write back
	// to (its body is not the one the session gives it) ...
	stale := *read
	stale.Body = "a body the sync will rewrite"

	// ... and after it read that copy, a grant reached the task.
	const added = "api:operator-added-since-read"
	if err := store.UpdateTask(ctx, id, func(t *model.Task) error {
		t.Grants = append(t.Grants, model.Grant{Capability: added, Via: model.GrantByLabel})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := syncConsoleSessionTask(ctx, deps, session, stale, standingNow.Add(time.Minute)); err != nil {
		t.Fatalf("syncConsoleSessionTask over a moved task: %v", err)
	}
	after, err := store.GetTask(ctx, id)
	if err != nil || after == nil {
		t.Fatalf("GetTask(%s): %v", id, err)
	}
	if !holdsGrant(*after, added) {
		t.Fatalf("session task grants after a sync from a stale read = %+v; the grant added since was overwritten", after.Grants)
	}

	// The next cycle, reading the task as it stands, keeps it too.
	if err := syncConsoleSessionTask(ctx, deps, session, *after, standingNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("syncConsoleSessionTask: %v", err)
	}
	again, err := store.GetTask(ctx, id)
	if err != nil || again == nil {
		t.Fatalf("GetTask(%s): %v", id, err)
	}
	if !holdsGrant(*again, added) {
		t.Fatalf("session task grants after the next sync = %+v, want %q kept", again.Grants, added)
	}
}

func TestConsoleSessionTaskMoved(t *testing.T) {
	t.Parallel()
	base := model.Task{
		Title:  "Director",
		Body:   "body",
		Grants: []model.Grant{{Capability: "durable", Via: model.GrantByGrain, Folder: &model.FolderRef{Path: []string{"a"}}}},
		Tags:   []string{"x"},
	}
	same := base
	same.Grants = []model.Grant{{Capability: "durable", Via: model.GrantByGrain, Folder: &model.FolderRef{Path: []string{"a"}}}}
	same.Reads = []model.RepoRef{}
	if consoleSessionTaskMoved(same, base) {
		t.Fatal("an identical task read again (fresh Folder pointer, empty rather than nil Reads) reads as moved")
	}
	for name, mutate := range map[string]func(*model.Task){
		"grant added":    func(t *model.Task) { t.Grants = append(t.Grants, model.Grant{Capability: "y"}) },
		"grant via":      func(t *model.Task) { t.Grants = []model.Grant{{Capability: "durable", Via: model.GrantByLabel}} },
		"folder":         func(t *model.Task) { t.Grants = []model.Grant{{Capability: "durable", Via: model.GrantByGrain}} },
		"body":           func(t *model.Task) { t.Body = "other" },
		"writes":         func(t *model.Task) { t.Writes = []model.WriteTarget{{Repo: model.RepoRef{Owner: "o", Name: "n"}}} },
		"tags":           func(t *model.Task) { t.Tags = nil },
		"model":          func(t *model.Task) { t.Model = "m" },
		"auto approve":   func(t *model.Task) { t.AutoApproveProposals = true },
		"network access": func(t *model.Task) { t.NetworkAccess = model.NetworkOpen },
	} {
		moved := base
		moved.Grants = append([]model.Grant(nil), base.Grants...)
		mutate(&moved)
		if !consoleSessionTaskMoved(moved, base) {
			t.Errorf("%s: not read as moved", name)
		}
	}
}

func holdsGrant(t model.Task, capability string) bool {
	return slices.ContainsFunc(t.Grants, func(g model.Grant) bool { return g.Capability == capability })
}
