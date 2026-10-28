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
