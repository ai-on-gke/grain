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

package agent

import (
	"errors"
	"strings"
	"testing"
)

// The shared half of the classification: an HTTP status a provider's own
// edge writes, recognised for whichever framework asked.
func TestCapacityFailureRecognisesTheSharedMarkers(t *testing.T) {
	for _, text := range []string{
		"503 Service Unavailable",
		"upstream said 502 Bad Gateway",
		"504 Gateway Timeout after 60s",
	} {
		capacity := CapacityFailure("claude", nil, text, errNoCapacityExit)
		if capacity == nil {
			t.Errorf("CapacityFailure(%q) = nil, want a capacity refusal", text)
			continue
		}
		if capacity.Framework != "claude" {
			t.Errorf("Framework = %q, want claude", capacity.Framework)
		}
		if !errors.Is(capacity, ErrCapacity) {
			t.Errorf("CapacityFailure(%q) does not match ErrCapacity", text)
		}
		if !errors.Is(capacity, errNoCapacityExit) {
			t.Errorf("CapacityFailure(%q) lost the underlying error", text)
		}
	}
}

var errNoCapacityExit = errors.New("exit status 1")

// A framework's own vocabulary is consulted alongside the shared list,
// and only for the framework that owns it: a phrase one provider writes
// is, in another provider's stream, a phrase somebody merely typed.
func TestCapacityFailureUsesTheFrameworksOwnMarkers(t *testing.T) {
	const text = "the model is overloaded. please try again later."
	if capacity := CapacityFailure("antigravity", []string{"model is overloaded"}, text, nil); capacity == nil {
		t.Error("CapacityFailure = nil for the framework's own marker")
	}
	if capacity := CapacityFailure("codex", []string{"server is overloaded"}, text, nil); capacity != nil {
		t.Errorf("CapacityFailure = %v, want nil: another framework's phrase is not evidence here", capacity)
	}
}

// Every ordinary failure has to stay an ordinary failure. The text these
// markers are matched against is a run's own transcript, which has been
// carrying a repository's files and test output for an hour, so the
// digits and the words on their own must not be enough.
func TestCapacityFailureIgnoresAnOrdinaryFailure(t *testing.T) {
	for _, text := range []string{
		"",
		"the tests failed: 503 assertions, 502 passed",
		"docs/runbook.md: what to do when the queue is unavailable",
		"the agent said the server was overloaded with work",
		"exceeded max turns (20) without a final answer",
	} {
		if capacity := CapacityFailure("antigravity", []string{"model is overloaded"},
			text, errors.New("exit status 1")); capacity != nil {
			t.Errorf("CapacityFailure(%q) = %v, want nil", text, capacity)
		}
	}
}

// The message is bounded the same way a usage limit's is: this text
// lands in task_run.detail, which `grain get` and the UI print inline.
func TestCapacityFailureTrimsTheProvidersOwnBody(t *testing.T) {
	body := "{\n  \"error\": {\n    \"code\": 503,\n    \"message\": \"" +
		strings.Repeat("the model is overloaded. ", 40) + "\"\n  }\n}"
	capacity := CapacityFailure("antigravity", []string{"model is overloaded"}, body, nil)
	if capacity == nil {
		t.Fatal("CapacityFailure = nil for a 503 body")
	}
	if strings.Contains(capacity.Message, "\n") {
		t.Errorf("Message = %q, want one line", capacity.Message)
	}
	if len(capacity.Message) > maxLimitMessage+3 {
		t.Errorf("Message is %d bytes, want no more than %d", len(capacity.Message), maxLimitMessage+3)
	}
}

// Capacity is what orchestrator.RunDispatch asks with, so it has to
// reach a wrapped one -- and has to say no to a usage limit, which is
// the neighbouring refusal with a very different response.
func TestCapacityFindsAWrappedRefusalAndOnlyThat(t *testing.T) {
	capacity := &CapacityError{Framework: "codex", Message: "503 Service Unavailable"}
	wrapped := errors.New("orchestrator: running r1: " + capacity.Error())
	if _, ok := Capacity(wrapped); ok {
		t.Error("Capacity matched an error that merely quotes the sentence")
	}
	if _, ok := Capacity(errors.Join(errors.New("boom"), capacity)); !ok {
		t.Error("Capacity did not find a wrapped CapacityError")
	}
	if _, ok := Capacity(&UsageLimitError{Framework: "codex"}); ok {
		t.Error("Capacity matched a usage limit: the two ask for different responses")
	}
	if _, ok := UsageLimit(capacity); ok {
		t.Error("UsageLimit matched a capacity refusal, which must not pause the deployment")
	}
}
