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

package ui_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A gate override that does not name ci keeps waiting for checks on the
// forced commit (orchestrator.forcedMergeKeepsCI), so ForceMergeWithGates
// must not cancel an in-flight grain CI run unless ci itself was overridden.
func TestAPartialGateOverrideKeepsRunningGrainCIAlive(t *testing.T) {
	t.Parallel()
	const head = "cccccccccccccccccccccccccccccccccccccccc"
	for name, tc := range map[string]struct {
		gates      []string
		wantCancel bool
	}{
		"review only":   {gates: []string{"review"}, wantCancel: false},
		"review and ci": {gates: []string{"review", "ci"}, wantCancel: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, store, ctx := testClient(t)
			runner := &fakeCIRunner{}
			c.Config.CI = runner
			c.Config.GitHubPullRequests = prHead(head)
			task := create(t, c, ctx)
			if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
				tk.AutoMerge = true
				tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#91"})
				model.AddReviewFindings(tk, model.ReviewFindingsRecord{
					Gate:       model.GateReview,
					ReviewTask: "801",
					Summary:    "open finding",
					At:         baseTime,
				})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			done := time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC)
			if err := store.Observe(ctx, model.Observation{
				TaskID:      task.ID,
				CompletedAt: &done,
				CI:          &model.CIStatus{RunID: "run-live", Commit: head},
			}); err != nil {
				t.Fatal(err)
			}

			if err := c.ForceMergeWithGates(ctx, task.ID, "release blocker", head, tc.gates); err != nil {
				t.Fatalf("ForceMergeWithGates(%v): %v", tc.gates, err)
			}
			if tc.wantCancel {
				if len(runner.cancelled) != 1 || runner.cancelled[0] != "run-live" || runner.sources[0] != ci.CancelBySystem {
					t.Fatalf("cancelled = %v (%v), want [run-live] by system", runner.cancelled, runner.sources)
				}
			} else if len(runner.cancelled) != 0 {
				t.Fatalf("cancelled = %v, want in-flight grain CI kept running when ci was not overridden", runner.cancelled)
			}
		})
	}
}
