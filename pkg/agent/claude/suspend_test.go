package claude

// The cross-dispatch half of retry.go: a run handed the session a
// previous run of the same task left suspended
// (agent.RunConfig.ResumeSession) picks that conversation up instead of
// starting the task's thinking from nothing, and every run hands back
// the session it ends in so the next one can do the same
// (agent.Result.ResumableSession).
//
// retry_test.go covers the same machinery inside one run, where both
// ends of the resume belong to the same Run call. What is different here
// -- and what these tests pin -- is everything that is *not* the same
// across a dispatch: the turn sent is the whole prompt rather than a
// nudge, the sandbox is a new one and the model is told so, and a resume
// the CLI refuses is read as a pruned session rather than as an argv it
// will not take.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// The point of the feature. A dispatch handed a suspended session joins
// it on its very first exec, so the work the previous run did is still
// in the conversation the model is answering from.
func TestRunResumesTheSessionAPreviousDispatchSuspended(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-42"), assistantText("carrying on"), successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), ResumeSession: "sess-42",
	})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("claude was exec'd %d time(s), want 1", len(r.calls))
	}
	if got := argValue(r.calls[0].args, "--resume"); got != "sess-42" {
		t.Errorf("the first attempt resumed %q, want sess-42 -- the session the last run left suspended", got)
	}
	// The whole prompt, unlike the in-run resume: the task may have been
	// edited, a person may have answered the question the last run
	// stopped on, and the conversation cannot know any of it.
	if !strings.Contains(r.calls[0].stdin, "do the thing") {
		t.Errorf("the resumed dispatch's turn = %q, want the task's own prompt in it -- the brief has moved on since that conversation", r.calls[0].stdin)
	}
	if !strings.HasPrefix(r.calls[0].stdin, agent.SuspendedPreamble) {
		t.Errorf("the resumed dispatch's turn = %q, want agent.SuspendedPreamble in front of it: the sandbox is a new one and nothing else says so", r.calls[0].stdin)
	}
	if result.ResumableSession != "sess-42" {
		t.Errorf("ResumableSession = %q, want sess-42 -- the run stayed in the session it was resumed into", result.ResumableSession)
	}
}

// Every ordinary run hands its session back, whether or not it was given
// one: that is what makes a *first* dispatch leave something for the
// second to resume.
func TestRunReportsTheSessionItOpened(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-1"), assistantText("pushed"), successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if got := argValue(r.calls[0].args, "--resume"); got != "" {
		t.Errorf("a first dispatch asked to resume %q, and there was nothing suspended", got)
	}
	if r.calls[0].stdin != "do the thing" {
		t.Errorf("a first dispatch's turn = %q, want the task's prompt alone", r.calls[0].stdin)
	}
	if result.ResumableSession != "sess-1" {
		t.Errorf("ResumableSession = %q, want sess-1", result.ResumableSession)
	}
}

// The case the whole feature is aimed at: a run the wall-clock cap or
// the provider ended still leaves its conversation behind, and a partial
// result is what carries it. A run that only reports its session when it
// succeeds resumes exactly the runs that had nothing left to do.
func TestAFailedRunStillReportsItsSession(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-3"), assistantText("half way through"),
			`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`),
			err: errors.New("exit status 1")},
	}}
	f := newFramework(r, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if result == nil {
		t.Fatal("Run returned no result beside its error, so the run's session went with it")
	}
	if result.ResumableSession != "sess-3" {
		t.Errorf("ResumableSession = %q, want sess-3 -- a run that ended badly is the one worth resuming", result.ResumableSession)
	}
}

// A suspended session the CLI no longer has. grain does not own the
// store those conversations live in and nothing stops it being pruned,
// so the dispatch must fall back to working the task from its prompt
// rather than failing -- and must say in the transcript that it did.
func TestADispatchWhoseSuspendedSessionIsGoneRunsTheTaskAnyway(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: "", err: errors.New("exit status 1 (stderr: No conversation found with session ID: sess-old)")},
		{stdout: stream(systemInit("sess-new"), assistantText("from the top"), successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do the thing", SandboxRoot: t.TempDir(), ResumeSession: "sess-old",
	})
	if err != nil {
		t.Fatalf("Run err = %v, want the fallback to have carried the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("claude was exec'd %d time(s), want 2 (the refused resume, then the task from its prompt)", len(r.calls))
	}
	if got := argValue(r.calls[1].args, "--resume"); got != "" {
		t.Errorf("the fallback still asked to resume %q", got)
	}
	// The plain prompt, not agent.RestartPreamble's: nothing was done in
	// this sandbox, which is what that preamble exists to warn about.
	if r.calls[1].stdin != "do the thing" {
		t.Errorf("the fallback's turn = %q, want the task's prompt alone -- this sandbox is fresh, so there is no earlier attempt's work in it to warn about", r.calls[1].stdin)
	}
	if !strings.Contains(result.Transcript, "could not be resumed") {
		t.Errorf("the transcript does not say the suspended session was gone:\n%s", result.Transcript)
	}
	if result.ResumableSession != "sess-new" {
		t.Errorf("ResumableSession = %q, want sess-new -- the session this run actually opened is the one to keep", result.ResumableSession)
	}
}

// And the refused resume must not cost the run its retries or its turn
// budget, for the reason the in-run fallback does not: no model turn was
// bought with it.
func TestARefusedSuspendedResumeCostsTheRunNoTurns(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: "", err: errors.New("exit status 1 (stderr: No conversation found)")},
		{stdout: stream(systemInit("sess-new"), successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), MaxTurns: 5, ResumeSession: "sess-old",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := argValue(r.calls[1].args, "--max-turns"); got != "5" {
		t.Errorf("the attempt after the refused resume got --max-turns %q, want 5 -- the refused exec ran no model turn", got)
	}
}

// A session grain was told to resume, refused, and then reopened under a
// new id is still worth keeping -- but a build that refuses the *flag*
// is not, and the two have to be told apart. Here claude refuses a
// session it named itself, mid-run, which can only be the argv: the run
// reports no resumable session at all, so the next dispatch is not sent
// back into the same refusal.
func TestARunWhoseBuildRefusesTheResumeFlagReportsNoSession(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-9"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: "", err: errors.New("exit status 1 (stderr: error: unknown option '--resume')")},
		{stdout: stream(systemInit("sess-10"), assistantText("done"), successResult("done", 1))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if result.ResumableSession != "" {
		t.Errorf("ResumableSession = %q, want none: this build will not reopen a session, so handing one to the next dispatch buys it a refused exec", result.ResumableSession)
	}
}
