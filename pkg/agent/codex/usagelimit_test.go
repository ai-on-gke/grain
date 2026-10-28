package codex

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// A plan limit is not a run that failed on its own terms, and the type
// is what says so: orchestrator.RunDispatch pauses dispatch on it rather
// than sending the next task at the same refusal.
func TestUsageLimitFailureRecognisesCodexsOwnRefusal(t *testing.T) {
	limit := usageLimitFailure("You've hit your usage limit. Try again in 4h 12m.", nil)
	if limit == nil {
		t.Fatal("usageLimitFailure = nil for codex's own limit message")
	}
	if limit.Framework != "codex" {
		t.Errorf("Framework = %q, want codex -- a deployment running more than one has to know whose credential ran out", limit.Framework)
	}
	if want := 4*time.Hour + 12*time.Minute; limit.RetryAfter != want {
		t.Errorf("RetryAfter = %v, want %v", limit.RetryAfter, want)
	}
	if !errors.Is(limit, agent.ErrUsageLimit) {
		t.Error("the limit does not match agent.ErrUsageLimit")
	}
}

// The API's own wording, which reaches us through the subprocess's exit
// rather than through the stream, is the other half of the same fact.
func TestUsageLimitFailureReadsTheSubprocessError(t *testing.T) {
	limit := usageLimitFailure("", errors.New(`exit status 1 (stderr: {"error":{"code":"rate_limit_exceeded"}})`))
	if limit == nil {
		t.Fatal("usageLimitFailure = nil for a rate_limit_exceeded body")
	}
	if limit.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want none: the API named no delay", limit.RetryAfter)
	}
}

// Every ordinary failure has to stay an ordinary failure: pausing the
// whole deployment on a run that merely failed would be the expensive
// mistake here.
func TestUsageLimitFailureIgnoresAnOrdinaryFailure(t *testing.T) {
	for _, text := range []string{
		"",
		"the tests failed: 429 lines changed",
		"stream disconnected before completion",
	} {
		if limit := usageLimitFailure(text, errors.New("exit status 1")); limit != nil {
			t.Errorf("usageLimitFailure(%q) = %v, want nil", text, limit)
		}
	}
}

// End to end through Run: a codex that exits non-zero having reported a
// limit comes back as an agent.UsageLimitError, not as "running codex:
// exit status 1" -- the one thing about that failure a deployment can
// act on.
func TestRunReportsAUsageLimitAsSuch(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"run_command","status":"completed","result":"pushed"}}`,
		`{"type":"turn.failed","error":{"message":"You've hit your usage limit. Try again in 45m."}}`,
	}, "\n")
	r := &recordingRunner{stdout: stream, err: errors.New("exit status 1")}
	f := newTestFramework(r, "/usr/local/bin/grain")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("err = %v, want an agent.UsageLimitError", err)
	}
	if limit.RetryAfter != 45*time.Minute {
		t.Errorf("RetryAfter = %v, want 45m", limit.RetryAfter)
	}
	// A run that pushed a branch and only then met the limit has already
	// changed the world.
	if result == nil || len(result.ToolCalls) != 1 {
		t.Fatalf("Result = %+v, want the work the run did before the refusal", result)
	}
}
