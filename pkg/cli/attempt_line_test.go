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

package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain get` names the model each attempt ran on, beside its framework
// where that is known too, and says nothing about one it was not told.
func TestAttemptLineNamesTheModelItRanOn(t *testing.T) {
	start := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	for _, tc := range []struct {
		a    ui.Attempt
		want string
	}{
		{ui.Attempt{Number: 1, StartedAt: start, FinishedAt: &end, Outcome: "failed", Model: "gemini-3.1-pro", AgentFramework: "antigravity"},
			"  #1 started 2026-10-26T10:00:00Z, finished 2026-10-26T10:01:00Z, failed, on gemini-3.1-pro (antigravity)"},
		{ui.Attempt{Number: 2, StartedAt: start, Model: "gemini-3.1-flash"},
			"  #2 started 2026-10-26T10:00:00Z, still running, on gemini-3.1-flash"},
		{ui.Attempt{Number: 3, StartedAt: start},
			"  #3 started 2026-10-26T10:00:00Z, still running"},
	} {
		if got := attemptLine(tc.a); got != tc.want {
			t.Errorf("attemptLine(%d) = %q, want %q", tc.a.Number, got, tc.want)
		}
		if tc.a.Model == "" && strings.Contains(attemptLine(tc.a), " on ") {
			t.Errorf("attemptLine(%d) names a model it was not given", tc.a.Number)
		}
	}
}
