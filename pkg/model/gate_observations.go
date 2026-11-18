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

package model

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
)

// Diagnostic tables are additive, created by Init on both engines. They have
// no foreign references from lifecycle views, queues, waits or event outboxes.
const gateObservationTable = `CREATE TABLE IF NOT EXISTS ` + "`gate_observation_shadow`" + ` (
 ` + "`task_id`" + ` TEXT NOT NULL PRIMARY KEY,
 ` + "`generation`" + ` INTEGER NOT NULL,
 ` + "`source`" + ` TEXT NOT NULL,
 ` + "`view_json`" + ` TEXT NOT NULL,
 ` + "`started_at`" + ` DATETIME NOT NULL,
 ` + "`evaluated_at`" + ` DATETIME NOT NULL,
 ` + "`history_at`" + ` DATETIME NOT NULL,
 ` + "`evaluations`" + ` INTEGER NOT NULL
)`
const gateObservationHistoryTable = `CREATE TABLE IF NOT EXISTS ` + "`gate_observation_shadow_history`" + ` (
 ` + "`task_id`" + ` TEXT NOT NULL,
 ` + "`generation`" + ` INTEGER NOT NULL,
 ` + "`at`" + ` DATETIME NOT NULL,
 ` + "`view_json`" + ` TEXT NOT NULL,
 ` + "`comparison`" + ` TEXT NOT NULL,
 PRIMARY KEY (` + "`task_id`,`generation`" + `)
)`

type GateObservationSample struct {
	Generation uint64                `json:"generation"`
	At         time.Time             `json:"at"`
	Comparison string                `json:"comparison"`
	View       gates.ObservationView `json:"view"`
}
type GateObservationDiagnostics struct {
	StartedAt   time.Time               `json:"started_at"`
	Evaluations uint64                  `json:"evaluations"`
	Counts      map[string]int          `json:"counts"`
	History     []GateObservationSample `json:"history"`
}

// captureReportFacts reads only one task, its latest run, latest own non-handover
// comment, and exact current close event. The transaction is the complete local
// collection scope; no remote or paged evidence is claimed complete.
func captureReportFacts(ctx context.Context, q querier, id string, now time.Time) (gates.Facts, string, error) {
	f := gates.Facts{TaskID: id, Complete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute)}
	t, err := getTask(ctx, q, id)
	if err != nil {
		return f, "", err
	}
	if t == nil {
		return f, "", sql.ErrNoRows
	}
	o, err := getObservation(ctx, q, id)
	if err != nil {
		return f, "", err
	}
	if o == nil {
		o = &Observation{TaskID: id}
	}
	var state string
	if err = q.QueryRowContext(ctx, "SELECT `state` FROM `task_state` WHERE `task_id` = ?", id).Scan(&state); err != nil {
		return f, "", err
	}
	var goals int
	if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_goal` WHERE `task_id` = ?", id).Scan(&goals); err != nil {
		return f, "", err
	}
	f.Applicable = t.Intent == IntentAnalyze && AwaitsAck(*t) && !NeverCompletes(*t) && !t.Interactive && !t.HasConsole() && !t.KeepRunning && goals == 0 && !ClosesOnAnswer(*t)
	f.UnsupportedReason = "only finite, noninteractive analyze tasks with a person receipt requirement are projected"
	var runID, outcome string
	var started time.Time
	var finished sql.NullTime
	err = q.QueryRowContext(ctx, "SELECT `id`,`started_at`,`finished_at`,COALESCE(`outcome`,'') FROM `task_run` WHERE `task_id` = ? ORDER BY `attempt` DESC LIMIT 1", id).Scan(&runID, &started, &finished, &outcome)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return f, "", err
	}
	var noWork int
	if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_run_tool` WHERE `run_id` = ? AND `tool` = 'nothing_needed' AND `calls` > `errored` + `timed_out`", runID).Scan(&noWork); err != nil {
		return f, "", err
	}
	if noWork > 0 {
		f.Applicable = false
		f.UnsupportedReason = "the latest run declared nothing needed; report delivery and receipt are not equivalent milestones"
	}
	f.Running = runID != "" && !finished.Valid
	f.Settled = o.CompletedAt != nil && o.PendingQuestionCommentID == nil && finished.Valid && !f.Running && !o.CompletedAt.Before(started)
	var cid int64
	var body string
	var bodyLen int
	var at time.Time
	err = q.QueryRowContext(ctx, "SELECT `id`,SUBSTR(`body`,1,8192),LENGTH(`body`),`created_at` FROM `task_comment` WHERE `task_id` = ? AND `handover` = 0 AND ((`author_kind` = 'automation' AND `author_id` = 'grain' AND `author_behalf_kind` = 'agent' AND `author_behalf_id` = ?) OR (`author_kind` = 'agent' AND `author_id` = ?)) ORDER BY `id` DESC LIMIT 1", id, id, id).Scan(&cid, &body, &bodyLen, &at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return f, "", err
	}
	if cid > 0 && f.Settled && (at.Before(started) || at.After(*o.CompletedAt)) {
		f.Complete = false
		f.IncompleteReason = "Newest report cannot be correlated with the settled run."
	}
	if bodyLen > 8192 {
		f.Complete = false
		f.IncompleteReason = "Report exceeds this observer’s 8192-character evidence bound."
	}
	subject := gates.Subject{Kind: "task", TaskID: id}
	// Attachments are durable independent writes. This first body-only
	// projection does not claim a versioned complete attachment collection.
	if cid > 0 {
		var attachments int
		if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_attachment` WHERE `task_id` = ? AND `comment_id` = ?", id, cid).Scan(&attachments); err != nil {
			return f, "", err
		}
		if attachments > 0 {
			f.Complete = false
			f.IncompleteReason = "Attachment-bearing reports are incomplete in this body-only observer."
		}
	}
	if cid > 0 && strings.TrimSpace(body) != "" && f.Settled && !at.Before(started) && !at.After(*o.CompletedAt) {
		version := gates.Digest(struct {
			ID   int64
			Body string
			At   time.Time
		}{cid, body, at})
		f.Report = &gates.Evidence{Kind: "report", ID: strconv.FormatInt(cid, 10), Version: version, Subject: subject, Digest: version}
	}
	var eid int64
	var kind, actor, source string
	var closed time.Time
	if o.ClosedAt != nil {
		f.ReceiptUnversioned = true
		err = q.QueryRowContext(ctx, "SELECT `id`,`actor_kind`,`actor_id`,`actor_source`,`at` FROM `task_event` WHERE `task_id` = ? AND `kind` = 'closed' AND `at` = ? ORDER BY `id` DESC LIMIT 1", id, *o.ClosedAt).Scan(&eid, &kind, &actor, &source, &closed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return f, "", err
		}
		p := Principal{Kind: PrincipalKind(kind), ID: actor, Source: AuthSource(source)}
		// These legacy close events do not name a report version. Strictly
		// later timestamps are the only supported ordering proof: equal
		// timestamps can also mean a revised report arrived after the close.
		if f.Report != nil && strings.TrimSpace(actor) != "" && kind == string(PrincipalHuman) && p.Verified() && closed.After(at) && closed.After(*o.CompletedAt) {
			version := gates.Digest(struct {
				Event  int64
				Report string
				At     time.Time
			}{eid, f.Report.Version, closed})
			f.Receipt = &gates.Evidence{Kind: "acknowledgment", ID: strconv.FormatInt(eid, 10), Version: version, Subject: subject, Digest: version}
		}
	}
	// Any message, including one posted while evaluation runs, changes the fence.
	var lastComment int64
	if err = q.QueryRowContext(ctx, "SELECT COALESCE(MAX(`id`),0) FROM `task_comment` WHERE `task_id` = ?", id).Scan(&lastComment); err != nil {
		return f, "", err
	}
	// Only gate-relevant task/collection versions belong in this fence.
	// ObservedAt heartbeats, CI refreshes and unrelated task edits are not
	// new report/receipt evidence and must not flood comparison history.
	f.SourceVersion = gates.Digest(struct {
		TaskID                                                                string
		Applicable, Complete, Running, Settled                                bool
		UnsupportedReason, IncompleteReason, RegistryPin, State, Run, Outcome string
		Started                                                               time.Time
		Finished                                                              sql.NullTime
		Completed, Closed                                                     *time.Time
		PendingQuestion                                                       *int64
		Report, Receipt                                                       *gates.Evidence
		LastComment, Event                                                    int64
		NoWork                                                                int
	}{id, f.Applicable, f.Complete, f.Running, f.Settled, f.UnsupportedReason, f.IncompleteReason, gates.Digest(gates.Discover()), state, runID, outcome, started, finished, o.CompletedAt, o.ClosedAt, o.PendingQuestionCommentID, f.Report, f.Receipt, lastComment, eid, noWork})

	change, version, err := captureChangeGateFacts(ctx, q, *t, o)
	if err != nil {
		return f, "", err
	}
	f.Change = change
	if change != nil {
		f.SourceVersion = gates.Digest([]any{f.SourceVersion, version})
	}
	return f, state, nil
}

// beginGateObservation preserves normal writer serialization while bounding
// SQLite's lock acquisition to this diagnostic's deadline. Its busy handler can
// otherwise outlast context cancellation by the legacy five-second timeout.
// The change belongs only to a leased connection and is restored before reuse;
// Postgres already honors context while acquiring its advisory writer lock.
func (s *Store) beginGateObservation(ctx context.Context) (*sql.Tx, func(), error) {
	deadline, bounded := ctx.Deadline()
	if !bounded || IsPostgresDB(s.db) {
		tx, err := s.db.BeginTx(ctx, nil)
		return tx, func() {}, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	var busy int
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	release := func() {
		// Cleanup must survive the request's cancellation. If restoration
		// fails, discard the connection rather than changing legacy writers.
		restore, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := conn.ExecContext(restore, fmt.Sprintf("PRAGMA busy_timeout = %d", busy)); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}
	timeout := min(busy, max(1, int(time.Until(deadline).Milliseconds())))
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", timeout)); err != nil {
		release()
		return nil, nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		release()
		return nil, nil, err
	}
	return tx, release, nil
}

// ObserveTaskGates is display-driven shadow maintenance. It intentionally does
// NOT use Store.write: diagnostic commits must not signal Changed(), wake a
// dispatcher, append integration events or contribute task-failure accounting.
// The caller must already have authorized this task and its repository task-read
// scope (including transitive history), and bounded selection.
func (s *Store) ObserveTaskGates(ctx context.Context, id string, now time.Time) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	return s.observeTaskGates(ctx, id, now, true, "", nil)
}

// ObserveTaskGatesWithChecks accepts scoped external CI reads alongside the
// ordinary transactional report and review observations.
func (s *Store) ObserveTaskGatesWithChecks(ctx context.Context, id string, now time.Time, fence string, checks []gates.ChecksFacts) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	return s.observeTaskGates(ctx, id, now, true, fence, checks)
}

// ObserveTaskGatePredictions omits transitive history for scoped API callers.
func (s *Store) ObserveTaskGatePredictions(ctx context.Context, id string, now time.Time) (*gates.ObservationView, error) {
	return s.ObserveTaskGatePredictionsWithChecks(ctx, id, now, "", nil)
}

func (s *Store) ObserveTaskGatePredictionsWithChecks(ctx context.Context, id string, now time.Time, fence string, checks []gates.ChecksFacts) (*gates.ObservationView, error) {
	v, _, err := s.observeTaskGates(ctx, id, now, false, fence, checks)
	return v, err
}

var ErrGateObservationScopeDenied = errors.New("gate observation task repository is outside the authorized read scope")

func (s *Store) observeTaskGates(ctx context.Context, id string, now time.Time, history bool, fence string, checks []gates.ChecksFacts) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	var view gates.ObservationView
	var diag GateObservationDiagnostics
	var last error
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		tx, release, err := s.beginGateObservation(ctx)
		if err != nil {
			return nil, nil, err
		}
		err = func() error {
			// Authorize the current repository inside the collection transaction,
			// before any cache/backoff replay or transitive evidence read.
			if HasExternalRefReadable(ctx) {
				var owner, name string
				if err := tx.QueryRowContext(ctx, "SELECT COALESCE(`target_owner`,''),COALESCE(`target_name`,'') FROM `task` WHERE `id` = ?", id).Scan(&owner, &name); err != nil {
					return err
				}
				repo := ""
				if owner != "" || name != "" {
					repo = RepoFromStored(owner, name).String()
				}
				if !ExternalRefReadable(ctx, repo) {
					return ErrGateObservationScopeDenied
				}
			}
			// Persisted error backoff survives restarts and coalesces failed
			// refreshes. It expires only into another diagnostic collection.
			var cached string
			cacheErr := tx.QueryRowContext(ctx, "SELECT `view_json` FROM `gate_observation_shadow` WHERE `task_id` = ?", id).Scan(&cached)
			if cacheErr == nil {
				var failure gates.ObservationView
				if json.Unmarshal([]byte(cached), &failure) == nil && failure.Dirty && currentShadowBindings(failure, id) && len(failure.Results) > 0 && failure.Results[0].RetryAt != nil && now.Before(*failure.Results[0].RetryAt) {
					view = failure
					if history {
						diag, cacheErr = readGateDiagnostics(ctx, tx, id)
						return cacheErr
					}
					return nil
				}
			} else if !errors.Is(cacheErr, sql.ErrNoRows) {
				return cacheErr
			}
			f, state, err := captureReportFacts(ctx, tx, id, now)
			if err != nil {
				return err
			}
			invalidated := false
			if len(checks) > 0 {
				task, _, current, err := gateChecksInput(ctx, tx, id)
				if err != nil {
					return err
				}
				subjects := CheckSubjects(*task)
				if len(checks) > 8 || len(subjects) != len(checks) {
					return fmt.Errorf("incomplete change collection")
				}
				seen := map[gates.Subject]bool{}
				for _, c := range checks {
					if c.TaskID != id || !slices.Contains(subjects, c.Subject) || seen[c.Subject] {
						return fmt.Errorf("foreign checks subject")
					}
					seen[c.Subject] = true
					if c.Subject.ChangeKind == "pr" {
						for _, source := range c.Sources {
							if source.Kind != "github" {
								continue
							}
							repo, e := ParseRepo(c.Subject.Repository)
							if e != nil {
								return e
							}
							known, e := recordedGateChecks(ctx, tx, repo, c.Head)
							if e != nil {
								return e
							}
							if c.RecordedChecksVersion == "" || c.RecordedChecksVersion != known.Version {
								invalidated = true
								c.PolicyComplete = false
								c.Gap = "Recorded CI evidence changed during collection."
							}
						}
					}
					if fence == "" || fence != current {
						invalidated = true
						c.PolicyComplete = false
						c.Gap = "Local evidence changed during collection."
					}
					f.Checks = append(f.Checks, c)
				}
				versions := []string{f.SourceVersion}
				for _, c := range f.Checks {
					versions = append(versions, gates.ChecksVersion(c))
				}
				f.SourceVersion = gates.Digest(versions)
			}
			var previous, source string
			var generation, evaluations uint64
			var started, evaluated, historyAt time.Time
			err = tx.QueryRowContext(ctx, "SELECT `generation`,`source`,`view_json`,`started_at`,`evaluated_at`,`history_at`,`evaluations` FROM `gate_observation_shadow` WHERE `task_id` = ?", id).Scan(&generation, &source, &previous, &started, &evaluated, &historyAt, &evaluations)
			missing := errors.Is(err, sql.ErrNoRows)
			if err != nil && !missing {
				return err
			}
			if missing {
				generation, started, err = gateHistoryFloor(ctx, tx, id, now)
				if err != nil {
					return err
				}
				evaluations = generation
			}
			bindings := observationBindings(id, f)
			unsupportedCache, rejectedCache, bindingsChanged := false, false, false
			if !missing {
				var cachedView gates.ObservationView
				if err = json.Unmarshal([]byte(previous), &cachedView); err != nil {
					return err
				}
				unsupportedCache = !currentShadowBindings(cachedView, id)
				rejectedCache = cachedView.Dirty
				bindingsChanged = gates.Digest(cachedView.Bindings) != gates.Digest(bindings)
				// Remote reads start outside this transaction. A late older
				// request must never rewind newer accepted evidence, including
				// across a restart. A changed local fence still invalidates the
				// result rather than returning a possibly stale cached success.
				if len(checks) > 0 && now.Before(evaluated) {
					if !invalidated && !unsupportedCache && !rejectedCache && !bindingsChanged {
						view = cachedView
						if history {
							diag, err = readGateDiagnostics(ctx, tx, id)
							return err
						}
						return nil
					}
					now = evaluated
				}
			}
			changed := missing || source != f.SourceVersion || bindingsChanged
			if !changed && !unsupportedCache && !rejectedCache && now.Before(evaluated.Add(time.Minute)) && freshChecks(f.Checks, now) && freshObservation(previous, now) {
				if err = json.Unmarshal([]byte(previous), &view); err != nil {
					return err
				}
			} else {
				generation++
				evaluations++
				view = gates.ObservationView{APIVersion: "grain.gates/v1", Mode: "observation_only", Authority: "legacy", SourceRevision: f.SourceVersion, ComparedAt: now, LegacyState: state, Bindings: bindings, Results: []gates.EvaluationState{}}
				for _, b := range view.Bindings {
					r := gates.Evaluate(b, f, now, generation)
					if unsupportedCache {
						r.Status = "unknown"
						r.Summary = "Cached gate configuration or implementation is unsupported; the observation profile was regenerated."
						r.HumanText = nil
						r.Waits = []gates.Wait{}
					}
					// IDs are namespaced diagnostics, never accepted by an authority API.
					r.EvaluationID = fmt.Sprintf("shadow-%s-%d-%s", id, generation, b.ID)
					view.Results = append(view.Results, gates.EvaluationState{EvaluationID: r.EvaluationID, Revision: generation, Disposition: "accepted", Result: r, AcceptedAt: &now, DirtyGeneration: generation, CoveredGeneration: generation})
				}
				data, err := json.Marshal(view)
				if err != nil {
					return err
				}
				if len(data) > 256*1024 {
					return fmt.Errorf("observation view exceeds diagnostic byte bound")
				}
				if changed || unsupportedCache || !now.Before(historyAt.Add(time.Hour)) {
					comparison := compareReportMilestones(f, view)
					if unsupportedCache {
						comparison = "unknown_unsupported_config"
					}
					if _, err = tx.ExecContext(ctx, "INSERT INTO `gate_observation_shadow_history` (`task_id`,`generation`,`at`,`view_json`,`comparison`) VALUES (?,?,?,?,?)", id, generation, now, string(data), comparison); err != nil {
						return err
					}
					historyAt = now
				}
				_, err = tx.ExecContext(ctx, "INSERT INTO `gate_observation_shadow` (`task_id`,`generation`,`source`,`view_json`,`started_at`,`evaluated_at`,`history_at`,`evaluations`) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT (`task_id`) DO UPDATE SET `generation`=excluded.`generation`,`source`=excluded.`source`,`view_json`=excluded.`view_json`,`evaluated_at`=excluded.`evaluated_at`,`history_at`=excluded.`history_at`,`evaluations`=excluded.`evaluations`", id, generation, f.SourceVersion, string(data), started, now, historyAt, evaluations)
				if err != nil {
					return err
				}
				if err = pruneGateObservations(ctx, tx, id, now); err != nil {
					return err
				}
			}
			if history {
				diag, err = readGateDiagnostics(ctx, tx, id)
				return err
			}
			return nil
		}()
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		_ = tx.Rollback()
		release()
		if err == nil {
			return &view, &diag, nil
		}
		last = err
		if !isSerializationFailure(err) {
			break
		}
	}
	return nil, nil, last
}

func compareReportMilestones(f gates.Facts, v gates.ObservationView) string {
	if len(f.Checks) > 0 {
		labels := []string{}
		for i, c := range f.Checks {
			a, b := v.Results[2+i*2].Result.Status, v.Results[3+i*2].Result.Status
			label := "checks_" + a
			if a != b {
				label = "checks_legacy_difference_" + a + "_" + b
			}
			if c.Gap != "" {
				label += "_source_gap"
			}
			labels = append(labels, label)
		}
		if f.Change != nil {
			label := "review_approval_provenance_gap"
			if !f.Change.Complete {
				label = "review_approval_incomplete"
			}
			labels = append(labels, label)
		}
		return strings.Join(labels, ";")
	}
	if f.Change != nil {
		if !f.Change.Complete {
			return "review_approval_incomplete"
		}
		return "review_approval_provenance_gap"
	}
	if !f.Applicable {
		return "not_applicable"
	}
	if !f.Complete {
		return "unknown_incomplete"
	}
	if f.Settled && f.Report == nil {
		return "delivery_evidence_gap"
	}
	if f.ReceiptUnversioned && f.Receipt == nil {
		return "receipt_provenance_gap"
	}
	if v.Results[0].Result.Status == "met" && f.Report == nil {
		return "delivery_discrepancy"
	}
	if v.Results[1].Result.Status == "met" && f.Receipt == nil {
		return "receipt_discrepancy"
	}
	return "equivalent_milestones"
}

// RecordGateObservationFailure retains safe rejected diagnostics with bounded
// exponential retry. No raw infrastructure errors or evidence bodies are stored.
// This is controller state, never evaluator-private durable state.
func (s *Store) RecordGateObservationFailure(ctx context.Context, id string, now time.Time, view *gates.ObservationView) error {
	tx, release, err := s.beginGateObservation(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); release() }()
	if HasExternalRefReadable(ctx) {
		var owner, name string
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(`target_owner`,''),COALESCE(`target_name`,'') FROM `task` WHERE `id` = ?", id).Scan(&owner, &name); err != nil {
			return err
		}
		repo := ""
		if owner != "" || name != "" {
			repo = RepoFromStored(owner, name).String()
		}
		if !ExternalRefReadable(ctx, repo) {
			return ErrGateObservationScopeDenied
		}
	}
	var raw string
	var generation, evaluations uint64
	var started, evaluated time.Time
	err = tx.QueryRowContext(ctx, "SELECT `view_json`,`generation`,`started_at`,`evaluations`,`evaluated_at` FROM `gate_observation_shadow` WHERE `task_id` = ?", id).Scan(&raw, &generation, &started, &evaluations, &evaluated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		generation, started, err = gateHistoryFloor(ctx, tx, id, now)
		if err != nil {
			return err
		}
		evaluations = generation
	}
	delay := time.Second
	if raw != "" {
		var old gates.ObservationView
		if json.Unmarshal([]byte(raw), &old) == nil && currentShadowBindings(old, id) {
			// now is the collection's start, not its failure completion.
			// A slower failed request cannot replace a newer accepted read;
			// bursts of failures share one generation until its retry is due.
			first := old.Results[0] // currentShadowBindings validated the fixed pair
			backingOff := old.Dirty && first.RetryAt != nil && now.Before(*first.RetryAt)
			if !now.After(evaluated) || backingOff {
				*view = old
				return tx.Commit()
			}
			if old.Dirty && first.Result.Error != nil && first.RetryAt != nil {
				// Recover the previous nominal delay before doubling it;
				// retry jitter must not compound across generations.
				prior := first.RetryAt.Sub(old.ComparedAt)
				nominal := prior * 100 / time.Duration(100+first.DirtyGeneration%21)
				delay = min(2*nominal, time.Minute)
			}
		}
	}
	task, err := getTask(ctx, tx, id)
	if err != nil {
		return err
	}
	requestedChecks := false
	for _, b := range view.Bindings {
		if b.Type == "checks" {
			requestedChecks = true
		}
	}
	if raw != "" {
		var previous gates.ObservationView
		if json.Unmarshal([]byte(raw), &previous) == nil && currentShadowBindings(previous, id) {
			for _, b := range previous.Bindings {
				if b.Type == "checks" {
					requestedChecks = true
				}
			}
		}
	}
	unavailableChecks := []gates.ChecksFacts{}
	if task != nil && requestedChecks {
		for _, sub := range CheckSubjects(*task) {
			if len(unavailableChecks) < 8 {
				unavailableChecks = append(unavailableChecks, gates.ChecksFacts{TaskID: id, Subject: sub})
			}
		}
	}
	bindings := gates.BindingsFor(id, unavailableChecks)
	if raw != "" {
		var previous gates.ObservationView
		if json.Unmarshal([]byte(raw), &previous) == nil && currentShadowBindings(previous, id) {
			for _, b := range previous.Bindings[2:] {
				if b.Type != "checks" {
					bindings = append(bindings, b)
				}
			}
		}
	}
	if len(bindings) > 64 {
		bindings = bindings[:64]
	}
	*view = gates.UnavailableObservationForBindings(id, now, bindings)
	generation++
	evaluations++

	// Stable 0..20% jitter coalesces replays without evaluator randomness.
	retry := now.Add(delay + delay*time.Duration(generation%21)/100)
	for i := range view.Results {
		r := &view.Results[i]
		r.EvaluationID = fmt.Sprintf("shadow-%s-%d-error-%d", id, generation, i)
		r.Result.EvaluationID = r.EvaluationID
		r.Revision = generation
		r.RetryAt = &retry
		r.DirtyGeneration = generation
		r.CoveredGeneration = generation - 1
	}
	rawBytes, err := json.Marshal(view)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO `gate_observation_shadow` (`task_id`,`generation`,`source`,`view_json`,`started_at`,`evaluated_at`,`history_at`,`evaluations`) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT (`task_id`) DO UPDATE SET `generation`=excluded.`generation`,`source`=excluded.`source`,`view_json`=excluded.`view_json`,`evaluated_at`=excluded.`evaluated_at`,`history_at`=excluded.`history_at`,`evaluations`=excluded.`evaluations`", id, generation, view.SourceRevision, string(rawBytes), started, now, now, evaluations)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO `gate_observation_shadow_history` (`task_id`,`generation`,`at`,`view_json`,`comparison`) VALUES (?,?,?,?,?)", id, generation, now, string(rawBytes), "unknown_error")
	if err != nil {
		return err
	}
	if err = pruneGateObservations(ctx, tx, id, now); err != nil {
		return err
	}
	return tx.Commit()
}

func readGateDiagnostics(ctx context.Context, q querier, id string) (GateObservationDiagnostics, error) {
	diag := GateObservationDiagnostics{Counts: map[string]int{}, History: []GateObservationSample{}}
	err := q.QueryRowContext(ctx, "SELECT `started_at`,`evaluations` FROM `gate_observation_shadow` WHERE `task_id` = ?", id).Scan(&diag.StartedAt, &diag.Evaluations)
	if err != nil {
		return diag, err
	}

	rows, err := q.QueryContext(ctx, "SELECT `comparison`,COUNT(*) FROM `gate_observation_shadow_history` WHERE `task_id` = ? GROUP BY `comparison`", id)
	if err != nil {
		return diag, err
	}
	for rows.Next() {
		var label string
		var n int
		if err = rows.Scan(&label, &n); err != nil {
			rows.Close()
			return diag, err
		}
		diag.Counts[label] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return diag, err
	}
	rows, err = q.QueryContext(ctx, "SELECT `generation`,`at`,`view_json`,`comparison` FROM `gate_observation_shadow_history` WHERE `task_id` = ? ORDER BY `generation` DESC LIMIT 20", id)
	if err != nil {
		return diag, err
	}
	defer rows.Close()
	for rows.Next() {
		var sample GateObservationSample
		var raw string
		if err = rows.Scan(&sample.Generation, &sample.At, &raw, &sample.Comparison); err != nil {
			return diag, err
		}
		if err = json.Unmarshal([]byte(raw), &sample.View); err != nil {
			return diag, err
		}
		diag.History = append(diag.History, sample)
	}
	return diag, rows.Err()
}

// Prune with ordered cutoffs rather than tuple NOT IN against the retained
// 32768-row set. SQLite can otherwise do a quadratic anti-join while holding
// the same writer lock used by legacy work. Existing time/primary-key indexes
// bound the scans and target only expired or overflow rows.
func pruneGateObservations(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM `gate_observation_shadow_history` WHERE `at` < ?", now.Add(-72*time.Hour)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM `gate_observation_shadow_history` WHERE `task_id` = ? AND `generation` <= (SELECT `generation` FROM `gate_observation_shadow_history` WHERE `task_id` = ? ORDER BY `generation` DESC LIMIT 1 OFFSET 1024)", id, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM `gate_observation_shadow` WHERE `evaluated_at` < ? OR `task_id` NOT IN (SELECT `task_id` FROM `gate_observation_shadow` ORDER BY `evaluated_at` DESC,`task_id` DESC LIMIT 32)", now.Add(-72*time.Hour)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM `gate_observation_shadow_history` WHERE (`at`,`task_id`,`generation`) <= (SELECT `at`,`task_id`,`generation` FROM `gate_observation_shadow_history` ORDER BY `at` DESC,`task_id` DESC,`generation` DESC LIMIT 1 OFFSET 32768)")
	return err
}

// Cached diagnostics cannot smuggle an unregistered profile or old build into
// a current accepted observation. Only this fixed monitor projection is valid.
func currentShadowBindings(v gates.ObservationView, id string) bool {
	if v.APIVersion != "grain.gates/v1" || v.Mode != "observation_only" || v.Authority != "legacy" || v.LifecycleEffects || v.ActionEffects || v.DispatchEffects || v.AutomaticCutover {
		return false
	}
	base := gates.Bindings(id)
	if len(v.Bindings) < 2 || len(v.Bindings) > 64 || len(v.Results) != len(v.Bindings) || gates.Digest(v.Bindings[:2]) != gates.Digest(base) {
		return false
	}
	seen := map[string]bool{}
	for i, b := range v.Bindings {
		if seen[b.ID] || b.Purpose != "monitor" || b.Required || b.Action != nil || b.Origin.ID != id || b.Origin.Revision != gates.Implementation {
			return false
		}
		seen[b.ID] = true
		if i >= 2 {
			if b.Type == "checks" {
				var config gates.ChecksConfig
				if json.Unmarshal(b.Config, &config) != nil {
					return false
				}
				expected := gates.ChecksBinding(gates.ChecksFacts{TaskID: id, Subject: b.Subject}, config.Semantics == "legacy_ci_v1")
				if b.Subject.Kind != "change" || gates.Digest(b) != gates.Digest(expected) {
					return false
				}
			} else if (b.Type != "review" && b.Type != "approval") || b.Subject.Kind != "change" || b.Subject.TaskID != "" || b.Subject.Authority == "" || b.Subject.Repository == "" || b.Subject.ChangeKind != "pr" || b.Subject.ID == "" {
				return false
			}
		}
		if _, err := gates.Validate(b.Type, b.Version, b.Config); err != nil {
			return false
		}
		if b.Type == "review" {
			var config gates.ReviewConfig
			if json.Unmarshal(b.Config, &config) != nil || (config.Semantics == "legacy_completion_v1" && !b.Origin.Imported) {
				return false
			}
		}
		r := v.Results[i].Result
		if r.Type != b.Type || r.Version != b.Version || len(r.Work) > 0 || len(r.Actions) > 0 {
			return false
		}
	}
	return true
}

// Current caches may be evicted before their bounded history expires. Resume
// above retained keys on both acceptance and failure paths, preserving the first
// retained collection marker. Unsampled minute refresh counts are not recoverable.
func gateHistoryFloor(ctx context.Context, q querier, id string, now time.Time) (uint64, time.Time, error) {
	var generation uint64
	err := q.QueryRowContext(ctx, "SELECT `generation` FROM `gate_observation_shadow_history` WHERE `task_id` = ? ORDER BY `generation` DESC LIMIT 1", id).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, now, nil
	}
	if err != nil {
		return 0, time.Time{}, err
	}
	var started time.Time
	err = q.QueryRowContext(ctx, "SELECT `at` FROM `gate_observation_shadow_history` WHERE `task_id` = ? ORDER BY `generation` ASC LIMIT 1", id).Scan(&started)
	return generation, started, err
}

func freshChecks(checks []gates.ChecksFacts, now time.Time) bool {
	for _, c := range checks {
		if !now.Before(c.ValidUntil) {
			return false
		}
	}
	return true
}

func freshObservation(raw string, now time.Time) bool {
	var v gates.ObservationView
	if json.Unmarshal([]byte(raw), &v) != nil {
		return false
	}
	for _, r := range v.Results {
		if !now.Before(r.Result.ValidUntil) {
			return false
		}
	}
	return true
}

// GateObservationRetry is a read-only persisted diagnostic backoff check. It
// prevents source outages from creating one set of remote reads per UI poll.
// Authorization and resource/history redaction remain the ordinary caller's job.
func (s *Store) GateObservationRetry(ctx context.Context, id string, now time.Time, history bool) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	var raw string
	var owner, name string
	err := s.db.QueryRowContext(ctx, "SELECT g.`view_json`, COALESCE(t.`target_owner`,''), COALESCE(t.`target_name`,'') FROM `gate_observation_shadow` g JOIN `task` t ON g.`task_id` = t.`id` WHERE g.`task_id` = ?", id).Scan(&raw, &owner, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	repo := ""
	if owner != "" || name != "" {
		repo = RepoFromStored(owner, name).String()
	}
	if HasExternalRefReadable(ctx) && !ExternalRefReadable(ctx, repo) {
		return nil, nil, ErrGateObservationScopeDenied
	}
	var v gates.ObservationView
	if json.Unmarshal([]byte(raw), &v) != nil || !currentShadowBindings(v, id) || !v.Dirty || len(v.Results) == 0 || v.Results[0].RetryAt == nil || !now.Before(*v.Results[0].RetryAt) {
		return nil, nil, nil
	}
	if !history {
		return &v, nil, nil
	}
	d, err := readGateDiagnostics(ctx, s.db, id)
	if err != nil {
		return nil, nil, err
	}
	return &v, &d, nil
}
