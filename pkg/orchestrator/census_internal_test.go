package orchestrator

// What a run's tool calls become, before anything stores them.

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
)

func call(name, text string, isError bool) agent.ToolCall {
	return agent.ToolCall{Name: name, Text: text, IsError: isError}
}

func command(cmd, text string, isError bool) agent.ToolCall {
	return agent.ToolCall{
		Name: "run_command", Arguments: map[string]any{"command": cmd},
		Text: text, IsError: isError,
	}
}

func TestRunTelemetryCountsCallsErrorsAndSizesPerTool(t *testing.T) {
	t.Parallel()
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		command("go test ./...", strings.Repeat("x", 5000), true),
		command("go test ./pkg/model", strings.Repeat("x", 100), false),
		call("edit_file", "String not found", true),
		call("edit_file", "Edited", false),
		call("read_file", strings.Repeat("y", 30), false),
	}}

	got := runTelemetry(result)
	if len(got.Tools) != 3 {
		t.Fatalf("census = %+v, want a row per tool", got.Tools)
	}
	// Order is the order the run first reached for each tool.
	if got.Tools[0].Tool != "run_command" || got.Tools[1].Tool != "edit_file" || got.Tools[2].Tool != "read_file" {
		t.Errorf("tools came back %q, %q, %q", got.Tools[0].Tool, got.Tools[1].Tool, got.Tools[2].Tool)
	}
	cmd := got.Tools[0]
	if cmd.Calls != 2 || cmd.Errored != 1 {
		t.Errorf("run_command = %d calls, %d errored; want 2 and 1", cmd.Calls, cmd.Errored)
	}
	if cmd.ResultBytes != 5100 || cmd.MaxResultBytes != 5000 {
		t.Errorf("run_command sizes = %d total, %d max; want 5100 and 5000", cmd.ResultBytes, cmd.MaxResultBytes)
	}
	if cmd.Sizes.Total() != 2 || cmd.Sizes[model.SizeBucket(5000)] != 1 {
		t.Errorf("run_command histogram = %v, want both its results in it", cmd.Sizes)
	}
	// An errored call is an ordinary turn of the agent's own loop, and is
	// counted as a call as well as an error -- an error rate over calls
	// that excluded them could never exceed zero.
	if edits := got.Tools[1]; edits.Calls != 2 || edits.Errored != 1 {
		t.Errorf("edit_file = %+v, want 2 calls of which 1 errored", edits)
	}
}

// A run that never got as far as a tool records nothing, rather than a
// row of zeroes that would drag every rate it was averaged into.
func TestRunTelemetryIsEmptyForARunThatCalledNothing(t *testing.T) {
	t.Parallel()
	if !runTelemetry(nil).Empty() {
		t.Error("a run with no result recorded a census")
	}
	if !runTelemetry(&agent.Result{}).Empty() {
		t.Error("a run that made no calls recorded a census")
	}
}

// The timeout count is the sandbox's own health, and it comes from the
// bound's notice rather than from the exit status: exit=-1 is also what
// "could not start the command at all" looks like.
func TestRunTelemetryCountsOnlyTheCommandsTheBoundKilled(t *testing.T) {
	t.Parallel()
	// A copy of the notice pkg/mcp appends, checked against the reader
	// that owns it before it is relied on: a stale copy here fails as
	// "this literal is out of date" rather than as "the census stopped
	// counting timeouts".
	killed := "exit=-1\nstdout:\nbuilding\nstderr:\n\n\n[grain] Killed after 300s by " +
		"run_command's default bound, which this call did not choose -- it passed no `timeout`."
	if !mcp.RunCommandTimedOut(killed) {
		t.Fatalf("this test's copy of run_command's timeout notice is stale:\n%s", killed)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		command("sleep 600", killed, true),
		command("false", "exit=1\nstdout:\n\nstderr:\n", true),
		command("true", "exit=0\nstdout:\n\nstderr:\n", false),
	}}
	got := runTelemetry(result)
	if len(got.Tools) != 1 {
		t.Fatalf("census = %+v, want one row", got.Tools)
	}
	if use := got.Tools[0]; use.Calls != 3 || use.Errored != 2 || use.TimedOut != 1 {
		t.Errorf("run_command = %+v; want 3 calls, 2 errored, 1 killed by the bound", use)
	}
}

// The CI loop, in the order the run went round it: a push, a wait that
// failed, another push, a wait that passed. PushesBefore on that last
// wait is the answer to "how many pushes did this run take to go green".
func TestRunTelemetryRecordsTheCILoopInOrder(t *testing.T) {
	t.Parallel()
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		command("cd work && git commit -am wip", "exit=0\nstdout:\n\nstderr:\n", false),
		command("cd work && git push origin grain/task-1", "exit=0\nstdout:\n\nstderr:\n", false),
		call("wait_for_checks", "Waited 4m12s for CI on grain/task-1 at abc1234.\n\n"+
			"Checks against abc1234:\n  FAILING tests\n\n1 failing, 0 not finished, 0 otherwise done.\n\n"+
			"CI has failed, so I stopped waiting. Reproduce those failures", false),
		command("cd work && git push origin grain/task-1", "exit=0\nstdout:\n\nstderr:\n", false),
		call("wait_for_checks", "Waited 9m0s for CI on grain/task-1 at def5678.\n\n"+
			"Checks against def5678:\n  ok tests\n\n0 failing, 0 not finished, 1 otherwise done.\n\n"+
			"Every check against def5678 finished and none of them failed. Note that", false),
	}}

	got := runTelemetry(result)
	if len(got.CheckWaits) != 2 {
		t.Fatalf("recorded %d CI waits, want 2: %+v", len(got.CheckWaits), got.CheckWaits)
	}
	first, second := got.CheckWaits[0], got.CheckWaits[1]
	if first.Seq != 0 || first.Verdict != mcp.WaitVerdictFailed ||
		first.Waited != 4*time.Minute+12*time.Second || first.PushesBefore != 1 {
		t.Errorf("first wait = %+v", first)
	}
	if second.Seq != 1 || second.Verdict != mcp.WaitVerdictPassed ||
		second.Waited != 9*time.Minute || second.PushesBefore != 2 {
		t.Errorf("second wait = %+v", second)
	}
}

// A push that GitHub refused is not a push. PushesBefore feeds "how many
// pushes before green", where an invented one is worse than a missed one.
func TestRunTelemetryDoesNotCountARejectedPush(t *testing.T) {
	t.Parallel()
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		command("cd work && git push origin grain/task-1", "exit=1\nstdout:\n\nstderr:\nrejected", true),
		command("cd work && git status", "exit=0\nstdout:\n\nstderr:\n", false),
		call("wait_for_checks", "Waited 30s for CI on grain/task-1 at abc1234.\n\n"+
			"GitHub still reports no checks at all against abc1234 after 30s. Either this repo", false),
	}}
	got := runTelemetry(result)
	if len(got.CheckWaits) != 1 {
		t.Fatalf("recorded %d CI waits, want 1", len(got.CheckWaits))
	}
	if got.CheckWaits[0].Verdict != mcp.WaitVerdictNoChecks || got.CheckWaits[0].PushesBefore != 0 {
		t.Errorf("wait = %+v, want no checks and no pushes before it", got.CheckWaits[0])
	}
}

// Cuts are counted for every tool, not only the two the census reads
// more than a count off: mcp's result cap applies to all of them, and the
// size recorded for a call is the size *after* it, so a census without
// this would show a deployment whose answers all fit comfortably under a
// cap that was in fact trimming them to it.
func TestRunTelemetryCountsTheAnswersGrainCutShort(t *testing.T) {
	t.Parallel()
	// A copy of the notice pkg/mcp writes where the middle was, checked
	// against the reader that owns it before it is relied on, exactly as
	// the timeout notice above is.
	cut := func(dropped string) string {
		return "the head of it\n[grain] " + dropped + " of 40 KB elided here by grain, not by " +
			"the command -- a tool result is capped so one answer cannot eat the run's " +
			"context. To see it: call it again for less.\nthe tail of it\n"
	}
	if _, ok := mcp.ResultElision(cut("32 KB")); !ok {
		t.Fatalf("this test's copy of the elision notice is stale:\n%s", cut("32 KB"))
	}

	result := &agent.Result{ToolCalls: []agent.ToolCall{
		command("go test ./...", cut("32 KB"), true),
		command("go version", "exit=0\nstdout:\ngo1.24\nstderr:\n", false),
		call("list_ci_runs", cut("4 KB"), false),
		// A cut whose size this build cannot read back is still a cut. It
		// counts as one and stays out of the distribution rather than
		// being recorded as a nought-byte drop.
		call("read_memory_document", cut("some"), false),
		call("edit_file", "Edited", false),
	}}

	byTool := map[string]model.RunToolUse{}
	for _, use := range runTelemetry(result).Tools {
		byTool[use.Tool] = use
	}
	if cmd := byTool["run_command"]; cmd.Calls != 2 || cmd.Elided != 1 ||
		cmd.Dropped.Total() != 1 || cmd.Dropped[model.SizeBucket(32<<10)] != 1 {
		t.Errorf("run_command = %+v; want 1 of its 2 answers cut, at 32 KB", cmd)
	}
	if ci := byTool["list_ci_runs"]; ci.Elided != 1 || ci.Dropped[model.SizeBucket(4<<10)] != 1 {
		t.Errorf("list_ci_runs = %+v; want its answer counted as cut at 4 KB", ci)
	}
	if doc := byTool["read_memory_document"]; doc.Elided != 1 || doc.Dropped.Total() != 0 {
		t.Errorf("read_memory_document = %+v; want a cut counted with no size read off it", doc)
	}
	if edits := byTool["edit_file"]; edits.Elided != 0 || edits.Dropped.Total() != 0 {
		t.Errorf("edit_file = %+v; want nothing cut", edits)
	}
}

// The red-CI procedure as the census sees it: red after the first push,
// a narrowed reproduction (which fails, as a reproduction should), the
// fix push, then a selective wait before the full one.
func TestRunTelemetryRecordsHowTheRunWorkedARedBuild(t *testing.T) {
	t.Parallel()
	failed := "Waited 4m12s for CI on grain/task-1 at abc1234.\n\n" +
		"Checks against abc1234:\n  FAILING tests\n\n1 failing, 0 not finished, 0 otherwise done.\n\n" +
		"CI has failed, so I stopped waiting. Reproduce those failures"
	if wait, ok := mcp.ReadCheckWait(failed); !ok || wait.Verdict != mcp.WaitVerdictFailed {
		t.Fatalf("this test's copy of a failed wait is stale: %+v", wait)
	}
	ok := "exit=0\nstdout:\n\nstderr:\n"
	result := &agent.Result{ToolCalls: []agent.ToolCall{
		command("cd work && go test ./pkg/ci", ok, false),
		command("cd work && git push origin grain/task-1", ok, false),
		call("wait_for_checks", failed, false),
		command("cd work && go test -race -run 'TestTheGoJobs' ./tests/deploy", "exit=1\nFAIL", true),
		command("cd work && git push origin grain/task-1", ok, false),
		{Name: "wait_for_checks", Arguments: map[string]any{"jobs": []any{"go"}}, Text: "Waited 1m0s"},
	}}
	got := runTelemetry(result).CIHabit
	want := model.RunCIHabit{NarrowTestBeforeFixPush: true, PushedAfterRed: true, Selective: true}
	if got == nil || *got != want {
		t.Errorf("CI habit = %+v, want %+v", got, want)
	}

	// A repair run, handed the red: the narrowed test before its first
	// push is the one that counts, and a full wait is not selective.
	result = &agent.Result{ToolCalls: []agent.ToolCall{
		command("go test -run=TestX ./pkg/x", ok, false),
		command("git push origin grain/task-2", ok, false),
		call("wait_for_checks", "Waited 1m0s", false),
	}}
	got = runTelemetry(result).CIHabit
	if got == nil || !got.NarrowTestBeforePush || got.Selective || got.PushedAfterRed {
		t.Errorf("CI habit = %+v, want only a narrowed test before the first push", got)
	}

	// A run that never went red and never narrowed anything has no row.
	result = &agent.Result{ToolCalls: []agent.ToolCall{
		command("go test ./...", ok, false),
		command("git push origin grain/task-3", ok, false),
	}}
	if got := runTelemetry(result).CIHabit; got != nil {
		t.Errorf("CI habit = %+v, want none", got)
	}
}

func TestIsNarrowTestRecognisesTheRunnersSelectionFlags(t *testing.T) {
	t.Parallel()
	for cmd, want := range map[string]bool{
		"go test ./...":                                   false,
		"go test -race ./pkg/ci":                          false,
		"go test -run TestFoo ./pkg/ci":                   true,
		"go test -race -run='TestA|TestB' ./tests/deploy": true,
		"pytest tests/test_x.py::test_y":                  true,
		"pytest -k parser":                                true,
		"pytest":                                          false,
		"npx playwright test --grep inbox":                true,
		"npx playwright test":                             false,
		"git log --grep -run":                             false,
	} {
		if got := isNarrowTest(command(cmd, "", false)); got != want {
			t.Errorf("isNarrowTest(%q) = %v, want %v", cmd, got, want)
		}
	}
}
