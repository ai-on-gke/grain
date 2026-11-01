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

type trackingWarmSandbox struct {
	Sandbox
	released atomic.Int32
}

func (s *trackingWarmSandbox) Release(context.Context) error {
	s.released.Add(1)
	return nil
}

func TestEvictOldestWarmSessionSandboxesEvictsInIdleOrderAndMarksReclaimed(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	now := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)

	var revoked []string
	deps := Deps{
		Store: store,
		RevokeSandboxToken: func(id string) error {
			revoked = append(revoked, id)
			return nil
		},
	}

	sb1 := &trackingWarmSandbox{}
	sb2 := &trackingWarmSandbox{}
	sb3 := &trackingWarmSandbox{}
	var counted1, counted2, counted3 atomic.Int32

	// t2 is oldest (now - 10m); t1 and t3 share IdleSince (now - 5m), so tie-break picks t1 before t3.
	parkWarmSessionSandbox(deps, "t1", "sb-1", "k1", "", sb1, func(context.Context) { counted1.Add(1) }, model.Commitment{}, now.Add(-5*time.Minute), time.Hour)
	parkWarmSessionSandbox(deps, "t2", "sb-2", "k2", "", sb2, func(context.Context) { counted2.Add(1) }, model.Commitment{}, now.Add(-10*time.Minute), time.Hour)
	parkWarmSessionSandbox(deps, "t3", "sb-3", "k3", "", sb3, func(context.Context) { counted3.Add(1) }, model.Commitment{}, now.Add(-5*time.Minute), time.Hour)

	if got := EvictOldestWarmSessionSandboxes(deps, 2); got != 2 {
		t.Fatalf("EvictOldestWarmSessionSandboxes = %d, want 2", got)
	}
	if sb2.released.Load() != 1 || counted2.Load() != 1 {
		t.Errorf("oldest sandbox t2 not released/counted: released=%d counted=%d", sb2.released.Load(), counted2.Load())
	}
	if sb1.released.Load() != 1 || counted1.Load() != 1 {
		t.Errorf("tie-broken sandbox t1 not released/counted: released=%d counted=%d", sb1.released.Load(), counted1.Load())
	}
	if sb3.released.Load() != 0 || counted3.Load() != 0 {
		t.Errorf("newest sandbox t3 should remain warm: released=%d counted=%d", sb3.released.Load(), counted3.Load())
	}
	if len(revoked) != 2 || revoked[0] != "sb-2" || revoked[1] != "sb-1" {
		t.Errorf("revoked tokens = %v, want [sb-2 sb-1]", revoked)
	}

	// Evicted tasks report wasReclaimed=true when later claimed.
	if entry, reclaimed := claimWarmSessionSandbox(deps, "t2", now); entry != nil || !reclaimed {
		t.Errorf("claimWarmSessionSandbox(t2) = (%v, %v), want (nil, true)", entry, reclaimed)
	}
	if entry, reclaimed := claimWarmSessionSandbox(deps, "t1", now); entry != nil || !reclaimed {
		t.Errorf("claimWarmSessionSandbox(t1) = (%v, %v), want (nil, true)", entry, reclaimed)
	}
	// Remaining warm sandbox t3 is still claimable.
	entry, reclaimed := claimWarmSessionSandbox(deps, "t3", now)
	if entry == nil || entry.SandboxID != "sb-3" || reclaimed {
		t.Fatalf("claimWarmSessionSandbox(t3) = (%+v, %v), want sb-3 and reclaimed=false", entry, reclaimed)
	}
	releaseWarmSandboxEntry(context.Background(), entry)
}

func TestAddendaPollerUsesMaxSeenIDAndStreamsNewCommentsOnce(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	now := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)
	task := model.Task{ID: "t-addenda", Title: "session"}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	addComment := func(author, body string) model.Comment {
		c := model.Comment{
			TaskID:    task.ID,
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: author}},
			Body:      body,
			CreatedAt: now,
		}
		id, err := store.AddComment(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		c.ID = id
		return c
	}

	c1 := addComment("alice", "initial context")
	_ = addComment("bob", "middle comment")
	c3 := addComment("alice", "latest prompt comment")

	// Pass seen out of ID order (c3 before c1): lastCommentID must still pick c3.ID
	// and never replay c2 or c3 on the first poll.
	poll := addendaPoller(store, task.ID, []model.Comment{c3, c1})
	initial, err := poll(ctx)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(initial) != 0 {
		t.Fatalf("first poll returned %v, want empty when nothing newer than max(seen.ID) arrived", initial)
	}

	// Two new comments arrive between polls; both are delivered in order on the next poll and not repeated.
	_ = addComment("alice", "first mid-run addendum")
	_ = addComment("carol", "second mid-run addendum")

	batch, err := poll(ctx)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(batch) != 2 || !strings.Contains(batch[0], "first mid-run addendum") || !strings.Contains(batch[1], "second mid-run addendum") {
		t.Fatalf("second poll = %v, want both new addenda in order", batch)
	}

	again, err := poll(ctx)
	if err != nil {
		t.Fatalf("third poll: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("third poll = %v, want empty after cursor advanced", again)
	}
}
