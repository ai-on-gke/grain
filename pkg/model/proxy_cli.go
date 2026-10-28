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

// proxy_cli.go keeps what the sandbox proxy's acting /cli/ routes
// (pkg/gitproxy's cli_actions.go) record about a live run beyond a call
// count: each CI wait grain ci wait / run-job served, for
// task_run_check_wait, and the arguments of calls whose arguments are the
// product -- grain pr review-comment's findings, which the finish path
// relays exactly as it relays add_review_comment calls.
//
// Both are held per run in memory, beside the proxy's tool census
// (proxyTools), because that is where the end-of-run readers look:
// RecordBatchRunTelemetry rewrites a run's check waits from the run's own
// record and would otherwise drop the proxy's, and the review relay reads
// the run's tool calls once, when it finishes.

import (
	"context"
	"database/sql"
	"maps"
	"strings"
	"time"
)

// ProxyToolCall is one call the sandbox proxy served on a run's behalf,
// with the arguments it was made with, named after the MCP tool it stands
// in for.
type ProxyToolCall struct {
	Tool      string
	Arguments map[string]any
}

// RecordProxyCheckWait records one CI wait the sandbox proxy served for
// runID: held for the end-of-run telemetry write, and written now as well
// so the wait is counted even if that write never comes.
func (s *Store) RecordProxyCheckWait(ctx context.Context, runID, verdict string, waited time.Duration) error {
	runID, verdict = strings.TrimSpace(runID), strings.TrimSpace(verdict)
	if s == nil || runID == "" || verdict == "" {
		return nil
	}
	if waited < 0 {
		waited = 0
	}
	wait := RunCheckWait{RunID: runID, Verdict: verdict, Waited: waited}
	s.proxyToolsMu.Lock()
	if s.proxyCheckWaits == nil {
		s.proxyCheckWaits = make(map[string][]RunCheckWait)
	}
	s.proxyCheckWaits[runID] = append(s.proxyCheckWaits[runID], wait)
	s.proxyToolsMu.Unlock()

	return s.write(ctx, "record proxy CI wait for run "+runID, func(tx *sql.Tx) error {
		var seq sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			"SELECT MAX(`seq`) FROM `task_run_check_wait` WHERE `run_id` = ?", runID).Scan(&seq); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_run_check_wait` (`run_id`,`seq`,`verdict`,`waited_ms`,`pushes_before`) "+
				"VALUES (?,?,?,?,?)",
			runID, seq.Int64+1, verdict, waited.Milliseconds(), 0)
		return err
	})
}

// mergeWithProxyCheckWaits is waits followed by the proxy's own for
// runID, numbered on from the last of waits.
func (s *Store) mergeWithProxyCheckWaits(runID string, waits []RunCheckWait) []RunCheckWait {
	if s == nil {
		return waits
	}
	s.proxyToolsMu.Lock()
	extra := append([]RunCheckWait(nil), s.proxyCheckWaits[runID]...)
	s.proxyToolsMu.Unlock()
	if len(extra) == 0 {
		return waits
	}
	out := append([]RunCheckWait(nil), waits...)
	seq := 0
	for _, w := range waits {
		seq = max(seq, w.Seq)
	}
	for _, w := range extra {
		seq++
		w.Seq = seq
		out = append(out, w)
	}
	return out
}

// RecordProxyToolCallArgs keeps one call's arguments for runID's finish
// path (TakeProxyToolCalls).
func (s *Store) RecordProxyToolCallArgs(ctx context.Context, runID, tool string, args map[string]any) error {
	runID, tool = strings.TrimSpace(runID), strings.TrimSpace(tool)
	if s == nil || runID == "" || tool == "" {
		return nil
	}
	s.proxyToolsMu.Lock()
	defer s.proxyToolsMu.Unlock()
	if s.proxyCalls == nil {
		s.proxyCalls = make(map[string][]ProxyToolCall)
	}
	s.proxyCalls[runID] = append(s.proxyCalls[runID], ProxyToolCall{Tool: tool, Arguments: maps.Clone(args)})
	return nil
}

// TakeProxyToolCalls returns and forgets the calls RecordProxyToolCallArgs
// kept for runID, in the order they were made.
func (s *Store) TakeProxyToolCalls(runID string) []ProxyToolCall {
	if s == nil {
		return nil
	}
	s.proxyToolsMu.Lock()
	defer s.proxyToolsMu.Unlock()
	calls := s.proxyCalls[runID]
	delete(s.proxyCalls, runID)
	return calls
}

// RecordProxyAPICall records one audited proxy/API/capability call in
// task_run_api_call immediately and holds it in memory so end-of-run
// RecordBatchRunTelemetry merges it with any MCP tool calls.
func (s *Store) RecordProxyAPICall(ctx context.Context, runID, tool, targetIDs string, resultBytes int64) error {
	runID, tool = strings.TrimSpace(runID), strings.TrimSpace(tool)
	if s == nil || runID == "" || tool == "" {
		return nil
	}
	if resultBytes < 0 {
		resultBytes = 0
	}
	now := time.Now().UTC()
	call := RunAPICall{
		RunID:       runID,
		Tool:        tool,
		TargetIDs:   strings.TrimSpace(targetIDs),
		ResultBytes: resultBytes,
		At:          now,
	}
	s.proxyToolsMu.Lock()
	if s.proxyAPICalls == nil {
		s.proxyAPICalls = make(map[string][]RunAPICall)
	}
	s.proxyAPICalls[runID] = append(s.proxyAPICalls[runID], call)
	s.proxyToolsMu.Unlock()

	return s.write(ctx, "record proxy API call "+tool+" for run "+runID, func(tx *sql.Tx) error {
		var seq sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			"SELECT MAX(`seq`) FROM `task_run_api_call` WHERE `run_id` = ?", runID).Scan(&seq); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_run_api_call` (`run_id`,`seq`,`tool`,`target_ids`,`result_bytes`,`at`) "+
				"VALUES (?,?,?,?,?,?)",
			runID, seq.Int64+1, call.Tool, call.TargetIDs, call.ResultBytes, call.At)
		return err
	})
}

func (s *Store) mergeWithProxyAPICalls(runID string, calls []RunAPICall) []RunAPICall {
	if s == nil {
		return calls
	}
	s.proxyToolsMu.Lock()
	extra := append([]RunAPICall(nil), s.proxyAPICalls[runID]...)
	s.proxyToolsMu.Unlock()
	if len(extra) == 0 {
		return calls
	}
	out := append([]RunAPICall(nil), calls...)
	seq := 0
	for _, c := range calls {
		seq = max(seq, c.Seq)
	}
	for _, c := range extra {
		seq++
		c.Seq = seq
		out = append(out, c)
	}
	return out
}

// ActiveRunID returns the ID of taskID's currently live run, or "" if none is live.
func (s *Store) ActiveRunID(ctx context.Context, taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if s == nil || taskID == "" {
		return ""
	}
	var runID string
	if err := s.db.QueryRowContext(ctx,
		"SELECT `id` FROM `task_run` WHERE `task_id` = ? AND `finished_at` IS NULL "+
			"ORDER BY `started_at` DESC LIMIT 1", taskID).Scan(&runID); err != nil {
		return ""
	}
	return runID
}

// WriteTaskHandover writes or replaces the standing handover summary for taskID.
// When called more than once during the same live run, the existing handover
// comment written in that run is updated in place so the last call in a run wins.
func (s *Store) WriteTaskHandover(ctx context.Context, taskID, runID, body string) error {
	taskID = strings.TrimSpace(taskID)
	runID = strings.TrimSpace(runID)
	body = strings.TrimSpace(body)
	if s == nil || taskID == "" || body == "" {
		return nil
	}
	existing, err := s.Comments(ctx, taskID)
	if err != nil {
		return err
	}
	var runStart time.Time
	if runID != "" {
		_ = s.db.QueryRowContext(ctx,
			"SELECT `started_at` FROM `task_run` WHERE `id` = ?", runID).Scan(&runStart)
	}
	for i := len(existing) - 1; i >= 0; i-- {
		if existing[i].Handover {
			if strings.TrimSpace(existing[i].Body) == body {
				return nil
			}
			if !runStart.IsZero() && !existing[i].CreatedAt.Before(runStart) {
				targetID := existing[i].ID
				now := time.Now().UTC()
				return s.write(ctx, "update handover on task "+taskID, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx,
						"UPDATE `task_comment` SET `body` = ?, `created_at` = ? WHERE `id` = ?",
						body, now, targetID)
					return err
				})
			}
			break
		}
	}
	agentPrincipal := Principal{Kind: PrincipalAgent, ID: taskID}
	_, err = s.AddComment(ctx, Comment{
		TaskID: taskID,
		Author: Attribution{
			Actor:      Principal{Kind: PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &agentPrincipal,
		},
		Body:      body,
		CreatedAt: time.Now().UTC(),
		Handover:  true,
	})
	return err
}
