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

// ScheduleSuppressionReason is which unfinished thing is holding a
// schedule's cadence -- one value per kind of firing, because "the
// previous firing has not finished" means a different object, and a
// different thing to do about it, for each of the three (OverlapPolicy's
// own doc comment on what counts as unfinished).
//
// The values are the vocabulary pkg/ui puts on the wire unchanged, the
// same way RecurrenceKind's are: one spelling for this fact, not a
// second one per layer.
type ScheduleSuppressionReason string

const (
	// SuppressedByOpenTask is a schedule that files a task whose last
	// firing's task is still open. Closing it lets the schedule fire.
	SuppressedByOpenTask ScheduleSuppressionReason = "task"
	// SuppressedBySuiteRun is a schedule that runs a suite whose last
	// run has not finished.
	SuppressedBySuiteRun ScheduleSuppressionReason = "suiteRun"
	// SuppressedByWorkflowRun is a schedule translated out of a
	// workflow's own cron whose last firing's CI run has not ended --
	// or has ended with nobody sent to read it yet, which is the same
	// half of the firing (ScheduledWorkflowRun.Settled).
	SuppressedByWorkflowRun ScheduleSuppressionReason = "workflowRun"
	// SuppressedByOpenReview is the other half of that one: the run
	// finished, the review was filed, and nobody has closed it. Unlike
	// every reason above it expires -- workflowReviewHold, reported as
	// ScheduleSuppression.Until.
	SuppressedByOpenReview ScheduleSuppressionReason = "review"
	// SuppressedByStandingWork is a schedule whose work lands in a
	// repository this deployment does not run the standing work of --
	// the repository default is off, or the schedule's own override is
	// (model.RunsWorkItem, grain/task-3332). Unlike every reason above it
	// is not waiting on anything: each occurrence is skipped and the
	// cadence moves on (suppressStandingWorkOff), so this is reported
	// whether or not the schedule is due, for as long as it is true.
	SuppressedByStandingWork ScheduleSuppressionReason = "standingWork"
	// SuppressedByWatchedRepoOffTarget is a git_ref schedule whose watched
	// repository is not in Config.TargetRepos (or is a repository-declared
	// schedule naming grain/config). reconcileGitRefSchedules refuses to
	// poll it until the repository is added to targetRepos; because a
	// git_ref schedule carries a placeholder NextRunAt in the future, it
	// must be reported ahead of the due-time check so Processes does not
	// render it as actively polling ("on ref move").
	SuppressedByWatchedRepoOffTarget ScheduleSuppressionReason = "watchedRepoOffTarget"
	// SuppressedByPublishingHeld is a schedule that declares
	// `skip-when: publishing-held` (SkipWhenPublishingHeld) whose base
	// branch's publishing is held pending the release triage task TaskID
	// names (BaseBranchPublishing.Gated, grain/task-4393). Held, as an
	// overlap is: it fires once when that task closes.
	SuppressedByPublishingHeld ScheduleSuppressionReason = "publishingHeld"
)

// ScheduleSuppression is a firing that has come due and is not
// happening: the schedule's previous firing has not finished and its
// overlap policy is to skip (OverlapSkip).
//
// It exists to be *shown*. A suppressed firing leaves NextRunAt exactly
// where it was, so the poll re-asks and re-skips every cycle, and until
// this there was nothing anywhere saying so -- a schedules page drew a
// next-run time that had already gone by, with no hint why, for as long
// as the previous firing stayed unfinished (grain/task-493 bounded that
// wait for a workflow schedule's review; this is how somebody finds out
// they are inside one).
type ScheduleSuppression struct {
	// Reason is which unfinished thing is holding the cadence.
	Reason ScheduleSuppressionReason
	// TaskID is the open task doing the holding, for the two reasons
	// that have one -- SuppressedByOpenTask and SuppressedByOpenReview.
	// It is the whole point of reporting this: what to do about a
	// schedule that has stopped firing is to go and close that task.
	TaskID string
	// Until is when grain stops waiting and fires anyway, for the one
	// reason that is bounded from here (SuppressedByOpenReview, and
	// workflowReviewHold is the bound). Zero for every other reason, and
	// for a review whose filing time cannot be measured: those wait on
	// something ending rather than on a clock, so there is no date to
	// show and showing one would be a guess.
	Until time.Time
	// Detail is the sentence to show for a reason that has one of its
	// own -- SuppressedByStandingWork's "skipped: this deployment doesn't
	// run <repo>'s standing work", or which schedule in a shared overlap
	// group is holding this one -- and empty for the reasons the task or
	// the date above already say everything about.
	Detail string
	// HeldByScheduleID is the other schedule in this one's overlap group
	// (Schedule.OverlapGroup, grain/task-3895) whose unfinished firing is
	// holding it, and empty when what is holding it is its own previous
	// firing.
	HeldByScheduleID string
}

// openReviewID is the review holding the cadence, for
// ScheduleFiring.OpenReview: set only for SuppressedByOpenReview.
func (sup ScheduleSuppression) openReviewID() string {
	if sup.Reason == SuppressedByOpenReview {
		return sup.TaskID
	}
	return ""
}

// SuppressedFiring reports why sched has come due and is not firing, or
// nil when nothing is holding it back.
//
// It is the read-only half of the overlap check each fireXSchedule makes
// -- the same store questions, in the same order, asked without writing
// anything -- so that a page can say what the firing loop is quietly
// doing every cycle. Kept beside them rather than derived from a column
// a suppressed firing writes: there is no such column, a suppression is
// a fact about right now (the task holding it may have been closed a
// second ago), and a stored one would go stale exactly when it mattered.
//
// Three conditions before the store is asked anything, and each of them
// is a different answer to "why is this schedule not firing", already
// visible without this:
//
//   - A disabled schedule is not firing because it is paused, which
//     every UI showing a schedule already says.
//   - A schedule set to OverlapAllow never suppresses anything.
//   - A schedule whose next run is still ahead is not firing because it
//     is not due yet. Only a NextRunAt that has gone by can be a
//     suppressed firing, because skipping is what leaves one there.
//
// The other suppression a firing has (suppressOffTargetRepos) is
// deliberately not reported here: that one advances the cadence, so it
// leaves a NextRunAt in the future and does not look like this. It is
// not a schedule silently waiting on work that has not finished; it is
// a schedule whose repo an operator took off the list, having been told
// so at the save that did it (StrandedWork).
func (s *Store) SuppressedFiring(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location) (*ScheduleSuppression, error) {

	if !sched.Enabled {
		return nil, nil
	}
	// Asked ahead of the conditions below, because it is the one
	// suppression that advances the cadence and so never leaves a
	// NextRunAt that has gone by: it is the schedule's standing status,
	// and the only place its skipped firings are said.
	if off, err := s.standingWorkOff(ctx, sched, sched.Target); err != nil {
		return nil, err
	} else if off {
		return &ScheduleSuppression{
			Reason: SuppressedByStandingWork,
			Detail: "skipped: " + StandingWorkOffReason(sched.Target),
		}, nil
	}
	if off, mode, err := s.workflowGrainCIOff(ctx, sched, sched.Target); err != nil {
		return nil, err
	} else if off {
		return &ScheduleSuppression{
			Reason: SuppressedByStandingWork,
			Detail: "skipped: " + WorkflowGrainCIOffReason(mode),
		}, nil
	}
	if sched.Recurrence.Kind == RecurrenceGitRef && sched.Recurrence.GitRef != nil {
		if watched, err := ParseRepo(sched.Recurrence.GitRef.Repo); err == nil {
			if IsConfigRepo(watched) && IsRepoDeclaredID(sched.ID) {
				return &ScheduleSuppression{
					Reason: SuppressedByWatchedRepoOffTarget,
					Detail: fmt.Sprintf("watches %s, which a repository-declared schedule may not read", watched),
				}, nil
			}
			targets, err := s.TargetRepos(ctx)
			if err != nil {
				return nil, fmt.Errorf("reading target repos for git_ref suppression: %w", err)
			}
			if len(targets) > 0 && !RepoAllowed(targets, watched) {
				return &ScheduleSuppression{
					Reason: SuppressedByWatchedRepoOffTarget,
					Detail: fmt.Sprintf("watches %s, which is not on this deployment's target repo list", watched),
				}, nil
			}
		}
	}
	if sched.NextRunAt.After(now) {
		return nil, nil
	}
	if sched.Overlap.Allows() {
		// Nothing of its own holds it; its declared condition still may.
		return s.publishingSuppression(ctx, sched, now)
	}
	_, workflow := workflowReviewSchedule(sched)
	switch {
	case sched.SuiteID != nil:
		// fireSuiteSchedule's own check.
		active, err := s.HasActiveRunForSchedule(ctx, sched.ID)
		if err != nil {
			return nil, fmt.Errorf("checking for a previous unfinished firing: %w", err)
		}
		if active {
			return &ScheduleSuppression{Reason: SuppressedBySuiteRun}, nil
		}
	case workflow:
		// fireWorkflowSchedule's own check, which is two questions --
		// and the only one of the three with a bound worth reporting.
		hold, err := s.workflowFiringInFlight(ctx, sched, now, loc)
		if err != nil {
			return nil, err
		}
		switch {
		case hold.Run:
			return &ScheduleSuppression{Reason: SuppressedByWorkflowRun}, nil
		case hold.Held:
			return &ScheduleSuppression{
				Reason: SuppressedByOpenReview,
				TaskID: hold.Review,
				Until:  hold.Until,
			}, nil
		}
	default:
		// fireTaskSchedule's own check, asked for the task rather than
		// for a bare yes/no (HasOpenTaskWithTag): which task is holding
		// the schedule is the actionable half of the answer, and it is
		// the same row either way.
		open, err := s.OpenTaskWithTag(ctx, ScheduleFiringTag(sched.ID))
		if err != nil {
			return nil, fmt.Errorf("checking for a previous unfinished firing: %w", err)
		}
		if open != nil {
			return &ScheduleSuppression{Reason: SuppressedByOpenTask, TaskID: open.ID}, nil
		}
	}
	// The shared group's check, after the schedule's own as each
	// fireXSchedule asks it: its own previous firing is the more useful
	// thing to name when both are true.
	if sched.OverlapGroup != "" {
		sup, err := s.OverlapGroupInFlight(ctx, sched, s.overlapGroupRepo(ctx, sched), now)
		if err != nil {
			return nil, err
		}
		if sup != nil {
			return sup, nil
		}
	}
	// A declared condition last, as each fireXSchedule asks it: a
	// previous firing still going is the more useful thing to name.
	return s.publishingSuppression(ctx, sched, now)
}

// overlapGroupRepo is the repository sched's overlap group is scoped to:
// the repository its firings go into. That is sched.Target, except for a
// schedule whose template is bound to a repository of its own and which
// has not fired since (fireTaskSchedule's composed target, which a
// firing resyncs onto the row).
func (s *Store) overlapGroupRepo(ctx context.Context, sched Schedule) RepoRef {
	if sched.Target == (RepoRef{}) && sched.TemplateID != nil && !IsSystemTemplateID(*sched.TemplateID) {
		if tmpl, err := s.GetTemplate(ctx, *sched.TemplateID); err == nil && tmpl != nil && tmpl.Target != nil {
			return *tmpl.Target
		}
	}
	return sched.Target
}

// OverlapGroupInFlight reports which other schedule in sched's overlap
// group (Schedule.OverlapGroup, grain/task-3895), in repo, has a firing
// that has not finished, or nil when none has -- the group half of the
// overlap check, asked by every fireXSchedule after its own and by
// SuppressedFiring. Unfinished means what it means for that peer's own
// overlap check: an unfinished suite run, a ScheduledWorkflowRun not
// yet settled or a review of one still inside its hold, or an open task
// the peer filed. Then, so a task outlives a schedule that was renamed
// or withdrawn while it ran, any open task carrying the group's tag.
//
// Scoped to repo, which the caller passes as the repository the firing
// goes into: a group is that repository's own, so a schedule elsewhere
// naming the same group neither holds nor is held by this one.
//
// It writes nothing, which is what lets SuppressedFiring ask it.
func (s *Store) OverlapGroupInFlight(ctx context.Context, sched Schedule, repo RepoRef,
	now time.Time) (*ScheduleSuppression, error) {

	group := strings.TrimSpace(sched.OverlapGroup)
	if sched.Overlap.Allows() || group == "" || repo == (RepoRef{}) {
		return nil, nil
	}
	peers, err := s.SchedulesInOverlapGroup(ctx, repo, group)
	if err != nil {
		return nil, err
	}
	// Open tasks a peer's own check has already decided are not holding
	// it (a review past its hold, a standing job resting between wakes),
	// so the tag sweep below does not hold the group on them either.
	notHolding := map[string]bool{}
	for _, peer := range peers {
		if peer.ID == sched.ID {
			continue
		}
		held := func(reason ScheduleSuppressionReason, taskID string, until time.Time) *ScheduleSuppression {
			return &ScheduleSuppression{Reason: reason, TaskID: taskID, Until: until,
				HeldByScheduleID: peer.ID, Detail: overlapGroupDetail(group, peer.ID, peer.Title, reason)}
		}
		_, peerWorkflow := workflowReviewSchedule(peer)
		switch {
		case peer.SuiteID != nil:
			active, err := s.HasActiveRunForSchedule(ctx, peer.ID)
			if err != nil {
				return nil, fmt.Errorf("checking %s in overlap group %q: %w", peer.ID, group, err)
			}
			if active {
				return held(SuppressedBySuiteRun, "", time.Time{}), nil
			}
		case peerWorkflow:
			hold, err := s.workflowFiringInFlight(ctx, peer, now, peer.Location())
			if err != nil {
				return nil, err
			}
			switch {
			case hold.Run:
				return held(SuppressedByWorkflowRun, "", time.Time{}), nil
			case hold.Held:
				return held(SuppressedByOpenReview, hold.Review, hold.Until), nil
			case hold.Review != "":
				notHolding[hold.Review] = true
			}
		default:
			open, err := s.OpenTaskWithTag(ctx, ScheduleFiringTag(peer.ID))
			if err != nil {
				return nil, fmt.Errorf("checking %s in overlap group %q: %w", peer.ID, group, err)
			}
			if open == nil {
				continue
			}
			if peer.TemplateID != nil && IsStandingTemplateID(*peer.TemplateID) {
				// A standing job's task is open for good; it is only a
				// firing in flight while it is actually running.
				st, err := s.State(ctx, open.ID)
				if err != nil {
					return nil, err
				}
				resting, err := s.RestingTask(ctx, open.ID)
				if err != nil {
					return nil, err
				}
				if st == StateAwaitingReply || resting {
					notHolding[open.ID] = true
					continue
				}
			}
			return held(SuppressedByOpenTask, open.ID, time.Time{}), nil
		}
	}

	groupTag := ScheduleOverlapGroupTag(repo, group)
	ids, err := s.OpenTasksWithTag(ctx, groupTag)
	if err != nil {
		return nil, fmt.Errorf("checking open tasks in overlap group %q: %w", group, err)
	}
	for _, id := range ids {
		if notHolding[id] {
			continue
		}
		task, err := s.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		var holder, title string
		if task != nil {
			holder = scheduleIDFromTags(task.Tags)
		}
		if holder == "" {
			// A task a scheduled suite run filed names its schedule on
			// the run rather than in a tag.
			if holder, err = s.ScheduleOfSuiteRunTask(ctx, id); err != nil {
				return nil, err
			}
		}
		if holder == sched.ID {
			// Its own task: the schedule's own check has already
			// answered for it.
			continue
		}
		if holder != "" {
			if h, err := s.GetSchedule(ctx, holder); err == nil && h != nil {
				title = h.Title
			}
		}
		return &ScheduleSuppression{Reason: SuppressedByOpenTask, TaskID: id, HeldByScheduleID: holder,
			Detail: overlapGroupDetail(group, holder, title, SuppressedByOpenTask)}, nil
	}
	holders, err := s.ActiveSuiteRunSchedulesWithGroupTag(ctx, groupTag)
	if err != nil {
		return nil, fmt.Errorf("checking active suite runs in overlap group %q: %w", group, err)
	}
	for _, holder := range holders {
		if holder == sched.ID {
			continue
		}
		var title string
		if holder != "" {
			if h, err := s.GetSchedule(ctx, holder); err == nil && h != nil {
				title = h.Title
			}
		}
		return &ScheduleSuppression{Reason: SuppressedBySuiteRun, HeldByScheduleID: holder,
			Detail: overlapGroupDetail(group, holder, title, SuppressedBySuiteRun)}, nil
	}
	return nil, nil
}

// scheduleIDFromTags is the schedule a task's ScheduleFiringTag names.
func scheduleIDFromTags(tags []string) string {
	for _, t := range tags {
		if id, ok := strings.CutPrefix(t, "schedule:"); ok && id != "" {
			return id
		}
	}
	return ""
}

// scheduleLabel names a schedule in a sentence.
func scheduleLabel(id, title string) string {
	switch {
	case title != "" && id != "" && title != id:
		return fmt.Sprintf("schedule %q (%s)", title, id)
	case id != "":
		return fmt.Sprintf("schedule %s", id)
	case title != "":
		return fmt.Sprintf("schedule %q", title)
	default:
		return "another schedule"
	}
}

// overlapGroupDetail is the sentence for a firing held by another
// schedule in its overlap group, as the schedules page shows it after
// "not firing: ".
func overlapGroupDetail(group, holderID, holderTitle string, reason ScheduleSuppressionReason) string {
	who := scheduleLabel(holderID, holderTitle)
	switch reason {
	case SuppressedBySuiteRun:
		return fmt.Sprintf("the suite run %s started has not finished, and both are in overlap group %q", who, group)
	case SuppressedByWorkflowRun:
		return fmt.Sprintf("the CI run %s started has not finished, and both are in overlap group %q", who, group)
	case SuppressedByOpenReview:
		return fmt.Sprintf("the review of %s's last run is still open, and both are in overlap group %q", who, group)
	default:
		return fmt.Sprintf("the task %s filed is still open, and both are in overlap group %q", who, group)
	}
}
