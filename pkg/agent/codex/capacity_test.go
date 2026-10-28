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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// The OpenAI API answers an overloaded model in words, and codex fails
// the turn carrying the sentence. Run must hand it back as an
// agent.CapacityError, which is what keeps the attempt off the task's
// own failure streak (model.CapacityOutcome) without pausing the
// deployment.
func TestRunReportsAnOverloadedProviderAsCapacity(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox",` +
			`"tool":"run_command","status":"completed","result":"pushed"}}`,
		`{"type":"turn.failed","error":{"message":"The server is overloaded or not ready yet."}}`,
	}, "\n")
	r := &recordingRunner{stdout: stream, err: errors.New("exit status 1")}
	// withoutRetries because Run now retries a capacity refusal in place
	// (retry.go), and this runner answers every attempt with the same
	// canned refusal: the classification under test is unchanged, and
	// the retry loop has tests of its own next door.
	f := newTestFramework(r, "/usr/local/bin/grain", withoutRetries())

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	capacity, ok := agent.Capacity(err)
	if !ok {
		t.Fatalf("err = %v, want an agent.CapacityError", err)
	}
	if capacity.Framework != "codex" {
		t.Errorf("Framework = %q, want codex", capacity.Framework)
	}
	if _, isLimit := agent.UsageLimit(err); isLimit {
		t.Error("an overload read as a usage limit: it must not pause dispatch")
	}
	// A run that pushed a branch and only then was refused has already
	// changed the world.
	if result == nil || len(result.ToolCalls) != 1 {
		t.Fatalf("Result = %+v, want the work the run did before the refusal", result)
	}
}

// A 429 is the one refusal codex reports in words of its own rather than
// the provider's: it retries the call not at all and collapses the whole
// thing to "exceeded retry limit, last status: 429 Too Many Requests",
// the provider's body gone with it (capacity.go's header, and
// docs/hand-runs/2026-09-08-codex-retry.md §7). Run must still hand that
// back as an agent.CapacityError -- so that the attempt is retried in
// place, is recorded under model.CapacityOutcome, and costs the task
// nothing -- and must not read it as a usage limit, which would stop
// dispatch deployment-wide for what is most often one busy minute.
func TestRunReportsARateLimitedProviderAsCapacity(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"error","message":"exceeded retry limit, last status: 429 Too Many Requests"}`,
		`{"type":"turn.failed","error":{"message":"exceeded retry limit, last status: 429 Too Many Requests"}}`,
	}, "\n")
	r := &recordingRunner{stdout: stream, err: errors.New("exit status 1")}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutRetries())

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	capacity, ok := agent.Capacity(err)
	if !ok {
		t.Fatalf("err = %v, want an agent.CapacityError: a rate limit read as an ordinary "+
			"failure counts against the task's own streak, and five of them retire it", err)
	}
	if capacity.Framework != "codex" {
		t.Errorf("Framework = %q, want codex", capacity.Framework)
	}
	if _, isLimit := agent.UsageLimit(err); isLimit {
		t.Error("a 429 read as a usage limit: it must not pause dispatch deployment-wide")
	}
	if !retryable(stream, errors.New("exit status 1"), &turnCap{}) {
		t.Error("retryable = false for a 429: codex retries one zero times, so nothing else will")
	}
}

// The wording either side of the status is codex's and moves when codex
// ships, so both halves of the marker pair are exercised: the sentence
// this release writes, and the status with its reason phrase, which is
// what still matches if the sentence is reworded.
func TestCapacityFailureReadsA429WhicheverWayItIsWorded(t *testing.T) {
	for _, text := range []string{
		"exceeded retry limit, last status: 429 Too Many Requests",
		"exceeded retry limit, last status: 429 too many requests",
		"unexpected status 429 Too Many Requests: rate limit reached, url: https://api.openai.com/v1/responses",
	} {
		capacity := capacityFailure(text, errors.New("exit status 1"))
		if capacity == nil {
			t.Errorf("capacityFailure(%q) = nil, want a capacity refusal", text)
			continue
		}
		if limit := usageLimitFailure(text, errors.New("exit status 1")); limit != nil {
			t.Errorf("usageLimitFailure(%q) = %v: a bare 429 must not pause dispatch", text, limit)
		}
	}
}

// The provider's own HTTP status, which reaches us through the
// subprocess's exit rather than through the stream.
func TestCapacityFailureReadsTheSubprocessError(t *testing.T) {
	capacity := capacityFailure("", errors.New("exit status 1 (stderr: 503 Service Unavailable)"))
	if capacity == nil {
		t.Fatal("capacityFailure = nil for a 503")
	}
	if !errors.Is(capacity, agent.ErrCapacity) {
		t.Error("the refusal does not match agent.ErrCapacity")
	}
}

// Every ordinary failure has to stay an ordinary failure: a fault read
// as the provider's is a fault nothing ever fixes.
func TestCapacityFailureIgnoresAnOrdinaryFailure(t *testing.T) {
	for _, text := range []string{
		"",
		"the build failed: 503 packages could not be fetched",
		"stream disconnected before completion",
		// The bare status both classifiers refuse to read, still
		// refused now that its reason phrase is a marker: three digits
		// are three digits, and a repository's own output is full of
		// them.
		"the rate limiter test expected 429 and the handler returned 200",
		"exceeded retry limit, last status: 400 Bad Request",
	} {
		if capacity := capacityFailure(text, errors.New("exit status 1")); capacity != nil {
			t.Errorf("capacityFailure(%q) = %v, want nil", text, capacity)
		}
	}
}
