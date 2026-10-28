package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// handoverFramework answers the last turn with a write_handover call and
// records what it was asked.
type handoverFramework struct {
	got []agent.RunConfig
}

func (f *handoverFramework) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.got = append(f.got, cfg)
	return &agent.Result{ToolCalls: []agent.ToolCall{{Name: "write_handover",
		Arguments: map[string]any{"handover": "half the parser is done and pushed"}}}}, nil
}

// A run whose context ran out is a `context` suspension, is given one
// last turn in its session to write its handover, and leaves no session
// for the next run to join.
func TestContextExhaustionSuspendsAndAsksForAHandover(t *testing.T) {
	store, ctx := openTestStore(t)
	tk := stdTask("subject")
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "subject-1", TaskID: tk.ID, Attempt: 1, StartedAt: standingNow},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Now: func() time.Time { return standingNow }}
	runErr := fmt.Errorf("claude: %w", errors.New("API Error: 400 prompt is too long: 210000 tokens > 200000 maximum"))
	end := classifyRunEnd(ctx, store, cfg, tk, "subject-1", nil, runErr, nil, 0, PauseScope{}, standingNow)
	if end.suspend != model.SuspendContext || end.outcome != model.SuspendedOutcome {
		t.Fatalf("ended (%q, %q), want a context suspension", end.outcome, end.suspend)
	}

	fw := &handoverFramework{}
	lastTurnForHandover(ctx, store, cfg, fw, agent.RunConfig{Prompt: "the original prompt", MaxTurns: 100}, tk, "sess-1")
	if len(fw.got) != 1 || fw.got[0].ResumeSession != "sess-1" || fw.got[0].MaxTurns != contextHandoverTurns ||
		!strings.Contains(fw.got[0].Prompt, "write_handover") {
		t.Fatalf("last turn = %+v, want one turn in sess-1 asking for the handover", fw.got)
	}
	comments, err := store.Comments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) == 0 || !comments[len(comments)-1].Handover ||
		!strings.Contains(comments[len(comments)-1].Body, "half the parser") {
		t.Fatalf("comments = %+v, want the handover relayed", comments)
	}
	lastTurnForHandover(ctx, store, cfg, fw, agent.RunConfig{}, tk, "")
	if len(fw.got) != 1 {
		t.Fatal("a run with no session was given a last turn")
	}

	if err := store.SetSuspendedSession(ctx, tk.ID, &model.SuspendedSession{Framework: "claude", Token: "sess-1",
		RunID: "subject-1", At: standingNow}, standingNow); err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordRunEnd(ctx, ctx, store, cfg, tk, "subject-1", obs, &agent.Result{ResumableSession: "sess-1"},
		end, nil, true, "sess-1", "sess-1"); err != nil {
		t.Fatal(err)
	}
	if obs, err = store.GetObservation(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	if obs.SuspendedSession != nil {
		t.Fatalf("a context suspension left session %+v for the next run to join", obs.SuspendedSession)
	}
	runs, err := store.Runs(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].SuspendKind != model.SuspendContext {
		t.Fatalf("run recorded with suspend_kind %q, want context", runs[0].SuspendKind)
	}
}

func TestContextExhaustedReadsTheCLIsWords(t *testing.T) {
	for _, err := range []error{
		agent.ErrContextExhausted,
		fmt.Errorf("x: %w", agent.ErrContextExhausted),
		errors.New("Error: Prompt is too long"),
		errors.New(`{"code":"context_length_exceeded"}`),
	} {
		if !agent.ContextExhausted(err) {
			t.Errorf("ContextExhausted(%v) = false", err)
		}
	}
	for _, err := range []error{nil, errors.New("exceeded max turns (20) without a final answer")} {
		if agent.ContextExhausted(err) {
			t.Errorf("ContextExhausted(%v) = true", err)
		}
	}
}
