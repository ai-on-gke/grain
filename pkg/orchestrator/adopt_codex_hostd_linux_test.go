//go:build linux

package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// TestResumeAdoptedRunKeepsItsEarlierAttempts' shape, for a codex run: a
// `codex exec --json` stream detached in grain hostd across an upgrade's
// flush, adopted by the next daemon from the persisted cursor, and read
// by codex's own parser (codex.Framework.ParseResult) rather than a fake
// -- so the run is finished by what it did, its tool call included, and
// not as interrupted, which is what an adopted codex run came to while
// codex could not read one (docs/non-disruptive-upgrade.md, "Which
// substrate carries which CLI").
//
// Not parallel: hostd binds its socket under a process-wide umask
// (hostd.Server), which a second hostd test starting beside this one
// would create its own socket and files under.
func TestResumeAdoptedHostdCodexRun(t *testing.T) {
	client := serveShHostd(t)
	store, ctx := openStore(t)
	_, gh := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t", model.RepoRef{Owner: "acme", Name: "widgets"})
	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t-1", Attempt: 1}
	startRun(t, ctx, store, d, time.Now().UTC().Add(-time.Minute))
	sandboxDir := t.TempDir()
	if _, err := orchestrator.NewHostSandboxes(sandboxDir).Acquire(ctx, d.RunID, orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	first := `{"type":"thread.started","thread_id":"th-1"}` + "\n" + `{"type":"turn.started"}` + "\n"
	rest := []string{
		`{"type":"item.started","item":{"id":"i1","type":"mcp_tool_call","server":"grain-sandbox","tool":"comment_on_issue","arguments":{"comment":"done"}}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"mcp_tool_call","server":"grain-sandbox","tool":"comment_on_issue","status":"completed","result":"Recorded"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"agent_message","text":"commented"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1}}`,
	}
	script := "printf '%s\\n' '" + strings.Join(strings.Split(strings.TrimSpace(first), "\n"), "' '") + "'; sleep 2; printf '%s\\n' '" +
		strings.Join(rest, "' '") + "'"

	// The previous daemon: codex writes its first lines, an upgrade
	// flushes the run, and codex goes on in hostd to finish its turn.
	previous := &runhost.Hostd{Client: client, CursorEvery: time.Millisecond,
		OnCursor: func(ctx context.Context, run string, n int64) {
			_ = store.SetHostdCursor(ctx, run, n, time.Now().UTC())
		}}
	var tee syncBuf
	runCtx := runhost.WithRun(ctx, runhost.Run{Task: task.ID, Run: d.RunID, Deadline: time.Now().Add(time.Hour)})
	detached := make(chan error, 1)
	go func() {
		_, err := previous.Host(agent.LocalHost{}, "sh").Run(runCtx, agent.Command{Args: []string{"-c", script}}, &tee)
		detached <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); tee.String() != first; {
		if time.Now().After(deadline) {
			t.Fatalf("codex never wrote its first lines: %q", tee.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := previous.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-detached; !errors.Is(err, agent.ErrDetached) {
		t.Fatalf("Run after the flush = %v, want ErrDetached", err)
	}
	transcripts := t.TempDir()
	if err := os.WriteFile(filepath.Join(transcripts, d.RunID), []byte(tee.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// The next daemon adopts it, and codex's own parser reads it.
	sandboxes := orchestrator.NewHostSandboxes(sandboxDir)
	var adopted []orchestrator.AdoptedRun
	if _, err := orchestrator.RecoverHostd(ctx, store, gh, client, "test", time.Now().UTC(),
		func(a runhost.Adoption, c int64) bool {
			sb, err := orchestrator.ReadoptSandbox(ctx, sandboxes, a.Claim.Run)
			if err != nil {
				t.Errorf("readopting the sandbox: %v", err)
				return false
			}
			adopted = append(adopted, orchestrator.AdoptedRun{Adoption: a, Cursor: c, Sandbox: sb})
			return true
		}); err != nil {
		t.Fatal(err)
	}
	if len(adopted) != 1 || adopted[0].Cursor != int64(len(first)) {
		t.Fatalf("adopted %+v, want run t-1 at cursor %d", adopted, len(first))
	}
	deps := orchestrator.Deps{Store: store, Client: gh, Sandboxes: sandboxes,
		Framework: orchestrator.StaticFramework(codex.New("codex", "grain")),
		Config:    orchestrator.Config{TranscriptDir: transcripts}}
	next := &runhost.Hostd{Client: client, CursorEvery: time.Millisecond}
	if err := orchestrator.ResumeAdoptedRun(ctx, deps, next, adopted[0]); err != nil {
		t.Fatalf("ResumeAdoptedRun: %v", err)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil || len(runs) != 1 || runs[0].FinishedAt == nil {
		t.Fatalf("runs = %+v (%v), want the one run finished", runs, err)
	}
	if runs[0].Outcome == model.InterruptedOutcome || runs[0].Outcome == "failed" {
		t.Fatalf("outcome = %q (%s), want the run's own ending", runs[0].Outcome, runs[0].Detail)
	}
}
