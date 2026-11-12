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
)

// BlockerHold is why a task another one is waiting on (a depends-on or
// child-of link, LinkKind.Blocks) will not close until a person acts on
// it: the blocker's own state, and the hold in a person's words.
type BlockerHold struct {
	// ID is the blocking task.
	ID string
	// State is its state.
	State State
	// Why names what holds it and what ends that: "failed -- Retry or
	// close it".
	Why string
}

// BlockerHoldOf is what holds blocker, in state, open until a person
// acts on it, and false for a blocker grain is still driving towards
// closing -- one queued, running, completed on the merge queue, or
// deferred on a wake time, a goal or a resume_later wait that something
// releases.
//
// A blocking link is satisfied only by its target closing (the
// task_blocked view), and the states this answers true for are the ones
// that never close by themselves: a task whose runs failed
// MaxConsecutiveFailures times in a row, one nobody has approved, one
// waiting on a reply or a Submit, one an operator paused, one deferred
// indefinitely, and a live task that by design never completes
// (NeverCompletes). A task queued on one of those is waiting on that
// person, not on dispatch, and its row should say so (task 4856).
//
// paused is whether an operator has paused blocker (TaskPause), goal
// whether it rests on a goal (TaskGoal), and heldResume whether a
// resume_later condition still holds its deferral (Store.heldResumes);
// the caller reads them, so a list can read each once for every row.
func BlockerHoldOf(blocker Task, state State, paused, goal, heldResume bool) (BlockerHold, bool) {
	hold := BlockerHold{ID: blocker.ID, State: state}
	switch {
	case state == StateClosed || state == "":
		return BlockerHold{}, false
	case paused:
		hold.Why = string(state) + ", paused by an operator -- Resume or close it"
	case NeverCompletes(blocker):
		hold.Why = "never completes by itself -- close it"
	case state == StateFailed:
		hold.Why = "failed -- Retry or close it"
	case state == StateProposed:
		hold.Why = "proposed -- Approve or close it"
	case state == StateAwaitingReply:
		hold.Why = "awaiting a reply -- Reply or close it"
	case state == StateAwaitingSubmit:
		hold.Why = "awaiting Submit -- Submit or close it"
	case state == StateDeferred && blocker.DeferredUntil == nil && !goal && !heldResume:
		hold.Why = "deferred with no wake time -- Undefer or close it"
	default:
		return BlockerHold{}, false
	}
	return hold, true
}

// BlockerHolds is BlockerHoldOf for each of ids, keyed by id, leaving
// out every blocker grain is still driving and every id naming no task.
// It reads each blocker on its own, so a caller with many tasks to
// answer for collects their blockers into one call.
func (s *Store) BlockerHolds(ctx context.Context, ids []string) (map[string]BlockerHold, error) {
	out := map[string]BlockerHold{}
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		t, err := s.GetTask(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("reading blocker %s: %w", id, err)
		}
		if t == nil {
			continue
		}
		state, err := s.State(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("reading blocker %s's state: %w", id, err)
		}
		if state == StateClosed {
			continue
		}
		pause, err := s.GetPause(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("reading blocker %s's pause: %w", id, err)
		}
		var goal, held bool
		if state == StateDeferred && t.DeferredUntil == nil {
			g, err := s.TaskGoal(ctx, id)
			if err != nil {
				return nil, err
			}
			resumes, err := s.heldResumes(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("reading blocker %s's resume_later holds: %w", id, err)
			}
			goal, held = g != nil, len(resumes) > 0
		}
		if hold, ok := BlockerHoldOf(*t, state, pause != nil, goal, held); ok {
			out[id] = hold
		}
	}
	return out, nil
}

// ResumeHeldTasks is every task a resume_later condition still holds
// deferred (Store.heldResumes), as a set: BlockerHoldOf's heldResume for
// a whole list in one query.
func (s *Store) ResumeHeldTasks(ctx context.Context) (map[string]bool, error) {
	held, err := s.heldResumes(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(held))
	for _, h := range held {
		out[h.taskID] = true
	}
	return out, nil
}

// HoldsOf is the holds among blockedBy, in blockedBy's order: the
// blockers of one task that a person has to act on, out of holds read
// for many (BlockerHolds). nil for none.
func HoldsOf(blockedBy []string, holds map[string]BlockerHold) []BlockerHold {
	var out []BlockerHold
	for _, id := range blockedBy {
		if h, ok := holds[id]; ok {
			out = append(out, h)
		}
	}
	return out
}
