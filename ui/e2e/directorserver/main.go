// Command directorserver is a real, disposable Grain deployment for the
// Director browser journey. Only the provider is scripted: the SQLite store,
// UI server, orchestrator, host sandbox, state checkout, MCP server and browser
// are the same implementations an operator drives in a deployment.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/admincaps"
	"github.com/ai-on-gke/grain/pkg/capability/bootstrap"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/capability/prioritydispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/sysstat"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const diagnosticSecret = "director-e2e-secret-42"
const diagnosticLimit = 256 * 1024

type sandboxHealth struct {
	inner *orchestrator.HostSandboxes
}

type taskCancellerAdapter struct {
	cancellers *orchestrator.TaskCancellers
}

func (a taskCancellerAdapter) CancelTask(_ context.Context, taskID string) bool {
	return a.cancellers.Cancel(taskID, orchestrator.ErrTurnStopped)
}

func (s sandboxHealth) SandboxCount() int { return s.inner.SandboxCount() }

func (s sandboxHealth) Health(ctx context.Context) []ui.SandboxSnapshot {
	got := s.inner.Health(ctx)
	out := make([]ui.SandboxSnapshot, len(got))
	for i, h := range got {
		termination := h.Error
		if h.Ready {
			termination = "running"
		} else if termination == "" {
			termination = "unknown"
		}
		out[i] = ui.SandboxSnapshot{
			Sandbox: h.Sandbox, Kind: h.Kind, Backend: h.Backend, Name: h.Name,
			CPUs: h.CPUs, MemoryMB: h.MemoryMB, DiskGB: h.DiskGB,
			NetworkAccess: h.NetworkAccess, Ready: h.Ready, Error: h.Error,
			TerminationReason: termination, LoadAverage: h.LoadAverage,
			MemoryUsedMB: h.MemoryUsedMB, MemoryTotalMB: h.MemoryTotalMB,
			SwapUsedMB: h.SwapUsedMB, SwapTotalMB: h.SwapTotalMB,
			DiskUsedMB: h.DiskUsedMB, DiskTotalMB: h.DiskTotalMB,
			NestedVirt: h.NestedVirt,
		}
	}
	return out
}

func hostPressure() (ui.HostPressure, error) {
	s, err := sysstat.Read()
	if err != nil {
		return ui.HostPressure{}, err
	}
	return ui.HostPressure{
		LoadAverage1: s.LoadAverage1, LoadAverage5: s.LoadAverage5,
		LoadAverage15: s.LoadAverage15, CPUs: s.CPUs,
		CPUUtilization: s.CPUUtilization, MemoryUsedMB: s.MemUsedMB,
		MemoryTotalMB: s.MemTotalMB, SwapUsedMB: s.SwapUsedMB,
		SwapTotalMB: s.SwapTotalMB,
	}, nil
}

type scriptedDirector struct {
	grainBin     string
	baseURL      string
	runs         atomic.Int64
	releaseFirst chan struct{}
}

func (f *scriptedDirector) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	args := []string{"mcpserver", "-sandbox-root", cfg.SandboxRoot, "-server", f.baseURL, "-task", cfg.TaskID}
	args = append(args, agent.GrantArgs(cfg)...)
	args = append(args, agent.ProposeRepoArgs(cfg)...)
	args = append(args, agent.InteractiveArgs(cfg)...)
	args = append(args, agent.AddendaArgs(cfg)...)
	args = append(args, agent.RunDeadlineArgs(ctx, cfg)...)
	server, err := mcp.StartCommandHTTP(ctx, f.grainBin, args)
	if err != nil {
		return nil, err
	}
	defer server.Close()

	var calls []agent.ToolCall
	call := func(name string, arguments map[string]any) (*mcp.CallResult, error) {
		result, err := callTool(ctx, server, name, arguments)
		if err != nil {
			return nil, err
		}
		calls = append(calls, agent.ToolCall{
			Name: name, Arguments: arguments, Text: result.Text(), IsError: result.IsError,
		})
		return result, nil
	}

	if strings.Contains(cfg.Prompt, "Hold this turn in running") {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if strings.Contains(cfg.Prompt, "File two follow-up tasks and record the standing handover summary.") {
		for i, item := range []struct {
			id, title, body string
		}{
			{
				id:    "triage-child-1",
				title: "Audit session redirect handling",
				body:  "Verify /tasks/<id> redirects for console sessions.",
			},
			{
				id:    "triage-child-2",
				title: "Verify session work tree links",
				body:  "Confirm WorkTree disclosure renders on ConsoleSessionPage.",
			},
		} {
			proposal, err := call("propose_task", map[string]any{
				"id":    item.id,
				"title": item.title,
				"body":  item.body,
			})
			if err != nil {
				return nil, err
			}
			if proposal.IsError {
				return nil, fmt.Errorf("propose_task %d failed: %s", i+1, proposal.Text())
			}
		}
		handover, err := call("write_handover", map[string]any{
			"handover": "Triage handover summary: audited redirect handling and queued two follow-up tasks.",
		})
		if err != nil {
			return nil, err
		}
		if handover.IsError {
			return nil, fmt.Errorf("write_handover failed: %s", handover.Text())
		}
		if _, err := call("comment_on_issue", map[string]any{
			"comment": "Filed two follow-up tasks and recorded the standing handover summary.",
		}); err != nil {
			return nil, err
		}
		return &agent.Result{
			FinalText:  "Filed two follow-up tasks and recorded the standing handover summary.",
			ToolCalls:  calls,
			Transcript: "filed two follow-up tasks; wrote handover",
		}, nil
	}
	if strings.Contains(cfg.Prompt, "Record director handover before delete.") {
		handover, err := call("write_handover", map[string]any{
			"handover": "Pre-delete director handover summary that must not survive deletion.",
		})
		if err != nil {
			return nil, err
		}
		if handover.IsError {
			return nil, fmt.Errorf("write_handover failed: %s", handover.Text())
		}
		if _, err := call("comment_on_issue", map[string]any{
			"comment": "Director pre-delete handover recorded.",
		}); err != nil {
			return nil, err
		}
		return &agent.Result{
			FinalText:  "Director pre-delete handover recorded.",
			ToolCalls:  calls,
			Transcript: "wrote pre-delete handover",
		}, nil
	}
	if !strings.Contains(cfg.Prompt, "Diagnose the controller and sandbox state") &&
		!strings.Contains(cfg.Prompt, "Which task is this reply attached to?") &&
		!strings.Contains(cfg.Prompt, "Stay here until I close this live task.") {
		return &agent.Result{
			FinalText:  "Session ready.",
			Transcript: "opened session from first message",
		}, nil
	}

	run := f.runs.Add(1)
	if run == 1 {
		// Keep the first real turn live until the browser has followed it
		// through every task surface. The provider is the one simulated
		// boundary in this fixture; dispatch, task state and polling remain
		// the production implementations the operator is exercising.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.releaseFirst:
		}

		checkout, err := call("read_file", map[string]any{
			"file_path": "grain-state/tables/grain_config.json",
		})
		if err != nil {
			return nil, err
		}
		if checkout.IsError || !strings.Contains(checkout.Text(), "controller-ready") ||
			strings.Contains(checkout.Text(), diagnosticSecret) {
			return nil, fmt.Errorf("sandbox state checkout was not the clean committed snapshot: %s", checkout.Text())
		}

		health, err := call("read_deployment_health", map[string]any{})
		if err != nil {
			return nil, err
		}
		var snapshot struct {
			Health       ui.SandboxHealthResponse `json:"health"`
			SandboxCount int                      `json:"sandboxCount"`
		}
		if health.IsError || json.Unmarshal([]byte(health.Text()), &snapshot) != nil ||
			!snapshot.Health.Enabled || snapshot.Health.Host == nil ||
			len(snapshot.Health.Sandboxes) != 1 || snapshot.SandboxCount != 1 {
			return nil, fmt.Errorf("deployment health was not a live controller plus one sandbox: %s", health.Text())
		}

		oversized, err := call("read_deployment_state", map[string]any{
			"path": "tables/oversized.json",
		})
		if err != nil {
			return nil, err
		}
		if !oversized.IsError || len(oversized.Text()) > diagnosticLimit ||
			!strings.Contains(oversized.Text(), "over the per-call limit") {
			return nil, fmt.Errorf("oversized controller state was not refused with a bounded answer: %s", oversized.Text())
		}

		state, err := call("read_deployment_state", map[string]any{
			"path": "tables/grain_config.json",
		})
		if err != nil {
			return nil, err
		}
		if state.IsError || len(state.Text()) > diagnosticLimit ||
			strings.Contains(state.Text(), diagnosticSecret) ||
			!strings.Contains(state.Text(), "[redacted: secret diagnostic/token]") {
			return nil, fmt.Errorf("controller state was not bounded and redacted: %s", state.Text())
		}

		proposalArgs := map[string]any{
			"id":    "pressure-follow-up",
			"title": "Investigate controller pressure",
			"body":  "Reproduce the controller and sandbox pressure observed by the Director diagnostic.",
		}
		proposal, err := call("propose_task", proposalArgs)
		if err != nil {
			return nil, err
		}
		match := regexp.MustCompile(`Proposed task #(\S+) is visible now: (\S+)\. It (?:remains|is filed) unapproved`).FindStringSubmatch(proposal.Text())
		if proposal.IsError || len(match) != 3 {
			return nil, fmt.Errorf("proposal did not return its durable task and link: %s", proposal.Text())
		}
		message := fmt.Sprintf(
			"Controller diagnostics are bounded and healthy: %d vCPU reported, with %d live sandbox; "+
				"an oversized state table was refused at the per-call limit. "+
				"The read-only `grain-state` checkout was available, and the %d-byte controller-state "+
				"answer replaced the stored credential with `[redacted: secret diagnostic/token]`.\n\n"+
				"[Open proposed task #%s](%s) — it is visible now and remains unapproved.",
			snapshot.Health.Host.CPUs, snapshot.SandboxCount, len(state.Text()), match[1], match[2],
		)
		if _, err := call("chat", map[string]any{"message": message}); err != nil {
			return nil, err
		}
		if _, err := call("comment_on_issue", map[string]any{"comment": "Diagnostic exchange complete."}); err != nil {
			return nil, err
		}
		return &agent.Result{
			FinalText: "Diagnostic exchange complete.", ToolCalls: calls,
			Transcript: "read sandbox state; read deployment health; read controller state; filed proposal; chatted",
		}, nil
	}

	if run >= 3 {
		// The final turn is deliberately left in flight so closing the real
		// task cancels a real attempt and leaves its separator in the chat.
		<-ctx.Done()
		return nil, ctx.Err()
	}

	message := fmt.Sprintf("This reply is attached to the open replacement task #%s, not its closed history.", cfg.TaskID)
	if _, err := call("chat", map[string]any{"message": message}); err != nil {
		return nil, err
	}
	if _, err := call("comment_on_issue", map[string]any{
		"comment": fmt.Sprintf("Replacement exchange complete on task #%s.", cfg.TaskID),
	}); err != nil {
		return nil, err
	}
	return &agent.Result{
		FinalText: "Replacement exchange complete.", ToolCalls: calls,
		Transcript: "answered on the replacement session task",
	}, nil
}

func callTool(ctx context.Context, server *mcp.ManagedHTTP, name string, arguments map[string]any) (*mcp.CallResult, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+server.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tools/call %s: %s: %s", name, resp.Status, data)
	}
	var envelope struct {
		Result mcp.CallResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("tools/call %s: %s (%d)", name, envelope.Error.Message, envelope.Error.Code)
	}
	return &envelope.Result, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var (
		rootFlag = flag.String("root", "", "persistent fixture root directory")
		addrFlag = flag.String("addr", "127.0.0.1:0", "listen address")
	)
	flag.Parse()

	grainBin := os.Getenv("GRAIN_E2E_DEMO_BIN")
	if grainBin == "" {
		return errors.New("GRAIN_E2E_DEMO_BIN is required")
	}
	root := *rootFlag
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "grain-director-e2e-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(root)
	}

	stateDir, gitServer, err := seedStateRepository(root)
	if err != nil {
		return err
	}
	defer gitServer.Close()

	dataDir := filepath.Join(root, "data")
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
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
	stored, err := store.GetConfig(ctx)
	if err != nil {
		return err
	}
	if stored == nil || stored.ConnectionsOnly() {
		cfg := model.DefaultConfig()
		cfg.MaxWorkers = 1
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
		}}
		if err := store.PutConfig(ctx, cfg); err != nil {
			return err
		}
	}

	secretsDir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return err
	}
	secretStore := secrets.Open(secrets.Config{
		File:    filepath.Join(secretsDir, secrets.DefaultFileName),
		KeyFile: filepath.Join(secretsDir, secrets.DefaultKeyFileName),
	})
	if err := secretStore.Set("diagnostic", "token", []byte(diagnosticSecret)); err != nil {
		return err
	}
	// The remote carries a clean export. The controller-local synchronized
	// checkout is made hostile after the push so the diagnostic's redactor,
	// not the sandbox checkout, is what protects the known credential.
	stateTable := filepath.Join(stateDir, "tables", "grain_config.json")
	if err := os.WriteFile(stateTable, []byte(
		"{\n  \"status\": \"controller-ready\",\n  \"credential\": \""+diagnosticSecret+"\"\n}\n",
	), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stateDir, "tables", "oversized.json"),
		bytes.Repeat([]byte("x"), diagnosticLimit+1), 0o644); err != nil {
		return err
	}

	sandboxRoot := filepath.Join(root, "sandboxes")
	if err := os.MkdirAll(sandboxRoot, 0o755); err != nil {
		return err
	}
	sandboxes := orchestrator.NewHostSandboxes(sandboxRoot)
	stateRepo := model.RepoRef{Owner: "acme", Name: "grain-state"}
	defaultRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
	provider := &scriptedDirector{
		grainBin: grainBin, releaseFirst: make(chan struct{}, 1),
	}

	listener, err := net.Listen("tcp4", *addrFlag)
	if err != nil {
		return err
	}
	baseURL := "http://" + listener.Addr().String()
	provider.baseURL = baseURL

	// The run's access code in a file named in this process's environment,
	// as a daemon names its local API token, so the grain mcpserver each
	// run starts presents it (ui.LocalAPITokenFileEnv).
	codeFile := filepath.Join(os.TempDir(), "access-code")
	if err := os.WriteFile(codeFile, []byte(os.Getenv("GRAIN_E2E_ACCESS_CODE")+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing the access code: %w", err)
	}
	os.Setenv(ui.LocalAPITokenFileEnv, codeFile)

	cancellers := orchestrator.NewTaskCancellers()
	handler := ui.NewServer(ui.Config{
		Actor:         model.Principal{Kind: model.PrincipalHuman, ID: "director-e2e-operator"},
		DefaultTarget: &defaultRepo, Capabilities: ui.OfferedCapabilities(),
		Secrets: secretStore, StateRepoRef: func() model.RepoRef { return stateRepo },
		Sandboxes: sandboxHealth{inner: sandboxes}, HostStats: hostPressure,
		SandboxShapeIgnored: true,
		TaskCanceller:       taskCancellerAdapter{cancellers: cancellers},
		// The run's access code (e2e/global-setup.js): every caller,
		// loopback included, shows it, as on every grain daemon.
		LocalAPIToken: os.Getenv("GRAIN_E2E_ACCESS_CODE"),
	}, store)
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/e2e/release-first-run" {
			select {
			case provider.releaseFirst <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/e2e/task-tags" {
			id := r.URL.Query().Get("task")
			t, err := store.GetTask(r.Context(), id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if t == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": t.Tags})
			return
		}
		handler.ServeHTTP(w, r)
	})
	httpServer := &http.Server{Handler: testHandler, ReadHeaderTimeout: 5 * time.Second}

	deps := orchestrator.Deps{
		Store: store, Sandboxes: sandboxes,
		Framework: orchestrator.StaticFramework(provider),
		Config: orchestrator.Config{
			Capabilities:  model.NewCapabilityRegistry(append(admincaps.Providers(), bootstrap.New(), durable.New(), prioritydispatch.New())...),
			GitRemoteBase: gitServer.URL, GrainStateDir: stateDir,
			GrainSecretsDir: secretsDir, StateRepo: func() model.RepoRef { return stateRepo },
			TaskCancellers: cancellers,
		},
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, time.Now().UTC()); err != nil {
		return fmt.Errorf("initial Director cycle: %w", err)
	}

	cyclesDone := make(chan struct{})
	go func() {
		defer close(cyclesDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := orchestrator.RunCycle(ctx, deps, now.UTC()); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "director e2e cycle: %v\n", err)
				}
			}
		}
	}()
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()
	fmt.Printf("grain director e2e serving at %s\n", baseURL)

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
	return nil
}

func seedStateRepository(root string) (string, *httptest.Server, error) {
	gitRoot := filepath.Join(root, "git")
	bare := filepath.Join(gitRoot, "acme", "grain-state.git")
	state := filepath.Join(root, "state-repo")
	if _, err := os.Stat(state); err == nil {
		return state, httptest.NewServer(gitHTTPBackend(gitRoot)), nil
	}
	for _, args := range [][]string{
		{"init", "--bare", bare},
		{"init", "-b", "main", state},
		{"-C", state, "config", "user.name", "Director fixture"},
		{"-C", state, "config", "user.email", "director@example.com"},
	} {
		if err := command(root, "git", args...); err != nil {
			return "", nil, err
		}
	}
	if err := os.MkdirAll(filepath.Join(state, "tables"), 0o755); err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(state, staterepo.SchemaVersionFile),
		[]byte(strconv.Itoa(model.SchemaVersion)+"\n"), 0o644); err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(state, "tables", "grain_config.json"),
		[]byte("{\n  \"status\": \"controller-ready\"\n}\n"), 0o644); err != nil {
		return "", nil, err
	}
	for _, args := range [][]string{
		{"-C", state, "add", "."},
		{"-C", state, "commit", "-m", "Seed deployment state"},
		{"-C", state, "remote", "add", "origin", bare},
		{"-C", state, "push", "origin", "main"},
		{"--git-dir", bare, "symbolic-ref", "HEAD", "refs/heads/main"},
	} {
		if err := command(root, "git", args...); err != nil {
			return "", nil, err
		}
	}
	server := httptest.NewServer(gitHTTPBackend(gitRoot))
	return state, server, nil
}

func command(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

func gitHTTPBackend(projectRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd := exec.Command("git", "http-backend")
		cmd.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+projectRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+r.URL.Path,
			"QUERY_STRING="+r.URL.RawQuery,
			"REQUEST_METHOD="+r.Method,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"),
			"HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
			"GATEWAY_INTERFACE=CGI/1.1",
			"SERVER_PROTOCOL=HTTP/1.1",
		)
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.Output()
		if err != nil {
			http.Error(w, fmt.Sprintf("git http-backend: %v", err), http.StatusInternalServerError)
			return
		}
		separator := []byte("\r\n\r\n")
		headEnd := bytes.Index(out, separator)
		if headEnd == -1 {
			separator = []byte("\n\n")
			headEnd = bytes.Index(out, separator)
		}
		if headEnd == -1 {
			http.Error(w, "git http-backend returned no headers", http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		for _, line := range strings.Split(string(out[:headEnd]), "\n") {
			key, value, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			if strings.EqualFold(key, "Status") {
				if fields := strings.Fields(value); len(fields) > 0 {
					status, _ = strconv.Atoi(fields[0])
				}
				continue
			}
			w.Header().Add(key, value)
		}
		w.WriteHeader(status)
		_, _ = w.Write(out[headEnd+len(separator):])
	}
}
