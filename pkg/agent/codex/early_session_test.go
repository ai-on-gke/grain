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
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// The thread codex names on its first event is reported as it arrives
// (agent.RunConfig.OnResumableSession), under the same rule as the one on
// the Result: only from a home that outlives the run. A run with no task
// id works in a temp home that goes with it, and reports nothing.
func TestRunReportsItsThreadAsItIsNamedOnlyFromAKeptHome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		taskID string
		want   string
	}{
		{"the task's own home", "42", "t1"},
		{"a temp home", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &attemptRunner{replies: []attemptReply{
				{stdout: attemptStream(agentMessage("done"), `{"type":"turn.completed"}`)},
			}}
			f := newTestFramework(r, "/usr/local/bin/grain", withSessionsIn(t.TempDir()), withoutRetries())
			var reported []string
			if _, err := f.Run(context.Background(), agent.RunConfig{
				Prompt: "do the thing", SandboxRoot: t.TempDir(), TaskID: tc.taskID,
				OnResumableSession: func(s string) { reported = append(reported, s) },
			}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			var got string
			if len(reported) == 1 {
				got = reported[0]
			} else if len(reported) > 1 {
				t.Fatalf("reported %q, want at most one thread", reported)
			}
			if got != tc.want {
				t.Errorf("reported %q, want %q", got, tc.want)
			}
		})
	}
}
