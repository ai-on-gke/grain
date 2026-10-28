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
	"sort"
	"strings"
	"time"
)

// How many changes each red stretch of a base branch held, and for how
// long (grain/task-986). While a branch's post-merge run is red, the
// changes queued for it sit in base_broken (orchestrator's
// mergeStateBaseBroken) until its repair lands; that state is written on
// each task's row and overwritten the moment it ends, so nothing could say
// afterwards whether a repair that took four hours held one change or
// twelve -- which is the difference between a repair worth hurrying and
// one nobody was waiting on.
//
// One row per *pass*: a stretch of consecutive merge-queue cycles in which
// the branch was broken or had a change held on it. The row is open
// (FinishedAt nil) while the pass lasts and is updated once a cycle by
// orchestrator.recordBaseHolds; it is closed by the first cycle that
// finds the branch green with nothing held. Bounded like
// merge_batch_history: a sample to read a distribution off, not an audit
// log.

// BaseHoldHistoryKept is how many finished passes keep their record. Open
// passes are never trimmed -- at most one per branch, and each is a pass
// still being measured.
const BaseHoldHistoryKept = 200

// BaseHoldPass is one red stretch of one base branch and what it held.
type BaseHoldPass struct {
	ID     int64
	Repo   RepoRef
	Branch string
	// RepairTaskID is the base branch's repair task as last seen during
	// the pass (BaseBranchCI.RepairTaskID), empty while none was filed --
	// a red that cleared on its confirmation run never has one.
	RepairTaskID string
	// StartedAt is the first cycle that saw the pass, SampledAt the latest,
	// and FinishedAt the cycle that found it over -- nil while it lasts.
	StartedAt  time.Time
	SampledAt  time.Time
	FinishedAt *time.Time
	// Held is how many changes were in base_broken on the branch at the
	// latest sample, Peak the most at any one sample, and HeldTasks every
	// change that was held at any point, so Total is how many different
	// changes the pass cost a wait.
	Held      int
	Peak      int
	HeldTasks []string
	// PRSeconds is the area under Held: each sample's count times the time
	// to the next one, summed. A daemon that was down between two samples
	// credits the gap to the count it last saw, since those changes were
	// still not merging.
	PRSeconds int64
}

// Total is how many different changes the pass held.
func (p BaseHoldPass) Total() int { return len(p.HeldTasks) }

// PRHeld is PRSeconds as a duration: PR-time held, so a pass holding two
// changes for an hour reads as two hours.
func (p BaseHoldPass) PRHeld() time.Duration { return time.Duration(p.PRSeconds) * time.Second }

// Sample folds one cycle's held changes into the pass: the time since the
// last sample is credited to the count that sample saw, and held becomes
// the count for the next stretch.
func (p *BaseHoldPass) Sample(held []string, now time.Time) {
	if gap := now.Sub(p.SampledAt); gap > 0 {
		p.PRSeconds += int64(p.Held) * int64(gap/time.Second)
	}
	p.SampledAt = now
	p.Held = len(held)
	if p.Held > p.Peak {
		p.Peak = p.Held
	}
	seen := make(map[string]bool, len(p.HeldTasks))
	for _, id := range p.HeldTasks {
		seen[id] = true
	}
	for _, id := range held {
		if !seen[id] {
			seen[id] = true
			p.HeldTasks = append(p.HeldTasks, id)
		}
	}
	sort.Strings(p.HeldTasks)
}

// Finish closes the pass at now, crediting the last stretch.
func (p *BaseHoldPass) Finish(now time.Time) {
	p.Sample(nil, now)
	at := now
	p.FinishedAt = &at
}

// PutBaseHoldPass writes one pass: a new row for a pass with no ID, which
// it returns, and an update of its own row otherwise. Closing a pass trims
// the finished history back to BaseHoldHistoryKept rows in the same write.
func (s *Store) PutBaseHoldPass(ctx context.Context, p BaseHoldPass) (int64, error) {
	id := p.ID
	err := s.write(ctx, "record base branch hold for "+p.Repo.String()+" "+p.Branch,
		func(tx *sql.Tx) error {
			var finished any
			if p.FinishedAt != nil {
				finished = p.FinishedAt.UTC()
			}
			tasks := strings.Join(p.HeldTasks, ",")
			if id == 0 {
				res, err := tx.ExecContext(ctx,
					"INSERT INTO `base_hold_history` (`owner`,`name`,`branch`,`repair_task_id`,"+
						"`started_at`,`sampled_at`,`finished_at`,`held`,`peak`,`total`,`held_tasks`,"+
						"`pr_seconds`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
					p.Repo.Owner, p.Repo.Name, p.Branch, p.RepairTaskID, p.StartedAt.UTC(),
					p.SampledAt.UTC(), finished, p.Held, p.Peak, p.Total(), tasks, p.PRSeconds)
				if err != nil {
					return err
				}
				if id, err = res.LastInsertId(); err != nil {
					return err
				}
			} else if _, err := tx.ExecContext(ctx,
				"UPDATE `base_hold_history` SET `repair_task_id` = ?, `sampled_at` = ?, "+
					"`finished_at` = ?, `held` = ?, `peak` = ?, `total` = ?, `held_tasks` = ?, "+
					"`pr_seconds` = ? WHERE `id` = ?",
				p.RepairTaskID, p.SampledAt.UTC(), finished, p.Held, p.Peak, p.Total(), tasks,
				p.PRSeconds, id); err != nil {
				return err
			}
			if p.FinishedAt == nil {
				return nil
			}
			_, err := tx.ExecContext(ctx,
				"DELETE FROM `base_hold_history` WHERE `finished_at` IS NOT NULL AND `id` NOT IN "+
					"(SELECT `id` FROM `base_hold_history` WHERE `finished_at` IS NOT NULL "+
					"ORDER BY `id` DESC LIMIT ?)",
				BaseHoldHistoryKept)
			return err
		})
	return id, err
}

// OpenBaseHoldPasses is every pass still being measured -- at most one per
// branch.
func (s *Store) OpenBaseHoldPasses(ctx context.Context) ([]BaseHoldPass, error) {
	return s.baseHoldPasses(ctx, "WHERE `finished_at` IS NULL ")
}

// BaseHoldPasses is the whole kept history, open passes included, newest
// first -- at most BaseHoldHistoryKept finished rows plus one open row per
// branch, so it is read entire rather than paged.
func (s *Store) BaseHoldPasses(ctx context.Context) ([]BaseHoldPass, error) {
	return s.baseHoldPasses(ctx, "")
}

func (s *Store) baseHoldPasses(ctx context.Context, where string) ([]BaseHoldPass, error) {
	var out []BaseHoldPass
	err := each(ctx, s.db,
		"SELECT `id`,`owner`,`name`,`branch`,`repair_task_id`,`started_at`,`sampled_at`,"+
			"`finished_at`,`held`,`peak`,`held_tasks`,`pr_seconds` FROM `base_hold_history` "+
			where+"ORDER BY `id` DESC", nil,
		func(rows *sql.Rows) error {
			var p BaseHoldPass
			var started, sampled time.Time
			var finished sql.NullTime
			var tasks string
			if err := rows.Scan(&p.ID, &p.Repo.Owner, &p.Repo.Name, &p.Branch, &p.RepairTaskID,
				&started, &sampled, &finished, &p.Held, &p.Peak, &tasks, &p.PRSeconds); err != nil {
				return err
			}
			p.StartedAt, p.SampledAt = started.UTC(), sampled.UTC()
			if finished.Valid {
				at := finished.Time.UTC()
				p.FinishedAt = &at
			}
			if tasks != "" {
				p.HeldTasks = strings.Split(tasks, ",")
			}
			out = append(out, p)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading the base branch hold history: %w", err)
	}
	return out, nil
}
