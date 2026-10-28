package staterepo_test

// How often a deployment commits to its own state repository, guarded
// cheaply.
//
// growth_test.go measures what these tests assert: a simulated day of a
// busy deployment cost 2,880 commits and 2.9 GiB of .git under
// bwsalmon/grain#174, which exported every table on every sync. The
// answer to that was two clocks -- settings on every sync, grain's own
// record of itself on an hourly one -- and the answer after it was to
// stop putting grain's own record of itself in the repository at all
// (grain/task-395). There is one clock left and the tables it writes
// are the ones a human or an agent proposes changes to, so what a day
// of a busy deployment costs this repository is now a commit per
// settings change and nothing whatever else.
//
// That is what these assert, small enough to run on every build: a day
// of grain going about its business is a day of no commits, a settings
// change still lands in seconds, and a clock that has moved is never on
// its own a reason to write anything.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// clock is a hand-wound Config.Now: a day of a 30s sync loop passes in a
// few hundred calls rather than in a day.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) tick()          { c.t = c.t.Add(30 * time.Second) }
func newClock() *clock          { return &clock{t: now} }

// openRepoOnAClock is a local-only repository on a hand-wound clock --
// the state every test here starts from.
func openRepoOnAClock(t *testing.T, c *clock) *staterepo.Repo {
	t.Helper()
	repo, err := staterepo.Open(context.Background(), staterepo.Config{
		Dir: filepath.Join(t.TempDir(), "state"), Now: c.now,
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	return repo
}

// A day of nothing but grain watching its own pull requests is a day of
// no commits at all.
//
// task_observation is stamped on every reconcile cycle whether or not
// anything moved, so this is what a busy deployment with no settings
// changes actually looks like. It used to be 2,880 commits a day; the
// hourly clock made it 24; and the narrowing made it none, because none
// of what those cycles wrote is in this repository to be written out.
//
// It is worth an explicit test rather than being left to the export's
// own unit tests, and the reason is what it would take to break it: not
// a line anybody would notice writing, but a table quietly rejoining the
// export -- a new operational table added to SettingsTables by habit, a
// path that reaches for the whole-database Export by mistake. The
// symptom of that is not a wrong file, it is a deployment that starts
// committing to its own settings repository every thirty seconds
// forever, and this is the cheapest place to find out.
func TestGrainGoingAboutItsBusinessNeverCommits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	c := newClock()
	repo := openRepoOnAClock(t, c)
	if err := staterepo.Seed(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	head, _ := repo.Head(ctx)

	// Two hours of ticks, each one restamping observed_at exactly as a
	// reconcile cycle does, and each one starting and finishing a run
	// with a transcript on it -- which is the largest thing this
	// deployment writes and used to be the largest file in the dump.
	//
	// Two rather than four because what the hours are for is crossing
	// the interval grain's own record used to be written out on, and
	// twice is as much evidence as four times for half the seconds.
	const hours = 2
	for i := 0; i < hours*120; i++ {
		c.tick()
		at := c.t
		if err := store.ObserveField(ctx, "a1b2", at, func(o *model.Observation) {
			o.PrOpenedAt = &at
		}); err != nil {
			t.Fatalf("observing: %v", err)
		}
		id := fmt.Sprintf("a1b2-%d", i)
		if err := store.StartRun(ctx, model.Run{
			ID: id, TaskID: "a1b2", Sandbox: "grain-a1b2", Attempt: i + 1, StartedAt: at,
		}, model.Limits{}); err != nil {
			t.Fatalf("starting a run: %v", err)
		}
		if err := store.SetRunTranscript(ctx, id, "the agent said something at tick "+id); err != nil {
			t.Fatalf("writing a transcript: %v", err)
		}
		if err := store.FinishRun(ctx, id, at, "succeeded", "opened a pull request"); err != nil {
			t.Fatalf("finishing the run: %v", err)
		}
		changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
		if err != nil {
			t.Fatalf("syncing: %v", err)
		}
		if changed {
			t.Fatalf("a tick of grain writing to its own tables committed, at tick %d", i)
		}
	}
	if after, _ := repo.Head(ctx); after != head {
		t.Fatalf("%d hours of grain's own work moved HEAD: %s -> %s", hours, head, after)
	}
	// And none of it is in the working tree either, committed or not: an
	// export that wrote the files and left them uncommitted would be the
	// same leak with a commit's delay on it.
	noOperationalTables(t, repo.Dir())
	if status := strings.TrimSpace(git(t, repo.Dir(), "status", "--porcelain")); status != "" {
		t.Fatalf("the working tree is not clean after %d hours of grain's own work:\n%s", hours, status)
	}
}

// The other half of the trade, and the half the repository exists for: a
// settings change is committed on the very next sync, not on the churn
// clock.
func TestSettingsStillReachTheRepositoryImmediately(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	c := newClock()
	repo := openRepoOnAClock(t, c)
	if err := staterepo.Seed(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	c.tick()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:            model.RepoRef{Owner: "owner", Name: "payments-api"},
		PromptExtension: "always run make test",
	}); err != nil {
		t.Fatalf("configuring a repo: %v", err)
	}
	changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if !changed {
		t.Fatal("a settings change did not commit on the next sync")
	}
	got := read(t, filepath.Join(repo.Dir(), staterepo.TablesDir, "repo_config.json"))
	if !strings.Contains(got, "always run make test") {
		t.Fatalf("the settings change is not in the dump:\n%s", got)
	}
}

// The property bwsalmon/grain#174 rests on, restated across an hour of
// idle ticks: an unchanged database produces byte-identical files, so a
// sync with nothing to say commits nothing, however many times it is
// asked and whatever the clock has done in between.
//
// The hour is not arbitrary. Sync has hourly work in it that is not the
// export -- the workflow health check, git's own maintenance -- and a
// tick that crossed one of those and committed over it would be exactly
// as bad as one that exported wrongly.
func TestAnUnchangedDatabaseCommitsNothingAcrossAnIdleHour(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	c := newClock()
	repo := openRepoOnAClock(t, c)
	if err := staterepo.Seed(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	head, _ := repo.Head(ctx)

	// 120 ticks of a 30-second loop is the hour: the first 119 stay
	// inside it and tick 120 lands on the far side. Repeating the same
	// unchanged export for two more hours adds no distinct clock state to
	// this assertion.
	for i := 0; i < 120; i++ {
		c.tick()
		changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
		if err != nil {
			t.Fatalf("syncing: %v", err)
		}
		if changed {
			t.Fatalf("a sync with nothing to say committed, at tick %d", i)
		}
	}
	if after, _ := repo.Head(ctx); after != head {
		t.Fatalf("HEAD moved without anything changing: %s -> %s", head, after)
	}
}

// SyncAll is what a human pressing Sync, or a daemon shutting down,
// means: everything now.
//
// It used to mean more than Sync -- the churn tier written out whether
// or not its clock said it was due -- and there is nothing left for it
// to force, because what it used to force is not in this repository any
// more. So what it has to be is Sync exactly: a shutdown that committed
// something an ordinary tick would not have would make the last commit
// of every deployment's day a different shape from all the others, and
// one that refused to commit a settings change because a human rather
// than a timer asked for it would lose that change on the way out.
func TestSyncAllIsSyncAndLeavesNothingUnwritten(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	c := newClock()
	repo := openRepoOnAClock(t, c)
	if err := staterepo.Seed(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// A run starts and finishes, which is the largest thing an ordinary
	// tick declines to write. A shutdown declines to write it too.
	c.tick()
	if err := store.StartRun(ctx, model.Run{
		ID: "a1b2-1", TaskID: "a1b2", Sandbox: "grain-a1b2", Attempt: 1, StartedAt: c.t,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	if changed, err := staterepo.SyncAll(ctx, repo, db, model.SchemaVersion); err != nil || changed {
		t.Fatalf("a shutdown sync wrote grain's own record out: changed=%v err=%v", changed, err)
	}
	noOperationalTables(t, repo.Dir())

	// And a settings change made in the last moments before the stop
	// goes out on it, which is the half of "leaves nothing unwritten"
	// that is still a claim about anything.
	c.tick()
	putTemplate(t, ctx, store, "tpl-1", "Configured just before the stop")
	changed, err := staterepo.SyncAll(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("syncing all: %v", err)
	}
	if !changed {
		t.Fatal("SyncAll committed nothing with a settings change waiting to be written out")
	}
	hasTemplate(t, repo.Dir(), "tpl-1", "Configured just before the stop")
}

// A merged pull request against the repository must not roll the
// database's own record of what grain did back to whenever churn was
// last exported. The merge is about settings; the database is ahead on
// runs by up to a churn interval, and that is by design.
func TestAMergedChangeDoesNotRollBackRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	c := newClock()
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote, Now: c.now})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// A run happens after the last churn export -- the ordinary case, for
	// the 59 minutes of every hour when one has not just run.
	c.tick()
	if err := store.StartRun(ctx, model.Run{
		ID: "a1b2-1", TaskID: "a1b2", Sandbox: "grain-a1b2", Attempt: 1, StartedAt: c.t,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	if err := store.FinishRun(ctx, "a1b2-1", c.t, "succeeded", "opened a pull request"); err != nil {
		t.Fatalf("finishing the run: %v", err)
	}

	// Meanwhile a pull request retitling the template is merged and pushed.
	mergeIntoRemote(t, remote, staterepo.DumpPath("template"), "Run the nightly sweep", "Retitled by a pull request")

	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading after the merge: %v", err)
	}
	tpl, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil || tpl == nil {
		t.Fatalf("reading the template: %v %v", tpl, err)
	}
	if tpl.Title != "Retitled by a pull request" {
		t.Fatalf("the merged change did not reach the database: %q", tpl.Title)
	}
	runs, err := store.Runs(ctx, "a1b2")
	if err != nil {
		t.Fatalf("reading the runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("the import rolled the run back: %d runs, want 1", len(runs))
	}
}
