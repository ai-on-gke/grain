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

package daemon

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// read_grain_metrics' github section leads with when the budget runs
// out, says when polling is being held back, and names who spent it.
func TestPrintGitHubSaysWhenTheBudgetRunsOutAndWhoSpentIt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 4, 10, 0, 0, time.UTC)
	var out bytes.Buffer
	printGitHub(&out, ui.MetricsGitHub{
		Enabled: true, Sent: 340, Revalidated: 138, Spent: 202,
		Budget: ui.MetricsGitHubBudget{Limit: 5000, Remaining: 600, ResetsAt: now.Add(50 * time.Minute),
			SpendPerHour: 3600, ExhaustedAt: now.Add(10 * time.Minute), Low: true, LowFraction: 0.15},
		Polls: github.PollStats{Tracked: 4, BackedOff: 3, BaseSeconds: 30, CapSeconds: 480, MedianSeconds: 120, MaxSeconds: 480},
		Callers: []ui.MetricsGitHubCaller{{Caller: "sync", Sent: 300, Spent: 200,
			Kinds: []ui.MetricsGitHubCallerKind{{Kind: "head read", Sent: 200, Spent: 180}}}},
	}, now)
	text := out.String()
	for _, want := range []string{"600 of 5000 left", "LOW (under 15%)", "3600/h", "runs out           in 10m0s",
		"poll schedule      3 of 4 PR(s) backed off (median 2m0s, max 8m0s; 30s..8m0s)",
		"sync", "head read 180/200"} {
		if !strings.Contains(text, want) {
			t.Errorf("the github section lacks %q:\n%s", want, text)
		}
	}
	out.Reset()
	printGitHub(&out, ui.MetricsGitHub{}, now)
	if out.Len() != 0 {
		t.Errorf("a UI with no transport printed a github section:\n%s", out.String())
	}
}
