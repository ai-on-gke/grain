package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// A background run on a framework with no Stop hook of its own that ends
// with no closing move is resumed once, in the same session, with the
// stop rule's nudge and nothing else -- and the block is in the run's
// stored transcript and its tool census.
func TestRunDispatchResumesARunThatStoppedWithoutClosingOnce(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var calls []agent.RunConfig
	fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		calls = append(calls, cfg)
		if len(calls) == 1 {
			return &agent.Result{
				FinalText:        "Checking the CI log now -- back in a minute.",
				ToolCalls:        []agent.ToolCall{{Name: "read_file"}},
				Transcript:       "first turn",
				ResumableSession: "sess-1",
			}, nil
		}
		// The continuation also ends without closing: it is not sent
		// back a second time.
		return &agent.Result{
			FinalText:        "Still nothing to report.",
			ToolCalls:        []agent.ToolCall{{Name: "run_command", Arguments: map[string]any{"command": "ls"}}},
			Transcript:       "second turn",
			ResumableSession: "sess-1",
		}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{},
		*task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("framework ran %d time(s), want 2: the run, and one continuation", len(calls))
	}
	cont := calls[1]
	if cont.ResumeSession != "sess-1" {
		t.Errorf("continuation ResumeSession = %q, want sess-1", cont.ResumeSession)
	}
	if cont.ContinuePrompt != agent.BackgroundContinueReason {
		t.Errorf("continuation ContinuePrompt = %q, want the background nudge", cont.ContinuePrompt)
	}
	if cont.ResumeMessage() != agent.BackgroundContinueReason {
		t.Errorf("continuation would be sent %q, want the nudge alone", cont.ResumeMessage())
	}
	if cont.Prompt != calls[0].Prompt || cont.TaskID != calls[0].TaskID || cont.SandboxRoot != calls[0].SandboxRoot {
		t.Error("the continuation did not run under the same RunConfig as the run")
	}

	transcript, found, err := store.RunTranscript(ctx, "t1", 1)
	if err != nil || !found {
		t.Fatalf("reading the transcript: found=%v err=%v", found, err)
	}
	note := agent.StopNote(agent.StopBlock{Via: agent.StopViaResume, Reason: agent.BackgroundContinueReason})
	if !strings.Contains(transcript, "first turn") || !strings.Contains(transcript, note) ||
		!strings.Contains(transcript, "second turn") {
		t.Errorf("transcript does not record the block between the two turns:\n%s", transcript)
	}

	uses, err := store.RunToolUses(ctx)
	if err != nil {
		t.Fatalf("reading the census: %v", err)
	}
	blocked := 0
	for _, u := range uses {
		if u.Tool == agent.StopCensusTool {
			blocked += u.Calls
		}
	}
	if blocked != 1 {
		t.Errorf("census counts %d stop block(s), want 1: %+v", blocked, uses)
	}
}

// stopHookFramework stands in for claude: a framework whose CLI applies
// the stop rule itself.
type stopHookFramework struct{ agentFunc }

func (stopHookFramework) EnforcesStopRule() bool { return true }

// A framework that enforces the rule itself is never resumed by grain,
// and neither is a run that closed, nor one with no session to rejoin.
func TestRunDispatchDoesNotResumeWhatTheStopRuleAllows(t *testing.T) {
	t.Parallel()
	closing := &agent.Result{FinalText: "done", ResumableSession: "sess-1",
		ToolCalls: []agent.ToolCall{{Name: "comment_on_issue"}}}
	unclosed := &agent.Result{FinalText: "checking now", ResumableSession: "sess-1",
		ToolCalls: []agent.ToolCall{{Name: "read_file"}}}
	noSession := &agent.Result{FinalText: "checking now",
		ToolCalls: []agent.ToolCall{{Name: "read_file"}}}
	for _, c := range []struct {
		name   string
		result *agent.Result
		hook   bool
	}{
		{"closed", closing, false},
		{"framework has a Stop hook", unclosed, true},
		{"no resumable session", noSession, false},
	} {
		store, ctx := openStore(t)
		dispatchTask(t, ctx, store, "t1")
		d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
		startRun(t, ctx, store, d, baseTime)
		task, err := store.GetTask(ctx, "t1")
		if err != nil || task == nil {
			t.Fatalf("reading task: %v", err)
		}
		runs := 0
		run := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
			runs++
			r := *c.result
			return &r, nil
		})
		var fw agent.Framework = run
		if c.hook {
			fw = stopHookFramework{run}
		}
		if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{},
			*task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
			t.Fatalf("%s: RunDispatch: %v", c.name, err)
		}
		if runs != 1 {
			t.Errorf("%s: framework ran %d times, want 1", c.name, runs)
		}
	}
}

// An interactive session that answered in plain text is not resumed; one
// that ended on a promise is, with the session nudge.
func TestRunDispatchResumesASessionOnlyOnAPromise(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		final string
		want  int
	}{
		{"The queue is clear; nothing is waiting on it.", 1},
		{"I'll keep going and report back.", 2},
	} {
		store, ctx := openStore(t)
		task := dispatchTask(t, ctx, store, "t1")
		task.Interactive = true
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("filing interactive task: %v", err)
		}
		d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
		startRun(t, ctx, store, d, baseTime)
		got, err := store.GetTask(ctx, "t1")
		if err != nil || got == nil {
			t.Fatalf("reading task: %v", err)
		}
		var prompts []string
		fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			prompts = append(prompts, cfg.ContinuePrompt)
			return &agent.Result{FinalText: c.final, ResumableSession: "sess-1"}, nil
		})
		if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{},
			*got, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
			t.Fatalf("RunDispatch: %v", err)
		}
		if len(prompts) != c.want {
			t.Errorf("%q: framework ran %d times, want %d", c.final, len(prompts), c.want)
		}
		if c.want == 2 && prompts[1] != agent.SessionContinueReason {
			t.Errorf("%q: continuation nudge = %q, want SessionContinueReason", c.final, prompts[1])
		}
	}
}
