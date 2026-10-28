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

// What happened *inside* a run, kept as data.
//
// Everything else pkg/metrics measures is derived from moments already
// recorded for other reasons -- filed, approved, dispatched, finished --
// and this file is the one exception the rules in that package's own doc
// comment allow: it stores rows, because what it stores is not derivable
// from anything that survives the run.
//
// The census itself is not new. orchestrator.toolCallSummary has always
// counted every tool a run called and how many of those calls came back
// as errors, for every run including the successful ones -- and then
// rendered it into English and put it in task_run.detail, a column a
// human reads one row at a time. Nothing could aggregate it, because it
// was prose; agent.Result, where the numbers came from, is discarded the
// moment the run returns; and Result.Transcript is per-framework prose
// that a tool's own output can forge (outcomeOf's doc comment). So "is
// edit_file's error rate climbing?" and "how big does a run_command
// answer actually get?" -- the questions that would size a truncation cap
// or find a sandbox going bad -- had no answer anywhere.
//
// Two tables, because the run holds two different kinds of fact:
//
//   - task_run_tool is the census, one row per run per tool. Counts and
//     sizes only; nothing here is an argument, a result or a snippet of
//     output, so no part of a task's contents lands in a measurement
//     table.
//   - task_run_check_wait is one row per wait_for_checks call, because
//     the CI loop the prompt sends every run around is a sequence rather
//     than a total: which verdict each wait ended in, how long it
//     blocked, and how many pushes came before it are three facts about
//     one call, and averaging them into a per-run row would lose the one
//     question worth asking (how many pushes it took to go green).
//
// Both are written once, by orchestrator.RunDispatch, after the run has
// already finished -- the same after-the-fact shape SetRunTranscript has
// -- and never updated afterwards.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SizeHistogram counts tool results by size, in base-2 buckets: bucket e
// holds every result of [2^(e-1), 2^e) bytes, and bucket 0 holds the
// empty ones.
//
// It exists because a percentile needs a distribution and a distribution
// needs every sample, and a row per tool *call* is the one storage shape
// this was not worth: a busy run makes hundreds, and pkg/metrics reads
// its tables whole (Store.TaskTimings' own doc comment on why). Twenty-odd
// small integers per run per tool answer the question that matters --
// "what cap would have kept 95% of these results whole?" -- to within a
// factor of two, and metrics.Sizes names its percentiles AtMost so that
// the factor of two is stated rather than implied.
//
// A map rather than a slice because it is sparse: a tool whose results
// are all a few hundred bytes touches one or two buckets.
type SizeHistogram map[int]int

// SizeBucket is the bucket a result of n bytes falls in.
func SizeBucket(n int64) int {
	if n <= 0 {
		return 0
	}
	// bits.Len is the smallest e with n < 2^e, which is exactly the
	// bucket's own definition.
	return bits.Len64(uint64(n))
}

// SizeBucketMax is the largest result bucket e can hold -- the bound a
// percentile drawn from this histogram is honest to.
func SizeBucketMax(e int) int64 {
	if e <= 0 {
		return 0
	}
	return int64(1)<<uint(e) - 1
}

// Add records one result of n bytes.
func (h SizeHistogram) Add(n int64) { h[SizeBucket(n)]++ }

// Merge adds every count in other into h, which is how a report over many
// runs gets one distribution out of a row per run.
func (h SizeHistogram) Merge(other SizeHistogram) {
	for bucket, count := range other {
		h[bucket] += count
	}
}

// Total is how many results the histogram counts.
func (h SizeHistogram) Total() int {
	n := 0
	for _, count := range h {
		n += count
	}
	return n
}

// Buckets is the occupied buckets, smallest first -- the order anything
// walking a distribution needs them in.
func (h SizeHistogram) Buckets() []int {
	out := make([]int, 0, len(h))
	for bucket := range h {
		out = append(out, bucket)
	}
	sort.Ints(out)
	return out
}

// Encode renders the histogram for its column: "bucket:count" pairs,
// comma separated, smallest bucket first. Empty for a histogram with
// nothing in it, so an empty column and an empty histogram are the same
// thing in both directions.
//
// A text column rather than a table of its own: these counts are only
// ever read as a whole, alongside the row they belong to, and never
// queried or joined on.
func (h SizeHistogram) Encode() string {
	if len(h) == 0 {
		return ""
	}
	parts := make([]string, 0, len(h))
	for _, bucket := range h.Buckets() {
		parts = append(parts, strconv.Itoa(bucket)+":"+strconv.Itoa(h[bucket]))
	}
	return strings.Join(parts, ",")
}

// DecodeSizeHistogram reads Encode's output back. A malformed pair is
// dropped rather than failing the read: this is a measurement, and a
// report that refuses to render because one historical row is unreadable
// is worse than one that measures the rest.
func DecodeSizeHistogram(text string) SizeHistogram {
	h := SizeHistogram{}
	for _, pair := range strings.Split(text, ",") {
		bucket, count, ok := strings.Cut(pair, ":")
		if !ok {
			continue
		}
		b, err := strconv.Atoi(strings.TrimSpace(bucket))
		if err != nil || b < 0 {
			continue
		}
		c, err := strconv.Atoi(strings.TrimSpace(count))
		if err != nil || c <= 0 {
			continue
		}
		h[b] += c
	}
	return h
}

// RunToolUse is one run's whole use of one tool: how often it called it,
// how often that came back an error, how often the tool's own bound ended
// the call rather than the call finishing, how often grain cut the answer
// short, and how big the answers were.
//
// TimedOut is only ever non-zero for a tool that bounds its own work and
// says so -- run_command, today (mcp.RunCommandTimedOut). It is a count
// of calls grain itself cut off, which is a different thing from a call
// that failed: the first sizes a timeout, the second is the work.
//
// Elided and Dropped are the other half of Sizes, and the reason they are
// stored at all: mcp's result caps censor the size distribution they were
// sized from. Every answer is at or under the cap once it has been
// applied, so Sizes alone can no longer tell a tool whose answers fit from
// one whose answers are all being cut down to fit. Elided counts the calls
// grain cut (mcp.ResultElision), and Dropped is a histogram of how many
// bytes each of those cuts took out -- one sample per cut call, however
// many notices that call's answer carries, so Dropped.Total() is Elided
// minus the cuts whose size could not be read back.
type RunToolUse struct {
	RunID          string
	Tool           string
	Calls          int
	Errored        int
	TimedOut       int
	Elided         int
	ResultBytes    int64
	MaxResultBytes int64
	Sizes          SizeHistogram
	Dropped        SizeHistogram
}

// RunCheckWait is one wait_for_checks call: how it ended, how long it
// blocked, and how many pushes this run had made before it.
//
// PushesBefore is what makes "how many pushes does a run take to go
// green?" answerable -- read off the first wait in a run that ended
// WaitVerdictPassed. It is stored rather than derived for the same reason
// the census is: the run's own sequence of tool calls does not survive it.
type RunCheckWait struct {
	RunID        string
	Seq          int
	Verdict      string
	Waited       time.Duration
	PushesBefore int
	// Provider is the CI the wait was on: CIProviderGrain, CIProviderGitHub
	// or CIProviderTAP. Empty on a row recorded before the label existed,
	// which was a GitHub wait_for_checks (CIProviderOf).
	Provider string
}

// The CIs a recorded wait can be on.
const (
	CIProviderGrain  = "grain"
	CIProviderGitHub = "github"
	CIProviderTAP    = "tap"
)

// CIProviderOf is w's provider, reading a row recorded before the label
// existed as the GitHub wait it was.
func (w RunCheckWait) CIProviderOf() string {
	if w.Provider == "" {
		return CIProviderGitHub
	}
	return w.Provider
}

// RunWaitCredit is one run's whole account with the wall clock: how long
// it spent blocked inside wait_for_checks, and how much of that time was
// credited back to its deadline rather than charged against it
// (orchestrator.RunWaitCreditCap).
//
// The two differ only for a run that reached the cap, which is exactly
// what makes the pair worth storing: the cap was set from a distribution
// of waits, and the only honest way to re-price it later is against the
// runs that actually ran into it.
type RunWaitCredit struct {
	RunID    string
	Blocked  time.Duration
	Credited time.Duration
}

// RunCIHabit is how one run worked a red build: whether it reproduced a
// failure with a narrowed local test before pushing, and whether it asked
// CI for anything less than the whole suite (the red-CI procedure every
// run with grain CI is given -- orchestrator's redCIProcedureSection).
//
// NarrowTestBeforePush is a narrowed test run before the run's first
// push, which is the moment that matters for a repair task, red from its
// first turn. NarrowTestBeforeFixPush is the same thing measured from the
// run's first failed wait_for_checks to the push after it, which is the
// moment that matters for every other run: its own change is what went
// red. PushedAfterRed says there was such a push at all -- the run tried
// to fix a red it was shown -- and Selective that it called run_ci_job,
// retrigger_ci, or wait_for_checks with `jobs`.
//
// Stored for the reason the census is: the order of a run's calls does
// not survive it, and "before the first push" is a fact about order.
type RunCIHabit struct {
	RunID                   string
	NarrowTestBeforePush    bool
	NarrowTestBeforeFixPush bool
	PushedAfterRed          bool
	Selective               bool
}

// Empty reports whether the run did none of the things RunCIHabit
// records, which is a run that gets no row.
func (h RunCIHabit) Empty() bool {
	return !h.NarrowTestBeforePush && !h.NarrowTestBeforeFixPush && !h.PushedAfterRed && !h.Selective
}

// RunAPICallRetention is how long task_run_api_call rows are kept locally
// before being pruned (docs/design/durable-tasks.md#DT-14).
const RunAPICallRetention = 30 * 24 * time.Hour

// RunAPICall is one row of task_run_api_call: one agent-API or grain tool
// call a run made, with the target ids it read or touched and how many
// bytes the answer carried back, but never the result text itself.
type RunAPICall struct {
	RunID       string
	Seq         int
	Tool        string
	TargetIDs   string
	ResultBytes int64
	At          time.Time
}

// RunReportCheck is one row of task_run_report_check: the steward report
// checker's (or the weekly sampled judgement audit's) counts over one wake's
// closing report (docs/design/durable-tasks.md#DT-14).
type RunReportCheck struct {
	RunID      string
	Claims     int
	Uncited    int
	Unread     int
	Mismatched int
}

// RunTelemetry is everything one run recorded about its own tool use --
// what RecordRunTelemetry writes in a single transaction, so a report
// never sees a run's census half-written.
//
// WaitCredit is nil for a run that never blocked on CI, which is the same
// "no row rather than a row of zeroes" rule the two slices follow.
type RunTelemetry struct {
	Tools      []RunToolUse
	CheckWaits []RunCheckWait
	WaitCredit *RunWaitCredit
	// CIHabit is nil for a run that did none of what RunCIHabit records.
	CIHabit *RunCIHabit
	// APICalls is the per-call log of agent-API / grain tool calls
	// (task_run_api_call).
	APICalls []RunAPICall
}

// Empty reports whether there is nothing to record -- a run that never
// reached its agent, or one that made no tool calls at all. Such a run
// gets no rows rather than a row of zeroes: "this run called nothing" is
// already recorded, in task_run.detail and in its outcome, and a zero row
// here would only dilute every per-tool rate it was averaged into.
func (t RunTelemetry) Empty() bool {
	return len(t.Tools) == 0 && len(t.CheckWaits) == 0 && t.WaitCredit == nil && t.CIHabit == nil && len(t.APICalls) == 0
}

// RecordRunTelemetry writes runID's census, replacing anything already
// recorded for that run.
//
// Replacing rather than adding: the write happens once per run today, and
// a retry of it (or a recovery path that runs it again) must leave one
// census rather than two, since every count here is summed.
//
// Recording it must never cost a run -- the caller logs a failure and
// carries on, the same rule SetRunAgentStarted's own doc comment states:
// a measurement that cannot be taken is not a reason to fail the work
// being measured.
func (s *Store) RecordRunTelemetry(ctx context.Context, runID string, t RunTelemetry) error {
	return s.RecordBatchRunTelemetry(ctx, map[string]RunTelemetry{runID: t})
}

// RecordBatchRunTelemetry writes telemetry for multiple runs in a single write transaction.
func (s *Store) RecordBatchRunTelemetry(ctx context.Context, batch map[string]RunTelemetry) (err error) {
	if len(batch) == 0 {
		return nil
	}
	// The end-of-run write is the proxy census's last reader
	// (mergeWithProxyTools): once it has landed, the run's in-memory
	// counts are dropped rather than held for the daemon's lifetime.
	// Not before -- s.write may run the closure again.
	defer func() {
		if err == nil {
			s.dropProxyTools(batch)
		}
	}()
	desc := "record run telemetry batch"
	if len(batch) == 1 {
		for runID := range batch {
			desc = "record run " + runID + " telemetry"
		}
	}
	return s.write(ctx, desc, func(tx *sql.Tx) error {
		for runID, t := range batch {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM `task_run_tool` WHERE `run_id` = ?", runID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM `task_run_check_wait` WHERE `run_id` = ?", runID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM `task_run_wait_credit` WHERE `run_id` = ?", runID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM `task_run_ci_habit` WHERE `run_id` = ?", runID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM `task_run_api_call` WHERE `run_id` = ?", runID); err != nil {
				return err
			}
			for _, use := range s.mergeWithProxyTools(runID, t.Tools) {
				if _, err := tx.ExecContext(ctx,
					"INSERT INTO `task_run_tool` (`run_id`,`tool`,`calls`,`errored`,`timed_out`,"+
						"`elided`,`result_bytes`,`max_result_bytes`,`size_buckets`,`dropped_buckets`) "+
						"VALUES (?,?,?,?,?,?,?,?,?,?)",
					runID, use.Tool, use.Calls, use.Errored, use.TimedOut, use.Elided,
					use.ResultBytes, use.MaxResultBytes, use.Sizes.Encode(), use.Dropped.Encode()); err != nil {
					return fmt.Errorf("recording %s's use of %s: %w", runID, use.Tool, err)
				}
			}
			for _, wait := range s.mergeWithProxyCheckWaits(runID, t.CheckWaits) {
				if _, err := tx.ExecContext(ctx,
					"INSERT INTO `task_run_check_wait` (`run_id`,`seq`,`verdict`,`waited_ms`,`pushes_before`,`provider`) "+
						"VALUES (?,?,?,?,?,?)",
					runID, wait.Seq, wait.Verdict,
					wait.Waited.Milliseconds(), wait.PushesBefore, wait.Provider); err != nil {
					return fmt.Errorf("recording %s's CI wait %d: %w", runID, wait.Seq, err)
				}
			}
			if credit := t.WaitCredit; credit != nil {
				if _, err := tx.ExecContext(ctx,
					"INSERT INTO `task_run_wait_credit` (`run_id`,`blocked_ms`,`credited_ms`) "+
						"VALUES (?,?,?)",
					runID, credit.Blocked.Milliseconds(), credit.Credited.Milliseconds()); err != nil {
					return fmt.Errorf("recording %s's wait credit: %w", runID, err)
				}
			}
			if habit := t.CIHabit; habit != nil && !habit.Empty() {
				if _, err := tx.ExecContext(ctx,
					"INSERT INTO `task_run_ci_habit` (`run_id`,`narrow_test_before_push`,"+
						"`narrow_test_before_fix_push`,`pushed_after_red`,`selective`) VALUES (?,?,?,?,?)",
					runID, habit.NarrowTestBeforePush, habit.NarrowTestBeforeFixPush,
					habit.PushedAfterRed, habit.Selective); err != nil {
					return fmt.Errorf("recording %s's CI habits: %w", runID, err)
				}
			}
			if err := insertRunAPICallsTx(ctx, tx, runID, t.APICalls, time.Now().UTC()); err != nil {
				return err
			}
		}
		cutoff := time.Now().UTC().Add(-RunAPICallRetention)
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `task_run_api_call` WHERE `at` < ?", cutoff); err != nil {
			return fmt.Errorf("pruning run API calls older than %s: %w", cutoff.Format(time.RFC3339Nano), err)
		}
		return nil
	})
}

func insertRunAPICallsTx(ctx context.Context, tx *sql.Tx, runID string, calls []RunAPICall, now time.Time) error {
	for i, call := range calls {
		seq := call.Seq
		if seq == 0 {
			seq = i + 1
		}
		at := call.At
		if at.IsZero() {
			at = now
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task_run_api_call` (`run_id`,`seq`,`tool`,`target_ids`,`result_bytes`,`at`) "+
				"VALUES (?,?,?,?,?,?)",
			runID, seq, call.Tool, call.TargetIDs, call.ResultBytes,
			at.UTC()); err != nil {
			return fmt.Errorf("recording %s's API call %d (%s): %w", runID, seq, call.Tool, err)
		}
	}
	return nil
}

// RecordRunAPICalls replaces runID's task_run_api_call rows and prunes rows
// older than RunAPICallRetention.
func (s *Store) RecordRunAPICalls(ctx context.Context, runID string, calls []RunAPICall) error {
	return s.write(ctx, "record run "+runID+" API calls", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `task_run_api_call` WHERE `run_id` = ?", runID); err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := insertRunAPICallsTx(ctx, tx, runID, calls, now); err != nil {
			return err
		}
		cutoff := now.Add(-RunAPICallRetention)
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `task_run_api_call` WHERE `at` < ?", cutoff); err != nil {
			return fmt.Errorf("pruning run API calls older than %s: %w", cutoff.Format(time.RFC3339Nano), err)
		}
		return nil
	})
}

// PruneRunAPICalls deletes task_run_api_call rows whose timestamp is older
// than RunAPICallRetention before now.
func (s *Store) PruneRunAPICalls(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := now.UTC().Add(-RunAPICallRetention)
	return s.write(ctx, "prune run API calls", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `task_run_api_call` WHERE `at` < ?", cutoff)
		return err
	})
}

// RunAPICalls returns every row in task_run_api_call, ordered by (run_id, seq).
func (s *Store) RunAPICalls(ctx context.Context) ([]RunAPICall, error) {
	var out []RunAPICall
	err := each(ctx, s.db,
		"SELECT `run_id`,`seq`,`tool`,`target_ids`,`result_bytes`,`at` "+
			"FROM `task_run_api_call` ORDER BY `run_id`, `seq`", nil,
		func(rows *sql.Rows) error {
			var call RunAPICall
			if err := rows.Scan(&call.RunID, &call.Seq, &call.Tool, &call.TargetIDs,
				&call.ResultBytes, &call.At); err != nil {
				return err
			}
			call.At = call.At.UTC()
			out = append(out, call)
			return nil
		})
	return out, err
}

// RunAPICallsForRun returns runID's task_run_api_call rows in call order.
func (s *Store) RunAPICallsForRun(ctx context.Context, runID string) ([]RunAPICall, error) {
	var out []RunAPICall
	err := each(ctx, s.db,
		"SELECT `run_id`,`seq`,`tool`,`target_ids`,`result_bytes`,`at` "+
			"FROM `task_run_api_call` WHERE `run_id` = ? ORDER BY `seq`",
		[]any{runID},
		func(rows *sql.Rows) error {
			var call RunAPICall
			if err := rows.Scan(&call.RunID, &call.Seq, &call.Tool, &call.TargetIDs,
				&call.ResultBytes, &call.At); err != nil {
				return err
			}
			call.At = call.At.UTC()
			out = append(out, call)
			return nil
		})
	return out, err
}

// RecordRunReportCheck writes or replaces runID's steward report check row.
func (s *Store) RecordRunReportCheck(ctx context.Context, c RunReportCheck) error {
	if c.RunID == "" {
		return fmt.Errorf("RecordRunReportCheck: empty RunID")
	}
	return s.write(ctx, "record run "+c.RunID+" report check", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `task_run_report_check` WHERE `run_id` = ?", c.RunID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_run_report_check` (`run_id`,`claims`,`uncited`,`unread`,`mismatched`) "+
				"VALUES (?,?,?,?,?)",
			c.RunID, c.Claims, c.Uncited, c.Unread, c.Mismatched)
		return err
	})
}

// GetRunReportCheck returns runID's steward report check row, or (nil, false)
// when no check has been recorded for that run.
func (s *Store) GetRunReportCheck(ctx context.Context, runID string) (*RunReportCheck, bool, error) {
	var c RunReportCheck
	err := s.db.QueryRowContext(ctx,
		"SELECT `run_id`,`claims`,`uncited`,`unread`,`mismatched` "+
			"FROM `task_run_report_check` WHERE `run_id` = ?", runID).
		Scan(&c.RunID, &c.Claims, &c.Uncited, &c.Unread, &c.Mismatched)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &c, true, nil
}

// RunReportChecks returns every row in task_run_report_check, ordered by run_id.
func (s *Store) RunReportChecks(ctx context.Context) ([]RunReportCheck, error) {
	var out []RunReportCheck
	err := each(ctx, s.db,
		"SELECT `run_id`,`claims`,`uncited`,`unread`,`mismatched` "+
			"FROM `task_run_report_check` ORDER BY `run_id`", nil,
		func(rows *sql.Rows) error {
			var c RunReportCheck
			if err := rows.Scan(&c.RunID, &c.Claims, &c.Uncited, &c.Unread, &c.Mismatched); err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	return out, err
}

// RunWaitCredits is every run's CI-wait account, oldest run first -- the
// whole table, for the reason RunToolUses gives.
func (s *Store) RunWaitCredits(ctx context.Context) ([]RunWaitCredit, error) {
	var out []RunWaitCredit
	err := each(ctx, s.db,
		"SELECT `run_id`,`blocked_ms`,`credited_ms` FROM `task_run_wait_credit` "+
			"ORDER BY `run_id`", nil,
		func(rows *sql.Rows) error {
			var credit RunWaitCredit
			var blockedMS, creditedMS int64
			if err := rows.Scan(&credit.RunID, &blockedMS, &creditedMS); err != nil {
				return err
			}
			credit.Blocked = time.Duration(blockedMS) * time.Millisecond
			credit.Credited = time.Duration(creditedMS) * time.Millisecond
			out = append(out, credit)
			return nil
		})
	return out, err
}

// RunCIHabits is every run's RunCIHabit row, oldest run first -- the
// whole table, for the reason RunToolUses gives.
func (s *Store) RunCIHabits(ctx context.Context) ([]RunCIHabit, error) {
	var out []RunCIHabit
	err := each(ctx, s.db,
		"SELECT `run_id`,`narrow_test_before_push`,`narrow_test_before_fix_push`,"+
			"`pushed_after_red`,`selective` FROM `task_run_ci_habit` ORDER BY `run_id`", nil,
		func(rows *sql.Rows) error {
			var h RunCIHabit
			if err := rows.Scan(&h.RunID, &h.NarrowTestBeforePush, &h.NarrowTestBeforeFixPush,
				&h.PushedAfterRed, &h.Selective); err != nil {
				return err
			}
			out = append(out, h)
			return nil
		})
	return out, err
}

// RunToolUses is every census row ever recorded, oldest run first -- the
// whole table, for the reason TaskTimings gives: a report's window bounds
// which measurements it covers, not which rows it may read, and these
// rows carry no time of their own to filter on anyway. They are windowed
// against their run's own finished_at, which pkg/metrics joins in memory
// by RunTiming.RunID.
func (s *Store) RunToolUses(ctx context.Context) ([]RunToolUse, error) {
	var out []RunToolUse
	err := each(ctx, s.db,
		"SELECT `run_id`,`tool`,`calls`,`errored`,`timed_out`,`elided`,`result_bytes`,"+
			"`max_result_bytes`,`size_buckets`,`dropped_buckets` FROM `task_run_tool` "+
			"ORDER BY `run_id`, `tool`", nil,
		func(rows *sql.Rows) error {
			var use RunToolUse
			var buckets, dropped sql.NullString
			if err := rows.Scan(&use.RunID, &use.Tool, &use.Calls, &use.Errored, &use.TimedOut,
				&use.Elided, &use.ResultBytes, &use.MaxResultBytes, &buckets, &dropped); err != nil {
				return err
			}
			use.Sizes = DecodeSizeHistogram(buckets.String)
			use.Dropped = DecodeSizeHistogram(dropped.String)
			out = append(out, use)
			return nil
		})
	return out, err
}

// RunCheckWaits is every wait_for_checks call ever recorded, in the order
// each run made them -- see RunToolUses on why this is the whole table.
func (s *Store) RunCheckWaits(ctx context.Context) ([]RunCheckWait, error) {
	var out []RunCheckWait
	err := each(ctx, s.db,
		"SELECT `run_id`,`seq`,`verdict`,`waited_ms`,`pushes_before`,`provider` "+
			"FROM `task_run_check_wait` ORDER BY `run_id`, `seq`", nil,
		func(rows *sql.Rows) error {
			var wait RunCheckWait
			var waitedMS int64
			if err := rows.Scan(&wait.RunID, &wait.Seq, &wait.Verdict, &waitedMS,
				&wait.PushesBefore, &wait.Provider); err != nil {
				return err
			}
			wait.Waited = time.Duration(waitedMS) * time.Millisecond
			out = append(out, wait)
			return nil
		})
	return out, err
}

// RecordProxyToolCall records one proxy (/fs/ or /cli/) tool invocation against
// runID in task_run_tool so store.RunToolUses reflects proxy calls immediately
// and merges them with any end-of-run MCP telemetry passed to RecordRunTelemetry.
func (s *Store) RecordProxyToolCall(ctx context.Context, runID, tool string, resultBytes int64, errored bool) error {
	runID = strings.TrimSpace(runID)
	tool = strings.TrimSpace(tool)
	if s == nil || runID == "" || tool == "" {
		return nil
	}
	if resultBytes < 0 {
		resultBytes = 0
	}

	s.proxyToolsMu.Lock()
	if s.proxyTools == nil {
		s.proxyTools = make(map[string]map[string]RunToolUse)
	}
	byTool := s.proxyTools[runID]
	if byTool == nil {
		byTool = make(map[string]RunToolUse)
		s.proxyTools[runID] = byTool
	}
	use := byTool[tool]
	use.RunID = runID
	use.Tool = tool
	use.Calls++
	if errored {
		use.Errored++
	}
	use.ResultBytes += resultBytes
	if resultBytes > use.MaxResultBytes {
		use.MaxResultBytes = resultBytes
	}
	if use.Sizes == nil {
		use.Sizes = SizeHistogram{}
	}
	use.Sizes.Add(resultBytes)
	byTool[tool] = use
	sizesSnapshot := SizeHistogram{}
	sizesSnapshot.Merge(use.Sizes)
	useSnapshot := use
	useSnapshot.Sizes = sizesSnapshot
	s.proxyToolsMu.Unlock()

	return s.write(ctx, "record proxy tool "+tool+" for run "+runID, func(tx *sql.Tx) error {
		var existing RunToolUse
		var buckets, dropped sql.NullString
		err := tx.QueryRowContext(ctx,
			"SELECT `calls`,`errored`,`timed_out`,`elided`,`result_bytes`,`max_result_bytes`,`size_buckets`,`dropped_buckets` "+
				"FROM `task_run_tool` WHERE `run_id` = ? AND `tool` = ?",
			runID, tool).Scan(&existing.Calls, &existing.Errored, &existing.TimedOut,
			&existing.Elided, &existing.ResultBytes, &existing.MaxResultBytes, &buckets, &dropped)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx,
				"INSERT INTO `task_run_tool` (`run_id`,`tool`,`calls`,`errored`,`timed_out`,"+
					"`elided`,`result_bytes`,`max_result_bytes`,`size_buckets`,`dropped_buckets`) "+
					"VALUES (?,?,?,?,?,?,?,?,?,?)",
				runID, tool, useSnapshot.Calls, useSnapshot.Errored, useSnapshot.TimedOut, useSnapshot.Elided,
				useSnapshot.ResultBytes, useSnapshot.MaxResultBytes, useSnapshot.Sizes.Encode(), useSnapshot.Dropped.Encode())
			return err
		}
		existing.Sizes = DecodeSizeHistogram(buckets.String)
		existing.Dropped = DecodeSizeHistogram(dropped.String)
		existing.Calls++
		if errored {
			existing.Errored++
		}
		existing.ResultBytes += resultBytes
		if resultBytes > existing.MaxResultBytes {
			existing.MaxResultBytes = resultBytes
		}
		existing.Sizes.Add(resultBytes)
		_, err = tx.ExecContext(ctx,
			"UPDATE `task_run_tool` SET `calls` = ?, `errored` = ?, `result_bytes` = ?, `max_result_bytes` = ?, `size_buckets` = ? "+
				"WHERE `run_id` = ? AND `tool` = ?",
			existing.Calls, existing.Errored, existing.ResultBytes, existing.MaxResultBytes, existing.Sizes.Encode(),
			runID, tool)
		return err
	})
}

func (s *Store) dropProxyTools(batch map[string]RunTelemetry) {
	s.proxyToolsMu.Lock()
	defer s.proxyToolsMu.Unlock()
	for runID := range batch {
		delete(s.proxyTools, runID)
		delete(s.proxyCheckWaits, runID)
	}
}

func (s *Store) mergeWithProxyTools(runID string, tools []RunToolUse) []RunToolUse {
	if s == nil {
		return tools
	}
	s.proxyToolsMu.Lock()
	byTool := s.proxyTools[runID]
	if len(byTool) == 0 {
		s.proxyToolsMu.Unlock()
		return tools
	}
	proxyCopy := make(map[string]RunToolUse, len(byTool))
	for k, v := range byTool {
		cp := v
		cp.Sizes = SizeHistogram{}
		cp.Sizes.Merge(v.Sizes)
		cp.Dropped = SizeHistogram{}
		cp.Dropped.Merge(v.Dropped)
		proxyCopy[k] = cp
	}
	s.proxyToolsMu.Unlock()

	out := make([]RunToolUse, 0, len(tools)+len(proxyCopy))
	seen := make(map[string]bool, len(tools))
	for _, u := range tools {
		if p, ok := proxyCopy[u.Tool]; ok {
			seen[u.Tool] = true
			u.Calls += p.Calls
			u.Errored += p.Errored
			u.TimedOut += p.TimedOut
			u.Elided += p.Elided
			u.ResultBytes += p.ResultBytes
			if p.MaxResultBytes > u.MaxResultBytes {
				u.MaxResultBytes = p.MaxResultBytes
			}
			if u.Sizes == nil {
				u.Sizes = SizeHistogram{}
			}
			u.Sizes.Merge(p.Sizes)
			if u.Dropped == nil {
				u.Dropped = SizeHistogram{}
			}
			u.Dropped.Merge(p.Dropped)
		}
		out = append(out, u)
	}
	var extraNames []string
	for name := range proxyCopy {
		if !seen[name] {
			extraNames = append(extraNames, name)
		}
	}
	sort.Strings(extraNames)
	for _, name := range extraNames {
		out = append(out, proxyCopy[name])
	}
	return out
}
