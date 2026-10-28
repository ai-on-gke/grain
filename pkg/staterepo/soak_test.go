package staterepo_test

// A soak: hundreds of rounds of everything that happens to a state
// repository, in an order nobody chose, with the database and the
// repository held against each other the whole way.
//
// The rest of this package's tests each set one situation up and check
// one thing about it, which is what tests should do and is not enough
// here. The failures this arrangement is prone to are failures of
// *sequence*: an export that committed before a merge arrived, a
// recovery that ran with a stranded commit still in the way, a restart
// that landed between a pull and the import it was supposed to trigger.
// Those need a run long enough for the orderings to collide, which is
// what this is.
//
// What the repository carries is editable configuration and nothing else
// (dump.go's ExportConfig): the three settings documents, the tables
// behind them, and the memory and release-note files. Tasks, comments,
// runs, observations and CI history stay in SQLite. That narrowing --
// grain/task-395, "Keep operational state out of the state repository"
// -- is why this file no longer asks whether a task round-trips through
// git, and it is also why half of what it does ask is worth more than it
// was. The repository is now the settings and only the settings, so
// "grain going about its business never commits anything" is a property
// with teeth rather than a truism, and "a clone is a complete restore"
// has become "a clone restores the configuration, and says nothing about
// what this deployment did" -- which is a claim somebody has to keep
// true on purpose.
//
// What it drives, per round, drawn at random:
//
//   - grain writing to its own tables: tasks with read scopes and
//     grants, comments, runs starting and finishing with transcripts on
//     them, observations stamped by a reconcile cycle. None of that
//     reaches the repository, and the cycle after it must commit
//     nothing at all
//   - a human editing the settings, which reach the repository the
//     ordinary way: through the database and the export
//   - somebody merging a pull request against the repository: a template
//     retitled, a template added, a template deleted, a file that is not
//     part of the dump
//   - a push that fails, leaving grain's own export stranded on this host
//     -- and so, once a merge lands on top of it, a divergence
//   - a restart, taking the daemon's own startup path
//   - a human pressing Sync rather than waiting for the timer
//   - an hour passing with nothing else happening
//   - a restore onto a new host: a fresh clone, a fresh database, and
//     everything the repository holds imported into it
//   - the store going away while the working tree survives -- a deleted
//     file, a volume restored from a backup that did not have it -- and
//     the start that has to restore this deployment out of the tree
//     rather than export nothing over it
//
// And what it checks, every round:
//
//   - the working tree is clean after a cycle that reported no error --
//     an export that wrote files it never committed would be a revert
//     waiting to be pushed
//   - grain has made no merge commit, because grain never merges
//   - a round in which grain only wrote to its own tables produced no
//     commit. This is the whole of what the narrowing above bought, and
//     it is the one thing here that a scripted test cannot say, because
//     what would break it is an operational table finding its way back
//     into the export rather than anything anybody wrote down
//   - the settings the daemon is running on are the ones last merged or
//     last edited here, and the settings document in the working tree
//     says the same
//   - the configuration in the repository is the configuration in the
//     database, poured into a scratch database and compared row by row
//   - nothing grain wrote to its own tables has gone missing across a
//     merge, a divergence, a recovery or a restart -- every one of which
//     imports, and every one of which would take task and task_run with
//     it if it imported a table too many
//   - a database restored from the repository re-exports to the same
//     bytes, which is the fixed point the whole arrangement rests on
//
// It is not skipped, and the word is here because it was for nine days
// and nobody could see it: `make soak` is a nightly job with a
// 150-minute budget behind it, and between 2026-09-11 and 2026-09-20 it
// reported success in two minutes having run a t.Skip. A check that
// asserts nothing and reports green is worse than one that is red.
//
// Sixty rounds is half a minute and is worth having on every commit;
// GRAIN_STATEREPO_SOAK_ROUNDS turns it up (two thousand is about ten
// minutes) for a change to this package that deserves one, and
// GRAIN_STATEREPO_SOAK_SEED reproduces a run that failed -- every
// failure names the seed it came from.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite/sqlitetest"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// defaultSoakRounds is what runs without anybody asking for it.
const defaultSoakRounds = 60

func TestSoakTheRepositoryAndTheDatabaseStayInAgreement(t *testing.T) {
	t.Parallel()
	rounds := defaultSoakRounds
	if v := os.Getenv("GRAIN_STATEREPO_SOAK_ROUNDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("GRAIN_STATEREPO_SOAK_ROUNDS=%q is not a number: %v", v, err)
		}
		rounds = n
	}
	seed := time.Now().UnixNano()
	if v := os.Getenv("GRAIN_STATEREPO_SOAK_SEED"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("GRAIN_STATEREPO_SOAK_SEED=%q is not a number: %v", v, err)
		}
		seed = n
	}
	t.Logf("soaking for %d rounds with GRAIN_STATEREPO_SOAK_SEED=%d", rounds, seed)

	d := newSoak(t, seed)
	for round := 1; round <= rounds; round++ {
		d.round = round
		d.act()
		d.cycle()
		d.check()
	}
	d.finish()
}

// soak is the deployment under test: one database, one working tree, one
// bare remote standing in for GitHub, and a clock the churn interval is
// measured against.
type soak struct {
	t     *testing.T
	ctx   context.Context
	rnd   *rand.Rand
	seed  int64
	round int

	store  *model.Store
	db     *sql.DB
	repo   *staterepo.Repo
	dir    string
	remote string
	clock  *soakClock

	// tasks is every task filed into the database this host is currently
	// running on, so a later round can pick one to comment on, run or
	// observe -- and so every round can check that none of them has gone.
	// Emptied when the store is lost, because the repository does not
	// hold them and nothing brings them back.
	tasks []string
	// runs is how many runs have been started into that same database,
	// for the same check one table over.
	runs int
	// templates is what the settings half currently holds, as this side
	// believes it: the expectation every check of a merged change is made
	// against.
	templates map[string]string
	nextID    int
	// quiet says this round did nothing the repository could have an
	// opinion about, so the cycle that follows must commit nothing. Set
	// by act, read by cycle.
	quiet bool

	// What the run did, reported at the end: a soak that never diverged
	// or never restarted tested less than it looks like it did.
	counts map[string]int
}

type soakClock struct{ t time.Time }

func (c *soakClock) now() time.Time { return c.t }

func newSoak(t *testing.T, seed int64) *soak {
	t.Helper()
	ctx := context.Background()
	store, db := openDB(t)
	d := &soak{
		t: t, ctx: ctx, rnd: rand.New(rand.NewSource(seed)), seed: seed,
		store: store, db: db, remote: bareRemote(t),
		dir:       filepath.Join(t.TempDir(), "state"),
		clock:     &soakClock{t: now},
		templates: map[string]string{},
		counts:    map[string]int{},
	}
	// A deployment that has been running a while before the soak starts:
	// settings a human configured, and some history behind them.
	d.putTemplate("tpl-0", "Run the nightly sweep")
	d.putTemplate("tpl-1", "Update the dependencies")
	for i := 0; i < 5; i++ {
		d.fileTask()
	}
	d.open()
	if err := staterepo.Load(d.ctx, d.repo, d.db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding the repository: %v", err)
	}
	return d
}

// open opens (or reopens) the working tree the way cmd/grain does.
func (d *soak) open() {
	d.t.Helper()
	repo, err := staterepo.Open(d.ctx, staterepo.Config{
		Dir: d.dir, Remote: d.remote, AuthorName: "grain", AuthorEmail: "grain@a-host",
		Now: d.clock.now,
	})
	if err != nil {
		d.t.Fatalf("round %d: opening the repository: %v", d.round, err)
	}
	if err := staterepo.EnsureIgnored(repo.Dir()); err != nil {
		d.t.Fatalf("round %d: writing .gitignore: %v", d.round, err)
	}
	d.repo = repo
}

func (d *soak) count(what string) { d.counts[what]++ }

// fatalf names the seed on every failure, because a soak that fails
// without one is a soak nobody can look at twice.
func (d *soak) fatalf(format string, args ...any) {
	d.t.Helper()
	d.t.Fatalf("round %d (GRAIN_STATEREPO_SOAK_SEED=%d): "+format,
		append([]any{d.round, d.seed}, args...)...)
}

// -- what happens to the deployment ------------------------------------

// act draws one round's worth of events. The weights are not arbitrary:
// grain writing to its own database is what a deployment does nearly all
// of the time, and the rare things -- a merge, a failed push, a restart
// -- are rare here in roughly the proportion they are rare there, which
// is what makes a long run cover orderings a scripted one would have to
// name in advance.
func (d *soak) act() {
	d.clock.t = d.clock.t.Add(30 * time.Second)
	// Most rounds are grain writing to tables the repository does not
	// carry, so most rounds must end in no commit at all. cycle holds
	// this to that, and the ones that set it false below are exactly the
	// ones with something to say to the repository.
	d.quiet = true
	switch n := d.rnd.Intn(100); {
	case n < 30:
		d.fileTask()
	case n < 45:
		d.startRun()
	case n < 55:
		d.finishRuns()
	case n < 65:
		d.observe()
	case n < 72:
		d.quiet = false
		d.editSettings()
	case n < 80:
		d.quiet = false
		_ = d.mergePullRequest()
	case n < 84:
		d.quiet = false
		d.strandAnExport()
	case n < 88:
		if d.rnd.Intn(2) == 0 {
			// A merge that lands while the process is down, so the start
			// below is the thing that has to notice and import it.
			d.quiet = false
			_ = d.mergePullRequest()
		}
		d.restart()
	case n < 92:
		d.letAnHourPass()
	case n < 95:
		d.restoreOntoANewHost()
	case n < 97:
		d.loseTheStore()
	default:
		// A quiet round: nothing at all happened. The cycle that follows
		// has to produce no commit, which is the property that keeps a
		// deployment from pushing noise forever.
		d.count("quiet round")
	}
}

func (d *soak) fileTask() {
	d.t.Helper()
	d.nextID++
	id := fmt.Sprintf("task-%04d", d.nextID)
	at := d.clock.t
	tk := task(id)
	tk.CreatedAt = &at
	tk.Reads = []model.RepoRef{{Owner: "owner", Name: "docs"}}
	tk.Grants = []model.Grant{{Capability: "githubtoken"}}
	if d.rnd.Intn(3) == 0 {
		tk.Reads = append(tk.Reads, model.RepoRef{Owner: "owner", Name: "protos"})
	}
	if err := d.store.PutTask(d.ctx, tk); err != nil {
		d.fatalf("filing %s: %v", id, err)
	}
	if _, err := d.store.AddComment(d.ctx, model.Comment{
		TaskID: id, CreatedAt: at, Body: "why this is being asked for",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}},
	}); err != nil {
		d.fatalf("commenting on %s: %v", id, err)
	}
	d.tasks = append(d.tasks, id)
	d.count("tasks filed")
}

func (d *soak) startRun() {
	d.t.Helper()
	if len(d.tasks) == 0 {
		return
	}
	taskID := d.tasks[d.rnd.Intn(len(d.tasks))]
	id := fmt.Sprintf("%s-run-%d", taskID, d.round)
	if err := d.store.StartRun(d.ctx, model.Run{
		ID: id, TaskID: taskID, Sandbox: "grain-" + id,
		Attempt: d.round, StartedAt: d.clock.t,
	}, model.Limits{}); err != nil {
		// One live run per task is a real constraint, and a dispatcher
		// that lost that race would skip too.
		return
	}
	d.runs++
	d.count("runs started")
}

func (d *soak) finishRuns() {
	d.t.Helper()
	rows, err := d.db.QueryContext(d.ctx, "SELECT `id` FROM `task_run` WHERE `finished_at` IS NULL")
	if err != nil {
		d.fatalf("listing live runs: %v", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			d.fatalf("listing live runs: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := d.store.FinishRun(d.ctx, id, d.clock.t, "succeeded", "opened a pull request"); err != nil {
			d.fatalf("finishing %s: %v", id, err)
		}
		if err := d.store.SetRunTranscript(d.ctx, id, randomText(4*1024)); err != nil {
			d.fatalf("writing a transcript for %s: %v", id, err)
		}
		d.count("runs finished")
	}
}

func (d *soak) observe() {
	d.t.Helper()
	for i := 0; i < 5 && i < len(d.tasks); i++ {
		id := d.tasks[len(d.tasks)-1-i]
		at := d.clock.t
		if err := d.store.ObserveField(d.ctx, id, at, func(o *model.Observation) {
			o.PrOpenedAt = &at
		}); err != nil {
			d.fatalf("observing %s: %v", id, err)
		}
	}
	d.count("observation sweeps")
}

// editSettings is a human changing something in the UI, which reaches
// the repository the ordinary way: through the database and the export.
func (d *soak) editSettings() {
	d.t.Helper()
	ids := d.templateIDs()
	if len(ids) == 0 {
		return
	}
	id := ids[d.rnd.Intn(len(ids))]
	d.putTemplate(id, fmt.Sprintf("Edited in the UI at round %d", d.round))
	d.count("settings edited here")
}

func (d *soak) putTemplate(id, title string) {
	d.t.Helper()
	if err := d.store.PutTemplate(d.ctx, model.Template{
		ID: id, Name: id, Title: title, Body: "as configured", CreatedAt: now,
	}); err != nil {
		d.fatalf("writing template %s: %v", id, err)
	}
	d.templates[id] = title
}

func (d *soak) templateIDs() []string {
	out := make([]string, 0, len(d.templates))
	for id := range d.templates {
		out = append(out, id)
	}
	sortStrings(out)
	return out
}

// mergePullRequest is the mechanism this whole package exists for: a
// change to the settings, made as a diff against the repository, merged
// by a human, and pulled down by the deployment on its next cycle.
func (d *soak) mergePullRequest() bool {
	d.t.Helper()
	work := filepath.Join(d.t.TempDir(), fmt.Sprintf("pr-%d", d.round))
	git(d.t, "", "clone", "--quiet", d.remote, work)
	if !staterepo.HasDump(work) {
		// Nothing has been pushed yet, so there is no dump to send a pull
		// request against.
		return false
	}
	path := filepath.Join(work, filepath.FromSlash(staterepo.DumpPath("template")))
	rows := readTemplateRows(d.t, path)
	if len(rows) == 0 {
		return false
	}
	what := ""
	switch d.rnd.Intn(4) {
	case 0:
		// Retitle one.
		i := d.rnd.Intn(len(rows))
		id, _ := rows[i]["id"].(string)
		title := fmt.Sprintf("Merged at round %d", d.round)
		rows[i]["title"] = title
		d.templates[id] = title
		what = "a template retitled"
	case 1:
		// Add one.
		d.nextID++
		id := fmt.Sprintf("tpl-merged-%04d", d.nextID)
		title := fmt.Sprintf("Added by a pull request at round %d", d.round)
		row := map[string]any{}
		for k, v := range rows[0] {
			row[k] = v
		}
		row["id"], row["name"], row["title"] = id, id, title
		rows = append(rows, row)
		d.templates[id] = title
		what = "a template added"
	case 2:
		// Delete one, which is the case that makes the import a
		// replacement rather than a merge.
		if len(rows) < 2 {
			return false
		}
		i := d.rnd.Intn(len(rows))
		id, _ := rows[i]["id"].(string)
		rows = append(rows[:i], rows[i+1:]...)
		delete(d.templates, id)
		what = "a template deleted"
	default:
		// A change to something that is not the dump at all, which grain
		// must carry rather than revert.
		if err := os.WriteFile(filepath.Join(work, "NOTES.md"),
			[]byte(fmt.Sprintf("merged at round %d\n", d.round)), 0o644); err != nil {
			d.fatalf("writing NOTES.md: %v", err)
		}
		what = "a file outside the dump"
	}
	if what != "a file outside the dump" {
		writeTemplateRows(d.t, path, rows)
	}
	git(d.t, work, "add", "--all", ".")
	git(d.t, work, "-c", "user.email=someone@example.com", "-c", "user.name=someone",
		"commit", "-m", "A merged pull request: "+what)
	git(d.t, work, "push", "--quiet", "origin", "main")
	d.count("pull requests merged")
	return true
}

// strandAnExport is a push that fails: the commit stays on this host,
// holding a settings change that exists nowhere else. Half the time a
// merge lands before it can be retried, which is the divergence -- and
// half of those, the process restarts into it, so the recovery is
// reached from a start rather than from a tick.
func (d *soak) strandAnExport() {
	d.t.Helper()
	unreachable := filepath.Join(d.t.TempDir(), "gone.git")
	git(d.t, d.dir, "remote", "set-url", "origin", unreachable)
	// Something to export, or the stranding is a no-op. It has to be a
	// settings change: a settings change is the only thing that reaches
	// the repository at all now, so it is the only thing a failed push
	// can strand.
	//
	// And a task alongside it, which is the other half of what a recovery
	// has to get right. The task is in the database and in no commit,
	// stranded or otherwise, so the reset that clears the divergence must
	// leave it exactly where it is -- check, below, holds every round to
	// that.
	d.editSettings()
	d.fileTask()
	_, _ = staterepo.Sync(d.ctx, d.repo, d.db, model.SchemaVersion)
	git(d.t, d.dir, "remote", "set-url", "origin", d.remote)
	d.count("pushes that failed")
	if d.rnd.Intn(2) != 0 {
		return
	}
	if !d.mergePullRequest() {
		return
	}
	d.count("divergences created")
	if d.rnd.Intn(2) == 0 {
		d.restart()
	}
}

// restart is the daemon starting again over the same data directory:
// staterepo.Load, and, if that is a divergence grain made itself, a
// recovery and a second Load. pkg/cli/daemon.go, verbatim.
func (d *soak) restart() {
	d.t.Helper()
	d.open()
	err := staterepo.Load(d.ctx, d.repo, d.db, model.SchemaVersion)
	if err == nil {
		d.count("restarts")
		return
	}
	if !errors.Is(err, staterepo.ErrDiverged) {
		d.fatalf("a start over the state repository failed: %v", err)
	}
	recovered, rerr := d.repo.RecoverDiverged(d.ctx)
	if rerr != nil || !recovered {
		d.fatalf("a start could not recover a divergence made of grain's own exports: %v %v", recovered, rerr)
	}
	if err := staterepo.Load(d.ctx, d.repo, d.db, model.SchemaVersion); err != nil {
		d.fatalf("loading after a recovery at startup: %v", err)
	}
	d.takeSettingsFromTheRepository()
	d.count("restarts that recovered a divergence")
}

// takeSettingsFromTheRepository is what the model owes a recovery --
// which, since imports became a three-way merge (merge.go), is nothing.
//
// RecoverDiverged throws away the commits this host has that the remote
// has not, and with them the only off-host copy of a settings edit
// committed into an export that never reached the remote. That edit used
// to be lost with them: the Apply straight afterwards replaced every
// settings row with the remote's version, and this function took the
// model's expectation from the tree to match. The import now merges
// against where the two histories part, so a row the merged pull request
// did not touch keeps the database's value and the next export writes it
// back out. The model already holds both sides -- the UI edit, and over
// it whatever the pull request did, which wins a row both touched -- so
// it is checked as it stands.
func (d *soak) takeSettingsFromTheRepository() {
	d.t.Helper()
	d.count("recoveries that kept the database's settings")
}

// loseTheStore is the case the loaded-head marker cannot see: the
// database is gone and the working tree is exactly as this host left it,
// so the marker still names HEAD and the ordinary answer -- "nothing
// arrived, import nothing" -- would come up on a database with nothing
// in it, and the export after it would commit that over the deployment.
//
// The start has to restore instead, out of the working tree it already
// has, and the round after this is the proof: the settings are the ones
// last merged, the repository still holds them, and the export that
// follows commits nothing, because a restore that came back as anything
// other than the bytes it was taken from would commit the difference.
//
// What does not come back is everything else. Tasks, comments, runs and
// observations are not in the repository and nothing here restores them,
// which is the cost side of grain/task-395 stated as an assertion rather
// than as a sentence in a commit message: this is the case where an
// operator finds out. The model empties its own record of them for the
// same reason, and the counters below say how often it happened, so a
// run that never lost a store is not mistaken for one that proved this.
//
// A fresh database at a directory of its own, which is what a deleted
// store file, a restored volume that did not include one, and a
// misconfigured path all amount to by the time sqlite.Open has returned.
// The working tree is untouched -- marker and all -- which is the whole
// point of the case.
func (d *soak) loseTheStore() {
	d.t.Helper()
	store, db := d.openScratchDB()
	d.store, d.db = store, db
	d.open()
	if err := staterepo.Load(d.ctx, d.repo, d.db, model.SchemaVersion); err != nil {
		d.fatalf("starting with the store lost and the working tree kept: %v", err)
	}
	if n := d.taskCount(); n != 0 {
		d.fatalf("a store restored from the repository came back with %d task(s); the repository "+
			"holds configuration only, so a restore that finds tasks means the export has widened "+
			"and this test has an opinion to update", n)
	}
	d.tasks, d.runs = nil, 0
	d.templates = templatesIn(d.t, d.dir)
	d.count("stores lost and restored from the working tree")
}

// letAnHourPass moves the clock on without anything else happening.
//
// It used to be here to cross Config.ChurnInterval, which is what made
// the next export write grain's own record of what it did out on its
// slower clock. Nothing is on that clock any more -- the export is
// configuration, configuration goes out whenever it changes, and the
// interval itself has gone with the rows it governed -- so what this is
// now is the plainest form of the property that matters most: time
// passing, on its own, is never a reason to commit. Sync still has
// hourly work in it (the workflow health check, git's own maintenance)
// and this is what walks the clock past it.
func (d *soak) letAnHourPass() {
	d.clock.t = d.clock.t.Add(time.Hour + time.Minute)
	d.count("hours passed with nothing happening")
}

// restoreOntoANewHost is the claim the repository exists to be able to
// make: a clone of it, on a machine that has never seen this deployment,
// is this deployment.
func (d *soak) restoreOntoANewHost() {
	d.t.Helper()
	dir := filepath.Join(d.t.TempDir(), fmt.Sprintf("restore-%d", d.round))
	restored, restoredDB := d.openScratchDB()
	defer restoredDB.Close()
	repo, err := staterepo.Open(d.ctx, staterepo.Config{Dir: dir, Remote: d.remote, Now: d.clock.now})
	if err != nil {
		d.fatalf("cloning onto a new host: %v", err)
	}
	if err := staterepo.Load(d.ctx, repo, restoredDB, model.SchemaVersion); err != nil {
		d.fatalf("loading onto a new host: %v", err)
	}
	if !staterepo.HasDump(dir) {
		return
	}
	// The dump the new host arrived with has to be the fixed point of its
	// own import: a restore that re-exported to different bytes would
	// commit a diff of itself on its first sync, on every host that ever
	// restored.
	//
	// Only when the tip is grain's own export. A merge that has not been
	// exported over yet holds a document somebody else's editor wrote,
	// and "grain re-exports it in its own shape" is what is supposed to
	// happen to that, not a failure.
	if strings.TrimSpace(git(d.t, dir, "log", "-1", "--format=%an")) == "grain" {
		again := filepath.Join(d.t.TempDir(), fmt.Sprintf("reexport-%d", d.round))
		if err := staterepo.ExportConfig(d.ctx, restoredDB, again); err != nil {
			d.fatalf("re-exporting a restored database: %v", err)
		}
		sameExport(d.t, dir, again)
	}

	// And every setting it restored came back: the whole claim a clone
	// makes now is that the deployment it lands on is configured the way
	// this one is.
	for id, title := range templatesIn(d.t, dir) {
		got, err := restored.GetTemplate(d.ctx, id)
		if err != nil {
			d.fatalf("reading restored template %s: %v", id, err)
		}
		if got == nil {
			d.fatalf("the dump names template %s and the restore does not have it", id)
		}
		if got.Title != title {
			d.fatalf("template %s reads %q on the new host and %q in the dump it came from",
				id, got.Title, title)
		}
	}
	// And nothing else did. A new host is a new deployment with this
	// one's configuration on it, not a copy of this one's history --
	// see loseTheStore for the same statement from the other side.
	if n := d.countTasksIn(restoredDB); n != 0 {
		d.fatalf("a clone onto a new host brought %d task(s) with it; the repository holds "+
			"configuration only", n)
	}
	d.count("restores onto a new host")
}

// openScratchDB opens a throwaway database at this build's schema, for a
// restore or a check to be poured into.
//
// From the per-binary schema template rather than by applying the schema
// again: a long run opens hundreds of these -- one every fifth round for
// the comparison in check, one per restore -- and the schema is the most
// expensive part of opening one by a wide margin. A caller that is done
// with the database should close it rather than leave it to the test's
// own cleanup, for the same reason in file descriptors.
func (d *soak) openScratchDB() (*model.Store, *sql.DB) {
	d.t.Helper()
	return sqlitetest.Open(d.t)
}

// -- one cycle of the daemon's own loop --------------------------------

// cycle is pkg/cli/statemanager.go's cycle: pull and apply what was
// merged, recovering a divergence that is grain's own to recover, then
// export, commit and push. Its error handling is that function's too,
// because the point of driving it here is to drive what the daemon
// actually does.
func (d *soak) cycle() {
	d.t.Helper()
	all := d.rnd.Intn(10) == 0 // a human pressing Sync now and then
	_, applyErr := staterepo.Apply(d.ctx, d.repo, d.db, model.SchemaVersion)
	if errors.Is(applyErr, staterepo.ErrDiverged) {
		recovered, rerr := d.repo.RecoverDiverged(d.ctx)
		if rerr != nil {
			d.fatalf("recovering a divergence made of grain's own exports: %v", rerr)
		}
		if !recovered {
			d.fatalf("a divergence made only of grain's own exports was refused: %v", applyErr)
		}
		d.count("divergences recovered")
		_, applyErr = staterepo.Apply(d.ctx, d.repo, d.db, model.SchemaVersion)
		d.takeSettingsFromTheRepository()
	}
	if applyErr != nil {
		d.fatalf("applying what the remote holds: %v", applyErr)
	}
	export := staterepo.Sync
	if all {
		export = staterepo.SyncAll
		d.count("syncs asked for by hand")
	}
	changed, err := export(d.ctx, d.repo, d.db, model.SchemaVersion)
	if err != nil {
		d.fatalf("exporting: %v", err)
	}
	if changed {
		d.count("commits")
	}
	// A round in which grain only wrote to its own tables has to leave the
	// repository alone entirely -- not "commit a small diff", not "commit
	// a timestamp", nothing.
	//
	// This is the assertion the export being configuration-only exists to
	// make true, and it is the one a scripted test cannot make for itself:
	// what breaks it is a table finding its way back into ExportConfig, or
	// something the sync path writes into the working tree as a side
	// effect, and neither announces itself. On a deployment that files
	// tasks and runs runs all day and changes a setting once a week, the
	// difference between this holding and not holding is the difference
	// between a couple of dozen commits a day and 2,880 of them.
	if changed && d.quiet {
		d.fatalf("a round that only wrote to grain's own tables committed to the repository:\n%s",
			git(d.t, d.dir, "show", "--stat", "--format=%s", "HEAD"))
	}
	// An export of a database nothing has touched since the last one has
	// to commit nothing. It is the property the whole cadence rests on --
	// a dump whose row order or formatting wandered would commit and push
	// on every tick forever, burying the changes that matter -- and it is
	// only ever true if it is true here, after whatever this round did.
	if again, err := staterepo.Sync(d.ctx, d.repo, d.db, model.SchemaVersion); err != nil {
		d.fatalf("re-exporting an untouched database: %v", err)
	} else if again {
		d.fatalf("exporting an untouched database committed something:\n%s",
			git(d.t, d.dir, "show", "--stat", "--format=%s", "HEAD"))
	}
}

// -- what is checked, every round --------------------------------------

func (d *soak) check() {
	d.t.Helper()
	// Nothing uncommitted. An export that wrote files and did not commit
	// them leaves the working tree holding a diff nobody made, which the
	// next commit would push as if grain meant it.
	if status := strings.TrimSpace(git(d.t, d.dir, "status", "--porcelain")); status != "" {
		d.fatalf("the working tree is not clean after a cycle:\n%s", status)
	}
	// grain never merges: Pull is fast-forward only, and a merge commit
	// in this history would mean something resolved a conflict in a
	// database dump by guesswork.
	if merges := strings.TrimSpace(git(d.t, d.dir, "log", "--merges", "--format=%H")); merges != "" {
		d.fatalf("the history holds a merge commit grain would never have made:\n%s", merges)
	}
	// The repository holds nothing operational, whatever this round did.
	// Stated here rather than left to the export's own tests because the
	// ways back in are sequences: a path that imports an old dump, a
	// recovery that resurrects a file a later export removed.
	for _, table := range []string{"task", "task_comment", "task_read", "task_grant",
		"task_run", "task_observation", "ci_run", "ci_job", "ci_step", "lease"} {
		if _, err := os.Stat(filepath.Join(d.dir, staterepo.TablesDir, table+".json")); err == nil {
			d.fatalf("%s/%s.json is in the repository; the export is configuration only",
				staterepo.TablesDir, table)
		}
	}

	// Nothing grain wrote to its own tables has gone missing. This is the
	// invariant the whole run exists to hold, and the narrowing of the
	// export made it sharper rather than weaker: a task filed is a task
	// the deployment still has, however many merges, failed pushes,
	// recoveries and restarts have happened since, and none of those can
	// lean on the repository to put it back. Every path here that imports
	// is a path that replaces rows, and one that named a table too many
	// would delete exactly these.
	for _, id := range d.tasks {
		got, err := d.store.GetTask(d.ctx, id)
		if err != nil {
			d.fatalf("reading task %s: %v", id, err)
		}
		if got == nil {
			d.fatalf("task %s was filed and is no longer in the database", id)
		}
		if len(got.Reads) == 0 {
			d.fatalf("task %s has lost the repos it may read", id)
		}
	}
	if n := d.taskCount(); n != len(d.tasks) {
		d.fatalf("the database holds %d tasks and %d were filed", n, len(d.tasks))
	}
	if n := d.runCount(); n != d.runs {
		d.fatalf("the database holds %d runs and %d were started", n, d.runs)
	}

	// The settings the deployment is running on are the ones last merged
	// or last edited here, whichever came later -- which is the whole
	// point of the mechanism.
	for id, title := range d.templates {
		got, err := d.store.GetTemplate(d.ctx, id)
		if err != nil {
			d.fatalf("reading template %s: %v", id, err)
		}
		if got == nil {
			d.fatalf("template %s is gone from the database; it should read %q", id, title)
		}
		if got.Title != title {
			d.fatalf("template %s reads %q; it should read %q", id, got.Title, title)
		}
	}
	if n := d.templateCount(); n != len(d.templates) {
		d.fatalf("the database holds %d templates and %d were expected", n, len(d.templates))
	}
	// And the document in the working tree says the same thing the
	// database does. The two can only disagree after a cycle that
	// exported: a reader of this repository -- a human opening a pull
	// request against it, `grain state check` in CI -- sees the file, not
	// the database, so an export that wrote a stale one would be wrong in
	// the only place anybody looks.
	if why := differingTemplates(templatesIn(d.t, d.dir), d.templates); why != "" {
		d.fatalf("%s holds %s", staterepo.DumpPath("template"), why)
	}

	// The configuration in the repository is the configuration in the
	// database, table by table and column by column rather than by the
	// handful of fields above.
	//
	// Every fifth round rather than every one: it pours the dump into a
	// database of its own, which is the most expensive thing here by a
	// distance and says the same thing whether it runs 60 times or 12.
	// finish() does it once more, against the remote, at the end.
	if d.round%5 != 0 {
		return
	}
	_, mirror := d.openScratchDB()
	defer mirror.Close()
	if err := staterepo.ImportTables(d.ctx, mirror, d.dir, configTables()); err != nil {
		d.fatalf("importing the working tree into a scratch database: %v", err)
	}
	sameTables(d.t, d.ctx, d.db, mirror, configTables(), "the working tree against the database")
}

func (d *soak) taskCount() int {
	d.t.Helper()
	return d.countTasksIn(d.db)
}

func (d *soak) countTasksIn(db *sql.DB) int {
	d.t.Helper()
	var n int
	if err := db.QueryRowContext(d.ctx, "SELECT COUNT(*) FROM `task`").Scan(&n); err != nil {
		d.fatalf("counting tasks: %v", err)
	}
	return n
}

func (d *soak) runCount() int {
	d.t.Helper()
	var n int
	if err := d.db.QueryRowContext(d.ctx, "SELECT COUNT(*) FROM `task_run`").Scan(&n); err != nil {
		d.fatalf("counting runs: %v", err)
	}
	return n
}

func (d *soak) templateCount() int {
	d.t.Helper()
	var n int
	if err := d.db.QueryRowContext(d.ctx, "SELECT COUNT(*) FROM `template`").Scan(&n); err != nil {
		d.fatalf("counting templates: %v", err)
	}
	return n
}

// -- the end of the run ------------------------------------------------

// finish leaves the deployment in the state a clean shutdown does -- one
// last sync -- and then asks the question the whole soak was building up
// to: is a clone of what was pushed configured the way the deployment
// that pushed it is?
//
// Not "is it the database that pushed it", which is what this used to
// ask and is no longer a claim the repository makes. The comparison is
// over configTables for that reason, and the two assertions under it are
// the other half of the same answer: what the clone does not have, it is
// not supposed to have.
func (d *soak) finish() {
	d.t.Helper()
	if _, err := staterepo.SyncAll(d.ctx, d.repo, d.db, model.SchemaVersion); err != nil {
		d.fatalf("the last sync of the run: %v", err)
	}
	dir := filepath.Join(d.t.TempDir(), "final-restore")
	_, restoredDB := d.openScratchDB()
	repo, err := staterepo.Open(d.ctx, staterepo.Config{Dir: dir, Remote: d.remote, Now: d.clock.now})
	if err != nil {
		d.fatalf("cloning the remote at the end of the run: %v", err)
	}
	if err := staterepo.Load(d.ctx, repo, restoredDB, model.SchemaVersion); err != nil {
		d.fatalf("restoring the remote at the end of the run: %v", err)
	}
	sameTables(d.t, d.ctx, d.db, restoredDB, configTables(),
		"a database restored from the remote at the end of the run")
	if n := d.countTasksIn(restoredDB); n != 0 {
		d.fatalf("the restore at the end of the run brought %d task(s) back; the repository holds "+
			"configuration only", n)
	}

	// And the same directory has to pass the check a pull request against
	// this repository runs in CI, which is what a reviewer's green tick
	// means.
	_, checkDB := d.openScratchDB()
	report, err := staterepo.Check(d.ctx, checkDB, dir, model.SchemaVersion)
	if err != nil {
		d.fatalf("`grain state check` on what the run pushed: %v", err)
	}
	if len(report.Warnings) != 0 {
		d.fatalf("`grain state check` warned about what grain itself wrote: %v", report.Warnings)
	}

	var summary []string
	for _, what := range sortedKeys(d.counts) {
		summary = append(summary, fmt.Sprintf("%s: %d", what, d.counts[what]))
	}
	d.t.Logf("%d rounds, %d rows restored: %s", d.round, report.Total(), strings.Join(summary, ", "))
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
