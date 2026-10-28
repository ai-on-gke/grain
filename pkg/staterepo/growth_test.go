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

package staterepo_test

// How fast does a state repository grow?
//
// bwsalmon/grain#174 exports the whole database on a 30s timer and
// commits whatever changed. For settings that is the point of the
// exercise -- they change rarely, and every change deserves a commit.
// For the tables grain writes to itself on every reconcile cycle it is
// not: a busy deployment commits a diff of task_run and task_observation
// every 30 seconds forever, and git keeps every version of every file it
// has ever been shown.
//
// This file measures that rather than guessing at it. It drives a real
// SQLite database through a day's worth of reconcile ticks with a
// workload shaped like a busy single-operator deployment, syncs a real
// git repository on every tick, and reports what the repository costs:
// how many commits, how large .git is loose and packed, and which files
// each commit was actually about. It runs the same day twice -- once
// with grain#174's cadence, once with this build's -- so the two are
// numbers side by side rather than an argument.
//
// It is skipped unless GRAIN_GROWTH_TICKS says how long a day to
// simulate (2880 is the real one), because a real measurement takes tens
// of minutes and belongs in a reviewer's hands rather than in CI. The
// property it measures is guarded cheaply in tier_test.go instead.

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// ticksPerDay is 24h of the daemon's own 30s state sync.
const ticksPerDay = 2880

// tickInterval is that sync, and the reconcile cycle it observes on.
const tickInterval = 30 * time.Second

// workload is the shape of a busy deployment, as the numbers a simulated
// day is built out of. They are not arbitrary: max_workers on a
// single-operator install is a handful, a run takes tens of minutes, and
// a reconcile cycle stamps observed_at on every task whose pull request
// it is still watching.
type workload struct {
	// seedTasks is how much history the deployment already has when the
	// day starts -- what makes the dump the size it actually is rather
	// than the size an empty one would be.
	seedTasks int
	// watched is how many tasks a reconcile cycle observes, and so how
	// many task_observation rows change every single tick.
	watched int
	// runsPerDay is how many runs start and finish over the day.
	runsPerDay int
	// tasksPerDay is how many new tasks are filed.
	tasksPerDay int
	// settingsChangesPerDay is how often a human or a merged pull request
	// actually changes something the repository exists for. This is the
	// signal every other number here is noise against.
	settingsChangesPerDay int
	// transcriptBytes and promptBytes are what a finished run writes into
	// task_run: the two columns that make that table the largest thing in
	// the dump by a distance.
	transcriptBytes int
	promptBytes     int
}

func busyDeployment() workload {
	return workload{
		seedTasks:             400,
		watched:               25,
		runsPerDay:            300,
		tasksPerDay:           60,
		settingsChangesPerDay: 4,
		transcriptBytes:       30 * 1024,
		promptBytes:           6 * 1024,
	}
}

func TestStateRepoGrowth(t *testing.T) {
	t.Parallel()
	v := os.Getenv("GRAIN_GROWTH_TICKS")
	if v == "" {
		t.Skip("set GRAIN_GROWTH_TICKS (2880 is a day) to measure how the state repository grows")
	}
	ticks, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("GRAIN_GROWTH_TICKS=%q is not a number: %v", v, err)
	}

	// The two arms are what git does with what accumulates, which is
	// the only dimension left. The third this used to have -- how often
	// grain's own record of what it did was written out -- went with the
	// rows themselves: the export is the configuration now, so a
	// simulated day of a busy deployment that nobody reconfigures has
	// nothing to commit on any clock.
	for _, scenario := range []struct {
		name string
		pack bool
	}{
		// grain#174 as merged: nothing ever packs what the exports leave
		// behind.
		{"grain#174: unpacked", false},
		// This build: git packing what accumulates.
		{"this build: packed", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			measure(t, ticks, scenario.pack)
		})
	}
}

func measure(t *testing.T, ticks int, pack bool) {
	ctx := context.Background()
	store, db := openDB(t)
	sim := newSimulation(t, store, db, busyDeployment())

	dir := t.TempDir()
	repo, err := staterepo.Open(ctx, staterepo.Config{
		Dir: dir, Now: sim.clock.now,
	})
	if err != nil {
		t.Fatalf("opening the repository: %v", err)
	}
	if !pack {
		// gc.auto = 0 turns off the housekeeping Repo.maintain leans on,
		// which is how this arm reproduces what grain#174 actually did.
		git(t, dir, "config", "gc.auto", "0")
	}
	if err := staterepo.Seed(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	// Packed, so the baseline and the end state are measured the same way
	// and the difference between them is what the day cost.
	git(t, dir, "gc", "--quiet", "--prune=now")
	seeded := gitSize(t, dir)

	commits, spent := 0, time.Duration(0)
	for tick := 0; tick < ticks; tick++ {
		sim.tick(ctx, tick)
		start := time.Now()
		changed, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion)
		spent += time.Since(start)
		if err != nil {
			t.Fatalf("syncing at tick %d: %v", tick, err)
		}
		if changed {
			commits++
		}
	}

	loose := gitSize(t, dir)
	git(t, dir, "gc", "--quiet", "--prune=now")
	packed := gitSize(t, dir)

	t.Logf("%d ticks (%.2f days), packing %v", ticks, float64(ticks)/ticksPerDay, pack)
	t.Logf("  commits:          %d", commits)
	t.Logf("  .git seeded:      %s (packed)", bytesHuman(seeded))
	t.Logf("  .git grew by:     %s as the day ran, %s once packed",
		bytesHuman(loose-seeded), bytesHuman(packed-seeded))
	t.Logf("  working tree:     %s", bytesHuman(treeSize(t, dir)))
	t.Logf("  time in Sync:     %s total, %s per tick", spent.Round(time.Second),
		(spent / time.Duration(ticks)).Round(time.Millisecond))
	for _, l := range churnReport(t, dir) {
		t.Logf("  %s", l)
	}
}

// churnReport attributes the repository's history to the files it is
// made of: for each path, how many commits touched it and how much diff
// it accounts for. That is the measurement the whole question turns on
// -- whether the repository is large because settings changed or because
// grain wrote down what it was doing.
func churnReport(t *testing.T, dir string) []string {
	t.Helper()
	out := git(t, dir, "log", "--numstat", "--pretty=format:@@", "--no-renames")
	type stat struct{ commits, added, removed int }
	stats := map[string]*stat{}
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if line == "@@" {
			seen = map[string]bool{}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		added, _ := strconv.Atoi(fields[0])
		removed, _ := strconv.Atoi(fields[1])
		path := fields[2]
		s := stats[path]
		if s == nil {
			s = &stat{}
			stats[path] = s
		}
		if !seen[path] {
			s.commits++
			seen[path] = true
		}
		s.added += added
		s.removed += removed
	}
	var lines []string
	for path, s := range stats {
		if s.commits < 2 {
			// Written once, at the seed, and never touched again: the
			// interesting rows are the ones a day of ticks kept rewriting.
			continue
		}
		lines = append(lines, fmt.Sprintf("%-40s %5d commits %9d +lines %9d -lines",
			path, s.commits, s.added, s.removed))
	}
	sortStrings(lines)
	return lines
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// simClock is the deployment's own clock, advanced a tick at a time.
// staterepo measures its churn interval against Config.Now, so a
// simulated day passes in a simulated day regardless of how long the
// measurement itself takes to run.
type simClock struct{ t time.Time }

func (c *simClock) now() time.Time { return c.t }

// simulation drives the database the way a running daemon does.
type simulation struct {
	t     *testing.T
	store *model.Store
	db    *sql.DB
	w     workload

	rnd      *rand.Rand
	clock    *simClock
	tasks    []string
	live     []liveRun
	nextRun  int
	settings int
}

type liveRun struct {
	id    string
	until int // the tick it finishes on
}

func newSimulation(t *testing.T, store *model.Store, db *sql.DB, w workload) *simulation {
	s := &simulation{
		t: t, store: store, db: db, w: w,
		rnd: rand.New(rand.NewSource(1)), clock: &simClock{t: now},
	}
	s.seed()
	return s
}

// seed builds the deployment the day starts from: settings a human
// configured once, and the tasks, runs and comments of the weeks before.
func (s *simulation) seed() {
	ctx := context.Background()
	s.putSettings(ctx)
	for i := 0; i < s.w.seedTasks; i++ {
		id := fmt.Sprintf("seed-%04d", i)
		s.file(ctx, id)
		// Roughly two runs per historical task, all long finished.
		for a := 1; a <= 2; a++ {
			s.finishedRun(ctx, id, a)
		}
	}
}

// putSettings writes the rows this repository exists for: templates and
// repo configuration. They are the thing a human or an agent proposes a
// change to, and the thing a diff of the state repository is supposed to
// be about.
func (s *simulation) putSettings(ctx context.Context) {
	body := strings.Repeat("a standing instruction for every run in this repo. ", 20) +
		strconv.Itoa(s.settings)
	for i := 0; i < 8; i++ {
		if err := s.store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:                model.RepoRef{Owner: "owner", Name: fmt.Sprintf("repo-%d", i)},
			DefaultCapabilities: []string{"githubtoken", "gcpkey"},
			PromptExtension:     body,
			SetupCommand:        "make deps",
		}); err != nil {
			s.t.Fatalf("writing repo_config: %v", err)
		}
	}
	for i := 0; i < 12; i++ {
		if err := s.store.PutTemplate(ctx, model.Template{
			ID:        fmt.Sprintf("tpl-%d", i),
			Name:      fmt.Sprintf("template %d", i),
			Title:     "Do the thing",
			Body:      body,
			Reads:     []model.RepoRef{{Owner: "owner", Name: "payments-api"}},
			CreatedAt: s.clock.t,
		}); err != nil {
			s.t.Fatalf("writing task_template: %v", err)
		}
	}
	s.settings++
}

func (s *simulation) file(ctx context.Context, id string) {
	at := s.clock.t
	tk := task(id)
	tk.CreatedAt = &at
	if err := s.store.PutTask(ctx, tk); err != nil {
		s.t.Fatalf("filing %s: %v", id, err)
	}
	if _, err := s.store.AddComment(ctx, model.Comment{
		TaskID: id, CreatedAt: at,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}},
		Body:   strings.Repeat("a sentence of context on what this task is for. ", 8),
	}); err != nil {
		s.t.Fatalf("commenting on %s: %v", id, err)
	}
	s.tasks = append(s.tasks, id)
}

// finishedRun writes one complete run, transcript and prompt and all --
// the row shape that makes task_run.json the largest file in the dump.
func (s *simulation) finishedRun(ctx context.Context, taskID string, attempt int) {
	id := fmt.Sprintf("%s-%d", taskID, attempt)
	if err := s.store.StartRun(ctx, model.Run{
		ID: id, TaskID: taskID, Sandbox: "grain-" + id, Attempt: attempt, StartedAt: s.clock.t,
	}, model.Limits{}); err != nil {
		s.t.Fatalf("starting %s: %v", id, err)
	}
	s.completeRun(ctx, id, s.clock.t.Add(18*time.Minute))
}

func (s *simulation) completeRun(ctx context.Context, id string, at time.Time) {
	if err := s.store.FinishRun(ctx, id, at, "succeeded", "opened a pull request"); err != nil {
		s.t.Fatalf("finishing %s: %v", id, err)
	}
	if err := s.store.SetRunTranscript(ctx, id, s.text(s.w.transcriptBytes)); err != nil {
		s.t.Fatalf("transcript for %s: %v", id, err)
	}
	if err := s.store.SetRunPrompt(ctx, id, s.text(s.w.promptBytes)); err != nil {
		s.t.Fatalf("prompt for %s: %v", id, err)
	}
}

// text is filler of roughly the right size and roughly the right
// compressibility: English-shaped, not random bytes, because git's own
// packing is what a pile of random blobs would misrepresent.
func (s *simulation) text(n int) string {
	words := []string{"the", "agent", "ran", "make", "test", "and", "it", "failed", "with",
		"an", "error", "in", "pkg", "model", "store", "so", "I", "changed", "the", "query",
		"pushed", "the", "branch", "opened", "a", "pull", "request", "waited", "for", "checks"}
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(words[s.rnd.Intn(len(words))])
		b.WriteByte(' ')
	}
	return b.String()
}

// tick advances the simulated deployment by one reconcile cycle.
func (s *simulation) tick(ctx context.Context, tick int) {
	s.clock.t = s.clock.t.Add(tickInterval)

	// Every cycle stamps observed_at on every task whose pull request the
	// orchestrator is still watching. This is the churn nothing asked
	// for: no human wrote it, no agent will read it, and it changes on
	// every single tick.
	for i := 0; i < s.w.watched && i < len(s.tasks); i++ {
		id := s.tasks[len(s.tasks)-1-i]
		at := s.clock.t
		if err := s.store.ObserveField(ctx, id, at, func(o *model.Observation) {
			o.PrOpenedAt = &at
		}); err != nil {
			s.t.Fatalf("observing %s: %v", id, err)
		}
	}

	// Runs finish on their own schedule.
	var still []liveRun
	for _, r := range s.live {
		if r.until > tick {
			still = append(still, r)
			continue
		}
		s.completeRun(ctx, r.id, s.clock.t)
	}
	s.live = still

	if due(tick, s.w.tasksPerDay) {
		s.file(ctx, fmt.Sprintf("day-%04d", tick))
	}
	if due(tick, s.w.settingsChangesPerDay) {
		s.putSettings(ctx)
	}
	if due(tick, s.w.runsPerDay) && len(s.tasks) > 0 {
		taskID := s.tasks[s.rnd.Intn(len(s.tasks))]
		s.nextRun++
		id := fmt.Sprintf("run-%05d", s.nextRun)
		if err := s.store.StartRun(ctx, model.Run{
			ID: id, TaskID: taskID, Sandbox: "grain-" + id,
			Attempt: 100 + s.nextRun, StartedAt: s.clock.t,
		}, model.Limits{}); err != nil {
			// One live run per task is a real constraint; skipping is what
			// the dispatcher would do too.
			return
		}
		s.live = append(s.live, liveRun{id: id, until: tick + 36})
	}
}

// due spreads n events evenly over a day's worth of ticks.
func due(tick, perDay int) bool {
	every := ticksPerDay / perDay
	if every < 1 {
		every = 1
	}
	return tick%every == 0
}

func gitSize(t *testing.T, dir string) int64 { return dirSize(t, filepath.Join(dir, ".git")) }

func treeSize(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("measuring %s: %v", dir, err)
	}
	return total
}

func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("measuring %s: %v", dir, err)
	}
	return total
}

func bytesHuman(n int64) string {
	switch {
	case n > 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n > 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n > 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
