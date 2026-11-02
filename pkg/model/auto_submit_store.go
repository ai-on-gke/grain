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
	"database/sql"
	"errors"
	"fmt"
)

// ExceededAutoSubmitLimit reports which of limits filing one more
// self-approved proposal under proposer would exceed --
// AutoSubmitWithinLimits when neither, which is the ordinary answer and
// the one that leaves the proposer's own permission (AutoSubmits) to
// decide.
//
// Asked per proposal rather than once per batch, deliberately: both
// filing paths write each proposal before deciding the next one, so the
// count below already includes the siblings filed a moment ago in the
// same run, and neither path has to keep a tally of its own that the
// other would not have.
//
// Depth is checked first because it is the more informative answer: a
// chain that has run out of depth says the tree is deep, where the
// fan-out of its last task says only that this particular task has been
// busy.
//
// Both halves are read off the store rather than off the Task in hand,
// so a caller needs nothing hydrated and the two paths cannot disagree
// by holding differently-loaded copies of the same parent.
func (s *Store) ExceededAutoSubmitLimit(ctx context.Context, proposer string,
	limits AutoSubmitLimits) (AutoSubmitLimit, error) {

	limits = limits.Resolved()

	// Walk LinkProposedBy up from the proposer, counting the unbroken
	// run of self-approved tasks it stands at the end of. The proposal
	// being decided would be one level below that run, so a run already
	// as long as the limit is where it stops.
	//
	// Bounded by the limit itself, which is what keeps this a couple of
	// queries and not a walk to the root: nothing above the point the
	// count is reached can change the answer. A chain broken by a human
	// approval -- or by a task nobody approved, or by one grain filed
	// itself -- ends the walk, because a person deciding is exactly what
	// this is counting the distance from.
	id := proposer
	for depth := 0; depth < limits.Depth; depth++ {
		selfApproved, parent, err := s.selfApprovedProposer(ctx, id)
		if err != nil {
			return AutoSubmitWithinLimits, err
		}
		if !selfApproved {
			break
		}
		if depth+1 >= limits.Depth {
			return AutoSubmitDepthReached, nil
		}
		if parent == "" {
			break
		}
		id = parent
	}

	n, err := s.selfApprovedProposalCount(ctx, proposer)
	if err != nil {
		return AutoSubmitWithinLimits, err
	}
	if n >= limits.FanOut {
		return AutoSubmitFanOutReached, nil
	}
	return AutoSubmitWithinLimits, nil
}

// selfApprovedProposer answers both halves of one step of that walk:
// whether the task id names was itself queued with nobody looking
// (SelfApproved), and which task proposed it.
//
// A task with no row at all reads as neither, which ends the walk: the
// chain above a task that has been deleted is not something to guess at,
// and stopping there errs toward the direction a trust bound should fail
// in -- withholding nothing, since the count so far still stands.
//
// One proposed-by link is all a proposal ever carries (both filing paths
// write exactly one), so the subselect takes the first by target for a
// deterministic answer rather than pretending to handle a second.
func (s *Store) selfApprovedProposer(ctx context.Context, id string) (selfApproved bool, proposer string, err error) {
	var actorKind, actorID, behalfKind, behalfID, parent sql.NullString
	err = s.db.QueryRowContext(ctx,
		"SELECT `approval_actor_kind`,`approval_actor_id`,`approval_behalf_kind`,`approval_behalf_id`,"+
			"(SELECT `target` FROM `task_link` WHERE `task_id` = `task`.`id` AND `kind` = ? "+
			"ORDER BY `target` LIMIT 1) "+
			"FROM `task` WHERE `id` = ?",
		string(LinkProposedBy), id,
	).Scan(&actorKind, &actorID, &behalfKind, &behalfID, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("reading how task %s was approved: %w", id, err)
	}
	var t Task
	if actorKind.Valid {
		t.Approval = &Attribution{
			Actor:      Principal{Kind: PrincipalKind(actorKind.String), ID: actorID.String},
			OnBehalfOf: principalFrom(behalfKind, behalfID, ""),
		}
	}
	return SelfApproved(t), parent.String, nil
}

// selfApprovedProposalCount is how many proposals this task has already
// had queued without anybody approving them -- its fan-out, as
// AutoSubmitLimits.FanOut counts it.
//
// Over the task's whole life, closed and completed children included:
// the bound is on how much work one permission may create, and work
// already done was still created by it.
func (s *Store) selfApprovedProposalCount(ctx context.Context, proposer string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_link` AS `l` JOIN `task` AS `t` ON `t`.`id` = `l`.`task_id` "+
			"WHERE `l`.`kind` = ? AND `l`.`target` = ? "+
			"AND `t`.`approval_actor_kind` = ? AND `t`.`approval_actor_id` IN (?,?) "+
			// The two spellings principalFrom reads as "nobody", so this
			// selects exactly the rows SelfApproved accepts in Go.
			"AND (`t`.`approval_behalf_kind` IS NULL OR `t`.`approval_behalf_kind` = '')",
		string(LinkProposedBy), proposer, string(PrincipalAutomation),
		AutoSubmitPrincipal.ID, ConsoleSessionPrincipal.ID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting the proposals task %s has had queued unreviewed: %w", proposer, err)
	}
	return n, nil
}
