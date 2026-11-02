// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// --- what a finished run's detail records about its tool calls ---------
//
// The question these exist for is not "is the sentence nicely worded".
// It is whether a deployment can answer, from its own store, whether a
// tool it built and named in the prompt is being called at all -- see
// outcomeOf's own doc comment. agent.Result is never persisted and
// Result.Transcript is optional prose, so task_run.detail is the whole
// record, and a successful run used to leave it blank.

// The successful ending is the one that matters most here: a run that
// pushed, read CI, repaired and pushed again is a run that succeeds, so
// the path that recorded nothing was exactly the path worth measuring.
func TestOutcomeOfRecordsWhatASuccessfulRunCalled(t *testing.T) {
	t.Parallel()
	outcome, detail := outcomeOf(&agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "run_command"},
		{Name: "open_pull_request"},
		{Name: "pull_request_status"},
		{Name: "run_command"},
		{Name: "pull_request_status"},
	}})
	if outcome != "succeeded" {
		t.Fatalf("outcome = %q, want %q", outcome, "succeeded")
	}
	for _, want := range []string{
		"5 tool call",
		"open_pull_request x1",
		"pull_request_status x2",
		"run_command x2",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to contain %q", detail, want)
		}
	}
}

// An errored tool call is a result the agent reads and works around, not
// a broken run -- pkg/mcp's run_command sets IsError for any non-zero
// exit status, so a grep that matched nothing used to fail a run that
// went on to open its pull request. See outcomeOf's own doc comment for
// what that cost: task_streak counts anything other than "succeeded", so
// those bogus failures backed a healthy task off and eventually stopped
// dispatching it altogether.
func TestOutcomeOfDoesNotFailARunOverAnErroringToolCall(t *testing.T) {
	t.Parallel()
	outcome, detail := outcomeOf(&agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "open_pull_request"},
		{Name: "run_command", Text: "exit=1\nstdout:\n\nstderr:\n", IsError: true},
	}})
	if outcome != "succeeded" {
		t.Fatalf("outcome = %q, want %q", outcome, "succeeded")
	}
	// The errors are still on the record -- both which tool they came
	// from and how many of the run's calls did not land.
	if !strings.Contains(detail, "run_command(error) x1") {
		t.Errorf("detail = %q, want it to name the tool that errored", detail)
	}
	if !strings.Contains(detail, "1 of them returned an error") {
		t.Errorf("detail = %q, want it to count the errored calls", detail)
	}
	if !strings.Contains(detail, "open_pull_request x1") {
		t.Errorf("detail = %q, want it to name every call the run made", detail)
	}
}

// The counterpart: a run with nothing wrong says nothing about errors,
// rather than reporting zero of them.
func TestOutcomeOfSaysNothingAboutErrorsWhenThereWereNone(t *testing.T) {
	t.Parallel()
	_, detail := outcomeOf(&agent.Result{ToolCalls: []agent.ToolCall{{Name: "run_command"}}})
	if strings.Contains(detail, "returned an error") {
		t.Errorf("detail = %q, want no mention of errors on a run that had none", detail)
	}
}

// The one ending with nothing to summarise keeps its own diagnosis: an
// agent that never called a tool did not fail at the work so much as
// never attempt it, and a count of zero says that worse than the
// sentence does.
func TestOutcomeOfStillDistinguishesARunThatNeverStarted(t *testing.T) {
	t.Parallel()
	outcome, detail := outcomeOf(&agent.Result{})
	if outcome != "failed" {
		t.Fatalf("outcome = %q, want %q", outcome, "failed")
	}
	if !strings.Contains(detail, "no tool calls at all") {
		t.Errorf("detail = %q, want it to say the agent never called a tool", detail)
	}
}

// --- what a redispatch is told about the attempts before it -----------
//
// Internal because the interesting cases are the bounds: how much of an
// arbitrarily long history reaches a prompt, and how much of one
// attempt's task_run.detail does. BuildPrompt is what a run actually
// reads, and run_test.go drives that end to end; these pin the shape the
// section is bounded to, which is the half that keeps a task on its
// ninth attempt from opening on a page of its own past.

func TestPreviousAttemptsSectionNamesEachAttemptsOutcomeAndDetail(t *testing.T) {
	t.Parallel()
	section := previousAttemptsSection(History{
		Attempt: 3,
		Attempts: []model.Run{
			{Attempt: 1, Outcome: "failed", Detail: "the agent made no tool calls at all"},
			{Attempt: 2, Outcome: "cancelled", Detail: "the run hit grain's own wall-clock cap"},
		},
		Commits: []string{"9f8e7d6 Bound the CI answer", "1a2b3c4 Add the failing test"},
	}, "grain/task-152")

	for _, want := range []string{
		"you are attempt 3",
		"attempt 1", "no tool calls at all",
		"attempt 2", "wall-clock cap",
		"grain/task-152",
		"9f8e7d6 Bound the CI answer",
		"1a2b3c4 Add the failing test",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("section does not mention %q:\n%s", want, section)
		}
	}
}

// A first attempt has no history, and gets no section at all rather than
// a heading saying so -- the same rule commentThreadSection follows for
// a task nobody has commented on.
func TestPreviousAttemptsSectionIsEmptyForAFirstAttempt(t *testing.T) {
	t.Parallel()
	if s := previousAttemptsSection(History{Attempt: 1}, "grain/task-152"); s != "" {
		t.Errorf("section for a first attempt = %q, want nothing at all", s)
	}
}

// Bounded to the last few attempts, and honest about the ones it left
// out: a task grain has run nine times has nothing more to teach its
// tenth attempt than its last three endings do, and a prompt is not a
// run listing.
func TestPreviousAttemptsSectionKeepsOnlyTheMostRecentAttempts(t *testing.T) {
	t.Parallel()
	var runs []model.Run
	for i := 1; i <= 9; i++ {
		runs = append(runs, model.Run{Attempt: i, Outcome: "failed", Detail: fmt.Sprintf("ending %d", i)})
	}
	section := previousAttemptsSection(History{Attempt: 10, Attempts: runs}, "grain/task-152")

	if !strings.Contains(section, "ending 9") || !strings.Contains(section, "ending 7") {
		t.Errorf("section drops the most recent attempts:\n%s", section)
	}
	if strings.Contains(section, "ending 6") {
		t.Errorf("section is not bounded to %d attempts:\n%s", maxPreviousAttempts, section)
	}
	if !strings.Contains(section, "6 earlier one(s) not listed") {
		t.Errorf("section does not say how many attempts it left out:\n%s", section)
	}
}

// One attempt's detail is a column nothing bounds -- a framework's own
// error text is whatever its CLI printed -- so it is trimmed to one line
// and to maxAttemptDetail here, the same shape agent.TrimLimitMessage
// gives the details it writes.
func TestPreviousAttemptsSectionTrimsAnUnboundedDetail(t *testing.T) {
	t.Parallel()
	section := previousAttemptsSection(History{
		Attempt:  2,
		Attempts: []model.Run{{Attempt: 1, Outcome: "failed", Detail: "panic:\n" + strings.Repeat("stack frame ", 200)}},
	}, "grain/task-152")

	var line string
	for _, l := range strings.Split(section, "\n") {
		if strings.HasPrefix(l, "- attempt 1") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no line for attempt 1 at all:\n%s", section)
	}
	// The whole attempt, on one line: the heading and the detail's own
	// ceiling and nothing else. A stack trace pasted in as its own
	// paragraphs would break the list this section is.
	if len(line) > maxAttemptDetail+len("- attempt 1 ended \"failed\": ")+len("...") {
		t.Errorf("attempt line is %d bytes, want it bounded by maxAttemptDetail (%d): %q",
			len(line), maxAttemptDetail, line)
	}
	if !strings.Contains(section, "panic:") {
		t.Errorf("the start of the detail was trimmed away with the rest:\n%s", section)
	}
}

// The commit list is bounded too, and says so: RunDispatch asks
// checkoutCommits for one more than the list holds precisely so "there is
// more" can be said without a second, unbounded read.
func TestPreviousAttemptsSectionSaysWhenThereAreMoreCommits(t *testing.T) {
	t.Parallel()
	var commits []string
	for i := 0; i <= maxBranchCommits; i++ {
		commits = append(commits, fmt.Sprintf("00000%02d commit %d", i, i))
	}
	section := previousAttemptsSection(History{
		Attempt:  2,
		Attempts: []model.Run{{Attempt: 1, Outcome: "failed"}},
		Commits:  commits,
	}, "grain/task-152")

	if strings.Contains(section, fmt.Sprintf("commit %d", maxBranchCommits)) {
		t.Errorf("section is not bounded to %d commits:\n%s", maxBranchCommits, section)
	}
	for _, want := range []string{"older ones still", "`git log`"} {
		if !strings.Contains(section, want) {
			t.Errorf("section does not point at the rest of the log (%q):\n%s", want, section)
		}
	}
}

// A run whose row was never finished -- a daemon that died mid-run,
// before recover.go's sweep reached it -- reads as the unknown it is
// rather than as a blank where an outcome should be.
func TestPreviousAttemptsSectionSaysWhenAnAttemptHasNoOutcome(t *testing.T) {
	t.Parallel()
	section := previousAttemptsSection(History{
		Attempt:  2,
		Attempts: []model.Run{{Attempt: 1}},
	}, "grain/task-152")
	if !strings.Contains(section, "no outcome recorded") {
		t.Errorf("section does not say the attempt's outcome is unknown:\n%s", section)
	}
}

// --- what the prompt says about the repo's setup command ---------------
//
// A run reads its prompt once, at turn 1, and there is nothing in the
// sandbox that says a setup command was ever run in it. So these pin the
// two facts a run has to be given: that the setup happened at all, and
// -- the case this whole field exists for -- that it failed, before the
// run reads the first broken build as its own doing.

func TestSetupSectionSaysNothingWhenTheRepoHasNoSetupCommand(t *testing.T) {
	t.Parallel()
	if got := setupSection(nil); got != "" {
		t.Errorf("setupSection(nil) = %q, want nothing at all", got)
	}
}

func TestSetupSectionNamesASucceededSetupSoItIsNotRunAgain(t *testing.T) {
	t.Parallel()
	got := setupSection(&SetupResult{Command: "make deps", ExitCode: 0, Output: "exit=0"})
	for _, want := range []string{"make deps", "succeeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt section = %q, want it to mention %q", got, want)
		}
	}
	if strings.Contains(got, "FAILED") {
		t.Errorf("prompt section = %q, want nothing said about a failure", got)
	}
}

func TestSetupSectionTellsTheRunAFailedSetupIsNotItsChange(t *testing.T) {
	t.Parallel()
	got := setupSection(&SetupResult{
		Command:  "make deps",
		ExitCode: 2,
		Output:   "exit=2\nstderr:\nno such package: widgetlib",
	})
	// The exit status, the command, and the tail -- and the sentence that
	// makes the difference between a run working around the failure and a
	// run "fixing" the code until the broken tree stops complaining.
	for _, want := range []string{"make deps", "exit 2", "no such package: widgetlib", "not your change"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt section = %q, want it to mention %q", got, want)
		}
	}
}

// --- what the sandbox does not have -----------------------------------
//
// sandboxToolingSection is three sentences that each stand for turns
// already spent: `gh` (a decision, docs/design.md), apt with no package
// lists on the image, and exit 127 in front of an `&&` chain. What these
// pin is that they are all still in there and that the GitHub one does
// not reach a run with nothing to read CI about.

func TestSandboxToolingSectionNamesTheAbsencesAndTheirSubstitutes(t *testing.T) {
	t.Parallel()
	got := sandboxToolingSection(true, false)
	for _, want := range []string{
		"`gh`", "wait_for_checks", "pull_request_status", "base_branch_status",
		"sudo apt-get update", "command -v", "127", "&&",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt section = %q, want it to mention %q", got, want)
		}
	}
	// A grain-ci run reads CI with the `grain` CLI: its MCP roster has no
	// CI tools (design/design/sandbox-first-agent-surface.design.md §6).
	got = sandboxToolingSection(true, true)
	for _, want := range []string{"`gh`", "`grain ci wait`", "`grain pr status`", "`grain pr base-status`"} {
		if !strings.Contains(got, want) {
			t.Errorf("grain-ci prompt section = %q, want it to mention %q", got, want)
		}
	}
	if strings.Contains(got, "wait_for_checks") {
		t.Errorf("grain-ci prompt section = %q names wait_for_checks, which its roster does not serve", got)
	}
}

// A task with no repo attached has no branch, no pull request and no CI,
// so the three tools that read one are three names it can do nothing
// with. The package and exit-127 halves are about the sandbox itself and
// hold for every run in one.
func TestSandboxToolingSectionSkipsTheCITalkForATaskWithNoRepo(t *testing.T) {
	t.Parallel()
	got := sandboxToolingSection(false, false)
	for _, unwanted := range []string{"`gh`", "wait_for_checks", "base_branch_status"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("prompt section = %q, want nothing about %q for a task with no repo", got, unwanted)
		}
	}
	for _, want := range []string{"sudo apt-get update", "command -v", "127"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt section = %q, want it to mention %q", got, want)
		}
	}
}

// An interactive run that answered in plain text, calling no tool, has
// answered: somebody asked something the agent could answer from what was
// in front of it. Recording it failed put "That did not get an answer"
// under a real answer (grain/task-1774). A background run that did the
// same, and any run that said nothing at all, still failed.
func TestOutcomeForCountsATextAnswerInAConversation(t *testing.T) {
	t.Parallel()
	text := &agent.Result{FinalText: "It is the second pane."}
	cases := []struct {
		name        string
		interactive bool
		result      *agent.Result
		want        string
	}{
		{"interactive text answer", true, text, "succeeded"},
		{"interactive silence", true, &agent.Result{FinalText: "  "}, "failed"},
		{"background text only", false, text, "failed"},
	}
	for _, c := range cases {
		outcome, detail := outcomeFor(model.Task{Interactive: c.interactive}, c.result)
		if outcome != c.want {
			t.Errorf("%s: outcome = %q (%q), want %q", c.name, outcome, detail, c.want)
		}
	}
}
