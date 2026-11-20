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
	"strconv"
)

// ObsoleteReview is one review grain filed that is still waiting to run,
// and the task it reviews, which has closed under it.
type ObsoleteReview struct {
	ReviewID   string
	ReviewedID string
}

// HasReviewActivity reports whether the store holds any task that declares a
// review template (Task.ReviewTemplateID) and whether it holds any review task
// grain filed (Filing.Reason == ReasonReview). Both probes read partial
// indexes on `task` so a deployment with no reviews short-circuits review
// reconciliation without scanning open pull requests or history.
func (s *Store) HasReviewActivity(ctx context.Context) (declarations, reviews bool, err error) {
	err = s.db.QueryRowContext(ctx,
		"SELECT "+
			"EXISTS (SELECT 1 FROM `task` WHERE `review_template_id` != ''), "+
			"EXISTS (SELECT 1 FROM `task` WHERE `origin_reason` = ?)",
		string(ReasonReview)).Scan(&declarations, &reviews)
	if err != nil {
		return false, false, fmt.Errorf("checking review activity: %w", err)
	}
	return declarations, reviews, nil
}

// FinishedReviewTaskIDs lists every review task grain filed (Filing.Reason ==
// ReasonReview) that is currently in StateCompleted and carries no fixes-link
// of its own, ordered by task ID. It drives off task_observation's partial
// index on open completed rows instead of reading State for every task.
func (s *Store) FinishedReviewTaskIDs(ctx context.Context) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT `o`.`task_id` FROM `task_observation` AS `o` "+
			"JOIN `task` AS `t` ON `t`.`id` = `o`.`task_id` "+
			"WHERE `o`.`closed_at` IS NULL "+
			"AND `o`.`completed_at` IS NOT NULL "+
			"AND `t`.`origin_reason` = ? "+
			"AND NOT EXISTS (SELECT 1 FROM `task_link` AS `l` WHERE `l`.`task_id` = `o`.`task_id` AND `l`.`kind` = ?) "+
			"AND NOT (`t`.`auto_merge` = FALSE AND EXISTS ("+
			"SELECT 1 FROM `task_link` AS `cl` WHERE `cl`.`task_id` = `o`.`task_id` AND `cl`.`kind` IN ("+changeLinkKindsSQL+"))) "+
			"ORDER BY `o`.`task_id`",
		[]any{string(ReasonReview), string(LinkFixes)},
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing finished review tasks: %w", err)
	}
	return out, nil
}

// OpenPathTriggerReviewsWithDependencies returns every open review task grain
// filed that carries both a LinkDependsOn link and a path-trigger review tag,
// oldest first. Scoping the candidate query to reviews keeps stale-dependency
// release from loading every blocked ordinary task in the store.
func (s *Store) OpenPathTriggerReviewsWithDependencies(ctx context.Context) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT `t`.`id` FROM `task` AS `t` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
			"WHERE `t`.`origin_reason` = ? "+
			"AND `o`.`closed_at` IS NULL "+
			"AND EXISTS (SELECT 1 FROM `task_link` AS `l` WHERE `l`.`task_id` = `t`.`id` AND `l`.`kind` = ?) "+
			"AND EXISTS (SELECT 1 FROM `task_tag` AS `tg` WHERE `tg`.`task_id` = `t`.`id` AND `tg`.`tag` LIKE ?) "+
			"ORDER BY `t`.`created_at`, `t`.`id`",
		[]any{string(ReasonReview), string(LinkDependsOn), "path-trigger-review:%"},
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing open path-trigger reviews with dependencies: %w", err)
	}
	return out, nil
}

// ObsoleteReviews lists every review grain filed (Filing.Reason
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
// One query rather than a GetTask per open task, driven off task's partial
// index on origin_reason = 'review' and direct observation/run predicates
// instead of joining the task_state view across history.
func (s *Store) ObsoleteReviews(ctx context.Context) ([]ObsoleteReview, error) {
	var out []ObsoleteReview
	err := each(ctx, s.db,
		"SELECT `t`.`id`, `l`.`target` FROM `task` AS `t` "+
			"JOIN `task_link` AS `l` ON `l`.`task_id` = `t`.`id` AND `l`.`kind` = ? "+
			"JOIN `task` AS `target_t` ON `target_t`.`id` = `l`.`target` "+
			"JOIN `task_observation` AS `tobs` ON `tobs`.`task_id` = `l`.`target` AND `tobs`.`closed_at` IS NOT NULL "+
			"LEFT JOIN `task_observation` AS `ro` ON `ro`.`task_id` = `t`.`id` "+
			"WHERE `t`.`origin_reason` = ? "+
			"AND `ro`.`closed_at` IS NULL "+
			"AND (`ro`.`completed_at` IS NULL OR (`t`.`auto_merge` = FALSE AND EXISTS ("+
			"SELECT 1 FROM `task_link` AS `cl` WHERE `cl`.`task_id` = `t`.`id` AND `cl`.`kind` IN ("+changeLinkKindsSQL+")))) "+
			"AND (`ro`.`completed_at` IS NOT NULL OR `ro`.`pending_question_comment_id` IS NOT NULL OR ("+
			"NOT EXISTS (SELECT 1 FROM `task_run` AS `r` WHERE `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL) "+
			"AND (COALESCE(`ro`.`streak_count`, 0) >= "+strconv.Itoa(MaxConsecutiveFailures)+" OR `t`.`deferred_at` IS NULL))) "+
			"AND NOT EXISTS (SELECT 1 FROM `task_link` AS `f` WHERE `f`.`task_id` = `t`.`id` AND `f`.`kind` = ?) "+
			"ORDER BY `t`.`id`",
		[]any{string(LinkProposedBy), string(ReasonReview), string(LinkFixes)},
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
