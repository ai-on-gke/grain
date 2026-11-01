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
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// deliveryWorld is docs/test-plans/2026-11-01-late.md §1's environment: the
// real store, UI server and reconcile loop, with a stub framework whose
// every run forks the real `grain mcpserver` against the daemon's HTTP API
// -- the sandbox CLI path a real agent's tool calls take, addenda poller and
// POST /api/tasks/{id}/delivered included -- and drives it from a script
// the test hands it, so each timing window is hit on purpose rather than by
// luck.
type deliveryWorld struct {
	t      *testing.T
	ctx    context.Context
	bin    string
	url    string
	store  *model.Store
	runs   *orchestrator.InFlight
	script chan deliveryScript

	handler http.Handler
	addr    string
	srvMu   sync.Mutex
	srv     *http.Server

	mu      sync.Mutex
	prompts map[string][]string // task ID -> each run's assembled prompt
}

// deliveryScript is one run's agent side: what it calls on the real MCP
// server and what it returns.
type deliveryScript func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result

func newDeliveryWorld(t *testing.T) *deliveryWorld {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	dir := t.TempDir()
	if keep := os.Getenv("GRAIN_WALKTHROUGH_DATA_DIR"); keep != "" {
		dir = keep // left behind for sqlite3 reads (docs/test-runs/*-interactive-message-delivery.md)
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx, cancel := context.WithCancel(context.Background())
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	w := &deliveryWorld{
		t: t, ctx: ctx, bin: buildGrainBinary(t), store: store,
		runs:    &orchestrator.InFlight{},
		script:  make(chan deliveryScript, 16),
		prompts: map[string][]string{},
	}
	sbx := orchestrator.NewHostSandboxes(t.TempDir())
	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  orchestrator.StaticFramework(&stubInteractiveAgent{onRun: w.run}),
		MaxWorkers: 2,
		Runs:       w.runs,
		CycleTimes: orchestrator.NewCycleTimes(10),
		Config: orchestrator.Config{
			StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities: model.NewCapabilityRegistry(durable.New(), selfdebug.New()),
			// Warm retention off: §1 is about the conversation, not the
			// sandbox, and the reaper can take a warm or just-claimed
			// session sandbox out from under a run (filed from task 2243).
			SessionSandboxIdleTTL: -1,
		},
	}
	wakes := make(chan struct{}, 16)
	wake := func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	}
	w.handler = ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		Wake:         wake,
	}), store)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w.addr = ln.Addr().String()
	w.url = "http://" + w.addr
	w.serve(ln)
	t.Cleanup(func() { w.stopServer() })
	live := daemon.NewLiveConfig(store, sbx, daemon.Config{PollInterval: 300 * time.Millisecond}, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		daemon.ReconcileWith(ctx, deps, live, wakes, nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = w.runs.Wait(context.Background())
	})
	return w
}

func (w *deliveryWorld) serve(ln net.Listener) {
	w.srvMu.Lock()
	defer w.srvMu.Unlock()
	w.srv = &http.Server{Handler: w.handler}
	go func() { _ = w.srv.Serve(ln) }()
}

// stopServer takes the daemon's API down, the way a daemon that has been
// sent SIGTERM stops answering its runs' mcpservers.
func (w *deliveryWorld) stopServer() {
	w.srvMu.Lock()
	defer w.srvMu.Unlock()
	if w.srv != nil {
		_ = w.srv.Close()
		w.srv = nil
	}
}

// startServer brings it back on the same address, which is all a run's
// mcpserver (-server) knows of it.
func (w *deliveryWorld) startServer() {
	w.t.Helper()
	ln, err := net.Listen("tcp", w.addr)
	if err != nil {
		w.t.Errorf("re-listening on %s: %v", w.addr, err)
		return
	}
	w.serve(ln)
}

// run is the stub framework: fork the real mcpserver for this run, the way
// agent/claude's --mcp-config does, and hand it to the next script.
func (w *deliveryWorld) run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	// The Director console session a fresh deployment files for itself is
	// dispatched too; it is not part of the walkthrough and must not take
	// a walkthrough task's script.
	if task, err := w.store.GetTask(ctx, cfg.TaskID); err != nil || task == nil || !strings.Contains(task.Title, "walkthrough") {
		return &agent.Result{FinalText: "ok"}, nil
	}
	w.mu.Lock()
	w.prompts[cfg.TaskID] = append(w.prompts[cfg.TaskID], cfg.Prompt)
	w.mu.Unlock()
	var script deliveryScript
	select {
	case script = <-w.script:
	case <-time.After(20 * time.Second):
		return nil, fmt.Errorf("walkthrough: no script queued for a run of %s", cfg.TaskID)
	}
	args := []string{"mcpserver", "-sandbox-root", cfg.SandboxRoot, "-server", w.url, "-task", cfg.TaskID,
		"-addenda-after", strconv.FormatInt(cfg.AddendaAfter, 10)}
	if cfg.Interactive {
		args = append(args, "-interactive")
	}
	cmd := exec.CommandContext(ctx, w.bin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := mcp.NewClient(stdin, stdout, stdin)
	defer func() {
		p.Close()
		werr := cmd.Wait()
		if w.t.Failed() {
			w.t.Logf("mcpserver for %s exited %v (run ctx: %v); stderr:\n%s", cfg.RunID, werr, ctx.Err(), stderr.String())
		}
	}()
	return script(ctx, cfg, p), nil
}

func (w *deliveryWorld) call(p *mcp.Client, name string, args map[string]any) string {
	w.t.Helper()
	res, err := p.CallTool(w.ctx, name, args)
	if err != nil {
		w.t.Errorf("tools/call %s: %v", name, err)
		return ""
	}
	return res.Text()
}

// comment posts body as the operator, the way the browser's composer does,
// and returns the new comment's ID.
func (w *deliveryWorld) comment(taskID, body string) int64 {
	w.t.Helper()
	resp, err := accessPost(w.url+"/api/tasks/"+taskID+"/comments", "application/json",
		strings.NewReader(fmt.Sprintf(`{"body":%q}`, body)))
	if err != nil {
		w.t.Errorf("POST comment: %v", err)
		return 0
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.t.Errorf("POST comment status = %d", resp.StatusCode)
		return 0
	}
	return w.commentID(taskID, body)
}

func (w *deliveryWorld) commentID(taskID, body string) int64 {
	w.t.Helper()
	cs, err := w.store.Comments(w.ctx, taskID)
	if err != nil {
		w.t.Error(err)
		return 0
	}
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i].Body == body {
			return cs[i].ID
		}
	}
	return 0
}

func (w *deliveryWorld) count(taskID, body string) int {
	cs, _ := w.store.Comments(w.ctx, taskID)
	n := 0
	for _, c := range cs {
		if strings.TrimSpace(c.Body) == body {
			n++
		}
	}
	return n
}

// snapshot is the row the report records before and after each step.
type deliverySnapshot struct {
	State     model.State
	Delivered int64
	Pending   int64
	Secret    string
	Runs      int
}

func (s deliverySnapshot) String() string {
	return fmt.Sprintf("state=%s delivered_comment_id=%d pending_question_comment_id=%d pending_secret=%q runs=%d",
		s.State, s.Delivered, s.Pending, s.Secret, s.Runs)
}

func (w *deliveryWorld) snap(taskID string) deliverySnapshot {
	w.t.Helper()
	st, _ := w.store.State(w.ctx, taskID)
	obs, _ := w.store.GetObservation(w.ctx, taskID)
	runs, _ := w.store.Runs(w.ctx, taskID)
	s := deliverySnapshot{State: st, Runs: len(runs)}
	if obs != nil {
		if obs.DeliveredCommentID != nil {
			s.Delivered = *obs.DeliveredCommentID
		}
		if obs.PendingQuestionCommentID != nil {
			s.Pending = *obs.PendingQuestionCommentID
		}
		s.Secret = obs.PendingSecret
	}
	return s
}

// settle waits until taskID has had wantRuns runs, none is in flight, and
// the task has stopped moving (not queued with a run about to start).
func (w *deliveryWorld) settle(taskID string, wantRuns int) deliverySnapshot {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		s := w.snap(taskID)
		if s.Runs >= wantRuns && !w.runs.Busy(taskID) && s.State != model.StateQueued && s.State != model.StateRunning {
			time.Sleep(400 * time.Millisecond) // one more reconcile pass must not move it
			if again := w.snap(taskID); again == s && !w.runs.Busy(taskID) {
				return s
			}
			continue
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("%s did not settle after %d run(s): %s", taskID, wantRuns, s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (w *deliveryWorld) prompt(taskID string, n int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.prompts[taskID]) < n {
		return ""
	}
	return w.prompts[taskID][n-1]
}

func (w *deliveryWorld) createTask(body string) string {
	w.t.Helper()
	resp, err := accessPost(w.url+"/api/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var created ui.Task
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil || resp.StatusCode != http.StatusCreated {
		w.t.Fatalf("POST /api/tasks = %d, %v", resp.StatusCode, err)
	}
	return created.ID
}

// TestInteractiveMessageDeliveryTimingWindowsWalkthrough walks
// docs/test-plans/2026-11-01-late.md §1 "not proved" 1 and 2: a human
// comment in each of the five timing windows around a real run's tool
// calls, each answered by the same run or the next one, the task never
// left awaiting a reply nobody will give nor completed.
func TestInteractiveMessageDeliveryTimingWindowsWalkthrough(t *testing.T) {
	w := newDeliveryWorld(t)
	chatID := ""
	turn := func(text string) *agent.Result { return &agent.Result{FinalText: text, Transcript: text} }

	// (a) before chat, and (b) during chat's wait.
	var aID, bID atomic.Int64
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		w.call(p, "run_command", map[string]any{"command": "true"})
		aID.Store(w.comment(cfg.TaskID, "window a: before chat"))
		t.Logf("(a) before: comment %d, %s", aID.Load(), w.snap(cfg.TaskID))
		if got := w.call(p, "chat", map[string]any{"message": "agent asks 1", "summary": "s"}); !strings.Contains(got, "window a: before chat") {
			t.Errorf("(a) chat result = %q, want the comment posted before chat", got)
		}
		t.Logf("(a) after chat: %s", w.snap(cfg.TaskID))
		go func() {
			for w.commentID(cfg.TaskID, "agent asks 2") == 0 {
				time.Sleep(10 * time.Millisecond)
			}
			time.Sleep(50 * time.Millisecond)
			bID.Store(w.comment(cfg.TaskID, "window b: during chat wait"))
		}()
		if got := w.call(p, "chat", map[string]any{"message": "agent asks 2", "summary": "s"}); !strings.Contains(got, "window b: during chat wait") {
			t.Errorf("(b) chat result = %q, want the comment posted during its wait", got)
		}
		return turn("answered a and b")
	}
	chatID = w.createTask(`{"title":"delivery walkthrough","description":"hello","interactive":true,"noRepo":true}`)
	s := w.settle(chatID, 1)
	t.Logf("(a)+(b) after run 1: comments a=%d b=%d; %s", aID.Load(), bID.Load(), s)
	if s.State != model.StateAwaitingReply || s.Delivered < bID.Load() || s.Runs != 1 {
		t.Errorf("(a)+(b): %s, want awaiting_reply with delivered >= %d after exactly one run", s, bID.Load())
	}

	// (c) during the turn's last ordinary tool call. The comment rides on
	// that call's result, but no tool call follows to acknowledge it
	// (pkg/mcp/addenda.go defers the ack), so the next run is given it too.
	var cID atomic.Int64
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		go func() {
			time.Sleep(300 * time.Millisecond)
			cID.Store(w.comment(cfg.TaskID, "window c: during last ordinary call"))
		}()
		got := w.call(p, "run_command", map[string]any{"command": "sleep 2"})
		t.Logf("(c) run_command result carries comment: %v; %s", strings.Contains(got, "window c"), w.snap(cfg.TaskID))
		return turn("answered c-turn")
	}
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		t.Logf("(c) re-dispatched run %s: AddendaAfter=%d, prompt carries the comment: %v", cfg.RunID, cfg.AddendaAfter,
			strings.Contains(cfg.Prompt, "window c: during last ordinary call"))
		return turn("answered c")
	}
	kick := w.comment(chatID, "kick run 2")
	t.Logf("(c) before: kick comment %d, %s", kick, w.snap(chatID))
	s = w.settle(chatID, 2)
	t.Logf("(c) after: comment c=%d; %s", cID.Load(), s)
	if s.State != model.StateAwaitingReply || s.Delivered < cID.Load() {
		t.Errorf("(c): %s, want awaiting_reply with delivered >= %d", s, cID.Load())
	}
	runsBefore := s.Runs

	// (d) during the final, turn-ending tool call.
	var dID atomic.Int64
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		w.call(p, "run_command", map[string]any{"command": "true"})
		go func() { dID.Store(w.comment(cfg.TaskID, "window d: during final call")) }()
		w.call(p, "comment_on_issue", map[string]any{"comment": "closing words d"})
		time.Sleep(200 * time.Millisecond)
		t.Logf("(d) after comment_on_issue: comment %d, %s", dID.Load(), w.snap(cfg.TaskID))
		return &agent.Result{FinalText: "closing words d", ToolCalls: []agent.ToolCall{{Name: "comment_on_issue",
			Arguments: map[string]any{"comment": "closing words d"}}}}
	}
	// (e) while ProcessResult runs: posted after the run's last tool call,
	// as the result goes back to the orchestrator.
	var eID atomic.Int64
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		t.Logf("(d) next run %s: AddendaAfter=%d, prompt carries the comment: %v", cfg.RunID, cfg.AddendaAfter,
			strings.Contains(cfg.Prompt, "window d: during final call"))
		if !strings.Contains(cfg.Prompt, "window d: during final call") {
			t.Errorf("(d): run after the turn-ending call was not given the comment in its prompt")
		}
		w.call(p, "run_command", map[string]any{"command": "true"})
		eID.Store(w.comment(cfg.TaskID, "window e: during ProcessResult"))
		return turn("answered e-turn")
	}
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		t.Logf("(e) next run %s: AddendaAfter=%d, prompt carries the comment: %v", cfg.RunID, cfg.AddendaAfter,
			strings.Contains(cfg.Prompt, "window e: during ProcessResult"))
		if !strings.Contains(cfg.Prompt, "window e: during ProcessResult") {
			t.Errorf("(e): next run's prompt is missing the comment posted during ProcessResult")
		}
		return turn("answered e")
	}
	kick = w.comment(chatID, "kick run d")
	t.Logf("(d) before: kick comment %d, %s", kick, w.snap(chatID))
	s = w.settle(chatID, runsBefore+3)
	t.Logf("(d)+(e) after: comments d=%d e=%d; %s", dID.Load(), eID.Load(), s)
	if s.State != model.StateAwaitingReply || s.Delivered < eID.Load() || s.Runs != runsBefore+3 {
		t.Errorf("(d)+(e): %s, want awaiting_reply with delivered >= %d after %d runs", s, eID.Load(), runsBefore+3)
	}
	if n := w.count(chatID, "closing words d"); n != 1 {
		t.Errorf("closing words d relayed %d times, want 1", n)
	}
}

func (w *deliveryWorld) notifications() string {
	w.t.Helper()
	resp, err := accessGet(w.url + "/api/notifications")
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	_, _ = io.Copy(&b, resp.Body)
	return b.String()
}

func (w *deliveryWorld) delivered(taskID string, through int64) int {
	w.t.Helper()
	resp, err := accessPost(w.url+"/api/tasks/"+taskID+"/delivered", "application/json",
		strings.NewReader(fmt.Sprintf(`{"through":%d}`, through)))
	if err != nil {
		w.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestInteractiveQuestionParkingWalkthrough walks §1 "not proved" 3: a
// question and a secret request parked, a comment that predates them
// leaving them parked, and a reply after them un-parking.
func TestInteractiveQuestionParkingWalkthrough(t *testing.T) {
	w := newDeliveryWorld(t)
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		return &agent.Result{FinalText: "hello"}
	}
	id := w.createTask(`{"title":"parking walkthrough","description":"hello","interactive":true,"noRepo":true}`)
	s := w.settle(id, 1)
	t.Logf("after run 1: %s", s)

	// A clean question: nothing posted during the run.
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		w.call(p, "ask_question", map[string]any{"question": "Q1: which one?", "summary": "Pick one"})
		return &agent.Result{ToolCalls: []agent.ToolCall{{Name: "ask_question",
			Arguments: map[string]any{"question": "Q1: which one?", "summary": "Pick one"}}}}
	}
	k := w.comment(id, "kick run 2")
	s = w.settle(id, 2)
	q1 := w.commentID(id, "Q1: which one?")
	t.Logf("question parked: kick=%d q1=%d; %s; notifications=%s", k, q1, s, w.notifications())
	if s.State != model.StateAwaitingReply || s.Pending != q1 {
		t.Errorf("after ask_question: %s, want awaiting_reply pending on %d", s, q1)
	}
	// A late ack naming a comment before the question must not un-park it.
	code := w.delivered(id, k)
	s2 := w.snap(id)
	t.Logf("POST delivered through=%d (predates the question) -> %d; %s", k, code, s2)
	if s2.Pending != q1 {
		t.Errorf("delivered through a pre-question comment un-parked the question: %s", s2)
	}

	// A human comment lands while the run's final ask_question is in
	// flight: the run never saw it, so the task must not sit parked on a
	// question whose answer may already be waiting.
	var hID atomic.Int64
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		if !strings.Contains(cfg.Prompt, "R1: the first") {
			t.Errorf("run 3 prompt is missing the reply to Q1")
		}
		w.call(p, "run_command", map[string]any{"command": "true"})
		hID.Store(w.comment(cfg.TaskID, "H: said while the question was being asked"))
		w.call(p, "ask_question", map[string]any{"question": "Q2: sure?", "summary": "Confirm"})
		t.Logf("after ask_question with H=%d posted before it: %s", hID.Load(), w.snap(cfg.TaskID))
		return &agent.Result{ToolCalls: []agent.ToolCall{{Name: "ask_question",
			Arguments: map[string]any{"question": "Q2: sure?", "summary": "Confirm"}}}}
	}
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		if !strings.Contains(cfg.Prompt, "H: said while the question was being asked") {
			t.Errorf("run after Q2 is missing H in its prompt")
		}
		w.call(p, "request_secret", map[string]any{"secret": "demo-key", "reason": "need it", "summary": "Set demo-key"})
		return &agent.Result{ToolCalls: []agent.ToolCall{{Name: "request_secret",
			Arguments: map[string]any{"secret": "demo-key", "reason": "need it", "summary": "Set demo-key"}}}}
	}
	r1 := w.comment(id, "R1: the first")
	t.Logf("reply R1=%d after Q1: %s", r1, w.snap(id))
	s = w.settle(id, 3)
	q2 := w.commentID(id, "Q2: sure?")
	t.Logf("after run 3: H=%d q2=%d; %s", hID.Load(), q2, s)
	if s.State == model.StateAwaitingReply && s.Delivered < hID.Load() {
		t.Errorf("run 3 parked on Q2 (%s) with H=%d never shown to any run", s, hID.Load())
		w.comment(id, "R2: nudge") // so the rest of the walkthrough can go on
	}
	s = w.settle(id, 4)
	sec := w.snap(id)
	t.Logf("secret parked: %s; notifications=%s", sec, w.notifications())
	if sec.Secret != "demo-key" || sec.Pending == 0 || sec.State != model.StateAwaitingReply {
		t.Errorf("after request_secret: %s, want parked on demo-key", sec)
	}
	code = w.delivered(id, w.commentID(id, "R1: the first"))
	t.Logf("POST delivered through R1 (predates the secret request) -> %d; %s", code, w.snap(id))
	if got := w.snap(id); got.Secret != "demo-key" {
		t.Errorf("delivered through a pre-request comment cleared the secret request: %s", got)
	}
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		return &agent.Result{FinalText: "thanks"}
	}
	r3 := w.comment(id, "R3: set it")
	t.Logf("reply R3=%d after the secret request: %s", r3, w.snap(id))
	s = w.settle(id, 5)
	t.Logf("after run 5: %s; notifications=%s", s, w.notifications())
	if s.Pending != 0 || s.Secret != "" || s.Delivered < r3 {
		t.Errorf("after the reply: %s, want un-parked with R3 delivered", s)
	}
}

// TestInteractiveGoalFinalTextRelayedOnceWalkthrough walks §1 "not proved"
// 5: a goal task's plain final text is relayed exactly once, with and
// without a comment landing as the result is processed.
func TestInteractiveGoalFinalTextRelayedOnceWalkthrough(t *testing.T) {
	w := newDeliveryWorld(t)
	var lateID atomic.Int64
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		w.call(p, "run_command", map[string]any{"command": "true"})
		return &agent.Result{FinalText: "goal answer one"}
	}
	id := w.createTask(`{"title":"goal walkthrough","description":"hello","interactive":true,"noRepo":true,` +
		`"goal":{"kind":"person-closes","spec":{}}}`)
	s := w.settle(id, 1)
	t.Logf("goal task after run 1 (no comment): %s; relayed %d time(s)", s, w.count(id, "goal answer one"))
	if n := w.count(id, "goal answer one"); n != 1 {
		t.Errorf("goal answer one relayed %d times, want 1", n)
	}
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		w.call(p, "run_command", map[string]any{"command": "true"})
		lateID.Store(w.comment(cfg.TaskID, "said as the goal answer is relayed"))
		return &agent.Result{FinalText: "goal answer two"}
	}
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		if !strings.Contains(cfg.Prompt, "said as the goal answer is relayed") {
			t.Errorf("goal task's next run is missing the late comment")
		}
		return &agent.Result{FinalText: "goal answer three"}
	}
	k := w.comment(id, "kick goal run 2")
	t.Logf("before run 2: kick=%d; %s", k, w.snap(id))
	s = w.settle(id, 3)
	t.Logf("goal task after runs 2-3: late comment %d; %s; relayed two=%d three=%d", lateID.Load(), s,
		w.count(id, "goal answer two"), w.count(id, "goal answer three"))
	if w.count(id, "goal answer two") != 1 || w.count(id, "goal answer three") != 1 {
		t.Errorf("goal answers relayed two=%d three=%d, want 1 each", w.count(id, "goal answer two"), w.count(id, "goal answer three"))
	}
	if s.Delivered < lateID.Load() || s.State == model.StateCompleted {
		t.Errorf("goal task after the late comment: %s, want it delivered and the task not completed", s)
	}
}

// TestInteractiveRestartBetweenPostAndDeliveryWalkthrough walks §1 "not
// proved" 6 at the HTTP boundary a run sees: a comment is posted while the
// run is in a tool call, the daemon's API goes down before the addenda
// poller can fetch it, and comes back on the same address. It is
// delivered exactly once, on the run's next tool call.
func TestInteractiveRestartBetweenPostAndDeliveryWalkthrough(t *testing.T) {
	w := newDeliveryWorld(t)
	var postedID atomic.Int64
	var seen atomic.Int32
	w.script <- func(ctx context.Context, cfg agent.RunConfig, p *mcp.Client) *agent.Result {
		w.call(p, "run_command", map[string]any{"command": "true"})
		go func() {
			time.Sleep(200 * time.Millisecond)
			postedID.Store(w.comment(cfg.TaskID, "said just before the restart"))
			w.stopServer()
			t.Logf("API down after comment %d: %s", postedID.Load(), w.snap(cfg.TaskID))
			time.Sleep(2 * time.Second)
			w.startServer()
			t.Logf("API back: %s", w.snap(cfg.TaskID))
		}()
		first := w.call(p, "run_command", map[string]any{"command": "sleep 1"})
		if strings.Contains(first, "said just before the restart") {
			seen.Add(1)
		}
		time.Sleep(2 * time.Second)
		for i := 0; i < 2; i++ {
			if strings.Contains(w.call(p, "run_command", map[string]any{"command": "true"}), "said just before the restart") {
				seen.Add(1)
			}
		}
		t.Logf("after the restart, seen in %d tool result(s): %s", seen.Load(), w.snap(cfg.TaskID))
		return &agent.Result{FinalText: "done"}
	}
	id := w.createTask(`{"title":"restart walkthrough","description":"hello","interactive":true,"noRepo":true}`)
	s := w.settle(id, 1)
	t.Logf("after run 1: comment %d; %s", postedID.Load(), s)
	if seen.Load() != 1 {
		t.Errorf("comment seen in %d tool results across the restart, want exactly 1", seen.Load())
	}
	if s.Delivered < postedID.Load() || s.Runs != 1 || s.State != model.StateAwaitingReply {
		t.Errorf("after the restart: %s, want awaiting_reply with comment %d delivered by the one run", s, postedID.Load())
	}
}
