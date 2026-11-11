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

package orchestrator_test

// Declarations newer than the daemon reading them (grain/task-4392): the
// file this build cannot decode keeps its last good version, the rest of
// the repository loads, and the refusal is an Inbox notice -- once.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// forwardDeclarations reads a tree the way the daemon's
// RepoDeclarationSource reads a clone: forward-compatibly, with a parse
// error marked as a refusal.
type forwardDeclarations struct {
	commit  string
	files   map[string]string
	headErr error
}

func (d *forwardDeclarations) Head(context.Context, model.RepoRef) (string, error) {
	return d.commit, d.headErr
}

func (d *forwardDeclarations) Read(_ context.Context, repo model.RepoRef) (model.RepoDeclarations, string, error) {
	tree := fstest.MapFS{}
	for name, body := range d.files {
		tree[name] = &fstest.MapFile{Data: []byte(body)}
	}
	declared, err := repodecl.LoadForwardCompatible(tree, repo)
	if err != nil {
		return model.RepoDeclarations{}, "", model.DeclarationRefusal{Err: err}
	}
	return declared, d.commit, nil
}

func trainFiles(syncAt, trainAt string) map[string]string {
	return map[string]string{
		".grain/templates/sync.yml":  "title: Sync\n",
		".grain/templates/train.yml": "title: Train\n",
		".grain/schedules/sync.yml":  "template: sync\ndaily:\n  at: \"" + syncAt + "\"\n",
		".grain/schedules/train.yml": "template: train\ndaily:\n  at: \"" + trainAt + "\"\noverlap: skip\n",
	}
}

func declarationNotices(t *testing.T, store *model.Store) []model.Notification {
	t.Helper()
	open, err := store.ListNotifications(t.Context(), model.NotificationFilter{Open: true, Family: orchestrator.DeclarationsRefusedSubject})
	if err != nil {
		t.Fatal(err)
	}
	return open
}

func scheduleAt(t *testing.T, store *model.Store, stem string) *model.Schedule {
	t.Helper()
	sched, err := store.GetSchedule(t.Context(), model.RepoDeclaredID(model.DeclaredSchedule, declaringRepo, stem))
	if err != nil {
		t.Fatal(err)
	}
	return sched
}

// An older parser's refusal of one file does not stop the others in the
// same repository from loading.
func TestAFileTooNewForThisDaemonKeepsItsLastGoodVersionAndTheRestLoad(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &forwardDeclarations{commit: "c1c1c1c1c1c1c1c1", files: trainFiles("02:30", "03:30")}
	runDeclarations(t, store, source, baseTime)
	before := *scheduleAt(t, store, "train")

	// Main moves on: train.yml now says something only a newer grain
	// decodes, sync.yml moves its time, and a new file in the newer shape
	// arrives.
	source.commit = "c2c2c2c2c2c2c2c2"
	source.files = trainFiles("04:30", "03:30")
	source.files[".grain/schedules/train.yml"] += "from-a-newer-grain: {skip: true}\n"
	source.files[".grain/schedules/stage1.yml"] = "template: train\ndaily:\n  at: \"05:00\"\nfrom-a-newer-grain: {}\n"
	runDeclarations(t, store, source, baseTime.Add(3*time.Minute))

	if sync := scheduleAt(t, store, "sync"); sync == nil || sync.Recurrence.TimeOfDay != 4*60+30 {
		t.Fatalf("sync.yml = %+v, want it moved to 04:30 -- the readable file did not load", sync)
	}
	if train := scheduleAt(t, store, "train"); train == nil || train.Recurrence != before.Recurrence {
		t.Fatalf("train.yml = %+v, want its last good version kept", train)
	}
	if row, _ := store.GetRepoDeclaration(ctx, before.ID); row == nil || row.Commit != "c1c1c1c1c1c1c1c1" {
		t.Errorf("train.yml's provenance = %+v, want it still at c1", row)
	}
	if stage1 := scheduleAt(t, store, "stage1"); stage1 != nil {
		t.Errorf("stage1.yml loaded although this build cannot read it: %+v", stage1)
	}
	sync, err := store.GetRepoDeclarationSync(ctx, declaringRepo)
	if err != nil || sync == nil {
		t.Fatal(sync, err)
	}
	for _, want := range []string{model.HeldBackMarker, ".grain/schedules/train.yml", "line 5", "c1c1c1c1c1c1",
		".grain/schedules/stage1.yml", "not loaded"} {
		if !strings.Contains(sync.LastError, want) {
			t.Errorf("sync error %q does not mention %q", sync.LastError, want)
		}
	}
	notices := declarationNotices(t, store)
	if len(notices) != 1 || !strings.Contains(notices[0].Body, "train.yml") || notices[0].Repo != declaringRepo.String() {
		t.Fatalf("notices = %+v, want one naming train.yml", notices)
	}

	// The same error, read again every poll, is not a notice every poll.
	runDeclarations(t, store, source, baseTime.Add(6*time.Minute))
	if again := declarationNotices(t, store); len(again) != 1 || again[0].RaiseCount != 1 {
		t.Fatalf("after a second poll: %+v, want the one notice raised once", again)
	}

	// The deployment upgrades (or the files are fixed): everything loads
	// and the notice clears.
	source.commit = "c3c3c3c3c3c3c3c3"
	source.files = trainFiles("04:30", "06:00")
	runDeclarations(t, store, source, baseTime.Add(9*time.Minute))
	if train := scheduleAt(t, store, "train"); train == nil || train.Recurrence.TimeOfDay != 6*60 {
		t.Fatalf("train.yml = %+v after the fix", train)
	}
	if sync, _ := store.GetRepoDeclarationSync(ctx, declaringRepo); sync.LastError != "" {
		t.Errorf("sync error after the fix = %q", sync.LastError)
	}
	if open := declarationNotices(t, store); len(open) != 0 {
		t.Errorf("notices after the fix = %+v, want none open", open)
	}
}

// A new file this build cannot read is fine on its own, but not once
// something the sync would write names it: that would be a schedule
// pointing at nothing, so the whole set is refused, as it always was.
func TestAHeldBackFileWithNoEarlierVersionThatSomethingNamesRefusesTheSet(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &forwardDeclarations{commit: "c1c1c1c1c1c1c1c1", files: trainFiles("02:30", "03:30")}
	runDeclarations(t, store, source, baseTime)

	source.commit = "c2c2c2c2c2c2c2c2"
	source.files[".grain/templates/stage1.yml"] = "title: Stage 1\nfrom-a-newer-grain: true\n"
	source.files[".grain/schedules/stage1.yml"] = "template: stage1\ndaily:\n  at: \"05:00\"\n"
	err := orchestrator.RunCycle(ctx, orchestrator.Deps{Store: store, Declarations: source}, baseTime.Add(3*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "could not read") {
		t.Fatalf("RunCycle = %v, want the dangling name refused", err)
	}
	sync, _ := store.GetRepoDeclarationSync(ctx, declaringRepo)
	if sync.Commit != "c1c1c1c1c1c1c1c1" || !strings.Contains(sync.LastError, "stay in force") {
		t.Errorf("sync = %+v, want c1 still in force and said so", sync)
	}
	if notices := declarationNotices(t, store); len(notices) != 1 || !strings.Contains(notices[0].Body, "c1c1c1c1c1c1") {
		t.Errorf("notices = %+v, want one naming the commit still in force", notices)
	}
}

// A whole set refused -- a file that is not YAML -- is a notice too, and
// it names the commit whose declarations are still running.
func TestARefusedSetRaisesOneNoticeNamingTheCommitInForce(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &forwardDeclarations{commit: "c1c1c1c1c1c1c1c1", files: trainFiles("02:30", "03:30")}
	runDeclarations(t, store, source, baseTime)

	source.commit = "c2c2c2c2c2c2c2c2"
	source.files[".grain/schedules/train.yml"] = "template: train\ndaily: [\n"
	deps := orchestrator.Deps{Store: store, Declarations: source}
	for i := 1; i <= 3; i++ {
		_ = orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Duration(3*i)*time.Minute))
	}
	notices := declarationNotices(t, store)
	if len(notices) != 1 || notices[0].RaiseCount != 1 {
		t.Fatalf("notices = %+v, want one, raised once", notices)
	}
	if !strings.Contains(notices[0].Body, "train.yml") || !strings.Contains(notices[0].Body, "c1c1c1c1c1c1 stay in force") {
		t.Errorf("notice body = %q", notices[0].Body)
	}
}

// A repository that cannot be reached is recorded, as before, but is
// nobody's file to fix and is not an Inbox notice.
func TestAnUnreachableRepositoryIsNotANotice(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	declarationsOn(t, store, ctx, true)
	source := &forwardDeclarations{commit: "c1", headErr: errors.New("connection refused")}
	_ = orchestrator.RunCycle(ctx, orchestrator.Deps{Store: store, Declarations: source}, baseTime)
	if sync, _ := store.GetRepoDeclarationSync(ctx, declaringRepo); sync == nil || sync.LastError == "" {
		t.Fatalf("sync = %+v, want the failure recorded", sync)
	}
	if notices := declarationNotices(t, store); len(notices) != 0 {
		t.Errorf("notices = %+v, want none", notices)
	}
}
