package codex

// The seam between retry.go's resume loop and usagelimit.go's reading of
// a refusal, for the ending retry_test.go stops short of: a run refused
// for capacity, resumed on the thread the first attempt opened, and
// *then* refused because the credential has no budget left.
//
// One capture holds a thread per attempt by then, so every part of the
// ending has to come from the attempt that produced it -- whether it was
// a limit at all, how long the provider said to wait, and the work the
// run had already done before it. Reading an earlier attempt's thread
// instead either pauses a deployment that has budget or leaves one that
// has none dispatching into the same wall.
//
// See agent/claude's file of the same name for the framework whose CLI
// spells the limit differently, and orchestrator/usagelimit_test.go for
// what the error asserted on here does to dispatch.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// quotaTurn is how codex words a plan's own ceiling: a failed turn
// carrying the CLI's sentence, with the relative "try again in" that
// becomes agent.UsageLimitError.RetryAfter.
const quotaTurn = `{"type":"turn.failed","error":{"message":"You've hit your usage limit. Try again in 4h 12m."}}`

// The case this file exists for: the resumed attempt is the one that
// meets the wall, and what comes back is the limit it met rather than
// the capacity refusal that sent the run there.
func TestAQuotaRefusalOnAResumedThreadEndsTheRunAsALimit(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("cloning"), overloadedTurn), err: errors.New("exit status 1")},
		{stdout: attemptStream(agentMessage("pushed the branch"), quotaTurn), err: errors.New("exit status 1")},
		// One more than the run should ever ask for, so a loop that
		// retried the limit fails here rather than erroring on an
		// exhausted script.
		{stdout: attemptStream(quotaTurn), err: errors.New("exit status 1")},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want the resumed attempt's usage limit", err)
	}
	if limit.Framework != usageLimitFramework {
		t.Errorf("Framework = %q, want %q -- a pause is scoped to the credential that met the limit", limit.Framework, usageLimitFramework)
	}
	if want := 4*time.Hour + 12*time.Minute; limit.RetryAfter != want {
		t.Errorf("RetryAfter = %s, want %s -- codex's own \"try again in\" off the attempt that met the limit", limit.RetryAfter, want)
	}
	if _, ok := agent.Capacity(err); ok {
		t.Errorf("Run err = %v, want the limit rather than the refusal that sent the run to it", err)
	}
	// Two execs and no more: a quota window is hours from clearing, so
	// the run's remaining retry would buy one more refusal.
	if len(r.calls) != 2 {
		t.Fatalf("codex was exec'd %d time(s), want 2 -- the refusal, then the resume that met the limit", len(r.calls))
	}
	if !argsHave(r.calls[1].args, "exec", "resume") || !argsHave(r.calls[1].args, "t1", "-") {
		t.Errorf("the second attempt's args = %v, want `codex exec resume ... t1 -`", r.calls[1].args)
	}

	// And the work survives it. A run that pushed a branch before the
	// credential ran out of budget has already changed the world, and
	// agent.Framework's contract is that the caller gets both halves.
	if result == nil {
		t.Fatal("Run returned no Result alongside the limit, want what the run did before it")
	}
	for _, want := range []string{"cloning", "pushed the branch", "resumed the same conversation in the same sandbox"} {
		if !strings.Contains(result.Transcript, want) {
			t.Errorf("Transcript missing %q:\n%s", want, result.Transcript)
		}
	}
}

// The same wall met through the other door: a refusal hard enough to
// kill codex before it failed a turn, so the only account of it is the
// subprocess error. The resumed attempt is read exactly as a first one
// would be -- it opened a thread, so nothing about it says the resume
// argv was refused.
func TestAQuotaRefusalThatKilledAResumedCodexIsStillALimit(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("cloning"), overloadedTurn), err: errors.New("exit status 1")},
		{stdout: attemptStream(), err: errors.New(`exit status 1 (stderr: 429 {"error":{"code":"rate_limit_exceeded"}})`)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want the limit the subprocess reported", err)
	}
	if !strings.Contains(limit.Message, "rate_limit_exceeded") {
		t.Errorf("Message = %q, want the provider's own words", limit.Message)
	}
	if len(r.calls) != 2 {
		t.Errorf("codex was exec'd %d time(s), want 2", len(r.calls))
	}
	if !slices.Contains(r.calls[1].args, "resume") {
		t.Errorf("the second attempt's args = %v, want it to have resumed the first attempt's thread", r.calls[1].args)
	}
}

// The other direction, and the reason parseEvents starts a fresh reading
// at each thread.started: a refusal belongs to the attempt that met it
// and to no later one. A run refused for capacity, resumed, and then
// failed on its own terms is an ordinary failure -- recorded against the
// task's own streak -- and reporting it as the provider's doing would
// leave a genuinely broken task retrying forever.
func TestAnEarlierRefusalDoesNotOutliveItsThread(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("cloning"), overloadedTurn), err: errors.New("exit status 1")},
		{stdout: attemptStream(agentMessage("running the tests"),
			`{"type":"turn.failed","error":{"message":"the tests failed"}}`), err: errors.New("exit status 1")},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the resumed attempt's own failure")
	}
	if _, ok := agent.Capacity(err); ok {
		t.Errorf("Run err = %v, want an ordinary failure: the refusal was the attempt before's", err)
	}
	if _, ok := agent.UsageLimit(err); ok {
		t.Errorf("Run err = %v, want an ordinary failure rather than a limit", err)
	}
	if !strings.Contains(err.Error(), "the tests failed") {
		t.Errorf("Run err = %v, want the resumed attempt's own account of itself", err)
	}
}

// The other side of that, so the test above cannot be satisfied by a
// classifier that simply stopped reading attempts which failed a turn:
// the *last* attempt's own refusal is still recognised when the only
// account of it is the subprocess error.
func TestTheLastAttemptsOwnRefusalIsStillRead(t *testing.T) {
	killed := attemptReply{
		stdout: attemptStream(),
		err:    errors.New("exit status 1 (stderr: 503 the server is overloaded or not ready yet)"),
	}
	r := &attemptRunner{replies: []attemptReply{
		{stdout: attemptStream(agentMessage("cloning"), overloadedTurn), err: errors.New("exit status 1")},
		killed, killed,
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.Capacity(err); !ok {
		t.Fatalf("Run err = %v, want the capacity refusal the last attempt died of", err)
	}
	if want := 1 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Errorf("codex was exec'd %d time(s), want %d", len(r.calls), want)
	}
}
