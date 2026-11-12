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
	"context"
	"fmt"
	"slices"
)

// ReviewPriorityFloor is the lowest priority grain files a gating review
// at. A review is short, and it is what frees a finished change to merge,
// so leaving it queued behind ordinary work holds that change -- its
// sandbox, its branch and an owner who already approved it -- for
// nothing. The floor sits two above DefaultTaskPriority, so a review
// jumps ordinary feature work and work nudged up by one,
// and below PriorityReleaseGate (8) and the repairs and release train at
// 9 (Stage1RepairPriority), so a review of an ordinary change never
// takes a slot the train or a red main is waiting for. A review of a
// change that is itself at 8 or 9 inherits that (ReviewPriority).
const ReviewPriorityFloor = 7

// ReviewPriorityHandSetTag marks a review whose own priority a person
// set (ui.Client.UpdateTask): from then on a later edit to the reviewed
// task's priority may raise it but never lowers it (CarryPriorityToReviews).
const ReviewPriorityHandSetTag = "priority-set-by-hand"

// ReviewPriority is the priority grain files a gating review of gated
// at: the gated task's own, or ReviewPriorityFloor when that is lower.
func ReviewPriority(gated Task) int {
	return max(effectivePriority(gated.Priority), ReviewPriorityFloor)
}

func effectivePriority(p int) int {
	if p == 0 {
		return DefaultTaskPriority
	}
	return p
}

// CarriedReviewPriority is the priority an open review at current should
// move to once its gated task's priority has gone from before to after:
// ReviewPriority of the new value, except that a review whose priority a
// person set (handSet) is only ever raised by it. ok is false when the
// review should stay as it is.
func CarriedReviewPriority(current, before, after int, handSet bool) (priority int, ok bool) {
	before, after = effectivePriority(before), effectivePriority(after)
	if before == after {
		return current, false
	}
	want := max(after, ReviewPriorityFloor)
	if handSet && want < current {
		return current, false
	}
	return want, want != current
}

// CarryPriorityToReviews carries a change to gatedID's priority, from
// before to after, onto the open gating reviews grain filed of it
// (ReasonReview, linked LinkProposedBy back to it), as
// CarriedReviewPriority says. It returns the ids of the reviews it moved.
func (s *Store) CarryPriorityToReviews(ctx context.Context, gatedID string, before, after int) ([]string, error) {
	if effectivePriority(before) == effectivePriority(after) {
		return nil, nil
	}
	ids, err := s.OpenTasksWithLinkTarget(ctx, LinkProposedBy, gatedID)
	if err != nil {
		return nil, err
	}
	var moved []string
	for _, id := range ids {
		changed := false
		if err := s.UpdateTask(ctx, id, func(t *Task) error {
			if t.Origin.Reason != ReasonReview {
				return nil
			}
			p, ok := CarriedReviewPriority(t.Priority, before, after, slices.Contains(t.Tags, ReviewPriorityHandSetTag))
			if ok {
				t.Priority, changed = p, true
			}
			return nil
		}); err != nil {
			return moved, fmt.Errorf("carrying %s's priority to its review %s: %w", gatedID, id, err)
		}
		if changed {
			moved = append(moved, id)
		}
	}
	return moved, nil
}
