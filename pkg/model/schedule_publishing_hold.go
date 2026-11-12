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
	"strings"
	"time"
)

// A schedule held while its base branch's publishing is (grain/task-4393).
//
// The hold itself is BaseBranchPublishingFindings': a branch that has
// published nothing for long enough to be a finding, with a release triage
// task open on it, is Gated, and deployment health says "further releases
// on this branch are held pending resolution of triage task N". A
// schedule that declares `skip-when: publishing-held` (SkipWhenPublishingHeld)
// is held by the same finding, so health and the schedule cannot disagree
// about whether publishing is held.
//
// Held the way an overlap skip is, not dropped: NextRunAt stays where it
// was, every cycle re-asks, the occurrence is one ScheduleSkip row however
// many cycles re-skip it, and the first cycle after the triage task closes
// fires it once (resolveHeldScheduleSkips records that as fired late). A
// firing somebody asked for by hand (FireScheduleNow) is not held: a
// person pressing Run now has decided the hold does not apply.

// publishingHold is the finding holding sched's firing into repo's branch,
// or nil when there is none -- always nil for a schedule that has not said
// `skip-when: publishing-held`, which asks the store nothing.
//
// branch is the base the firing goes into, empty for the repository's
// default; a finding about another branch of the same repository does not
// hold it.
func (s *Store) publishingHold(ctx context.Context, sched Schedule, repo RepoRef, branch string,
	now time.Time) (*BaseBranchPublishing, error) {

	if sched.SkipWhen != SkipWhenPublishingHeld || repo == (RepoRef{}) {
		return nil, nil
	}
	findings, err := s.BaseBranchPublishingFindings(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("checking whether %s's publishing is held: %w", repo, err)
	}
	branch = strings.TrimSpace(branch)
	for i := range findings {
		f := findings[i]
		if f.Repo != repo.String() || !f.Gated || f.TriageTaskID == "" {
			continue
		}
		if branch != "" && f.Branch != "" && f.Branch != branch {
			continue
		}
		return &f, nil
	}
	return nil, nil
}

// publishingHeldFiring is publishingHold as the ScheduleFiring a
// fireXSchedule returns for it, or nil. advance false (Run now, a wake)
// is never held.
func (s *Store) publishingHeldFiring(ctx context.Context, sched Schedule, repo RepoRef, branch string,
	now time.Time, advance bool) (*ScheduleFiring, error) {

	if !advance {
		return nil, nil
	}
	hold, err := s.publishingHold(ctx, sched, repo, branch, now)
	if err != nil || hold == nil {
		return nil, err
	}
	return &ScheduleFiring{Skipped: true, Skip: publishingHeldSkip(sched, now, *hold)}, nil
}

// PublishingHeldSkip is the ScheduleSkip a firing an event rather than a
// cadence asked for (a github_workflow_run trigger) records when sched's
// publishing is held, and false when it is not: there is no occurrence to
// hold, so the firing is dropped and recorded as such, as
// DroppedOverlapGroupSkip's is.
func (s *Store) PublishingHeldSkip(ctx context.Context, sched Schedule, repo RepoRef, branch string,
	now time.Time) (ScheduleSkip, bool, error) {

	hold, err := s.publishingHold(ctx, sched, repo, branch, now)
	if err != nil || hold == nil {
		return ScheduleSkip{}, false, err
	}
	sched.NextRunAt = now
	k := publishingHeldSkip(sched, now, *hold)
	k.Outcome, k.ResolvedAt = ScheduleSkipDropped, &now
	return *k, true, nil
}

// publishingHeldSkip is the record of an occurrence hold held.
func publishingHeldSkip(sched Schedule, now time.Time, hold BaseBranchPublishing) *ScheduleSkip {
	return &ScheduleSkip{ScheduleID: sched.ID, Occurrence: sched.NextRunAt, At: now,
		Reason: SuppressedByPublishingHeld, TaskID: hold.TriageTaskID, Detail: publishingHeldDetail(hold)}
}

// publishingHeldDetail is the sentence the schedules page and the skip
// record show: "publishing held by task N", and on what.
func publishingHeldDetail(hold BaseBranchPublishing) string {
	branch := hold.Branch
	if branch == "" {
		branch = "its default branch"
	}
	return fmt.Sprintf("publishing held by task %s (%s %s has published nothing since %s)",
		hold.TriageTaskID, hold.Repo, branch, hold.FailingSince.UTC().Format("2006-01-02 15:04 MST"))
}

// publishingSuppression is publishingHold for SuppressedFiring: the same
// question, for a schedule that has come due.
func (s *Store) publishingSuppression(ctx context.Context, sched Schedule, now time.Time) (*ScheduleSuppression, error) {
	if sched.SkipWhen != SkipWhenPublishingHeld {
		return nil, nil
	}
	hold, err := s.publishingHold(ctx, sched, s.overlapGroupRepo(ctx, sched), sched.Base, now)
	if err != nil || hold == nil {
		return nil, err
	}
	return &ScheduleSuppression{Reason: SuppressedByPublishingHeld, TaskID: hold.TriageTaskID,
		Detail: "skipped: " + publishingHeldDetail(*hold)}, nil
}
