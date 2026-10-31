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
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestInteractiveSubsequentExchangeLatencyOverRealMCPServer(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	bin := buildGrainBinary(t)

	const taskID = "interactive-chat-e2e"
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	now := time.Now().UTC()
	actor := human("operator")
	if err := store.PutTask(ctx, model.Task{
		ID:          taskID,
		Intent:      model.IntentImplement,
		Title:       "Interactive MCP chat latency test",
		Body:        "initial prompt",
		Interactive: true,
		Origin:      model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
		Approval:    &model.Attribution{Actor: actor},
		CreatedAt:   &now,
	}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: taskID + "-1", TaskID: taskID, Sandbox: taskID + "-1",
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
	}), store))

	p := startMCPServer(t, bin,
		"-sandbox-root", t.TempDir(),
		"-server", daemonURL,
		"-task", taskID,
		"-interactive")

	if names := toolNames(t, p); !names["chat"] {
		t.Fatalf("tools/list is missing chat despite -interactive; got %v", names)
	}

	for exchange := 1; exchange <= 3; exchange++ {
		agentMsg := fmt.Sprintf("agent prompt for exchange %d", exchange)
		humanReply := fmt.Sprintf("human reply for exchange %d", exchange)

		type callOutcome struct {
			text   string
			err    error
			doneAt time.Time
		}
		doneCh := make(chan callOutcome, 1)
		go func() {
			res, err := p.CallTool(ctx, "chat", map[string]any{"message": agentMsg})
			if err != nil {
				doneCh <- callOutcome{err: err, doneAt: time.Now()}
				return
			}
			if res.IsError {
				doneCh <- callOutcome{err: fmt.Errorf("chat tool error: %s", res.Text()), doneAt: time.Now()}
				return
			}
			doneCh <- callOutcome{text: res.Text(), doneAt: time.Now()}
		}()

		// Wait until the agent's message lands in the task's conversation so
		// the chat tool is parked inside TaskMessagesWait.
		deadline := time.Now().Add(2 * time.Second)
		for {
			comments, err := store.Comments(ctx, taskID)
			if err != nil {
				t.Fatalf("Comments: %v", err)
			}
			if len(comments) > 0 && comments[len(comments)-1].Body == agentMsg {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("exchange %d: agent chat message %q did not appear within 2s", exchange, agentMsg)
			}
			time.Sleep(2 * time.Millisecond)
		}

		// Give the MCP server a moment to enter the long-poll GET /messages?wait_ms=...
		time.Sleep(15 * time.Millisecond)

		replySentAt := time.Now()
		resp, err := accessPost(daemonURL+"/api/tasks/"+taskID+"/comments", "application/json",
			strings.NewReader(fmt.Sprintf(`{"body":%q}`, humanReply)))
		if err != nil {
			t.Fatalf("POST /comments: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /comments status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		out := <-doneCh
		if out.err != nil {
			t.Fatalf("exchange %d CallTool(chat): %v\nstderr:\n%s", exchange, out.err, p.stderr.String())
		}
		if !strings.Contains(out.text, humanReply) {
			t.Errorf("exchange %d chat result = %q, want %q", exchange, out.text, humanReply)
		}
		latency := out.doneAt.Sub(replySentAt)
		t.Logf("in-run subsequent exchange %d latency (POST /messages -> MCP chat return): %v", exchange, latency)
		if latency >= time.Second {
			t.Errorf("exchange %d latency = %v, want < 1s ceiling", exchange, latency)
		}
	}
}

func TestInteractiveInitialStartupAndAcrossRunLatencyEndToEnd(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	var mu sync.Mutex
	var turns []agent.RunConfig
	var firstTurnTimes []time.Time
	turnDoneCh := make(chan int, 10)

	ag := &stubInteractiveAgent{
		onRun: func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			if len(cfg.Tools) > 0 {
				_ = cfg.Tools[0].Handler(runCtx, map[string]any{"path": "."})
			}
			now := time.Now()
			mu.Lock()
			turns = append(turns, cfg)
			turnNum := len(turns)
			firstTurnTimes = append(firstTurnTimes, now)
			mu.Unlock()
			return &agent.Result{
				FinalText:        fmt.Sprintf("interactive reply %d", turnNum),
				Transcript:       fmt.Sprintf("interactive reply %d", turnNum),
				ResumableSession: "sess-e2e-1",
			}, nil
		},
	}

	sbx := orchestrator.NewHostSandboxes(t.TempDir())
	inFlight := &orchestrator.InFlight{}
	t.Cleanup(func() { _ = inFlight.Wait(context.Background()) })
	cycleTimes := orchestrator.NewCycleTimes(10)

	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  orchestrator.StaticFramework(ag),
		MaxWorkers: 2,
		Runs:       inFlight,
		CycleTimes: cycleTimes,
		Config: orchestrator.Config{
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New(), selfdebug.New()),
			SessionSandboxIdleTTL: 30 * time.Minute,
			WaitForSessionReply:   false,
		},
	}

	wakes := make(chan struct{}, 16)
	wakeFn := func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	}

	daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		Wake:         wakeFn,
	}), store))

	live := daemon.NewLiveConfig(store, sbx, daemon.Config{PollInterval: 30 * time.Second}, nil)
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		daemon.ReconcileWith(ctx, deps, live, wakes, nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-reconcileDone
	})

	// Wait for the initial reconcile pass to completely finish before measuring.
	initDeadline := time.Now().Add(10 * time.Second)
	for {
		if _, observed := cycleTimes.History(); observed >= 1 {
			break
		}
		if time.Now().After(initDeadline) {
			t.Fatal("timed out waiting for initial reconcile cycle")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 1. Initial Interactive Startup Test (< 45s budget, < 250ms in local sandbox).
	createStart := time.Now()
	resp, err := accessPost(daemonURL+"/api/tasks", "application/json",
		strings.NewReader(`{"title":"Interactive startup e2e","description":"hello session","interactive":true,"noRepo":true}`))
	if err != nil {
		t.Fatalf("POST /api/tasks: %v", err)
	}
	var created ui.Task
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		resp.Body.Close()
		t.Fatalf("decoding created task: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/tasks status = %d, want 201", resp.StatusCode)
	}

	waitForAwaitingReply := func(wantTurns int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			mu.Lock()
			gotTurns := len(turns)
			mu.Unlock()
			st, _ := store.State(ctx, created.ID)
			if gotTurns == wantTurns && !inFlight.Busy(created.ID) && orchestrator.WarmSessionSandboxID(store, created.ID) != "" && st == model.StateAwaitingReply {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for turn %d and awaiting_reply (gotTurns=%d, busy=%v, warm=%q, state=%s)",
					wantTurns, gotTurns, inFlight.Busy(created.ID), orchestrator.WarmSessionSandboxID(store, created.ID), st)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	_ = turnDoneCh

	waitForAwaitingReply(1)
	mu.Lock()
	startupLatency := firstTurnTimes[0].Sub(createStart)
	mu.Unlock()
	t.Logf("initial interactive session startup (POST /api/tasks -> agent first turn): %v", startupLatency)
	if startupLatency >= 5*time.Second {
		t.Errorf("initial interactive startup = %v, want < 5s in local sandbox (< 45s production ceiling)", startupLatency)
	}

	// Verify per-phase startup breakdown on GET /api/tasks/{id} and GET /api/metrics.
	detailResp, err := accessGet(daemonURL + "/api/tasks/" + created.ID)
	if err != nil {
		t.Fatalf("GET /api/tasks/%s: %v", created.ID, err)
	}
	var detail ui.TaskDetail
	if err := json.NewDecoder(detailResp.Body).Decode(&detail); err != nil {
		detailResp.Body.Close()
		t.Fatalf("decoding TaskDetail: %v", err)
	}
	detailResp.Body.Close()
	if len(detail.Attempts) != 1 || detail.Attempts[0].Startup == nil {
		t.Fatalf("TaskDetail.Attempts = %+v, want 1 attempt with Startup breakdown", detail.Attempts)
	}
	st := detail.Attempts[0].Startup
	if st.QueuedToDispatchedSeconds == nil || st.SandboxAcquireSeconds == nil ||
		st.PrepareSandboxSeconds == nil || st.AgentLaunchToFirstTurnSeconds == nil ||
		st.StartedToFirstTurnSeconds == nil {
		t.Errorf("AttemptStartup missing phase fields: %+v", st)
	}

	metricsResp, err := accessGet(daemonURL + "/api/metrics?window=1h")
	if err != nil {
		t.Fatalf("GET /api/metrics: %v", err)
	}
	var rep ui.MetricsReport
	if err := json.NewDecoder(metricsResp.Body).Decode(&rep); err != nil {
		metricsResp.Body.Close()
		t.Fatalf("decoding ui.MetricsReport: %v", err)
	}
	metricsResp.Body.Close()
	if len(rep.InteractiveStartup) != 5 {
		t.Fatalf("len(rep.InteractiveStartup) = %d, want 5", len(rep.InteractiveStartup))
	}
	for _, stage := range rep.InteractiveStartup {
		if stage.N < 1 {
			t.Errorf("GET /api/metrics InteractiveStartup stage %q N = %d, want >= 1", stage.Stage, stage.N)
		}
	}

	// 2. Subsequent Across-Run Exchanges Test (< 1s ceiling, < 250ms in local sandbox).
	for turn := 2; turn <= 3; turn++ {
		msgStart := time.Now()
		mresp, err := accessPost(daemonURL+"/api/tasks/"+created.ID+"/comments", "application/json",
			strings.NewReader(fmt.Sprintf(`{"body":"follow-up turn %d"}`, turn)))
		if err != nil {
			t.Fatalf("turn %d POST /comments: %v", turn, err)
		}
		mresp.Body.Close()
		if mresp.StatusCode != http.StatusOK {
			t.Fatalf("turn %d POST /comments status = %d, want 200", turn, mresp.StatusCode)
		}

		waitForAwaitingReply(turn)
		mu.Lock()
		turnLatency := firstTurnTimes[turn-1].Sub(msgStart)
		reused := turns[turn-1].SandboxReused
		mu.Unlock()
		t.Logf("across-run subsequent turn %d latency (POST /messages -> woken dispatch -> agent turn): %v (SandboxReused=%v)",
			turn, turnLatency, reused)
		if turnLatency >= 2*time.Second {
			t.Errorf("turn %d latency = %v, want < 2s under -race (< 1s uninstrumented ceiling)", turn, turnLatency)
		}
		if !reused {
			t.Errorf("turn %d SandboxReused = false, want true", turn)
		}
	}
}

type stubInteractiveAgent struct {
	onRun func(context.Context, agent.RunConfig) (*agent.Result, error)
}

func (a *stubInteractiveAgent) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return a.onRun(ctx, cfg)
}
