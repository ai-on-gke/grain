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

type gateTaskSnapshot struct {
	goalLoaded bool
	goal       *TaskGoal
	runLoaded  bool
	runID      string
	runOutcome string
	runStarted time.Time
	runFinish  sql.NullTime
}

// captureReportFacts reads only one task, its latest run, latest own non-handover
// comment, and exact current close event. The transaction is the complete local
// collection scope; no remote or paged evidence is claimed complete.
// Share the already authorized task rows within one diagnostic transaction.
func captureReportFacts(ctx context.Context, q querier, id string, now time.Time, t *Task, o *Observation) (gates.Facts, string, error) {
	return captureReportFactsWithSnap(ctx, q, id, now, t, o, nil)
}

func captureReportFactsWithSnap(ctx context.Context, q querier, id string, now time.Time, t *Task, o *Observation, snap *gateTaskSnapshot) (gates.Facts, string, error) {
	f := gates.Facts{TaskID: id, Complete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute)}
	if t == nil {
		return f, "", sql.ErrNoRows
	}
	var err error
	if o == nil {
		o = &Observation{TaskID: id}
	}
	var state string
	if err = q.QueryRowContext(ctx, "SELECT `state` FROM `task_state` WHERE `task_id` = ?", id).Scan(&state); err != nil {
		return f, "", err
	}
	var goals int
	if snap != nil && snap.goalLoaded {
		if snap.goal != nil {
			goals = 1
		}
	} else {
		if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_goal` WHERE `task_id` = ?", id).Scan(&goals); err != nil {
			return f, "", err
		}
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
	if snap != nil {
		snap.runLoaded = true
		snap.runID, snap.runStarted, snap.runFinish, snap.runOutcome = runID, started, finished, outcome
	}
	var noWork int
	if runID != "" {
		if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_run_tool` WHERE `run_id` = ? AND `tool` = 'nothing_needed' AND `calls` > `errored` + `timed_out`", runID).Scan(&noWork); err != nil {
			return f, "", err
		}
	}
	if noWork > 0 {
		f.Applicable = false
		f.UnsupportedReason = "the latest run declared nothing needed; report delivery and receipt are not equivalent milestones"
	}
	settledAt := o.CompletedAt
	if settledAt == nil && goals > 0 && finished.Valid && t.DeferredAt != nil {
		var raw string
		sourceErr := q.QueryRowContext(ctx, "SELECT `data` FROM `gate_outcome_source` WHERE `task_id` = ? AND `resource` = 'goal-rest'", id).Scan(&raw)
		if sourceErr != nil && !errors.Is(sourceErr, sql.ErrNoRows) {
			return f, "", sourceErr
		}
		var rest GateOutcomeSource
		if sourceErr == nil && json.Unmarshal([]byte(raw), &rest) == nil && rest.RunID == runID && rest.State == "resting_on_goal" && rest.Policy == GateOutcomePolicy(*t) && !rest.At.Before(started) && t.DeferredAt.Truncate(time.Microsecond).Equal(rest.At.Truncate(time.Microsecond)) {
			settledAt = &rest.At
		}
	}
	f.Running = runID != "" && !finished.Valid
	f.Settled = settledAt != nil && o.PendingQuestionCommentID == nil && finished.Valid && !f.Running && !settledAt.Before(started)
	var cid int64
	var body string
	var bodyLen int
	var at time.Time
	err = q.QueryRowContext(ctx, "SELECT `id`,SUBSTR(`body`,1,8192),LENGTH(`body`),`created_at` FROM `task_comment` WHERE `task_id` = ? AND `handover` = 0 AND ((`author_kind` = 'automation' AND `author_id` = 'grain' AND `author_behalf_kind` = 'agent' AND `author_behalf_id` = ?) OR (`author_kind` = 'agent' AND `author_id` = ?)) ORDER BY `id` DESC LIMIT 1", id, id, id).Scan(&cid, &body, &bodyLen, &at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return f, "", err
	}
	if cid > 0 && f.Settled && (at.Before(started) || at.After(*settledAt)) {
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
	if cid > 0 && strings.TrimSpace(body) != "" && f.Settled && !at.Before(started) && !at.After(*settledAt) {
		version := gates.Digest(struct {
			ID   int64
			Body string
			At   time.Time
		}{cid, body, at})
		f.Report = &gates.Evidence{Kind: "report", ID: strconv.FormatInt(cid, 10), Version: version, Subject: subject, Digest: version}
	}
	f.ReceiptUnversioned = o.ClosedAt != nil
	var eid int64
	if f.Settled && f.Report != nil {
		f.Receipt, eid, err = captureProductReceipt(ctx, q, id, o.ClosedAt, *settledAt, at, *f.Report)
		if err != nil {
			return f, "", err
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
	}{id, f.Applicable, f.Complete, f.Running, f.Settled, f.UnsupportedReason, f.IncompleteReason, gates.RegistryDigest(), state, runID, outcome, started, finished, o.CompletedAt, o.ClosedAt, o.PendingQuestionCommentID, f.Report, f.Receipt, lastComment, eid, noWork})

	return f, state, nil
}

// captureProductReceipt reads the existing close event without conferring any
// approval or closure authority. Legacy events do not name a product version;
// strict ordering is the conservative shadow proof for both reports and typed
// no-work finishes. Equal timestamps and unverifiable people remain unknown.
func captureProductReceipt(ctx context.Context, q querier, id string, closedAt *time.Time, settledAt, producedAt time.Time, product gates.Evidence) (*gates.Evidence, int64, error) {
	if closedAt == nil {
		return nil, 0, nil
	}
	var event int64
	var kind, actor, source string
	var closed time.Time
	err := q.QueryRowContext(ctx, "SELECT `id`,`actor_kind`,`actor_id`,`actor_source`,`at` FROM `task_event` WHERE `task_id` = ? AND `kind` = 'closed' AND `at` = ? ORDER BY `id` DESC LIMIT 1", id, *closedAt).Scan(&event, &kind, &actor, &source, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	p := Principal{Kind: PrincipalKind(kind), ID: actor, Source: AuthSource(source)}
	if strings.TrimSpace(actor) == "" || p.Kind != PrincipalHuman || !p.Verified() || !closed.After(producedAt) || !closed.After(settledAt) {
		return nil, event, nil
	}
	version := gates.Digest(struct {
		Event   int64
		Product string
		At      time.Time
	}{event, product.Version, closed})
	return &gates.Evidence{Kind: "acknowledgment", ID: strconv.FormatInt(event, 10), Version: version, Subject: product.Subject, Digest: version}, event, nil
}

// beginGateObservationRead opens a snapshot read-only transaction that takes
// neither SQLite's immediate write lock nor Postgres's advisory writer lock.
// On Postgres, REPEATABLE READ pins a single MVCC snapshot across the fact and
// shadow-row reads so concurrent commits cannot mix old facts with a newer
// cache row; SQLite WAL's deferred BEGIN already pins one snapshot at the first
// read.
func (s *Store) beginGateObservationRead(ctx context.Context) (*sql.Tx, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if IsPostgresDB(s.db) {
		opts.Isolation = sql.LevelRepeatableRead
	}
	return s.db.BeginTx(ctx, opts)
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
func (s *Store) ObserveTaskGates(ctx context.Context, id string, now time.Time, selected ...gates.ChildrenConfig) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	return s.observeTaskGates(ctx, id, now, true, "", nil, gates.QualificationCollection{}, selected...)
}

// ObserveTaskGatesWithChecks accepts scoped external CI reads alongside the
// ordinary transactional report and review observations.
func (s *Store) ObserveTaskGatesWithChecks(ctx context.Context, id string, now time.Time, fence string, checks []gates.ChecksFacts, selected ...gates.ChildrenConfig) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	return s.observeTaskGates(ctx, id, now, true, fence, checks, gates.QualificationCollection{}, selected...)
}

// ObserveTaskGatePredictions omits transitive history for scoped API callers.
func (s *Store) ObserveTaskGatePredictions(ctx context.Context, id string, now time.Time, selected ...gates.ChildrenConfig) (*gates.ObservationView, error) {
	return s.ObserveTaskGatePredictionsWithChecks(ctx, id, now, "", nil, selected...)
}

func (s *Store) ObserveTaskGatePredictionsWithChecks(ctx context.Context, id string, now time.Time, fence string, checks []gates.ChecksFacts, selected ...gates.ChildrenConfig) (*gates.ObservationView, error) {
	v, _, err := s.observeTaskGates(ctx, id, now, false, fence, checks, gates.QualificationCollection{}, selected...)
	return v, err
}

var ErrGateObservationScopeDenied = errors.New("gate observation task repository is outside the authorized read scope")

func (s *Store) ObserveTaskGatesWithQualifications(ctx context.Context, id string, now time.Time, c gates.QualificationCollection, selected ...gates.ChildrenConfig) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	return s.observeTaskGates(ctx, id, now, true, "", nil, c, selected...)
}
func (s *Store) ObserveTaskGatePredictionsWithQualifications(ctx context.Context, id string, now time.Time, c gates.QualificationCollection, selected ...gates.ChildrenConfig) (*gates.ObservationView, error) {
	v, _, err := s.observeTaskGates(ctx, id, now, false, "", nil, c, selected...)
	return v, err
}

// ObserveTaskGatesWithEvidence combines independent scoped source collections without replacing any gate family.
func (s *Store) ObserveTaskGatesWithEvidence(ctx context.Context, id string, now time.Time, fence string, checks []gates.ChecksFacts, c gates.QualificationCollection, selected ...gates.ChildrenConfig) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	return s.observeTaskGates(ctx, id, now, true, fence, checks, c, selected...)
}
func (s *Store) ObserveTaskGatePredictionsWithEvidence(ctx context.Context, id string, now time.Time, fence string, checks []gates.ChecksFacts, c gates.QualificationCollection, selected ...gates.ChildrenConfig) (*gates.ObservationView, error) {
	v, _, err := s.observeTaskGates(ctx, id, now, false, fence, checks, c, selected...)
	return v, err
}

type gateObservationPrep struct {
	view             gates.ObservationView
	diag             GateObservationDiagnostics
	resolved         bool
	transient        bool
	f                gates.Facts
	state            string
	bindings         []gates.Binding
	qualifications   gates.QualificationCollection
	previous         string
	source           string
	generation       uint64
	evaluations      uint64
	started          time.Time
	evaluated        time.Time
	historyAt        time.Time
	missing          bool
	unsupportedCache bool
	bindingsChanged  bool
	now              time.Time
}

func (s *Store) tryReadGateObservation(ctx context.Context, id string, now time.Time, history bool, access GateHealthAccess, fence string, checks []gates.ChecksFacts, qualifications gates.QualificationCollection, children []gates.ChildrenConfig) (gateObservationPrep, error) {
	tx, err := s.beginGateObservationRead(ctx)
	if err != nil {
		return gateObservationPrep{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return prepareGateObservation(ctx, tx, id, now, history, access, fence, checks, qualifications, children, true)
}

func prepareGateObservation(ctx context.Context, q querier, id string, now time.Time, history bool, access GateHealthAccess, fence string, checks []gates.ChecksFacts, qualifications gates.QualificationCollection, children []gates.ChildrenConfig, readOnly bool) (gateObservationPrep, error) {
	var zero gateObservationPrep
	if err := authorizeObservationTask(ctx, q, id); err != nil {
		return zero, err
	}
	goal, err := gateTaskGoal(ctx, q, id)
	if err != nil {
		return zero, err
	}
	selected, err := gateFailureBindingsWithGoal(ctx, q, id, goal, access)
	if err != nil {
		return zero, err
	}
	healthProjection := len(selected) > 0 && selected[0].Subject.Kind == "repository"
	canUseCache := true
	for _, b := range selected {
		canUseCache = canUseCache && healthBindingAllowed(b, access)
	}
	var previous, source string
	var generation, evaluations uint64
	var started, evaluated, historyAt time.Time
	shadowErr := q.QueryRowContext(ctx, "SELECT `generation`,`source`,`view_json`,`started_at`,`evaluated_at`,`history_at`,`evaluations` FROM `gate_observation_shadow` WHERE `task_id` = ?", id).Scan(&generation, &source, &previous, &started, &evaluated, &historyAt, &evaluations)
	missing := errors.Is(shadowErr, sql.ErrNoRows)
	if shadowErr != nil && !missing {
		return zero, shadowErr
	}
	// Persisted error backoff survives restarts and coalesces failed
	// refreshes. It expires only into another diagnostic collection.
	var cachedView gates.ObservationView
	var cachedViewOK bool
	if !missing {
		if json.Unmarshal([]byte(previous), &cachedView) == nil {
			cachedViewOK = true
			if canUseCache && cachedView.Dirty && currentShadowBindings(cachedView, id) && sameObservationFamily(cachedView, selected, healthProjection) && len(cachedView.Results) > 0 && cachedView.Results[0].RetryAt != nil && now.Before(*cachedView.Results[0].RetryAt) {
				out := gateObservationPrep{view: cachedView, resolved: true}
				if history {
					out.diag, err = readGateDiagnosticsFrom(ctx, q, id, started, evaluations)
					if err != nil {
						return zero, err
					}
				}
				return out, nil
			}
		}
	}
	// When the read-only pre-check can already tell that no read-only cache
	// path can succeed (missing/corrupt/unsupported/dirty or expired outside
	// a late-read window), fall through immediately to the writer transaction
	// without capturing facts twice.
	if readOnly && !healthProjection {
		if missing || !cachedViewOK {
			return zero, nil
		}
		if !currentShadowBindings(cachedView, id) || cachedView.Dirty {
			return zero, nil
		}
		effectiveEvaluated := evaluated
		if cachedView.ComparedAt.After(effectiveEvaluated) {
			effectiveEvaluated = cachedView.ComparedAt
		}
		if !now.Before(effectiveEvaluated) {
			if !now.Before(effectiveEvaluated.Add(time.Minute)) || !freshQualificationCache(cachedView, now) || !freshChecks(checks, now) || !freshObservationView(cachedView, now) {
				return zero, nil
			}
		}
	}
	// Reuse ordinary rows within this transaction across local facts
	// and CI acceptance. Rehydrating the same task/links/grants and
	// observation twice wastes the diagnostic budget under the race
	// detector; none of the policy/fence queries are skipped.
	var task *Task
	var observation *Observation
	if !healthProjection {
		task, err = getTask(ctx, q, id)
		if err != nil {
			return zero, err
		}
		if task == nil {
			return zero, sql.ErrNoRows
		}
		observation, err = getObservation(ctx, q, id)
		if err != nil {
			return zero, err
		}
	}
	sourceCtx := context.WithValue(ctx, qualificationBindingReserveKey{}, len(qualifications.Bindings(id))-2+2*len(checks))
	snap := &gateTaskSnapshot{goalLoaded: true, goal: goal}
	f, state, err := captureGateFacts(sourceCtx, q, id, now, access, task, observation, snap, children...)
	if err != nil {
		return zero, err
	}
	// Keep the transactional local fence independently of external reads.
	// The report dependency persists it without changing the wire schema.
	f.LocalSourceVersion = f.SourceVersion
	if f.Health != nil && !f.Complete {
		view := gates.ObservationView{APIVersion: "grain.gates/v1", Mode: "observation_only", Authority: "legacy", SourceRevision: f.SourceVersion, ComparedAt: now, LegacyState: state, Bindings: observationBindings(id, f), Results: []gates.EvaluationState{}}
		for _, b := range view.Bindings {
			r := gates.Evaluate(b, f, now, 0)
			view.Results = append(view.Results, gates.EvaluationState{EvaluationID: r.EvaluationID, Disposition: "rejected", Result: r})
		}
		return gateObservationPrep{view: view, resolved: true, transient: true}, nil
	}
	if f.Health != nil {
		qualifications = gates.QualificationCollection{}
	}
	invalidated := false
	if qualifications.QA != nil || qualifications.Release != nil {
		if (qualifications.QA != nil && qualifications.QA.TaskID != id) || (qualifications.Release != nil && qualifications.Release.TaskID != id) {
			return zero, fmt.Errorf("qualification source does not match task")
		}
		qualFence, fenceErr := qualificationObservationFence(ctx, q, id)
		if fenceErr != nil {
			return zero, fenceErr
		}
		if qualifications.StoreFence == "" || qualFence != qualifications.StoreFence {
			invalidated = true
			qualifications = qualifications.Invalidate("Task, assignment or policy changed before diagnostic persistence; recollect required.")
		}
		f.SourceVersion = gates.Digest(struct{ Report, Qualifications string }{f.SourceVersion, qualifications.SourceVersionAt(now)})
	}
	if len(checks) > 0 && f.Health == nil {
		task, _, current, err := gateChecksInputFrom(ctx, q, task, observation)
		if err != nil {
			return zero, err
		}
		subjects := CheckSubjects(*task)
		if len(checks) > 8 || len(subjects) != len(checks) {
			return zero, fmt.Errorf("incomplete change collection")
		}
		seen := map[gates.Subject]bool{}
		for _, c := range checks {
			if c.TaskID != id || !slices.Contains(subjects, c.Subject) || seen[c.Subject] {
				return zero, fmt.Errorf("foreign checks subject")
			}
			seen[c.Subject] = true
			if c.Subject.ChangeKind == "pr" {
				for _, src := range c.Sources {
					if src.Kind != "github" {
						continue
					}
					repo, e := ParseRepo(c.Subject.Repository)
					if e != nil {
						return zero, e
					}
					known, e := recordedGateChecks(ctx, q, repo, c.Head)
					if e != nil {
						return zero, e
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
	if missing {
		if readOnly {
			return zero, nil
		}
		generation, started, err = gateHistoryFloor(ctx, q, id, now)
		if err != nil {
			return zero, err
		}
		evaluations = generation
	}
	bindings := append(observationBindings(id, f, len(qualifications.Bindings(id))-2), qualifications.Bindings(id)[2:]...)
	unsupportedCache, rejectedCache, bindingsChanged, expiredCache := false, false, false, false
	qualificationsFresh := true
	if !missing {
		if !cachedViewOK {
			if err = json.Unmarshal([]byte(previous), &cachedView); err != nil {
				return zero, err
			}
		}
		qualificationsFresh = freshQualificationCache(cachedView, now)
		unsupportedCache = !currentShadowBindings(cachedView, id)
		// Postgres timestamp columns truncate to microseconds. The JSON
		// record preserves the original request time on both engines.
		if !unsupportedCache && cachedView.ComparedAt.After(evaluated) {
			evaluated = cachedView.ComparedAt
		}
		rejectedCache = cachedView.Dirty
		for _, entry := range cachedView.Results {
			if entry.Result.Status != "unknown" && !now.Before(entry.Result.ValidUntil) {
				expiredCache = true
			}
		}
		bindingsChanged = gates.Digest(cachedView.Bindings) != gates.Digest(bindings)
		// Remote reads start outside this transaction. A late older
		// request must never rewind newer accepted evidence, including
		// across a restart. A changed local fence still invalidates the
		// result rather than returning a possibly stale cached success.
		if now.Before(evaluated) {
			sameLocalSource := source == f.SourceVersion
			if f.Health == nil {
				sameLocalSource = len(cachedView.Results) > 0 && len(cachedView.Results[0].Result.Dependencies) > 0 &&
					cachedView.Results[0].Result.Dependencies[0].Version == f.LocalSourceVersion
			}
			if !invalidated && !unsupportedCache && !rejectedCache && !bindingsChanged && sameLocalSource {
				out := gateObservationPrep{view: cachedView, resolved: true}
				if history {
					out.diag, err = readGateDiagnosticsFrom(ctx, q, id, started, evaluations)
					if err != nil {
						return zero, err
					}
				}
				return out, nil
			}
			if readOnly {
				return zero, nil
			}
			now = evaluated
			if f.Health != nil {
				// The external read may have started before this task
				// acquired a health goal. Recollect local health facts
				// at the newer clock; never replay a changed source or
				// rewind its accepted timestamp with the old CI request.
				f, state, err = captureGateFacts(ctx, q, id, now, access, nil, nil, &gateTaskSnapshot{goalLoaded: true, goal: goal}, children...)
				if err != nil {
					return zero, err
				}
				f.LocalSourceVersion = f.SourceVersion
				bindings = observationBindings(id, f)
			} else if len(f.Checks) > 0 || qualifications.QA != nil || qualifications.Release != nil {
				// Local facts must refresh, but an older collection cannot
				// replace newer external evidence with an old positive head.
				const reason = "Local observation context changed after this older collection started; recollect external evidence."
				for i := range f.Checks {
					f.Checks[i].PolicyComplete = false
					f.Checks[i].Gap = reason
				}
				qualifications = qualifications.Invalidate(reason)
				versions := []string{f.SourceVersion, qualifications.SourceVersionAt(now)}
				for _, c := range f.Checks {
					versions = append(versions, gates.ChecksVersion(c))
				}
				f.SourceVersion = gates.Digest(versions)
			}
		}
	}
	changed := missing || source != f.SourceVersion || bindingsChanged
	if !changed && !unsupportedCache && !rejectedCache && !expiredCache && now.Before(evaluated.Add(time.Minute)) && qualificationsFresh && freshChecks(f.Checks, now) && freshObservationView(cachedView, now) {
		out := gateObservationPrep{view: cachedView, resolved: true}
		if history {
			out.diag, err = readGateDiagnosticsFrom(ctx, q, id, started, evaluations)
			if err != nil {
				return zero, err
			}
		}
		return out, nil
	}
	if readOnly {
		return zero, nil
	}
	return gateObservationPrep{
		f:                f,
		state:            state,
		bindings:         bindings,
		qualifications:   qualifications,
		previous:         previous,
		source:           source,
		generation:       generation,
		evaluations:      evaluations,
		started:          started,
		evaluated:        evaluated,
		historyAt:        historyAt,
		missing:          missing,
		unsupportedCache: unsupportedCache,
		bindingsChanged:  bindingsChanged,
		now:              now,
	}, nil
}

func (s *Store) observeTaskGates(ctx context.Context, id string, now time.Time, history bool, fence string, checks []gates.ChecksFacts, qualifications gates.QualificationCollection, children ...gates.ChildrenConfig) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	access := gateHealthAccessFrom(ctx)
	// Fast read-only pre-check: steady-state unchanged reads (plus persisted
	// error backoff, access-limited transient health projections, and late
	// older reads) write nothing, so serve them from a read-only snapshot
	// transaction without taking SQLite's write lock or Postgres's advisory
	// writer lock. Any cache miss, fence change, or expired lease falls through
	// to the serialized writer transaction below.
	if prep, err := s.tryReadGateObservation(ctx, id, now, history, access, fence, checks, qualifications, children); err == nil && prep.resolved {
		if prep.transient || !history {
			return &prep.view, nil, nil
		}
		filterHealthDiagnostics(&prep.diag, access)
		return &prep.view, &prep.diag, nil
	} else if err != nil && !isSerializationFailure(err) {
		return nil, nil, err
	}

	transient := false
	var view gates.ObservationView
	var diag GateObservationDiagnostics
	var last error
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		tx, release, err := s.beginGateObservation(ctx)
		if err != nil {
			return nil, nil, err
		}
		transient = false
		err = func() error {
			prep, err := prepareGateObservation(ctx, tx, id, now, history, access, fence, checks, qualifications, children, false)
			if err != nil {
				return err
			}
			if prep.resolved {
				view = prep.view
				diag = prep.diag
				transient = prep.transient
				return nil
			}
			f := prep.f
			state := prep.state
			bindings := prep.bindings
			quals := prep.qualifications
			previous := prep.previous
			generation := prep.generation
			evaluations := prep.evaluations
			started := prep.started
			historyAt := prep.historyAt
			unsupportedCache := prep.unsupportedCache
			changed := prep.missing || prep.source != f.SourceVersion || prep.bindingsChanged
			evalNow := prep.now

			generation++
			evaluations++
			view = gates.ObservationView{APIVersion: "grain.gates/v1", Mode: "observation_only", Authority: "legacy", SourceRevision: f.SourceVersion, ComparedAt: evalNow, LegacyState: state, Bindings: bindings, Results: []gates.EvaluationState{}}
			for _, b := range view.Bindings {
				r := gates.Evaluate(b, f, evalNow, generation)
				if b.Type == "qa_stage" || b.Type == "release_qualification" {
					r = quals.Evaluate(b, evalNow, generation)
				}
				if unsupportedCache {
					r.Status = "unknown"
					r.Error = nil
					r.Summary = "Cached gate configuration or implementation is unsupported; the observation profile was regenerated."
					r.HumanText = nil
					r.AgentText = nil
					r.Waits = []gates.Wait{}
				}
				// IDs are namespaced diagnostics, never accepted by an authority API.
				r.EvaluationID = fmt.Sprintf("shadow-%s-%d-%s", id, generation, b.ID)
				entry := gates.EvaluationState{EvaluationID: r.EvaluationID, Revision: generation, Disposition: "accepted", Result: r, AcceptedAt: &evalNow, DirtyGeneration: generation, CoveredGeneration: generation}
				if r.Status == "error" {
					// A persisted diagnostic error is not an accepted
					// predicate. Reuse this source lease as its read-driven
					// retry boundary; changed source evidence refreshes sooner.
					entry.Disposition = "rejected"
					entry.AcceptedAt = nil
					retry := r.ValidUntil
					entry.RetryAt = &retry
					entry.CoveredGeneration = generation - 1
				}
				view.Results = append(view.Results, entry)
			}
			gates.BoundQualificationView(&view, evalNow)
			data, err := json.Marshal(view)
			if err != nil {
				return err
			}
			if len(data) > 256*1024 {
				return fmt.Errorf("observation view exceeds diagnostic byte bound")
			}
			if changed || unsupportedCache || !evalNow.Before(historyAt.Add(time.Hour)) || f.Health != nil && healthPredictionsChanged(previous, view) {
				comparison := compareReportMilestones(f, view)
				if quals.QA != nil || quals.Release != nil {
					comparison = "qualification_observations_no_independent_parity_oracle"
				}
				if f.Health != nil {
					comparison = compareHealthPredictions(view)
				}
				if unsupportedCache {
					comparison = "unknown_unsupported_config"
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO `gate_observation_shadow_history` (`task_id`,`generation`,`at`,`view_json`,`comparison`) VALUES (?,?,?,?,?)", id, generation, evalNow, string(data), comparison); err != nil {
					return err
				}
				historyAt = evalNow
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO `gate_observation_shadow` (`task_id`,`generation`,`source`,`view_json`,`started_at`,`evaluated_at`,`history_at`,`evaluations`) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT (`task_id`) DO UPDATE SET `generation`=excluded.`generation`,`source`=excluded.`source`,`view_json`=excluded.`view_json`,`evaluated_at`=excluded.`evaluated_at`,`history_at`=excluded.`history_at`,`evaluations`=excluded.`evaluations`", id, generation, f.SourceVersion, string(data), started, evalNow, historyAt, evaluations)
			if err != nil {
				return err
			}
			if err = pruneGateObservations(ctx, tx, id, evalNow); err != nil {
				return err
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
			if transient || !history {
				return &view, nil, nil
			}
			filterHealthDiagnostics(&diag, access)
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
	labels := []string{compareOutcomeMilestones(f, v)}
	byID := map[string]string{}
	for _, entry := range v.Results {
		byID[entry.Result.BindingID] = entry.Result.Status
	}
	for _, c := range f.Checks {
		current, legacy := gates.ChecksBinding(c, false), gates.ChecksBinding(c, true)
		a, b := byID["task/"+f.TaskID+"/"+current.ID], byID["task/"+f.TaskID+"/"+legacy.ID]
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

func compareOutcomeMilestones(f gates.Facts, v gates.ObservationView) string {
	hasOutcomes, successContract := false, false
	for i, binding := range v.Bindings {
		if binding.Type != "integration" && binding.Type != "children" {
			continue
		}
		hasOutcomes = true
		predicted := v.Results[i].Result.Status
		if predicted == "unknown" || predicted == "error" {
			return "outcome_evidence_gap"
		}
		if legacy, ok := f.LegacyMet[binding.ID]; ok {
			if (predicted == "met") != legacy {
				return "outcome_discrepancy"
			}
		} else {
			successContract = true
		}
	}
	if hasOutcomes {
		if successContract {
			return "explicit_success_contract_only"
		}
		return "equivalent_outcome_milestones"
	}
	if !f.Applicable {
		return "not_applicable"
	}
	if f.Product != "" && f.Product != "report" {
		return "observed_" + f.Product + "_milestone"
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
	for _, entry := range v.Results {
		if entry.Result.Type == "integration" || entry.Result.Type == "children" {
			return "explicit_outcome_predictions"
		}
	}
	return "equivalent_milestones"
}

// RecordGateObservationFailure retains safe rejected diagnostics with bounded
// exponential retry. No raw infrastructure errors or evidence bodies are stored.
// This is controller state, never evaluator-private durable state.
func (s *Store) RecordGateObservationFailure(ctx context.Context, id string, now time.Time, view *gates.ObservationView, healthAccess ...GateHealthAccess) error {
	return s.RecordGateQualificationFailure(ctx, id, now, view, gates.QualificationCollection{}, healthAccess...)
}
func (s *Store) RecordGateQualificationFailure(ctx context.Context, id string, now time.Time, view *gates.ObservationView, qualifications gates.QualificationCollection, healthAccess ...GateHealthAccess) error {
	tx, release, err := s.beginGateObservation(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); release() }()
	access := gateHealthAccessFrom(ctx)
	if len(healthAccess) > 0 {
		access = healthAccess[0]
	}
	if err := authorizeObservationTask(ctx, tx, id); err != nil {
		return err
	}
	selected, err := gateFailureBindings(ctx, tx, id, access)
	if err != nil {
		return err
	}
	healthProjection := len(selected) > 0 && selected[0].Subject.Kind == "repository"
	for _, b := range selected {
		if !healthBindingAllowed(b, access) {
			*view = gates.UnavailableObservationForBindings(id, now, selected)
			return nil
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
		if json.Unmarshal([]byte(raw), &old) == nil && currentShadowBindings(old, id) && sameObservationFamily(old, selected, healthProjection) {
			if old.ComparedAt.After(evaluated) {
				evaluated = old.ComparedAt
			}
			// now is the collection's start, not its failure completion.
			// A slower failed request cannot replace a newer accepted read;
			// bursts of failures share one generation until its retry is due.
			first := old.Results[0] // currentShadowBindings validated a nonempty safe projection
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
	if healthProjection {
		*view = gates.UnavailableObservationForBindings(id, now, selected)
	} else {
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
			if json.Unmarshal([]byte(raw), &previous) == nil && currentShadowBindings(previous, id) && sameObservationFamily(previous, selected, false) {
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
		bindings := append(gates.BindingsFor(id, unavailableChecks), qualifications.Bindings(id)[2:]...)
		known := map[string]bool{}
		for _, b := range bindings {
			known[b.ID] = true
		}
		if raw != "" {
			var previous gates.ObservationView
			if json.Unmarshal([]byte(raw), &previous) == nil && currentShadowBindings(previous, id) && sameObservationFamily(previous, selected, false) {
				for _, b := range previous.Bindings[2:] {
					if b.Type != "checks" && !known[b.ID] {
						known[b.ID] = true
						bindings = append(bindings, b)
					}
				}
			}
		}
		if len(bindings) > 64 {
			bindings = bindings[:64]
		}
		*view = gates.UnavailableObservationForBindings(id, now, bindings)
	}

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
	var started time.Time
	var evaluations uint64
	if err := q.QueryRowContext(ctx, "SELECT `started_at`,`evaluations` FROM `gate_observation_shadow` WHERE `task_id` = ?", id).Scan(&started, &evaluations); err != nil {
		return GateObservationDiagnostics{Counts: map[string]int{}, History: []GateObservationSample{}}, err
	}
	return readGateDiagnosticsFrom(ctx, q, id, started, evaluations)
}

func readGateDiagnosticsFrom(ctx context.Context, q querier, id string, started time.Time, evaluations uint64) (GateObservationDiagnostics, error) {
	diag := GateObservationDiagnostics{StartedAt: started, Evaluations: evaluations, Counts: map[string]int{}, History: []GateObservationSample{}}

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
		for _, family := range strings.Split(label, ";") {
			diag.Counts[family] += n
		}
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
	readBytes := 0
	configs := map[shadowHistoryConfig]bool{}
	for rows.Next() {
		var sample GateObservationSample
		var raw string
		if err = rows.Scan(&sample.Generation, &sample.At, &raw, &sample.Comparison); err != nil {
			return diag, err
		}
		// Keep the newest complete sample even if it alone exceeds the read
		// budget. Older samples stay persisted and included in counts. Bound
		// decoding as well as row count so larger observer families cannot
		// spend the entire local collection budget on diagnostic history.
		if readBytes > 0 && readBytes+len(raw) > 64*1024 {
			break
		}
		readBytes += len(raw)
		// History is diagnostic input too. A restored or older row must not
		// project effectful authority or outputs that current replay rejects.
		// Keep the stored row for inspection/retention, expose the gap, and
		// preserve safe old implementation pins without treating them as current.
		if len(raw) > 256*1024 || json.Unmarshal([]byte(raw), &sample.View) != nil || !inertShadowObservation(sample.View) || !validShadowHistoryConfigs(sample.View, configs) {
			for _, family := range strings.Split(sample.Comparison, ";") {
				if diag.Counts[family] > 1 {
					diag.Counts[family]--
				} else {
					delete(diag.Counts, family)
				}
			}
			diag.Counts["unknown_invalid_history"]++
			continue
		}
		diag.History = append(diag.History, sample)
	}
	return diag, rows.Err()
}

// Config validation is shared only within this bounded history page. Most
// samples repeat the same immutable v1 configuration; validating it once avoids
// multiplying parser work by history length. Old build pins remain inspectable,
// while unregistered types, future versions and open/invalid profiles do not.
type shadowHistoryConfig struct {
	typ     string
	version uint32
	raw     string
}

func validShadowHistoryConfigs(v gates.ObservationView, configs map[shadowHistoryConfig]bool) bool {
	for i := range v.Bindings {
		b := &v.Bindings[i]
		key := shadowHistoryConfig{b.Type, b.Version, string(b.Config)}
		valid, known := configs[key]
		if !known {
			_, err := gates.Validate(b.Type, b.Version, b.Config)
			valid = err == nil
			configs[key] = valid
		}
		if !valid {
			return false
		}
	}
	return true
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

// The inert envelope applies to current and retained views. It intentionally
// permits safe older implementation pins in history; current replay additionally
// checks the supported registry/configuration below.
func inertShadowObservation(v gates.ObservationView) bool {
	if v.APIVersion != "grain.gates/v1" || v.Mode != "observation_only" || v.Authority != "legacy" || v.LifecycleEffects || v.ActionEffects || v.DispatchEffects || v.AutomaticCutover || len(v.Bindings) == 0 || len(v.Bindings) > 64 || len(v.Results) != len(v.Bindings) {
		return false
	}
	for i, b := range v.Bindings {
		r := v.Results[i].Result
		if r.Status == "error" && (v.Results[i].Disposition != "rejected" || v.Results[i].AcceptedAt != nil) {
			return false
		}
		if b.Purpose != "monitor" || b.Required || b.Action != nil || r.Type != b.Type || r.Version != b.Version || len(r.Work) > 0 || len(r.Actions) > 0 {
			return false
		}
	}
	return true
}

// Cached diagnostics cannot smuggle an unregistered profile or old build into
// a current accepted observation. Only this fixed monitor projection is valid.
func currentShadowBindings(v gates.ObservationView, id string) bool {
	if !inertShadowObservation(v) {
		return false
	}
	if len(v.Bindings) > 0 && v.Bindings[0].Subject.Kind == "repository" {
		if len(v.Bindings) < 2 || len(v.Bindings) > 4 || len(v.Results) != len(v.Bindings) {
			return false
		}
		seen := map[string]bool{}
		for i, b := range v.Bindings {
			if seen[b.ID] || b.Purpose != "monitor" || b.Required || b.Action != nil || b.Origin.ID != id || b.Origin.Revision == "" || b.Subject.Kind != "repository" || b.Subject.Repository == "" || b.Subject.Ref == "" || b.Subject.TaskID != "" || b.Subject.Authority != "" || b.Subject.ChangeKind != "" || b.Subject.ID != "" {
				return false
			}
			seen[b.ID] = true
			if b.Type != "base_health" && b.Type != "nightly_health" && b.Type != "publishing_health" && b.Type != "pipeline_health" {
				return false
			}
			if _, err := gates.Validate(b.Type, b.Version, b.Config); err != nil {
				return false
			}
			r := v.Results[i].Result
			if r.Type != b.Type || r.Version != b.Version || len(r.Work) > 0 || len(r.Actions) > 0 {
				return false
			}
		}
		return true
	}
	base := gates.Bindings(id)
	if len(v.Bindings) < 2 || len(v.Bindings) > 64 || len(v.Results) != len(v.Bindings) || gates.Digest(v.Bindings[:2]) != gates.Digest(base) {
		return false
	}
	seen := map[string]bool{}
	qaCount, releaseCount := 0, 0
	for i, b := range v.Bindings {
		if seen[b.ID] || b.Purpose != "monitor" || b.Required || b.Action != nil || b.Origin.ID != id || b.Origin.Revision != gates.Implementation {
			return false
		}
		seen[b.ID] = true
		if i >= 2 {
			switch b.Type {
			case "review", "approval":
				if b.Subject.Kind != "change" || b.Subject.TaskID != "" || b.Subject.Authority == "" || b.Subject.Repository == "" || b.Subject.ChangeKind != "pr" || b.Subject.ID == "" {
					return false
				}
			case "qa_stage":
				var cfg gates.QAStageConfig
				if json.Unmarshal(b.Config, &cfg) != nil || b.ID != "qa-"+cfg.Stage || b.Subject.Kind != "task" || b.Subject.TaskID != id {
					return false
				}
				qaCount++
			case "release_qualification":
				var cfg gates.ReleaseQualificationConfig
				if json.Unmarshal(b.Config, &cfg) != nil || b.ID != "release-"+cfg.Predicate || b.Subject.Kind != "repository" || b.Subject.Repository == "" || b.Subject.Ref == "" {
					return false
				}
				releaseCount++
			case "checks":
				var config gates.ChecksConfig
				if json.Unmarshal(b.Config, &config) != nil {
					return false
				}
				expected := gates.ChecksBinding(gates.ChecksFacts{TaskID: id, Subject: b.Subject}, config.Semantics == "legacy_ci_v1")
				if b.Subject.Kind != "change" || gates.Digest(b) != gates.Digest(expected) {
					return false
				}
			case "delivery", "acknowledgment", "integration", "children":
			default:
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
		if v.Dirty && (v.Results[i].Disposition != "rejected" || r.Status != "error" || r.Error == nil || len(r.Evidence) != 0 || len(r.Waits) != 0) {
			return false
		}
		if r.Type != b.Type || r.Version != b.Version || r.BindingID != "task/"+id+"/"+b.ID || len(r.Work) > 0 || len(r.Actions) > 0 {
			return false
		}
	}
	return (qaCount == 0 || qaCount == 3) && (releaseCount == 0 || releaseCount == 8)
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

// Family selection precedes failure replay: an old health cohort cannot replace
// a new report/change projection, or vice versa.
func sameObservationFamily(v gates.ObservationView, selected []gates.Binding, health bool) bool {
	if len(v.Bindings) == 0 {
		return false
	}
	if health {
		return gates.Digest(v.Bindings) == gates.Digest(selected)
	}
	return v.Bindings[0].Subject.Kind == "task"
}
func authorizeObservationTask(ctx context.Context, q querier, id string) error {
	if !HasExternalRefReadable(ctx) {
		return nil
	}
	var owner, name string
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(`target_owner`,''),COALESCE(`target_name`,'') FROM `task` WHERE `id` = ?", id).Scan(&owner, &name); err != nil {
		return err
	}
	repo := ""
	if owner != "" || name != "" {
		repo = RepoFromStored(owner, name).String()
	}
	if !ExternalRefReadable(ctx, repo) {
		return ErrGateObservationScopeDenied
	}
	return nil
}

func freshChecks(checks []gates.ChecksFacts, now time.Time) bool {
	for _, c := range checks {
		if !now.Before(c.ValidUntil) {
			return false
		}
	}
	return true
}

func freshObservationView(v gates.ObservationView, now time.Time) bool {
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
func (s *Store) GateObservationRetry(ctx context.Context, id string, now time.Time, history bool, healthAccess ...GateHealthAccess) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	if err := authorizeObservationTask(ctx, s.db, id); err != nil {
		return nil, nil, err
	}
	goal, err := gateTaskGoal(ctx, s.db, id)
	if err != nil {
		return nil, nil, err
	}
	if healthGoal(goal) {
		return nil, nil, nil
	}
	var raw string
	var owner, name string
	err = s.db.QueryRowContext(ctx, "SELECT g.`view_json`, COALESCE(t.`target_owner`,''), COALESCE(t.`target_name`,'') FROM `gate_observation_shadow` g JOIN `task` t ON g.`task_id` = t.`id` WHERE g.`task_id` = ?", id).Scan(&raw, &owner, &name)
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
	if json.Unmarshal([]byte(raw), &v) != nil || !currentShadowBindings(v, id) || !sameObservationFamily(v, gates.Bindings(id), false) || !v.Dirty || len(v.Results) == 0 || v.Results[0].RetryAt == nil || !now.Before(*v.Results[0].RetryAt) {
		return nil, nil, nil
	}
	if !history {
		return &v, nil, nil
	}
	d, err := readGateDiagnostics(ctx, s.db, id)
	if err != nil {
		return nil, nil, err
	}
	access := gateHealthAccessFrom(ctx)
	if len(healthAccess) > 0 {
		access = healthAccess[0]
	}
	filterHealthDiagnostics(&d, access)
	return &v, &d, nil
}

func freshQualificationCache(v gates.ObservationView, now time.Time) bool {
	for _, state := range v.Results {
		if (state.Result.Type == "qa_stage" || state.Result.Type == "release_qualification") && !now.Before(state.Result.ValidUntil) {
			return false
		}
	}
	return true
}

// Retain the task observer's backoff name with current health-aware replay.
func (s *Store) GateObservationBackoff(ctx context.Context, id string, now time.Time, history bool) (*gates.ObservationView, *GateObservationDiagnostics, error) {
	return s.GateObservationRetry(ctx, id, now, history)
}
