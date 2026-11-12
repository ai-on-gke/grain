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

package orchestrator

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Every gating review -- a declared review, the security review and a
// path-trigger review all build theirs in newReview -- runs at the
// priority of the change it gates, and never below the floor.
func TestReviewInheritsGatedTaskPriority(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	now := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo}); err != nil {
		t.Fatal(err)
	}
	tmpl := model.Template{ID: "every-pr", Name: "Every PR", Body: "Review it."}

	for _, tc := range []struct {
		name     string
		priority int
		want     int
	}{
		{"priority 9 inherits 9", 9, 9},
		{"priority 8 inherits 8", 8, 8},
		{"default rises to the floor", model.DefaultTaskPriority, model.ReviewPriorityFloor},
		{"unset rises to the floor", 0, model.ReviewPriorityFloor},
		{"priority 3 rises to the floor", 3, model.ReviewPriorityFloor},
	} {
		gated := model.Task{ID: "100", Priority: tc.priority, Target: &repo, Title: "change"}
		for _, inPlace := range []bool{false, true} {
			review, err := newReview(ctx, store, gated, tmpl, "Review", "Review body", inPlace, now)
			if err != nil {
				t.Fatalf("%s: newReview: %v", tc.name, err)
			}
			if review.Priority != tc.want {
				t.Errorf("%s (inPlace %v): review Priority = %d, want %d", tc.name, inPlace, review.Priority, tc.want)
			}
		}
	}
}
