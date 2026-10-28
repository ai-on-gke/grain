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
	"fmt"
)

// ObsoleteReview is one review grain filed that is still waiting to run,
// and the task it reviews, which has closed under it.
type ObsoleteReview struct {
	ReviewID   string
	ReviewedID string
}

// ObsoleteReviews lists every review grain filed (Origin.Reason
// ReasonReview) whose reviewed task -- the one its LinkProposedBy names --
// has closed, while the review itself has not settled and is not running,
// and opened no pull request of its own.
//
// A reviewed task closes when its pull request merges or is closed
// (orchestrator's recordPullRequestEvents), and when a human closes it.
// Either way the branch the review is stacked on is normally gone with
// it, and a review left queued fails on every dispatch looking for that
// branch. Running reviews are left out: a run already under way finishes,
// and closeFinishedReviews closes it once it has. Reviews with a pull
// request of their own are left out for closeSecurityReviewOnSettle's
// reason: what happens to commits stacked on a branch that has gone is
// the merge queue's question.
//
// One query rather than a GetTask per open task: this is read every
// cycle, and the backlog is much larger than the set it returns.
func (s *Store) ObsoleteReviews(ctx context.Context) ([]ObsoleteReview, error) {
	var out []ObsoleteReview
	err := each(ctx, s.db,
		"SELECT `t`.`id`, `l`.`target` FROM `task` AS `t` "+
			"JOIN `task_link` AS `l` ON `l`.`task_id` = `t`.`id` AND `l`.`kind` = ? "+
			"JOIN `task_state` AS `rs` ON `rs`.`task_id` = `t`.`id` "+
			"JOIN `task_state` AS `ts` ON `ts`.`task_id` = `l`.`target` "+
			"WHERE `t`.`origin_reason` = ? AND `ts`.`state` = ? "+
			"AND `rs`.`state` NOT IN (?, ?, ?, ?) "+
			"AND NOT EXISTS (SELECT 1 FROM `task_link` AS `f` WHERE `f`.`task_id` = `t`.`id` AND `f`.`kind` = ?) "+
			"ORDER BY `t`.`id`",
		[]any{string(LinkProposedBy), string(ReasonReview), string(StateClosed),
			string(StateClosed), string(StateCompleted), string(StateDeferred), string(StateRunning),
			string(LinkFixes)},
		func(rows *sql.Rows) error {
			var r ObsoleteReview
			if err := rows.Scan(&r.ReviewID, &r.ReviewedID); err != nil {
				return err
			}
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing reviews of closed tasks: %w", err)
	}
	return out, nil
}
