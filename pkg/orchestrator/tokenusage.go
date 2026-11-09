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

package orchestrator

import (
	"context"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// recordRunUsage writes what a finished run spent at its provider
// (agent.Result.Usage) to task_run_usage, keyed by the same connection
// and model a usage-limit pause on the run would be filed under
// (PauseScope), so the budget a pause refuses and the usage that spent
// it are counted against the same thing. A run whose CLI reported no
// usage writes nothing. Best-effort: accounting that cannot be written
// is logged, never a failure of the run.
func recordRunUsage(ctx context.Context, store *model.Store, taskID, runID string, scope PauseScope, result *agent.Result, started, finished time.Time) {
	if store == nil || result == nil || !result.Usage.Reported() {
		return
	}
	u := result.Usage
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := store.RecordRunUsage(writeCtx, model.RunUsage{
		RunID: runID, TaskID: taskID, Connection: scope.Connection, Team: scope.Team, Model: scope.Model, Framework: scope.Framework,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens,
		CostUSD: u.CostUSD, Turns: u.Turns, StartedAt: started, FinishedAt: finished,
	}); err != nil {
		log.Printf("orchestrator: run %s: recording its token usage: %v", runID, err)
	}
}
