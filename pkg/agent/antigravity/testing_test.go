package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
)

// TestNewForTestActuallyExecutesTheScriptedToolCalls is the guarantee the
// whole e2e suite rests on. The Gemini runtime this package replaced
// looped tool calls in-process, so a scripted test really did clone,
// commit and push; a fake that merely replayed a canned stream-json
// capture would keep every one of those tests green while quietly doing
// nothing at all. This asserts the file really lands on disk.
func TestNewForTestActuallyExecutesTheScriptedToolCalls(t *testing.T) {
	root := t.TempDir()
	f := NewForTest(Steps(
		ToolStep("write_file", map[string]any{"file_path": "ok.txt", "content": "PONG"}),
		TextStep("wrote it"),
	))

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "ok.txt"))
	if err != nil {
		t.Fatalf("the scripted write_file did not reach the sandbox: %v", err)
	}
	if strings.TrimSpace(string(data)) != "PONG" {
		t.Errorf("ok.txt = %q, want PONG", data)
	}
	if result.FinalText != "wrote it" {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "wrote it")
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "write_file" {
		t.Fatalf("ToolCalls = %+v, want the one scripted call", result.ToolCalls)
	}
	if result.ToolCalls[0].IsError {
		t.Errorf("write_file reported an error: %q", result.ToolCalls[0].Text)
	}
}

// TestNewForTestConfinesToolsToTheSandboxRoot: the fake stands up the
// same mcp.NewSandboxTools a real run's forked mcpserver would, so the
// confinement a scripted test relies on is the real one.
func TestNewForTestConfinesToolsToTheSandboxRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "escaped.txt")
	f := NewForTest(Steps(
		ToolStep("write_file", map[string]any{"file_path": outside, "content": "nope"}),
		TextStep("tried"),
	))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("a scripted write outside the sandbox root landed anyway (stat err = %v)", err)
	}
}

// TestNewForTestRecordsAToolErrorRatherThanFailingTheRun matches what a
// real run does with a failing command: the error is the tool's result,
// not the run's.
func TestNewForTestRecordsAToolErrorRatherThanFailingTheRun(t *testing.T) {
	f := NewForTest(Steps(
		ToolStep("run_command", map[string]any{"command": "echo boom >&2; exit 1"}),
		TextStep("could not do it"),
	))
	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.ToolCalls) != 1 || !result.ToolCalls[0].IsError {
		t.Fatalf("ToolCalls = %+v, want one call recorded as an error", result.ToolCalls)
	}
	if result.FinalText != "could not do it" {
		t.Errorf("FinalText = %q, want the run to have carried on to its answer", result.FinalText)
	}
}

// TestScriptSeesThePrompt is what the randomized cluster test needs: its
// generator reads the target repo and branch out of the prompt, and the
// prompt has to arrive the same way a real agy would have received it
// (over the stdin user event Framework.Run writes).
func TestScriptSeesThePrompt(t *testing.T) {
	const prompt = "Work in acme/widgets. Push your change to a new branch named \"grain/task-9\""
	var seen string
	f := NewForTest(ScriptFunc(func(p string) (Step, bool) {
		seen = p
		return TextStep("ok"), true
	}))
	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: prompt, SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Contains rather than equals: Framework.Run prepends toolPreamble to
	// what it sends, and a real agy sees that too. The generator matches
	// the target repo and branch out of whatever arrives, so what this
	// pins is that the task's own prompt reaches the script whole.
	if !strings.Contains(seen, prompt) {
		t.Errorf("Script saw prompt %q, want it to carry %q", seen, prompt)
	}
}

// TestScriptIsAskedForTheNextStepOnlyAfterTheLastToolRan is the contract
// the load test's own timing depends on, and what makes a dynamic script
// able to decide its next move from work the previous one really did.
func TestScriptIsAskedForTheNextStepOnlyAfterTheLastToolRan(t *testing.T) {
	root := t.TempDir()
	var order []string
	step := 0
	f := NewForTest(ScriptFunc(func(string) (Step, bool) {
		// Whether the first step's file exists by the time the second
		// step is asked for is exactly the question.
		if _, err := os.Stat(filepath.Join(root, "first.txt")); err == nil {
			order = append(order, "saw-first-file")
		} else {
			order = append(order, "no-file-yet")
		}
		step++
		if step == 1 {
			return ToolStep("write_file", map[string]any{"file_path": "first.txt", "content": "x"}), true
		}
		return TextStep("done"), true
	}))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"no-file-yet", "saw-first-file"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("script call order = %v, want %v: Next must be called after the previous tool ran", order, want)
	}
}

// TestNewForTestHonoursTheTurnCap proves a scripted run is subject to the
// same cap a real one is -- the load and randomized tests lean on runs
// terminating.
func TestNewForTestHonoursTheTurnCap(t *testing.T) {
	f := NewForTest(ScriptFunc(func(string) (Step, bool) {
		// A script that never offers a final answer: only the cap ends
		// this run.
		return ToolStep("run_command", map[string]any{"command": "true"}), true
	}))
	_, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 2,
	})
	if err == nil {
		t.Fatal("Run err = nil for a script that never finishes, want the cap to end it")
	}
}

// TestExhaustedScriptEndsTheRunWithoutHanging covers a script that simply
// runs out -- the shape several migrated tests have, where the framework
// used to be handed one response short of a final answer.
func TestExhaustedScriptEndsTheRunWithoutHanging(t *testing.T) {
	f := NewForTest(Steps(ToolStep("run_command", map[string]any{"command": "true"})))
	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil for an exhausted script, want an error")
	}
	if result == nil || len(result.ToolCalls) != 1 {
		t.Fatalf("result = %+v, want the call it made before running out", result)
	}
}
