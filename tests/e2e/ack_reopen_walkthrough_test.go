// docs/test-plans/2026-10-06.md §6 step 2: a person's comment on a task
// waiting to be acknowledged sends it back to the queue (pkg/ui's
// AddComment, grain/task-2320), walked through a real dispatch rather
// than pkg/ui/ack_reopen_test.go's hand-written run records. A scripted
// agent finishes the task with comment_on_issue; an agent's comment and a
// team broadcast leave it where it is; a person's comment queues it, the
// next cycle runs it, and the prompt that run is given carries the
// comment.
package e2e

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// promptRecorder is a scripted framework that also keeps the prompt each
// run of one task was started with. Other tasks the orchestrator files for
// itself (the director's console session) run the same script and are not
// recorded.
type promptRecorder struct {
	task   string
	script []antigravity.Step
	mu     sync.Mutex
	got    []string
}

type recordingFramework struct {
	r     *promptRecorder
	inner agent.Framework
}

func (f recordingFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	if cfg.TaskID == f.r.task {
		f.r.mu.Lock()
		f.r.got = append(f.r.got, cfg.Prompt)
		f.r.mu.Unlock()
	}
	return f.inner.Run(ctx, cfg)
}

func (r *promptRecorder) framework(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
	return recordingFramework{r: r, inner: antigravity.NewForTest(antigravity.Steps(r.script...))}, nil
}

func (r *promptRecorder) prompts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func TestAPersonsCommentReopensAnAwaitingAckTaskAndTheRunIsGivenIt(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "reports"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}
	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")

	const id = "t-ack-reopen"
	rec := &promptRecorder{task: id, script: commentOnlyScript("the report is attached; nothing to change")}
	deps := orchestrator.Deps{
		Store: w.store, Client: github.NewClient(sim, nil), Sandboxes: worldSandboxes{w}, MaxWorkers: 1,
		Framework: rec.framework,
	}
	fileIssue(w, id, human("erin"), repo)

	clock := baseTime
	cycle := func() {
		t.Helper()
		clock = clock.Add(time.Minute)
		if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle: %v", err)
		}
	}
	cycle()
	assertState(w, id, model.StateAwaitingReply, false)
	if n := len(rec.prompts()); n != 1 {
		t.Fatalf("%d runs after the first cycle, want 1", n)
	}

	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("erin")}, w.store)

	// What a run says on its task is not an instruction to run again.
	agentCtx := ui.WithPrincipal(w.ctx, model.Principal{Kind: model.PrincipalAgent, ID: id})
	if err := client.AddComment(agentCtx, id, "one more note from the agent", nil); err != nil {
		t.Fatalf("agent comment: %v", err)
	}
	cycle()
	assertState(w, id, model.StateAwaitingReply, false)

	// Nor is a person's broadcast to the whole deployment: it is
	// addressed to all of its work, not to this report.
	sent, err := client.BroadcastTeamMessage(w.ctx,
		ui.BroadcastTeamRequest{Message: "code freeze from 17:00"})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if !slices.Contains(sent.TaskIDs, id) {
		t.Fatalf("the broadcast reached %v, want %s among them", sent.TaskIDs, id)
	}
	cycle()
	assertState(w, id, model.StateAwaitingReply, false)
	if n := len(rec.prompts()); n != 1 {
		t.Fatalf("%d runs after an agent comment and a broadcast, want still 1", n)
	}

	// A person's own comment is.
	const ask = "thanks -- please also do X"
	if err := client.AddComment(w.ctx, id, ask, nil); err != nil {
		t.Fatalf("person's comment: %v", err)
	}
	assertState(w, id, model.StateQueued, false)
	cycle()
	prompts := rec.prompts()
	if len(prompts) != 2 {
		t.Fatalf("%d runs after a person's comment, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], ask) {
		t.Fatalf("the second run's prompt does not carry the comment %q:\n%s", ask, prompts[1])
	}
	if strings.Contains(prompts[1], "code freeze from 17:00") {
		t.Logf("the second run's prompt also carries the earlier broadcast")
	}
	// The run answered with a comment again, so it is back waiting on a
	// reader -- not on another dispatch.
	assertState(w, id, model.StateAwaitingReply, false)
	cycle()
	if n := len(rec.prompts()); n != 2 {
		t.Fatalf("%d runs after the second answer, want 2", n)
	}
}
