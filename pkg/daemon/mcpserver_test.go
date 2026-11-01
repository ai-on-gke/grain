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

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func newTestInteractiveServer(t *testing.T) (*model.Store, *ui.Client, *httptest.Server, model.Task) {
	t.Helper()
	store, db := sqlitetest.Open(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	uiCfg := ui.Config{
		Actor:         ui.DefaultActor("operator"),
		SourceDB:      db,
		DBDriver:      "sqlite",
		LocalAPIToken: testAccessCode,
	}
	uiSrv := ui.NewServer(uiCfg, store)
	srv := httptest.NewServer(uiSrv)
	t.Cleanup(srv.Close)

	ctx := t.Context()
	task := model.Task{
		ID:          "1933",
		Title:       "Director",
		Body:        "Interactive console session.",
		Interactive: true,
		CreatedAt:   &now,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	if err := store.Approve(ctx, task.ID, model.Attribution{Actor: ui.DefaultActor("operator")}, now); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := store.SetTaskGoal(ctx, task.ID, model.GoalDecl{Kind: model.GoalPersonCloses}, "grain", now); err != nil {
		t.Fatalf("SetTaskGoal: %v", err)
	}
	return store, ui.NewClient(uiCfg, store), srv, task
}

// Race 1: A human comment is already waiting in the database (after
// deliveredCursor) before the agent calls chat. Chat must return that comment
// immediately on the first poll without parking and waiting for a second
// comment, advance DeliveredCommentID, and clear PendingQuestionCommentID.
func TestDaemonChatReturnsCommentPostedBeforeChatStarts(t *testing.T) {
	t.Parallel()
	store, uiClient, srv, task := newTestInteractiveServer(t)
	ctx := t.Context()

	// Seed initial prompt comment and mark it delivered.
	promptID, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Body:      "initial request",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, task.ID, promptID, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Operator posts a follow-up comment while the agent is still working and
	// before the agent invokes chat.
	if err := uiClient.AddComment(ctx, task.ID, "ok, did that do it?", nil); err != nil {
		t.Fatal(err)
	}

	httpClient := accessCodeClient(srv.URL)
	cursor := newDeliveredCursor(httpClient, task.ID, promptID)
	chat := daemonChat{
		client:    httpClient,
		taskID:    task.ID,
		poll:      50 * time.Millisecond,
		delivered: cursor,
	}

	start := time.Now()
	turn, err := chat.Chat(ctx, "I finished the first step; what next?", "", nil, 10*time.Second)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Chat took %v, want immediate return on first poll", elapsed)
	}
	if !strings.Contains(turn.Reply, "ok, did that do it?") {
		t.Fatalf("turn.Reply = %q, want the already-waiting comment", turn.Reply)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.DeliveredCommentID == nil || *obs.DeliveredCommentID <= promptID {
		t.Fatalf("DeliveredCommentID = %v, want advanced past %d", obs, promptID)
	}
	if obs.PendingQuestionCommentID != nil {
		t.Fatalf("PendingQuestionCommentID = %v, want nil after pre-existing comment answered chat", *obs.PendingQuestionCommentID)
	}
}

// Race 2: A human comment is posted while chat is actively waiting.
func TestDaemonChatReturnsCommentPostedDuringWait(t *testing.T) {
	t.Parallel()
	store, uiClient, srv, task := newTestInteractiveServer(t)
	ctx := t.Context()

	promptID, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Body:      "hello",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, task.ID, promptID, time.Now()); err != nil {
		t.Fatal(err)
	}

	httpClient := accessCodeClient(srv.URL)
	cursor := newDeliveredCursor(httpClient, task.ID, promptID)
	chat := daemonChat{
		client:    httpClient,
		taskID:    task.ID,
		poll:      10 * time.Millisecond,
		delivered: cursor,
	}

	go func() {
		// Wait until chat has posted its question and parked the task.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			obs, _ := store.GetObservation(ctx, task.ID)
			if obs != nil && obs.PendingQuestionCommentID != nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		_ = uiClient.AddComment(ctx, task.ID, "reply during chat wait", nil)
	}()

	turn, err := chat.Chat(ctx, "waiting for your input", "", nil, 10*time.Second)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(turn.Reply, "reply during chat wait") {
		t.Fatalf("turn.Reply = %q, want 'reply during chat wait'", turn.Reply)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID != nil {
		t.Fatalf("PendingQuestionCommentID = %v, want cleared after reply", obs)
	}
}

// Race 3: Two comments posted back-to-back before/during chat are both
// returned and marked delivered.
func TestDaemonChatReturnsBackToBackCommentsBeforeAndDuringChat(t *testing.T) {
	t.Parallel()
	store, uiClient, srv, task := newTestInteractiveServer(t)
	ctx := t.Context()

	promptID, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Body:      "start",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, task.ID, promptID, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Post two comments back-to-back before chat is called.
	if err := uiClient.AddComment(ctx, task.ID, "first rapid comment", nil); err != nil {
		t.Fatal(err)
	}
	if err := uiClient.AddComment(ctx, task.ID, "second rapid comment", nil); err != nil {
		t.Fatal(err)
	}

	httpClient := accessCodeClient(srv.URL)
	cursor := newDeliveredCursor(httpClient, task.ID, promptID)
	chat := daemonChat{
		client:    httpClient,
		taskID:    task.ID,
		poll:      10 * time.Millisecond,
		delivered: cursor,
	}

	turn, err := chat.Chat(ctx, "ready for instructions", "", nil, 5*time.Second)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(turn.Reply, "first rapid comment") || !strings.Contains(turn.Reply, "second rapid comment") {
		t.Fatalf("turn.Reply = %q, want both back-to-back comments", turn.Reply)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var maxHumanID int64
	for _, c := range comments {
		if c.Author.Actor.Kind == model.PrincipalHuman && c.ID > maxHumanID {
			maxHumanID = c.ID
		}
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.DeliveredCommentID == nil || *obs.DeliveredCommentID != maxHumanID {
		t.Fatalf("DeliveredCommentID = %v, want %d", obs.DeliveredCommentID, maxHumanID)
	}
}

// Race 4: A comment returned by addendaPoller on the final tool call of a run
// is NOT recorded as delivered in Observation.DeliveredCommentID unless the
// agent makes a subsequent tool call.
func TestDaemonAddendaOnFinalToolCallLeavesCommentUndeliveredInStore(t *testing.T) {
	t.Parallel()
	store, uiClient, srv, task := newTestInteractiveServer(t)
	ctx := t.Context()

	promptID, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Body:      "initial prompt",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, task.ID, promptID, time.Now()); err != nil {
		t.Fatal(err)
	}

	httpClient := accessCodeClient(srv.URL)
	cursor := newDeliveredCursor(httpClient, task.ID, promptID)
	addenda := daemonAddenda{
		client:    httpClient,
		taskID:    task.ID,
		delivered: cursor,
	}

	registry := mcp.NewRegistry()
	registry.AnnounceAddenda(promptID, addenda)
	registry.Register(mcp.Tool{
		Name:        "run_command",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) mcp.Result { return mcp.Result{Text: "exit=0"} },
	})
	mcpClient := mcp.NewInProcess(ctx, registry)
	t.Cleanup(func() { mcpClient.Close() })

	// Operator posts a comment while the final tool call is in flight.
	if err := uiClient.AddComment(ctx, task.ID, "The last message I sent didn't get a response; please diagnose why", nil); err != nil {
		t.Fatal(err)
	}

	res, err := mcpClient.CallTool(ctx, "run_command", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text(), "please diagnose why") {
		t.Fatalf("tool result = %q, want addendum text", res.Text())
	}

	// If the run exits now without making another tool call, DeliveredCommentID
	// in the database must still be promptID so the orchestrator knows the
	// comment was not answered!
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.DeliveredCommentID == nil || *obs.DeliveredCommentID != promptID {
		t.Fatalf("DeliveredCommentID after final tool call = %v, want %d (unacknowledged)", obs.DeliveredCommentID, promptID)
	}

	// Moreover, if the agent instead calls chat on its next step without having
	// acknowledged the addendum prior to chat, handleCall acknowledges it, OR if
	// a new run/chat starts with cursor at promptID, chat still sees it.
	if _, err := mcpClient.CallTool(ctx, "run_command", nil); err != nil {
		t.Fatal(err)
	}
	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.DeliveredCommentID == nil || *obs.DeliveredCommentID <= promptID {
		t.Fatalf("DeliveredCommentID after subsequent tool call = %v, want > %d", obs.DeliveredCommentID, promptID)
	}
}

// Race 5: A comment posted across a simulated daemon restart gap is delivered
// and acknowledged once the daemon comes back up.
func TestDaemonChatDeliversCommentAcrossDaemonRestartGap(t *testing.T) {
	t.Parallel()
	store, uiClient, _, task := newTestInteractiveServer(t)
	ctx := t.Context()

	promptID, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Body:      "start session",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, task.ID, promptID, time.Now()); err != nil {
		t.Fatal(err)
	}

	uiSrv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator"),
		LocalAPIToken: testAccessCode,
	}, store)

	var restarting atomic.Bool
	var getPolls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/messages") {
			n := getPolls.Add(1)
			if n >= 2 && n <= 5 {
				restarting.Store(true)
				http.Error(w, "daemon restarting", http.StatusServiceUnavailable)
				return
			}
			if n == 6 && restarting.CompareAndSwap(true, false) {
				_ = uiClient.AddComment(ctx, task.ID, "comment posted right across daemon restart", nil)
			}
		}
		uiSrv.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)

	httpClient := accessCodeClient(proxy.URL)
	httpClient.RetryPolicy = &ui.RetryPolicy{}
	cursor := newDeliveredCursor(httpClient, task.ID, promptID)
	chat := daemonChat{
		client:    httpClient,
		taskID:    task.ID,
		poll:      5 * time.Millisecond,
		delivered: cursor,
	}

	turn, err := chat.Chat(ctx, "waiting across restart", "", nil, 10*time.Second)
	if err != nil {
		t.Fatalf("Chat across restart: %v", err)
	}
	if !strings.Contains(turn.Reply, "comment posted right across daemon restart") {
		t.Fatalf("turn.Reply = %q, want comment from after restart", turn.Reply)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.DeliveredCommentID == nil || *obs.DeliveredCommentID <= promptID {
		t.Fatalf("DeliveredCommentID = %v, want advanced past %d", obs, promptID)
	}
}

// TestAgentPoolPrefersPreviousFrameworkAndModelAndFallsBackWhenPaused verifies
// that when default_model lists multiple frameworks ("gemini-pro-coordinator,claude-opus-5-5"),
// an unpinned interactive task with PreferFramework="claude" and PreferModel="claude-opus-5-5"
// stays on claude-opus-5-5 across runs (preserving warm CLI sessions), and falls back
// to gemini-pro-coordinator when claude-opus-5-5 is paused on a usage limit.
func TestAgentPoolPrefersPreviousFrameworkAndModelAndFallsBackWhenPaused(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	connAG := model.AgentBackend{
		ID:        "conn-ag",
		Framework: model.AgentFrameworkAntigravity,
		AuthMode:  model.AntigravityAuthAmbient,
		Models:    []string{"gemini-pro-coordinator"},
	}
	connClaude := model.AgentBackend{
		ID:        "conn-claude",
		Framework: model.AgentFrameworkClaude,
		Models:    []string{"claude-opus-5-5"},
	}
	pool := providerDefaultPool(t, "gemini-pro-coordinator,claude-opus-5-5", connAG, connClaude)
	pause := &orchestrator.Pause{}
	pool.Pause = pause

	live := DispatchConfig(ctx, pool.Store, pool.cfg)
	choice := orchestrator.AgentChoice{
		PreferFramework: model.AgentFrameworkClaude,
		PreferModel:     "claude-opus-5-5",
	}
	req, err := pool.resolve(ctx, live, choice)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(req.models) < 2 || req.models[0] != "claude-opus-5-5" {
		t.Fatalf("req.models = %v, want claude-opus-5-5 first", req.models)
	}

	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	pool.mu.Lock()
	picked, candidates, _ := pool.selectCandidateLocked(live, req, now)
	pool.mu.Unlock()
	if picked != "claude-opus-5-5" || len(candidates) != 1 || candidates[0].ID != "conn-claude" {
		t.Fatalf("picked = %q, candidates = %+v, want claude-opus-5-5 on conn-claude", picked, candidates)
	}

	// Pause claude-opus-5-5 on conn-claude due to a usage limit and verify fallback to gemini-pro-coordinator.
	pause.Begin(now, orchestrator.PauseScope{
		Connection: "conn-claude",
		Model:      "claude-opus-5-5",
		Team:       model.DefaultTeamID,
		Framework:  model.AgentFrameworkClaude,
	}, &agent.UsageLimitError{ResetAt: now.Add(time.Hour)})
	pool.mu.Lock()
	pickedAfterPause, candidatesAfterPause, _ := pool.selectCandidateLocked(live, req, now)
	pool.mu.Unlock()
	if pickedAfterPause != "gemini-pro-coordinator" || len(candidatesAfterPause) != 1 || candidatesAfterPause[0].ID != "conn-ag" {
		t.Fatalf("pickedAfterPause = %q, candidates = %+v, want fallback to gemini-pro-coordinator on conn-ag", pickedAfterPause, candidatesAfterPause)
	}
}
