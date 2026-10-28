package claude

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// The Anthropic API answers an overloaded model with a 529 whose error
// type is overloaded_error, and claude fails the turn carrying that
// body. Run must hand it back as an agent.CapacityError, which is what
// keeps the attempt off the task's own failure streak
// (model.CapacityOutcome) without pausing the deployment.
func TestRunReportsAnOverloadedProviderAsCapacity(t *testing.T) {
	fake := &fakeRunner{
		stdout: streamJSONLine(t, map[string]any{
			"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": `API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		}),
		err: errors.New("exit status 1"),
	}
	// withoutRetries because Run now retries a capacity refusal in place
	// (retry.go), and this runner answers every attempt with the same
	// canned refusal: the classification under test is unchanged, and
	// the retry loop has tests of its own next door.
	f := newFramework(fake, "mcpserver-path", withoutRetries())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	capacity, ok := agent.Capacity(err)
	if !ok {
		t.Fatalf("Run err = %v, want an agent.CapacityError", err)
	}
	if capacity.Framework != "claude" {
		t.Errorf("Framework = %q, want claude", capacity.Framework)
	}
	if !strings.Contains(capacity.Message, "overloaded_error") {
		t.Errorf("Message = %q, want the provider's own words", capacity.Message)
	}
	if _, isLimit := agent.UsageLimit(err); isLimit {
		t.Error("an overload read as a usage limit: it must not pause dispatch")
	}
}

// A refusal that is both is a usage limit: pausing is what keeps the
// rest of the queue off a window that will not reopen for hours, and an
// overload costs nothing to retry.
func TestRunPrefersAUsageLimitToACapacityRefusal(t *testing.T) {
	fake := &fakeRunner{
		stdout: streamJSONLine(t, map[string]any{
			"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": `API Error: 429 {"error":{"type":"rate_limit_error"}} (503 Service Unavailable behind it)`,
		}),
		err: errors.New("exit status 1"),
	}
	f := newFramework(fake, "mcpserver-path")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.UsageLimit(err); !ok {
		t.Fatalf("Run err = %v, want the usage limit to win", err)
	}
	if _, ok := agent.Capacity(err); ok {
		t.Error("a usage limit also read as a capacity refusal: only the pause is the right response")
	}
}

// A run that merely ran out of turns is not the provider's doing, even
// when its last tool call printed the words.
func TestCapacityFailureIgnoresAnOrdinaryFailure(t *testing.T) {
	for _, text := range []string{
		"",
		"the tests failed: 529 assertions",
		"the sandbox is unavailable",
	} {
		if capacity := capacityFailure(text, errors.New("exit status 1")); capacity != nil {
			t.Errorf("capacityFailure(%q) = %v, want nil", text, capacity)
		}
	}
}
