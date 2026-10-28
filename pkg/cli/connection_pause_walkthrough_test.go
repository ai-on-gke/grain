package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/codex"
	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/secrets"
)

// connectionPauseCodexStub is the provider boundary for the walkthrough
// below: a real `codex` subprocess that refuses one key with Codex's own
// usage-limit wording and answers every other key with a minimal
// successful event stream, logging which key each run spent.
func connectionPauseCodexStub(t *testing.T, exhaustedKey string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := strings.NewReplacer("REVIEWED_VERSION", codex.ReviewedCLIVersion, "EXHAUSTED_KEY", exhaustedKey).Replace(`#!/bin/sh
if [ "$1" = "--version" ]; then
	echo "codex-cli REVIEWED_VERSION"
	exit 0
fi
printf '%s\n' "$OPENAI_API_KEY" >> "$GRAIN_MODEL_DISPATCH_LOG"
if [ "$OPENAI_API_KEY" = "EXHAUSTED_KEY" ]; then
	echo '{"type":"turn.failed","error":{"message":"You have hit your usage limit for this plan. Try again in 2h 0m."}}'
	exit 1
fi
echo '{"type":"thread.started","thread_id":"thread-1"}'
echo '{"type":"item.started","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox","tool":"run_command","arguments":{"command":"true"}}}'
echo '{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox","tool":"run_command","status":"completed","result":"ok"}}'
echo '{"type":"item.completed","item":{"id":"i2","item_type":"agent_message","text":"done"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1}}'
`)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing Codex stub: %v", err)
	}
	return path
}

// docs/test-plans/2026-10-11.md §14 step 1's dispatch half
// (docs/test-runs/2026-10-12-connection-pauses-db-read.md), which grain
// demo cannot show because it dispatches nothing: a real run on the
// deployment's conn-a meets the provider's usage limit, and the pause it
// begins -- through the daemon's own connection pool and RunDispatch,
// nothing begun by hand -- is conn-a's alone. Tasks with no pin rotate
// onto conn-b and conn-c, the second of them saved under another team's
// editor (the connections are the deployment's,
// docs/single-tenant-reset.md stage 1), and their runs succeed; a task
// pinned to conn-borrow, which borrows conn-a's key, waits with it.
func TestWalkthroughAUsageLimitPausesOnlyTheConnectionWhoseKeyMetIt(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	logPath := filepath.Join(t.TempDir(), "dispatches")
	t.Setenv("GRAIN_MODEL_DISPATCH_LOG", logPath)
	now := time.Now().UTC()

	backends := []model.AgentBackend{
		{ID: "conn-a", Name: "Conn A", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.1-codex"}},
		{ID: "conn-borrow", Name: "Borrower", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.1-codex"}, CredentialBackendID: "conn-a"},
		{ID: "conn-b", Name: "Conn B", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.1-codex"}},
		{ID: "conn-c", Name: "Conn C", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.1-codex"}, Team: "beta"},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"conn-a", "conn-b", "conn-c"} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(id), secrets.AgentCredentialKey, []byte("sk-"+id)); err != nil {
			t.Fatal(err)
		}
	}

	pool := daemon.NewAgentPool(daemon.Config{CodexPath: connectionPauseCodexStub(t, "sk-conn-a")}, store, secretStore)
	pause := &orchestrator.Pause{}
	pool.Pause = pause
	cfg := orchestrator.Config{Pause: pause, AgentFramework: model.AgentFrameworkCodex, Now: func() time.Time { return now }}

	file := func(id, backend string) model.Task {
		t.Helper()
		human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
		task := model.Task{
			ID: id, Intent: model.IntentImplement, Title: "Connection pause " + id,
			Origin:   model.Origin{Attribution: human, Reason: model.ReasonDirect},
			Approval: &human, AgentFramework: model.AgentFrameworkCodex, AgentBackendID: backend,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("filing %s: %v", id, err)
		}
		return task
	}
	run := func(task model.Task) (string, error) {
		t.Helper()
		framework, err := pool.Build(ctx, orchestrator.ChoiceFor(task))
		if err != nil {
			t.Fatalf("building %s's framework: %v", task.ID, err)
		}
		d := dispatch.Dispatch{TaskID: task.ID, RunID: dispatch.RunID(task.ID, 1), Attempt: 1}
		if err := store.StartRun(ctx, model.Run{ID: d.RunID, TaskID: task.ID, Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
			t.Fatalf("starting %s: %v", task.ID, err)
		}
		_, runErr := orchestrator.RunDispatch(ctx, store, framework, cfg, task, d, nil, t.TempDir(), "", nil, now)
		return framework.(*daemon.BackendFramework).Id, runErr
	}
	outcome := func(id string) string {
		t.Helper()
		runs, err := store.Runs(ctx, id)
		if err != nil || len(runs) != 1 {
			t.Fatalf("%s runs = %+v (%v), want one", id, runs, err)
		}
		return runs[0].Outcome
	}

	// A run pinned to conn-a meets the limit.
	if conn, err := run(file("t1", "conn-a")); conn != "conn-a" || err == nil {
		t.Fatalf("t1 ran on %q with error %v, want conn-a refused", conn, err)
	}
	if got := outcome("t1"); got != model.PausedOutcome {
		t.Fatalf("t1's run is %q, want %q -- a usage limit is not the task's failure", got, model.PausedOutcome)
	}
	pauses := pause.Pauses(now)
	if len(pauses) != 1 || pauses[0].Connection != "conn-a" {
		t.Fatalf("Pauses = %+v, want conn-a alone", pauses)
	}

	// Tasks with no pin rotate onto the connections with keys of their
	// own, never onto conn-a or the borrower of its key.
	unpinned := orchestrator.AgentChoice{Framework: model.AgentFrameworkCodex}
	if reason := pool.Availability(ctx, unpinned); reason != "" {
		t.Errorf("an unpinned task waits (%q) with conn-b and conn-c free", reason)
	}
	ranOn := map[string]bool{}
	for _, id := range []string{"t2", "t3"} {
		conn, err := run(file(id, ""))
		if err != nil || (conn != "conn-b" && conn != "conn-c") {
			t.Errorf("%s ran on %q (%v), want conn-b or conn-c and success", id, conn, err)
		}
		ranOn[conn] = true
		if got := outcome(id); got != "succeeded" {
			t.Errorf("%s's run is %q, want succeeded", id, got)
		}
	}
	if !ranOn["conn-b"] || !ranOn["conn-c"] {
		t.Errorf("t2 and t3 ran on %v, want one each on conn-b and conn-c", ranOn)
	}
	// A task pinned to the borrower waits on the key it borrows.
	borrower := file("t4", "conn-borrow")
	if reason := pool.Availability(ctx, orchestrator.ChoiceFor(borrower)); !strings.Contains(reason, "paused") {
		t.Errorf("a task pinned to conn-borrow is admitted (%q) while conn-a's key is paused", reason)
	}

	spent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(spent))
	if len(got) != 3 || got[0] != "sk-conn-a" || strings.Contains(strings.Join(got[1:], ","), "sk-conn-a") {
		t.Errorf("keys spent = %v, want sk-conn-a once and then conn-b's and conn-c's", got)
	}

	// Lifting it by its connection, as DELETE /api/pause?connection= does,
	// puts the borrower back in the rotation at once.
	if lifted := pause.LiftWhere(func(p model.UsageLimitPause) bool { return p.Connection == "conn-a" }); len(lifted) != 1 {
		t.Fatalf("lifting conn-a cleared %+v, want its one pause", lifted)
	}
	if reason := pool.Availability(ctx, orchestrator.ChoiceFor(borrower)); reason != "" {
		t.Errorf("conn-borrow still waits (%q) after conn-a's pause was lifted", reason)
	}
}
