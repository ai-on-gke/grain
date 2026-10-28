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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// scriptedSession is the session's agent: it answers in plain text and
// keeps every prompt it was given.
type scriptedSession struct {
	mu      sync.Mutex
	prompts []string
}

func (s *scriptedSession) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, cfg.Prompt)
	n := len(s.prompts)
	s.mu.Unlock()
	answer := fmt.Sprintf("Scripted answer %d.", n)
	return &agent.Result{FinalText: answer, Transcript: answer}, nil
}

func (s *scriptedSession) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.MkdirTemp("", "grain-console-rest-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

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
	cfg.MaxWorkers = 1
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
	provider := &scriptedSession{}
	handler := ui.NewServer(ui.Config{
		Actor:         model.Principal{Kind: model.PrincipalHuman, ID: "console-rest-e2e-operator"},
		DefaultTarget: &defaultRepo, Capabilities: ui.OfferedCapabilities(),
		SandboxShapeIgnored: true,
		// The run's access code (e2e/global-setup.js): every caller,
		// loopback included, shows it, as on every grain daemon.
		LocalAPIToken: os.Getenv("GRAIN_E2E_ACCESS_CODE"),
	}, store)
	deps := orchestrator.Deps{
		Store: store, Sandboxes: orchestrator.NewHostSandboxes(sandboxRoot),
		Framework: orchestrator.StaticFramework(provider),
		Config: orchestrator.Config{
			Capabilities: model.NewCapabilityRegistry(durable.New()),
		},
		MaxWorkers: 1,
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /e2e/prompts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"prompts": provider.snapshot()})
	})
	mux.HandleFunc("POST /e2e/end-exchange", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("task")
		if id == "" {
			http.Error(w, "task is required", http.StatusBadRequest)
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
	return nil
}
