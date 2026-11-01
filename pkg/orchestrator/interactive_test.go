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

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestInteractiveMultiTurnConversationMixingChatAndPlainTextExits drives a
// 5+ turn interactive conversation mixing chat tool calls and plain-text turn
// exits, verifying that every operator message gets a reply without needing
// to be sent twice and that warm session sandboxes and session tokens carry
// across both in-process turns and separate dispatches.
func TestInteractiveMultiTurnConversationMixingChatAndPlainTextExits(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.SessionSandboxIdleTTL = 30 * time.Minute
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	now := baseTime.Add(time.Minute)
	var nowMu sync.Mutex
	currentNow := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	advance := func(d time.Duration) time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		now = now.Add(d)
		return now
	}

	userClient := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	userClient.Now = currentNow

	agentClient := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("grain"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	agentClient.Now = currentNow

	sendUser := func(msg string) {
		t.Helper()
		advance(time.Second)
		if err := userClient.AddComment(ctx, director.ID, msg, nil); err != nil {
			t.Fatalf("AddComment(%q): %v", msg, err)
		}
	}

	// We drive 5 user messages across 2 dispatches:
	// - Dispatch 1:
	//   * Turn 1: user "msg-1" -> agent calls chat("chat-reply-1") -> user "msg-2" during chat ->
	//     agent finishes Turn 1 with plain text "plain-reply-2".
	//   * Continue in-process (WaitForSessionReply): session waits in StateAwaitingReply ->
	//     user posts "msg-3" -> agent resumes in-process and finishes Turn 2 with plain text "plain-reply-3",
	//     then we cancel the in-process wait after Turn 2 relays so the run finishes and parks the sandbox warm.
	// - Dispatch 2 (on the warm sandbox):
	//   * User posts "msg-4" while session is parked between runs -> RunCycle dispatches run 2 on the warm sandbox ->
	//     agent calls chat("chat-reply-4") -> user posts "msg-5" -> agent finishes with comment_on_issue("final-reply-5").
	turn1ChatPosted := make(chan struct{})
	turn2Relayed := make(chan struct{})
	turn4ChatPosted := make(chan struct{})

	var mu sync.Mutex
	var callIndex int
	var resumeTokens []string
	var reusedFlags []bool

	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		mu.Lock()
		callIndex++
		idx := callIndex
		resumeTokens = append(resumeTokens, cfg.ResumeSession)
		reusedFlags = append(reusedFlags, cfg.SandboxReused)
		mu.Unlock()

		switch idx {
		case 1:
			// Turn 1: simulate chat("chat-reply-1") waiting for "msg-2", then exit with plain text "plain-reply-2".
			posted, err := agentClient.PostAgentMessage(runCtx, director.ID, "chat-reply-1", "chat-reply-1", nil)
			if err != nil {
				return nil, err
			}
			close(turn1ChatPosted)
			var reply *model.Comment
			for {
				comments, err := store.Comments(runCtx, director.ID)
				if err != nil {
					return nil, err
				}
				for i := range comments {
					c := &comments[i]
					if c.ID > posted.ID && c.Author.Actor.Kind == model.PrincipalHuman {
						reply = c
						break
					}
				}
				if reply != nil {
					break
				}
				select {
				case <-runCtx.Done():
					return nil, runCtx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err := agentClient.RecordDelivered(runCtx, director.ID, reply.ID); err != nil {
				return nil, err
			}
			return &agent.Result{
				FinalText:        "plain-reply-2: got " + reply.Body,
				ResumableSession: "sess-token-1",
			}, nil

		case 2:
			// Turn 2 (in-process continuation after plain-text exit): receives "msg-3" in ContinuePrompt.
			if !strings.Contains(cfg.ContinuePrompt, "msg-3") {
				return nil, fmt.Errorf("turn 2 ContinuePrompt = %q, want msg-3", cfg.ContinuePrompt)
			}
			go func() {
				// Wait until Turn 2's plain-text answer is relayed and the session enters its in-process wait,
				// then post "msg-4" after we let the run finish.
				for {
					comments, _ := store.Comments(ctx, director.ID)
					found := false
					for _, c := range comments {
						if strings.Contains(c.Body, "plain-reply-3") {
							found = true
							break
						}
					}
					if found {
						close(turn2Relayed)
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
			return &agent.Result{
				FinalText:        "plain-reply-3",
				ResumableSession: "sess-token-2",
			}, nil

		case 3:
			// Dispatch 2: receives "msg-4", calls chat("chat-reply-4") waiting for "msg-5", then finishes with comment_on_issue.
			posted, err := agentClient.PostAgentMessage(runCtx, director.ID, "chat-reply-4", "chat-reply-4", nil)
			if err != nil {
				return nil, err
			}
			close(turn4ChatPosted)
			var reply *model.Comment
			for {
				comments, err := store.Comments(runCtx, director.ID)
				if err != nil {
					return nil, err
				}
				for i := range comments {
					c := &comments[i]
					if c.ID > posted.ID && c.Author.Actor.Kind == model.PrincipalHuman {
						reply = c
						break
					}
				}
				if reply != nil {
					break
				}
				select {
				case <-runCtx.Done():
					return nil, runCtx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err := agentClient.RecordDelivered(runCtx, director.ID, reply.ID); err != nil {
				return nil, err
			}
			return &agent.Result{
				ToolCalls: []agent.ToolCall{{
					Name:      "comment_on_issue",
					Arguments: map[string]any{"comment": "final-reply-5: got " + reply.Body},
				}},
				ResumableSession: "sess-token-3",
			}, nil

		default:
			return nil, fmt.Errorf("unexpected agent call %d", idx)
		}
	})

	sbx := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
	runs := &orchestrator.InFlight{}
	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  orchestrator.StaticFramework(fw),
		Runs:       runs,
		MaxWorkers: 2,
		Config: orchestrator.Config{
			Now:                   currentNow,
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New()),
			SessionSandboxIdleTTL: 300 * time.Millisecond,
			CancelPollInterval:    10 * time.Millisecond,
		},
	}

	// Step 1: User sends "msg-1" and RunCycle dispatches run 1.
	sendUser("msg-1")
	if err := orchestrator.RunCycle(ctx, deps, advance(time.Second)); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	// Step 2: Wait for Turn 1's chat("chat-reply-1") to park, then send "msg-2".
	<-turn1ChatPosted
	sendUser("msg-2")

	// Step 3: Turn 1 exits with plain text "plain-reply-2". Wait until continueSessionTurns
	// parks in-process (StateAwaitingReply), verify state is StateAwaitingReply, then send "msg-3".
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := store.State(ctx, director.ID)
		comments, _ := store.Comments(ctx, director.ID)
		hasPlain2 := false
		for _, c := range comments {
			if strings.Contains(c.Body, "plain-reply-2: got msg-2") {
				hasPlain2 = true
				break
			}
		}
		if hasPlain2 && st == model.StateAwaitingReply {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for plain-reply-2 and StateAwaitingReply (last state=%q)", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	sendUser("msg-3")

	// Step 4: Wait for Turn 2 ("plain-reply-3") to relay, then let the short idle TTL (300ms)
	// expire so run 1 exits cleanly and parks the session sandbox warm.
	<-turn2Relayed
	runs.Wait(ctx)

	// Run reconcileConsoleSessions so the session rests on its opening/resting state between runs.
	if err := orchestrator.RunCycle(ctx, deps, advance(time.Second)); err != nil {
		t.Fatalf("rest cycle: %v", err)
	}

	// Step 5: User sends "msg-4" while the session is parked between runs.
	// Verify the very first message wakes the session (not dropped by ParkConsoleSessionTask's deferral!).
	sendUser("msg-4")
	stAfterMsg4, err := store.State(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stAfterMsg4 != model.StateQueued {
		t.Fatalf("state after msg-4 = %q, want %q (first message must wake parked console session)", stAfterMsg4, model.StateQueued)
	}

	// Use a longer idle TTL for Dispatch 2 so the warm sandbox isn't expired before dispatch,
	// then wait for turn4ChatPosted and send "msg-5".
	deps.Config.SessionSandboxIdleTTL = 50 * time.Millisecond
	if err := orchestrator.RunCycle(ctx, deps, advance(time.Millisecond)); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	<-turn4ChatPosted
	sendUser("msg-5")
	runs.Wait(ctx)

	// Verify all 5 user messages and all 5 agent replies are in the conversation in order!
	comments, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range comments {
		bodies = append(bodies, c.Body)
	}
	joined := strings.Join(bodies, "\n---\n")
	for _, want := range []string{
		"msg-1",
		"chat-reply-1",
		"msg-2",
		"plain-reply-2: got msg-2",
		"msg-3",
		"plain-reply-3",
		"msg-4",
		"chat-reply-4",
		"msg-5",
		"final-reply-5: got msg-5",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in conversation:\n%s", want, joined)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(resumeTokens) != 3 {
		t.Fatalf("agent invocations = %d (%v), want 3", len(resumeTokens), resumeTokens)
	}
	if resumeTokens[1] != "sess-token-1" {
		t.Errorf("turn 2 ResumeSession = %q, want sess-token-1", resumeTokens[1])
	}
	if resumeTokens[2] != "sess-token-2" {
		t.Errorf("turn 3 ResumeSession = %q, want sess-token-2", resumeTokens[2])
	}
}

// TestInteractiveConcurrentCommentDuringProcessResultLeavesTaskQueued verifies
// that when a user posts a comment concurrently while a run is finishing /
// inside ProcessResult (after AdvanceDelivered), ProcessResult relays the run's
// closing text without parking/deferring/completing the task over the unanswered
// comment, leaving the task immediately queued for the next dispatch.
func TestInteractiveConcurrentCommentDuringProcessResultLeavesTaskQueued(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)

	userClient := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)

	// Simulate Run 1 starting, advancing DeliveredCommentID for the initial comment,
	// and a concurrent user comment arriving right before ProcessResult runs.
	d := dispatch.Dispatch{TaskID: director.ID, RunID: director.ID + "-1", Attempt: 1}
	startRun(t, ctx, store, d, t1.Add(time.Second))
	commentsBefore, err := store.Comments(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	var lastBefore int64
	for _, c := range commentsBefore {
		if c.ID > lastBefore {
			lastBefore = c.ID
		}
	}
	tFinish := t1.Add(2 * time.Second)
	if err := store.AdvanceDelivered(ctx, director.ID, lastBefore, tFinish); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, d.RunID, tFinish, "succeeded", "plain text"); err != nil {
		t.Fatal(err)
	}

	// Concurrent user comment arrives right as the run finishes, before ProcessResult!
	tConcurrent := tFinish.Add(100 * time.Millisecond)
	userClient.Now = func() time.Time { return tConcurrent }
	if err := userClient.AddComment(ctx, director.ID, "concurrent follow-up while finishing", nil); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	task, err := store.GetTask(ctx, director.ID)
	if err != nil || task == nil {
		t.Fatalf("GetTask: %v", err)
	}
	res := &agent.Result{FinalText: "turn 1 plain text answer"}
	if err := orchestrator.ProcessResult(ctx, store, nil, *task, res, d.RunID, tConcurrent.Add(100*time.Millisecond)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// Assert:
	// 1. The task is StateQueued (NOT StateDeferred, StateAwaitingReply, or StateCompleted).
	// 2. Both "turn 1 plain text answer" and "concurrent follow-up while finishing" are present.
	// 3. The next RunCycle immediately dispatches Run 2 to answer the concurrent comment!
	st, err := store.State(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state after ProcessResult with concurrent comment = %q, want %q", st, model.StateQueued)
	}

	var run2Prompt string
	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			run2Prompt = cfg.Prompt
			return textAnswer("answered concurrent follow-up").Run(ctx, cfg)
		})),
		Config: orchestrator.Config{
			StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities: model.NewCapabilityRegistry(durable.New()),
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, tConcurrent.Add(time.Second)); err != nil {
		t.Fatalf("RunCycle for concurrent comment: %v", err)
	}
	if !strings.Contains(run2Prompt, "concurrent follow-up while finishing") {
		t.Fatalf("run 2 was not dispatched with the concurrent comment; prompt len = %d", len(run2Prompt))
	}
}

// A chat filed with only a body has no comment for its first turn's
// prompt, so that turn delivered nothing. A message a person posts while
// it writes its last answer reaches it by no tool call, and must still be
// dispatched for rather than read as answered: with no delivered mark
// recorded, undeliveredHumanComment could not tell it was unseen, and the
// task sat awaiting a reply to a message nobody had answered
// (docs/test-runs/2026-11-01-warm-sandbox-adoption.md).
func TestInteractiveFirstTurnWithNoCommentsDispatchesForAMessageItNeverSaw(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	task := model.Task{
		ID: "first-turn-chat", Intent: model.IntentAnalyze, Interactive: true,
		Title: "a chat", Body: "hello session",
		Origin:  model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding: model.BindingDirective,
	}
	task.Approval = &model.Attribution{Actor: human}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	userClient := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice"), Capabilities: ui.OfferedCapabilities()}, store)

	var prompts []string
	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: orchestrator.StaticFramework(agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			prompts = append(prompts, cfg.Prompt)
			if len(prompts) == 1 {
				if err := userClient.AddComment(ctx, task.ID, "one more thing while you answer", nil); err != nil {
					t.Errorf("AddComment: %v", err)
				}
			}
			return textAnswer(fmt.Sprintf("answer %d", len(prompts))).Run(ctx, cfg)
		})),
		Config: orchestrator.Config{
			StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities: model.NewCapabilityRegistry(durable.New()),
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle (turn 1): %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("turn 1 dispatched %d time(s), want 1", len(prompts))
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateQueued {
		t.Fatalf("state after turn 1 = %q (%v), want %q: the message posted while it answered was never shown to it", st, err, model.StateQueued)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle (turn 2): %v", err)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "one more thing while you answer") {
		t.Fatalf("turn 2 was not dispatched for the unseen message (%d dispatch(es))", len(prompts))
	}
}

// TestInteractiveFrameworkStickinessAndUsageLimitFallback verifies that an
// unpinned interactive task prefers the framework and model of its previous
// attempt (preserving warm CLI sessions across runs), and falls back to the
// rest of the rotation when that model fails over on a usage limit.
func TestInteractiveFrameworkStickinessAndUsageLimitFallback(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.DefaultModel = "gemini-pro-coordinator,claude-opus-5-5"
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	var choices []orchestrator.AgentChoice
	var call int
	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(_ context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
			choices = append(choices, choice)
			call++
			switch call {
			case 1:
				// Attempt 1 runs on claude / claude-opus-5-5 (e.g. primary was busy on attempt 1)
				// and leaves a suspended claude session token.
				return &labeledFramework{
					framework: "claude",
					model:     "claude-opus-5-5",
					inner: agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
						return &agent.Result{
							ToolCalls: []agent.ToolCall{{
								Name:      "comment_on_issue",
								Arguments: map[string]any{"comment": "attempt 1 on claude"},
							}},
							ResumableSession: "claude-session-abc",
						}, nil
					}),
				}, nil
			case 2:
				// Attempt 2: verify choice prefers claude / claude-opus-5-5!
				// Then simulate claude-opus-5-5 hitting a usage limit so it fails over.
				return &labeledFramework{
					framework: "claude",
					model:     "claude-opus-5-5",
					inner: agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
						return nil, &agent.UsageLimitError{
							ResetAt: baseTime.Add(2 * time.Hour),
							Message: "claude usage limit reached",
						}
					}),
				}, nil
			default:
				// Attempt 3 (after usage-limit failover): choice must include claude-opus-5-5 in AvoidModels.
				return &labeledFramework{
					framework: "antigravity",
					model:     "gemini-pro-coordinator",
					inner:     textAnswer("attempt 3 fell back to gemini-pro-coordinator"),
				}, nil
			}
		},
		Config: orchestrator.Config{
			StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities: model.NewCapabilityRegistry(durable.New()),
		},
		MaxWorkers: 1,
	}

	// Exchange 1: reply and run attempt 1 (lands on claude / claude-opus-5-5).
	t1 := baseTime.Add(time.Minute)
	replyToSession(t, store, director.ID, t1)
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
		t.Fatalf("attempt 1 cycle: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, t1.Add(2*time.Second)); err != nil {
		t.Fatalf("rest cycle after attempt 1: %v", err)
	}

	// Exchange 2: reply and run attempt 2. Because attempt 1 ran on claude / claude-opus-5-5
	// and left a suspended session, dispatchChoiceFor must set PreferFramework="claude" and PreferModel="claude-opus-5-5".
	t2 := t1.Add(time.Minute)
	replyToSession(t, store, director.ID, t2)
	choicesBeforeAttempt2 := len(choices)
	_ = orchestrator.RunCycle(ctx, deps, t2.Add(time.Second))
	if len(choices) <= choicesBeforeAttempt2 {
		t.Fatal("expected Framework to be called for attempt 2")
	}
	c2 := choices[len(choices)-1]
	if c2.PreferFramework != "claude" || c2.PreferModel != "claude-opus-5-5" {
		t.Fatalf("attempt 2 AgentChoice = %+v, want PreferFramework=\"claude\" and PreferModel=\"claude-opus-5-5\"", c2)
	}

	// Attempt 2 hit a usage limit on claude-opus-5-5, so the task is suspended for model failover.
	// On the next cycle after the failover backoff, AvoidModels must include "claude-opus-5-5".
	choicesBeforeAttempt3 := len(choices)
	if err := orchestrator.RunCycle(ctx, deps, t2.Add(5*time.Minute)); err != nil {
		t.Fatalf("attempt 3 failover cycle: %v", err)
	}
	if len(choices) <= choicesBeforeAttempt3 {
		t.Fatal("expected Framework to be called for attempt 3 failover")
	}
	c3 := choices[len(choices)-1]
	foundAvoid := false
	for _, m := range c3.AvoidModels {
		if m == "claude-opus-5-5" {
			foundAvoid = true
			break
		}
	}
	if !foundAvoid {
		t.Fatalf("attempt 3 AvoidModels = %v, want claude-opus-5-5 avoided after usage limit", c3.AvoidModels)
	}
}

type labeledFramework struct {
	framework string
	model     string
	inner     agent.Framework
}

func (f *labeledFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f.inner.Run(ctx, cfg)
}

func (f *labeledFramework) CredentialConnection() (string, string) {
	return "conn-" + f.framework, model.DefaultTeamID
}
func (f *labeledFramework) CredentialFramework() string { return f.framework }
func (f *labeledFramework) CredentialModel() string     { return f.model }

// liveAdoptedInteractiveHost simulates an adopted run host that is still mid-chat
// when the daemon restarts: while Attach is running, the agent calls recreate_sandbox
// and completes a chat turn, and then exits cleanly with a comment_on_issue.
type liveAdoptedInteractiveHost struct {
	recreations *orchestrator.SandboxRecreations
	store       *model.Store
	taskID      string
	recreated   orchestrator.SandboxRecreation
	recreateErr error
	chatReply   string
	released    []string
}

func (h *liveAdoptedInteractiveHost) Attach(ctx context.Context, _ runhost.Info, _ int64, tee io.Writer) (string, error) {
	// 1. Mid-adoption recreate_sandbox call (Bug 4: previously failed with
	//    "this task has no live run on this daemon, so there is no sandbox to rebuild").
	h.recreated, h.recreateErr = h.recreations.Recreate(ctx, h.taskID)
	if h.recreateErr != nil {
		return "", h.recreateErr
	}

	// 2. Subsequent chat turn on the adopted run: post agent question, receive user reply, mark delivered.
	now := time.Now().UTC()
	agentClient := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("grain"),
		Capabilities: ui.OfferedCapabilities(),
	}, h.store)
	agentClient.Now = func() time.Time { return now }
	posted, err := agentClient.PostAgentMessage(ctx, h.taskID, "adopted chat question", "adopted chat question", nil)
	if err != nil {
		return "", err
	}

	userClient := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, h.store)
	userClient.Now = func() time.Time { return now.Add(time.Second) }
	if err := userClient.AddComment(ctx, h.taskID, "user reply after daemon restart", nil); err != nil {
		return "", err
	}

	comments, err := h.store.Comments(ctx, h.taskID)
	if err != nil {
		return "", err
	}
	for _, c := range comments {
		if c.ID > posted.ID && c.Author.Actor.Kind == model.PrincipalHuman {
			h.chatReply = c.Body
			if err := agentClient.RecordDelivered(ctx, h.taskID, c.ID); err != nil {
				return "", err
			}
			break
		}
	}

	const out = "adopted-interactive-stdout\n"
	if tee != nil {
		_, _ = io.WriteString(tee, out)
	}
	return out, nil
}

func (h *liveAdoptedInteractiveHost) Release(_ context.Context, info runhost.Info, _ string) {
	h.released = append(h.released, info.Name)
}

// TestInteractiveRunHostAdoptionMidChatWithRecreateSandboxAndSubsequentTurns
// verifies that when a daemon restarts and adopts an interactive run executing
// in a reused warm session sandbox (r.Sandbox != r.ID), RecoverRunHosts claims
// the warm sandbox by SandboxName(), ResumeAdoptedRun registers it in
// SandboxRecreations so recreate_sandbox succeeds mid-chat, subsequent chat
// turns succeed, and the sandbox is parked warm for the next exchange.
func TestInteractiveRunHostAdoptionMidChatWithRecreateSandboxAndSubsequentTurns(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.SessionSandboxIdleTTL = 30 * time.Minute
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	started := time.Now().UTC().Add(-time.Minute)
	replyToSession(t, store, director.ID, started.Add(-time.Second))

	// Run 1-2 is reusing warm session sandbox "1-1" from attempt 1!
	warmSandboxName := director.ID + "-1"
	d := dispatch.Dispatch{TaskID: director.ID, RunID: director.ID + "-2", Attempt: 2}
	startRun(t, ctx, store, d, started)
	if err := store.SetRunSandbox(ctx, d.RunID, warmSandboxName, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSpoolCursor(ctx, d.RunID, 0, started); err != nil {
		t.Fatal(err)
	}

	sandboxDir := t.TempDir()
	sandboxes := orchestrator.NewHostSandboxes(sandboxDir)
	if _, err := sandboxes.Acquire(ctx, warmSandboxName, orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	spool := t.TempDir()
	driver := &listedHosts{hosts: []runhost.Info{{
		Name:    "rh-interactive",
		Running: true,
		Labels:  runhost.Labels{Deployment: "d", Task: director.ID, Run: d.RunID, Protocol: runhost.Protocol, Spool: spool},
	}}}

	// 1. RecoverRunHosts must populate Claim.Sandbox = "1-1" so Claim.SandboxName()
	//    readopts the warm sandbox "1-1" instead of failing on "1-2".
	var adopted []orchestrator.AdoptedRun
	if _, err := orchestrator.RecoverRunHosts(ctx, store, nil, &runhost.Host{Driver: driver, Deployment: "d"}, time.Now().UTC(),
		func(a runhost.Adoption, cursor int64) bool {
			if a.Claim.SandboxName() != warmSandboxName {
				t.Errorf("Claim.SandboxName() = %q, want %q", a.Claim.SandboxName(), warmSandboxName)
				return false
			}
			sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, a.Claim.SandboxName())
			if err != nil {
				t.Errorf("ReadoptSandbox(%q): %v", a.Claim.SandboxName(), err)
				return false
			}
			adopted = append(adopted, orchestrator.AdoptedRun{Adoption: a, Cursor: cursor, Sandbox: sb})
			return true
		}); err != nil {
		t.Fatal(err)
	}
	if len(adopted) != 1 {
		t.Fatalf("adopted = %d, want 1", len(adopted))
	}

	// 2. ResumeAdoptedRun registers the adopted run in SandboxRecreations, allows
	//    recreate_sandbox and subsequent chat turns to succeed, and parks the sandbox warm.
	recreations := orchestrator.NewSandboxRecreations()
	host := &liveAdoptedInteractiveHost{
		recreations: recreations,
		store:       store,
		taskID:      director.ID,
	}
	fw := parsingFramework{
		parsed:  make(chan string, 1),
		earlier: make(chan string, 1),
		result: toolResult(agent.ToolCall{
			Name:      "comment_on_issue",
			Arguments: map[string]any{"comment": "finished adopted turn after chat"},
		}),
	}
	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: sandboxes,
		Framework: orchestrator.StaticFramework(fw),
		Config: orchestrator.Config{
			SandboxRecreations:    recreations,
			SessionSandboxIdleTTL: 30 * time.Minute,
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New()),
		},
	}
	recordAdoptedSessionKeys(t, ctx, store, deps, director.ID, d.RunID)
	if err := orchestrator.ResumeAdoptedRun(ctx, deps, host, adopted[0]); err != nil {
		t.Fatalf("ResumeAdoptedRun: %v", err)
	}
	if host.recreateErr != nil {
		t.Fatalf("recreate_sandbox on adopted interactive run failed: %v", host.recreateErr)
	}
	if host.chatReply != "user reply after daemon restart" {
		t.Fatalf("chatReply = %q, want %q", host.chatReply, "user reply after daemon restart")
	}
	if got := orchestrator.WarmSessionSandboxID(store, director.ID); got != warmSandboxName {
		t.Fatalf("WarmSessionSandboxID after adopted interactive run = %q, want %q", got, warmSandboxName)
	}
}

type simpleAdoptedHost struct {
	stdout string
}

func (h *simpleAdoptedHost) Attach(_ context.Context, _ runhost.Info, _ int64, tee io.Writer) (string, error) {
	if tee != nil {
		_, _ = io.WriteString(tee, h.stdout)
	}
	return h.stdout, nil
}

func (h *simpleAdoptedHost) Release(_ context.Context, _ runhost.Info, _ string) {}

// TestResumeAdoptedRunEnforcesWarmSandboxIsolationAndLedgerPark verifies three
// warm-sandbox invariants across daemon restarts:
//  1. If a task's isolation shape or grants were narrowed while the run was in
//     flight before the daemon restarted, ResumeAdoptedRun does not stamp the
//     old privileged sandbox with the narrowed task's key and releases it instead.
//  2. If an adopted run holding a sandbox-materialized capability calls
//     recreate_sandbox (whose capability placements could not be restored across
//     the restart), the degraded rebuilt sandbox is released rather than parked
//     warm for subsequent turns.
//  3. When an unchanged first-attempt interactive run (sandbox name == runID) is
//     adopted and parked warm, defer deps.Ledger.Release runs before
//     parkWarmSessionInLedger so the idle commitment remains tracked in Ledger.
func TestResumeAdoptedRunEnforcesWarmSandboxIsolationAndLedgerPark(t *testing.T) {
	t.Parallel()

	t.Run("narrowed_task_before_adoption_releases_privileged_sandbox", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = 30 * time.Minute
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID: model.DirectorSessionID, Title: "Director",
				Capabilities: []string{selfdebug.CapabilityName}, NetworkAccess: model.NetworkOpen,
			}}
		})
		runSessions(t, store, baseTime)
		director := sessionTasks(t, store)[0]

		started := time.Now().UTC().Add(-time.Minute)
		replyToSession(t, store, director.ID, started.Add(-time.Second))

		d := dispatch.Dispatch{TaskID: director.ID, RunID: director.ID + "-1", Attempt: 1}
		startRun(t, ctx, store, d, started)
		if err := store.SetRunSandbox(ctx, d.RunID, d.RunID, nil); err != nil {
			t.Fatal(err)
		}
		// Record the dispatch-time privileged keys before the task is narrowed.
		if err := store.SetRunSandboxKeys(ctx, d.RunID,
			"shape={CPUs:0 MemoryMB:0 DiskGB:0 NetworkAccess:open GrainSource:true OperatorEnv:false Class:}|grants=task:durable:grain,task:self-debug:grain",
			"durable:grain,self-debug:grain"); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSpoolCursor(ctx, d.RunID, 0, started); err != nil {
			t.Fatal(err)
		}

		// Operator withdraws self-debug and narrows network access BEFORE the adopting daemon reads the task.
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = 30 * time.Minute
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID: model.DirectorSessionID, Title: "Director",
				Capabilities: []string{}, NetworkAccess: model.NetworkNone,
			}}
		})
		runSessions(t, store, started.Add(10*time.Second))

		sandboxDir := t.TempDir()
		sandboxes := orchestrator.NewHostSandboxes(sandboxDir)
		if _, err := sandboxes.Acquire(ctx, d.RunID, orchestrator.Shape{NetworkAccess: model.NetworkOpen, GrainSource: true}); err != nil {
			t.Fatal(err)
		}
		sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, d.RunID)
		if err != nil {
			t.Fatal(err)
		}

		fw := parsingFramework{
			parsed:  make(chan string, 1),
			earlier: make(chan string, 1),
			result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "finished adopted turn"},
			}),
		}
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: sandboxes,
			Framework: orchestrator.StaticFramework(fw),
			Config: orchestrator.Config{
				SessionSandboxIdleTTL: 30 * time.Minute,
				StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				Capabilities:          model.NewCapabilityRegistry(durable.New(), selfdebug.New()),
			},
		}
		info := runhost.Info{
			Name:    "rh-narrowed",
			Running: true,
			Labels:  runhost.Labels{Deployment: "d", Task: director.ID, Run: d.RunID, Protocol: runhost.Protocol, Spool: t.TempDir()},
		}
		adopted := orchestrator.AdoptedRun{
			Adoption: runhost.Adoption{Claim: runhost.Claim{Run: d.RunID, Task: director.ID, Sandbox: d.RunID}, Host: info},
			Sandbox:  sb,
		}
		if err := orchestrator.ResumeAdoptedRun(ctx, deps, &simpleAdoptedHost{stdout: "ok\n"}, adopted); err != nil {
			t.Fatalf("ResumeAdoptedRun: %v", err)
		}
		if got := orchestrator.WarmSessionSandboxID(store, director.ID); got != "" {
			t.Fatalf("WarmSessionSandboxID = %q, want empty (privileged sandbox must not be parked under narrowed task key)", got)
		}
	})

	t.Run("degraded_recreate_with_capability_grant_does_not_park_warm", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = 30 * time.Minute
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID: model.DirectorSessionID, Title: "Director",
				Capabilities: []string{selfdebug.CapabilityName}, NetworkAccess: model.NetworkOpen,
			}}
		})
		runSessions(t, store, baseTime)
		director := sessionTasks(t, store)[0]

		started := time.Now().UTC().Add(-time.Minute)
		replyToSession(t, store, director.ID, started.Add(-time.Second))

		d := dispatch.Dispatch{TaskID: director.ID, RunID: director.ID + "-1", Attempt: 1}
		startRun(t, ctx, store, d, started)
		if err := store.SetRunSandbox(ctx, d.RunID, d.RunID, nil); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSpoolCursor(ctx, d.RunID, 0, started); err != nil {
			t.Fatal(err)
		}

		sandboxDir := t.TempDir()
		sandboxes := orchestrator.NewHostSandboxes(sandboxDir)
		if _, err := sandboxes.Acquire(ctx, d.RunID, orchestrator.Shape{NetworkAccess: model.NetworkOpen, GrainSource: true}); err != nil {
			t.Fatal(err)
		}
		sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, d.RunID)
		if err != nil {
			t.Fatal(err)
		}

		recreations := orchestrator.NewSandboxRecreations()
		host := &liveAdoptedInteractiveHost{
			recreations: recreations,
			store:       store,
			taskID:      director.ID,
		}
		fw := parsingFramework{
			parsed:  make(chan string, 1),
			earlier: make(chan string, 1),
			result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "finished adopted turn after degraded recreate"},
			}),
		}
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: sandboxes,
			Framework: orchestrator.StaticFramework(fw),
			Config: orchestrator.Config{
				SandboxRecreations:    recreations,
				SessionSandboxIdleTTL: 30 * time.Minute,
				StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				Capabilities:          model.NewCapabilityRegistry(durable.New(), selfdebug.New()),
			},
		}
		info := runhost.Info{
			Name:    "rh-degraded",
			Running: true,
			Labels:  runhost.Labels{Deployment: "d", Task: director.ID, Run: d.RunID, Protocol: runhost.Protocol, Spool: t.TempDir()},
		}
		adopted := orchestrator.AdoptedRun{
			Adoption: runhost.Adoption{Claim: runhost.Claim{Run: d.RunID, Task: director.ID, Sandbox: d.RunID}, Host: info},
			Sandbox:  sb,
		}
		// Recorded, so it is the degraded rebuild alone that keeps the
		// sandbox from being parked.
		recordAdoptedSessionKeys(t, ctx, store, deps, director.ID, d.RunID)
		if err := orchestrator.ResumeAdoptedRun(ctx, deps, host, adopted); err != nil {
			t.Fatalf("ResumeAdoptedRun: %v", err)
		}
		if host.recreateErr != nil {
			t.Fatalf("recreate_sandbox failed: %v", host.recreateErr)
		}
		if got := orchestrator.WarmSessionSandboxID(store, director.ID); got != "" {
			t.Fatalf("WarmSessionSandboxID = %q, want empty after adopted rebuild with unrestorable capability", got)
		}
	})

	t.Run("unrecorded_keys_release_sandbox", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = 30 * time.Minute
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID: model.DirectorSessionID, Title: "Director",
				Capabilities: []string{selfdebug.CapabilityName}, NetworkAccess: model.NetworkOpen,
			}}
		})
		runSessions(t, store, baseTime)
		director := sessionTasks(t, store)[0]

		started := time.Now().UTC().Add(-time.Minute)
		replyToSession(t, store, director.ID, started.Add(-time.Second))

		// A run dispatched by a daemon that recorded no session keys (one
		// from before task_run_sandbox_key, or a failed SetRunSandboxKeys).
		d := dispatch.Dispatch{TaskID: director.ID, RunID: director.ID + "-1", Attempt: 1}
		startRun(t, ctx, store, d, started)
		if err := store.SetRunSandbox(ctx, d.RunID, d.RunID, nil); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSpoolCursor(ctx, d.RunID, 0, started); err != nil {
			t.Fatal(err)
		}

		// Narrowed before the adopting daemon reads the task: computing the
		// key from the task now would name a sandbox it no longer matches.
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = 30 * time.Minute
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID: model.DirectorSessionID, Title: "Director",
				Capabilities: []string{}, NetworkAccess: model.NetworkNone,
			}}
		})
		runSessions(t, store, started.Add(10*time.Second))

		sandboxes := orchestrator.NewHostSandboxes(t.TempDir())
		if _, err := sandboxes.Acquire(ctx, d.RunID, orchestrator.Shape{NetworkAccess: model.NetworkOpen, GrainSource: true}); err != nil {
			t.Fatal(err)
		}
		sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, d.RunID)
		if err != nil {
			t.Fatal(err)
		}
		fw := parsingFramework{
			parsed:  make(chan string, 1),
			earlier: make(chan string, 1),
			result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "finished adopted turn"},
			}),
		}
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: sandboxes,
			Framework: orchestrator.StaticFramework(fw),
			Config: orchestrator.Config{
				SessionSandboxIdleTTL: 30 * time.Minute,
				StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				Capabilities:          model.NewCapabilityRegistry(durable.New(), selfdebug.New()),
			},
		}
		info := runhost.Info{
			Name:    "rh-unrecorded",
			Running: true,
			Labels:  runhost.Labels{Deployment: "d", Task: director.ID, Run: d.RunID, Protocol: runhost.Protocol, Spool: t.TempDir()},
		}
		adopted := orchestrator.AdoptedRun{
			Adoption: runhost.Adoption{Claim: runhost.Claim{Run: d.RunID, Task: director.ID, Sandbox: d.RunID}, Host: info},
			Sandbox:  sb,
		}
		if err := orchestrator.ResumeAdoptedRun(ctx, deps, &simpleAdoptedHost{stdout: "ok\n"}, adopted); err != nil {
			t.Fatalf("ResumeAdoptedRun: %v", err)
		}
		if got := orchestrator.WarmSessionSandboxID(store, director.ID); got != "" {
			t.Fatalf("WarmSessionSandboxID = %q, want empty (no recorded keys: the sandbox must not be parked)", got)
		}
	})

	t.Run("first_attempt_adopted_run_retains_idle_park_in_ledger", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = 30 * time.Minute
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID: model.DirectorSessionID, Title: "Director",
				Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
			}}
		})
		runSessions(t, store, baseTime)
		director := sessionTasks(t, store)[0]

		started := time.Now().UTC().Add(-time.Minute)
		replyToSession(t, store, director.ID, started.Add(-time.Second))

		// First attempt: sandbox name == runID ("1-1").
		d := dispatch.Dispatch{TaskID: director.ID, RunID: director.ID + "-1", Attempt: 1}
		startRun(t, ctx, store, d, started)
		if err := store.SetRunSandbox(ctx, d.RunID, d.RunID, nil); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSpoolCursor(ctx, d.RunID, 0, started); err != nil {
			t.Fatal(err)
		}

		sandboxDir := t.TempDir()
		sandboxes := orchestrator.NewHostSandboxes(sandboxDir)
		if _, err := sandboxes.Acquire(ctx, d.RunID, orchestrator.Shape{NetworkAccess: model.NetworkOpen}); err != nil {
			t.Fatal(err)
		}
		sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, d.RunID)
		if err != nil {
			t.Fatal(err)
		}

		ledger := &model.Ledger{}
		t.Cleanup(func() { ledger.EvictOldestIdle(1) })
		fw := parsingFramework{
			parsed:  make(chan string, 1),
			earlier: make(chan string, 1),
			result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "finished first-attempt adopted turn"},
			}),
		}
		deps := orchestrator.Deps{
			Store:      store,
			Sandboxes:  sandboxes,
			Ledger:     ledger,
			AgentShape: model.Commitment{CPUs: 2, MemoryMB: 4096},
			Framework:  orchestrator.StaticFramework(fw),
			Config: orchestrator.Config{
				SessionSandboxIdleTTL: 30 * time.Minute,
				StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				Capabilities:          model.NewCapabilityRegistry(durable.New()),
			},
		}
		info := runhost.Info{
			Name:    "rh-ledger",
			Running: true,
			Labels:  runhost.Labels{Deployment: "d", Task: director.ID, Run: d.RunID, Protocol: runhost.Protocol, Spool: t.TempDir()},
		}
		adopted := orchestrator.AdoptedRun{
			Adoption: runhost.Adoption{Claim: runhost.Claim{Run: d.RunID, Task: director.ID, Sandbox: d.RunID}, Host: info},
			Sandbox:  sb,
		}
		recordAdoptedSessionKeys(t, ctx, store, deps, director.ID, d.RunID)
		if err := orchestrator.ResumeAdoptedRun(ctx, deps, &simpleAdoptedHost{stdout: "ok\n"}, adopted); err != nil {
			t.Fatalf("ResumeAdoptedRun: %v", err)
		}
		if got := orchestrator.WarmSessionSandboxID(store, director.ID); got != d.RunID {
			t.Fatalf("WarmSessionSandboxID = %q, want %q", got, d.RunID)
		}
		if got := ledger.IdleCount(model.PoolAgent); got != 1 {
			t.Fatalf("ledger.IdleCount(PoolAgent) = %d, want 1 (deferred Release must not wipe out parked warm sandbox)", got)
		}
	})
}

// recordAdoptedSessionKeys records the session keys runOne would have
// recorded when it dispatched runID, as task stands now.
func recordAdoptedSessionKeys(t *testing.T, ctx context.Context, store *model.Store, deps orchestrator.Deps, taskID, runID string) {
	t.Helper()
	task, err := store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		t.Fatalf("GetTask(%s): %v", taskID, err)
	}
	sandboxKey, grantsKey := orchestrator.SessionKeysForTest(ctx, deps, *task)
	if err := store.SetRunSandboxKeys(ctx, runID, sandboxKey, grantsKey); err != nil {
		t.Fatal(err)
	}
}
