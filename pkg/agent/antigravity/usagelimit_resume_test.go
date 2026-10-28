package antigravity

// The seam between resume.go's loop and usagelimit.go's reading of a
// refusal. resume.go picks a run back up on two quite different endings
// -- a provider that refused it for want of capacity, and an attempt
// that was simply cut off without reaching a verdict -- and both of them
// have to give way to the one ending that is neither: the credential
// running out of quota, which is hours from clearing and belongs to
// dispatch's pause rather than to a 30-second backoff.
//
// resume_test.go covers a quota refusal on the *first* attempt, under
// the FAILURE status agy gives a classified refusal, and
// diagnostics_test.go covers the shape agy 1.1.27 actually produced: the
// generic ERROR sentence on the stream with the API's RESOURCE_EXHAUSTED
// in the private log. What is left, and is what a deployment meets once
// resuming works, is the same wall met by an attempt that was *already*
// a resume -- and the ERROR status read off the stream, which is the one
// status the interruption path would otherwise pick back up.
//
// The same reading decides the other ending a multi-attempt capture can
// be misread into -- a capacity refusal that belonged to an earlier
// attempt -- and that matters more here than next door, because this is
// the loop that resumes an attempt which produced no terminal event at
// all. So those are here too: the same question asked of the same seam.
//
// orchestrator/usagelimit_test.go has what the error asserted on here
// does to dispatch; agent/claude and agent/codex have files of this name
// for the two CLIs that spell the same refusal differently.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// quotaResult is the Gemini API's own refusal as agy classifies it: a
// FAILURE status carrying RESOURCE_EXHAUSTED and the RetryInfo delay
// that becomes agent.UsageLimitError.RetryAfter.
const quotaResult = `{"event":"result","result":{"status":"FAILURE","error":"[429 RESOURCE_EXHAUSTED] ` +
	`You exceeded your current quota. Details: [{\"retryDelay\":\"56s\"}]"}}`

// quotaUnderErrorStatus is the same refusal reaching the stream under
// ERROR -- the status resume.go reads as an interruption and picks back
// up. It must not pick this one up: the words are the provider's, and
// they say the account has nothing left to spend.
const quotaUnderErrorStatus = `{"event":"result","result":{"status":"ERROR","error":"[429 RESOURCE_EXHAUSTED] ` +
	`You exceeded your current quota."}}`

// The case this file exists for: the resumed attempt is the one that
// meets the wall, and what comes back is the limit it met rather than
// the capacity refusal that sent the run there.
func TestAQuotaRefusalOnAResumedConversationEndsTheRunAsALimit(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-7"),
			toolActive(0, "run_command", `{"command":"git push"}`),
			toolDone(0, "run_command", "pushed"),
			overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("conv-7"), quotaResult), err: errors.New("exit status 1")},
		// One more than the run should ever ask for, so a loop that
		// resumed through the limit fails here rather than erroring on
		// an exhausted script.
		{stdout: stream(conversationInit("conv-7"), quotaResult), err: errors.New("exit status 1")},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want the resumed attempt's usage limit", err)
	}
	if limit.Framework != usageLimitFramework {
		t.Errorf("Framework = %q, want %q -- a pause is scoped to the credential that met the limit", limit.Framework, usageLimitFramework)
	}
	if limit.RetryAfter != 56*time.Second {
		t.Errorf("RetryAfter = %s, want 56s -- the API's own retryDelay off the attempt that met the limit", limit.RetryAfter)
	}
	if _, ok := agent.Capacity(err); ok {
		t.Errorf("Run err = %v, want the limit rather than the refusal that sent the run to it", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want 2 -- the refusal, then the resume that met the limit", len(r.calls))
	}
	if got := flagValue(r.calls[1].args, "--conversation"); got != "conv-7" {
		t.Errorf("the second attempt resumed conversation %q, want conv-7", got)
	}

	// And the work survives it: a run that pushed a branch before the
	// quota ran out has already changed the world, and
	// agent.Framework's contract is that the caller gets both halves.
	if result == nil {
		t.Fatal("Run returned no Result alongside the limit, want what the run did before it")
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "run_command" {
		t.Errorf("ToolCalls = %+v, want the first attempt's own call", result.ToolCalls)
	}
	if !strings.Contains(result.Transcript, "resumed the same conversation in the same sandbox") {
		t.Errorf("the transcript does not say why it appears to start twice:\n%s", result.Transcript)
	}
}

// The other way into a resumed attempt, and the one this package has
// that the other two do not: an attempt that was not refused at all but
// cut off -- agy ending in ERROR with nothing to classify -- is resumed,
// and the resumed attempt then meets the quota wall. The run ends there:
// an interruption is worth picking back up, and a limit is what stops
// the picking up.
func TestAQuotaRefusalEndsARunResumedAfterAnInterruption(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-3"), genericAgyFailure), err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("conv-3"), quotaResult), err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("conv-3"), quotaResult), err: errors.New("exit status 1")},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	if _, ok := agent.UsageLimit(err); !ok {
		t.Fatalf("Run err = %v, want the usage limit the resumed attempt met", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want 2 -- the interruption resumed once, and the limit ending it", len(r.calls))
	}
	// The turn the resumed attempt sent says the CLI stopped, not that a
	// provider refused it: nothing had refused anything at that point,
	// and resume.go declines to tell the model otherwise.
	second, err := promptFromStdin(r.calls[1].stdin)
	if err != nil {
		t.Fatalf("decoding the resumed attempt's turn: %v", err)
	}
	if !strings.Contains(second, "the agent CLI you run in stopped") {
		t.Errorf("the resumed attempt's turn = %q, want agent.InterruptedPrompt", second)
	}
}

// The guard inside the interruption path, read off the stream rather
// than off the private log diagnostics_test.go uses. ERROR is the status
// resume.go picks a run back up on, and a refusal that arrives under it
// carrying the provider's own quota words is not an interruption at all:
// resuming it would spend the run's whole budget re-reading the same
// refusal, and would leave dispatch unpaused for however long that took.
func TestAQuotaRefusalUnderAgysErrorStatusIsNotResumed(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-1"), quotaUnderErrorStatus), err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("conv-1"), quotaUnderErrorStatus), err: errors.New("exit status 1")},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.UsageLimit(err); !ok {
		t.Fatalf("Run err = %v, want a usage limit: the status was ERROR but the words were the provider's", err)
	}
	if len(r.calls) != 1 {
		t.Errorf("agy was exec'd %d time(s), want 1 -- a quota refusal is dispatch's wait, not a resume's", len(r.calls))
	}
}

// And the interruption path is still there for what it is for: the same
// ERROR status with nothing in it about quota is resumed, one exec
// later, on the same conversation. Without this the test above would
// pass for a loop that had stopped resuming interruptions altogether.
func TestAnErrorStatusWithNoRefusalInItIsStillResumed(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-2"), genericAgyFailure), err: errors.New("exit status 1")},
		{stdout: stream(conversationInit("conv-2"), agentTurn(0, "done"), successResult("done"))},
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want the resumed attempt to have carried the run", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("agy was exec'd %d time(s), want 2", len(r.calls))
	}
	if got := flagValue(r.calls[1].args, "--conversation"); got != "conv-2" {
		t.Errorf("the resumed attempt asked for conversation %q, want conv-2", got)
	}
	if result.FinalText != "done" {
		t.Errorf("FinalText = %q, want the resumed attempt's answer", result.FinalText)
	}
}

// The ending a refusal must not be allowed to become, and the shape this
// package makes easy: a run refused for capacity, resumed, and then cut
// off twice without agy writing a terminal event at all -- which is
// exactly what an interruption is. A capture parsed whole still holds
// the first attempt's refusal at that point, since parseEvents clears
// that state on a successful result and nowhere else, so reporting it
// would tell orchestrator.RunDispatch that the provider had no capacity
// for a run whose last two attempts died of something else. That is
// model.CapacityOutcome, an outcome no failure streak counts, so a task
// failing this way would never retire.
func TestAnEarlierRefusalDoesNotOutliveItsAttempt(t *testing.T) {
	// Resumed, and killed before it reached a verdict: the interruption
	// this file's loop exists to pick back up, and no account of a
	// provider anywhere in it.
	cutOff := attemptReply{stdout: stream(conversationInit("conv-4")), err: errors.New("exit status 2")}
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-4"), overloadedResult), err: errors.New("exit status 1")},
		cutOff, cutOff,
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the last attempt's own failure")
	}
	if _, ok := agent.Capacity(err); ok {
		t.Errorf("Run err = %v, want an ordinary failure: the refusal was the first attempt's", err)
	}
	if _, ok := agent.UsageLimit(err); ok {
		t.Errorf("Run err = %v, want an ordinary failure rather than a limit", err)
	}
	if want := 1 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Errorf("agy was exec'd %d time(s), want %d", len(r.calls), want)
	}
}

// The other side of that, so the test above cannot be satisfied by a
// classifier that simply stopped reading attempts without a terminal
// event: the *last* attempt's own refusal is still recognised when the
// only account of it is the subprocess error.
func TestTheLastAttemptsOwnRefusalIsStillRead(t *testing.T) {
	killed := attemptReply{
		stdout: stream(conversationInit("conv-5")),
		err:    errors.New("exit status 1 (stderr: 503 Service Unavailable, the model is overloaded)"),
	}
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-5"), overloadedResult), err: errors.New("exit status 1")},
		killed, killed,
	}}
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.Capacity(err); !ok {
		t.Fatalf("Run err = %v, want the capacity refusal the last attempt died of", err)
	}
}
