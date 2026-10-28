package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// modelDispatchCodexStub is a real subprocess at the framework boundary. It
// records the model, effort and account each dispatch actually received,
// rejects one configured model with Codex's account-incompatibility wording,
// and completes every corrected model with a minimal real Codex event stream.
func modelDispatchCodexStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := strings.ReplaceAll(`#!/bin/sh
if [ "$1" = "--version" ]; then
	echo "codex-cli REVIEWED_VERSION"
	exit 0
fi
model=
while [ "$#" -gt 0 ]; do
	if [ "$1" = "--model" ]; then
		model="$2"
		shift 2
		continue
	fi
	shift
done
effort=$(sed -n 's/^model_reasoning_effort = "\(.*\)"$/\1/p' "$CODEX_HOME/config.toml")
printf 'codex|%s|%s|%s\n' "$model" "$effort" "$OPENAI_API_KEY" >> "$GRAIN_MODEL_DISPATCH_LOG"
if [ "$model" = "gpt-account-incompatible" ]; then
	echo '{"type":"turn.failed","error":{"message":"The configured model is not available with your ChatGPT account"}}'
	exit 1
fi
echo '{"type":"thread.started","thread_id":"thread-1"}'
echo '{"type":"item.started","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox","tool":"run_command","arguments":{"command":"true"}}}'
echo '{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox","tool":"run_command","status":"completed","result":"ok"}}'
echo '{"type":"item.completed","item":{"id":"i2","item_type":"agent_message","text":"done"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1}}'
`, "REVIEWED_VERSION", codex.ReviewedCLIVersion)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing Codex stub: %v", err)
	}
	return path
}

// modelDispatchClaudeStub is the healthy, explicitly configured backend in
// the same flow. Its different framework proves a Codex compatibility pause
// does not stop unrelated dispatch.
func modelDispatchClaudeStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
model=
while [ "$#" -gt 0 ]; do
	if [ "$1" = "--model" ]; then
		model="$2"
		shift 2
		continue
	fi
	shift
done
printf 'claude|%s||%s\n' "$model" "$CLAUDE_CODE_OAUTH_TOKEN" >> "$GRAIN_MODEL_DISPATCH_LOG"
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"run_command","input":{"command":"true"}}]}}'
echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}}'
echo '{"type":"result","result":"done"}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing Claude stub: %v", err)
	}
	return path
}

func putModelDispatchTask(t *testing.T, ctx context.Context, store *model.Store, id, framework string) model.Task {
	t.Helper()
	human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "Exercise model dispatch " + id,
		Origin:   model.Origin{Attribution: human, Reason: model.ReasonDirect},
		Approval: &human, AgentFramework: framework,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing task %s: %v", id, err)
	}
	return task
}

// This is section 2's missing seam in one flow: persisted modular backends
// become concrete subprocess settings, an account/model refusal pauses Codex
// without charging another attempt while a healthy explicit backend runs,
// and editing the rejected backend makes the new effective configuration
// eligible as soon as the operator resumes dispatch.
func TestModelBackendSettingsDriveDispatchAndRecoverFromIncompatibility(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	logPath := filepath.Join(t.TempDir(), "dispatches")
	t.Setenv("GRAIN_MODEL_DISPATCH_LOG", logPath)
	now := time.Now().UTC()

	backends := []model.AgentBackend{
		{
			ID: "codex-preview", Name: "Preview account", Framework: model.AgentFrameworkCodex,
			Models: []string{"gpt-account-incompatible"},
		},
		{
			ID: "claude-stable", Name: "Stable account", Framework: model.AgentFrameworkClaude,
			Models: []string{"claude-sonnet-stable"},
		},
	}
	if err := store.PutConfig(ctx, model.Config{
		AgentBackends: backends,
	}); err != nil {
		t.Fatalf("saving backend settings: %v", err)
	}
	for id, credential := range map[string]string{
		"codex-preview": "sk-preview",
		"claude-stable": "sk-ant-oat01-stable",
	} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(id),
			secrets.AgentCredentialKey, []byte(credential)); err != nil {
			t.Fatalf("saving %s credential: %v", id, err)
		}
	}

	build := daemon.AgentFrameworks(daemon.Config{
		CodexPath:  modelDispatchCodexStub(t),
		ClaudePath: modelDispatchClaudeStub(t),
	}, store, secretStore)
	task := putModelDispatchTask(t, ctx, store, "t1", model.AgentFrameworkCodex)
	first := dispatch.Dispatch{TaskID: task.ID, RunID: dispatch.RunID(task.ID, 1), Attempt: 1}
	if err := store.StartRun(ctx, model.Run{
		ID: first.RunID, TaskID: task.ID, Attempt: first.Attempt, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting first run: %v", err)
	}

	firstFramework, err := build(ctx, orchestrator.AgentChoice{Framework: task.AgentFramework, BackendID: task.AgentBackendID})
	if err != nil {
		t.Fatalf("building first backend: %v", err)
	}
	if got := firstFramework.(*daemon.BackendFramework).Id; got != "codex-preview" {
		t.Fatalf("first backend = %q, want codex-preview", got)
	}
	pause := &orchestrator.Pause{}
	_, runErr := orchestrator.RunDispatch(ctx, store, firstFramework, orchestrator.Config{
		Pause: pause, AgentFramework: model.AgentFrameworkCodex, Now: func() time.Time { return now },
	}, task, first, nil, t.TempDir(), "", nil, now)
	incompat, ok := agent.ModelIncompatibility(runErr)
	if !ok {
		t.Fatalf("first dispatch error = %v, want a model incompatibility", runErr)
	}
	if incompat.Framework != model.AgentFrameworkCodex {
		t.Fatalf("incompatibility framework = %q, want Codex", incompat.Framework)
	}

	runs, err := store.Runs(ctx, task.ID)
	if err != nil || len(runs) != 1 || runs[0].Outcome != model.PausedOutcome {
		t.Fatalf("runs after incompatibility = %+v (%v), want one paused run", runs, err)
	}
	streak, err := store.FailureStreak(ctx, task.ID)
	if err != nil || streak == nil || streak.Count != 0 {
		t.Fatalf("failure streak after incompatibility = %+v (%v), want no charge to the task's retry budget", streak, err)
	}
	// The pause is keyed by the connection whose credential was refused
	// and the model it refused, and covers nothing else: not the
	// connection's other models, nor another connection.
	codexPaused := pause.ModelPaused("codex-preview", "gpt-account-incompatible", model.DefaultTeamID, now)
	codexWhole := pause.ConnectionPaused("codex-preview", model.DefaultTeamID, now)
	claudePaused := pause.ModelPaused("claude-stable", "claude-sonnet-4-5", model.DefaultTeamID, now)
	if !codexPaused || codexWhole || claudePaused {
		t.Fatalf("pause coverage = codex-preview's model %t, codex-preview as a whole %t, claude-stable %t; want only the incompatible model",
			codexPaused, codexWhole, claudePaused)
	}
	// Which connection and model a task lands on is the pool's to say;
	// here each framework has exactly one of each.
	connectionFor := map[string]string{
		model.AgentFrameworkCodex: "codex-preview", model.AgentFrameworkClaude: "claude-stable",
	}
	modelFor := map[string]string{
		model.AgentFrameworkCodex: "gpt-account-incompatible", model.AgentFrameworkClaude: "claude-sonnet-4-5",
	}

	// Run a real scheduler pass with both tasks ready. It must leave the
	// paused Codex task at one attempt and spend the worker on the healthy
	// explicitly configured Claude backend.
	healthyTask := putModelDispatchTask(t, ctx, store, "t2", model.AgentFrameworkClaude)
	next, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(time.Second),
		dispatch.PausedFor(func(taskID string) bool {
			candidate, readErr := store.GetTask(ctx, taskID)
			if readErr != nil || candidate == nil {
				return true
			}
			return pause.ModelPaused(connectionFor[candidate.AgentFramework], modelFor[candidate.AgentFramework], model.DefaultTeamID, now)
		}))
	if err != nil {
		t.Fatalf("dispatching around the paused backend: %v", err)
	}
	if len(next) != 1 || next[0].TaskID != healthyTask.ID || next[0].Attempt != 1 {
		t.Fatalf("dispatch while Codex is paused = %+v, want Claude task t2 attempt 1", next)
	}
	if runs, err := store.Runs(ctx, task.ID); err != nil || len(runs) != 1 {
		t.Fatalf("paused task spent another attempt: %+v (%v)", runs, err)
	}

	healthy, err := build(ctx, orchestrator.AgentChoice{Framework: healthyTask.AgentFramework, BackendID: healthyTask.AgentBackendID})
	if err != nil {
		t.Fatalf("building healthy backend: %v", err)
	}
	if got := healthy.(*daemon.BackendFramework).Id; got != "claude-stable" {
		t.Fatalf("healthy backend = %q, want claude-stable", got)
	}
	if _, err := orchestrator.RunDispatch(ctx, store, healthy, orchestrator.Config{
		Pause: pause, AgentFramework: model.AgentFrameworkCodex,
		Now: func() time.Time { return now.Add(2 * time.Second) },
	}, healthyTask, next[0], nil, t.TempDir(), "", nil, now.Add(time.Second)); err != nil {
		t.Fatalf("healthy backend dispatch while Codex is paused: %v", err)
	}
	healthyRuns, err := store.Runs(ctx, healthyTask.ID)
	if err != nil || len(healthyRuns) != 1 || healthyRuns[0].Outcome != "succeeded" {
		t.Fatalf("healthy backend runs = %+v (%v), want a successful actual dispatch", healthyRuns, err)
	}
	healthyTask.DeferredAt = &now
	if err := store.PutTask(ctx, healthyTask); err != nil {
		t.Fatalf("deferring completed control task: %v", err)
	}

	// Correct the rejected backend under its stable ID. The pool must key
	// unavailability by this effective configuration, or this edit would
	// remain hidden behind the old model's 15-minute window even after an
	// operator explicitly resumed dispatch.
	backends[0].Models = []string{"gpt-preview-fixed"}
	if err := store.PutConfig(ctx, model.Config{
		AgentBackends: backends,
	}); err != nil {
		t.Fatalf("correcting backend settings: %v", err)
	}
	reloaded, err := store.GetConfig(ctx)
	if err != nil || reloaded == nil ||
		len(reloaded.AgentBackends[0].Models) != 1 || reloaded.AgentBackends[0].Models[0] != "gpt-preview-fixed" {
		t.Fatalf("reloaded corrected settings = %+v (%v)", reloaded, err)
	}
	if !pause.Lift() {
		t.Fatal("corrected backend left no incompatibility pause to resume")
	}

	resumed, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("dispatching corrected settings: %v", err)
	}
	if len(resumed) != 1 || resumed[0].TaskID != task.ID || resumed[0].Attempt != 2 {
		t.Fatalf("resumed dispatch = %+v, want t1 attempt 2", resumed)
	}
	corrected, err := build(ctx, orchestrator.AgentChoice{Framework: task.AgentFramework, BackendID: task.AgentBackendID})
	if err != nil {
		t.Fatalf("building corrected backend: %v", err)
	}
	if got := corrected.(*daemon.BackendFramework).Id; got != "codex-preview" {
		t.Fatalf("backend after correction = %q, want corrected codex-preview", got)
	}
	if _, err := orchestrator.RunDispatch(ctx, store, corrected, orchestrator.Config{
		Pause: pause, AgentFramework: model.AgentFrameworkCodex,
		Now: func() time.Time { return now.Add(4 * time.Second) },
	}, task, resumed[0], nil, t.TempDir(), "", nil, now.Add(3*time.Second)); err != nil {
		t.Fatalf("resumed dispatch: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading dispatched configurations: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{
		// A connection carries no effort of its own: none is passed.
		"codex|gpt-account-incompatible||sk-preview",
		"claude|claude-sonnet-stable||sk-ant-oat01-stable",
		"codex|gpt-preview-fixed||sk-preview",
	}
	if len(lines) != len(want) {
		t.Fatalf("dispatched configurations = %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("dispatch %d configuration = %q, want %q", i+1, lines[i], want[i])
		}
	}
	runs, err = store.Runs(ctx, task.ID)
	if err != nil || len(runs) != 2 || runs[1].Outcome != "succeeded" {
		t.Fatalf("runs after correction = %+v (%v), want the paused task resumed successfully", runs, err)
	}
}

func TestConnectionsLoadBalanceAcrossSupportedModels(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)

	backends := []model.AgentBackend{
		{
			ID:        "conn-codex-primary",
			Name:      "Codex Primary",
			Framework: model.AgentFrameworkCodex,
			Models:    []string{"gpt-5.1-codex", "gpt-5.6-sol"},
			AuthMode:  "api_key",
		},
		{
			ID:        "conn-codex-secondary",
			Name:      "Codex Secondary",
			Framework: model.AgentFrameworkCodex,
			Models:    []string{"gpt-5.1-codex"},
			AuthMode:  "api_key",
		},
		{
			ID:        "conn-claude-fast",
			Name:      "Claude Fast",
			Framework: model.AgentFrameworkClaude,
			Models:    []string{"claude-sonnet-5"},
			AuthMode:  "api_key",
		},
	}
	for id, credential := range map[string]string{
		"conn-codex-primary":   "sk-codex-1",
		"conn-codex-secondary": "sk-codex-2",
		"conn-claude-fast":     "sk-ant-1",
	} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(id),
			secrets.AgentCredentialKey, []byte(credential)); err != nil {
			t.Fatalf("saving %s credential: %v", id, err)
		}
	}

	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatalf("saving config: %v", err)
	}
	// The default model is the team's own: a deployment-wide one is
	// never read for a team's tasks (grain/task-1601).
	if err := store.UpdateConfig(ctx, func(c *model.Config) error { c.DefaultModel = "gpt-5.1-codex"; return nil }); err != nil {
		t.Fatalf("saving the default team: %v", err)
	}

	cfg := daemon.Config{
		CodexPath:  modelDispatchCodexStub(t),
		ClaudePath: modelDispatchClaudeStub(t),
	}
	build := daemon.AgentFrameworks(cfg, store, secretStore)

	// 1. Default model dispatch (gpt-5.1-codex) should load balance between conn-codex-primary and conn-codex-secondary.
	f1, err := build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("build 1: %v", err)
	}
	if got := f1.(*daemon.BackendFramework).Id; got != "conn-codex-primary" {
		t.Fatalf("build 1 backend = %q, want conn-codex-primary", got)
	}

	f2, err := build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("build 2: %v", err)
	}
	if got := f2.(*daemon.BackendFramework).Id; got != "conn-codex-secondary" {
		t.Fatalf("build 2 backend = %q, want conn-codex-secondary", got)
	}

	f3, err := build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("build 3: %v", err)
	}
	if got := f3.(*daemon.BackendFramework).Id; got != "conn-codex-primary" {
		t.Fatalf("build 3 backend = %q, want conn-codex-primary", got)
	}

	// 2. Specific model choice "gpt-5.6-sol" only supported by conn-codex-primary.
	fSol, err := build(ctx, orchestrator.AgentChoice{BackendID: "gpt-5.6-sol"})
	if err != nil {
		t.Fatalf("build gpt-5.6-sol: %v", err)
	}
	if got := fSol.(*daemon.BackendFramework).Id; got != "conn-codex-primary" {
		t.Fatalf("build gpt-5.6-sol backend = %q, want conn-codex-primary", got)
	}

	// 3. Specific model choice "claude-sonnet-5" only supported by conn-claude-fast.
	fClaude, err := build(ctx, orchestrator.AgentChoice{BackendID: "claude-sonnet-5"})
	if err != nil {
		t.Fatalf("build claude-sonnet-5: %v", err)
	}
	if got := fClaude.(*daemon.BackendFramework).Id; got != "conn-claude-fast" {
		t.Fatalf("build claude-sonnet-5 backend = %q, want conn-claude-fast", got)
	}

	// 4. Pinning by connection ID.
	fPinned, err := build(ctx, orchestrator.AgentChoice{BackendID: "conn-codex-secondary"})
	if err != nil {
		t.Fatalf("build pinned: %v", err)
	}
	if got := fPinned.(*daemon.BackendFramework).Id; got != "conn-codex-secondary" {
		t.Fatalf("build pinned backend = %q, want conn-codex-secondary", got)
	}
}

// Every task runs on the deployment's connections and its default model
// (docs/single-tenant-reset.md, stage 1), whichever team it carries and
// whichever team's editor saved the connection: the default model is the
// default team's row's, and no task is refused a connection for being
// saved under another team. A default model nothing serves is still
// refused rather than run on something else.
func TestModelBackendDispatchRunsEveryTaskOnTheDeploymentsConnections(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)

	backends := []model.AgentBackend{
		{
			ID:        "conn-saved-default",
			Name:      "Saved under the default team",
			Framework: model.AgentFrameworkCodex,
			AuthMode:  "api_key",
			Models:    []string{"gpt-5.1-codex"},
			Team:      "default",
		},
		{
			ID:        "conn-saved-infra",
			Name:      "Saved under infra",
			Framework: model.AgentFrameworkCodex,
			AuthMode:  "api_key",
			Models:    []string{"gpt-5.1-codex"},
			Team:      "infra",
		},
	}

	for id, credential := range map[string]string{
		"conn-saved-default": "sk-codex-1",
		"conn-saved-infra":   "sk-codex-2",
	} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(id),
			secrets.AgentCredentialKey, []byte(credential)); err != nil {
			t.Fatalf("saving %s credential: %v", id, err)
		}
	}

	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatalf("saving config: %v", err)
	}
	// The deployment's default model is stored on the default team's row
	// until the team table goes.
	if err := store.UpdateConfig(ctx, func(c *model.Config) error { c.DefaultModel = "gpt-5.1-codex"; return nil }); err != nil {
		t.Fatalf("saving the default team: %v", err)
	}
	// Another team's own model choice is never read.

	cfg := daemon.Config{
		CodexPath: modelDispatchCodexStub(t),
	}
	build := daemon.AgentFrameworks(cfg, store, secretStore)

	used := map[string]int{}
	for _, team := range []string{"default", "", "infra", "unassigned-team"} {
		for i := 0; i < 2; i++ {
			f, err := build(ctx, orchestrator.AgentChoice{})
			if err != nil {
				t.Fatalf("build for team %q: %v", team, err)
			}
			used[f.(*daemon.BackendFramework).Id]++
		}
	}
	if used["conn-saved-default"] == 0 || used["conn-saved-infra"] == 0 || len(used) != 2 {
		t.Fatalf("eight builds across four teams used %v, want both of the deployment's connections and nothing else", used)
	}

	// With only the connection saved under infra, every task runs on it:
	// there is no team for it to belong to any more. Use a fresh pool so the
	// unstarted leases from the eight builds above (4 per connection,
	// filling the default per-connection concurrency limit of 4) do not hold
	// conn-saved-infra at its concurrency ceiling.
	build = daemon.AgentFrameworks(cfg, store, secretStore)
	if err := store.PutConfig(ctx, model.Config{
		AgentBackends: []model.AgentBackend{backends[1]},
	}); err != nil {
		t.Fatalf("saving config: %v", err)
	}
	for _, team := range []string{"default", "", "infra"} {
		f, err := build(ctx, orchestrator.AgentChoice{})
		if err != nil {
			t.Fatalf("build for team %q with only conn-saved-infra: %v", team, err)
		}
		if got := f.(*daemon.BackendFramework).Id; got != "conn-saved-infra" {
			t.Fatalf("build for team %q = %q, want conn-saved-infra", team, got)
		}
	}

	// A default model nothing serves is refused, not run on the
	// connection that serves something else.
	if err := store.UpdateConfig(ctx, func(team *model.Config) error {
		team.DefaultModel = "gpt-5.6-unserved"
		return nil
	}); err != nil {
		t.Fatalf("changing the deployment's default model: %v", err)
	}
	_, err := build(ctx, orchestrator.AgentChoice{})
	if err == nil || !strings.Contains(err.Error(), "gpt-5.6-unserved") {
		t.Fatalf("build with a default model nothing serves = %v, want a refusal naming gpt-5.6-unserved", err)
	}
}
