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

package orchestrator_test

// An interactive session continued in-process between turns
// (continueSessionTurns): what each turn is shown, how the idle wait
// wakes, what a daemon adopting the run relays, and how the run's own
// clock bounds the wait.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// inProcessSession files the director session on a fresh store and
// returns it, with the database under the store for a test that needs a
// second Store over it (another process's writes).
func inProcessSession(t *testing.T) (*model.Store, *sql.DB, model.Task) {
	t.Helper()
	orchestrator.LogToTest(t)
	store, db := sqlitetest.Open(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.SessionSandboxIdleTTL = 30 * time.Minute
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	return store, db, sessionTasks(t, store)[0]
}

func inProcessDeps(store *model.Store, fw agent.Framework, ttl, poll time.Duration) orchestrator.Deps {
	return orchestrator.Deps{
		Store:      store,
		Sandboxes:  orchestrator.NewHostSandboxes(os.TempDir()),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 1,
		Config: orchestrator.Config{
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New()),
			SessionSandboxIdleTTL: ttl,
			WaitForSessionReply:   true,
			CancelPollInterval:    poll,
		},
	}
}

func sessionAnswer(turn int) *agent.Result {
	return &agent.Result{
		FinalText:        fmt.Sprintf("in-process answer %d", turn),
		Transcript:       fmt.Sprintf("in-process answer %d", turn),
		ResumableSession: "sess-live-1",
	}
}

// A person's message posted after the turn's last tool call, while it
// was writing its answer, reached the turn neither in its prompt nor as
// an addendum on a tool result. It is not counted as delivered: it wakes
// the next turn at once, which is shown it -- and not the message the
// turn was handed as an addendum (Observation.DeliveredCommentID).
func TestInProcessSessionTurnIsShownAMessagePostedAfterItsLastToolCall(t *testing.T) {
	t.Parallel()
	store, _, director := inProcessSession(t)
	ctx := t.Context()

	ag := &recordingSessionAgent{
		onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
			if turn == 1 {
				// Seen mid-turn: the forked mcpserver handed it over on a
				// tool result and reported it delivered.
				seen, err := store.AddComment(ctx, model.Comment{TaskID: director.ID, Author: aliceSays,
					Body: "a message the turn was handed", CreatedAt: baseTime.Add(90 * time.Second)})
				if err != nil {
					return nil, err
				}
				if err := store.AdvanceDelivered(ctx, director.ID, seen, baseTime.Add(90*time.Second)); err != nil {
					return nil, err
				}
				// After the last tool call: nothing carries it to this turn.
				if _, err := store.AddComment(ctx, model.Comment{TaskID: director.ID, Author: aliceSays,
					Body: "a message posted while it wrote its answer", CreatedAt: baseTime.Add(100 * time.Second)}); err != nil {
					return nil, err
				}
			}
			return sessionAnswer(turn), nil
		},
	}
	// A TTL far longer than the test: the turn must not wait for a
	// message that is already there.
	deps := inProcessDeps(store, ag, 30*time.Minute, 20*time.Millisecond)

	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)
	done := make(chan error, 1)
	go func() { done <- orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)) }()

	// Turn 2 answers, and then the session waits for a third message:
	// close the task once turn 2 has run so the cycle returns.
	deadline := time.After(10 * time.Second)
	for {
		comments, err := store.Comments(ctx, director.ID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fmt.Sprint(comments), "in-process answer 2") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no second turn: framework.Run calls = %d; the message posted during the answer was dropped", len(ag.configs))
		case <-time.After(10 * time.Millisecond):
		}
	}
	closeTask(t, ctx, store, director.ID, time.Now())
	if err := <-done; err != nil {
		t.Fatalf("cycle: %v", err)
	}

	if len(ag.configs) < 2 {
		t.Fatalf("framework.Run calls = %d, want the message to wake a second turn", len(ag.configs))
	}
	prompt := ag.configs[1].ContinuePrompt
	if !strings.Contains(prompt, "a message posted while it wrote its answer") {
		t.Fatalf("turn 2 ContinuePrompt = %q, want the message turn 1 never saw", prompt)
	}
	if strings.Contains(prompt, "a message the turn was handed") {
		t.Fatalf("turn 2 ContinuePrompt = %q, want the addendum turn 1 was already handed left out", prompt)
	}
}

// The idle wait polls at the deployment's own interval
// (Config.CancelPollInterval), not at a fixed 25ms: a message another
// process writes is found at the next poll and no sooner. One written
// through this process's own Store wakes the wait at once, however long
// the interval is (Store.CommentAdded).
func TestInProcessSessionWaitPollsAtTheConfiguredInterval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		poll time.Duration
		// otherProcess writes the message through a second Store over
		// the same database, which signals nothing in this one.
		otherProcess bool
		min, max     time.Duration
	}{
		{name: "another process's message waits for the poll", poll: 600 * time.Millisecond, otherProcess: true,
			min: 300 * time.Millisecond, max: 5 * time.Second},
		{name: "this process's message wakes the wait", poll: time.Hour,
			max: 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, db, director := inProcessSession(t)
			ctx := t.Context()
			writer := store
			if tc.otherProcess {
				writer = model.New(db)
			}

			var mu sync.Mutex
			var posted, woke time.Time
			ag := &recordingSessionAgent{
				onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
					switch turn {
					case 1:
						go func() {
							// Not until the wait has made its first poll:
							// it marks the question pending right after
							// that read, and a message already there at
							// it is found at once -- which, behind a fixed
							// sleep, a loaded -race run kept doing.
							for {
								o, err := store.GetObservation(ctx, director.ID)
								if err == nil && o != nil && o.PendingQuestionCommentID != nil {
									break
								}
								if ctx.Err() != nil {
									return
								}
								time.Sleep(5 * time.Millisecond)
							}
							mu.Lock()
							posted = time.Now()
							mu.Unlock()
							_, _ = writer.AddComment(ctx, model.Comment{TaskID: director.ID, Author: aliceSays,
								Body: "second message", CreatedAt: baseTime.Add(2 * time.Minute)})
						}()
					case 2:
						mu.Lock()
						woke = time.Now()
						mu.Unlock()
					}
					return sessionAnswer(turn), nil
				},
			}
			// The TTL ends the wait after turn 2.
			deps := inProcessDeps(store, ag, 2*time.Second, tc.poll)
			deps.Config.CancelPollInterval = tc.poll

			t1 := baseTime.Add(time.Minute)
			replyToSession(t, store, director.ID, t1)
			done := make(chan error, 1)
			go func() { done <- orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)) }()
			waitUntil(t, 8*time.Second, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return !woke.IsZero()
			})
			closeTask(t, ctx, store, director.ID, time.Now())
			if err := <-done; err != nil {
				t.Fatalf("cycle: %v", err)
			}
			mu.Lock()
			gap := woke.Sub(posted)
			mu.Unlock()
			if gap < tc.min || gap > tc.max {
				t.Fatalf("turn 2 started %v after the message, want between %v and %v (poll interval %v)",
					gap, tc.min, tc.max, tc.poll)
			}
		})
	}
}

func waitUntil(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %v", limit)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A daemon that adopts a session's run part-way through a later turn
// parses every turn's calls back out of the run's transcript. The answer
// and the proposal the earlier turn relayed in-process are on the task
// already (task_run_relayed_call), so only the turn in flight is relayed:
// no second copy of either.
func TestAdoptedInProcessSessionRelaysOnlyTheTurnInFlight(t *testing.T) {
	t.Parallel()
	store, _, director := inProcessSession(t)
	ctx := t.Context()

	answer1 := agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "first answer"}}
	proposal := agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
		"title": "Follow up on the first answer", "body": "Something the first turn found."}}
	answer2 := agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "second answer"}}

	ag := &recordingSessionAgent{
		onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
			if turn == 1 {
				go func() {
					time.Sleep(50 * time.Millisecond)
					_, _ = store.AddComment(ctx, model.Comment{TaskID: director.ID, Author: aliceSays,
						Body: "and then?", CreatedAt: baseTime.Add(2 * time.Minute)})
				}()
				return &agent.Result{ToolCalls: []agent.ToolCall{answer1, proposal},
					ResumableSession: "sess-live-1"}, nil
			}
			// The daemon stops mid-turn, leaving the turn's CLI in its
			// run host for the next daemon to adopt.
			return nil, fmt.Errorf("claude: %w", runhost.ErrDetached)
		},
	}
	deps := inProcessDeps(store, ag, 5*time.Second, 20*time.Millisecond)
	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)
	_ = orchestrator.RunCycle(ctx, deps, t1.Add(time.Second))
	if len(ag.configs) != 2 {
		t.Fatalf("framework.Run calls = %d, want turn 1 and the detached turn 2", len(ag.configs))
	}
	runs, err := store.Runs(ctx, director.ID)
	if err != nil || len(runs) != 1 || runs[0].FinishedAt != nil {
		t.Fatalf("runs = %+v (%v), want the one run left live for adoption", runs, err)
	}
	run := runs[0]
	if n := countComments(t, store, director.ID, "first answer"); n != 1 {
		t.Fatalf("first answer relayed %d times in-process, want once", n)
	}

	// The next daemon: turn 2's run host has finished, and the framework
	// reads the whole run -- turn 1's calls and turn 2's -- back.
	spool := t.TempDir()
	if err := os.WriteFile(filepath.Join(spool, "stdout"), []byte("turn2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exit, _ := json.Marshal(runhost.Exit{Code: 0, FinishedAt: time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(spool, "exit.json"), exit, 0o600); err != nil {
		t.Fatal(err)
	}
	info := runhost.Info{Name: "rh", Labels: runhost.Labels{Deployment: "d", Task: director.ID, Run: run.ID,
		Protocol: runhost.Protocol, Spool: spool}}
	driver := &listedHosts{hosts: []runhost.Info{info}}
	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())
	sb, err := sandboxes.Acquire(ctx, run.ID, orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	fw := parsingFramework{parsed: make(chan string, 1), earlier: make(chan string, 1),
		result: &agent.Result{ToolCalls: []agent.ToolCall{answer1, proposal, answer2}, ResumableSession: "sess-live-1"}}
	adoptDeps := orchestrator.Deps{Store: store, Sandboxes: sandboxes, Framework: orchestrator.StaticFramework(fw),
		Config: orchestrator.Config{
			StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities: model.NewCapabilityRegistry(durable.New()),
		}}
	host := &runhost.Host{Driver: driver, Deployment: "d", Poll: 5 * time.Millisecond}
	adopted := orchestrator.AdoptedRun{Adoption: runhost.Adoption{
		Claim: runhost.Claim{Run: run.ID, Task: director.ID}, Host: info}, Sandbox: sb}
	if err := orchestrator.ResumeAdoptedRun(ctx, adoptDeps, host, adopted); err != nil {
		t.Fatalf("ResumeAdoptedRun: %v", err)
	}

	if n := countComments(t, store, director.ID, "first answer"); n != 1 {
		t.Fatalf("first answer posted %d times after adoption, want once", n)
	}
	if n := countComments(t, store, director.ID, "second answer"); n != 1 {
		t.Fatalf("second answer posted %d times after adoption, want once", n)
	}
	var filed int
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Title == "Follow up on the first answer" {
			filed++
		}
	}
	if filed != 1 {
		t.Fatalf("proposal filed %d times after adoption, want once", filed)
	}
	relayed, err := store.RelayedCalls(ctx, run.ID)
	if err != nil || len(relayed) != 0 {
		t.Fatalf("relayed calls after the run finished = %v (%v), want none left", relayed, err)
	}
}

func countComments(t *testing.T, store *model.Store, taskID, body string) int {
	t.Helper()
	comments, err := store.Comments(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, c := range comments {
		if strings.TrimSpace(c.Body) == body {
			n++
		}
	}
	return n
}
