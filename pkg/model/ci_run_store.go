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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Grain CI's runs, as rows: written by ci.NewStoredRunStore as a run is
// planned, as each of its jobs finishes and once it concludes, and read
// back by `grain ci show`, the CI page and orchestrator.collectCIRun --
// the last of which asks about runs that are, by then, routinely older
// than the daemon.
//
// A run is rewritten whole rather than patched column by column. There
// is one writer (the daemon that is running it), a run has a handful of
// jobs and a job a handful of steps, so the simple thing leaves no way
// for a run's rows to half-agree with each other.
//
// It is no longer true that a write happens only as a job finishes: a
// step that is running publishes what it has printed so far, so that a
// run can be watched rather than only read afterwards (ci.
// LiveOutputInterval, which is the pace, and says why it is the pace it
// is). That makes this the write path of a live log as well as of a
// result, and the reason the interval is measured in seconds rather than
// in lines.
//
// That one case has a write of its own, because it is the only one that
// repeats: PutCIStepOutput updates a single `output` cell, and
// CIStepOutputChange is how a caller holding both versions of a run finds
// out whether that narrow statement is exactly the write the whole-run
// one would have made. Everything else -- a job starting, a job
// finishing, a run concluding -- still goes through PutCIRun, so the
// invariant above is untouched: the narrow write is taken only when it
// leaves the same rows behind.

// The column lists, and the row each of them takes.
//
// ciRunValues, ciJobValues and ciStepValues are one row apiece, in the
// order the list beside them names. They are functions rather than
// argument lists written out at the call site so that the statements that
// write a run and the comparison that decides a run can be written
// narrowly (CIStepOutputChange) cannot drift apart: a column added to one
// is a column added to both, and a column somebody forgets to compare is
// not a column that can exist.

const ciRunColumns = "`id`,`run_key`,`repo`,`ref`,`commit_sha`,`event`,`requested_by`," +
	"`started_at`,`finished_at`,`conclusion`,`interrupted`,`error`,`cancel_reason`," +
	"`cancelled_by`,`build_revision`,`build_time`,`build_modified`,`recheck`"

const ciJobColumns = "`workflow`,`workflow_path`,`job_id`,`name`,`job_key`,`cached`,`sandbox`," +
	"`started_at`,`finished_at`,`conclusion`,`error`,`activity`,`activity_at`," +
	"`queued_ms`,`queued_cause`,`queued_reason`,`retry_count`,`retry_reason`," +
	"`flake`,`quarantined`,`quarantined_tests`,`status`,`warning`,`failure_summary`,`base_commit`,`attempts`"

const ciStepColumns = "`name`,`conclusion`,`exit_code`,`output`,`error`,`started_at`,`finished_at`"

func ciRunValues(run CIRun) []any {
	return []any{
		run.ID, run.Key, run.Repo, run.Ref, run.Commit, run.Event, run.RequestedBy,
		ciTime(run.StartedAt), ciTime(run.FinishedAt), run.Conclusion, run.Interrupted, run.Error,
		run.CancelReason, run.CancelledBy,
		run.Build, ciTime(run.BuildTime), run.BuildModified, run.Recheck,
	}
}

func ciJobValues(runID string, index int, job CIJob) []any {
	return []any{
		runID, index,
		job.Workflow, job.WorkflowPath, job.JobID, job.Name, job.Key, job.Cached, job.Sandbox,
		ciTime(job.StartedAt), ciTime(job.FinishedAt), job.Conclusion, job.Error,
		job.Activity, ciTime(job.ActivityAt),
		// Milliseconds: a wait is read in minutes and written in a column
		// somebody may well look at by hand, and nanoseconds would be
		// neither.
		job.Queued.Milliseconds(), job.QueuedCause, job.QueuedReason,
		job.RetryCount, job.RetryReason,
		job.Flake, job.Quarantined, ciNameList(job.QuarantinedTests), job.Status, job.Warning,
		job.FailureSummary, job.BaseCommit, job.Attempts,
	}
}

// ciNameList is a list of test names as the one TEXT column it is kept
// in: a JSON array, or nothing at all for no names, so a job that
// quarantined nothing has an empty column rather than "null" or "[]".
func ciNameList(names []string) string {
	if len(names) == 0 {
		return ""
	}
	b, err := json.Marshal(names)
	if err != nil {
		return ""
	}
	return string(b)
}

// ciNameListOf reads ciNameList back. A column that does not parse reads
// as no names rather than failing the whole run's read.
func ciNameListOf(s string) []string {
	if s == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(s), &names); err != nil {
		return nil
	}
	return names
}

func ciStepValues(runID string, jobIndex, stepIndex int, step CIStep) []any {
	return []any{
		runID, jobIndex, stepIndex,
		step.Name, step.Conclusion, step.ExitCode, step.Output, step.Error,
		ciTime(step.StartedAt), ciTime(step.FinishedAt),
	}
}

// valuePlaceholders is "?,?,?" for three values -- written from the row
// rather than counted by hand, for the same reason the rows above are
// functions.
func valuePlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(",?", n)[1:]
}

// PutCIRun records a run and everything under it, replacing whatever was
// stored for the same id.
//
// Whole-run replacement, in one transaction: the caller holds the run as
// one value and this is that value written down, so a reader never sees
// a run whose jobs are from one write and whose steps are from another.
func (s *Store) PutCIRun(ctx context.Context, run CIRun) error {
	return s.write(ctx, "put CI run "+run.ID, func(tx *sql.Tx) error {
		return putCIRun(ctx, tx, run)
	})
}

func putCIRun(ctx context.Context, tx *sql.Tx, run CIRun) error {
	if run.ID == "" {
		return errors.New("model: a CI run needs an id")
	}
	values := ciRunValues(run)
	if _, err := tx.ExecContext(ctx,
		"REPLACE INTO `ci_run` ("+ciRunColumns+") VALUES ("+valuePlaceholders(len(values))+")",
		values...,
	); err != nil {
		return fmt.Errorf("writing CI run %s: %w", run.ID, err)
	}
	if err := deleteCIRunChildren(ctx, tx, run.ID); err != nil {
		return err
	}
	for i, job := range run.Jobs {
		values := ciJobValues(run.ID, i, job)
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `ci_job` (`run_id`,`job_index`,"+ciJobColumns+") "+
				"VALUES ("+valuePlaceholders(len(values))+")",
			values...,
		); err != nil {
			return fmt.Errorf("writing job %d of CI run %s: %w", i, run.ID, err)
		}
		for j, step := range job.Steps {
			values := ciStepValues(run.ID, i, j, step)
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `ci_step` (`run_id`,`job_index`,`step_index`,"+ciStepColumns+") "+
					"VALUES ("+valuePlaceholders(len(values))+")",
				values...,
			); err != nil {
				return fmt.Errorf("writing step %d of job %d of CI run %s: %w", j, i, run.ID, err)
			}
		}
	}
	return nil
}

type stepOutputWriteReq struct {
	ctx       context.Context
	runID     string
	jobIndex  int
	stepIndex int
	output    string
	err       error
	done      chan struct{}
}

const maxStepOutputBatchSize = 100

// PutCIStepOutput appends a log chunk for one running step into
// ci_step_log_chunk and touches nothing else -- the whole of what a run in
// flight has to say when a step that is still running publishes another five
// seconds of its log.
//
// Concurrent calls to PutCIStepOutput are batched into single transactions
// to eliminate SQLite writer lock contention and serialization bottlenecks
// under high concurrency streaming load.
//
// It exists because the whole-run write is the wrong shape for that one
// message and the right shape for every other: PutCIRun replaces the run
// row, deletes every job and step under it and re-inserts them all, which
// for a live publication is a transaction carrying every step's output in
// order to change one cell of one of them. A step printing for forty
// minutes sends a few hundred of those (ci.LiveOutputInterval), and a
// busy deployment has several such steps at once. Appending chunks to
// ci_step_log_chunk avoids whole-table row updates and write amplification
// during streaming, rewriting whole-step output into ci_step only upon
// step conclusion or full-run replacement.
//
// Nothing here bounds the text: what one step and what one run are
// allowed to keep is pkg/ci's policy (MaxStoredStepOutput,
// MaxStoredRunOutput, applied by ci.storedRun before either write), and a
// narrow write that quietly applied a different rule than the whole-run
// one would be the one way this could go wrong. Its caller uses
// CIStepOutputChange to establish that this statement leaves behind
// exactly the rows PutCIRun would have.
//
// A step that is not stored is an error rather than a silent no-op: the
// row was read moments ago by the caller deciding to write it, so its
// absence means the run has been pruned or the indexes are wrong, and
// both are worth hearing about.
func (s *Store) PutCIStepOutput(ctx context.Context, runID string, jobIndex, stepIndex int, output string) error {
	req := &stepOutputWriteReq{
		ctx:       ctx,
		runID:     runID,
		jobIndex:  jobIndex,
		stepIndex: stepIndex,
		output:    output,
		done:      make(chan struct{}),
	}

	s.stepOutputMu.Lock()
	s.stepOutputQueue = append(s.stepOutputQueue, req)
	if s.stepOutputFlusher {
		s.stepOutputMu.Unlock()
		select {
		case <-req.done:
			return req.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	s.stepOutputFlusher = true
	s.stepOutputMu.Unlock()

	for {
		s.stepOutputMu.Lock()
		if len(s.stepOutputQueue) == 0 {
			s.stepOutputFlusher = false
			s.stepOutputMu.Unlock()
			break
		}
		var batch []*stepOutputWriteReq
		if len(s.stepOutputQueue) <= maxStepOutputBatchSize {
			batch = s.stepOutputQueue
			s.stepOutputQueue = nil
		} else {
			batch = s.stepOutputQueue[:maxStepOutputBatchSize]
			s.stepOutputQueue = s.stepOutputQueue[maxStepOutputBatchSize:]
		}
		s.stepOutputMu.Unlock()

		s.flushCIStepOutputBatch(batch)
	}

	select {
	case <-req.done:
		return req.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) flushCIStepOutputBatch(batch []*stepOutputWriteReq) {
	if len(batch) == 0 {
		return
	}

	ctx := context.Background()
	err := s.write(ctx, "batch put CI step output", func(tx *sql.Tx) error {
		for _, req := range batch {
			if err := req.ctx.Err(); err != nil {
				req.err = err
				continue
			}
			// The batch's own context, not req.ctx: the transaction is
			// shared, so one caller giving up part-way through its
			// statements must not fail -- and roll back -- every other
			// caller's write. A caller that has already given up is
			// skipped above instead.
			err := putSingleCIStepOutput(ctx, tx, req.runID, req.jobIndex, req.stepIndex, req.output)
			if err != nil {
				if strings.Contains(err.Error(), "no such step is stored") {
					req.err = err
					continue
				}
				return err
			}
		}
		return nil
	})

	for _, req := range batch {
		if err != nil && req.err == nil {
			req.err = err
		}
		close(req.done)
	}
}

func putSingleCIStepOutput(ctx context.Context, tx *sql.Tx, runID string, jobIndex, stepIndex int, output string) error {
	what := fmt.Sprintf("put output of step %d of job %d of CI run %s", stepIndex, jobIndex, runID)
	var baseOutput string
	err := tx.QueryRowContext(ctx,
		"SELECT `output` FROM `ci_step` WHERE `run_id` = ? AND `job_index` = ? AND `step_index` = ?",
		runID, jobIndex, stepIndex).Scan(&baseOutput)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s: no such step is stored", what)
		}
		return fmt.Errorf("%s: %w", what, err)
	}

	var chunks []string
	var nextChunkIndex int
	rows, err := tx.QueryContext(ctx,
		"SELECT `chunk_index`, `output` FROM `ci_step_log_chunk` WHERE `run_id` = ? AND `job_index` = ? AND `step_index` = ? ORDER BY `chunk_index`",
		runID, jobIndex, stepIndex)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer rows.Close()
	for rows.Next() {
		var idx int
		var chunk string
		if err := rows.Scan(&idx, &chunk); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		chunks = append(chunks, chunk)
		nextChunkIndex = idx + 1
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	current := baseOutput
	if len(chunks) > 0 {
		current += strings.Join(chunks, "")
	}

	toAppend := output
	if strings.HasPrefix(output, current) {
		toAppend = output[len(current):]
	} else if current != "" {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `ci_step_log_chunk` WHERE `run_id` = ? AND `job_index` = ? AND `step_index` = ?",
			runID, jobIndex, stepIndex); err != nil {
			return fmt.Errorf("%s: clearing outdated chunks: %w", what, err)
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `ci_step` SET `output` = '' WHERE `run_id` = ? AND `job_index` = ? AND `step_index` = ?",
			runID, jobIndex, stepIndex); err != nil {
			return fmt.Errorf("%s: clearing base step output: %w", what, err)
		}
		nextChunkIndex = 0
		toAppend = output
	}
	if toAppend == "" && (len(chunks) > 0 || current == output) {
		return nil
	}

	_, err = tx.ExecContext(ctx,
		"INSERT INTO `ci_step_log_chunk` (`run_id`,`job_index`,`step_index`,`chunk_index`,`output`,`created_at`) "+
			"VALUES (?,?,?,?,?,?)",
		runID, jobIndex, stepIndex, nextChunkIndex, toAppend, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// CIStepOutputChange reports whether one step's output is the only thing
// that separates the run stored (before) from the run a caller is about
// to store (after), and which step that is.
//
// It is the identity problem the narrow write has, answered in the one
// place that knows what a row holds. A caller handing progress to a store
// has a run, not a row: pkg/ci's observer finds a job by its workflow
// path and job id and replaces it wholesale, and the indexes ci_job and
// ci_step are keyed by are this package's business. So rather than
// threading "this is a live publication, at job 2 step 5" down from the
// engine, the caller says what it has -- the two versions of the run --
// and this compares them column by column, which is the question that
// actually matters: is a single UPDATE the same write as the whole-run
// replacement?
//
// Everything is compared as the row values it would be written as
// (ciRunValues and friends), not as the Go structs: a time that has been
// through the database and one that has not are different values and the
// same column. ok is false for anything else at all -- a job added, a
// step added, two outputs moved, a timestamp advanced, a conclusion
// arriving -- so the fallback is the whole-run write and the narrow one
// is taken only where it is provably equivalent.
//
// A run that differs in nothing is reported as no change (ok is false):
// the caller is welcome to write it again, and PutCIRun is what it means
// by that.
func CIStepOutputChange(before, after CIRun) (jobIndex, stepIndex int, ok bool) {
	if !reflect.DeepEqual(ciRunValues(before), ciRunValues(after)) {
		return 0, 0, false
	}
	if len(before.Jobs) != len(after.Jobs) {
		return 0, 0, false
	}
	found := false
	for i := range before.Jobs {
		if !reflect.DeepEqual(ciJobValues(before.ID, i, before.Jobs[i]), ciJobValues(after.ID, i, after.Jobs[i])) {
			return 0, 0, false
		}
		if len(before.Jobs[i].Steps) != len(after.Jobs[i].Steps) {
			return 0, 0, false
		}
		for j := range before.Jobs[i].Steps {
			was, is := before.Jobs[i].Steps[j], after.Jobs[i].Steps[j]
			if reflect.DeepEqual(ciStepValues(before.ID, i, j, was), ciStepValues(after.ID, i, j, is)) {
				continue
			}
			if found {
				return 0, 0, false // two steps changed: that is not this write
			}
			// The stored step, carrying the new output and nothing else
			// new. If that writes the row the caller is asking for, the
			// output column is the whole of the difference -- and if it
			// does not, some other column moved too. Asked this way round
			// rather than by naming `output`'s position in the row, so
			// that a column added to ciStepValues needs nothing here.
			was.Output = is.Output
			if !reflect.DeepEqual(ciStepValues(after.ID, i, j, was), ciStepValues(after.ID, i, j, is)) {
				return 0, 0, false
			}
			jobIndex, stepIndex, found = i, j, true
		}
	}
	return jobIndex, stepIndex, found
}

func deleteCIRunChildren(ctx context.Context, tx *sql.Tx, ids ...any) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := "?"
	for range ids[1:] {
		placeholders += ",?"
	}
	for _, table := range []string{"ci_step_log_chunk", "ci_step", "ci_job"} {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `"+table+"` WHERE `run_id` IN ("+placeholders+")", ids...); err != nil {
			return fmt.Errorf("clearing %s: %w", table, err)
		}
	}
	return nil
}

// GetCIRun returns one run entire -- jobs, steps and every step's output
// -- or nil if this deployment has no such run (it never had one, or it
// has since been pruned).
func (s *Store) GetCIRun(ctx context.Context, id string) (*CIRun, error) {
	run, err := scanCIRun(s.db.QueryRowContext(ctx,
		"SELECT "+ciRunColumns+" FROM `ci_run` WHERE `id` = ?", id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading CI run %s: %w", id, err)
	}
	jobs, err := ciJobsOf(ctx, s.db, []string{id}, true)
	if err != nil {
		return nil, err
	}
	run.Jobs = jobs[id]
	return &run, nil
}

// ListCIRuns returns runs newest first, at most limit of them (zero or
// less means all), with their jobs and steps but *without* any step's
// output.
//
// Output is left out because nothing lists it. A step keeps tens of
// kilobytes of what it printed, a run has as many steps as its workflows
// declare, and the caller is drawing a table with no output column in it
// (ui.ciListSummary threw exactly this away again at the far end); the
// one reader that wants a step's output is looking at that step, and
// GetCIRun is how it gets there.
func (s *Store) ListCIRuns(ctx context.Context, limit int) ([]CIRun, error) {
	query := "SELECT " + ciRunColumns + " FROM `ci_run` ORDER BY `started_at` DESC, `id` DESC"
	var args []any
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	var runs []CIRun
	var ids []string
	if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		run, err := scanCIRun(rows.Scan)
		if err != nil {
			return err
		}
		runs = append(runs, run)
		ids = append(ids, run.ID)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing CI runs: %w", err)
	}
	jobs, err := ciJobsOf(ctx, s.db, ids, false)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		runs[i].Jobs = jobs[runs[i].ID]
	}
	return runs, nil
}

// CIRunQuery is one narrowed question about the stored runs, in this
// package's own terms: columns, not pkg/ci's vocabulary (this package
// does not import that one -- see CIRun).
//
// It exists so that a filter reaches the whole history rather than the
// newest page of it (grain/task-289). Everything a reader can narrow by
// is a column or a row of `ci_job`, so all of it is answerable here; the
// alternative, which is what this replaces, was to list the newest few
// hundred runs and filter those in Go, which made "was this job red on
// another branch last week" a question about a day and a half whatever
// the deployment kept.
//
// Every field is optional and an empty one narrows nothing, so the zero
// value matches every stored run. They compose: a run has to satisfy all
// of them.
type CIRunQuery struct {
	// Repo is a whole repository name, compared without regard to case.
	Repo string
	// Ref is matched as a substring of the branch, without regard to
	// case, so "task-289" finds "grain/task-289".
	Ref string
	// Conclusions are the verdicts a concluded run may carry; a run with
	// any of them matches. Together with Running and Pending this is one
	// condition -- a run matching any of the three kinds is kept -- and
	// none of them set is no condition at all.
	Conclusions []string
	// Running keeps runs with no conclusion that have begun a job, and
	// Pending those with no conclusion and no job begun. The two are
	// separate because "working" and "waiting for a machine" are a
	// question about this deployment's capacity rather than about any
	// commit (ci.RunStateOf, which is the rule these two restate).
	Running bool
	Pending bool
	// Job keeps runs that have a job by this name or id, compared
	// without regard to case and whatever that job did.
	Job string
	// Since drops runs that started before it. A run that has not
	// started is kept: it is queued, so it cannot be older than any
	// window somebody is asking about.
	Since time.Time
	// Until drops runs that started at or after it, and runs that have
	// not started at all (ci.RunFilter.Until).
	Until time.Time
}

// CIRunStamp is one matching run as the narrowing pass reads it: its id
// and the two instants every order over a list of runs is a function of
// (started, finished, and the duration between them).
//
// Three columns rather than the whole row because of what the two passes
// are for. This one runs over every matching run, however many that is;
// the row, its jobs and its steps are then read for the page that is
// actually answered with (ListCIRunsByID). A zero time is a run that has
// not started, or has not finished.
type CIRunStamp struct {
	ID         string
	StartedAt  time.Time
	FinishedAt time.Time
	// Repo is where the run ran, carried for a reader whose view is
	// narrowed by repository (ci.RunFilter.Visible): that question is a
	// predicate over team repositories and their globs rather than
	// anything SQL can be asked, so it is answered over the stamps,
	// before the page is cut and before anything is counted.
	Repo string
}

// MatchCIRuns is which runs answer q, newest first, with no bound on how
// many: the id and timestamps of each, for a caller that will order them
// and then read back the page it wants (ListCIRunsByID).
//
// Ordered by start here only so that the answer is deterministic and the
// caller's own sort -- which is stable, and which knows what a missing
// timestamp means (ci.SortRuns) -- has a defined order to preserve.
func (s *Store) MatchCIRuns(ctx context.Context, q CIRunQuery) ([]CIRunStamp, error) {
	where, args := ciRunConditions(q)
	query := "SELECT `id`,`started_at`,`finished_at`,`repo` FROM `ci_run` r" + where +
		" ORDER BY `started_at` DESC, `id` DESC"
	var out []CIRunStamp
	if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		var stamp CIRunStamp
		var started, finished sql.NullTime
		if err := rows.Scan(&stamp.ID, &started, &finished, &stamp.Repo); err != nil {
			return err
		}
		stamp.StartedAt, stamp.FinishedAt = started.Time, finished.Time
		out = append(out, stamp)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("matching CI runs: %w", err)
	}
	return out, nil
}

// CountCIRuns is how many runs this deployment holds in all -- what an
// answer to a narrowed question says it was narrowed from, so that
// "nothing matched" is distinguishable from "nothing has run".
func (s *Store) CountCIRuns(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `ci_run`").Scan(&n); err != nil {
		return 0, fmt.Errorf("counting CI runs: %w", err)
	}
	return n, nil
}

// ListCIRunsByID reads these runs in full -- jobs and steps, and no
// step's output, exactly as ListCIRuns serves them -- in the order the
// ids are given, which is the order the caller has already decided on.
//
// An id that is not stored is left out rather than being an error: the
// ids came from a read of their own, and a run pruned between the two is
// an ordinary race rather than something a listing should fail over.
func (s *Store) ListCIRunsByID(ctx context.Context, ids []string) ([]CIRun, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	placeholders := "?"
	args[0] = ids[0]
	for i, id := range ids[1:] {
		placeholders += ",?"
		args[i+1] = id
	}
	found := map[string]CIRun{}
	if err := each(ctx, s.db,
		"SELECT "+ciRunColumns+" FROM `ci_run` WHERE `id` IN ("+placeholders+")", args,
		func(rows *sql.Rows) error {
			run, err := scanCIRun(rows.Scan)
			if err != nil {
				return err
			}
			found[run.ID] = run
			return nil
		}); err != nil {
		return nil, fmt.Errorf("reading CI runs: %w", err)
	}
	jobs, err := ciJobsOf(ctx, s.db, ids, false)
	if err != nil {
		return nil, err
	}
	out := make([]CIRun, 0, len(ids))
	for _, id := range ids {
		run, ok := found[id]
		if !ok {
			continue
		}
		run.Jobs = jobs[id]
		out = append(out, run)
	}
	return out, nil
}

// ciRunConditions is a query as a WHERE clause and its arguments, or ""
// for a query that asks nothing.
//
// Comparisons are spelled with LOWER and INSTR rather than with LIKE and
// a collation: what a reader means by "the same repository" and "part of
// this branch name" is ci.RunFilter's rule, and writing it out is what
// keeps the SQL and the in-memory filter answering alike (they are
// tested against each other -- see the store's own tests).
func ciRunConditions(q CIRunQuery) (string, []any) {
	var conds []string
	var args []any
	if q.Repo != "" {
		conds = append(conds, "LOWER(`repo`) = LOWER(?)")
		args = append(args, q.Repo)
	}
	if q.Ref != "" {
		conds = append(conds, "INSTR(LOWER(`ref`), LOWER(?)) > 0")
		args = append(args, q.Ref)
	}
	if q.Job != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM `ci_job` j WHERE j.`run_id` = r.`id` AND "+
			"(LOWER(j.`name`) = LOWER(?) OR LOWER(j.`job_id`) = LOWER(?)))")
		args = append(args, q.Job, q.Job)
	}
	if !q.Since.IsZero() {
		conds = append(conds, "(`started_at` IS NULL OR `started_at` >= ?)")
		args = append(args, q.Since.UTC())
	}
	if !q.Until.IsZero() {
		conds = append(conds, "(`started_at` IS NOT NULL AND `started_at` < ?)")
		args = append(args, q.Until.UTC())
	}
	if state, stateArgs := ciRunStateCondition(q); state != "" {
		conds = append(conds, state)
		args = append(args, stateArgs...)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ciRunStateCondition is the one condition the three state fields make
// between them: a run matching any of the kinds asked for.
//
// "Has begun" is a job with a conclusion or a start of its own, which is
// the same test ci.RunStateOf makes of a run in memory: a run enters the
// store when it is queued, so its own `started_at` says nothing about
// whether anything is running it.
func ciRunStateCondition(q CIRunQuery) (string, []any) {
	const begun = "EXISTS (SELECT 1 FROM `ci_job` j WHERE j.`run_id` = r.`id` AND " +
		"(j.`conclusion` != '' OR j.`started_at` IS NOT NULL))"
	var kinds []string
	var args []any
	if len(q.Conclusions) > 0 {
		placeholders := make([]string, 0, len(q.Conclusions))
		for _, word := range q.Conclusions {
			placeholders = append(placeholders, "LOWER(?)")
			args = append(args, word)
		}
		kinds = append(kinds, "(`conclusion` != '' AND LOWER(`conclusion`) IN ("+
			strings.Join(placeholders, ",")+"))")
	}
	if q.Running {
		kinds = append(kinds, "(`conclusion` = '' AND "+begun+")")
	}
	if q.Pending {
		kinds = append(kinds, "(`conclusion` = '' AND NOT "+begun+")")
	}
	if len(kinds) == 0 {
		return "", nil
	}
	return "(" + strings.Join(kinds, " OR ") + ")", args
}

// LiveCIRun returns the run with this fingerprint that has not concluded,
// if there is one -- what ci.Service.Start answers a second request for
// work already in flight with, rather than by doing it twice. The newest
// wins if somehow there are two.
//
// A run left unconcluded by a daemon that stopped is not live and is not
// returned: RecoverInterruptedCIRuns stamps those at startup, which is
// what keeps this from handing a caller a run nothing is running.
func (s *Store) LiveCIRun(ctx context.Context, key string) (*CIRun, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		"SELECT `id` FROM `ci_run` WHERE `run_key` = ? AND `conclusion` = '' "+
			"ORDER BY `started_at` DESC, `id` DESC LIMIT 1", key).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("looking for a live CI run for %s: %w", key, err)
	}
	return s.GetCIRun(ctx, id)
}

// LiveCIRuns returns every stored CI run that has not concluded yet, with
// its jobs and steps.
func (s *Store) LiveCIRuns(ctx context.Context) ([]CIRun, error) {
	query := "SELECT " + ciRunColumns + " FROM `ci_run` WHERE `conclusion` = '' ORDER BY `started_at` ASC, `id` ASC"
	var runs []CIRun
	var ids []string
	if err := each(ctx, s.db, query, nil, func(rows *sql.Rows) error {
		run, err := scanCIRun(rows.Scan)
		if err != nil {
			return err
		}
		runs = append(runs, run)
		ids = append(ids, run.ID)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("listing live CI runs: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	jobs, err := ciJobsOf(ctx, s.db, ids, true)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		runs[i].Jobs = jobs[runs[i].ID]
	}
	return runs, nil
}

// RecoverInterruptedCIRuns marks every run still recorded as in flight
// as cancelled and interrupted, and reports how many it stamped.
//
// Called once, at startup, before anything reads a run back. An
// in-memory store answered this question by forgetting everything; a
// durable one has to answer it on purpose, because a row saying "no
// conclusion" means "still going" and nothing is going any more. Left
// alone, such a row would deduplicate a fresh request against a run that
// died (LiveCIRun) and leave the task waiting on it waiting forever
// (orchestrator.collectCIRun).
//
// The run is kept rather than deleted: the jobs that did finish are the
// most interesting thing about a daemon that stopped mid-run, and this
// is the case the in-memory store served as a blank page.
func (s *Store) RecoverInterruptedCIRuns(ctx context.Context, conclusion, reason string, now time.Time) (int, error) {
	return s.RecoverInterruptedCIRunsExcept(ctx, conclusion, reason, now, nil)
}

// RecoverInterruptedCIRunsExcept marks every unconcluded run not in keepRunIDs
// as cancelled and interrupted, and reports how many it stamped.
func (s *Store) RecoverInterruptedCIRunsExcept(ctx context.Context, conclusion, reason string, now time.Time, keepRunIDs []string) (int, error) {
	var stamped int
	err := s.write(ctx, "recover interrupted CI runs", func(tx *sql.Tx) error {
		query := "UPDATE `ci_run` SET `conclusion` = ?, `interrupted` = 1, `finished_at` = ?, " +
			"`error` = CASE WHEN `error` = '' THEN ? ELSE `error` END " +
			"WHERE `conclusion` = ''"
		args := []any{conclusion, now.UTC(), reason}
		if len(keepRunIDs) > 0 {
			placeholders := "?"
			for range keepRunIDs[1:] {
				placeholders += ",?"
			}
			query += " AND `id` NOT IN (" + placeholders + ")"
			for _, id := range keepRunIDs {
				args = append(args, id)
			}
		}
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		stamped = int(n)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("recovering interrupted CI runs: %w", err)
	}
	return stamped, nil
}

// PruneCIRuns keeps the newest keep concluded runs and deletes the rest,
// reporting how many it deleted. A run still in flight is neither
// counted nor deleted, whatever the limit says -- dropping the record of
// something that is still going would leave a caller polling an id that
// has vanished.
//
// keep of zero or less prunes nothing, which is what "no limit" means
// here rather than "delete everything".
func (s *Store) PruneCIRuns(ctx context.Context, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	var deleted int
	err := s.write(ctx, "prune CI runs", func(tx *sql.Tx) error {
		// Ids only, newest first, and the ones past the limit dropped
		// here rather than by an OFFSET: the row count is held near keep
		// by this very call, so reading it is cheap, and no dialect has
		// to be asked for "every row after the first n".
		var doomed []any
		seen := 0
		if err := each(ctx, tx,
			"SELECT `id` FROM `ci_run` WHERE `conclusion` != '' "+
				"ORDER BY `started_at` DESC, `id` DESC", nil,
			func(rows *sql.Rows) error {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				if seen++; seen > keep {
					doomed = append(doomed, id)
				}
				return nil
			}); err != nil {
			return err
		}
		if len(doomed) == 0 {
			return nil
		}
		if err := deleteCIRunChildren(ctx, tx, doomed...); err != nil {
			return err
		}
		placeholders := "?"
		for range doomed[1:] {
			placeholders += ",?"
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `ci_run` WHERE `id` IN ("+placeholders+")", doomed...); err != nil {
			return err
		}
		deleted = len(doomed)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("pruning CI runs: %w", err)
	}
	return deleted, nil
}

// CIStepOutputBounds is how much step output a deployment keeps: a
// ceiling on the bytes of it, and an age past which a run's output goes
// whatever room there is under that ceiling (ci.StoredRunRetention,
// which is where a deployment's own numbers come from).
//
// Bytes rather than a count of runs, because a run's output is anything
// from nothing to a few hundred kilobytes depending on what its steps
// printed, and disk is what an operator is actually budgeting
// (grain/task-269). Either bound may be left off: a MaxBytes of zero or
// less is no ceiling, and a zero Before is no age.
type CIStepOutputBounds struct {
	MaxBytes int64
	Before   time.Time
}

// CIStepOutputSweep is what one sweep did, for the line an operator
// reads in the journal. Kept is what is left behind, which is the number
// that says whether the ceiling is the right one.
type CIStepOutputSweep struct {
	Runs  int
	Steps int
	Bytes int64
	Kept  int64
}

// CIStepOutputUsage is how much of that ceiling is in use right now:
// the bytes of step output stored, across how many runs, and how far
// back they go.
//
// It is the reading an operator needs before touching either bound
// (grain/task-290). A ceiling says what a deployment is willing to
// spend; this says what it is spending, and the two together are the
// only way to tell "raise it, the evidence is being swept before anybody
// reads it" from "lower it, this is a gigabyte of logs nobody opens".
// Oldest is the direct answer to "will Friday's failure still be
// readable on Monday".
type CIStepOutputUsage struct {
	// Runs is how many runs have any output left, and Steps how many
	// steps across them -- a run whose output has been swept counts in
	// neither, though its record stands.
	Runs  int
	Steps int
	// Bytes is what those steps hold, measured exactly as the sweep
	// charges it.
	Bytes int64
	// Oldest is when the oldest run that still has output started, zero
	// if nothing does. It moves forward as the sweep works, which is
	// what makes it a retention reading rather than a fact about the
	// history: the run's own record may well outlive it.
	Oldest time.Time
}

// CIStepOutputUsage totals what PruneCIStepOutput would be sweeping,
// without sweeping anything: the same accounting, over the same rows,
// so a pane's figure and the sweep's ceiling are always in the same
// units.
//
// Every run that has output, in flight ones included -- the sweep
// charges those against the ceiling too, so a reading that left them out
// would be smaller than the number the bound is actually applied to.
func (s *Store) CIStepOutputUsage(ctx context.Context) (CIStepOutputUsage, error) {
	var usage CIStepOutputUsage
	// Grouped by run rather than totalled in SQL because the oldest run
	// is wanted alongside the bytes, and MIN of a date column comes back
	// from an aggregate without its declared type (TaskSpans scans the
	// same thing as a string for that reason). One row per run that has
	// output is a few thousand rows at the outside -- the run history is
	// bounded too -- and it is exactly the rowset the sweep walks.
	if err := each(ctx, s.db,
		"SELECT r.`started_at`, COUNT(s.`output`), SUM(LENGTH(CAST(s.`output` AS BLOB))) "+
			"FROM `ci_run` r JOIN `ci_step` s ON s.`run_id` = r.`id` "+
			"WHERE s.`output` != '' GROUP BY r.`id`", nil,
		func(rows *sql.Rows) error {
			var started sql.NullTime
			var steps int
			var bytes int64
			if err := rows.Scan(&started, &steps, &bytes); err != nil {
				return err
			}
			usage.Runs++
			usage.Steps += steps
			usage.Bytes += bytes
			if started.Valid && (usage.Oldest.IsZero() || started.Time.Before(usage.Oldest)) {
				usage.Oldest = started.Time.UTC()
			}
			return nil
		}); err != nil {
		return CIStepOutputUsage{}, fmt.Errorf("measuring stored CI step output: %w", err)
	}
	return usage, nil
}

// PruneCIStepOutput clears the step output of concluded runs until what
// is left fits within bounds, without deleting the runs, jobs, steps or
// any verdict on them: a run record outlives its own output, which is
// the whole point of keeping the two bounds apart.
//
// Newest first, and a whole run at a time. Once a run does not fit, it
// and everything older than it loses its output -- which is "sweep the
// oldest until it fits" written as one pass, and it drops whole runs
// because half a run's log answers nothing (a reader wants the step that
// failed and the step before it).
//
// A run still in flight is never cleared -- something is writing to it
// -- but its output is charged against the ceiling all the same, so the
// bound describes the table rather than the part of it nothing is
// touching.
//
// Bounds that say nothing sweep nothing, which is what "no limit" means
// here rather than "delete everything".
func (s *Store) PruneCIStepOutput(ctx context.Context, bounds CIStepOutputBounds) (CIStepOutputSweep, error) {
	if bounds.MaxBytes <= 0 && bounds.Before.IsZero() {
		return CIStepOutputSweep{}, nil
	}
	var sweep CIStepOutputSweep
	err := s.write(ctx, "prune CI step output", func(tx *sql.Tx) error {
		sweep = CIStepOutputSweep{}
		// One row per run that has any output at all, newest first, with
		// the bytes it holds. LENGTH of a BLOB cast rather than of the
		// text, so what is counted is what is stored rather than a count
		// of characters (memory_store.go measures a document the same
		// way).
		type candidate struct {
			id      string
			bytes   int64
			started sql.NullTime
			live    bool
		}
		var runs []candidate
		if err := each(ctx, tx,
			"SELECT r.`id`, r.`started_at`, r.`conclusion`, SUM(LENGTH(CAST(s.`output` AS BLOB))) "+
				"FROM `ci_run` r JOIN `ci_step` s ON s.`run_id` = r.`id` "+
				"WHERE s.`output` != '' GROUP BY r.`id` "+
				"ORDER BY r.`started_at` DESC, r.`id` DESC", nil,
			func(rows *sql.Rows) error {
				var c candidate
				var conclusion string
				if err := rows.Scan(&c.id, &c.started, &conclusion, &c.bytes); err != nil {
					return err
				}
				c.live = conclusion == ""
				runs = append(runs, c)
				return nil
			}); err != nil {
			return err
		}
		var doomed []any
		spent := int64(0)
		full := false
		for _, run := range runs {
			aged := !bounds.Before.IsZero() && run.started.Valid && run.started.Time.Before(bounds.Before)
			over := bounds.MaxBytes > 0 && (full || spent+run.bytes > bounds.MaxBytes)
			if run.live || !(aged || over) {
				// A run in flight is kept whatever the bounds say, and
				// charged for all the same: it is about to be rewritten
				// by the process that is running it, and clearing a
				// column underneath that write would achieve nothing but
				// a hole in a live log.
				spent += run.bytes
				sweep.Kept += run.bytes
				continue
			}
			if over {
				// Past the ceiling: everything older goes too, so that
				// the sweep is one contiguous tail rather than whichever
				// older runs happened to be small enough to slot in.
				full = true
			}
			doomed = append(doomed, run.id)
			sweep.Bytes += run.bytes
		}
		if len(doomed) == 0 {
			return nil
		}
		placeholders := "?"
		for range doomed[1:] {
			placeholders += ",?"
		}
		res, err := tx.ExecContext(ctx,
			"UPDATE `ci_step` SET `output` = '' WHERE `output` != '' AND `run_id` IN ("+placeholders+")", doomed...)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `ci_step_log_chunk` WHERE `run_id` IN ("+placeholders+")", doomed...); err != nil {
			return err
		}
		type attemptRow struct {
			runID    string
			jobIndex int
			attempts string
		}
		var attemptRows []attemptRow
		if err := each(ctx, tx,
			"SELECT `run_id`, `job_index`, `attempts` FROM `ci_job` WHERE `attempts` != '' AND `run_id` IN ("+placeholders+")",
			doomed, func(rows *sql.Rows) error {
				var ar attemptRow
				if err := rows.Scan(&ar.runID, &ar.jobIndex, &ar.attempts); err != nil {
					return err
				}
				attemptRows = append(attemptRows, ar)
				return nil
			}); err != nil {
			return err
		}
		for _, ar := range attemptRows {
			if stripped, changed := stripCIJobAttemptsOutput(ar.attempts); changed {
				if _, err := tx.ExecContext(ctx,
					"UPDATE `ci_job` SET `attempts` = ? WHERE `run_id` = ? AND `job_index` = ?",
					stripped, ar.runID, ar.jobIndex); err != nil {
					return err
				}
			}
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		sweep.Steps = int(n)
		sweep.Runs = len(doomed)
		return nil
	})
	if err != nil {
		return CIStepOutputSweep{}, fmt.Errorf("pruning CI step output: %w", err)
	}
	return sweep, nil
}

func stripCIJobAttemptsOutput(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return raw, false
	}
	changed := false
	for _, entry := range entries {
		if v, ok := entry["go_test_log"].(string); ok && v != "" {
			delete(entry, "go_test_log")
			changed = true
		}
		if steps, ok := entry["steps"].([]any); ok {
			for _, s := range steps {
				if sm, ok := s.(map[string]any); ok {
					if out, ok := sm["output"].(string); ok && out != "" {
						delete(sm, "output")
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return raw, false
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return raw, false
	}
	return string(b), true
}

func scanCIRun(scan func(...any) error) (CIRun, error) {
	var run CIRun
	var started, finished, built sql.NullTime
	if err := scan(&run.ID, &run.Key, &run.Repo, &run.Ref, &run.Commit, &run.Event, &run.RequestedBy,
		&started, &finished, &run.Conclusion, &run.Interrupted, &run.Error, &run.CancelReason,
		&run.CancelledBy, &run.Build, &built, &run.BuildModified, &run.Recheck); err != nil {
		return CIRun{}, err
	}
	run.StartedAt, run.FinishedAt, run.BuildTime = started.Time, finished.Time, built.Time
	return run, nil
}

// ciJobsOf reads the jobs of every run named, in plan order, each with
// its steps -- one query for the jobs and one for the steps however many
// runs are asked about, since the listing asks about all of them at
// once.
func ciJobsOf(ctx context.Context, q querier, ids []string, withOutput bool) (map[string][]CIJob, error) {
	out := map[string][]CIJob{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	placeholders := "?"
	args[0] = ids[0]
	for i, id := range ids[1:] {
		placeholders += ",?"
		args[i+1] = id
	}

	// Jobs are collected as pointers and flattened at the end, so the
	// steps below can be filed straight onto the job they belong to --
	// appending to a slice of values would move the ones already in it.
	ordered := map[string][]*CIJob{}
	at := map[string]map[int]*CIJob{}
	if err := each(ctx, q,
		"SELECT `run_id`,`job_index`,"+ciJobColumns+" FROM `ci_job` "+
			"WHERE `run_id` IN ("+placeholders+") ORDER BY `run_id`,`job_index`", args,
		func(rows *sql.Rows) error {
			var runID string
			var index int
			var job CIJob
			var started, finished, activityAt sql.NullTime
			var queuedMS int64
			var quarantinedTests string
			if err := rows.Scan(&runID, &index, &job.Workflow, &job.WorkflowPath, &job.JobID, &job.Name,
				&job.Key, &job.Cached, &job.Sandbox, &started, &finished, &job.Conclusion, &job.Error,
				&job.Activity, &activityAt, &queuedMS, &job.QueuedCause, &job.QueuedReason,
				&job.RetryCount, &job.RetryReason,
				&job.Flake, &job.Quarantined, &quarantinedTests, &job.Status, &job.Warning,
				&job.FailureSummary, &job.BaseCommit, &job.Attempts); err != nil {
				return err
			}
			if !withOutput && job.Attempts != "" {
				job.Attempts, _ = stripCIJobAttemptsOutput(job.Attempts)
			}
			job.QuarantinedTests = ciNameListOf(quarantinedTests)
			job.StartedAt, job.FinishedAt, job.ActivityAt = started.Time, finished.Time, activityAt.Time
			job.Queued = time.Duration(queuedMS) * time.Millisecond
			ordered[runID] = append(ordered[runID], &job)
			if at[runID] == nil {
				at[runID] = map[int]*CIJob{}
			}
			at[runID][index] = &job
			return nil
		}); err != nil {
		return nil, fmt.Errorf("reading CI jobs: %w", err)
	}

	output := "`output`"
	if !withOutput {
		output = "''"
	}
	if err := each(ctx, q,
		"SELECT `run_id`,`job_index`,`name`,`conclusion`,`exit_code`,"+output+",`error`,"+
			"`started_at`,`finished_at` FROM `ci_step` "+
			"WHERE `run_id` IN ("+placeholders+") ORDER BY `run_id`,`job_index`,`step_index`", args,
		func(rows *sql.Rows) error {
			var runID string
			var index int
			var step CIStep
			var started, finished sql.NullTime
			if err := rows.Scan(&runID, &index, &step.Name, &step.Conclusion, &step.ExitCode,
				&step.Output, &step.Error, &started, &finished); err != nil {
				return err
			}
			step.StartedAt, step.FinishedAt = started.Time, finished.Time
			job := at[runID][index]
			if job == nil {
				// A step whose job is not there is a row this package did
				// not write; skipping it is the only reading that does not
				// invent a job.
				return nil
			}
			job.Steps = append(job.Steps, step)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("reading CI steps: %w", err)
	}

	if withOutput {
		if err := each(ctx, q,
			"SELECT `run_id`,`job_index`,`step_index`,`output` FROM `ci_step_log_chunk` "+
				"WHERE `run_id` IN ("+placeholders+") ORDER BY `run_id`,`job_index`,`step_index`,`chunk_index`", args,
			func(rows *sql.Rows) error {
				var runID string
				var jobIdx, stepIdx int
				var chunk string
				if err := rows.Scan(&runID, &jobIdx, &stepIdx, &chunk); err != nil {
					return err
				}
				if job := at[runID][jobIdx]; job != nil && stepIdx >= 0 && stepIdx < len(job.Steps) {
					job.Steps[stepIdx].Output += chunk
				}
				return nil
			}); err != nil {
			return nil, fmt.Errorf("reading CI step log chunks: %w", err)
		}
	}

	for runID, jobs := range ordered {
		flat := make([]CIJob, 0, len(jobs))
		for _, job := range jobs {
			flat = append(flat, *job)
		}
		out[runID] = flat
	}
	return out, nil
}

// ciTime binds a time that may be the zero value: NULL rather than year
// one, so a run that has not finished is distinguishable in SQL from one
// that finished at a time nobody believes.
func ciTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
