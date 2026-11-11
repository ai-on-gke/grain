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
	"sort"
	"time"
)

// StaleSchedule is an enabled schedule that has gone more than twice its
// cadence without a firing that ran anything (Schedule.LastRunAt): the
// shape of ai-on-gke/grain's live real-model suites, whose
// `.grain/workflows` crons kept coming due after the repository moved to
// ci_mode: github and were dropped every night (suppressWorkflowGrainCIOff)
// -- for two weeks, with nothing anywhere saying so.
//
// A dropped firing advances NextRunAt and leaves LastRunAt alone, which is
// why LastRunAt is the reading: it is the one field a schedule that
// silently does nothing cannot keep fresh.
type StaleSchedule struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Repo  RepoRef `json:"repo"`
	// Workflow is the `.grain/workflows` stem a workflow schedule fires,
	// empty for any other schedule.
	Workflow string `json:"workflow,omitempty"`
	// Cadence is the gap between two of the schedule's occurrences.
	Cadence time.Duration `json:"cadence"`
	// LastRunAt is its last firing that ran anything; nil if it never
	// has, in which case staleness is counted from CreatedAt.
	LastRunAt *time.Time `json:"lastRunAt,omitempty"`
	// Reason says why it is not running, where grain can tell.
	Reason string `json:"reason,omitempty"`
}

// StaleSchedules lists every enabled schedule whose last firing that ran
// anything -- or, for one that never has, its creation -- is more than
// twice its cadence before now, oldest first.
func (s *Store) StaleSchedules(ctx context.Context, now time.Time) ([]StaleSchedule, error) {
	scheds, err := s.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	modes := map[RepoRef]CIMode{}
	modeOf := func(repo RepoRef) CIMode {
		mode, seen := modes[repo]
		if !seen && repo != (RepoRef{}) {
			if cfg, err := s.GetRepoConfig(ctx, repo); err == nil && cfg != nil {
				mode = cfg.CIMode
			}
			modes[repo] = mode
		}
		return mode
	}
	// covered is every workflow some enabled schedule does run under its
	// repository's CI mode: the .grain/schedules/ entry with grain-ci: true
	// the reason below tells a reader to declare. Once it exists the
	// workflow's own cron, still dropped at every firing, is not a
	// schedule that stopped running -- the workflow is running, from the
	// other schedule, which is judged on its own.
	type workflowKey struct {
		repo       RepoRef
		stem, base string
	}
	covered := map[workflowKey]bool{}
	for _, sched := range scheds {
		if stem, ok := workflowReviewSchedule(sched); ok && sched.Enabled && sched.WorkflowRunsUnder(modeOf(sched.Target)) {
			covered[workflowKey{sched.Target, stem, sched.Base}] = true
		}
	}
	var out []StaleSchedule
	for _, sched := range scheds {
		if !sched.Enabled {
			continue
		}
		cadence := scheduleCadence(sched, now)
		if cadence <= 0 {
			continue
		}
		since := sched.CreatedAt
		if sched.LastRunAt != nil {
			since = *sched.LastRunAt
		}
		if since.IsZero() || now.Sub(since) <= 2*cadence {
			continue
		}
		stale := StaleSchedule{ID: sched.ID, Title: sched.Title, Repo: sched.Target, Cadence: cadence, LastRunAt: sched.LastRunAt}
		if stem, ok := workflowReviewSchedule(sched); ok {
			stale.Workflow = stem
			mode := modeOf(sched.Target)
			if mode != "" && !sched.WorkflowRunsUnder(mode) {
				if covered[workflowKey{sched.Target, stem, sched.Base}] {
					continue
				}
				stale.Reason = fmt.Sprintf("%s, so .grain/workflows/%s.yml's cron is dropped at every firing: "+
					"port it to .github/workflows/ or declare a .grain/schedules/ entry with grain-ci: true",
					WorkflowGrainCIOffReason(mode), stem)
			}
		}
		if stale.Reason == "" && sched.OverlapGroup != "" {
			// Held by another schedule in its overlap group: still not
			// running, but the holder is the thing to look at.
			if sup, err := s.SuppressedFiring(ctx, sched, now, sched.Location()); err == nil && sup != nil && sup.HeldByScheduleID != "" {
				stale.Reason = "held: " + sup.Detail
			}
		}
		out = append(out, stale)
	}
	sort.Slice(out, func(i, j int) bool { return staleSince(out[i]).Before(staleSince(out[j])) })
	return out, nil
}

func staleSince(s StaleSchedule) time.Time {
	if s.LastRunAt != nil {
		return *s.LastRunAt
	}
	return time.Time{}
}

// scheduleCadence is the gap between sched's next two occurrences after
// now; zero for a recurrence with no next occurrence.
func scheduleCadence(sched Schedule, now time.Time) time.Duration {
	loc := sched.Location()
	first := sched.Recurrence.Next(now, loc)
	if first.IsZero() || !first.After(now) {
		return 0
	}
	second := sched.Recurrence.Next(first, loc)
	if !second.After(first) {
		return 0
	}
	return second.Sub(first)
}
