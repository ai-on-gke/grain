package claude

// The seam between the two files either side of it: retry.go's resume
// loop, which execs claude again on the same session when the provider
// refuses a run mid-flight, and usagelimit.go, which decides that a
// refusal was the account running out of budget rather than the provider
// having a bad minute.
//
// retry_test.go already covers the first attempt meeting each wall. What
// is not covered there, and is what a deployment actually meets, is the
// second: a run refused for capacity, resumed, and *then* refused for
// quota. Everything about that ending has to come from the attempt that
// produced it -- the type, the reset instant, the work the run had
// already done -- because one capture now holds a result event per
// attempt and reading the wrong one either pauses a deployment that has
// budget or fails to pause one that has none.
//
// The same reading decides the other two endings a multi-attempt capture
// can be misread into -- a capacity refusal that belonged to an earlier
// attempt, and an ordinary failure hidden behind one -- so those are here
// too: they are the same question asked of the same seam.
//
// What the orchestrator does with the error these tests assert on is
// orchestrator/usagelimit_test.go's own subject: the run records
// model.PausedOutcome, dispatch of this framework stops until the window
// resets, and the runs in flight on the same credential are cancelled.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// quotaResult is how an OAuth account's own limit reaches this package
// when claude fails the run over it: the CLI's machine-readable form,
// "...usage limit reached|<unix seconds>", on a terminal result event
// with is_error set. The epoch is what usagelimit.go's strict pattern
// reads, and what the pause below is timed by.
const quotaResult = `{"type":"result","subtype":"error_during_execution","is_error":true,` +
	`"result":"Claude AI usage limit reached|1772370000"}`

// quotaReset is quotaResult's epoch as an instant.
var quotaReset = time.Unix(1772370000, 0).UTC()

// The case this file exists for. A run refused for capacity is resumed
// -- same sandbox, same session -- and the resumed attempt meets the
// account's own limit instead. What comes back has to be the limit, with
// the reset the second attempt reported: the deployment pauses on it,
// and a capacity ending here would send the next task straight at the
// same wall.
func TestAQuotaRefusalOnAResumedAttemptEndsTheRunAsALimit(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-7"), assistantText("cloning"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(systemInit("sess-7"), assistantText("pushed the branch"), quotaResult), err: errors.New("exit status 1")},
		// One more than the run should ever ask for, so a loop that
		// retried the limit fails here rather than erroring on an
		// exhausted script.
		{stdout: stream(systemInit("sess-7"), quotaResult), err: errors.New("exit status 1")},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want the resumed attempt's usage limit", err)
	}
	if limit.Framework != usageLimitFramework {
		t.Errorf("Framework = %q, want %q -- a pause is scoped to the credential that met the limit", limit.Framework, usageLimitFramework)
	}
	if !limit.ResetAt.Equal(quotaReset) {
		t.Errorf("ResetAt = %s, want %s: the reset is the one the attempt that met the limit reported", limit.ResetAt, quotaReset)
	}
	// Two execs and no more: a quota window is hours from clearing, so
	// spending the run's remaining retries on it would buy nothing but
	// two more refusals.
	if len(r.calls) != 2 {
		t.Fatalf("claude was exec'd %d time(s), want 2 -- the refusal, then the resume that met the limit", len(r.calls))
	}
	if got := argValue(r.calls[1].args, "--resume"); got != "sess-7" {
		t.Errorf("the second attempt resumed session %q, want sess-7", got)
	}

	// And the work survives it. A run that pushed a branch before the
	// account ran out of budget has already changed the world, and
	// agent.Framework's contract is that the caller gets both halves --
	// orchestrator.RunDispatch processes those tool calls even as it
	// records the run as paused.
	if result == nil {
		t.Fatal("Run returned no Result alongside the limit, want what the run did before it")
	}
	for _, want := range []string{"cloning", "pushed the branch", "resumed the same conversation in the same sandbox"} {
		if !strings.Contains(result.Transcript, want) {
			t.Errorf("Transcript missing %q:\n%s", want, result.Transcript)
		}
	}
}

// The same ending in claude's other spelling of it, which is the one
// nothing else about a capture marks as a failure: the CLI delivers the
// limit as the run's final answer and exits 0. The resumed attempt has
// to be read under the strict rule exactly as a first attempt is, or a
// run that was refused once and then hit the wall reports success.
func TestAQuotaRefusalDeliveredAsTheResumedAttemptsFinalAnswerStillReportsALimit(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-3"), assistantText("reading the repo"), overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(systemInit("sess-3"),
			`{"type":"result","result":"Claude AI usage limit reached|1772370000"}`)},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want the limit claude delivered as the resumed attempt's answer", err)
	}
	if !limit.ResetAt.Equal(quotaReset) {
		t.Errorf("ResetAt = %s, want %s", limit.ResetAt, quotaReset)
	}
	if result == nil {
		t.Error("Run returned no Result alongside the limit, want the parsed one")
	}
}

// The other direction, and the false positive a multi-attempt capture
// makes possible: the phrase that means "out of budget" appearing in an
// *earlier* attempt's work rather than in the ending of the last one. A
// run that grepped for it, was refused for capacity mid-flight, and then
// finished on the resumed session is a run that succeeded -- pausing the
// deployment over what the agent read would be the strict rule failing
// at the one thing it is strict for.
func TestAnEarlierAttemptsQuotedLimitDoesNotEndTheRun(t *testing.T) {
	quoting := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1",` +
		`"content":"usagelimit.go: Claude AI usage limit reached|1772370000"}]}}`
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-5"), quoting, overloadedResult), err: errors.New("exit status 1")},
		{stdout: stream(systemInit("sess-5"), assistantText("done"), successResult("found the phrase in one file", 2))},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "grep for it", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run err = %v, want the resumed attempt's success: the phrase was in the agent's own work", err)
	}
	if result.FinalText != "found the phrase in one file" {
		t.Errorf("FinalText = %q, want the resumed attempt's answer", result.FinalText)
	}
}

// A capacity refusal on the attempt *after* a quota one cannot happen --
// the quota ends the run -- but the reverse ordering of the same two
// walls can, and the run must end on the second one rather than on
// whichever was worse. This is the property that makes the pause
// self-correcting: each run reports the wall it actually stopped at.
func TestTheRunEndsOnTheWallItsLastAttemptMet(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-1"), quotaResult), err: errors.New("exit status 1")},
		{stdout: stream(systemInit("sess-1"), overloadedResult), err: errors.New("exit status 1")},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.UsageLimit(err); !ok {
		t.Fatalf("Run err = %v, want the first attempt's usage limit", err)
	}
	if _, ok := agent.Capacity(err); ok {
		t.Errorf("Run err = %v, want a limit rather than a capacity refusal", err)
	}
	if len(r.calls) != 1 {
		t.Errorf("claude was exec'd %d time(s), want 1: a quota refusal is dispatch's wait, not a backoff's", len(r.calls))
	}
}

// And the ending a refusal must not be allowed to become: the run was
// refused once, resumed, and the resumed attempt was killed without
// claude writing a terminal event at all. A capture parsed whole still
// holds the first attempt's refusal at that point, and reporting it
// would tell orchestrator.RunDispatch that the provider had no capacity
// for a run whose last attempt died of something else entirely -- which
// is model.CapacityOutcome, an outcome no failure streak counts, so a
// task failing this way would never retire.
func TestAnEarlierRefusalDoesNotOutliveItsAttempt(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-2"), assistantText("cloning"), overloadedResult), err: errors.New("exit status 1")},
		// Resumed, wrote a turn, and was killed: no result event of its
		// own, and nothing in what it did say about a provider.
		{stdout: stream(systemInit("sess-2"), assistantText("running the tests")), err: errors.New("exit status 2")},
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

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
	if !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("Run err = %v, want the resumed attempt's own exit status", err)
	}
}

// The other side of that, so the test above cannot be satisfied by a
// classifier that simply stopped reading attempts without a terminal
// event: the *last* attempt's own refusal is still recognised when the
// only account of it is the subprocess error.
func TestTheLastAttemptsOwnRefusalIsStillRead(t *testing.T) {
	// Refused on every attempt, the last two with nothing but the exit
	// error to say so, so the run spends its whole retry budget and ends
	// on an attempt that wrote no terminal event of its own.
	killed := attemptReply{
		stdout: stream(systemInit("sess-4")),
		err:    errors.New("exit status 1 (stderr: 503 Service Unavailable)"),
	}
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(systemInit("sess-4"), overloadedResult), err: errors.New("exit status 1")},
		killed, killed,
	}}
	f := newFramework(r, "mcpserver-path", withoutBackoff())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.Capacity(err); !ok {
		t.Fatalf("Run err = %v, want the capacity refusal the last attempt died of", err)
	}
	if want := 1 + agent.DefaultTransientRetries; len(r.calls) != want {
		t.Errorf("claude was exec'd %d time(s), want %d", len(r.calls), want)
	}
}
