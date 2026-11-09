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

// Command consolerestserver is a real, disposable Grain deployment for the
// console resting-note walkthrough (docs/test-plans/2026-10-11.md §9). `grain
// demo` runs no orchestrator, so nothing in it ever parks a console session;
// this is the same store, UI server and orchestrator cycle a deployment runs,
// with one standing session configured.
//
// The live console-session boundary is the one thing scripted:
//
//   - the session's agent provider answers every dispatched run with one
//     line of final text, and records the prompt grain assembled for it
//     (GET /e2e/prompts), which is the "next run's assembled prompt" the
//     walkthrough inspects; and
//   - POST /e2e/end-exchange stands in for a live run that finished its
//     exchange without anyone replying: it records the session's task as
//     completed, exactly as a finished run's result does
//     (orchestrator's finish path), and the real reconciler then parks it
//     again. That is one park cycle with no human reply.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type scriptedMCPCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type scriptedTurn struct {
	MCPCalls  []scriptedMCPCall `json:"mcpCalls"`
	FinalText string            `json:"finalText"`
	// DelayMsBeforeExit holds the turn open this long after its last MCP
	// call, as a real agent process lingers after ask_question before it
	// exits: the window in which a parked task's attempt is still live
	// (the park-settled `grain wait --until awaiting-reply` race, #888).
	DelayMsBeforeExit int `json:"delayMsBeforeExit,omitempty"`
}

type recordedToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Text      string         `json:"text"`
	IsError   bool           `json:"isError"`
}

type recordedRun struct {
	TaskID        string             `json:"taskId"`
	Turn          int                `json:"turn"`
	Prompt        string             `json:"prompt"`
	Reads         []string           `json:"reads"`
	Writes        []string           `json:"writes"`
	Grants        []string           `json:"grants"`
	NetworkAccess string             `json:"networkAccess"`
	ToolCalls     []recordedToolCall `json:"toolCalls"`
}

type interactiveControl struct {
	replies    int
	lastSeenID int64
	holdNext   bool
	releaseCh  chan struct{}
}

// scriptedSession is the session's agent: it answers in plain text and
// keeps every prompt it was given. When interactive control is configured for
// a task via /e2e/interactive-control, runs for that task can be held in
// Running state and answer with all human comments received up to release.
// Tasks queued via /e2e/script-runs execute scripted tool calls through
// a real `grain mcpserver` subprocess against this server.
type scriptedSession struct {
	mu                    sync.Mutex
	store                 *model.Store
	serverURL             string
	tokenFile             string
	prompts               []string
	lastInteractiveTaskID string
	interactive           map[string]*interactiveControl
	scriptedTurns         map[string][]scriptedTurn
	scriptedByTitle       map[string][]scriptedTurn
	runs                  []recordedRun
}

func (s *scriptedSession) setServer(serverURL, tokenFile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serverURL = serverURL
	s.tokenFile = tokenFile
}

func (s *scriptedSession) queueScriptedTurns(taskID, title string, turns []scriptedTurn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if taskID != "" {
		if s.scriptedTurns == nil {
			s.scriptedTurns = make(map[string][]scriptedTurn)
		}
		s.scriptedTurns[taskID] = append(s.scriptedTurns[taskID], turns...)
		return
	}
	if s.scriptedByTitle == nil {
		s.scriptedByTitle = make(map[string][]scriptedTurn)
	}
	s.scriptedByTitle[title] = append(s.scriptedByTitle[title], turns...)
}

func (s *scriptedSession) recordedRuns(taskID string) []recordedRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []recordedRun
	for _, r := range s.runs {
		if taskID == "" || r.TaskID == taskID {
			out = append(out, r)
		}
	}
	return out
}

func grainBinary() string {
	for _, k := range []string{"GRAIN_E2E_DEMO_BIN", "GRAIN_BIN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return "grain"
}

func (s *scriptedSession) runScriptedTurn(ctx context.Context, cfg agent.RunConfig, turn scriptedTurn, serverURL, tokenFile string) (*agent.Result, error) {
	sandboxDir := cfg.SandboxRoot
	if sandboxDir == "" {
		var err error
		sandboxDir, err = os.MkdirTemp("", "grain-e2e-mcp-sandbox-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(sandboxDir)
	}
	args := []string{
		"mcpserver",
		"-sandbox-root", sandboxDir,
		"-server", serverURL,
		"-task", cfg.TaskID,
	}
	args = append(args, agent.GrantArgs(cfg)...)
	if cfg.Interactive {
		args = append(args, "-interactive")
	}
	cmd := exec.CommandContext(ctx, grainBinary(), args...)
	env := os.Environ()
	if tokenFile != "" {
		env = append(env, "GRAIN_LOCAL_API_TOKEN_FILE="+tokenFile)
	}
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting grain mcpserver: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdout)
	callRPC := func(id int, method string, params any) (map[string]any, error) {
		reqBytes, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  method,
			"params":  params,
		})
		if err != nil {
			return nil, err
		}
		if _, err := stdin.Write(append(reqBytes, '\n')); err != nil {
			return nil, fmt.Errorf("writing %s to mcpserver: %w (stderr: %s)", method, err, stderr.String())
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("reading %s from mcpserver: %w (stderr: %s)", method, err, stderr.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, err
		}
		return resp, nil
	}

	if _, err := callRPC(1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "consolerestserver", "version": "1.0.0"},
	}); err != nil {
		return nil, err
	}

	var agentCalls []agent.ToolCall
	var recorded []recordedToolCall
	for i, c := range turn.MCPCalls {
		resp, err := callRPC(i+2, "tools/call", map[string]any{
			"name":      c.Name,
			"arguments": c.Arguments,
		})
		if err != nil {
			return nil, err
		}
		var text string
		var isErr bool
		if resObj, ok := resp["result"].(map[string]any); ok {
			isErr, _ = resObj["isError"].(bool)
			if content, ok := resObj["content"].([]any); ok {
				for _, item := range content {
					if m, ok := item.(map[string]any); ok {
						if t, ok := m["text"].(string); ok {
							text += t
						}
					}
				}
			}
		} else if errObj, ok := resp["error"].(map[string]any); ok {
			isErr = true
			text, _ = errObj["message"].(string)
		}
		agentCalls = append(agentCalls, agent.ToolCall{
			Name:      c.Name,
			Arguments: c.Arguments,
			IsError:   isErr,
		})
		recorded = append(recorded, recordedToolCall{
			Name:      c.Name,
			Arguments: c.Arguments,
			Text:      text,
			IsError:   isErr,
		})
	}
	_ = stdin.Close()
	_ = cmd.Wait()
	if turn.DelayMsBeforeExit > 0 {
		select {
		case <-time.After(time.Duration(turn.DelayMsBeforeExit) * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	var reads, writes []string
	grants := append([]string(nil), cfg.Grants...)
	var netAccess string
	if tk, err := s.store.GetTask(ctx, cfg.TaskID); err == nil && tk != nil {
		for _, r := range tk.Reads {
			reads = append(reads, r.String())
		}
		for _, w := range tk.Writes {
			writes = append(writes, w.Repo.String())
		}
		for _, g := range tk.Grants {
			if !slices.Contains(grants, g.Capability) {
				grants = append(grants, g.Capability)
			}
		}
		netAccess = string(tk.NetworkAccess)
	}
	s.mu.Lock()
	turnNum := 1
	for _, r := range s.runs {
		if r.TaskID == cfg.TaskID {
			turnNum++
		}
	}
	s.runs = append(s.runs, recordedRun{
		TaskID:        cfg.TaskID,
		Turn:          turnNum,
		Prompt:        cfg.Prompt,
		Reads:         reads,
		Writes:        writes,
		Grants:        grants,
		NetworkAccess: netAccess,
		ToolCalls:     recorded,
	})
	s.mu.Unlock()

	finalText := turn.FinalText
	if finalText == "" {
		finalText = fmt.Sprintf("Scripted MCP turn %d finished.", turnNum)
	}
	return &agent.Result{
		FinalText:        finalText,
		Transcript:       finalText,
		ResumableSession: fmt.Sprintf("scripted-session-%s", cfg.TaskID),
		ToolCalls:        agentCalls,
	}, nil
}

func closeRelease(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (s *scriptedSession) configureInteractive(taskID string, holdNext, release bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if taskID == "" {
		taskID = s.lastInteractiveTaskID
	}
	if taskID == "" {
		return
	}
	s.lastInteractiveTaskID = taskID
	if s.interactive == nil {
		s.interactive = make(map[string]*interactiveControl)
	}
	ctrl := s.interactive[taskID]
	if ctrl == nil {
		ctrl = &interactiveControl{}
		s.interactive[taskID] = ctrl
	}
	if holdNext {
		closeRelease(ctrl.releaseCh)
		ctrl.holdNext = true
		ctrl.releaseCh = make(chan struct{})
	}
	if release {
		closeRelease(ctrl.releaseCh)
		ctrl.releaseCh = nil
		ctrl.holdNext = false
	}
}

func (s *scriptedSession) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var taskTitle string
	if tk, err := s.store.GetTask(ctx, cfg.TaskID); err == nil && tk != nil {
		taskTitle = tk.Title
	}
	s.mu.Lock()
	if len(s.scriptedTurns[cfg.TaskID]) == 0 && taskTitle != "" && len(s.scriptedByTitle[taskTitle]) > 0 {
		if s.scriptedTurns == nil {
			s.scriptedTurns = make(map[string][]scriptedTurn)
		}
		s.scriptedTurns[cfg.TaskID] = s.scriptedByTitle[taskTitle]
		delete(s.scriptedByTitle, taskTitle)
	}
	if queue := s.scriptedTurns[cfg.TaskID]; len(queue) > 0 {
		turn := queue[0]
		s.scriptedTurns[cfg.TaskID] = queue[1:]
		serverURL := s.serverURL
		tokenFile := s.tokenFile
		s.mu.Unlock()
		return s.runScriptedTurn(ctx, cfg, turn, serverURL, tokenFile)
	}
	ctrl, isInteractive := s.interactive[cfg.TaskID]
	var waitCh chan struct{}
	if isInteractive && ctrl.holdNext {
		waitCh = ctrl.releaseCh
		ctrl.holdNext = false
	}
	if !isInteractive {
		s.prompts = append(s.prompts, cfg.Prompt)
		n := len(s.prompts)
		s.mu.Unlock()
		answer := fmt.Sprintf("Scripted answer %d.", n)
		return &agent.Result{FinalText: answer, Transcript: answer}, nil
	}
	s.mu.Unlock()

	if waitCh != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-waitCh:
		}
	}

	s.mu.Lock()
	seenID := ctrl.lastSeenID
	s.mu.Unlock()

	comments, err := s.store.Comments(ctx, cfg.TaskID)
	if err != nil {
		return nil, err
	}
	var received []string
	var maxID int64
	for _, c := range comments {
		if c.Author.Actor.Kind == model.PrincipalHuman && c.ID > seenID {
			received = append(received, strings.TrimSpace(c.Body))
			if c.ID > maxID {
				maxID = c.ID
			}
		}
	}
	if maxID > 0 {
		_ = s.store.AdvanceDelivered(ctx, cfg.TaskID, maxID, time.Now().UTC())
	}

	s.mu.Lock()
	if maxID > ctrl.lastSeenID {
		ctrl.lastSeenID = maxID
	}
	ctrl.replies++
	replyNum := ctrl.replies
	s.mu.Unlock()

	answer := fmt.Sprintf("Agent chat reply %d: received [%s]", replyNum, strings.Join(received, ", "))
	return &agent.Result{
		FinalText:        answer,
		Transcript:       answer,
		ResumableSession: "warm-session-e2e",
		ToolCalls: []agent.ToolCall{{
			Name:      "comment_on_issue",
			Arguments: map[string]any{"comment": answer},
		}},
	}, nil
}

func (s *scriptedSession) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

// taskCanceller is ui.Config.TaskCanceller over orchestrator.TaskCancellers,
// as the daemon's taskCancellerAdapter is.
type taskCanceller struct{ cancellers *orchestrator.TaskCancellers }

func (c taskCanceller) CancelTask(_ context.Context, taskID string) bool {
	return c.cancellers.Cancel(taskID, orchestrator.ErrTurnStopped)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type isolatingHostSandboxes struct {
	*orchestrator.HostSandboxes
}

func (isolatingHostSandboxes) IsolatesNetwork() bool { return true }

func (s isolatingHostSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	shape.NetworkAccess = model.NetworkOpen
	return s.HostSandboxes.Acquire(ctx, name, shape)
}

func startGitServer(root string) (*httptest.Server, error) {
	gitRoot := filepath.Join(root, "git")
	if err := os.MkdirAll(gitRoot, 0o755); err != nil {
		return nil, err
	}
	var mu sync.Mutex
	ensureBare := func(owner, name string) error {
		mu.Lock()
		defer mu.Unlock()
		bare := filepath.Join(gitRoot, owner, name+".git")
		if _, err := os.Stat(bare); err == nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
			return err
		}
		if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
			return fmt.Errorf("git init --bare: %w: %s", err, out)
		}
		work, err := os.MkdirTemp(root, "seed-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(work)
		for _, args := range [][]string{
			{"init", "--initial-branch=main", work},
			{"-C", work, "config", "user.name", "E2E Seed"},
			{"-C", work, "config", "user.email", "seed@example.test"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("git %v: %w: %s", args, err, out)
			}
		}
		if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# "+owner+"/"+name+"\n"), 0o644); err != nil {
			return err
		}
		for _, args := range [][]string{
			{"-C", work, "add", "README.md"},
			{"-C", work, "commit", "-m", "Initial commit"},
			{"-C", work, "push", bare, "main:main"},
			{"-C", bare, "config", "http.receivepack", "true"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("git %v: %w: %s", args, err, out)
			}
		}
		return nil
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("finding git: %w", err)
	}
	backend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + gitRoot,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 2 {
			owner := parts[0]
			name := strings.TrimSuffix(parts[1], ".git")
			if owner != "" && name != "" {
				_ = ensureBare(owner, name)
			}
		}
		backend.ServeHTTP(w, r)
	})), nil
}

func run() error {
	root, err := os.MkdirTemp("", "grain-console-rest-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	gitServer, err := startGitServer(root)
	if err != nil {
		return err
	}
	defer gitServer.Close()

	var tokenFile string
	if code := os.Getenv("GRAIN_E2E_ACCESS_CODE"); code != "" {
		tokenFile = filepath.Join(root, "access-code")
		if err := os.WriteFile(tokenFile, []byte(code+"\n"), 0o600); err != nil {
			return err
		}
	}

	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(root, "data")))
	if err != nil {
		return err
	}
	defer db.Close()
	store := model.New(db)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := store.Init(ctx); err != nil {
		return err
	}
	cfg := model.DefaultConfig()
	cfg.MaxWorkers = 2
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID: model.DirectorSessionID, Title: "Director",
		Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
	}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		return err
	}

	sandboxRoot := filepath.Join(root, "sandboxes")
	if err := os.MkdirAll(sandboxRoot, 0o755); err != nil {
		return err
	}
	defaultRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
	provider := &scriptedSession{store: store}
	// Stop reaches a held run the way it does on a daemon
	// (daemon.taskCancellerAdapter): the run's own context is cancelled
	// with orchestrator.ErrTurnStopped, rather than only its stored row
	// being finished while the scripted agent goes on holding the task.
	cancellers := orchestrator.NewTaskCancellers()
	inFlight := &orchestrator.InFlight{}
	wakeCh := make(chan struct{}, 1)
	wake := func() {
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	handler := ui.NewServer(ui.Config{
		TaskCanceller: taskCanceller{cancellers},
		Wake:          wake,
		Actor:         model.Principal{Kind: model.PrincipalHuman, ID: "console-rest-e2e-operator"},
		DefaultTarget: &defaultRepo, Capabilities: append(ui.OfferedCapabilities(), ui.APICapabilities()...),
		// The run's access code (e2e/global-setup.js): every caller,
		// loopback included, shows it, as on every grain daemon.
		LocalAPIToken: os.Getenv("GRAIN_E2E_ACCESS_CODE"),
		// A secret store, as every daemon has: without one a run parked
		// on request_secret could never be answered here (PUT
		// /api/tasks/{id}/secret answered 404 "no local secrets
		// directory"), so the request_secret -> set -> requeue loop
		// went unexercised end to end.
		Secrets: secrets.New(filepath.Join(root, "secrets")),
		// The orchestrator's sub-cases, as on a daemon and grain demo,
		// so a parked run's reason names its ask (#782) here too.
		Reasons: daemon.ReasonClassifier{},
	}, store)
	// api grants resolve as on every grain daemon
	// (ui.AgentAPIDynamicProvider): every console session holds
	// model.ConsoleSessionCIReadGrant by default.
	registry := model.NewCapabilityRegistry()
	registry.RegisterDynamic(ui.AgentAPIDynamicProvider)
	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: isolatingHostSandboxes{orchestrator.NewHostSandboxes(sandboxRoot)},
		Framework: orchestrator.StaticFramework(provider),
		Runs:      inFlight,
		Config: orchestrator.Config{
			Capabilities:       registry,
			GitRemoteBase:      gitServer.URL,
			TaskCancellers:     cancellers,
			CancelPollInterval: 50 * time.Millisecond,
		},
		MaxWorkers: 2,
	}

	// One cycle at a time, whether the ticker or a test hook asked for it,
	// so a hook's write never lands in the middle of a reconcile. The
	// cycle's clock is read once it holds the lock, not when it was asked
	// for: a cycle that waited behind another one with an earlier time
	// would stamp the answer it posts before a reply that arrived while
	// it waited, and the chat, which sorts by time, drew the answer above
	// the message it answers.
	var cycle sync.Mutex
	runCycle := func() error {
		cycle.Lock()
		defer cycle.Unlock()
		return orchestrator.RunCycle(ctx, deps, time.Now().UTC())
	}
	if err := runCycle(); err != nil {
		return fmt.Errorf("initial console session cycle: %w", err)
	}
	if err := inFlight.Wait(ctx); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /e2e/prompts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"prompts": provider.snapshot()})
	})
	mux.HandleFunc("POST /e2e/script-runs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TaskID string         `json:"taskId"`
			Title  string         `json:"title"`
			Turns  []scriptedTurn `json:"turns"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.TaskID == "" && body.Title == "" {
			http.Error(w, "taskId or title is required", http.StatusBadRequest)
			return
		}
		provider.queueScriptedTurns(body.TaskID, body.Title, body.Turns)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /e2e/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runs": provider.recordedRuns(r.URL.Query().Get("task")),
		})
	})
	mux.HandleFunc("POST /e2e/interactive-control", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TaskID     string `json:"taskId"`
			HoldNext   bool   `json:"holdNext"`
			ReleaseRun bool   `json:"releaseRun"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		provider.configureInteractive(body.TaskID, body.HoldNext, body.ReleaseRun)
		w.WriteHeader(http.StatusNoContent)
	})
	waitIdle := func(waitCtx context.Context, taskID string) error {
		for inFlight.Busy(taskID) {
			select {
			case <-waitCtx.Done():
				return waitCtx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
		return nil
	}
	mux.HandleFunc("POST /e2e/end-exchange", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("task")
		if id == "" {
			http.Error(w, "task is required", http.StatusBadRequest)
			return
		}
		if err := waitIdle(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusRequestTimeout)
			return
		}
		cycle.Lock()
		now := time.Now().UTC()
		err := store.ObserveField(r.Context(), id, now, func(o *model.Observation) {
			o.PendingQuestionCommentID = nil
		})
		cycle.Unlock()
		if err == nil {
			err = runCycle()
		}
		if err == nil {
			err = waitIdle(r.Context(), id)
		}
		if err == nil {
			err = runCycle()
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", handler)

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	provider.setServer("http://"+listener.Addr().String(), tokenFile)
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	cyclesDone := make(chan struct{})
	go func() {
		defer close(cyclesDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wakeCh:
				if err := runCycle(); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "console rest e2e cycle: %v\n", err)
				}
			case <-ticker.C:
				if err := runCycle(); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "console rest e2e cycle: %v\n", err)
				}
			}
		}
	}()
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()
	fmt.Printf("grain console rest e2e serving at http://%s\n", listener.Addr())

	select {
	case <-ctx.Done():
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	<-cyclesDone
	_ = inFlight.Wait(shutdownCtx)
	return nil
}
