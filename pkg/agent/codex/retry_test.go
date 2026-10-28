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

package codex

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

// attemptRunner is a fake codex that answers a run's successive execs
// from a script, recording what each one was handed.
//
// recordingRunner replays one canned stream however many times it is
// called, which is exactly what this file must not be tested against:
// the property under test is that the second exec differs from the first
// -- a different prompt on stdin -- and a runner with one answer could
// not tell the difference.
//
// It writes its reply a line at a time and checks cancellation between
// lines, so the turn cap tripping mid-attempt really does stop that
// attempt, the way procgroup.Prepare makes it stop a real codex.
type attemptRunner struct {
	replies []attemptReply
	calls   []attemptCall
	// onCall, when set, runs just before each exec's reply, with that
	// exec's number and the directory it was started in. It is where a
	// test puts what a real codex would have written into the
	// CODEX_HOME it was given -- a thread of its own, which is the thing
	// a home that outlives the run exists to keep (suspend_test.go).
	onCall func(call int, dir string)
}

type attemptReply struct {
	stdout string
	err    error
}

type attemptCall struct {
	args  []string
	stdin string
	dir   string
}

func (r *attemptRunner) Run(ctx context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (string, error) {
	r.calls = append(r.calls, attemptCall{args: slices.Clone(args), stdin: stdin, dir: dir})
	if r.onCall != nil {
		r.onCall(len(r.calls), dir)
	}
	if len(r.calls) > len(r.replies) {
		return "", fmt.Errorf("codex was exec'd %d time(s), and this script has %d reply/replies", len(r.calls), len(r.replies))
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

// withoutRetries turns the in-run retry off altogether -- for a test
// about something other than the loop, whose one scripted reply would
// otherwise be replayed at every attempt.
func withoutRetries() Option {
	return func(f *Framework) { f.retry = agent.RetryPolicy{} }
}

// overloadedTurn is how a provider with no capacity reaches this
// package: a failed turn carrying its own words.
const overloadedTurn = `{"type":"turn.failed","error":{"message":"The server is overloaded or not ready yet."}}`

func attemptStream(lines ...string) string {
	return strings.Join(append([]string{`{"type":"thread.started","thread_id":"t1"}`, `{"type":"turn.started"}`}, lines...), "\n") + "\n"
}

func agentMessage(text string) string {
	return fmt.Sprintf(`{"type":"item.completed","item":{"id":"m-%s","type":"agent_message","text":%q}}`, text, text)
}

// The whole point of retry.go: a run the provider refused mid-flight is
// started again in the sandbox it is already holding, rather than
// costing a dispatch, a granule VM boot, a fresh clone and everything the
// agent had written but not pushed -- and it rejoins the thread the
// first attempt opened rather than starting the task over, which is what
// keeps the model turns already spent as well.
func TestRunResumesTheThreadAfterATransientRefusal(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("cloning"), overloadedTurn), err: errors.New("exit status 1")},
		{stdout: attemptStream(agentMessage("pushed the branch"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	root := t.TempDir()
	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: root})
	if err != nil {
		t.Fatalf("Run err = %v, want the retry to have carried the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("codex was exec'd %d time(s), want 2", len(r.calls))
	}
	if result.FinalText != "pushed the branch" {
		t.Errorf("FinalText = %q, want the second attempt's answer -- the last terminal event is what a run ended as", result.FinalText)
	}
	// The same sandbox, which is the saving this whole file is about.
	if r.calls[1].dir != r.calls[0].dir || r.calls[1].dir == root {
		t.Errorf("the retry ran in %q, want the sandbox the first attempt was working in (%q)", r.calls[1].dir, root)
	}
	// The first attempt opens a thread; the second one is pointed back
	// at it by the id the first announced on its own stream.
	if !argsHave(r.calls[0].args, "exec", "--json") {
		t.Errorf("the first attempt's args = %v, want a plain `codex exec`", r.calls[0].args)
	}
	if slices.Contains(r.calls[0].args, "resume") {
		t.Errorf("the first attempt's args = %v, want no resume: there is no thread yet", r.calls[0].args)
	}
	if !argsHave(r.calls[1].args, "exec", "resume") {
		t.Errorf("the retry's args = %v, want `codex exec resume`", r.calls[1].args)
	}
	if !argsHave(r.calls[1].args, "t1", "-") {
		t.Errorf("the retry's args = %v, want the first attempt's thread id (t1) as resume's positional argument, with the stdin \"-\" after it", r.calls[1].args)
	}
	// The nudge rather than the task, since the thread it is rejoining
	// already has the prompt and everything the first attempt did.
	if r.calls[1].stdin != agent.ResumePrompt {
		t.Errorf("the retry's prompt = %q, want agent.ResumePrompt -- repeating the task would read as \"start again\" to a model that had already got half way", r.calls[1].stdin)
	}
	if r.calls[0].stdin != "do the thing" {
		t.Errorf("the first attempt's prompt = %q, want the task's prompt alone", r.calls[0].stdin)
	}
	for _, want := range []string{"cloning", "pushed the branch", "resumed the same conversation in the same sandbox"} {
		if !strings.Contains(result.Transcript, want) {
			t.Errorf("Transcript missing %q:\n%s", want, result.Transcript)
		}
	}
}

// The fallback the measurement did not remove: a codex that will not
// perform the resume invocation this package asks for writes nothing to
// stdout at all -- no `thread.started`, which every exec that starts
// announces before it so much as authenticates. That is read as the
// binary refusing the argv rather than as anything about this run, so
// the run drops the resume for good and starts the task again in the
// same sandbox: the variant this package ran on before codex's resume
// was measured, and the one a future codex that drops the subcommand
// degrades to.
func TestARefusedResumeFallsBackToRestartingTheTask(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("cloning"), overloadedTurn), err: errors.New("exit status 1")},
		// codex parsed the argv, disliked it, and exited without
		// streaming a line.
		{stdout: "", err: errors.New("exit status 2")},
		{stdout: attemptStream(agentMessage("pushed the branch"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	root := t.TempDir()
	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: root})
	if err != nil {
		t.Fatalf("Run err = %v, want the fallback to have carried the run", err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("codex was exec'd %d time(s), want 3 -- the refused resume, then the same attempt restarted from the prompt", len(r.calls))
	}
	// The third exec is the second *attempt*: a resume codex would not
	// perform spent no model turn, so it must not have spent one of the
	// run's two retries either.
	if slices.Contains(r.calls[2].args, "resume") {
		t.Errorf("the fallback's args = %v, want no resume: this run has stopped asking for one", r.calls[2].args)
	}
	if r.calls[2].dir != r.calls[0].dir || r.calls[2].dir == root {
		t.Errorf("the fallback ran in %q, want the same sandbox (%q)", r.calls[2].dir, root)
	}
	if !strings.HasPrefix(r.calls[2].stdin, agent.RestartPreamble) {
		t.Errorf("the fallback's prompt = %q, want agent.RestartPreamble in front of the task's own prompt", r.calls[2].stdin)
	}
	if !strings.Contains(r.calls[2].stdin, "do the thing") {
		t.Errorf("the fallback's prompt = %q, want the task's own prompt back", r.calls[2].stdin)
	}
	// And the transcript says it happened, because the person reading it
	// has to decide whether this package should stop asking to resume.
	if !strings.Contains(result.Transcript, "refused the invocation resuming this run's thread") {
		t.Errorf("Transcript does not say the resume was refused:\n%s", result.Transcript)
	}
}

// A run that is refused twice asks to resume once and then stops: the
// second refusal comes back from a resumed exec that *did* open a thread,
// so nothing about it says the argv was wrong, and the run goes on
// resuming rather than falling back.
func TestASecondRefusalStillResumes(t *testing.T) {
	refused := attemptReply{stdout: attemptStream(overloadedTurn), err: errors.New("exit status 1")}
	r := &attemptRunner{replies: []attemptReply{refused, refused, refused}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("Run err = nil, want the capacity refusal the run ended on")
	}
	if want := 1 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Fatalf("codex was exec'd %d time(s), want %d", len(r.calls), want)
	}
	for _, i := range []int{1, 2} {
		if !argsHave(r.calls[i].args, "exec", "resume") {
			t.Errorf("attempt %d's args = %v, want `codex exec resume`", i+1, r.calls[i].args)
		}
	}
}

// An attempt that died before codex named a thread leaves nothing to
// resume, so the retry starts the task again rather than passing an
// empty id -- which the measurement says codex answers with "no rollout
// found", costing an exec for nothing.
func TestARetryWithNoThreadToResumeRestartsTheTask(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		// No thread.started: a codex killed, or refused, before it ever
		// opened one.
		{stdout: overloadedTurn + "\n", err: errors.New("exit status 1")},
		{stdout: attemptStream(agentMessage("done"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run err = %v, want the retry to have carried the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("codex was exec'd %d time(s), want 2", len(r.calls))
	}
	if slices.Contains(r.calls[1].args, "resume") {
		t.Errorf("the retry's args = %v, want no resume: the first attempt named no thread", r.calls[1].args)
	}
	if !strings.HasPrefix(r.calls[1].stdin, agent.RestartPreamble) {
		t.Errorf("the retry's prompt = %q, want agent.RestartPreamble in front of the task's own prompt", r.calls[1].stdin)
	}
}

// The live transcript mirror is opened O_TRUNC once and reused, so a
// retry appends to what the run had already streamed instead of erasing
// it -- the file orchestrator renders a still-running run from.
func TestTheTranscriptMirrorSurvivesARetry(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("first attempt"), overloadedTurn), err: errors.New("exit status 1")},
		{stdout: attemptStream(agentMessage("second attempt"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	path := filepath.Join(t.TempDir(), "run.jsonl")
	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TranscriptPath: path,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mirrored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the mirror: %v", err)
	}
	for _, want := range []string{"first attempt", "second attempt"} {
		if !strings.Contains(string(mirrored), want) {
			t.Errorf("the mirror lost %q:\n%s", want, mirrored)
		}
	}
}

// RunConfig.MaxTurns is a ceiling on the run, not on each exec of codex:
// a run that spent one of its two turns and was then refused gets one
// more, not two.
func TestTheTurnCapStaysCumulativeAcrossAttempts(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("one"), overloadedTurn), err: errors.New("exit status 1")},
		{stdout: attemptStream(agentMessage("two"), agentMessage("three"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 2})
	if err == nil || !strings.Contains(err.Error(), "exceeded max turns") {
		t.Fatalf("Run err = %v, want the cap to have counted the first attempt's turn too", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("codex was exec'd %d time(s), want 2", len(r.calls))
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

// Only a capacity refusal is retried. An ordinary failure repeated is an
// ordinary failure that cost three times as much, and a quota refusal
// belongs to dispatch's pause rather than to a 30-second backoff.
//
// And a run that *finished* is never retried, whatever its stream
// carries. codex reports every attempt of a transport reconnect as an
// error event of its own -- with the provider's HTTP status in it -- and
// a run that recovers goes on to succeed: reading one of those as a
// refusal would start a finished run over.
func TestRunRetriesNothingButACapacityRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
		err    error
	}{
		{"an ordinary failure", attemptStream(`{"type":"turn.failed","error":{"message":"the tests failed"}}`),
			errors.New("exit status 1")},
		{"a quota refusal", attemptStream(`{"type":"turn.failed","error":{"message":"429 You exceeded your current quota"}}`),
			errors.New("exit status 1")},
		{"a run that recovered from a 503", attemptStream(
			`{"type":"error","message":"Reconnecting... 1/5 (unexpected status 503 Service Unavailable)"}`,
			agentMessage("back, and done"), `{"type":"turn.completed"}`), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &attemptRunner{replies: []attemptReply{
				{stdout: tc.stdout, err: tc.err},
				{stdout: tc.stdout, err: tc.err},
			}}
			f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())
			f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
			if len(r.calls) != 1 {
				t.Errorf("codex was exec'd %d time(s) for %s, want 1", len(r.calls), tc.name)
			}
		})
	}
}

// The same run, read the other way round: a stream whose reconnect
// error quotes a provider's 503 and then finishes is a successful run,
// not a capacity refusal and not a usage limit. Reading it as either
// would report a run that worked as one that never happened -- and, for
// a limit, would pause the whole deployment on it.
func TestARunThatRecoveredIsNotAProviderRefusal(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{{stdout: attemptStream(
		`{"type":"error","message":"Reconnecting... 1/5 (unexpected status 503 Service Unavailable)"}`,
		agentMessage("back, and done"), `{"type":"turn.completed"}`)}}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want a successful run", err)
	}
	if result.FinalText != "back, and done" {
		t.Errorf("FinalText = %q", result.FinalText)
	}
}

// The retry budget is two, and a provider still refusing after that is
// not having a blip: the task goes back to the queue, where dispatch's
// own backoff -- minutes, not seconds -- can wait it out without holding
// a sandbox open the whole time.
func TestRunStopsRetryingAfterItsBudget(t *testing.T) {
	refused := attemptReply{stdout: attemptStream(overloadedTurn), err: errors.New("exit status 1")}
	r := &attemptRunner{replies: []attemptReply{refused, refused, refused, refused}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.Capacity(err); !ok {
		t.Fatalf("Run err = %v, want the capacity refusal to be what the run ends on", err)
	}
	if want := 1 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Errorf("codex was exec'd %d time(s), want %d", len(r.calls), want)
	}
}

// A retry has to fit inside the runtime grain gave this run. One that
// would be cancelled a minute after it started has spent the backoff and
// a model call to end up exactly where it began.
func TestRunDoesNotRetryWithoutTheRuntimeToDoIt(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(overloadedTurn), err: errors.New("exit status 1")},
		{stdout: attemptStream(agentMessage("done"), `{"type":"turn.completed"}`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.Run(ctx, agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("Run err = nil, want the capacity refusal")
	}
	if len(r.calls) != 1 {
		t.Errorf("codex was exec'd %d time(s) with five seconds of runtime left, want 1", len(r.calls))
	}
}

// A multi-attempt capture holds a thread per attempt, and what the run
// ended as is what the last one says. Without this a run that was
// refused and then finished would be handed back to the caller as the
// refusal -- and one that failed twice would be reported with the wrong
// failure.
func TestParseTranscriptTakesTheLastAttemptsEnding(t *testing.T) {
	got, err := parseTranscript(attemptStream(agentMessage("first go"), overloadedTurn) +
		attemptStream(agentMessage("second go"), `{"type":"turn.completed"}`))
	if err != nil {
		t.Fatalf("parseTranscript: %v -- the last attempt's ending is what the run ended as", err)
	}
	if got.FinalText != "second go" {
		t.Errorf("FinalText = %q, want the last attempt's answer", got.FinalText)
	}

	_, err = parseTranscript(attemptStream(overloadedTurn) +
		attemptStream(`{"type":"turn.failed","error":{"message":"the tests failed"}}`))
	if err == nil || !strings.Contains(err.Error(), "the tests failed") {
		t.Errorf("err = %v, want the last attempt's own account of the failure", err)
	}
}
