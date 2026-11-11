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
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeReleaseLoopForge is the forge as the release-loop watchdog reads it:
// tags, compare, workflow runs and check runs, all set by the test.
type fakeReleaseLoopForge struct {
	github.Client // nil: anything else asked of it panics

	mu           sync.Mutex
	tags         []github.Tag
	commits      map[string][]github.Commit // "base..head" -> commits oldest-first
	workflowRuns map[string][]github.WorkflowRun
	checkRuns    map[string][]github.CheckRun
	app          string
}

func newFakeReleaseLoopForge() *fakeReleaseLoopForge {
	return &fakeReleaseLoopForge{
		commits:      map[string][]github.Commit{},
		workflowRuns: map[string][]github.WorkflowRun{},
		checkRuns:    map[string][]github.CheckRun{},
	}
}

func (f *fakeReleaseLoopForge) ListTags(_, _ string) ([]github.Tag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]github.Tag(nil), f.tags...), nil
}

func (f *fakeReleaseLoopForge) CompareCommits(_, _, base, head string) ([]github.Commit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]github.Commit(nil), f.commits[base+".."+head]...), nil
}

func (f *fakeReleaseLoopForge) WorkflowRunsOnCommit(_, _, headSHA string) ([]github.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]github.WorkflowRun(nil), f.workflowRuns[headSHA]...), nil
}

func (f *fakeReleaseLoopForge) ListWorkflowRuns(_, _, headSHA string) ([]github.CheckRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []github.CheckRun
	for _, r := range f.workflowRuns[headSHA] {
		c := r.Conclusion
		out = append(out, github.CheckRun{ID: r.ID, Name: r.Name, Status: r.Status, Conclusion: &c, HTMLURL: r.HTMLURL})
	}
	return out, nil
}

func (f *fakeReleaseLoopForge) ListCheckRuns(_, _, ref string) ([]github.CheckRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]github.CheckRun(nil), f.checkRuns[ref]...), nil
}

func (f *fakeReleaseLoopForge) CheckRunApp(_, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.app, nil
}

func (f *fakeReleaseLoopForge) setTags(tags ...github.Tag) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags = append([]github.Tag(nil), tags...)
}

func (f *fakeReleaseLoopForge) setCommits(base, head string, commits ...github.Commit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits[base+".."+head] = append([]github.Commit(nil), commits...)
}

func (f *fakeReleaseLoopForge) setRuns(sha string, runs ...github.WorkflowRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workflowRuns[sha] = runs
}

func (f *fakeReleaseLoopForge) setChecks(sha string, checks ...github.CheckRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkRuns[sha] = checks
}

// setStage0 records tests.yml and release.yml push runs on sha with the
// given conclusions ("in_progress" for one still running, "" for none).
func (f *fakeReleaseLoopForge) setStage0(sha, testsConclusion, releaseConclusion string) {
	run := func(id int64, name, path, conclusion string) github.WorkflowRun {
		r := github.WorkflowRun{ID: id, Name: name, Path: path, HeadSHA: sha, Event: "push",
			Status: "completed", Conclusion: conclusion}
		if conclusion == "in_progress" {
			r.Status, r.Conclusion = "in_progress", ""
		}
		return r
	}
	var runs []github.WorkflowRun
	if testsConclusion != "" {
		runs = append(runs, run(1, "Tests", ".github/workflows/tests.yml", testsConclusion))
	}
	if releaseConclusion != "" {
		runs = append(runs, run(2, "release", ".github/workflows/release.yml", releaseConclusion))
	}
	f.setRuns(sha, runs...)
}

// optInReleaseLoop declares both of the train's schedules on repo: the
// Stage 1 trigger and the 20-minute backstop, in overlap group
// release-train.
func optInReleaseLoop(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef, now time.Time) {
	t.Helper()
	optInStage1(t, ctx, store, repo)
	tmpl := "release-train"
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-train-periodic", Title: "Release train", Target: repo, Base: "main", Enabled: true,
		Overlap: model.OverlapSkip, OverlapGroup: "release-train", TemplateID: &tmpl,
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 20},
		NextRunAt:  now.Add(20 * time.Minute), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func putReleaseLoopTask(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef,
	id, title string, tags []string, createdAt time.Time) {
	t.Helper()
	target := repo
	orderKey, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: id, Intent: model.IntentImplement, Title: title, Body: "body", Target: &target, Base: "main",
		Binding: model.BindingDirective, Priority: 9,
		Approval: &model.Attribution{Actor: model.SchedulePrincipal}, ApprovedAt: &createdAt,
		CreatedAt: &createdAt, OrderKey: orderKey, Tags: tags,
		Origin: model.Origin{Attribution: model.Attribution{Actor: model.SchedulePrincipal}, Reason: model.ReasonSchedule},
	}); err != nil {
		t.Fatal(err)
	}
}

func putReleaseTrainTick(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef,
	id string, createdAt time.Time) {
	t.Helper()
	putReleaseLoopTask(t, ctx, store, repo, id, "Release train tick", []string{
		model.ScheduleFiringTag("sched-train-periodic"),
		model.ScheduleOverlapGroupTag(repo, "release-train"),
	}, createdAt)
}

const (
	loopRelSHA  = "1111111111111111111111111111111111111111"
	loopRC1SHA  = "2222222222222222222222222222222222222222"
	loopDocsSHA = "3333333333333333333333333333333333333333"
	loopFeatSHA = "4444444444444444444444444444444444444444"
	loopTipSHA  = "5555555555555555555555555555555555555555"
)

func loopIdle(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) *model.Notification {
	t.Helper()
	n, err := store.OpenNotification(ctx, releaseLoopIdleSubject(repo))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// No rc in qualification for 3h while main has a green fix raises
// pipeline.release-loop-idle naming the queued tick; cutting an rc clears
// it. A legacy rc left behind by a later release never counts as
// qualifying.
func TestReleaseLoopIdleFiresAfter3hAndClearsWhenAnRCIsCut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	optInReleaseLoop(t, ctx, store, repo, now)

	forge := newFakeReleaseLoopForge()
	forge.setTags(
		github.Tag{Name: "v1.14.0-rc.1", SHA: "0000000000000000000000000000000000000000"},
		github.Tag{Name: "v3.4.0", SHA: loopRelSHA},
	)
	forge.setCommits(loopRelSHA, "main", github.Commit{SHA: loopFeatSHA, Message: "fix(api): handle empty cursor"})
	forge.setStage0(loopFeatSHA, "success", "success")
	deps := Deps{Store: store, Client: forge}

	// Through the cycle step it runs in, beside the Stage 1 repairs.
	if err := reconcileGitHubCITails(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	if n := loopIdle(t, ctx, store, repo); n != nil {
		t.Fatalf("raised at once: %s", n.Body)
	}
	if err := reconcileGitHubCITails(ctx, deps, now.Add(2*time.Hour+50*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := loopIdle(t, ctx, store, repo); n != nil {
		t.Fatalf("raised before 3h: %s", n.Body)
	}

	putReleaseTrainTick(t, ctx, store, repo, "4101", now.Add(2*time.Hour+45*time.Minute))
	at := now.Add(3*time.Hour + 5*time.Minute)
	if err := reconcileGitHubCITails(ctx, deps, at); err != nil {
		t.Fatal(err)
	}
	n := loopIdle(t, ctx, store, repo)
	if n == nil {
		t.Fatal("no pipeline.release-loop-idle after 3h5m")
	}
	if n.Family != model.FamilyReleaseLoopIdle || n.Subject != "pipeline.release-loop-idle:acme/widgets" {
		t.Errorf("family %q subject %q", n.Family, n.Subject)
	}
	if n.TaskID != "4101" || !strings.Contains(n.Body, "[task 4101](/tasks/4101)") || !strings.Contains(n.Body, "is queued") ||
		!strings.Contains(n.Body, shortSHA(loopFeatSHA)) || !strings.Contains(n.Body, "`v3.4.0`") {
		t.Errorf("notification does not name the queued tick, the commit and the floor: task %q\n%s", n.TaskID, n.Body)
	}

	forge.setTags(
		github.Tag{Name: "v3.4.0", SHA: loopRelSHA},
		github.Tag{Name: "v3.5.0-rc.1", SHA: loopFeatSHA},
	)
	if err := reconcileGitHubCITails(ctx, deps, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := loopIdle(t, ctx, store, repo); n != nil {
		t.Fatalf("still open after v3.5.0-rc.1 was cut: %s", n.Body)
	}
}

// What does not raise it: docs/test-only commits, a commit whose Stage 0
// is not green on a push run, a repository that has not opted in; and what
// does: a green feature behind a tip still running CI.
func TestReleaseLoopIdleNeedsAGreenFixOrFeature(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	run := func(t *testing.T, optIn bool, setup func(*fakeReleaseLoopForge)) *model.Notification {
		t.Helper()
		store := openInternalStore(t)
		if optIn {
			optInReleaseLoop(t, ctx, store, repo, now)
		}
		forge := newFakeReleaseLoopForge()
		forge.setTags(github.Tag{Name: "v3.4.0", SHA: loopRelSHA})
		setup(forge)
		deps := Deps{Store: store, Client: forge}
		if err := reconcileReleaseLoopIdle(ctx, deps, now); err != nil {
			t.Fatal(err)
		}
		if err := reconcileReleaseLoopIdle(ctx, deps, now.Add(4*time.Hour)); err != nil {
			t.Fatal(err)
		}
		return loopIdle(t, ctx, store, repo)
	}

	t.Run("docs and tests only", func(t *testing.T) {
		t.Parallel()
		if n := run(t, true, func(f *fakeReleaseLoopForge) {
			f.setCommits(loopRelSHA, "main",
				github.Commit{SHA: loopDocsSHA, Message: "docs: clarify install notes"},
				github.Commit{SHA: loopTipSHA, Message: "Merge pull request #99 from acme/docs\n\ntest(ui): tighten selector"})
			f.setStage0(loopDocsSHA, "success", "success")
			f.setStage0(loopTipSHA, "success", "success")
		}); n != nil {
			t.Fatalf("raised on docs/test-only commits: %s", n.Body)
		}
	})
	t.Run("release.yml red", func(t *testing.T) {
		t.Parallel()
		if n := run(t, true, func(f *fakeReleaseLoopForge) {
			f.setCommits(loopRelSHA, "main", github.Commit{SHA: loopFeatSHA, Message: "feat: add metrics endpoint"})
			f.setStage0(loopFeatSHA, "success", "failure")
		}); n != nil {
			t.Fatalf("raised with release.yml red: %s", n.Body)
		}
	})
	t.Run("pull request runs only", func(t *testing.T) {
		t.Parallel()
		if n := run(t, true, func(f *fakeReleaseLoopForge) {
			f.setCommits(loopRelSHA, "main", github.Commit{SHA: loopFeatSHA, Message: "feat: add metrics endpoint"})
			f.setRuns(loopFeatSHA,
				github.WorkflowRun{ID: 1, Name: "Tests", Path: ".github/workflows/tests.yml", Event: "pull_request", Status: "completed", Conclusion: "success"},
				github.WorkflowRun{ID: 2, Name: "release", Path: ".github/workflows/release.yml", Event: "pull_request", Status: "completed", Conclusion: "success"})
		}); n != nil {
			t.Fatalf("raised on a pull request's runs: %s", n.Body)
		}
	})
	t.Run("not opted in", func(t *testing.T) {
		t.Parallel()
		if n := run(t, false, func(f *fakeReleaseLoopForge) {
			f.setCommits(loopRelSHA, "main", github.Commit{SHA: loopFeatSHA, Message: "feat: add metrics endpoint"})
			f.setStage0(loopFeatSHA, "success", "success")
		}); n != nil {
			t.Fatalf("raised for a repository without the train's schedules: %s", n.Body)
		}
	})
	t.Run("green feature behind a running tip", func(t *testing.T) {
		t.Parallel()
		n := run(t, true, func(f *fakeReleaseLoopForge) {
			f.setCommits(loopRelSHA, "main",
				github.Commit{SHA: loopFeatSHA, Message: "feat: continuous loop"},
				github.Commit{SHA: loopTipSHA, Message: "docs: update readme"})
			f.setStage0(loopFeatSHA, "success", "success")
			f.setStage0(loopTipSHA, "in_progress", "in_progress")
		})
		if n == nil || !strings.Contains(n.Body, shortSHA(loopFeatSHA)) {
			t.Fatalf("want a notification naming %s, got %+v", shortSHA(loopFeatSHA), n)
		}
	})
}

// A staged -pending release, grain/rc-hold in progress on a dropped rc, and
// an open [rc-blocker] each explain the wait; once nothing does, it fires.
func TestReleaseLoopIdleIsQuietWhileSomethingExplainsTheWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	optInReleaseLoop(t, ctx, store, repo, now)
	forge := newFakeReleaseLoopForge()
	forge.app = "grain-forge"
	forge.setCommits(loopRC1SHA, "main", github.Commit{SHA: loopFeatSHA, Message: "fix: follow-up patch"})
	forge.setStage0(loopFeatSHA, "success", "success")
	deps := Deps{Store: store, Client: forge}
	quietFor4h := func(from time.Time, why string) time.Time {
		t.Helper()
		if err := reconcileReleaseLoopIdle(ctx, deps, from); err != nil {
			t.Fatal(err)
		}
		if err := reconcileReleaseLoopIdle(ctx, deps, from.Add(4*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if n := loopIdle(t, ctx, store, repo); n != nil {
			t.Fatalf("raised while %s: %s", why, n.Body)
		}
		return from.Add(4*time.Hour + 5*time.Minute)
	}

	forge.setTags(
		github.Tag{Name: "v3.4.0", SHA: loopRelSHA},
		github.Tag{Name: "v3.5.0-rc.1", SHA: loopRC1SHA},
		github.Tag{Name: "v3.5.0-pending", SHA: loopRC1SHA},
	)
	at := quietFor4h(now, "v3.5.0-pending waited for a person")

	forge.setTags(
		github.Tag{Name: "v3.4.0", SHA: loopRelSHA},
		github.Tag{Name: "v3.5.0-rc.1", SHA: loopRC1SHA},
		github.Tag{Name: "v3.5.0-rc.1-discarded", SHA: loopRC1SHA},
	)
	forge.setChecks(loopRC1SHA, github.CheckRun{ID: 10, Name: model.ReleaseHoldCheckName, App: "grain-forge", Status: "in_progress"})
	at = quietFor4h(at, "grain/rc-hold was in progress")

	done := "success"
	forge.setChecks(loopRC1SHA, github.CheckRun{ID: 10, Name: model.ReleaseHoldCheckName, App: "grain-forge",
		Status: "completed", Conclusion: &done})
	putReleaseLoopTask(t, ctx, store, repo, "blocker-1", model.RCBlockerMarker("v3.5.0-rc.1")+" fix race in daemon", nil, at)
	at = quietFor4h(at, "an [rc-blocker] task was open")

	if err := closeTaskBy(ctx, store, "blocker-1", at, stage1Repairer, "merged", func(o *model.Observation) {
		o.ClosedAt = &at
	}); err != nil {
		t.Fatal(err)
	}
	if err := reconcileReleaseLoopIdle(ctx, deps, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := reconcileReleaseLoopIdle(ctx, deps, at.Add(3*time.Hour+5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n := loopIdle(t, ctx, store, repo)
	if n == nil {
		t.Fatal("no notification 3h after the hold and the blocker cleared with no rc.2 cut")
	}
	if !strings.Contains(n.Body, "no release-train tick has been filed yet") {
		t.Errorf("want the tick state said, got:\n%s", n.Body)
	}
}

// The notification says whether the newest tick is starved or failed.
func TestReleaseLoopIdleSaysWhetherTheNewestTickIsStarvedOrFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	raise := func(t *testing.T, setup func(*model.Store)) *model.Notification {
		t.Helper()
		store := openInternalStore(t)
		optInReleaseLoop(t, ctx, store, repo, now)
		setup(store)
		forge := newFakeReleaseLoopForge()
		forge.setTags(github.Tag{Name: "v3.4.0", SHA: loopRelSHA})
		forge.setCommits(loopRelSHA, "main", github.Commit{SHA: loopFeatSHA, Message: "feat: ship widget"})
		forge.setStage0(loopFeatSHA, "success", "success")
		deps := Deps{Store: store, Client: forge}
		if err := reconcileReleaseLoopIdle(ctx, deps, now); err != nil {
			t.Fatal(err)
		}
		if err := reconcileReleaseLoopIdle(ctx, deps, now.Add(3*time.Hour+5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		n := loopIdle(t, ctx, store, repo)
		if n == nil {
			t.Fatal("no notification")
		}
		return n
	}

	t.Run("starved", func(t *testing.T) {
		t.Parallel()
		n := raise(t, func(store *model.Store) { putReleaseTrainTick(t, ctx, store, repo, "5001", now) })
		if n.TaskID != "5001" || !strings.Contains(n.Body, "is starved") {
			t.Errorf("want starved tick 5001, got task %q:\n%s", n.TaskID, n.Body)
		}
	})
	t.Run("failed tick", func(t *testing.T) {
		t.Parallel()
		n := raise(t, func(store *model.Store) {
			putReleaseTrainTick(t, ctx, store, repo, "5002", now.Add(time.Hour))
			ended := now.Add(time.Hour + 2*time.Minute)
			if _, err := store.AddComment(ctx, model.Comment{TaskID: "5002", Author: model.Attribution{Actor: model.SchedulePrincipal},
				Body: "release-train-task: missing api:ci-read\nRELEASE_TRAIN_RESULT=failed", CreatedAt: ended}); err != nil {
				t.Fatal(err)
			}
			if err := closeTaskBy(ctx, store, "5002", ended, stage1Repairer, "done", func(o *model.Observation) {
				o.ClosedAt = &ended
			}); err != nil {
				t.Fatal(err)
			}
		})
		if n.TaskID != "5002" || !strings.Contains(n.Body, "failed") || !strings.Contains(n.Body, "RELEASE_TRAIN_RESULT=failed") {
			t.Errorf("want failed tick 5002, got task %q:\n%s", n.TaskID, n.Body)
		}
	})
	t.Run("failed firing", func(t *testing.T) {
		t.Parallel()
		n := raise(t, func(store *model.Store) {
			sched, err := store.GetSchedule(ctx, "sched-train-periodic")
			if err != nil || sched == nil {
				t.Fatal(err)
			}
			if _, err := store.RecordScheduleFiringFailure(ctx, *sched, now.Add(2*time.Hour),
				errors.New("template release-train not found")); err != nil {
				t.Fatal(err)
			}
		})
		if !strings.Contains(n.Body, "failed to fire") || !strings.Contains(n.Body, "template release-train not found") {
			t.Errorf("want the failed firing, got:\n%s", n.Body)
		}
	})
}

func TestCommitWarrantsReleaseMirrorsBumpKind(t *testing.T) {
	t.Parallel()
	for msg, want := range map[string]bool{
		"fix: a":                      true,
		"feat(ui): b":                 true,
		"refactor: c":                 true,
		"docs: d":                     false,
		"chore(deps): e":              false,
		"test: f":                     false,
		"ci: g":                       false,
		"docs!: drop flag":            true,
		"chore: x\n\nBREAKING CHANGE": false,
		"chore: x\n\nBREAKING CHANGE: the flag is gone": true,
		"Update README": true,
		"Merge pull request #1 from a/b\n\ndocs: tidy": false,
		"Merge pull request #2 from a/b\n\nfix: crash": true,
	} {
		if got := commitWarrantsRelease(github.Commit{Message: msg}); got != want {
			t.Errorf("commitWarrantsRelease(%q) = %v, want %v", msg, got, want)
		}
	}
}
