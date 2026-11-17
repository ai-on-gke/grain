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

// scale_test.go drives one deployment shape -- 100 running tasks, 1,000
// waiting tasks, 50,000 tasks in total -- against a real store (SQLite
// and PostgreSQL) and a real HTTP server, and says where it stops
// scaling. scalability_test.go and loadtest_test.go each push one
// dimension at a time (many teams, many CI streams, a sustained queue of
// a few dozen); neither ever has a large history, a large waiting queue
// and a full house of live runs at the same moment, which is what a busy
// deployment a few months old looks like and the shape every O(N) path
// in the tick, the queue and the list endpoints pays for.
//
// What runs, all at once, for cfg.duration:
//
//   - the store: cfg.total tasks, of which almost all are history
//     (closed, merged, failed, with runs, comments, custom fields,
//     depends_on edges and close events), cfg.waiting are waiting in
//     every way a task waits (queued, proposed, awaiting_reply, blocked
//     on an open depends_on, parked by resume_later), and cfg.running
//     hold live runs;
//   - the dispatcher: dispatch.Cycle, the selection and StartRun half of
//     every real dispatch, filling every slot that frees from the waiting
//     queue. A run itself is a fake: a goroutine that heartbeats
//     activity, writes transcript and status updates and comments the way
//     a sandbox's MCP and API proxy calls land in the store, then
//     finishes and closes its task and files a replacement so the waiting
//     queue stays at cfg.waiting. No subprocess, no VM: what is measured
//     is grain, not the sandbox;
//   - the orchestrator: orchestrator.RunCycle ticking back to back with
//     every slot already full (MaxWorkers is the live count's floor, so it
//     never dispatches a real sandbox itself), timing every reconciler
//     through CycleTimes exactly as GET /api/metrics serves it;
//   - the HTTP API: cfg.apiWorkers clients per endpoint on the task list
//     (default, state filters, substring search, cursor pages, summary),
//     the task detail and its conversation, /api/metrics,
//     /api/tasks/states and /api/inbox,
//     plus cfg.sseClients subscribers on /api/events?stream=1;
//   - a sampler reading the process's RSS, heap, goroutines and open file
//     descriptors.
//
// It then fails on anything a deployment would notice: an error from the
// store, a request that was not a 2xx, a tick, a dispatch or an endpoint
// whose p95 blew its budget, a slot that stopped being refilled. The
// budgets are sized for a 4-vCPU ubuntu-latest runner (the nightly
// `scale` job) and every one is an env var, so a larger stress run on a
// bigger host is the same test with different numbers (scaleConfigFromEnv).
//
// TestScaleSmoke runs the same harness at a few hundred tasks on every
// commit, ungated, so a change that breaks the harness breaks `make test`
// rather than the nightly. TestScale50k is the real run, gated on
// GRAIN_SCALE_TEST (`make scale`). docs/test-runs/2026-11-15-scale-50k.md
// has the method and the numbers this file measured.
package e2e

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type scaleConfig struct {
	total, waiting, running int
	repos                   int
	duration                time.Duration
	runLifetime             time.Duration // how long a fake run lives, +/- half
	heartbeat               time.Duration
	apiWorkers, sseClients  int
	seedWorkers             int

	// Budgets, at p95. Zero reports the number without judging it.
	tickP95, dispatchP95, apiP95, listAPIP95, writeP95 time.Duration
	// allowUnansweredLists lets the whole-store list endpoints named in
	// scaleUnansweredListAllowance finish with only api-unanswered/ samples
	// until their SQL paging/filtering follow-up lands.
	allowUnansweredLists bool
	// tickTimeout bounds one RunCycle, and requestTimeout one API call;
	// either running out is an error, not a slow sample.
	tickTimeout, requestTimeout time.Duration
	// minCompletions is how many fake runs must have finished (and so
	// had their slot refilled) by the end -- the continuous-dispatch
	// check. Zero disables it.
	minCompletions int
}

func defaultScaleConfig() scaleConfig {
	return scaleConfig{
		total: 50000, waiting: 1000, running: 100, repos: 20,
		duration: 4 * time.Minute, runLifetime: 8 * time.Second, heartbeat: 500 * time.Millisecond,
		apiWorkers: 1, sseClients: 10, seedWorkers: 8,
		// Ceilings, not targets. At this shape today's code takes most of
		// a minute to dispatch, up to two to tick and nearly two to list
		// on SQLite (docs/test-runs/2026-11-15-scale-50k.md), so a budget a
		// healthy deployment would want fails every night and gates
		// nothing. These sit at about three times what a 7-vCPU host
		// measured, for a 4-vCPU
		// hosted runner: what they catch is a regression on top of where
		// things are -- a tick or an endpoint that gets several times
		// worse, an endpoint that stops answering, a store error, a slot
		// that stops being refilled. duration (4m) stays above apiP95 (2m)
		// so cutOff catches an in-flight request that stalls past budget,
		// while the whole-store list endpoints that cannot answer under 7x
		// concurrency today are named in scaleUnansweredListAllowance until
		// follow-up #3 lands. Each follow-up that fixes a bottleneck is
		// meant to lower the budget it moved; the doc names the targets.
		tickP95: 6 * time.Minute, dispatchP95: 3 * time.Minute,
		apiP95: 2 * time.Minute, listAPIP95: 6 * time.Minute, writeP95: 20 * time.Second,
		allowUnansweredLists: true,
		minCompletions:       100,
		tickTimeout:          8 * time.Minute, requestTimeout: 8 * time.Minute,
	}
}

func scaleConfigFromEnv(t *testing.T) scaleConfig {
	t.Helper()
	cfg := defaultScaleConfig()
	envInt(t, "GRAIN_SCALE_TOTAL", &cfg.total)
	envInt(t, "GRAIN_SCALE_WAITING", &cfg.waiting)
	envInt(t, "GRAIN_SCALE_RUNNING", &cfg.running)
	envInt(t, "GRAIN_SCALE_REPOS", &cfg.repos)
	envInt(t, "GRAIN_SCALE_API_CLIENTS", &cfg.apiWorkers)
	envInt(t, "GRAIN_SCALE_SSE_CLIENTS", &cfg.sseClients)
	envInt(t, "GRAIN_SCALE_SEED_WORKERS", &cfg.seedWorkers)
	envInt(t, "GRAIN_SCALE_MIN_COMPLETIONS", &cfg.minCompletions)
	envDuration(t, "GRAIN_SCALE_DURATION", &cfg.duration)
	envDuration(t, "GRAIN_SCALE_RUN_LIFETIME", &cfg.runLifetime)
	envDuration(t, "GRAIN_SCALE_TICK_P95", &cfg.tickP95)
	envDuration(t, "GRAIN_SCALE_DISPATCH_P95", &cfg.dispatchP95)
	envDuration(t, "GRAIN_SCALE_API_P95", &cfg.apiP95)
	envDuration(t, "GRAIN_SCALE_LIST_API_P95", &cfg.listAPIP95)
	envDuration(t, "GRAIN_SCALE_WRITE_P95", &cfg.writeP95)
	envDuration(t, "GRAIN_SCALE_TICK_TIMEOUT", &cfg.tickTimeout)
	envDuration(t, "GRAIN_SCALE_REQUEST_TIMEOUT", &cfg.requestTimeout)
	if v := strings.TrimSpace(os.Getenv("GRAIN_SCALE_STRICT_LISTS")); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		cfg.allowUnansweredLists = false
	}
	return cfg
}

// scaleBackends is which stores a run covers: GRAIN_SCALE_BACKENDS
// (comma-separated, "sqlite,postgres" by default). PostgreSQL skips when
// there is no server or binaries to start one, as everywhere else in
// this package, unless GRAIN_REQUIRE_POSTGRES says it must not.
func scaleBackends() []string {
	if v := os.Getenv("GRAIN_SCALE_BACKENDS"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"sqlite", "postgres"}
}

// TestScale50k is the 100 running / 1,000 waiting / 50,000 total run.
func TestScale50k(t *testing.T) {
	if os.Getenv("GRAIN_SCALE_TEST") == "" {
		t.Skip("set GRAIN_SCALE_TEST=1 to run the 100 running / 1,000 waiting / 50,000 total scale test (make scale)")
	}
	cfg := scaleConfigFromEnv(t)
	// dispatch.Cycle logs every candidate it passes over, which at 1,000
	// waiting is megabytes a minute of nothing the report needs.
	// GRAIN_SCALE_VERBOSE keeps it. Not parallel, so the swap is safe.
	if os.Getenv("GRAIN_SCALE_VERBOSE") == "" {
		prev := log.Writer()
		log.SetOutput(io.Discard)
		t.Cleanup(func() { log.SetOutput(prev) })
	}
	for _, backend := range scaleBackends() {
		t.Run(backend, func(t *testing.T) { runScale(t, backend, cfg) })
	}
}

// TestScaleSmoke is the same harness at a size that costs seconds, run
// on every commit. It judges nothing that is wall-clock: it shares a CI
// machine with the whole suite under -race.
func TestScaleSmoke(t *testing.T) {
	t.Parallel()
	cfg := scaleConfig{
		total: 200, waiting: 40, running: 6, repos: 3,
		duration: 3 * time.Second, runLifetime: 600 * time.Millisecond, heartbeat: 100 * time.Millisecond,
		apiWorkers: 1, sseClients: 2, seedWorkers: 2,
		minCompletions: 1,
		tickTimeout:    2 * time.Minute, requestTimeout: 2 * time.Minute,
	}
	for _, backend := range scaleBackends() {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			runScale(t, backend, cfg)
		})
	}
}

func openScaleStore(t *testing.T, backend string) (*model.Store, *sql.DB) {
	t.Helper()
	switch backend {
	case "sqlite":
		db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
		if err != nil {
			t.Fatalf("opening sqlite: %v", err)
		}
		t.Cleanup(func() { db.Close() })
		store := model.New(db)
		if err := store.Init(context.Background()); err != nil {
			t.Fatalf("schema init: %v", err)
		}
		return store, db
	case "postgres":
		store, db := postgrestest.Open(t)
		return store, db
	}
	t.Fatalf("GRAIN_SCALE_BACKENDS: unknown backend %q (want sqlite or postgres)", backend)
	return nil, nil
}

// scaleStats is every number a run records, by name.
type scaleStats struct {
	mu        sync.Mutex
	latencies map[string]*LatencyTracker
	errors    map[string]int
	errSample map[string]string
	bytes     map[string]int64
	counters  map[string]*atomic.Int64
}

func newScaleStats() *scaleStats {
	return &scaleStats{
		latencies: map[string]*LatencyTracker{}, errors: map[string]int{},
		errSample: map[string]string{}, bytes: map[string]int64{}, counters: map[string]*atomic.Int64{},
	}
}

func (s *scaleStats) lat(name string) *LatencyTracker {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.latencies[name]
	if l == nil {
		l = &LatencyTracker{}
		s.latencies[name] = l
	}
	return l
}

func (s *scaleStats) count(name string) *atomic.Int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counters[name]
	if c == nil {
		c = &atomic.Int64{}
		s.counters[name] = c
	}
	return c
}

func (s *scaleStats) fail(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors[name]++
	if _, ok := s.errSample[name]; !ok {
		s.errSample[name] = err.Error()
	}
}

func (s *scaleStats) addBytes(name string, n int64) {
	s.mu.Lock()
	s.bytes[name] += n
	s.mu.Unlock()
}

// timed runs f, recording its latency under name and any error under
// name too, and reports whether it succeeded.
func (s *scaleStats) timed(name string, f func() error) bool {
	start := time.Now()
	err := f()
	s.lat(name).Record(time.Since(start))
	if err != nil {
		s.fail(name, err)
		return false
	}
	return true
}

var scaleActor = model.Principal{Kind: model.PrincipalHuman, ID: "scale-tester"}

// scaleBody is a description the size of a real one (a few KB of
// prose), different per task so substring search has something to scan.
func scaleBody(i int, rng *rand.Rand) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Scale task %d\n\nThis task was seeded by the scale harness. ", i)
	words := []string{"orchestrator", "dispatch", "sqlite", "postgres", "reconcile", "queue", "merge", "review",
		"sandbox", "transcript", "heartbeat", "comment", "schedule", "pull request", "branch", "template"}
	for b.Len() < 1500+rng.IntN(2000) {
		b.WriteString(words[rng.IntN(len(words))])
		b.WriteByte(' ')
	}
	return b.String()
}

func scaleTask(i int, id string, repo model.RepoRef, created time.Time, rng *rand.Rand) model.Task {
	approval := model.Attribution{Actor: scaleActor}
	return model.Task{
		ID: id, Title: fmt.Sprintf("Scale task %d: %s", i, []string{"fix", "add", "refactor", "document"}[i%4]),
		Body: scaleBody(i, rng), Intent: model.IntentImplement, Binding: model.BindingDirective,
		Target: &repo, CreatedAt: &created,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: scaleActor}, Reason: model.ReasonDirect},
		Approval: &approval,
		CustomFields: map[string][]string{
			"area":  {[]string{"orchestrator", "store", "ui", "ci", "merge"}[i%5]},
			"sizes": {[]string{"s", "m", "l"}[i%3]},
		},
	}
}

// scaleSeed is what seeding left behind for the run to use.
type scaleSeed struct {
	historyIDs []string
	searchTerm string
	nextID     atomic.Int64
	repos      []model.RepoRef
}

func (sd *scaleSeed) newID() string { return fmt.Sprintf("s%06d", sd.nextID.Add(1)) }

func seedScale(t *testing.T, ctx context.Context, store *model.Store, cfg scaleConfig, stats *scaleStats) *scaleSeed {
	t.Helper()
	sd := &scaleSeed{searchTerm: "needle-7"}
	for i := 0; i < cfg.repos; i++ {
		sd.repos = append(sd.repos, model.RepoRef{Owner: "acme", Name: fmt.Sprintf("svc-%02d", i)})
	}
	for _, f := range []string{"area", "sizes"} {
		if err := store.PutCustomField(ctx, model.CustomField{Name: f}); err != nil {
			t.Logf("declaring custom field %s: %v (seeding it undeclared)", f, err)
		}
	}

	// History: everything that is not waiting, and not the extra queue the
	// first dispatch drains into the running slots.
	history := cfg.total - cfg.waiting - cfg.running
	if history < 0 {
		history = 0
	}
	base := time.Now().UTC().Add(-90 * 24 * time.Hour)
	ids := make([]string, history)
	for i := range ids {
		ids[i] = sd.newID()
	}
	sd.historyIDs = ids

	start := time.Now()
	var wg sync.WaitGroup
	var seeded atomic.Int64
	work := make(chan int, 256)
	for w := 0; w < cfg.seedWorkers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w), 7))
			for i := range work {
				seedHistoryTask(ctx, store, sd, i, base.Add(time.Duration(i)*90*24*time.Hour/time.Duration(max(history, 1))), rng, stats)
				if n := seeded.Add(1); n%10000 == 0 {
					t.Logf("seeded %d/%d history tasks (%v)", n, history, time.Since(start).Round(time.Second))
				}
			}
		}(w)
	}
	for i := range ids {
		work <- i
	}
	close(work)
	wg.Wait()
	stats.lat("seed/history-total").Record(time.Since(start))
	t.Logf("seeded %d history tasks in %v (%.0f tasks/s)", history, time.Since(start).Round(time.Millisecond),
		float64(history)/time.Since(start).Seconds())

	// Waiting: the 1,000, in every shape a task waits in, plus the queue
	// the first dispatch moves into the running slots.
	start = time.Now()
	rng := rand.New(rand.NewPCG(11, 13))
	now := time.Now().UTC()
	var blockers []string
	for i := 0; i < cfg.waiting+cfg.running; i++ {
		kind := "queued"
		if i < cfg.waiting {
			switch p := i * 100 / max(cfg.waiting, 1); {
			case p < 40:
				kind = "queued"
			case p < 65:
				kind = "proposed"
			case p < 80:
				kind = "awaiting_reply"
			case p < 95:
				kind = "blocked"
			default:
				kind = "parked"
			}
		}
		id := sd.newID()
		task := scaleTask(i, id, sd.repos[i%len(sd.repos)], now.Add(-time.Duration(rng.IntN(3600))*time.Second), rng)
		switch kind {
		case "proposed":
			task.Approval = nil
			blockers = append(blockers, id)
		case "blocked":
			if len(blockers) > 0 {
				task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: blockers[rng.IntN(len(blockers))]})
			}
		}
		stats.timed("seed/waiting", func() error {
			if err := store.PutTask(ctx, task); err != nil {
				return err
			}
			switch kind {
			case "awaiting_reply":
				cid, err := store.AddComment(ctx, model.Comment{TaskID: id, Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "agent"}},
					Body: "Which of the two approaches should I take?", CreatedAt: now})
				if err != nil {
					return err
				}
				return store.Observe(ctx, model.Observation{TaskID: id, PendingQuestionCommentID: &cid, BaselineCommentID: &cid})
			case "parked":
				runID := id + "-1"
				if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: id, Sandbox: runID, Attempt: 1, StartedAt: now.Add(-time.Minute)}, model.Limits{}); err != nil {
					return err
				}
				resume := now.Add(6 * time.Hour)
				return store.Suspend(ctx, model.Suspension{RunID: runID, TaskID: id, Kind: model.SuspendRequested,
					Detail: "waiting on CI for my-branch", ResumeAt: &resume, CreatedAt: now})
			}
			return nil
		})
	}
	t.Logf("seeded %d waiting + %d to-be-running tasks in %v", cfg.waiting, cfg.running, time.Since(start).Round(time.Millisecond))
	return sd
}

// seedHistoryTask writes one finished task the way a real one ends up:
// filed, dispatched at least once, talked to, and closed -- merged
// through a pull request, closed on a comment, or failed out.
func seedHistoryTask(ctx context.Context, store *model.Store, sd *scaleSeed, i int, created time.Time, rng *rand.Rand, stats *scaleStats) {
	id := sd.historyIDs[i]
	task := scaleTask(i, id, sd.repos[i%len(sd.repos)], created, rng)
	if i%997 == 0 {
		task.Title += " " + sd.searchTerm
	}
	if i > 0 && i%4 == 0 {
		task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: sd.historyIDs[i-1-rng.IntN(min(i, 50))]})
	}
	merged := i%3 == 0
	if merged {
		task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: fmt.Sprintf("https://github.com/%s/pull/%d", task.Target, i)})
		task.AutoMerge = true
	}
	failed := i%20 == 0
	runs := 1
	if failed {
		runs = model.MaxConsecutiveFailures
	} else if i%5 == 0 {
		runs = 2
	}
	stats.timed("seed/history", func() error {
		if err := store.PutTask(ctx, task); err != nil {
			return err
		}
		at := created.Add(time.Minute)
		for a := 1; a <= runs; a++ {
			runID := fmt.Sprintf("%s-%d", id, a)
			if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: id, Sandbox: runID, Attempt: a, StartedAt: at}, model.Limits{}); err != nil {
				return err
			}
			at = at.Add(time.Duration(5+rng.IntN(30)) * time.Minute)
			outcome := "succeeded"
			if failed || a < runs {
				outcome = "failed"
			}
			if err := store.FinishRun(ctx, runID, at, outcome, "seeded run "+outcome); err != nil {
				return err
			}
		}
		for c := 0; c < 2; c++ {
			if _, err := store.AddComment(ctx, model.Comment{TaskID: id,
				Author: model.Attribution{Actor: scaleActor}, Body: fmt.Sprintf("Progress note %d on %s.", c, id), CreatedAt: at}); err != nil {
				return err
			}
		}
		if failed {
			return nil
		}
		return store.ObserveClose(ctx, id, at, scaleActor, "seeded close", func(o *model.Observation) {
			o.CompletedAt = &at
			o.ClosedAt = &at
			// Settled, as a merged pull request's task is once grain has
			// seen the merge: without it every one of these is an
			// unsettled close the PR reconcilers re-read every tick.
			if merged {
				o.PrMergedAt = &at
			}
		})
	})
}

// scaleRun is one fake run: it lives for a while writing what a live
// sandbox writes, then finishes, closes its task and files the task that
// replaces it in the waiting queue.
func scaleRun(runCtx context.Context, store *model.Store, cfg scaleConfig, sd *scaleSeed, d dispatch.Dispatch, rng *rand.Rand, stats *scaleStats, done func()) {
	defer done()
	// The run's own writes outlive the run phase: a run cut short by the
	// end of it still finishes and closes, so nothing is left live, and
	// a write is never failed for a cancellation that is the harness's.
	ctx := context.WithoutCancel(runCtx)
	lifetime := cfg.runLifetime/2 + time.Duration(rng.Int64N(int64(cfg.runLifetime)))
	end := time.Now().Add(lifetime)
	stats.timed("run/agent-started", func() error { return store.SetRunAgentStarted(ctx, d.RunID, time.Now().UTC()) })
	var transcript strings.Builder
	for step := 0; time.Now().Before(end); step++ {
		select {
		case <-runCtx.Done():
			end = time.Now()
			continue
		case <-time.After(cfg.heartbeat):
		}
		now := time.Now().UTC()
		stats.timed("run/heartbeat", func() error {
			_, err := store.SetTaskActivity(ctx, d.TaskID, fmt.Sprintf("step %d: running tests", step), now)
			return err
		})
		fmt.Fprintf(&transcript, "{\"step\":%d,\"tool\":\"run_command\",\"output\":\"%s\"}\n", step, strings.Repeat("x", 200))
		if transcript.Len() > 64<<10 {
			transcript.Reset()
		}
		stats.timed("run/transcript", func() error { return store.SetRunTranscript(ctx, d.RunID, transcript.String()) })
		if step%4 == 3 {
			stats.timed("run/comment", func() error {
				_, err := store.AddComment(ctx, model.Comment{TaskID: d.TaskID,
					Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "agent"}},
					Body:   fmt.Sprintf("status update %d", step), CreatedAt: now})
				return err
			})
		}
		if step%2 == 1 {
			stats.timed("run/proxy-read", func() error {
				if _, err := store.LiveRunCounts(ctx); err != nil {
					return err
				}
				_, err := store.GetTask(ctx, d.TaskID)
				return err
			})
		}
	}
	now := time.Now().UTC()
	if !stats.timed("run/finish", func() error {
		if err := store.FinishRun(ctx, d.RunID, now, "succeeded", "scale run finished"); err != nil {
			return err
		}
		return store.ObserveClose(ctx, d.TaskID, now, scaleActor, "scale run closed", func(o *model.Observation) {
			o.CompletedAt = &now
			o.ClosedAt = &now
		})
	}) {
		return
	}
	stats.count("runs/completed").Add(1)
	if runCtx.Err() != nil {
		return
	}
	// The replacement arrival keeps the waiting queue at cfg.waiting.
	id := sd.newID()
	stats.timed("run/file-replacement", func() error {
		return store.PutTask(ctx, scaleTask(int(sd.nextID.Load()), id, sd.repos[rng.IntN(len(sd.repos))], now, rng))
	})
}

func scaleAPIWorker(ctx context.Context, base, endpoint string, timeout, budget time.Duration, allowUnanswered bool, sd *scaleSeed, rng *rand.Rand, stats *scaleStats) {
	client := &http.Client{Timeout: timeout}
	cursor := ""
	// A request the end of the run cuts off is no sample, unless it falls
	// outside scaleUnansweredListAllowance and had already been waiting
	// longer than its budget: then it is the slowest sample of all, and
	// dropping it would let an endpoint that stalls mid-run pass.
	// One under its budget (or covered by the list allowance) is recorded
	// under api-unanswered/, with how long it had waited, and judgeScale
	// fails any unallowed endpoint that finished with no api/ samples.
	cutOffBudget := budget
	if allowUnanswered {
		cutOffBudget = 0
	}
	cutOff := func(name string, start time.Time) {
		waited := time.Since(start)
		if cutOffBudget > 0 && waited > cutOffBudget {
			stats.fail("api/"+name, fmt.Errorf("still unanswered after %v when the run ended (budget %v)", waited.Round(time.Second), cutOffBudget))
			return
		}
		stats.lat("api-unanswered/" + name).Record(waited)
	}
	get := func(reqCtx context.Context, name, path string) []byte {
		start := time.Now()
		req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, base+path, nil)
		resp, err := client.Do(presentAccessCode(req))
		if err != nil {
			if reqCtx.Err() == nil {
				stats.fail("api/"+name, err)
			} else {
				cutOff(name, start)
			}
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if reqCtx.Err() != nil {
			cutOff(name, start)
			return nil
		}
		stats.lat("api/" + name).Record(time.Since(start))
		stats.addBytes("api/"+name, int64(len(body)))
		if err != nil {
			stats.fail("api/"+name, err)
		} else if resp.StatusCode/100 != 2 {
			stats.fail("api/"+name, fmt.Errorf("%s: HTTP %d: %.200s", path, resp.StatusCode, body))
		}
		return body
	}
	// When no latency budget is set (TestScaleSmoke's 3s run under -race),
	// let each required endpoint finish at least one request up to
	// requestTimeout before honouring runCtx cancellation, just as the tick
	// loop runs at least one tick.
	for first := true; (first && budget == 0 && !allowUnanswered) || ctx.Err() == nil; first = false {
		reqCtx := ctx
		if first && budget == 0 && !allowUnanswered {
			reqCtx = context.WithoutCancel(ctx)
		}
		id := sd.historyIDs[rng.IntN(len(sd.historyIDs))]
		switch endpoint {
		case "tasks-default":
			get(reqCtx, endpoint, "/api/tasks")
		case "tasks-state-queued":
			get(reqCtx, endpoint, "/api/tasks?state=queued")
		case "tasks-state-running":
			get(reqCtx, endpoint, "/api/tasks?state=running,awaiting_reply")
		case "tasks-search":
			get(reqCtx, endpoint, "/api/tasks?q="+url.QueryEscape(sd.searchTerm))
		case "tasks-cursor-page":
			var page struct {
				NextCursor string `json:"nextCursor"`
			}
			body := get(reqCtx, endpoint, "/api/tasks?limit=100&cursor="+url.QueryEscape(cursor))
			if json.Unmarshal(body, &page) == nil {
				cursor = page.NextCursor
			}
		case "tasks-summary":
			get(reqCtx, endpoint, "/api/tasks?fields=summary&limit=50")
		case "task-detail":
			get(reqCtx, endpoint, "/api/tasks/"+id)
		case "task-conversation":
			get(reqCtx, endpoint, "/api/tasks/"+id+"/messages")
		case "metrics":
			get(reqCtx, endpoint, "/api/metrics")
		case "states":
			get(reqCtx, endpoint, "/api/tasks/states")
		case "inbox":
			get(reqCtx, endpoint, "/api/inbox")
		}
	}
}

// scaleEndpoints is every endpoint the API clients drive. Each gets
// cfg.apiWorkers clients of its own rather than a share of a common
// pool, so an endpoint that takes minutes to answer cannot starve the
// others of samples -- it is only its own clients that wait on it.
var scaleEndpoints = []string{
	"tasks-default", "tasks-state-queued", "tasks-state-running", "tasks-search", "tasks-cursor-page",
	"tasks-summary", "task-detail", "task-conversation", "metrics", "states", "inbox",
}

// scaleUnansweredListAllowance names the whole-store list endpoints that
// rebuild all 50,000 tasks per request without SQL paging or filtering and
// whose cache is invalidated on every live-run write
// (docs/test-runs/2026-11-15-scale-50k.md, limit #3 and follow-up #3 from
// task 5994: "GET /api/tasks / /api/inbox: page and filter in SQL, and stop
// live-run activity from invalidating the list cache"). With one client per
// endpoint driving all seven at once, none answered in the 50k runs. While an
// endpoint remains here (and cfg.allowUnansweredLists is true), judgeScale
// allows it to finish with only api-unanswered/ samples and checks any
// completed response against cfg.listAPIP95, while still failing on HTTP or
// transport errors. Remove entries as follow-up #3 lands, or set
// GRAIN_SCALE_STRICT_LISTS=1 to hold every endpoint to cfg.apiP95.
var scaleUnansweredListAllowance = map[string]string{
	"tasks-default":       "follow-up #3 (task 5994): GET /api/tasks whole-store rebuild and cache invalidation on live-run writes",
	"tasks-state-queued":  "follow-up #3 (task 5994): GET /api/tasks?state= whole-store load without SQL filtering",
	"tasks-state-running": "follow-up #3 (task 5994): GET /api/tasks?state= whole-store load without SQL filtering",
	"tasks-search":        "follow-up #3 (task 5994): GET /api/tasks?q= whole-store scan in Go",
	"tasks-cursor-page":   "follow-up #3 (task 5994): GET /api/tasks?cursor= whole-store load before paging",
	"tasks-summary":       "follow-up #3 (task 5994): GET /api/tasks?fields=summary whole-store load before projection",
	"inbox":               "follow-up #3 (task 5994): GET /api/inbox whole-store task load",
}

func (cfg scaleConfig) allowsUnansweredEndpoint(endpoint string) (string, bool) {
	if !cfg.allowUnansweredLists {
		return "", false
	}
	reason, ok := scaleUnansweredListAllowance[endpoint]
	return reason, ok
}

// scaleSSEClient holds one /api/events stream open for the run and
// counts what reaches it -- the fan-out every open browser tab costs.
func scaleSSEClient(ctx context.Context, base string, stats *scaleStats) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/events?stream=1", nil)
	start := time.Now()
	resp, err := http.DefaultClient.Do(presentAccessCode(req))
	if err != nil {
		if ctx.Err() == nil {
			stats.fail("sse/connect", err)
		}
		return
	}
	defer resp.Body.Close()
	stats.lat("sse/connect").Record(time.Since(start))
	if resp.StatusCode/100 != 2 {
		stats.fail("sse/connect", fmt.Errorf("HTTP %d", resp.StatusCode))
		return
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		stats.addBytes("sse/received", int64(len(line)))
		if strings.HasPrefix(string(line), "data:") {
			stats.count("sse/events").Add(1)
		}
	}
}

type runtimeSample struct {
	maxRSSKB, maxHeap, maxGoroutines, maxFDs int64
	gcPauseTotal                             time.Duration
	totalAlloc                               uint64
}

func sampleRuntime(ctx context.Context, every time.Duration, out *runtimeSample, mu *sync.Mutex) {
	var startAlloc uint64
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	startAlloc, startPause := ms.TotalAlloc, ms.PauseTotalNs
	for {
		runtime.ReadMemStats(&ms)
		rss := procStatusKB("VmRSS:")
		fds := int64(0)
		if ents, err := os.ReadDir("/proc/self/fd"); err == nil {
			fds = int64(len(ents))
		}
		mu.Lock()
		out.maxRSSKB = max(out.maxRSSKB, rss)
		out.maxHeap = max(out.maxHeap, int64(ms.HeapAlloc))
		out.maxGoroutines = max(out.maxGoroutines, int64(runtime.NumGoroutine()))
		out.maxFDs = max(out.maxFDs, fds)
		out.totalAlloc = ms.TotalAlloc - startAlloc
		out.gcPauseTotal = time.Duration(ms.PauseTotalNs - startPause)
		mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func procStatusKB(field string) int64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, field); ok {
			n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return n
		}
	}
	return 0
}

// countStates reads every task's state the way the list endpoint and
// the tick do -- through the task_state view -- and returns the census.
func countStates(ctx context.Context, store *model.Store) (map[model.State]int, error) {
	states, err := store.States(ctx)
	if err != nil {
		return nil, err
	}
	out := map[model.State]int{}
	for _, s := range states {
		out[s]++
	}
	return out, nil
}

func runScale(t *testing.T, backend string, cfg scaleConfig) {
	t.Logf("scale config (%s): %+v", backend, cfg)
	store, db := openScaleStore(t, backend)
	if store == nil {
		return
	}
	stats := newScaleStats()
	ctx := context.Background()

	var rtMu sync.Mutex
	var rt runtimeSample
	samplerCtx, stopSampler := context.WithCancel(ctx)
	defer stopSampler()
	go sampleRuntime(samplerCtx, 500*time.Millisecond, &rt, &rtMu)

	sd := seedScale(t, ctx, store, cfg, stats)
	if n := stats.errorsTotal(); n != 0 {
		t.Fatalf("seeding: %d store error(s): %v", n, stats.errSample)
	}
	census, err := countStates(ctx, store)
	if err != nil {
		t.Fatalf("States: %v", err)
	}
	t.Logf("seeded census: %v", census)
	total := 0
	for _, n := range census {
		total += n
	}
	if total < cfg.total {
		t.Fatalf("seeded %d tasks, want %d", total, cfg.total)
	}

	// Plan cost of the queries every tick and every list request runs,
	// on the store as seeded. A SCAN of `task` or `task_run` here is a
	// full-table scan at 50,000 rows.
	explainHotQueries(t, ctx, backend, db, store, stats)

	// The run phase's clock starts once every slot is filled (below), not
	// here: under -race on a loaded CI machine the setup alone can take
	// longer than a smoke run's whole duration.
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	var runs sync.WaitGroup
	var live atomic.Int64
	var maxLive atomic.Int64
	dispatcherRNG := rand.New(rand.NewPCG(21, 22))

	// The HTTP surface, served out of the same store.
	server := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor: ui.DefaultActor("operator"), Capabilities: ui.OfferedCapabilities(),
	}), store))

	var bg sync.WaitGroup
	for i := 0; i < cfg.sseClients; i++ {
		bg.Add(1)
		go func() {
			defer bg.Done()
			scaleSSEClient(runCtx, server, stats)
		}()
	}

	// The orchestrator tick, with every slot already taken by the
	// dispatcher below: MaxWorkers 1 against cfg.running live runs leaves
	// no free slot, so RunCycle reconciles, scans and decides without ever
	// starting a real sandbox of its own.
	cycles := orchestrator.NewCycleTimes(loadCycleHistory)
	metrics := newLoadMetrics()
	var genMu sync.Mutex
	genRNG := rand.New(rand.NewPCG(5, 6))
	deps := orchestrator.Deps{
		Store: store, Client: newLoadGitHub(), Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return antigravity.NewForTest(newLoadGenerator(&genMu, genRNG, metrics)), nil
		},
		Config:     orchestrator.Config{Capabilities: loadHarnessRegistry(newLoadCapability(0, 2*time.Second))},
		MaxWorkers: 1,
		CycleTimes: cycles,
	}

	// Fill the slots before the first tick and before the API workers
	// start, so the tick never sees a slot free and API latencies are
	// measured over the run phase rather than setup.
	dispatchOnce := func() {
		var ds []dispatch.Dispatch
		stats.timed("dispatch/cycle", func() error {
			var err error
			ds, err = dispatch.Cycle(ctx, store, model.Limits{Workers: cfg.running}, time.Now().UTC())
			return err
		})
		for _, d := range ds {
			n := live.Add(1)
			for m := maxLive.Load(); n > m && !maxLive.CompareAndSwap(m, n); m = maxLive.Load() {
			}
			stats.count("dispatch/started").Add(1)
			runs.Add(1)
			go scaleRun(runCtx, store, cfg, sd, d, rand.New(rand.NewPCG(dispatcherRNG.Uint64(), 1)), stats,
				func() { live.Add(-1); runs.Done() })
		}
	}
	dispatchOnce()
	time.AfterFunc(cfg.duration, stopRun)
	if got := live.Load(); got < int64(cfg.running) {
		t.Errorf("first dispatch filled %d of %d slots from a queue of %d", got, cfg.running, census[model.StateQueued])
	}
	for i := 0; i < cfg.apiWorkers; i++ {
		for j, endpoint := range scaleEndpoints {
			_, allowUnanswered := cfg.allowsUnansweredEndpoint(endpoint)
			bg.Add(1)
			go func(seed uint64, endpoint string, allowUnanswered bool) {
				defer bg.Done()
				scaleAPIWorker(runCtx, server, endpoint, cfg.requestTimeout, cfg.apiP95, allowUnanswered, sd, rand.New(rand.NewPCG(seed, 3)), stats)
			}(uint64(100+i*len(scaleEndpoints)+j), endpoint, allowUnanswered)
		}
	}

	bg.Add(1)
	go func() {
		defer bg.Done()
		for runCtx.Err() == nil {
			dispatchOnce()
			select {
			case <-runCtx.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()

	bg.Add(1)
	go func() {
		defer bg.Done()
		// At least one tick, however short the run: a run with no tick
		// measured nothing about the orchestrator.
		for first := true; first || runCtx.Err() == nil; first = false {
			tickCtx, cancel := context.WithTimeout(ctx, cfg.tickTimeout)
			start := time.Now()
			err := orchestrator.RunCycle(tickCtx, deps, start.UTC())
			cancel()
			stats.lat("orchestrator/tick").Record(time.Since(start))
			if err != nil && !strings.Contains(err.Error(), "task closed while its run was still live") {
				stats.fail("orchestrator/tick", err)
			}
		}
	}()

	<-runCtx.Done()
	bg.Wait()
	runs.Wait()
	stopSampler()

	if db != nil {
		dbs := db.Stats()
		t.Logf("DB pool (%s): open=%d max-open=%d wait-count=%d wait-total=%v max-idle-closed=%d",
			backend, dbs.OpenConnections, dbs.MaxOpenConnections, dbs.WaitCount, dbs.WaitDuration, dbs.MaxIdleClosed)
	}
	reportScale(t, backend, cfg, stats, cycles, &rt, &rtMu, maxLive.Load())
	judgeScale(t, cfg, stats)
}

func (s *scaleStats) errorsTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.errors {
		n += c
	}
	return n
}

// explainHotQueries logs the plan of the queries the tick and the list
// endpoints lean on hardest. SQLite only: Postgres's planner answers
// differently at every table size, so its plan says less than its
// timing does, which the run reports anyway.
func explainHotQueries(t *testing.T, ctx context.Context, backend string, db *sql.DB, store *model.Store, stats *scaleStats) {
	if db == nil {
		return
	}
	ready, err := store.Ready(ctx)
	if err != nil || len(ready) == 0 {
		t.Logf("no ready task to probe IsReady with (%v)", err)
		return
	}
	readyProbe := ready[len(ready)-1]
	// Timed on both backends, through the store, as the dispatcher pays
	// for it: the mean of a few calls.
	for i := 0; i < 5; i++ {
		stats.timed("query/IsReady", func() error { _, err := store.IsReady(ctx, readyProbe); return err })
		stats.timed("query/Ready", func() error { _, err := store.Ready(ctx); return err })
		stats.timed("query/States", func() error { _, err := store.States(ctx); return err })
		stats.timed("query/ListTasks", func() error { _, err := store.ListTasks(ctx); return err })
	}
	if backend != "sqlite" {
		return
	}
	for name, q := range map[string]string{
		"ready":       "SELECT `task_id` FROM `task_ready`",
		"task_state":  "SELECT `task_id`, `state` FROM `task_state`",
		"live_runs":   "SELECT `id` FROM `task_run` WHERE `finished_at` IS NULL",
		"task_by_id":  "SELECT `id` FROM `task` WHERE `id` = 's000001'",
		"comments_of": "SELECT `id` FROM `task_comment` WHERE `task_id` = 's000001'",
		// What dispatch.Cycle's stillReady asks once per candidate per
		// cycle: one task, through the whole view.
		"is_ready_one": "SELECT `task_id` FROM `task_ready` WHERE `task_id` = '" + readyProbe + "'",
	} {
		start := time.Now()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+q+")").Scan(&n); err != nil {
			t.Logf("query %s: %v", name, err)
			continue
		}
		stats.lat("query/" + name).Record(time.Since(start))
		rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q)
		if err != nil {
			continue
		}
		var scans []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if rows.Scan(&id, &parent, &notused, &detail) == nil && strings.HasPrefix(detail, "SCAN") {
				scans = append(scans, detail)
			}
		}
		rows.Close()
		t.Logf("query %-12s rows=%-6d took=%-12v full scans: %v", name, n, time.Since(start).Round(time.Microsecond), scans)
	}
}

func reportScale(t *testing.T, backend string, cfg scaleConfig, stats *scaleStats, cycles *orchestrator.CycleTimes,
	rt *runtimeSample, rtMu *sync.Mutex, maxLive int64) {
	var b strings.Builder
	fmt.Fprintf(&b, "=== scale report (%s): total=%d waiting=%d running=%d duration=%v ===\n",
		backend, cfg.total, cfg.waiting, cfg.running, cfg.duration)
	stats.mu.Lock()
	names := make([]string, 0, len(stats.latencies))
	for n := range stats.latencies {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "  %-28s %s", n, stats.latencies[n].Stats())
		if by := stats.bytes[n]; by > 0 {
			fmt.Fprintf(&b, " bytes=%d avg-bytes=%d", by, by/int64(max(stats.latencies[n].Stats().Count, 1)))
		}
		if e := stats.errors[n]; e > 0 {
			fmt.Fprintf(&b, " ERRORS=%d (%s)", e, stats.errSample[n])
		}
		b.WriteByte('\n')
	}
	for n, e := range stats.errors {
		if _, ok := stats.latencies[n]; !ok {
			fmt.Fprintf(&b, "  %-28s ERRORS=%d (%s)\n", n, e, stats.errSample[n])
		}
	}
	cnames := make([]string, 0, len(stats.counters))
	for n := range stats.counters {
		cnames = append(cnames, n)
	}
	sort.Strings(cnames)
	for _, n := range cnames {
		fmt.Fprintf(&b, "  %-28s %d\n", n, stats.counters[n].Load())
	}
	fmt.Fprintf(&b, "  %-28s %d\n", "sse/received-bytes", stats.bytes["sse/received"])
	stats.mu.Unlock()
	fmt.Fprintf(&b, "  max live runs                %d\n", maxLive)

	// Where each tick went, reconciler by reconciler: the slowest by p95.
	history, _ := cycles.History()
	byName := map[string]*LatencyTracker{}
	dispatchWait := &LatencyTracker{}
	for _, c := range history {
		dispatchWait.Record(c.DispatchWait)
		for _, r := range c.Reconcilers {
			if byName[r.Name] == nil {
				byName[r.Name] = &LatencyTracker{}
			}
			byName[r.Name].Record(r.Duration)
		}
	}
	fmt.Fprintf(&b, "  %-28s %s\n", "tick/dispatch-wait", dispatchWait.Stats())
	type rec struct {
		name string
		st   LatencyStats
	}
	var recs []rec
	for n, l := range byName {
		recs = append(recs, rec{n, l.Stats()})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].st.P95 > recs[j].st.P95 })
	for i, r := range recs {
		if i == 12 {
			break
		}
		fmt.Fprintf(&b, "  reconciler %-17s %s\n", r.name, r.st)
	}
	rtMu.Lock()
	fmt.Fprintf(&b, "  runtime: max-rss=%dMB max-heap=%dMB max-goroutines=%d max-fds=%d alloc-during-run=%dMB gc-pause-total=%v\n",
		rt.maxRSSKB/1024, rt.maxHeap>>20, rt.maxGoroutines, rt.maxFDs, rt.totalAlloc>>20, rt.gcPauseTotal)
	rtMu.Unlock()
	t.Log(b.String())
	if dir := os.Getenv("GRAIN_SCALE_REPORT_DIR"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(fmt.Sprintf("%s/scale-%s.txt", dir, backend), []byte(b.String()), 0o644)
	}
}

type scaleReporter interface {
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
}

func (s *scaleStats) latStats(name string) LatencyStats {
	s.mu.Lock()
	l := s.latencies[name]
	s.mu.Unlock()
	if l == nil {
		return LatencyStats{}
	}
	return l.Stats()
}

func judgeScale(t scaleReporter, cfg scaleConfig, stats *scaleStats) {
	stats.mu.Lock()
	errNames := make([]string, 0, len(stats.errors))
	for n := range stats.errors {
		errNames = append(errNames, n)
	}
	sort.Strings(errNames)
	for _, n := range errNames {
		t.Errorf("%s: %d error(s), first: %s", n, stats.errors[n], stats.errSample[n])
	}
	stats.mu.Unlock()
	budget := func(name string, p95 time.Duration) {
		if p95 == 0 {
			return
		}
		if got := stats.latStats(name); got.Count > 0 && got.P95 > p95 {
			t.Errorf("%s: p95 %v over its %v budget (%s)", name, got.P95, p95, got)
		}
	}
	budget("orchestrator/tick", cfg.tickP95)
	budget("dispatch/cycle", cfg.dispatchP95)
	for _, n := range []string{"run/heartbeat", "run/transcript", "run/comment", "run/finish"} {
		budget(n, cfg.writeP95)
	}
	if cfg.apiWorkers > 0 {
		for _, ep := range scaleEndpoints {
			name := "api/" + ep
			got := stats.latStats(name)
			unans := stats.latStats("api-unanswered/" + ep)
			reason, allowed := cfg.allowsUnansweredEndpoint(ep)
			if got.Count == 0 {
				if allowed {
					t.Logf("%s: 0 completed responses in %v (unanswered=%d max-wait=%v); allowed (%s)",
						name, cfg.duration, unans.Count, unans.Max, reason)
				} else if unans.Count > 0 {
					t.Errorf("%s: no response completed within %v (%d in-flight request(s) under api-unanswered/%s, max wait %v)",
						name, cfg.duration, unans.Count, ep, unans.Max)
				} else {
					t.Errorf("%s: no response completed within %v", name, cfg.duration)
				}
				continue
			}
			epBudget := cfg.apiP95
			if allowed && cfg.apiP95 > 0 && cfg.listAPIP95 > 0 {
				epBudget = cfg.listAPIP95
			}
			budget(name, epBudget)
		}
	}
	stats.mu.Lock()
	var extraAPIs []string
	for n := range stats.latencies {
		if ep, ok := strings.CutPrefix(n, "api/"); ok && !slices.Contains(scaleEndpoints, ep) {
			extraAPIs = append(extraAPIs, n)
		}
	}
	stats.mu.Unlock()
	sort.Strings(extraAPIs)
	for _, n := range extraAPIs {
		budget(n, cfg.apiP95)
	}
	if got := stats.latStats("orchestrator/tick").Count; got == 0 {
		t.Errorf("orchestrator: no tick finished within %v", cfg.duration)
	}
	if got := stats.count("runs/completed").Load(); got < int64(cfg.minCompletions) {
		t.Errorf("continuous dispatch: %d run(s) completed and were replaced, want at least %d", got, cfg.minCompletions)
	}
}

type recordedScaleReport struct {
	errors []string
	logs   []string
}

func (r *recordedScaleReport) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recordedScaleReport) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func TestScaleJudgeCatchesUnansweredEndpoints(t *testing.T) {
	t.Parallel()
	def := defaultScaleConfig()
	if def.apiP95 <= 0 || def.duration <= def.apiP95 {
		t.Fatalf("defaultScaleConfig duration (%v) must exceed apiP95 (%v) so cutOff is reachable", def.duration, def.apiP95)
	}
	for ep := range scaleUnansweredListAllowance {
		if !slices.Contains(scaleEndpoints, ep) {
			t.Errorf("scaleUnansweredListAllowance names unknown endpoint %q", ep)
		}
	}

	healthyStats := func() *scaleStats {
		s := newScaleStats()
		s.lat("orchestrator/tick").Record(10 * time.Millisecond)
		s.count("runs/completed").Store(int64(def.minCompletions))
		for _, ep := range scaleEndpoints {
			if _, allowed := def.allowsUnansweredEndpoint(ep); allowed {
				s.lat("api-unanswered/" + ep).Record(3 * time.Minute)
				continue
			}
			s.lat("api/" + ep).Record(50 * time.Millisecond)
		}
		return s
	}

	// Default 50k config: allowed whole-store list endpoints with only
	// api-unanswered/ samples pass (and are logged), while every other
	// endpoint has answered.
	rep := &recordedScaleReport{}
	judgeScale(rep, def, healthyStats())
	if len(rep.errors) != 0 {
		t.Fatalf("healthy stats with list allowance failed: %v", rep.errors)
	}
	if len(rep.logs) != len(scaleUnansweredListAllowance) {
		t.Fatalf("logged %d unanswered-list allowances, want %d: %v", len(rep.logs), len(scaleUnansweredListAllowance), rep.logs)
	}

	// A non-allowed endpoint with only api-unanswered/ (or no sample at all)
	// fails judgeScale even when cutOff did not fire.
	missingMetrics := healthyStats()
	delete(missingMetrics.latencies, "api/metrics")
	missingMetrics.lat("api-unanswered/metrics").Record(90 * time.Second)
	rep = &recordedScaleReport{}
	judgeScale(rep, def, missingMetrics)
	if len(rep.errors) != 1 || !strings.Contains(rep.errors[0], "api/metrics: no response completed") || !strings.Contains(rep.errors[0], "api-unanswered/metrics") {
		t.Fatalf("expected api/metrics unanswered failure, got: %v", rep.errors)
	}

	// Disabling the list allowance (GRAIN_SCALE_STRICT_LISTS=1, or Smoke)
	// fails every unanswered list endpoint.
	strict := def
	strict.allowUnansweredLists = false
	rep = &recordedScaleReport{}
	judgeScale(rep, strict, healthyStats())
	if len(rep.errors) != len(scaleUnansweredListAllowance) {
		t.Fatalf("strict mode reported %d error(s), want %d: %v", len(rep.errors), len(scaleUnansweredListAllowance), rep.errors)
	}

	// cutOff path: an endpoint that answers once and then stalls past
	// apiP95 before runCtx cancels fails via stats.fail.
	var calls atomic.Int64
	stallRelease := make(chan struct{})
	defer close(stallRelease)
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
			return
		}
		select {
		case <-r.Context().Done():
		case <-stallRelease:
		}
	}))
	stalledStats := newScaleStats()
	stalledStats.lat("orchestrator/tick").Record(10 * time.Millisecond)
	stalledStats.count("runs/completed").Store(1)
	for _, ep := range scaleEndpoints {
		if ep != "metrics" {
			stalledStats.lat("api/" + ep).Record(5 * time.Millisecond)
		}
	}
	runCtx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	scaleAPIWorker(runCtx, srv, "metrics", time.Second, 40*time.Millisecond, false,
		&scaleSeed{historyIDs: []string{"s000001"}}, rand.New(rand.NewPCG(1, 2)), stalledStats)
	cutOffCfg := scaleConfig{
		duration: 120 * time.Millisecond, apiWorkers: 1, apiP95: 40 * time.Millisecond, minCompletions: 1,
	}
	rep = &recordedScaleReport{}
	judgeScale(rep, cutOffCfg, stalledStats)
	if len(rep.errors) != 1 || !strings.Contains(rep.errors[0], "api/metrics") || !strings.Contains(rep.errors[0], "still unanswered") {
		t.Fatalf("expected cutOff failure on stalled 2nd metrics request, got: %v", rep.errors)
	}
}
