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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// countingTurns counts the turns it is given and remembers the first
// one's prompt.
type countingTurns struct {
	runs   atomic.Int32
	prompt atomic.Value
}

func (c *countingTurns) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	if c.runs.Add(1) == 1 {
		c.prompt.Store(cfg.ContinuePrompt)
	}
	return &agent.Result{FinalText: "next answer", ResumableSession: "sess-1"}, nil
}

// The idle wait of an in-process session is not credited to its run's
// clock (a console session's is unbounded; an ordinary interactive
// task's is not). A run whose deadline is nearer than the idle TTL plus a
// turn's reserve relays its answer and stops, rather than waiting and
// starting a turn the wall clock could cancel mid-answer; the next
// message is a fresh dispatch's. With room on the clock the same run
// waits and answers in-process.
func TestInProcessSessionNearItsDeadlineEndsTheRunRatherThanWaiting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		remaining time.Duration
		waits     bool
	}{
		{name: "near the deadline", remaining: 10 * time.Minute},
		{name: "room on the clock", remaining: 8 * time.Hour, waits: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openTestStore(t)
			now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
			task := model.Task{ID: "7", Title: "chat", Interactive: true}
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			if err := store.SetTaskGoal(ctx, task.ID, model.GoalDecl{Kind: model.GoalPersonCloses}, "test", now); err != nil {
				t.Fatal(err)
			}
			cfg := Config{SessionSandboxIdleTTL: 2 * time.Second, CancelPollInterval: 20 * time.Millisecond,
				Now: func() time.Time { return now }}
			cfg.inProcessSessionWait = true
			fw := &countingTurns{}
			deadline := time.Now().Add(tc.remaining)

			// A person writes as soon as the answer is up; only a session
			// that waits sees it.
			go func() {
				time.Sleep(100 * time.Millisecond)
				_, _ = store.AddComment(ctx, model.Comment{TaskID: task.ID,
					Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
					Body:   "and another thing", CreatedAt: now.Add(time.Minute)})
			}()
			first := &agent.Result{FinalText: "first answer", ResumableSession: "sess-1"}
			start := time.Now()
			result, runErr, _ := continueSessionTurns(ctx, ctx, store, cfg, fw, agent.RunConfig{}, task, "7-1",
				func() time.Time { return deadline }, nil, first, nil)
			if runErr != nil {
				t.Fatalf("continueSessionTurns: %v", runErr)
			}
			if !tc.waits {
				if got := fw.runs.Load(); got != 0 {
					t.Fatalf("in-process turns = %d, want none", got)
				}
				if elapsed := time.Since(start); elapsed >= cfg.SessionSandboxIdleTTL {
					t.Fatalf("returned after %v, want no idle wait", elapsed)
				}
				if result.FinalText != "first answer" {
					t.Fatalf("result = %q, want the first turn's", result.FinalText)
				}
			} else if prompt, _ := fw.prompt.Load().(string); !strings.Contains(prompt, "and another thing") {
				t.Fatalf("in-process turns = %d, first prompt %q; want the message answered in-process", fw.runs.Load(), prompt)
			}
			comments, err := store.Comments(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(commentBodies(comments), "first answer") {
				t.Fatalf("comments = %q, want the first answer relayed either way", commentBodies(comments))
			}
		})
	}
}

func commentBodies(comments []model.Comment) string {
	var b strings.Builder
	for _, c := range comments {
		b.WriteString(c.Body)
		b.WriteString("\n")
	}
	return b.String()
}
