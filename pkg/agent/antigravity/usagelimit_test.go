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

package antigravity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// agy reports a quota refusal as a failed terminal status. Run must
// hand that back as an agent.UsageLimitError rather than as its own
// generic "run ended in status FAILURE", which is what
// orchestrator.RunDispatch reads to pause the deployment instead of
// retrying into the same refusal.
func TestRunReportsAQuotaRefusalAsAUsageLimit(t *testing.T) {
	r := &recordingRunner{stdout: stream(
		initLine,
		`{"event":"result","result":{"status":"FAILURE","error":"429 RESOURCE_EXHAUSTED: You exceeded your current quota. retryDelay: 56s"}}`,
	)}
	f := newFramework(r, "/usr/local/bin/grain")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want an agent.UsageLimitError", err)
	}
	if limit.Framework != "antigravity" {
		t.Errorf("Framework = %q, want antigravity", limit.Framework)
	}
	if limit.RetryAfter != 56*time.Second {
		t.Errorf("RetryAfter = %s, want 56s -- the delay the API named", limit.RetryAfter)
	}
	if !strings.Contains(limit.Message, "RESOURCE_EXHAUSTED") {
		t.Errorf("Message = %q, want the provider's own words", limit.Message)
	}
	// agent.Framework's contract: a run that pushed a branch before it
	// ran out of quota still comes back with what it did.
	if result == nil {
		t.Error("Run returned no Result at all, want the partial one alongside the limit")
	}
}

// An ordinary failure must stay an ordinary failure: pausing the whole
// deployment is the one response that is much worse than retrying when
// it is the wrong one.
func TestRunKeepsAnOrdinaryFailureOrdinary(t *testing.T) {
	r := &recordingRunner{stdout: stream(
		initLine,
		`{"event":"result","result":{"status":"FAILURE","error":"the model returned no candidates"}}`,
	)}
	f := newFramework(r, "/usr/local/bin/grain")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if _, ok := agent.UsageLimit(err); ok {
		t.Errorf("Run err = %v, want it read as an ordinary failure", err)
	}
}

// The tool output of a successful run is not evidence about quota: agy
// reports a refusal in its terminal event, and a run that greps for the
// phrase must not put the deployment to sleep.
func TestRunIgnoresTheQuotaPhraseInASuccessfulRunsWork(t *testing.T) {
	r := &recordingRunner{stdout: stream(
		initLine,
		toolActive(0, "run_command", `{"command":"grep -r RESOURCE_EXHAUSTED ."}`),
		toolDone(0, "run_command", "usagelimit.go: resource_exhausted"),
		`{"event":"result","result":{"status":"SUCCESS","response":"found it"}}`,
	)}
	f := newFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "grep", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run err = %v, want the run to pass", err)
	}
}

func TestRetryDelayReadsTheAPIsOwnSpellings(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want time.Duration
	}{
		{"json", `{"retryDelay":"56s"}`, 56 * time.Second},
		{"snake case", `retry_delay: "30s"`, 30 * time.Second},
		{"fractional", `"retryDelay":"1.5s"`, 1500 * time.Millisecond},
		{"agy reset hours", `Your quota will reset after 2h28m27s.`, 2*time.Hour + 28*time.Minute + 27*time.Second},
		{"agy reset minutes", `Your quota will reset after 56m20s.`, 56*time.Minute + 20*time.Second},
		{"agy reset seconds", `Your quota will reset after 45s`, 45 * time.Second},
		{"agy reset in, any case", `YOUR QUOTA WILL RESET IN 1H5M.`, time.Hour + 5*time.Minute},
		{"both, reset longer", `"retryDelay":"56s" ... Your quota will reset after 56m20s.`, 56*time.Minute + 20*time.Second},
		{"both, retryDelay longer", `Your quota will reset after 30s. "retryDelay":"90s"`, 90 * time.Second},
		{"resets in", `Individual quota reached. Resets in 10h46m37s.`, 10*time.Hour + 46*time.Minute + 37*time.Second},
		{"resets in, any case", `individual quota reached. RESETS IN 17H32M.`, 17*time.Hour + 32*time.Minute},
		{"resets in and retryDelay, resets longer", `"retryDelay":"56s" Resets in 2h.`, 2 * time.Hour},
		{"all three, longest wins", `Your quota will reset after 1h. Resets in 3h. "retryDelay":"90s"`, 3 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := retryDelay(tc.text)
			if !ok {
				t.Fatalf("retryDelay(%q) found nothing", tc.text)
			}
			if got != tc.want {
				t.Errorf("retryDelay(%q) = %s, want %s", tc.text, got, tc.want)
			}
		})
	}
	if _, ok := retryDelay("no delay here"); ok {
		t.Error("retryDelay found a delay in text that names none")
	}
}

// agyQuotaResetRefusal is what production saw on 2026-10-24 (task 645):
// agy's own sentence, then the redacted diagnostic tail Framework.Run
// appends. Before the reset sentence was read, RetryAfter stayed zero and
// the model was paused for the orchestrator's 15-minute default rather
// than the two and a half hours the quota actually had left.
const agyQuotaResetRefusal = "You have exhausted your capacity on this model. Your quota will reset after 2h28m27s.\n" +
	"AGY diagnostic log (redacted; complete records from the final 64 KiB):\n" +
	"E1024 18:36:02.112 calling model: Error 429, Message: You have exhausted your capacity on this model. Your quota will reset after 2h28m27s., Status: RESOURCE_EXHAUSTED"

// agyIndividualQuotaRefusal is agy's newer wording, as production saw it
// on 2026-11-01 (task 1856, attempt #2; task 1908): before "Resets in"
// was read, each model pause lasted 15 minutes against a 10-17h window.
const agyIndividualQuotaRefusal = "cloudtop-antigravity: usage limit reached; Individual quota reached. " +
	"Please upgrade your subscription to increase your limits. Resets in 10h46m37s. " +
	"AGY diagnostic log (redacted; complete records from the final 64 KiB):\n" +
	"E1101 02:40:21.874 calling model: Error 429, Message: Individual quota reached. " +
	"Please upgrade your subscription to increase your limits. Resets in 10h46m37s., Status: RESOURCE_EXHAUSTED"

func TestUsageLimitFailureReadsAgysQuotaReset(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result string
		runErr error
		want   time.Duration
	}{
		{"production hours", agyQuotaResetRefusal, nil, 2*time.Hour + 28*time.Minute + 27*time.Second},
		{"production minutes", strings.ReplaceAll(agyQuotaResetRefusal, "2h28m27s", "56m20s"), nil, 56*time.Minute + 20*time.Second},
		{"sentence alone", "You have exhausted your capacity on this model. Your quota will reset after 45s.", nil, 45 * time.Second},
		{"in the subprocess error", "", errors.New("exit status 1: " + agyQuotaResetRefusal), 2*time.Hour + 28*time.Minute + 27*time.Second},
		{"individual quota, production", agyIndividualQuotaRefusal, nil, 10*time.Hour + 46*time.Minute + 37*time.Second},
		{"individual quota, sentence alone", "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 17h32m0s.", nil, 17*time.Hour + 32*time.Minute},
		{"individual quota, in the subprocess error", "", errors.New("exit status 1: " + agyIndividualQuotaRefusal), 10*time.Hour + 46*time.Minute + 37*time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := usageLimitFailure(tc.result, tc.runErr)
			if limit == nil {
				t.Fatal("usageLimitFailure = nil, want agy's quota refusal read as a usage limit")
			}
			if limit.RetryAfter != tc.want {
				t.Errorf("RetryAfter = %s, want %s -- the reset agy named", limit.RetryAfter, tc.want)
			}
			if limit.Scope != agent.LimitScopeModel {
				t.Errorf("Scope = %q, want the model's own quota", limit.Scope)
			}
		})
	}
}
