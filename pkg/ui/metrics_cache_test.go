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

package ui_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func metricsWith(t *testing.T, srv *ui.Server, path, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestMetricsCacheReusesWithinTTLAndIgnoresLiveRunActivity verifies that
// GET /api/metrics caches reports per (window, section, scope) for its short
// TTL, serves 304 on matching ETag, does not invalidate on live-run activity
// (heartbeats, transcripts, comments), and rebuilds once the TTL elapses.
func TestMetricsCacheReusesWithinTTLAndIgnoresLiveRunActivity(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()

	now := baseTime.Add(2 * time.Hour)
	var nowMu sync.Mutex
	c.Now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		nowMu.Lock()
		now = now.Add(d)
		nowMu.Unlock()
	}

	task := create(t, c, ctx)
	if err := c.Store.StartRun(ctx, model.Run{
		ID: "r-live", TaskID: task.ID, Sandbox: "s1", Attempt: 1,
		StartedAt: baseTime.Add(time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	first := metricsWith(t, srv, "/api/metrics", "")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d: %s", first.Code, first.Body)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("GET /api/metrics returned no ETag header")
	}
	if got := ui.MetricsBuilds(srv); got != 1 {
		t.Fatalf("builds after first GET = %d, want 1", got)
	}

	// Simulate live-run writes (heartbeat, transcript, comment) inside the TTL.
	if _, err := c.Store.SetTaskActivity(ctx, task.ID, "running tests", now); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.SetRunTranscript(ctx, "r-live", `{"step":1}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "agent"}},
		Body: "progress update", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	advance(2 * time.Second)
	hit := metricsWith(t, srv, "/api/metrics", "")
	if hit.Code != http.StatusOK || hit.Body.String() != first.Body.String() {
		t.Fatalf("cache hit status=%d body mismatch", hit.Code)
	}
	if got := ui.MetricsBuilds(srv); got != 1 {
		t.Fatalf("builds after live-run activity within TTL = %d, want 1", got)
	}

	notMod := metricsWith(t, srv, "/api/metrics", etag)
	if notMod.Code != http.StatusNotModified || notMod.Body.Len() != 0 {
		t.Fatalf("conditional GET status=%d len=%d, want 304 empty", notMod.Code, notMod.Body.Len())
	}
	if got := ui.MetricsBuilds(srv); got != 1 {
		t.Fatalf("builds after 304 = %d, want 1", got)
	}

	// Different window, section, or scope uses a distinct cache key.
	for _, path := range []string{
		"/api/metrics?window=24h",
		"/api/metrics?section=throughput",
		"/api/metrics?repo=acme/widgets",
		"/api/metrics?author=alice",
	} {
		rec := metricsWith(t, srv, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d: %s", path, rec.Code, rec.Body)
		}
	}
	if got := ui.MetricsBuilds(srv); got != 5 {
		t.Fatalf("builds across 5 distinct keys = %d, want 5", got)
	}

	// Advancing past TTL triggers a rebuild that observes newly filed tasks.
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "second task", Description: "after ttl", Approved: true, Repo: "acme/widgets",
	}); err != nil {
		t.Fatal(err)
	}
	advance(10 * time.Second)
	after := metricsWith(t, srv, "/api/metrics", etag)
	if after.Code != http.StatusOK {
		t.Fatalf("after TTL status=%d, want 200 with updated report", after.Code)
	}
	rep := decode[ui.MetricsReport](t, after)
	if rep.Throughput.TasksFiled != 2 {
		t.Errorf("TasksFiled after TTL rebuild = %d, want 2", rep.Throughput.TasksFiled)
	}
	if got := ui.MetricsBuilds(srv); got != 6 {
		t.Fatalf("builds after TTL expiry = %d, want 6", got)
	}
}

// TestMetricsCacheCoalescesConcurrentRequests verifies that a burst of
// concurrent callers arriving on a cold key share one build.
func TestMetricsCacheCoalescesConcurrentRequests(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	create(t, c, context.Background())
	c.Now = func() time.Time { return baseTime.Add(2 * time.Hour) }

	const callers = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := metricsWith(t, srv, "/api/metrics", "")
			if rec.Code != http.StatusOK {
				errs <- fmt.Errorf("status %d: %s", rec.Code, rec.Body)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := ui.MetricsBuilds(srv); got != 1 {
		t.Errorf("concurrent cold requests built %d times, want 1", got)
	}
}

// TestMetricsFixedEndSpanReusesUnchangedStorePastTTL verifies that a saved
// historical span (?since=&until=) skips rebuilding past TTL when the store
// has had no commits, and rebuilds past TTL once a commit lands.
func TestMetricsFixedEndSpanReusesUnchangedStorePastTTL(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()
	create(t, c, ctx)

	now := baseTime.Add(24 * time.Hour)
	c.Now = func() time.Time { return now }

	path := fmt.Sprintf("/api/metrics?since=%s&until=%s",
		baseTime.Add(-time.Hour).UTC().Format(time.RFC3339),
		baseTime.Add(2*time.Hour).UTC().Format(time.RFC3339))

	rec1 := metricsWith(t, srv, path, "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", rec1.Code, rec1.Body)
	}
	if got := ui.MetricsBuilds(srv); got != 1 {
		t.Fatalf("builds = %d, want 1", got)
	}

	// Past TTL with no store commits: ChangeToken matches -> still 1 build.
	now = now.Add(time.Minute)
	rec2 := metricsWith(t, srv, path, rec1.Header().Get("ETag"))
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("quiet fixed-end span past TTL status=%d, want 304", rec2.Code)
	}
	if got := ui.MetricsBuilds(srv); got != 1 {
		t.Fatalf("builds after quiet past-TTL poll = %d, want 1", got)
	}

	// After a commit inside the span and past TTL: ChangeToken moved -> rebuilds.
	inSpan := baseTime.Add(30 * time.Minute)
	if err := c.Store.PutTask(ctx, model.Task{
		ID: "in-span-2", Title: "backfilled task", Body: "body",
		Intent: model.IntentImplement, Binding: model.BindingDirective,
		Filing:    model.Filing{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}, Reason: model.ReasonDirect},
		CreatedAt: &inSpan, Owner: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	rec3 := metricsWith(t, srv, path, rec1.Header().Get("ETag"))
	if rec3.Code != http.StatusOK {
		t.Fatalf("post-commit fixed-end span status=%d, want 200", rec3.Code)
	}
	if got := decode[ui.MetricsReport](t, rec3).Throughput.TasksFiled; got != 2 {
		t.Errorf("TasksFiled = %d, want 2", got)
	}
	if got := ui.MetricsBuilds(srv); got != 2 {
		t.Fatalf("builds after commit + TTL = %d, want 2", got)
	}
}

// TestMetricsInMemorySectionsStayLiveWithoutCaching verifies that zero-DB
// sections (cycles, github, host) are never delayed by the store cache TTL.
func TestMetricsInMemorySectionsStayLiveWithoutCaching(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	c.Now = func() time.Time { return baseTime.Add(time.Hour) }

	for _, sec := range []string{"cycles", "github", "host"} {
		before := ui.MetricsBuilds(srv)
		for i := 0; i < 2; i++ {
			rec := metricsWith(t, srv, "/api/metrics?section="+sec, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("section=%s status=%d: %s", sec, rec.Code, rec.Body)
			}
		}
		if got := ui.MetricsBuilds(srv) - before; got != 2 {
			t.Errorf("in-memory section=%s built %d times for 2 calls, want 2", sec, got)
		}
	}
}

// TestMetricsBacklogSectionSkipsRunTable verifies that ?section=backlog reads
// only open tasks and states (not task_run or historical tasks).
func TestMetricsBacklogSectionSkipsRunTable(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()
	create(t, c, ctx)
	c.Now = func() time.Time { return baseTime.Add(time.Hour) }

	// Remove prompt/transcript columns so any scan of them would fail, and check
	// that section=backlog still succeeds and reports the queued task.
	if _, err := c.Store.DB().Exec("ALTER TABLE `task_run` RENAME COLUMN `prompt` TO `prompt_gone`"); err != nil {
		t.Fatal(err)
	}
	rec := metricsWith(t, srv, "/api/metrics?section=backlog", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("section=backlog status=%d: %s", rec.Code, rec.Body)
	}
	rep := decode[ui.MetricsReport](t, rec)
	if rep.Backlog.Queued != 1 {
		t.Errorf("Backlog.Queued = %d, want 1", rep.Backlog.Queued)
	}
	if rep.Throughput.TasksFiled != 0 {
		t.Errorf("section=backlog computed Throughput.TasksFiled = %d, want 0", rep.Throughput.TasksFiled)
	}
}

// TestMetricsIsCheapAtScaleOnBothBackends pins both correctness (windowed ==
// full-table report) and speed (cold miss and warm cache hit) on SQLite and
// PostgreSQL at a multi-thousand-task history shape.
func TestMetricsIsCheapAtScaleOnBothBackends(t *testing.T) {
	if testing.Short() && os.Getenv(postgrestest.RequireEnv) == "" {
		t.Skip("seeds 3,000 tasks across 90 days")
	}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			var store *model.Store
			var db *sql.DB
			if backend == "sqlite" {
				var err error
				db, err = sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				store = model.New(db)
				if err := store.Init(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				store, db = postgrestest.Open(t)
				if store == nil {
					t.Skip("no postgres")
				}
			}

			seedBulkMetricsHistory(t, ctx, db, now, 3000, 60, 10)

			// Verify equivalence against full-table compute on the 7d window.
			since := now.Add(-7 * 24 * time.Hour)
			client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice")}, store)
			client.Now = func() time.Time { return now }
			srv := ui.NewServerWithClient(client)

			coldStart := time.Now()
			coldRec := metricsWith(t, srv, "/api/metrics", "")
			coldDur := time.Since(coldStart)
			if coldRec.Code != http.StatusOK {
				t.Fatalf("cold GET /api/metrics = %d: %s", coldRec.Code, coldRec.Body)
			}
			gotRep := decode[ui.MetricsReport](t, coldRec)

			hitStart := time.Now()
			hitRec := metricsWith(t, srv, "/api/metrics", coldRec.Header().Get("ETag"))
			hitDur := time.Since(hitStart)
			if hitRec.Code != http.StatusNotModified {
				t.Fatalf("warm 304 = %d, want 304", hitRec.Code)
			}
			if ui.MetricsBuilds(srv) != 1 {
				t.Fatalf("builds = %d, want 1", ui.MetricsBuilds(srv))
			}

			// Compare against full-table Store reads.
			fullTasks, err := store.TaskTimings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			fullRuns, err := store.RunTimings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			fullStates, err := store.States(ctx)
			if err != nil {
				t.Fatal(err)
			}
			fullUses, err := store.RunToolUses(ctx)
			if err != nil {
				t.Fatal(err)
			}
			fullPrompts, err := store.RunPrompts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			fullTranscripts, err := store.RunTranscripts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := metrics.Compute(metrics.Input{
				Window:      metrics.Window{Since: since, Until: now},
				Tasks:       fullTasks,
				Runs:        fullRuns,
				States:      fullStates,
				ToolUses:    fullUses,
				Prompts:     fullPrompts,
				Transcripts: fullTranscripts,
			})
			if gotRep.Throughput.TasksFiled != want.Throughput.TasksFiled ||
				gotRep.Throughput.TasksCompleted != want.Throughput.TasksCompleted ||
				gotRep.Throughput.RunsFinished != want.Throughput.RunsFinished ||
				gotRep.Runs.Live != want.Runs.Live ||
				gotRep.Backlog.Queued != want.Backlog.Queued ||
				gotRep.Prompts.Runs != want.Prompts.Runs ||
				gotRep.Tools.Calls != want.Tools.Calls ||
				gotRep.Merges.Landed != want.Merges.Landed {
				t.Fatalf("windowed report mismatch vs full-table:\ngot:  filed=%d completed=%d runsFin=%d live=%d queued=%d prompts=%d tools=%d landed=%d\nwant: filed=%d completed=%d runsFin=%d live=%d queued=%d prompts=%d tools=%d landed=%d",
					gotRep.Throughput.TasksFiled, gotRep.Throughput.TasksCompleted, gotRep.Throughput.RunsFinished,
					gotRep.Runs.Live, gotRep.Backlog.Queued, gotRep.Prompts.Runs, gotRep.Tools.Calls, gotRep.Merges.Landed,
					want.Throughput.TasksFiled, want.Throughput.TasksCompleted, want.Throughput.RunsFinished,
					want.Runs.Live, want.Backlog.Queued, want.Prompts.Runs, want.Tools.Calls, want.Merges.Landed)
			}
			t.Logf("%s 3k tasks: cold=%v warm304=%v", backend, coldDur.Round(time.Microsecond), hitDur.Round(time.Microsecond))
			coldLimit := 5 * time.Second // generous for pure-Go modernc.org/sqlite under -race
			if backend == "postgres" {
				coldLimit = 2 * time.Second
			}
			if coldDur > coldLimit {
				t.Errorf("%s cold GET /api/metrics = %v at 3k tasks, want <= %v", backend, coldDur, coldLimit)
			}
			if hitDur > 100*time.Millisecond {
				t.Errorf("%s warm 304 = %v, want < 100ms", backend, hitDur)
			}
		})
	}
}

func seedBulkMetricsHistory(t *testing.T, ctx context.Context, db *sql.DB, now time.Time, total, waiting, running int) {
	t.Helper()
	history := total - waiting - running
	base := now.Add(-90 * 24 * time.Hour)
	body := strings.Repeat("task description text ", 60)
	promptText := strings.Repeat("agent prompt text ", 100)
	transcriptText := strings.Repeat(`{"step":1,"ok":true}`+"\n", 30)

	const batchSize = 500
	for startIdx := 0; startIdx < history; startIdx += batchSize {
		endIdx := min(startIdx+batchSize, history)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		stmtTask, err := tx.PrepareContext(ctx,
			"INSERT INTO `task` (`id`,`intent`,`title`,`body`,`origin_actor_kind`,`origin_actor_id`,`origin_reason`,"+
				"`approval_actor_kind`,`approval_actor_id`,`approved_at`,`target_owner`,`target_name`,`binding`,`auto_merge`,`created_at`,`owner`) "+
				"VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		stmtObs, err := tx.PrepareContext(ctx,
			"INSERT INTO `task_observation` (`task_id`,`closed_at`,`completed_at`,`pr_opened_at`,`pr_merged_at`,`streak_count`) "+
				"VALUES (?,?,?,?,?,?)")
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		stmtRun, err := tx.PrepareContext(ctx,
			"INSERT INTO `task_run` (`id`,`task_id`,`sandbox`,`attempt`,`started_at`,`agent_started_at`,`finished_at`,`outcome`,`detail`,`prompt`,`transcript`) "+
				"VALUES (?,?,?,?,?,?,?,?,?,?,?)")
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		stmtTool, err := tx.PrepareContext(ctx,
			"INSERT INTO `task_run_tool` (`run_id`,`tool`,`calls`,`errored`,`timed_out`,`elided`,`result_bytes`,`max_result_bytes`) VALUES (?,?,?,?,?,?,?,?)")
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		for i := startIdx; i < endIdx; i++ {
			id := fmt.Sprintf("m%06d", i+1)
			created := base.Add(time.Duration(int64(i)*int64(90*24*time.Hour/time.Second)/int64(history)) * time.Second)
			repoName := fmt.Sprintf("svc-%02d", i%5)
			merged := i%3 == 0
			failed := i%20 == 0
			runs := 1
			if failed {
				runs = model.MaxConsecutiveFailures
			} else if i%5 == 0 {
				runs = 2
			}
			if _, err := stmtTask.ExecContext(ctx, id, "implement", fmt.Sprintf("Task %d", i), body,
				"human", "alice", "direct", "human", "alice", created,
				"acme", repoName, "directive", merged, created, "alice"); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			at := created.Add(time.Minute)
			for a := 1; a <= runs; a++ {
				runID := fmt.Sprintf("%s-%d", id, a)
				finAt := at.Add(15 * time.Minute)
				outcome := "succeeded"
				if failed || a < runs {
					outcome = "failed"
				}
				if _, err := stmtRun.ExecContext(ctx, runID, id, runID, a, at, at.Add(30*time.Second), finAt, outcome, "done", promptText, transcriptText); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if i%4 == 0 {
					if _, err := stmtTool.ExecContext(ctx, runID, "run_command", 5, 0, 0, 0, 1024, 512); err != nil {
						_ = tx.Rollback()
						t.Fatal(err)
					}
				}
				at = finAt
			}
			if failed {
				if _, err := stmtObs.ExecContext(ctx, id, nil, nil, nil, nil, model.MaxConsecutiveFailures); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
			} else {
				var prOpened, prMerged any
				if merged {
					prOpened, prMerged = at.Add(-time.Minute), at
				}
				if _, err := stmtObs.ExecContext(ctx, id, at, at, prOpened, prMerged, 0); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
			}
		}
		stmtTask.Close()
		stmtObs.Close()
		stmtRun.Close()
		stmtTool.Close()
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < waiting+running; i++ {
		id := fmt.Sprintf("m%06d", history+i+1)
		created := now.Add(-time.Duration(i%3600) * time.Second)
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task` (`id`,`intent`,`title`,`body`,`origin_actor_kind`,`origin_actor_id`,`origin_reason`,"+
				"`approval_actor_kind`,`approval_actor_id`,`approved_at`,`target_owner`,`target_name`,`binding`,`auto_merge`,`created_at`,`owner`) "+
				"VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			id, "implement", fmt.Sprintf("Open %d", i), body,
			"human", "alice", "direct", "human", "alice", created,
			"acme", "svc-00", "directive", false, created, "alice"); err != nil {
			t.Fatal(err)
		}
		if i >= waiting {
			runID := id + "-1"
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `task_run` (`id`,`task_id`,`sandbox`,`attempt`,`started_at`,`agent_started_at`,`transcript`) VALUES (?,?,?,?,?,?,?)",
				runID, id, runID, 1, now.Add(-2*time.Minute), now.Add(-time.Minute), transcriptText); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
