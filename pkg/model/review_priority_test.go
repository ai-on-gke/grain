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

package model_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestReviewPriority(t *testing.T) {
	t.Parallel()
	if model.ReviewPriorityFloor <= model.DefaultTaskPriority || model.ReviewPriorityFloor >= model.PriorityReleaseGate {
		t.Fatalf("ReviewPriorityFloor = %d, want it above DefaultTaskPriority (%d) and below PriorityReleaseGate (%d)",
			model.ReviewPriorityFloor, model.DefaultTaskPriority, model.PriorityReleaseGate)
	}
	for _, tc := range []struct{ gated, want int }{
		{9, 9}, {8, 8}, {7, 7}, {5, 7}, {3, 7}, {0, 7},
	} {
		if got := model.ReviewPriority(model.Task{Priority: tc.gated}); got != tc.want {
			t.Errorf("ReviewPriority(priority %d) = %d, want %d", tc.gated, got, tc.want)
		}
	}
}

func TestCarriedReviewPriority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                   string
		current, before, after int
		handSet                bool
		want                   int
		wantOK                 bool
	}{
		{"raise carries", 7, 5, 9, false, 9, true},
		{"lower carries to the floor", 9, 9, 3, false, 7, true},
		{"lower within the floor is no move", 7, 5, 3, false, 7, false},
		{"unchanged is no move", 8, 9, 9, false, 8, false},
		{"hand-set is raised", 8, 5, 9, true, 9, true},
		{"hand-set is not lowered", 8, 9, 3, true, 8, false},
		{"hand-set is not lowered by a raise below it", 8, 3, 6, true, 8, false},
		{"hand-set low is raised by a raise", 2, 5, 6, true, 7, true},
	} {
		got, ok := model.CarriedReviewPriority(tc.current, tc.before, tc.after, tc.handSet)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: CarriedReviewPriority(%d, %d, %d, %v) = %d, %v; want %d, %v",
				tc.name, tc.current, tc.before, tc.after, tc.handSet, got, ok, tc.want, tc.wantOK)
		}
	}
}

// A review's lift to ReviewPriorityFloor is grain's, not a person's: it
// must not let the review's run file follow-ups above the default on
// its own permission. A review that inherited more from the change it
// gates, or whose priority a person set, keeps its own ceiling.
func TestReviewFloorIsNotAProposalCeiling(t *testing.T) {
	t.Parallel()
	review := func(priority int, tags ...string) model.Task {
		return model.Task{ID: "9", Priority: priority, Tags: tags, Origin: model.Origin{Reason: model.ReasonReview}}
	}
	for _, tc := range []struct {
		name     string
		parent   model.Task
		priority int
		held     bool
	}{
		{"floored review files at the default", review(model.ReviewPriorityFloor), model.DefaultTaskPriority, false},
		{"floored review files above the default", review(model.ReviewPriorityFloor), model.ReviewPriorityFloor, true},
		{"inherited 9 files at 9", review(9), 9, false},
		{"hand-set 7 files at 7", review(7, model.ReviewPriorityHandSetTag), 7, false},
		{"ordinary task at 7 files at 7", model.Task{ID: "9", Priority: 7}, 7, false},
	} {
		why := model.PersonOnlyProposalSettings(model.Task{Priority: tc.priority}, tc.parent)
		if held := why != ""; held != tc.held {
			t.Errorf("%s: held = %v (%q), want %v", tc.name, held, why, tc.held)
		}
	}
}
