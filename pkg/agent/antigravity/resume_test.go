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

package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// attemptRunner is a fake agy that answers a run's successive execs from
// a script, recording what each one was handed.
//
// recordingRunner replays one canned stream however many times it is
// called, which is exactly what this file must not be tested against:
// the property under test is that the second exec differs from the first
// -- a different argv, a different turn on stdin -- and a runner with one
// answer could not tell the difference.
//
// It writes its reply a line at a time and checks cancellation between
// lines, so the turn cap tripping mid-attempt really does stop that
// attempt, the way procgroup.Prepare makes it stop a real agy.
type attemptRunner struct {
	replies []attemptReply
	calls   []attemptCall
	// afterRead, when set, runs once each attempt has been handed its
	// HOME -- how a test stands in for agy's own habit of rewriting part
	// of the configuration it just read (refreshAgyHome).
	afterRead func(home string)
	// settingsSeen is agy's settings file as each attempt found it, and
	// conversationsSeen whether the directory agy keeps its
	// conversations in was still there.
	settingsSeen      []string
	conversationsSeen []bool
}

// agyConversationsDir is where agy keeps a session under the HOME it was
// started with (docs/agy-surface.md's own listing). Nothing in this
// package writes it; the tests below plant a file there to prove a
// retried run does not take it away.
const agyConversationsDir = ".gemini/antigravity-cli/conversations"

type attemptReply struct {
	stdout string
	err    error
}

type attemptCall struct {
	args  []string
	stdin string
	env   []string
	dir   string
}

func (r *attemptRunner) Run(ctx context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (string, error) {
	r.calls = append(r.calls, attemptCall{args: slices.Clone(args), stdin: stdin, env: slices.Clone(env), dir: dir})
	if home := homeOf(env); home != "" {
		settings, _ := os.ReadFile(filepath.Join(home, cliSettingsRelPath))
		r.settingsSeen = append(r.settingsSeen, string(settings))
		_, err := os.Stat(filepath.Join(home, agyConversationsDir))
		r.conversationsSeen = append(r.conversationsSeen, err == nil)
		if r.afterRead != nil {
			r.afterRead(home)
		}
	}
	if len(r.calls) > len(r.replies) {
		return "", fmt.Errorf("agy was exec'd %d time(s), and this script has %d reply/replies", len(r.calls), len(r.replies))
	}
	reply := r.replies[len(r.calls)-1]
	var written strings.Builder
	for _, line := range strings.SplitAfter(reply.stdout, "\n") {
		if line == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return written.String(), err
		}
		written.WriteString(line)
		if tee != nil {
			io.WriteString(tee, line)
		}
	}
	return written.String(), reply.err
}

// withoutBackoff installs a sleep that returns at once, so a test of the
// retry loop costs the suite no wall clock. Nothing else about the loop
// changes: the same waits are still asked for, they just do not happen.
func withoutBackoff() Option {
	return func(f *Framework) {
		f.retry.Sleep = func(context.Context, time.Duration) error { return nil }
	}
}

// conversationInit is agy's opening event with the session id it carries
// in a real capture (docs/agy-surface.md).
func conversationInit(id string) string {
	return `{"event":"init","conversation_id":"` + id +
		`","init":{"cwd":"/w","tools":["call_mcp_tool"],"permission_mode":"always-proceed"}}`
}

// overloadedResult is how the Gemini API's 503 reaches this package: a
// failed terminal status carrying the provider's own words.
const overloadedResult = `{"event":"result","result":{"status":"FAILURE","error":"[503 Service Unavailable] The model is overloaded. Please try again later."}}`

func successResult(text string) string {
	return `{"event":"result","result":{"status":"SUCCESS","response":"` + text + `"}}`
}

func homeOf(env []string) string {
	for _, e := range env {
		if home, ok := strings.CutPrefix(e, "HOME="); ok {
			return home
		}
	}
	return ""
}

// The whole point of resume.go: a run the provider refused mid-flight is
// picked up where it stopped -- same sandbox, same private HOME, same agy
// conversation -- instead of costing a dispatch, a sandbox rebuild and
// every model turn already spent.
func TestRunResumesTheConversationAfterATransientRefusal(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(
			conversationInit("conv-7"),
			toolActive(0, "run_command", `{"command":"git clone"}`),
			toolDone(0, "run_command", "cloned"),
			overloadedResult,
		), err: errors.New("exit status 1")},
		{stdout: stream(
			conversationInit("conv-7"),
			agentTurn(1, "pushed the branch"),
			successResult("pushed the branch"),
		)},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())
	transcriptPath := filepath.Join(t.TempDir(), "agy-stream.jsonl")

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt:         "do the thing",
		SandboxRoot:    t.TempDir(),
		TranscriptPath: transcriptPath,
	})
	if err != nil {
		t.Fatalf("Run err = %v, want the resumed attempt to carry the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want 2", len(r.calls))
	}

	if got := flagValue(r.calls[0].args, "--conversation"); got != "" {
		t.Errorf("the first attempt asked to resume %q; there was nothing to resume yet", got)
	}
	if got := flagValue(r.calls[1].args, "--conversation"); got != "conv-7" {
		t.Errorf("the retry resumed conversation %q, want conv-7 -- the id agy put on its own stream", got)
	}

	// The retry must not re-send the prompt: the conversation it is
	// joining already has it, and repeating it reads as "start over" to
	// a model that had already got half way.
	first, err := promptFromStdin(r.calls[0].stdin)
	if err != nil || !strings.Contains(first, "do the thing") {
		t.Fatalf("the first attempt's turn = %q (%v), want the task's own prompt", first, err)
	}
	second, err := promptFromStdin(r.calls[1].stdin)
	if err != nil {
		t.Fatalf("decoding the resumed attempt's turn: %v", err)
	}
	if strings.Contains(second, "do the thing") {
		t.Errorf("the resumed attempt sent the prompt again (%q): the conversation already has it", second)
	}
	if !strings.Contains(second, "Carry on from where you stopped") {
		t.Errorf("the resumed attempt's turn = %q, want it to say the interruption was not the agent's doing", second)
	}

	// Same sandbox, same HOME -- which is what makes resuming possible
	// at all, agy keeping its conversations under the HOME it was
	// started with.
	if r.calls[0].dir != r.calls[1].dir {
		t.Errorf("the retry started in %q, the first attempt in %q: it must be the same sandbox", r.calls[1].dir, r.calls[0].dir)
	}
	if h0, h1 := homeOf(r.calls[0].env), homeOf(r.calls[1].env); h0 == "" || h0 != h1 {
		t.Errorf("HOME = %q then %q: the retry must reuse the first attempt's private HOME", h0, h1)
	}

	// What the first attempt actually did survives into the Result: a
	// tool call grain has to process is not less real for the provider
	// having refused the turn after it.
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "run_command" {
		t.Errorf("ToolCalls = %+v, want the first attempt's own call", result.ToolCalls)
	}
	if result.FinalText != "pushed the branch" {
		t.Errorf("FinalText = %q, want the resumed attempt's answer", result.FinalText)
	}

	// And the raw mirror holds both attempts: it is opened O_TRUNC once,
	// so a retry that reopened it would have thrown the first away.
	mirror, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(mirror), `"event":"init"`); n != 2 {
		t.Errorf("the transcript mirror holds %d init event(s), want one per attempt", n)
	}
	if !strings.Contains(string(mirror), "overloaded") {
		t.Errorf("the transcript mirror lost the refusal that caused the retry:\n%s", mirror)
	}
}

// The retry budget is bounded, and running out of it leaves the run
// ending exactly as it did before this file existed: as the provider's
// doing, which is what keeps it off the task's failure streak.
func TestRunGivesUpAfterItsRetryBudgetAndStillReportsCapacity(t *testing.T) {
	refused := attemptReply{
		stdout: stream(conversationInit("conv-1"), overloadedResult),
		err:    errors.New("exit status 1"),
	}
	// One more reply than the budget allows, so an over-eager loop fails
	// this test rather than erroring on an exhausted script.
	r := &attemptRunner{replies: []attemptReply{refused, refused, refused, refused}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.Capacity(err); !ok {
		t.Fatalf("Run err = %v, want the capacity refusal to survive the retries", err)
	}
	if want := 1 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Errorf("agy was exec'd %d time(s), want %d", len(r.calls), want)
	}
}

// RunConfig.MaxTurns is a ceiling on the run, not on each exec of agy:
// a run that spent one of its two turns and was then refused gets one
// more, not two.
func TestTheTurnCapStaysCumulativeAcrossAttempts(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("c"), agentTurn(0, "one"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("c"), agentTurn(1, "two"), agentTurn(2, "three"), successResult("done"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 2})
	if err == nil || !strings.Contains(err.Error(), "exceeded max turns") {
		t.Fatalf("Run err = %v, want the cap to have counted the first attempt's turn too", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want 2", len(r.calls))
	}
	if result == nil {
		t.Fatal("Run returned no Result at all, want what the run managed before the cap stopped it")
	}
	// The cap cancelled the second attempt's own context, which is the
	// half of this that a single shared cancel would have got wrong:
	// cancelling the first attempt's context stops nothing.
	if strings.Contains(result.Transcript, "three") {
		t.Errorf("the cap did not stop the second attempt; transcript:\n%s", result.Transcript)
	}
}

// The belt and braces for the one thing no test in this repository can
// prove without a live binary: that --conversation composes with
// --input-format stream-json. An exec that passed the flag and died
// without agy opening a session at all is read as the argv being
// refused, and the run falls back to the cheap variant -- start again
// from the prompt, in the same sandbox -- rather than turning a provider
// blip into a hard failure. See resume.go, and tests/e2e's
// TestLiveAgyResumesAConversationAcrossExecs for the proof itself.
func TestRunRetriesFromScratchWhenAgyRefusesToResume(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-9"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: "", err: errors.New("exit status 2 (stderr: unknown flag: --conversation)")},
		{stdout: stream(conversationInit("conv-10"), agentTurn(0, "done"), successResult("done"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want the fresh retry to carry the run", err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("agy was exec'd %d time(s), want 3 (the refusal, the rejected resume, the fresh retry)", len(r.calls))
	}
	if got := flagValue(r.calls[2].args, "--conversation"); got != "" {
		t.Errorf("the fresh retry still asked to resume %q after agy refused the flag", got)
	}
	turn, err := promptFromStdin(r.calls[2].stdin)
	if err != nil || !strings.Contains(turn, "do the thing") {
		t.Errorf("the fresh retry's turn = %q (%v), want the task's own prompt back", turn, err)
	}
	if !strings.Contains(result.Transcript, "refused the invocation") {
		t.Errorf("the transcript says nothing about the refused resume, which is the one thing a maintainer has to see:\n%s", result.Transcript)
	}
}

// A rejected resume must not spend a retry: no model turn was bought
// with it, so the run still has both of its attempts at the provider.
func TestARefusedResumeCostsTheRunNoRetry(t *testing.T) {
	refused := attemptReply{
		stdout: stream(conversationInit("conv-1"), overloadedResult),
		err:    errors.New("exit status 1"),
	}
	rejected := attemptReply{stdout: "", err: errors.New("exit status 2 (stderr: unknown flag)")}
	r := &attemptRunner{replies: []attemptReply{refused, rejected, refused, refused, refused}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("Run err = nil, want the capacity refusal")
	}
	// The rejected resume, plus the three attempts the budget allows.
	if want := 2 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Errorf("agy was exec'd %d time(s), want %d", len(r.calls), want)
	}
}

// Only a capacity refusal is retried. An ordinary failure repeated is an
// ordinary failure that cost three times as much, and a quota refusal
// belongs to dispatch's pause rather than to a 30-second backoff.
func TestRunRetriesNothingButACapacityRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
	}{
		{"an ordinary failure", stream(conversationInit("c"),
			`{"event":"result","result":{"status":"FAILURE","error":"the tests failed"}}`)},
		{"a quota refusal", stream(conversationInit("c"),
			`{"event":"result","result":{"status":"FAILURE","error":"429 RESOURCE_EXHAUSTED: You exceeded your current quota"}}`)},
		{"a run that finished", stream(conversationInit("c"), agentTurn(0, "hi"), successResult("hi"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &attemptRunner{replies: []attemptReply{
				{stdout: tc.stdout, err: errors.New("exit status 1")},
				{stdout: tc.stdout},
			}}
			f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())
			f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
			if len(r.calls) != 1 {
				t.Errorf("agy was exec'd %d time(s) for %s, want 1", len(r.calls), tc.name)
			}
		})
	}
}

// A retry has to fit inside the runtime grain gave this run. One that
// would be cancelled a minute after it started has spent the backoff and
// a model call to end up exactly where it began.
func TestRunDoesNotRetryWithoutTheRuntimeToDoIt(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("c"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("c"), successResult("done"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.Run(ctx, agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("Run err = nil, want the capacity refusal")
	}
	if len(r.calls) != 1 {
		t.Errorf("agy was exec'd %d time(s) with five seconds of runtime left, want 1", len(r.calls))
	}
}

// The parser has to find the session id wherever agy writes it: at the
// top level of the init event, and nested inside the step_update and
// result payloads (docs/agy-surface.md's own capture).
func TestParseEventsReadsAgysConversationID(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"the init event", `{"event":"init","conversation_id":"abc","init":{"tools":[]}}`},
		{"a step_update", `{"event":"step_update","step_update":{"conversation_id":"abc","step_index":0,"state":"DONE","step_type":"agent_response"}}`},
		{"the result event", `{"event":"result","result":{"conversation_id":"abc","status":"SUCCESS","response":"hi"}}`},
	} {
		if got := parseEvents(stream(tc.line)).conversationID; got != "abc" {
			t.Errorf("conversationID off %s = %q, want abc", tc.name, got)
		}
	}
	if got := parseEvents(stream(initLine)).conversationID; got != "" {
		t.Errorf("conversationID off a capture carrying none = %q, want empty", got)
	}
}

// A multi-attempt capture holds a result event per attempt, and what the
// run ended as is what the last one says. Without this a run that was
// refused and then finished would be handed back to the caller as the
// refusal.
func TestParseTranscriptTakesTheLastResultEvent(t *testing.T) {
	got, err := parseTranscript(stream(
		conversationInit("c"),
		overloadedResult,
		conversationInit("c"),
		agentTurn(0, "second time lucky"),
		successResult("second time lucky"),
	))
	if err != nil {
		t.Fatalf("parseTranscript err = %v, want the later success to win", err)
	}
	if got.FinalText != "second time lucky" {
		t.Errorf("FinalText = %q, want the last result event's answer", got.FinalText)
	}
}

// And the other direction: a run that finished and was then refused --
// which cannot happen today, the loop stopping at the first success --
// must not read as a success. Last result wins means last result wins.
func TestParseTranscriptDoesNotLetAnEarlierSuccessHideALaterFailure(t *testing.T) {
	if _, err := parseTranscript(stream(successResult("done"), overloadedResult)); err == nil {
		t.Error("parseTranscript err = nil, want the last result event's failure")
	}
}

// The trap a second launch in one HOME walks into, and the reason
// refreshAgyHome exists: agy rewrites settings.json as it starts,
// keeping the keys it owns and dropping the permissions block grain
// wrote. A retry given the file agy left behind would run with none of
// this run's own rules and say nothing about it.
//
// It is also the assertion that the refresh does not take the
// conversation with it -- rewriting the HOME wholesale would leave
// --conversation naming a session that is no longer on disk, which is
// the failure this whole file exists to avoid.
func TestARetryGetsThisRunsConfigurationBack(t *testing.T) {
	r := &attemptRunner{
		replies: []attemptReply{
			{stdout: stream(conversationInit("conv-2"), overloadedResult), err: errors.New("exit status 1")},
			{stdout: stream(conversationInit("conv-2"), successResult("done"))},
		},
		afterRead: func(home string) {
			// agy 1.1.26's own rewrite, measured in
			// tests/e2e/live_hook_config_test.go.
			if err := os.WriteFile(filepath.Join(home, cliSettingsRelPath), []byte(`{"modelProvider":"gemini"}`), 0o600); err != nil {
				t.Error(err)
			}
			// And the conversation it wrote while it was running.
			if err := os.MkdirAll(filepath.Join(home, agyConversationsDir), 0o700); err != nil {
				t.Error(err)
			}
		},
	}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff(), WithAPIKey("k"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run err = %v, want the retry to carry the run", err)
	}
	if len(r.settingsSeen) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want 2", len(r.settingsSeen))
	}
	for i, settings := range r.settingsSeen {
		if !strings.Contains(settings, "permissions") {
			t.Errorf("attempt %d started on settings with no permissions block (%s): "+
				"a retry must be handed this run's own configuration, not what the last launch left", i+1, settings)
		}
		if !strings.Contains(settings, "gemini") {
			t.Errorf("attempt %d started on settings that do not ask agy to use the API key (%s)", i+1, settings)
		}
	}
	if r.conversationsSeen[0] {
		t.Error("the first attempt found a conversations directory this test had not planted yet")
	}
	if !r.conversationsSeen[1] {
		t.Error("the retry found no conversations directory: refreshing the HOME took the session it was about to resume")
	}
}

// The other half of resume.go, and the half the failing dispatches of
// 2026-09-15 needed: agy ending a run in its own ERROR status is not a
// verdict on the task, so the run is picked up where it stopped rather
// than handed back for a fresh sandbox to start again from nothing.
func TestRunResumesTheConversationAfterAgyEndsInError(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(
			conversationInit("conv-3"),
			toolActive(0, "run_command", `{"command":"git push"}`),
			toolDone(0, "run_command", "pushed"),
			genericAgyFailure,
		), err: errors.New("exit status 1")},
		{stdout: stream(
			conversationInit("conv-3"),
			agentTurn(1, "opened the pull request"),
			successResult("opened the pull request"),
		)},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want the resumed attempt to carry the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want 2", len(r.calls))
	}
	if got := flagValue(r.calls[1].args, "--conversation"); got != "conv-3" {
		t.Errorf("the second attempt resumed conversation %q, want conv-3", got)
	}

	// And it is told the truth about why it stopped. Nothing here
	// measured a provider at all -- agy's sentence names no cause -- so
	// the turn a refusal sends would be a claim this package cannot make.
	turn, err := promptFromStdin(r.calls[1].stdin)
	if err != nil {
		t.Fatalf("decoding the resumed attempt's turn: %v", err)
	}
	if strings.Contains(turn, "do the thing") {
		t.Errorf("the resumed attempt sent the prompt again (%q): the conversation already has it", turn)
	}
	if strings.Contains(turn, "provider refused") {
		t.Errorf("the resumed attempt was told the provider refused it (%q); agy said no such thing", turn)
	}
	if !strings.Contains(turn, "stopped mid-run") {
		t.Errorf("the resumed attempt's turn = %q, want it to say the CLI itself stopped", turn)
	}

	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "run_command" {
		t.Errorf("ToolCalls = %+v, want the interrupted attempt's own call", result.ToolCalls)
	}
	if !strings.Contains(result.Transcript, "cut off by agy ending the run in status ERROR") {
		t.Errorf("the transcript does not say why the run appears to restart half way through:\n%s", result.Transcript)
	}
}

// An interruption is only ever turned into a resume. With no
// conversation to join, starting the task again from its prompt is a
// guess about a failure nobody could classify -- and dispatch makes that
// same guess anyway, with a clean sandbox behind it.
func TestAnInterruptionWithNoConversationToResumeIsReported(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(initLine, genericAgyFailure), err: errors.New("exit status 1")},
		{stdout: okStream()},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("Run err = nil, want agy's own failure")
	}
	if len(r.calls) != 1 {
		t.Errorf("agy was exec'd %d time(s) with no conversation to resume, want 1", len(r.calls))
	}
}

// The resumes come out of the retry budget the refusals spend, and stop
// where it does: past two of these the run is better off back in the
// queue, where dispatch's own backoff can wait without holding a sandbox.
func TestInterruptionsSpendTheSameBoundedBudget(t *testing.T) {
	interrupted := attemptReply{
		stdout: stream(conversationInit("conv-4"), genericAgyFailure),
		err:    errors.New("exit status 1"),
	}
	// One more reply than the budget allows, so an over-eager loop fails
	// this test rather than erroring on an exhausted script.
	r := &attemptRunner{replies: []attemptReply{interrupted, interrupted, interrupted, interrupted}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "Agent execution terminated due to error") {
		t.Fatalf("Run err = %v, want agy's own last failure", err)
	}
	if want := 1 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Errorf("agy was exec'd %d time(s), want %d", len(r.calls), want)
	}
}

// What separates an interruption from a verdict: FAILURE is the status a
// classified refusal arrives under, so a FAILURE this package cannot
// classify is one it cannot tell from a run that ended on its own terms.
// It ends where it did, conversation or no conversation.
func TestAnUnclassifiedFailureStatusIsNotResumed(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-5"),
			`{"event":"result","result":{"status":"FAILURE","error":"the tests failed"}}`), err: errors.New("exit status 1")},
		{stdout: okStream()},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("Run err = nil, want the failure agy reported")
	}
	if len(r.calls) != 1 {
		t.Errorf("agy was exec'd %d time(s) for an unclassified FAILURE, want 1", len(r.calls))
	}
}

// The belt and braces for the premise this whole file rests on, in the
// one case that has no cheap variant behind it. If agy will not take
// --conversation at all, an interruption costs one extra exec -- an
// instant one, spending no model turn -- and then ends exactly as it
// would have without any of this.
func TestAnInterruptionEndsWhereItStoppedWhenAgyRefusesToResume(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-6"), genericAgyFailure), err: errors.New("exit status 1")},
		{stdout: "", err: errors.New("exit status 2 (stderr: unknown flag: --conversation)")},
		{stdout: okStream()},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure the resume could not pick up")
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want the refused resume and no restart from the top", len(r.calls))
	}
	if !strings.Contains(result.Transcript, "ends where it stopped instead") {
		t.Errorf("the transcript says nothing about the refused resume, which is the one thing a maintainer has to see:\n%s", result.Transcript)
	}
}

// When agy resumes a conversation (--conversation <id>) that already holds an
// ERROR step from an earlier attempt (such as a stream idle timeout), its
// terminal result event still reports status ERROR with that historical step's
// error and exits 1 even after the resumed attempt completes its own
// agent_response step and populates result.response. Treating that terminal
// event as a fresh interruption loops through the remaining retry budget and
// discards comment_on_issue / ask_question in afterAgentRun.
func TestRunCompletesWhenResumedConversationStillReportsEarlierErrorStep(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(
			conversationInit("conv-err"),
			`{"event":"result","result":{"conversation_id":"conv-err","status":"ERROR","response":"","error":"stream idle timeout"}}`,
		), err: errors.New("exit status 1")},
		{stdout: stream(
			conversationInit("conv-err"),
			toolActive(1, "mcp_grain-sandbox_comment_on_issue", `{"comment":"diagnosed"}`),
			toolDone(1, "mcp_grain-sandbox_comment_on_issue", "Recorded."),
			agentTurn(2, "Posted the diagnosis."),
			`{"event":"result","result":{"conversation_id":"conv-err","status":"ERROR","response":"Posted the diagnosis.","error":"stream idle timeout"}}`,
		), err: errors.New("exit status 1")},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "triage", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want nil when the resumed attempt completed its turn", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want 2 (no extra retry after completed turn)", len(r.calls))
	}
	if result.FinalText != "Posted the diagnosis." {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "Posted the diagnosis.")
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "comment_on_issue" {
		t.Errorf("ToolCalls = %+v, want comment_on_issue call preserved", result.ToolCalls)
	}
}
