package orchestrator

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestNoDispatchCodeAsksIsConsoleSession enforces DT-7: Task.IsConsoleSession
// is a display predicate only, and no dispatch, liveness, preemption, pause,
// base-repair or merge-queue code asks it.
func TestNoDispatchCodeAsksIsConsoleSession(t *testing.T) {
	t.Parallel()
	dirs := []string{"../dispatch", "../orchestrator", "../daemon", "../model"}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			for _, decl := range file.Decls {
				fn, isFn := decl.(*ast.FuncDecl)
				if isFn && ((filepath.ToSlash(path) == "../model/console_session.go" && fn.Name.Name == "IsConsoleSession") ||
					(filepath.ToSlash(path) == "../model/task_kinds.go" && fn.Name.Name == "TaskKindTags")) {
					continue
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					ident, ok := n.(*ast.Ident)
					if ok && ident.Name == "IsConsoleSession" {
						pos := fset.Position(ident.Pos())
						t.Errorf("%s:%d references IsConsoleSession; dispatch, liveness and merge-queue code must ask HasPriorityDispatch or the task's goal instead",
							filepath.ToSlash(path), pos.Line)
					}
					return true
				})
			}
		}
	}
}

type internalAgentFunc func(context.Context, agent.RunConfig) (*agent.Result, error)

func (f internalAgentFunc) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f(ctx, cfg)
}

func replyToSessionInternal(t *testing.T, store *model.Store, taskID string, now time.Time) {
	t.Helper()
	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return now }
	if err := client.AddComment(t.Context(), taskID, "what is this deployment doing?", nil); err != nil {
		t.Fatalf("replying to session: %v", err)
	}
}

// TestConsoleSessionHasNo8hDeadline verifies that reconcileConsoleSessions
// files and back-fills every console session with the durable grant and the
// person-closes goal, and that its runs have no 8h deadline and no wait-credit
// cap.
func TestConsoleSessionHasNo8hDeadline(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	deps := Deps{Store: store}
	if err := reconcileConsoleSessions(ctx, deps, standingNow); err != nil {
		t.Fatalf("reconcileConsoleSessions: %v", err)
	}
	id, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil || id == "" {
		t.Fatalf("finding director session task: id=%q err=%v", id, err)
	}
	director, err := store.GetTask(ctx, id)
	if err != nil || director == nil {
		t.Fatalf("GetTask(%s): %v", id, err)
	}
	if !director.IsDurable() {
		t.Fatalf("director session task grants = %+v, want %q", director.Grants, model.DurableCapability)
	}
	goal, err := store.TaskGoal(ctx, id)
	if err != nil || goal == nil || goal.Kind != model.GoalPersonCloses {
		t.Fatalf("director session task goal = %+v (%v), want %q", goal, err, model.GoalPersonCloses)
	}

	// interactiveDispatch sets MaxRunRuntime to DefaultMaxInteractiveRuntime (8h),
	// and durableDispatch zeroes both maxRunRuntime and runWaitCreditCap.
	cfg := durableDispatch(*director, interactiveDispatch(*director, Config{MaxRunRuntime: 2 * time.Hour}))
	if cfg.maxRunRuntime() != 0 || cfg.runWaitCreditCap() != 0 {
		t.Fatalf("console session dispatch: maxRunRuntime = %v, runWaitCreditCap = %v; want 0 and 0 (no 8h deadline)",
			cfg.maxRunRuntime(), cfg.runWaitCreditCap())
	}

	// Back-fill check: a legacy console session task with neither durable nor a
	// goal gains both on the next reconcileConsoleSessions pass.
	legacyCreated := standingNow.Add(-time.Hour)
	if err := store.PutTask(ctx, model.Task{
		ID:          id,
		Intent:      model.IntentAnalyze,
		Title:       director.Title,
		Body:        director.Body,
		Origin:      director.Origin,
		Tags:        director.Tags,
		Grants:      []model.Grant{{Capability: "self-debug", Via: model.GrantByGrain}},
		Interactive: true,
		CreatedAt:   &legacyCreated,
		Approval:    director.Approval,
		ApprovedAt:  director.ApprovedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearTaskGoal(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := reconcileConsoleSessions(ctx, deps, standingNow.Add(time.Minute)); err != nil {
		t.Fatalf("reconcileConsoleSessions back-fill: %v", err)
	}
	backfilled, err := store.GetTask(ctx, id)
	if err != nil || backfilled == nil || !backfilled.IsDurable() {
		t.Fatalf("back-filled session task = %+v (%v), want durable grant", backfilled, err)
	}
	backfilledGoal, err := store.TaskGoal(ctx, id)
	if err != nil || backfilledGoal == nil || backfilledGoal.Kind != model.GoalPersonCloses {
		t.Fatalf("back-filled session goal = %+v (%v), want %q", backfilledGoal, err, model.GoalPersonCloses)
	}
}

// TestConsoleSessionRestartMidExchangeResumesSameConversation verifies that a
// daemon restart mid-exchange suspends the session run with SuspendRestart and
// resumes the same conversation on the next cycle rather than parking it on a
// failure note.
func TestConsoleSessionRestartMidExchangeResumesSameConversation(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	cfg := model.DefaultConfig()
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID:            model.DirectorSessionID,
		Title:         "Director",
		Capabilities:  []string{},
		NetworkAccess: model.NetworkOpen,
	}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := reconcileConsoleSessions(ctx, Deps{Store: store}, standingNow); err != nil {
		t.Fatal(err)
	}
	id, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil || id == "" {
		t.Fatalf("director task id = %q, err = %v", id, err)
	}

	said := standingNow.Add(time.Minute)
	replyToSessionInternal(t, store, id, said)

	started := said.Add(time.Second)
	run1 := id + "-1"
	if err := store.StartRun(ctx, model.Run{ID: run1, TaskID: id, Attempt: 1, StartedAt: started}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	comments, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, id, comments[len(comments)-1].ID, started); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSuspendedSession(ctx, id, &model.SuspendedSession{
		Framework: model.AgentFrameworkClaude, Token: "sess-restart-1", RunID: run1, At: started,
	}, started); err != nil {
		t.Fatal(err)
	}
	restarted := started.Add(30 * time.Second)
	if err := store.Suspend(ctx, model.Suspension{
		RunID:     run1,
		TaskID:    id,
		Kind:      model.SuspendRestart,
		CreatedAt: restarted,
		Detail:    "daemon restarted",
	}); err != nil {
		t.Fatal(err)
	}

	var resumed string
	fw := StaticFramework(internalAgentFunc(func(_ context.Context, runCfg agent.RunConfig) (*agent.Result, error) {
		resumed = runCfg.ResumeSession
		return &agent.Result{
			FinalText:        "Resumed and answered.",
			ResumableSession: "sess-restart-1",
		}, nil
	}))
	deps := Deps{
		Store:      store,
		Sandboxes:  NewHostSandboxes(t.TempDir()),
		Framework:  fw,
		MaxWorkers: 1,
		Config: Config{
			AgentFramework: model.AgentFrameworkClaude,
			Capabilities:   model.NewCapabilityRegistry(durable.New()),
		},
	}
	if err := RunCycle(ctx, deps, restarted.Add(time.Second)); err != nil {
		t.Fatalf("RunCycle after restart: %v", err)
	}
	if resumed != "sess-restart-1" {
		t.Fatalf("resumed session = %q, want sess-restart-1", resumed)
	}
}

// TestConsoleSessionAddingCapabilityMidConversationRestartsAndResumes verifies
// that adding a capability to a console session mid-conversation cancels the
// live run with errCapabilityChanged (SuspendCapability) and automatically
// resumes the same conversation with the new capability.
func TestConsoleSessionAddingCapabilityMidConversationRestartsAndResumes(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	cfg := model.DefaultConfig()
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID:            model.DirectorSessionID,
		Title:         "Director",
		Capabilities:  []string{},
		NetworkAccess: model.NetworkOpen,
	}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := reconcileConsoleSessions(ctx, Deps{Store: store}, standingNow); err != nil {
		t.Fatal(err)
	}
	id, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil || id == "" {
		t.Fatalf("director task id = %q, err = %v", id, err)
	}

	said := standingNow.Add(time.Minute)
	replyToSessionInternal(t, store, id, said)

	started := said.Add(time.Second)
	run1 := id + "-1"
	if err := store.StartRun(ctx, model.Run{ID: run1, TaskID: id, Attempt: 1, StartedAt: started}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	comments, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, id, comments[len(comments)-1].ID, started); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSuspendedSession(ctx, id, &model.SuspendedSession{
		Framework: model.AgentFrameworkClaude, Token: "sess-cap-1", RunID: run1, At: started,
	}, started); err != nil {
		t.Fatal(err)
	}

	cancellers := NewTaskCancellers()
	runCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	unreg := cancellers.register(id, cancel)
	restarts := NewCapabilityRestarts()
	deps := Deps{
		Store:     store,
		Sandboxes: NewHostSandboxes(t.TempDir()),
		Config: Config{
			AgentFramework:     model.AgentFrameworkClaude,
			TaskCancellers:     cancellers,
			CapabilityRestarts: restarts,
			Capabilities:       model.NewCapabilityRegistry(durable.New(), selfdebug.New()),
		},
	}

	// Add self-debug capability while the conversation run is live.
	cfg.ConsoleSessions[0].Capabilities = []string{"self-debug"}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	changedAt := started.Add(10 * time.Second)
	if err := reconcileConsoleSessions(ctx, deps, changedAt); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCapabilityChanges(ctx, deps, changedAt); err != nil {
		t.Fatal(err)
	}
	if runCtx.Err() != nil {
		t.Fatalf("cancelled before grace elapsed: %v", context.Cause(runCtx))
	}
	afterGrace := changedAt.Add(capabilityRestartGrace)
	if err := reconcileCapabilityChanges(ctx, deps, afterGrace); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(context.Cause(runCtx), errCapabilityChanged) {
		t.Fatalf("cancel cause = %v, want errCapabilityChanged", context.Cause(runCtx))
	}
	unreg()

	task, err := store.GetTask(ctx, id)
	if err != nil || task == nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	end := classifyRunEnd(ctx, store, deps.Config, *task, run1, nil, context.Canceled, context.Cause(runCtx),
		0, PauseScope{}, started)
	if end.suspend != model.SuspendCapability {
		t.Fatalf("classifyRunEnd suspend = %q, want %q", end.suspend, model.SuspendCapability)
	}
	deps.Config.Now = func() time.Time { return afterGrace }
	if err := recordRunEnd(ctx, ctx, store, deps.Config, *task, run1, obs, nil, end, nil, true, "", ""); err != nil {
		t.Fatal(err)
	}

	var resumed string
	deps.Framework = StaticFramework(internalAgentFunc(func(_ context.Context, runCfg agent.RunConfig) (*agent.Result, error) {
		resumed = runCfg.ResumeSession
		return &agent.Result{
			FinalText:        "Resumed with self-debug.",
			ResumableSession: "sess-cap-1",
		}, nil
	}))
	deps.MaxWorkers = 1
	if err := RunCycle(ctx, deps, afterGrace.Add(time.Second)); err != nil {
		t.Fatalf("RunCycle after capability restart: %v", err)
	}
	if resumed != "sess-cap-1" {
		t.Fatalf("resumed session = %q, want sess-cap-1", resumed)
	}
	updated, err := store.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(updated.Grants, func(g model.Grant) bool { return g.Capability == "self-debug" }) {
		t.Fatalf("updated grants = %+v, want self-debug included", updated.Grants)
	}
}

type connTestFramework struct {
	run        func(context.Context, agent.RunConfig) (*agent.Result, error)
	credential string
	team       string
}

func (f connTestFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f.run(ctx, cfg)
}

func (f connTestFramework) CredentialConnection() (string, string) {
	return f.credential, f.team
}

type failoverPool struct {
	pause *Pause
	now   func() time.Time
	mu    sync.Mutex
	calls []string
}

func (p *failoverPool) ForTask(_ context.Context, _ AgentChoice) (agent.Framework, error) {
	if !p.pause.ConnectionPaused("conn-a", model.DefaultTeamID, p.now()) {
		return connTestFramework{
			credential: "conn-a",
			team:       model.DefaultTeamID,
			run: func(context.Context, agent.RunConfig) (*agent.Result, error) {
				p.mu.Lock()
				p.calls = append(p.calls, "conn-a")
				p.mu.Unlock()
				return &agent.Result{ResumableSession: "sess-quota-1"}, &agent.UsageLimitError{
					Framework: "claude",
					ResetAt:   p.now().Add(time.Hour),
				}
			},
		}, nil
	}
	return connTestFramework{
		credential: "conn-b",
		team:       model.DefaultTeamID,
		run: func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			p.mu.Lock()
			p.calls = append(p.calls, "conn-b:"+cfg.ResumeSession)
			p.mu.Unlock()
			return &agent.Result{
				FinalText:        "Answered on conn-b.",
				ResumableSession: cfg.ResumeSession,
			}, nil
		},
	}, nil
}

// TestConsoleSessionUsageLimitResumesOnAnotherConnection verifies that a usage
// limit mid-conversation pauses the exhausted connection and automatically
// resumes the session on another connection on the next cycle.
func TestConsoleSessionUsageLimitResumesOnAnotherConnection(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	cfg := model.DefaultConfig()
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID:            model.DirectorSessionID,
		Title:         "Director",
		Capabilities:  []string{},
		NetworkAccess: model.NetworkOpen,
	}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := reconcileConsoleSessions(ctx, Deps{Store: store}, standingNow); err != nil {
		t.Fatal(err)
	}
	id, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil || id == "" {
		t.Fatalf("director task id = %q, err = %v", id, err)
	}

	said := standingNow.Add(time.Minute)
	replyToSessionInternal(t, store, id, said)

	now := said.Add(time.Second)
	pause := &Pause{}
	pool := &failoverPool{pause: pause, now: func() time.Time { return now }}
	deps := Deps{
		Store:      store,
		Sandboxes:  NewHostSandboxes(t.TempDir()),
		Framework:  pool.ForTask,
		MaxWorkers: 1,
		Config: Config{
			AgentFramework: model.AgentFrameworkClaude,
			Pause:          pause,
			Now:            func() time.Time { return now },
			Capabilities:   model.NewCapabilityRegistry(durable.New()),
		},
	}

	// Cycle 1 hits usage limit on conn-a.
	_ = RunCycle(ctx, deps, now)
	if !pause.ConnectionPaused("conn-a", model.DefaultTeamID, now) {
		t.Fatal("conn-a should be paused after hitting usage limit")
	}

	// Cycle 2 fails over to conn-b and resumes the same conversation.
	now = now.Add(time.Second)
	if err := RunCycle(ctx, deps, now); err != nil {
		t.Fatalf("RunCycle failover: %v", err)
	}
	if !slices.Equal(pool.calls, []string{"conn-a", "conn-b:sess-quota-1"}) {
		t.Fatalf("pool calls = %v, want [conn-a conn-b:sess-quota-1]", pool.calls)
	}
}
