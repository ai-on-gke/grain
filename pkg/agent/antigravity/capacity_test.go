package antigravity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// The Gemini API answers an overloaded model with a 503, and agy reports
// it as a failed terminal status like any other failure. Run must hand
// that back as an agent.CapacityError rather than as a plain failure,
// which is what orchestrator.RunDispatch reads to keep the attempt off
// the task's own failure streak.
func TestRunReportsAnOverloadedModelAsCapacity(t *testing.T) {
	r := &recordingRunner{stdout: stream(
		initLine,
		`{"event":"result","result":{"status":"FAILURE","error":"[503 Service Unavailable] The model is overloaded. Please try again later."}}`,
	)}
	// withoutBackoff because Run now retries a refusal like this in
	// place before giving up on it (resume.go), and this test is about
	// what it reports once it has: the retry loop itself is
	// resume_test.go's.
	f := newFramework(r, "/usr/local/bin/grain", withoutBackoff())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	capacity, ok := agent.Capacity(err)
	if !ok {
		t.Fatalf("Run err = %v, want an agent.CapacityError", err)
	}
	if capacity.Framework != "antigravity" {
		t.Errorf("Framework = %q, want antigravity", capacity.Framework)
	}
	if !strings.Contains(capacity.Message, "overloaded") {
		t.Errorf("Message = %q, want the provider's own words", capacity.Message)
	}
	// The response to this must stay narrower than the response to a
	// quota refusal: nothing about an overload justifies stopping the
	// whole deployment.
	if _, isLimit := agent.UsageLimit(err); isLimit {
		t.Error("an overload read as a usage limit: it must not pause dispatch")
	}
	// agent.Framework's contract: a run that pushed a branch before the
	// provider refused it still comes back with what it did.
	if result == nil {
		t.Error("Run returned no Result at all, want the partial one alongside the refusal")
	}
}

// The gRPC status, which reaches us through the subprocess's own exit
// rather than through the stream, is the other half of the same fact.
func TestCapacityFailureReadsTheSubprocessError(t *testing.T) {
	capacity := capacityFailure("", errors.New(`exit status 1 (stderr: rpc error: code = Unavailable desc = the service is overloaded)`))
	if capacity == nil {
		t.Fatal("capacityFailure = nil for a gRPC UNAVAILABLE")
	}
	if !errors.Is(capacity, agent.ErrCapacity) {
		t.Error("the refusal does not match agent.ErrCapacity")
	}
}

// The API's own JSON body, in both the spellings a client renders it in.
func TestCapacityFailureReadsTheAPIsStatusField(t *testing.T) {
	for _, text := range []string{
		`{"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`,
		`{"error": {"code": 503, "status": "UNAVAILABLE"}}`,
		"Error 503, Message: no capacity, Status: UNAVAILABLE, Details: []",
	} {
		if capacity := capacityFailure(text, nil); capacity == nil {
			t.Errorf("capacityFailure(%q) = nil, want a capacity refusal", text)
		}
	}
}

// A quota refusal is not an overload, and the difference is what decides
// whether the deployment pauses. A body that says both is a quota
// refusal: the pause is the response that keeps the rest of the queue
// off the same wall.
func TestRunPrefersAQuotaRefusalToACapacityOne(t *testing.T) {
	r := &recordingRunner{stdout: stream(
		initLine,
		`{"event":"result","result":{"status":"FAILURE","error":"429 RESOURCE_EXHAUSTED: You exceeded your current quota (the model is overloaded with your requests)"}}`,
	)}
	f := newFramework(r, "/usr/local/bin/grain")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if _, ok := agent.UsageLimit(err); !ok {
		t.Fatalf("Run err = %v, want the quota refusal to win", err)
	}
	if _, ok := agent.Capacity(err); ok {
		t.Error("a quota refusal also read as a capacity one: only the pause is the right response")
	}
}

// An ordinary failure must stay an ordinary failure. Reading one as the
// provider's fault would hide a real fault and retry it forever, which
// is the expensive mistake in this direction.
func TestRunKeepsAnOrdinaryFailureOrdinaryDespiteCapacityWords(t *testing.T) {
	r := &recordingRunner{stdout: stream(
		initLine,
		`{"event":"result","result":{"status":"FAILURE","error":"the tests failed: 503 assertions, and the queue is unavailable"}}`,
	)}
	f := newFramework(r, "/usr/local/bin/grain")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if _, ok := agent.Capacity(err); ok {
		t.Errorf("Run err = %v, want it read as an ordinary failure", err)
	}
}

// The tool output of a successful run is not evidence about the
// provider: a run that greps for the phrase -- reading this very file,
// say -- must not have its own success turned into a refusal.
func TestRunIgnoresCapacityWordsInASuccessfulRunsWork(t *testing.T) {
	r := &recordingRunner{stdout: stream(
		initLine,
		toolActive(0, "run_command", `{"command":"grep -r 'model is overloaded' ."}`),
		toolDone(0, "run_command", "capacity.go: model is overloaded"),
		`{"event":"result","result":{"status":"SUCCESS","response":"found it"}}`,
	)}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "grep", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run err = %v, want the run to pass", err)
	}
}
