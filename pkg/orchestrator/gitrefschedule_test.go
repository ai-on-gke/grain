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

// git_ref schedules (model.RecurrenceGitRef): a schedule that fires when a
// ref of another repository comes to name a different commit, read by the
// daemon itself rather than by an agent woken to look.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// fakeRefs is a RefSource whose answers the test sets, counting the asks.
type fakeRefs struct {
	mu     sync.Mutex
	commit map[string]string
	asks   int
}

func (f *fakeRefs) set(repo, ref, commit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commit == nil {
		f.commit = map[string]string{}
	}
	f.commit[repo+" "+ref] = commit
}

func (f *fakeRefs) Ref(_ context.Context, repo model.RepoRef, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks++
	return f.commit[repo.String()+" "+ref], nil
}

func (f *fakeRefs) askCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asks
}

const (
	refCommitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	refCommitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	refCommitC = "cccccccccccccccccccccccccccccccccccccccc"
)

func gitRefSchedule(t *testing.T, store *model.Store, id string, overlap model.OverlapPolicy) model.Schedule {
	t.Helper()
	sched := model.Schedule{
		ID:     id,
		Title:  "Sync the upstream release",
		Body:   "Bring this edition up to upstream's latest release.",
		Target: model.RepoRef{Owner: "acme", Name: "edition"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceGitRef,
			GitRef: &model.GitRefTrigger{Repo: "upstream/core", Ref: "refs/tags/latest"}},
		Overlap:   overlap,
		Enabled:   true,
		NextRunAt: baseTime,
		CreatedAt: baseTime,
	}
	if err := store.PutSchedule(t.Context(), sched); err != nil {
		t.Fatalf("filing schedule: %v", err)
	}
	return sched
}

func runWithRefs(t *testing.T, store *model.Store, refs orchestrator.RefSource, now time.Time) {
	t.Helper()
	if err := orchestrator.RunCycle(t.Context(), orchestrator.Deps{Store: store, Refs: refs}, now); err != nil {
		t.Fatalf("RunCycle at %v: %v", now, err)
	}
}

func TestGitRefScheduleFiresOnlyWhenTheRefMoves(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := gitRefSchedule(t, store, "sched-ref", model.OverlapAllow)
	refs := &fakeRefs{}
	tag := "schedule:" + sched.ID

	// The ref does not exist yet: nothing to record, nothing fired.
	now := baseTime
	runWithRefs(t, store, refs, now)
	if got := scheduledTasksTagged(t, store, tag); len(got) != 0 {
		t.Fatalf("filed %d tasks before the ref existed, want 0", len(got))
	}

	// First sight of the ref records where it is and fires nothing.
	refs.set("upstream/core", "refs/tags/latest", refCommitA)
	now = now.Add(3 * time.Minute)
	runWithRefs(t, store, refs, now)
	if got := scheduledTasksTagged(t, store, tag); len(got) != 0 {
		t.Fatalf("filed %d tasks on first sight of the ref, want 0", len(got))
	}
	cursor, err := store.GetScheduleRefCursor(ctx, sched.ID)
	if err != nil || cursor == nil || cursor.Commit != refCommitA {
		t.Fatalf("cursor = %+v, %v; want it at %s", cursor, err, refCommitA)
	}

	// Unchanged: still nothing, however many polls.
	for range 3 {
		now = now.Add(3 * time.Minute)
		runWithRefs(t, store, refs, now)
	}
	if got := scheduledTasksTagged(t, store, tag); len(got) != 0 {
		t.Fatalf("filed %d tasks for a ref that never moved, want 0", len(got))
	}

	// Moved: one firing, told which move it was filed for.
	refs.set("upstream/core", "refs/tags/latest", refCommitB)
	now = now.Add(3 * time.Minute)
	runWithRefs(t, store, refs, now)
	filed := scheduledTasksTagged(t, store, tag)
	if len(filed) != 1 {
		t.Fatalf("filed %d tasks after the ref moved, want 1", len(filed))
	}
	if filed[0].Target == nil || *filed[0].Target != sched.Target {
		t.Errorf("target = %+v, want the schedule's own %s", filed[0].Target, sched.Target)
	}
	comments, err := store.Comments(ctx, filed[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	said := false
	for _, c := range comments {
		said = said || (strings.Contains(c.Body, refCommitA) && strings.Contains(c.Body, refCommitB))
	}
	if !said {
		t.Errorf("the filed task's comments %+v do not say the ref moved from %s to %s", comments, refCommitA, refCommitB)
	}
	if cursor, _ := store.GetScheduleRefCursor(ctx, sched.ID); cursor == nil || cursor.Commit != refCommitB {
		t.Errorf("cursor = %+v after firing, want it at %s", cursor, refCommitB)
	}

	// And not again for the same commit.
	now = now.Add(3 * time.Minute)
	runWithRefs(t, store, refs, now)
	if got := scheduledTasksTagged(t, store, tag); len(got) != 1 {
		t.Fatalf("filed %d tasks, want still 1 once the move had fired", len(got))
	}
}

func TestGitRefScheduleWaitsOutItsPollInterval(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	gitRefSchedule(t, store, "sched-ref", model.OverlapAllow)
	refs := &fakeRefs{}
	refs.set("upstream/core", "refs/tags/latest", refCommitA)

	runWithRefs(t, store, refs, baseTime)
	runWithRefs(t, store, refs, baseTime.Add(10*time.Second))
	runWithRefs(t, store, refs, baseTime.Add(time.Minute))
	if got := refs.askCount(); got != 1 {
		t.Fatalf("asked for the ref %d times inside one poll interval, want 1", got)
	}
	runWithRefs(t, store, refs, baseTime.Add(3*time.Minute))
	if got := refs.askCount(); got != 2 {
		t.Fatalf("asked for the ref %d times after the interval, want 2", got)
	}
}

func TestGitRefScheduleHeldByAnOpenFiringFiresOnceItCloses(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := gitRefSchedule(t, store, "sched-ref", model.OverlapSkip)
	refs := &fakeRefs{}
	tag := "schedule:" + sched.ID

	refs.set("upstream/core", "refs/tags/latest", refCommitA)
	now := baseTime
	runWithRefs(t, store, refs, now)
	refs.set("upstream/core", "refs/tags/latest", refCommitB)
	now = now.Add(3 * time.Minute)
	runWithRefs(t, store, refs, now)
	first := scheduledTasksTagged(t, store, tag)
	if len(first) != 1 {
		t.Fatalf("filed %d tasks on the first move, want 1", len(first))
	}

	// A second move while the first firing is still open is held, not
	// dropped: the cursor stays where the first firing left it.
	refs.set("upstream/core", "refs/tags/latest", refCommitC)
	now = now.Add(3 * time.Minute)
	runWithRefs(t, store, refs, now)
	if got := scheduledTasksTagged(t, store, tag); len(got) != 1 {
		t.Fatalf("filed %d tasks while the first firing was open, want 1", len(got))
	}
	if cursor, _ := store.GetScheduleRefCursor(ctx, sched.ID); cursor == nil || cursor.Commit != refCommitB {
		t.Fatalf("cursor = %+v while held, want it still at %s", cursor, refCommitB)
	}

	closed := now
	if err := store.Observe(ctx, model.Observation{TaskID: first[0].ID, ClosedAt: &closed}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	runWithRefs(t, store, refs, now)
	if got := scheduledTasksTagged(t, store, tag); len(got) != 2 {
		t.Fatalf("filed %d tasks once the first firing closed, want 2", len(got))
	}
}

func TestGitRefScheduleIgnoresARepositoryOffTheTargetList(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	if err := store.SetTargetRepos(ctx, []string{"acme/edition"}); err != nil {
		t.Fatal(err)
	}
	gitRefSchedule(t, store, "sched-ref", model.OverlapAllow)
	refs := &fakeRefs{}
	refs.set("upstream/core", "refs/tags/latest", refCommitA)

	runWithRefs(t, store, refs, baseTime)
	if got := refs.askCount(); got != 0 {
		t.Fatalf("read a ref of a repository off the target list %d times, want 0", got)
	}
}

// A schedule a repository declares may not watch grain/config: no
// repository's file may reach grain's own configuration, which the
// RefSource would answer off grain's disk with no credential in front of
// it.
func TestGitRefScheduleDeclaredByARepositoryNeverReadsGrainConfig(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	sched := model.Schedule{
		ID:     model.RepoDeclaredID(model.DeclaredSchedule, model.RepoRef{Owner: "acme", Name: "edition"}, "sync"),
		Title:  "Watch grain's settings",
		Body:   "Report where grain/config is.",
		Target: model.RepoRef{Owner: "acme", Name: "edition"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceGitRef,
			GitRef: &model.GitRefTrigger{Repo: "grain/config", Ref: "refs/heads/main"}},
		Overlap:   model.OverlapAllow,
		Enabled:   true,
		NextRunAt: baseTime,
		CreatedAt: baseTime,
	}
	if err := store.PutSchedule(t.Context(), sched); err != nil {
		t.Fatalf("filing schedule: %v", err)
	}
	refs := &fakeRefs{}
	refs.set("grain/config", "refs/heads/main", refCommitA)

	runWithRefs(t, store, refs, baseTime)
	if got := refs.askCount(); got != 0 {
		t.Fatalf("a repository's schedule read grain/config's ref %d times, want 0", got)
	}
}

func TestDueSchedulesLeavesGitRefSchedulesOut(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	gitRefSchedule(t, store, "sched-ref", model.OverlapAllow)
	due, err := store.DueSchedules(ctx, baseTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("DueSchedules = %+v, want a git_ref schedule left to its own reconciler", due)
	}
}
