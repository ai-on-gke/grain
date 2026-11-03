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

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestResumeLaterNearTheDeadlineDefersTheTask walks
// docs/test-plans/2026-11-02.md §7 gap 6 (PR #248): a run inside
// mcp.RunDeadlineNoticeWindow of its deadline -- so the real mcpserver
// appends the deadline notice to every tool result, resume_later's
// included -- calls resume_later with a when. The orchestrator reads the
// condition back past that notice and defers the task on it, rather than
// taking the run for one that simply ended.
func TestResumeLaterNearTheDeadlineDefersTheTask(t *testing.T) {
	w := newDeliveryWorld(t)
	w.mcpArgs = func(agent.RunConfig) []string {
		return []string{"-run-deadline", time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)}
	}
	var resumeText string
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		if out := w.call(p, "run_command", map[string]any{"command": "true"}); !strings.Contains(out, "left before grain cancels this run") {
			t.Errorf("run_command 5m from the deadline answered %q, want the deadline notice", out)
		}
		args := map[string]any{"reason": "out of time: carry on next run", "when": map[string]any{"at": "1h"}}
		resumeText = w.call(p, "resume_later", args)
		return &agent.Result{ToolCalls: []agent.ToolCall{{Name: "resume_later", Arguments: args, Text: resumeText}}}
	}
	id := w.createTask(`{"title":"resume_later deadline walkthrough","description":"hello","noRepo":true,"approved":true}`)
	s := w.settle(id, 1)
	t.Logf("resume_later answered:\n%s", resumeText)
	t.Logf("after the run: %s", s)
	if !strings.Contains(resumeText, "resume_on: ") || !strings.Contains(resumeText, "left before grain cancels this run") {
		t.Fatalf("resume_later's result %q, want its condition with the deadline notice after it", resumeText)
	}
	if _, ok := mcp.ResumeLaterTaken(resumeText); !ok {
		t.Errorf("ResumeLaterTaken could not read the condition back from %q", resumeText)
	}
	if s.State != model.StateDeferred {
		t.Errorf("after resume_later with when.at near the deadline: %s, want deferred", s)
	}
	task, err := w.store.GetTask(w.ctx, id)
	if err != nil || task == nil {
		t.Fatalf("reading task %s: %v", id, err)
	}
	if task.DeferredUntil == nil || time.Until(*task.DeferredUntil) < 50*time.Minute || time.Until(*task.DeferredUntil) > 70*time.Minute {
		t.Errorf("task %s deferred until %v, want about an hour from now", id, task.DeferredUntil)
	}
}
