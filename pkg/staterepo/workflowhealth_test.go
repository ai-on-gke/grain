package staterepo_test

// The check grain installed, watched: whether it runs at all.
//
// Installing the file is where the story used to stop, and a file that
// is installed is not a check that works. GitHub concludes a run before
// any job of it starts when the repository cannot run Actions -- they
// are disabled or restricted for it, it has no runner it may use, or,
// for a private one, its account has no minutes left or has hit a
// spending limit -- and that check then fails every pull request against
// the deployment's own settings, on every tree, with nothing under it to
// read. It is the exact opposite of the case grain can see: the
// repository looks healthy from every surface grain has, and the changes
// it gates are all red.
//
// So the properties here are the ones an operator reading a warning
// depends on: it is only said when every finished run really did die at
// startup, it stops being said the moment one run gets a job started, it
// is never said about a repository that simply has not run the check
// yet, and asking GitHub about it costs one read an hour rather than one
// per thirty-second sync.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// fakeRuns is a Config.WorkflowRuns callback a test drives: it answers
// with whatever the test last set, counts how often it was asked, and
// remembers which file it was asked about.
type fakeRuns struct {
	mu    sync.Mutex
	runs  []staterepo.WorkflowRun
	err   error
	calls int
	file  string
}

func (f *fakeRuns) callback(_ context.Context, file string) ([]staterepo.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.file = file
	return f.runs, f.err
}

func (f *fakeRuns) set(runs []staterepo.WorkflowRun, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs, f.err = runs, err
}

func (f *fakeRuns) asked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// dead is a run of the check GitHub concluded before any job of it
// started, which is the only interesting shape here.
func dead(url string) staterepo.WorkflowRun {
	return staterepo.WorkflowRun{
		Status: "completed", Conclusion: staterepo.StartupFailureConclusion, URL: url,
	}
}

func TestACheckThatNeverStartsIsReportedAndThenForgotten(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	remote := bareRemote(t)
	clock := now
	answers := &fakeRuns{}
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: remote,
		WorkflowRuns: answers.callback,
		Now:          func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	// Nothing has been read yet, and a deployment grain has never been
	// able to ask about must not be reported as one whose check is dead.
	if _, reported := repo.WorkflowStartupFailures(ctx); reported {
		t.Fatal("a repository nothing has been read about reports a dead check")
	}

	// The seed installs the file, and every run of it so far is one
	// GitHub killed before a job started.
	answers.set([]staterepo.WorkflowRun{
		dead("https://github.com/o/r/actions/runs/9"),
		dead("https://github.com/o/r/actions/runs/8"),
		dead("https://github.com/o/r/actions/runs/7"),
	}, nil)
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	// The seed installs the file; the sync tick after it is what looks at
	// how the file has been doing, which is the shape of every other
	// thing this package watches for.
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	failures, reported := repo.WorkflowStartupFailures(ctx)
	if !reported {
		t.Fatal("a deployment whose check has never once started has no way to say so")
	}
	if failures.Runs != 3 {
		t.Errorf("read %d finished runs, want 3", failures.Runs)
	}
	if !failures.At.Equal(now) {
		t.Errorf("looked at %s, want %s", failures.At, now)
	}
	// The newest run's page, because GitHub's own reason for concluding
	// it that way is written there and in no API answer grain gets: it is
	// the whole of what a reader can act on.
	if want := "https://github.com/o/r/actions/runs/9"; failures.RunURL != want {
		t.Errorf("sent a reader to %q, want the newest run at %q", failures.RunURL, want)
	}
	// Asked about the file grain installed, by the path it knows it by.
	if answers.file != staterepo.WorkflowFile {
		t.Errorf("asked GitHub about %q, want %q", answers.file, staterepo.WorkflowFile)
	}

	// The operator raises the account's spending limit, and the next pull
	// request against the repository gets a job started -- however that
	// job ends. One is enough: what was being reported was a repository
	// that could not run Actions at all, and this is one that can.
	clock = clock.Add(2 * time.Hour)
	answers.set([]staterepo.WorkflowRun{
		{Status: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/10"},
		dead("https://github.com/o/r/actions/runs/9"),
		dead("https://github.com/o/r/actions/runs/8"),
	}, nil)
	if err := store.PutTask(ctx, task("c3d4")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if _, reported := repo.WorkflowStartupFailures(ctx); reported {
		t.Error("grain still calls the check dead after a run of it got a job started")
	}
}

// The two answers that are not evidence of anything, and which a warning
// would be a lie about: a repository nobody has opened a pull request
// against yet, and one whose only run is still queued. The check is
// pull-request-triggered, so "no runs" is the ordinary state of a state
// repository the day it is adopted.
func TestNoRunsAtAllIsNotADeadCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		runs []staterepo.WorkflowRun
	}{
		{name: "nothing has ever run it", runs: nil},
		{name: "its only run is still going", runs: []staterepo.WorkflowRun{{Status: "in_progress"}}},
		{name: "queued behind a runner", runs: []staterepo.WorkflowRun{{Status: "queued"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ctx
			store, db := openDB(t)
			if err := store.PutTask(ctx, task("a1b2")); err != nil {
				t.Fatalf("putting: %v", err)
			}
			answers := &fakeRuns{}
			answers.set(tc.runs, nil)
			repo, err := staterepo.Open(ctx, staterepo.Config{
				Dir: filepath.Join(t.TempDir(), "state"), Remote: bareRemote(t),
				WorkflowRuns: answers.callback,
			})
			if err != nil {
				t.Fatalf("opening: %v", err)
			}
			if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
				t.Fatalf("seeding: %v", err)
			}
			if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
				t.Fatalf("syncing: %v", err)
			}
			if answers.asked() == 0 {
				t.Fatal("grain never asked how the check it installed has been running")
			}
			if _, reported := repo.WorkflowStartupFailures(ctx); reported {
				t.Error("a check with no finished run behind it was reported as dead")
			}
		})
	}
}

// A GitHub that will not answer -- an unreachable API, a credential
// without Actions read -- leaves everything as it was. The condition
// this reports is one an operator acts on by paying a bill or changing a
// repository's settings, and a warning that came and went with the
// network would be one nobody could act on at all.
func TestAFailedReadChangesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	clock := now
	answers := &fakeRuns{}
	answers.set([]staterepo.WorkflowRun{dead("https://github.com/o/r/actions/runs/1")}, nil)
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: filepath.Join(t.TempDir(), "state"), Remote: bareRemote(t),
		WorkflowRuns: answers.callback,
		Now:          func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if _, reported := repo.WorkflowStartupFailures(ctx); !reported {
		t.Fatal("the dead check was not reported in the first place")
	}

	clock = clock.Add(2 * time.Hour)
	answers.set(nil, errors.New("403 Forbidden"))
	if err := store.PutTask(ctx, task("c3d4")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("a sync must not fail because GitHub would not say how a workflow is doing: %v", err)
	}
	if _, reported := repo.WorkflowStartupFailures(ctx); !reported {
		t.Error("a read that failed took a condition an operator has not fixed off the pane")
	}
}

// Once an hour, not once a tick. The sync loop runs every thirty
// seconds, and this question is about something that changes when
// somebody pays a bill.
func TestGitHubIsAskedOnceAnIntervalRatherThanOnceASync(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	clock := now
	answers := &fakeRuns{}
	answers.set([]staterepo.WorkflowRun{dead("https://github.com/o/r/actions/runs/1")}, nil)
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: filepath.Join(t.TempDir(), "state"), Remote: bareRemote(t),
		WorkflowRuns: answers.callback,
		Now:          func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if got := answers.asked(); got != 1 {
		t.Fatalf("the first sync asked GitHub %d times, want 1", got)
	}
	for i := 0; i < 5; i++ {
		clock = clock.Add(30 * time.Second)
		if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
			t.Fatalf("syncing: %v", err)
		}
	}
	if got := answers.asked(); got != 1 {
		t.Errorf("five syncs within the interval asked GitHub %d times, want 1", got)
	}
	clock = clock.Add(time.Hour)
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if got := answers.asked(); got != 2 {
		t.Errorf("a sync an interval later asked GitHub %d times, want 2", got)
	}
}

// The deployments with nothing to watch: no remote to run Actions, an
// operator who turned the check off, and a caller with no GitHub behind
// it at all. None of them should be asked about, and none of them should
// be told their check is broken.
func TestNothingIsWatchedWhereNoCheckWasEverOffered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		remote bool
		cfg    staterepo.Config
	}{
		{name: "the operator turned it off", remote: true, cfg: staterepo.Config{NoWorkflow: true}},
		{name: "local only", remote: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db := openDB(t)
			if err := store.PutTask(ctx, task("a1b2")); err != nil {
				t.Fatalf("putting: %v", err)
			}
			answers := &fakeRuns{}
			answers.set([]staterepo.WorkflowRun{dead("https://github.com/o/r/actions/runs/1")}, nil)
			cfg := tc.cfg
			cfg.Dir = filepath.Join(t.TempDir(), "state")
			cfg.WorkflowRuns = answers.callback
			if tc.remote {
				cfg.Remote = bareRemote(t)
			}
			repo, err := staterepo.Open(ctx, cfg)
			if err != nil {
				t.Fatalf("opening: %v", err)
			}
			if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
				t.Fatalf("seeding: %v", err)
			}
			if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
				t.Fatalf("syncing: %v", err)
			}
			if got := answers.asked(); got != 0 {
				t.Errorf("grain asked GitHub %d times about a check it is not offering", got)
			}
			if _, reported := repo.WorkflowStartupFailures(ctx); reported {
				t.Error("a deployment that was never offered the check was told its check is dead")
			}
		})
	}
}

// And a merge that drops the workflow takes the observation with it: the
// runs grain read were runs of a file this repository no longer has, and
// installWorkflow is about to offer it again. What happens to *those*
// runs is the next observation's business.
func TestDroppingTheWorkflowDropsWhatGrainSawOfIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	answers := &fakeRuns{}
	answers.set([]staterepo.WorkflowRun{dead("https://github.com/o/r/actions/runs/1")}, nil)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Remote: bareRemote(t), WorkflowRuns: answers.callback,
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if _, reported := repo.WorkflowStartupFailures(ctx); !reported {
		t.Fatal("the dead check was not reported in the first place")
	}
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(staterepo.WorkflowFile))); err != nil {
		t.Fatalf("removing the workflow: %v", err)
	}
	if _, reported := repo.WorkflowStartupFailures(ctx); reported {
		t.Error("a repository with no check in it is still reported as one whose check dies")
	}
}
