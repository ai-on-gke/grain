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

package forge_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
)

func strPtr(s string) *string { return &s }

func TestIsExternalCheckClassifiesThirdPartyChecksOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		check         github.CheckRun
		requiredCheck string
		want          bool
	}{
		{
			name:  "cla/google from google-cla app is external",
			check: github.CheckRun{Name: "cla/google", Status: "queued", App: "google-cla"},
			want:  true,
		},
		{
			name:  "codecov app check is external",
			check: github.CheckRun{Name: "codecov/project", Status: "in_progress", App: "codecov"},
			want:  true,
		},
		{
			name:  "github-actions app is never external",
			check: github.CheckRun{Name: "Tests / unit", Status: "in_progress", App: "github-actions"},
			want:  false,
		},
		{
			name: "actions html_url is never external",
			check: github.CheckRun{
				Name:    "build",
				Status:  "in_progress",
				App:     "custom-runner",
				HTMLURL: "https://github.com/acme/widgets/actions/runs/12345/job/67890",
			},
			want: false,
		},
		{
			name:          "grain required state check is never external",
			check:         github.CheckRun{Name: "grain-state-check", Status: "queued", App: "grain-bot"},
			requiredCheck: "grain-state-check",
			want:          false,
		},
		{
			name:  "grain CI check is never external",
			check: github.CheckRun{Name: "grain/ci", Status: "in_progress", App: "grain"},
			want:  false,
		},
		{
			name:  "synthetic check without app is not treated as external",
			check: github.CheckRun{Name: "deploy approval", Status: "queued"},
			want:  false,
		},
	}
	for _, tc := range cases {
		if got := forge.IsExternalCheck(tc.check, tc.requiredCheck); got != tc.want {
			t.Errorf("%s: IsExternalCheck(%+v, %q) = %v, want %v",
				tc.name, tc.check, tc.requiredCheck, got, tc.want)
		}
	}
}

func TestExternalChecksStalledResetsOnProgressAndNewCheckRuns(t *testing.T) {
	t.Parallel()
	ref := forge.Ref{Owner: "acme", Repo: "widgets", Number: 99101}
	defer forge.ForgetExternalChecks(ref)

	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	checks := []github.CheckRun{
		{ID: 1, Name: "Tests / unit", Status: "completed", Conclusion: strPtr("success"), App: "github-actions"},
		{ID: 2, Name: "cla/google", Status: "queued", App: "google-cla"},
	}

	pending, waited, stalled, threshold := forge.ExternalChecksStalledWithin(ref, "sha-1", checks, "", forge.GrainCI{}, t0, 0)
	if len(pending) != 1 || pending[0].Name != "cla/google" || stalled || waited != 0 || threshold != forge.DefaultExternalCheckStallThreshold {
		t.Fatalf("at t0: pending=%v waited=%v stalled=%v threshold=%v", pending, waited, stalled, threshold)
	}

	// 20 minutes without progress: not stalled yet.
	_, waited, stalled, _ = forge.ExternalChecksStalledWithin(ref, "sha-1", checks, "", forge.GrainCI{}, t0.Add(20*time.Minute), 0)
	if stalled || waited != 20*time.Minute {
		t.Fatalf("at 20m: waited=%v stalled=%v, want 20m false", waited, stalled)
	}

	// A new check run appearing on the same head counts as progress and resets the clock.
	withNewRun := append([]github.CheckRun{}, checks...)
	withNewRun = append(withNewRun, github.CheckRun{
		ID: 3, Name: "lint", Status: "completed", Conclusion: strPtr("success"), App: "github-actions",
	})
	_, waited, stalled, _ = forge.ExternalChecksStalledWithin(ref, "sha-1", withNewRun, "", forge.GrainCI{}, t0.Add(25*time.Minute), 0)
	if stalled || waited != 0 {
		t.Fatalf("after new check run at 25m: waited=%v stalled=%v, want 0 false", waited, stalled)
	}

	// 29 minutes after that reset (54m total): still under 30m since last progress.
	_, waited, stalled, _ = forge.ExternalChecksStalledWithin(ref, "sha-1", withNewRun, "", forge.GrainCI{}, t0.Add(54*time.Minute), 0)
	if stalled || waited != 29*time.Minute {
		t.Fatalf("at 54m (29m since progress): waited=%v stalled=%v, want 29m false", waited, stalled)
	}

	// 31 minutes after last progress (56m total): stalled.
	_, waited, stalled, _ = forge.ExternalChecksStalledWithin(ref, "sha-1", withNewRun, "", forge.GrainCI{}, t0.Add(56*time.Minute), 0)
	if !stalled || waited != 31*time.Minute {
		t.Fatalf("at 56m (31m since progress): waited=%v stalled=%v, want 31m true", waited, stalled)
	}

	// While an in-repo Actions check is still running, external stall does not fire.
	actionsRunning := []github.CheckRun{
		{ID: 1, Name: "Tests / unit", Status: "in_progress", App: "github-actions"},
		{ID: 2, Name: "cla/google", Status: "queued", App: "google-cla"},
	}
	pending, _, stalled, _ = forge.ExternalChecksStalledWithin(ref, "sha-1", actionsRunning, "", forge.GrainCI{}, t0.Add(90*time.Minute), 0)
	if len(pending) != 0 || stalled {
		t.Fatalf("with Actions running: pending=%v stalled=%v, want empty false", pending, stalled)
	}

	// While grain CI is required and has not passed, external stall does not fire.
	pending, _, stalled, _ = forge.ExternalChecksStalledWithin(
		ref, "sha-1", withNewRun, "", forge.GrainCI{Required: true, Running: true}, t0.Add(90*time.Minute), 0,
	)
	if len(pending) != 0 || stalled {
		t.Fatalf("with grain CI unfinished: pending=%v stalled=%v, want empty false", pending, stalled)
	}
}
