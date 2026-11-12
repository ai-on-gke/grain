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
	"path"
	"sort"
	"strings"
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
//
// A `.grain/workflows` cron dropped by the repository's CI mode is skipped
// when another enabled schedule on the same target runs the same workflow
// stem on the default branch and is allowed to run under that mode (for
// example a `.grain/schedules/` entry with `grain-ci: true`): the workflow
// keeps its `on: schedule` trigger only because `workflow:` schedules
// require one, so the dropped cron is inert on purpose.
func (s *Store) StaleSchedules(ctx context.Context, now time.Time) ([]StaleSchedule, error) {
	scheds, err := s.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	modes := map[RepoRef]CIMode{}
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
			mode, seen := modes[sched.Target]
			if !seen && sched.Target != (RepoRef{}) && !IsConfigRepo(sched.Target) {
				if cfg, err := s.GetRepoConfig(ctx, sched.Target); err == nil && cfg != nil {
					mode = cfg.CIMode
				}
				modes[sched.Target] = mode
			}
			if mode != "" && !sched.WorkflowRunsUnder(mode) {
				if s.isWorkflowCronSchedule(ctx, sched, stem) && workflowRunByPeer(scheds, sched, stem, mode) {
					continue
				}
				stale.Reason = fmt.Sprintf("%s, so .grain/workflows/%s.yml's cron is dropped at every firing: "+
					"port it to .github/workflows/ or declare a .grain/schedules/ entry with grain-ci: true",
					WorkflowGrainCIOffReason(mode), stem)
			}
		}
		out = append(out, stale)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := staleSince(out[i]), staleSince(out[j]); !a.Equal(b) {
			return a.Before(b)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// isWorkflowCronSchedule reports whether sched is a schedule translated from
// .grain/workflows/<stem>.yml's own `on: schedule` trigger
// (repodecl.scheduledWorkflows), rather than a .grain/schedules/ or settings
// schedule that runs the same workflow without `grain-ci: true`.
func (s *Store) isWorkflowCronSchedule(ctx context.Context, sched Schedule, stem string) bool {
	if strings.TrimSpace(sched.Base) != "" || sched.Target == (RepoRef{}) {
		return false
	}
	if decl, err := s.GetRepoDeclaration(ctx, sched.ID); err == nil && decl != nil {
		return path.Dir(decl.Path) == DeclarationDir+"/workflows"
	}
	prefix := RepoDeclaredID(DeclaredSchedule, sched.Target, "workflow."+stem)
	if sched.ID == prefix {
		return true
	}
	rest, ok := strings.CutPrefix(sched.ID, prefix+".")
	if !ok || rest == "" {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return false
		}
	}
	return true
}

// workflowRunByPeer reports whether another enabled schedule on sched.Target
// runs stem on the default branch and may run under mode.
func workflowRunByPeer(scheds []Schedule, sched Schedule, stem string, mode CIMode) bool {
	for _, peer := range scheds {
		if peer.ID == sched.ID || !peer.Enabled || !peer.Target.Same(sched.Target) {
			continue
		}
		if !onDefaultBranch(peer.Base) || !peer.WorkflowRunsUnder(mode) {
			continue
		}
		if peerStem, ok := workflowReviewSchedule(peer); ok && peerStem == stem {
			return true
		}
	}
	return false
}

func onDefaultBranch(base string) bool {
	base = strings.TrimSpace(base)
	return base == "" || base == "main"
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
