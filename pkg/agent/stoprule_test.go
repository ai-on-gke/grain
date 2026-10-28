package agent

import (
	"strings"
	"testing"
)

// TestPromisesMoreWork is the detector against final messages of both
// kinds. The promises are the endings task 831's Director session
// stopped on (2026-10-25/26) and their near relatives; the answers are
// the shapes real runs close on -- a report, a summary with a code
// block, an offer the person can take up, a "let me know" -- none of
// which may be sent back.
func TestPromisesMoreWork(t *testing.T) {
	promises := []string{
		"Checking the merge queue now -- back in a minute.",
		"The CI run is still going. I'll keep going and report back when it finishes.",
		"Checking now.",
		"Let me look at the failing job.",
		"I found the branch. Let me check what CI says about it:",
		"Task 1203 is still running. Checking its log now.",
		"OK, that's the root cause. I'll fix it and push.",
		"Thanks -- I'll continue with the migration next.",
		"Got it. Looking into the failing ui e2e job next.",
		"Here is what I found so far:\n\n- the queue is held\n- two PRs are waiting\n\nNext steps:",
		"Understood. Stand by while I re-run the suite.",
		"**Let me check the logs.**",
		"Sure, give me a moment to read through pkg/orchestrator.",
	}
	for _, text := range promises {
		if !PromisesMoreWork(text) {
			t.Errorf("PromisesMoreWork(%q) = false, want true", text)
		}
	}

	answers := []string{
		"",
		"Done. The fix is pushed as #4950 and CI is green.",
		"Task 1203 merged at 18:02 and the queue is clear. Nothing else is waiting on it.",
		"I've filed task 1250 for the follow-up and linked it to this one.",
		"The flake is in TestDemoServer; it has failed 3 of the last 20 runs. Let me know if you want it quarantined.",
		"If you want, I'll fix the other two call sites as well.",
		"Would you like me to open the follow-up? I'll leave it until you say.",
		"They let me know the deploy finished, so the rollout is complete.",
		"Run this to reproduce:\n\n```\ngo test ./pkg/agent -run TestX\nlet me check:\n```\n\nIt fails on main too.",
		"The answer is no: the queue never retries a cancelled batch.",
		"Summary of the change:\n\n1. added the hook\n2. added tests\n\nAll checks passed.",
		"I'll wait for your answer before touching the schedule.",
	}
	for _, text := range answers {
		if PromisesMoreWork(text) {
			t.Errorf("PromisesMoreWork(%q) = true, want false", text)
		}
	}
}

func TestDecideStopSession(t *testing.T) {
	// A session may end on a plain answer, with or without tool calls.
	if d := DecideStop(true, "The queue is clear.", nil); d.Block {
		t.Errorf("session answer blocked: %+v", d)
	}
	// It is sent back when it ends on a promise, whatever tools it used.
	calls := []ToolCall{{Name: "comment_on_issue"}}
	d := DecideStop(true, "Checking the CI log now, back in a minute.", calls)
	if !d.Block || d.Reason != SessionContinueReason {
		t.Errorf("session promise = %+v, want a block with SessionContinueReason", d)
	}
}

func TestDecideStopBackground(t *testing.T) {
	cases := []struct {
		name  string
		calls []ToolCall
		block bool
	}{
		{"no tool calls", nil, true},
		{"only reading", []ToolCall{{Name: "read_file"}, {Name: "run_command", Arguments: map[string]any{"command": "go test ./..."}}}, true},
		{"failed push", []ToolCall{{Name: "run_command", Arguments: map[string]any{"command": "git push origin b"}, IsError: true}}, true},
		{"blocking wait_for_task", []ToolCall{{Name: "wait_for_task", Arguments: map[string]any{"task_id": "12"}}}, true},
		{"pushed", []ToolCall{{Name: "run_command", Arguments: map[string]any{"command": "cd work && git push origin b"}}}, false},
		{"comment_on_issue", []ToolCall{{Name: "comment_on_issue"}}, false},
		{"ask_question", []ToolCall{{Name: "ask_question"}}, false},
		{"nothing_needed", []ToolCall{{Name: "nothing_needed"}}, false},
		{"request_secret", []ToolCall{{Name: "request_secret"}}, false},
		{"wait_for_task park", []ToolCall{{Name: "wait_for_task", Arguments: map[string]any{"park": true}}}, false},
		{"wait_for_task resume_at", []ToolCall{{Name: "wait_for_task", Arguments: map[string]any{"resume_at": "1h"}}}, false},
		{"errored closing tool still counts", []ToolCall{{Name: "ask_question", IsError: true}}, false},
	}
	for _, c := range cases {
		d := DecideStop(false, "All done.", c.calls)
		if d.Block != c.block {
			t.Errorf("%s: Block = %v, want %v", c.name, d.Block, c.block)
		}
		if d.Block && d.Reason != BackgroundContinueReason {
			t.Errorf("%s: Reason = %q", c.name, d.Reason)
		}
	}
	for _, tool := range []string{"comment_on_issue", "ask_question", "nothing_needed", "request_secret", "wait_for_task", "push"} {
		if !strings.Contains(BackgroundContinueReason, tool) {
			t.Errorf("BackgroundContinueReason does not name %s", tool)
		}
	}
}

func TestContinueResult(t *testing.T) {
	first := &Result{FinalText: "checking now", ToolCalls: []ToolCall{{Name: "read_file"}}, Transcript: "first", ResumableSession: "s1"}
	next := &Result{FinalText: "done", ToolCalls: []ToolCall{{Name: "comment_on_issue"}}, Transcript: "second", ResumableSession: "s2"}
	block := StopBlock{Via: StopViaResume, Reason: SessionContinueReason}
	got := ContinueResult(first, next, block)
	if got.FinalText != "done" || len(got.ToolCalls) != 2 || got.ResumableSession != "s2" {
		t.Errorf("merged = %+v", got)
	}
	if len(got.StopBlocks) != 1 || got.StopBlocks[0] != block {
		t.Errorf("StopBlocks = %+v", got.StopBlocks)
	}
	if !strings.Contains(got.Transcript, "first") || !strings.Contains(got.Transcript, StopNote(block)) ||
		!strings.HasSuffix(got.Transcript, "second") {
		t.Errorf("Transcript = %q", got.Transcript)
	}
	if len(first.ToolCalls) != 1 || first.StopBlocks != nil {
		t.Errorf("first was modified: %+v", first)
	}
	alone := ContinueResult(first, nil, block)
	if alone.FinalText != "checking now" || len(alone.StopBlocks) != 1 {
		t.Errorf("alone = %+v", alone)
	}
}

func TestResumeMessage(t *testing.T) {
	cfg := RunConfig{Prompt: "the task"}
	if got := cfg.ResumeMessage(); got != SuspendedPreamble+"the task" {
		t.Errorf("ResumeMessage() = %q", got)
	}
	cfg.ContinuePrompt = SessionContinueReason
	if got := cfg.ResumeMessage(); got != SessionContinueReason {
		t.Errorf("ResumeMessage() with ContinuePrompt = %q", got)
	}
}
