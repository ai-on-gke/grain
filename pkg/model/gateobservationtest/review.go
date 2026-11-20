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

package gateobservationtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunReviewRegressions tests ordering and persistence failures using the same
// real store operations on SQLite and Postgres.
func RunReviewRegressions(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	base := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP}
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seed := func(t *testing.T, id, body string) {
		t.Helper()
		must(t, s.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentAnalyze, Title: "Analyze a report",
			Filing:   model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective, CreatedAt: &base,
		}))
		must(t, s.StartRun(ctx, model.Run{ID: id + "-run", TaskID: id, Attempt: 1, StartedAt: base, Sandbox: "test"}, model.Limits{}))
		if body != "" {
			_, err := s.AddComment(ctx, model.Comment{
				TaskID: id, Author: model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: id}},
				Body: body, CreatedAt: base.Add(time.Second),
			})
			must(t, err)
		}
		must(t, s.FinishRun(ctx, id+"-run", base.Add(time.Second), "succeeded", ""))
		must(t, s.ObserveField(ctx, id, base.Add(time.Second), func(o *model.Observation) {
			at := base.Add(time.Second)
			o.CompletedAt = &at
		}))
	}
	read := func(t *testing.T, id string, now time.Time) (*gates.ObservationView, *model.GateObservationDiagnostics) {
		t.Helper()
		v, d, err := s.ObserveTaskGates(ctx, id, now)
		must(t, err)
		assertShadowView(t, v)
		return v, d
	}

	t.Run("complete-empty-and-foreign-comments", func(t *testing.T) {
		id := "empty-report-review"
		seed(t, id, "")
		checkEmpty := func() {
			t.Helper()
			v, d := read(t, id, base.Add(3*time.Second))
			if v.Results[0].Result.Status != "unmet" || v.Results[1].Result.Status != "unknown" || !v.Results[0].Result.Dependencies[0].Complete || len(v.Results[0].Result.Evidence) != 0 || d.Counts["delivery_evidence_gap"] == 0 {
				t.Fatalf("complete empty report collection was not negative evidence: %+v", v)
			}
		}
		checkEmpty()
		for _, c := range []model.Comment{
			{Author: model.Attribution{Actor: human}},
			{Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "another-task"}}},
			{Author: model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: "another-task"}}},
			{Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: id}}, Handover: true},
		} {
			c.TaskID, c.Body, c.CreatedAt = id, "Not a report", base.Add(time.Second)
			_, err := s.AddComment(ctx, c)
			must(t, err)
			checkEmpty()
		}
	})

	t.Run("bounded-and-current-report-evidence", func(t *testing.T) {
		for _, tc := range []struct {
			name, body, want string
			complete         bool
		}{
			{"whitespace", " ", "unmet", true},
			{"body-bound", strings.Repeat("x", 8193), "unknown", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				id := "report-review-" + tc.name
				seed(t, id, tc.body)
				v, _ := read(t, id, base.Add(3*time.Second))
				if v.Results[0].Result.Status != tc.want || v.Results[0].Result.Dependencies[0].Complete != tc.complete {
					t.Fatalf("report bound/completeness: %+v", v.Results[0])
				}
			})
		}
		id := "report-review-new-run"
		seed(t, id, "Previous run report")
		must(t, s.StartRun(ctx, model.Run{ID: id + "-second", TaskID: id, Attempt: 2, StartedAt: base.Add(4 * time.Second), Sandbox: "test"}, model.Limits{}))
		must(t, s.FinishRun(ctx, id+"-second", base.Add(5*time.Second), "succeeded", ""))
		must(t, s.ObserveField(ctx, id, base.Add(5*time.Second), func(o *model.Observation) { at := base.Add(5 * time.Second); o.CompletedAt = &at }))
		v, _ := read(t, id, base.Add(6*time.Second))
		if v.Results[0].Result.Status != "unknown" || v.Results[1].Result.Status != "unknown" || v.Results[0].Result.Dependencies[0].Complete {
			t.Fatal("previous run's report satisfied the latest run")
		}
	})

	t.Run("receipt-provenance", func(t *testing.T) {
		for i, tc := range []struct {
			name    string
			actor   model.Principal
			closeAt time.Time
			want    string
		}{
			{"verified", human, base.Add(2 * time.Second), "met"},
			{"fallback", model.Principal{Kind: model.PrincipalHuman, ID: "operator", Source: model.SourceAccessCode}, base.Add(2 * time.Second), "unknown"},
			{"automation", model.GrainPrincipal, base.Add(2 * time.Second), "unknown"},
			{"service", model.Principal{Kind: model.PrincipalService, ID: "robot", Source: model.SourceIAP}, base.Add(2 * time.Second), "unknown"},
			{"missing-identity", model.Principal{Kind: model.PrincipalHuman, Source: model.SourceIAP}, base.Add(2 * time.Second), "unknown"},
			{"ambiguous-order", human, base.Add(time.Second), "unknown"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				id := fmt.Sprintf("receipt-review-%d", i)
				seed(t, id, "Durable report")
				must(t, s.ObserveClose(ctx, id, tc.closeAt, tc.actor, "read", func(o *model.Observation) { o.ClosedAt = &tc.closeAt }))
				v, _ := read(t, id, base.Add(3*time.Second))
				if got := v.Results[1].Result.Status; got != tc.want {
					t.Fatalf("receipt = %s, want %s", got, tc.want)
				}
			})
		}
	})

	t.Run("revised-report-at-close-timestamp", func(t *testing.T) {
		id := "receipt-revised-at-close"
		seed(t, id, "Durable report")
		closed := base.Add(2 * time.Second)
		must(t, s.ObserveClose(ctx, id, closed, human, "read", func(o *model.Observation) { o.ClosedAt = &closed }))
		v, _ := read(t, id, closed)
		if v.Results[1].Result.Status != "met" {
			t.Fatal("initial receipt missing")
		}
		// This report is written AFTER close, but has the same timestamp.
		// It must never inherit the old report's receipt.
		_, err := s.AddComment(ctx, model.Comment{
			TaskID: id, Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: id}},
			Body: "Revised after receipt", CreatedAt: closed,
		})
		must(t, err)
		must(t, s.ObserveField(ctx, id, closed, func(o *model.Observation) { o.CompletedAt = &closed }))
		v, d := read(t, id, closed)
		if v.Results[0].Result.Status != "met" || v.Results[1].Result.Status != "unknown" || d.Counts["receipt_provenance_gap"] == 0 {
			t.Fatalf("old receipt covered revised report: %+v", v.Results)
		}
	})

	t.Run("failure-ordering-and-coalescing", func(t *testing.T) {
		id := "failure-review"
		seed(t, id, "Durable report")
		initialAt := base.Add(3 * time.Second)
		initial, _ := read(t, id, initialAt)
		newerAt := initialAt.Add(2 * time.Minute)
		newer, newerDiag := read(t, id, newerAt)
		// Model an earlier slow read finishing with an error after the newer
		// read has committed. Failure completion must not rewind the cache.
		failure := *initial
		failureAt := initialAt.Add(time.Second)
		must(t, s.RecordGateObservationFailure(ctx, id, failureAt, &failure))
		current, diag := read(t, id, newerAt)
		if !reflect.DeepEqual(current, newer) || diag.Evaluations != newerDiag.Evaluations {
			t.Fatal("older failure overwrote a newer accepted observation")
		}
		// Concurrent failures of the same attempted refresh must share the
		// retry and generation, including across store instances.
		failureAt = newerAt.Add(time.Second)
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				input := *initial // deliberately accepted/stale input
				errs <- model.New(db).RecordGateObservationFailure(ctx, id, failureAt, &input)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			must(t, err)
		}
		failed, fd := read(t, id, failureAt)
		if !failed.Dirty || !failed.ComparedAt.Equal(failureAt) || fd.Evaluations != newerDiag.Evaluations+1 || fd.Counts["unknown_error"] != 1 {
			t.Fatalf("failure burst was not coalesced: %+v, %+v", failed, fd)
		}
		for _, entry := range failed.Results {
			if entry.Disposition != "rejected" || entry.AcceptedAt != nil || entry.Result.Status != "error" || entry.Result.Error == nil || entry.Result.Dependencies[0].Complete || len(entry.Result.Evidence) != 0 || len(entry.Result.Work) != 0 || len(entry.Result.Actions) != 0 || len(entry.Result.Waits) != 0 {
				t.Fatalf("failure inherited accepted evidence or effects: %+v", entry)
			}
		}
		// Expired retry must recollect even when the source fence matches the
		// last success. Replaying a failure must not lease it for a minute.
		_, err := db.ExecContext(ctx, "UPDATE `gate_observation_shadow` SET `source` = ? WHERE `task_id` = ?", newer.SourceRevision, id)
		must(t, err)
		recovered, _ := read(t, id, *failed.Results[0].RetryAt)
		if recovered.Dirty || recovered.Results[0].Result.Status != "met" {
			t.Fatal("expired retry reused the rejected observation")
		}
	})

	t.Run("deadline-noninterference", func(t *testing.T) {
		id := "deadline-review"
		seed(t, id, "Durable report")
		before, err := s.GetObservation(ctx, id)
		must(t, err)
		commits, wake := s.CommitCount(), s.Changed()
		// Hold the real writer lock. Neither a timed-out collection nor its
		// timed-out failure persistence can become legacy work or a failure.
		blocker, err := db.BeginTx(ctx, nil)
		must(t, err)
		defer blocker.Rollback()
		for _, fail := range []bool{false, true} {
			started := time.Now()
			deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			if fail {
				v := gates.UnavailableObservation(id, base.Add(3*time.Second))
				err = s.RecordGateObservationFailure(deadline, id, base.Add(3*time.Second), &v)
			} else {
				_, _, err = s.ObserveTaskGates(deadline, id, base.Add(3*time.Second))
			}
			cancel()
			if err == nil || time.Since(started) > time.Second {
				t.Fatalf("contended diagnostic exceeded its bounded wait: %v (%s)", err, time.Since(started))
			}
		}
		must(t, blocker.Rollback())
		if !model.IsPostgresDB(db) {
			// Hold both recently returned connections so the restored
			// diagnostic connection cannot hide behind the lock holder.
			for range 2 {
				conn, err := db.Conn(ctx)
				must(t, err)
				defer conn.Close()
				var busy int
				must(t, conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy))
				if busy != 5000 {
					t.Fatalf("diagnostic leaked busy timeout %d to legacy writers", busy)
				}
			}
		}
		after, err := s.GetObservation(ctx, id)
		must(t, err)
		if !reflect.DeepEqual(before, after) || s.CommitCount() != commits {
			t.Fatal("timeout changed legacy progress/failure/question state")
		}
		select {
		case <-wake:
			t.Fatal("timeout woke dispatch")
		default:
		}
		for _, table := range []string{"gate_observation_shadow", "gate_observation_shadow_history"} {
			var count int
			must(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"` WHERE `task_id` = ?", id).Scan(&count))
			if count != 0 {
				t.Fatal("timed-out diagnostic committed a partial result")
			}
		}
	})

	t.Run("retention", func(t *testing.T) {
		id := "retention-review"
		_, err := db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS `gate_shadow_history_at` ON `gate_observation_shadow_history` (`at`)")
		must(t, err)
		must(t, s.Init(ctx))
		must(t, s.Init(ctx))
		seed(t, id, "Durable report")
		now := base.Add(5 * time.Minute)
		accepted, _ := read(t, id, now)
		raw, err := json.Marshal(accepted)
		must(t, err)
		tx, err := db.BeginTx(ctx, nil)
		must(t, err)
		defer tx.Rollback()
		// Fill this task beyond its per-task limit and rotate beyond the
		// current-cache bound. The extra history bodies carry no evidence.
		for i := 2; i <= 1030; i++ {
			_, err = tx.ExecContext(ctx, "INSERT INTO `gate_observation_shadow_history` (`task_id`,`generation`,`at`,`view_json`,`comparison`) VALUES (?,?,?,?,?)", id, i, now, string(raw), "equivalent_milestones")
			must(t, err)
		}
		_, err = tx.ExecContext(ctx, "UPDATE `gate_observation_shadow` SET `generation` = 1030 WHERE `task_id` = ?", id)
		must(t, err)
		for i := 0; i < 35; i++ {
			_, err = tx.ExecContext(ctx, "INSERT INTO `gate_observation_shadow` (`task_id`,`generation`,`source`,`view_json`,`started_at`,`evaluated_at`,`history_at`,`evaluations`) VALUES (?,?,?,?,?,?,?,?)", fmt.Sprintf("rotated-%d", i), 1, "none", "{}", now, now.Add(-time.Second), now, 1)
			must(t, err)
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO `gate_observation_shadow_history` (`task_id`,`generation`,`at`,`view_json`,`comparison`) VALUES (?,?,?,?,?)", "expired", 1, now.Add(-73*time.Hour), "{}", "unknown_error")
		must(t, err)
		must(t, tx.Commit())
		// Failure writes must enforce the same limits as successful writes.
		failureAt := now.Add(time.Second)
		must(t, s.RecordGateObservationFailure(ctx, id, failureAt, accepted))
		_, diag := read(t, id, failureAt)
		assertHistoryPage := func(newest uint64) {
			if len(diag.History) == 0 || len(diag.History) > 20 || diag.History[0].Generation != newest {
				t.Fatalf("history page omitted the newest sample or exceeded its row bound: %d", len(diag.History))
			}
			loadedBytes := 0
			for _, sample := range diag.History {
				raw, err := json.Marshal(sample.View)
				must(t, err)
				loadedBytes += len(raw)
			}
			if len(diag.History) > 1 && loadedBytes > model.MaxGateObservationHistoryBytes {
				t.Fatal("history page exceeded its byte bound")
			}
			if len(diag.History) < 20 {
				var nextRaw string
				must(t, db.QueryRowContext(ctx, "SELECT `view_json` FROM `gate_observation_shadow_history` WHERE `task_id` = ? AND `generation` < ? ORDER BY `generation` DESC LIMIT 1", id, diag.History[len(diag.History)-1].Generation).Scan(&nextRaw))
				if loadedBytes+len(nextRaw) <= model.MaxGateObservationHistoryBytes {
					t.Fatal("history page stopped before filling its load budget")
				}
			}
		}
		assertHistoryPage(1031)
		var count int
		must(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `gate_observation_shadow_history` WHERE `task_id` = ?", id).Scan(&count))
		if count != 1024 {
			t.Fatalf("per-task retention = %d", count)
		}
		must(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `gate_observation_shadow`").Scan(&count))
		if count != 32 {
			t.Fatalf("current cache retention = %d", count)
		}
		must(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `gate_observation_shadow_history` WHERE `task_id` = 'expired'").Scan(&count))
		if count != 0 {
			t.Fatal("expired history retained")
		}
		// Fill a representative 32-task history at its global capacity. The
		// next write must trim globally as well as preserving this task's
		// newest samples; no unbounded history query reaches the API.
		_, err = db.ExecContext(ctx, "WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 32768) INSERT INTO `gate_observation_shadow_history` (`task_id`,`generation`,`at`,`view_json`,`comparison`) SELECT 'retention-global-' || CAST((n-1)/1024 AS TEXT), (n-1)%1024+1, ?, '{}', 'test' FROM seq", now.Add(-time.Second))
		must(t, err)
		started := time.Now()
		must(t, s.RecordGateObservationFailure(ctx, id, now.Add(3*time.Second), accepted))
		t.Logf("failure write with full global history: %s", time.Since(started))
		must(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `gate_observation_shadow_history`").Scan(&count))
		if count != 32768 {
			t.Fatalf("global history retention = %d", count)
		}
		_, diag = read(t, id, now.Add(3*time.Second))
		assertHistoryPage(1032)
		// Normal collection must still recover at capacity, including a one-row
		// per-task/global overflow. Record latency without asserting production
		// wall-clock budgets under the race detector's SQLite instrumentation.
		started = time.Now()
		v, _, err := s.ObserveTaskGates(ctx, id, now.Add(6*time.Second))
		t.Logf("successful collection at global capacity: %s", time.Since(started))
		must(t, err)
		if v.Dirty || v.Results[0].Result.Status != "met" {
			t.Fatal("full history prevented collection recovery")
		}
	})
}
