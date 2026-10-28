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

package model

import (
	"testing"
	"time"
)

// TestEndingOfSplitsTheOutcomesThatShareAWord is the whole point of
// RunEnding: "cancelled" and "failed" each cover two endings with
// different fixes, and the only thing that tells them apart is the
// sentence orchestrator recorded beside them -- built here, so that the
// writer and the reader cannot drift.
func TestEndingOfSplitsTheOutcomesThatShareAWord(t *testing.T) {
	// The suffix orchestrator.partialWorkSuffix appends to a detail: what
	// the run got done before it ended. Every marker below has to survive
	// being followed by it.
	const partial = "; the run made 12 tool call(s) [run_command x11, edit_file x1] first"

	for _, tc := range []struct {
		name    string
		outcome string
		detail  string
		want    RunEnding
	}{
		{"the wall-clock cap", "cancelled", RuntimeCapDetail(2*time.Hour) + partial, EndingRuntimeCap},
		{"a human closing the task", "cancelled", TaskClosedDetail, EndingTaskClosed},
		{"a turn stopped by user", "cancelled", TurnStoppedDetail, EndingTurnStopped},
		{"a cancellation that says neither", "cancelled", "", EndingCancelled},
		{"the turn budget", "failed",
			"claude: exceeded max turns (100) without a final answer" + partial, EndingTurnsExhausted},
		{"any other failure", "failed", "the sandbox stopped answering", EndingFailed},
		{"an outcome this build does not know", "orphaned", "", EndingFailed},
		{"a run that produced nothing", "no_action",
			"the run made 3 tool call(s), and finished without pushing a branch", EndingNoAction},
		{"a usage limit", PausedOutcome, "the agent's usage limit was reached", EndingUsageLimit},
		{"a provider with no capacity", CapacityOutcome,
			"antigravity: the provider had no capacity for this run; 503 Service Unavailable", EndingCapacity},
		{"a daemon stopped under the run", InterruptedOutcome, InterruptedDetail + partial, EndingInterrupted},
		{"a host whose credentials expired", EnvironmentOutcome,
			"host credentials expired: run `renew-host-creds` as alice on build-1.example.test, then retry", EndingEnvironment},
		{"a run that never reached its agent", "setup-failed", "cloning: no such repo", EndingSetupFailed},
		{"a run that worked", "succeeded", "the run made 40 tool call(s)", EndingSucceeded},
		{"a run that found nothing needed", NothingNeededOutcome, "the agent found nothing needed", EndingNothingNeeded},
		{"a run with no outcome at all", "", "", EndingUnrecorded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EndingOf(tc.outcome, tc.detail); got != tc.want {
				t.Errorf("EndingOf(%q, %q) = %q, want %q", tc.outcome, tc.detail, got, tc.want)
			}
		})
	}
}

// TestRuntimeCapDetailStillReadsAsASentence guards the half of the round
// trip a marker cannot: RuntimeCapDetail is read by a human on the
// attempt itself, and a marker constant that swallowed the number would
// leave that reader unable to tell a run that ran two minutes over from
// one that ran an hour over.
func TestRuntimeCapDetailStillReadsAsASentence(t *testing.T) {
	got := RuntimeCapDetail(90 * time.Minute)
	if want := "the run exceeded its 1h30m0s wall-clock limit"; got != want {
		t.Errorf("RuntimeCapDetail() = %q, want %q", got, want)
	}
}
