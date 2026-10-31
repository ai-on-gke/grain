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
	"time"
)

// SchedulePrincipal is the automation actor every task a schedule files
// is attributed to -- its own actor, distinct from "grain" (relayed
// agent output), from "merge-queue" (automatic PR repair) and from
// SuitePrincipal (suite.go), so a human reading a task's conversation or
// origin can tell which of grain's own mechanisms filed it.
var SchedulePrincipal = Principal{Kind: PrincipalAutomation, ID: "schedule"}

// ScheduleFiringTag is the idempotency marker every task a schedule
// files carries -- v1's scheduled_jobs.py marker_label, ported onto
// Task.Tags per docs/data-model.md ("neither a state nor a capability:
// it is an idempotency tag"). fireTaskSchedule checks it before filing
// again, so a schedule whose previous firing is still running (or
// awaiting a reply, or otherwise unclosed) does not pile up a second one
// on top of it.
func ScheduleFiringTag(scheduleID string) string { return "schedule:" + scheduleID }

// ScheduleFiring is what one firing actually did -- what a caller needs
// to say so afterwards, which is the whole reason firing lives here
// rather than in pkg/orchestrator where it used to (grain/task-227): the
// reconciler fires a schedule because its cadence came round and reports
// nothing to anybody, but a human pressing "Run now" is owed an answer,
// and pkg/ui cannot import pkg/orchestrator (a presentation-layer
// package importing core dispatch logic runs the wrong way, ui's own
// sandbox_health.go on why). Both callers fire through the same code
// here, so a run started by hand is the same firing the cadence would
// have made.
type ScheduleFiring struct {
	// Skipped is a firing this schedule's own overlap policy suppressed:
	// its previous firing had not finished and Schedule.Overlap is
	// OverlapSkip. Nothing was filed and nothing was started, and the
	// schedule's timing is left exactly as it was.
	Skipped bool
	// OffTargetRepos names the repo a firing would have gone into when
	// that repo is not one this deployment may dispatch work into
	// (RepoAllowed against Config.TargetRepos), and is nil for every
	// other firing. Nothing was filed and nothing was started; unlike
	// Skipped, the cadence did move on -- see suppressOffTargetRepos.
	//
	// The repo rather than a bare flag because both callers have
	// something to say about it and neither has it to hand: the
	// reconciler logs which schedule stopped firing where, and pkg/ui
	// tells whoever pressed "Run now" why nothing ran, in the vocabulary
	// beside the rule (OffAllowlistRefusal).
	OffTargetRepos *RepoRef
	// StandingWorkOff names the repo a firing would have gone into when
	// this deployment does not run that repo's standing work -- the
	// repository default is off, or this schedule's own override is
	// (RunsWorkItem with ScheduleItem, grain/task-3332). Nothing was filed
	// and nothing was started, and the cadence moved on exactly as it
	// does for OffTargetRepos, so turning the work back on resumes the
	// schedule at its next occurrence instead of releasing a backlog of
	// catch-up firings (suppressStandingWorkOff).
	StandingWorkOff *RepoRef
	// TaskID is the task a task-backed schedule filed -- empty for a
	// suite-backed schedule, and for a firing either field above
	// suppressed.
	TaskID string
	// SuiteRunID is the run a suite-backed schedule started -- zero for
	// a task-backed schedule, and for a firing either field above
	// suppressed.
	SuiteRunID int64
	// OpenReview is the review task of a previous firing of a workflow
	// schedule that was still open when this one came round, and is empty
	// for every other firing and for one that found nothing open.
	//
	// Skipped says which of the two things that means. With Skipped set,
	// this task is *why* nothing fired: the previous firing is genuinely
	// still in flight, and the cadence waits for it. Without it, the
	// review had been open so long that grain stopped counting it as in
	// flight (workflowReviewHold) and fired anyway -- an abandoned review
	// rather than one somebody is reading, and the one case where a
	// firing wants saying out loud, since a review nobody closes used to
	// stop the schedule for good and silently.
	OpenReview string
	// WorkflowRun is a firing of a schedule translated out of a
	// workflow's own `on: schedule` cron: neither of the two above,
	// because this firing files nothing yet. It records the firing
	// (ScheduledWorkflowRun) and the daemon's next cycle starts the CI
	// run for it; the review task is filed later still, once that run
	// has a verdict to be reviewed (orchestrator's scheduledworkflow.go).
	//
	// A caller with somebody waiting on it -- ui.Client.RunScheduleNow --
	// needs this to say what happened, since there is no id to report
	// and "nothing was filed" is not what happened.
	WorkflowRun bool
	// Skip is the record of a firing that came due and filed nothing, for
	// whichever reason (ScheduleSkip, grain/task-3560): set alongside
	// Skipped, OffTargetRepos and StandingWorkOff, nil for a firing that
	// fired.
	Skip *ScheduleSkip
	// Woke is a firing of a standing template (IsStandingTemplateID) that
	// put the task an earlier firing filed back in the queue rather than
	// filing a new one; TaskID is that task.
	Woke bool
	// SkipRecorded is whether this firing's Skip is a new record rather
	// than another cycle re-skipping an occurrence already recorded --
	// what the reconciler logs on, so a held occurrence is one log line
	// rather than one per poll. Only a cadence firing (FireSchedule)
	// records a skip; somebody pressing "Run now" is told directly.
	SkipRecorded bool
}

// FireSchedule fires sched because its cadence came due: whichever of
// the two things a schedule can fire it names -- a suite run (SuiteID
// set) or a single task -- followed by advancing its own timing to the
// next occurrence. This is the reconciler's entry point
// (orchestrator.reconcileSchedule); FireScheduleNow below is the one a
// human pressing "Run now" reaches.
//
// The two paths differ only in what a firing *is* -- both check that the
// previous firing has finished first (unless Overlap says otherwise),
// and both record the firing afterwards through recordFiring.
//
// stateRepo is this deployment's own state repository, and is only ever
// read to resolve one of grain's own templates (resolveTemplate,
// SystemTemplate): those are the same code on every deployment except
// for the repository they read, and the store cannot know which that is.
// The zero RepoRef is a fine answer -- a deployment whose state is
// local-only has none -- and every schedule a human wrote ignores it
// entirely.
func (s *Store) FireSchedule(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location, stateRepo RepoRef) (ScheduleFiring, error) {

	return s.fireSchedule(ctx, sched, now, loc, stateRepo, true, "")
}

// resolveTemplate is the template id means whatever it means: this
// build's own for a "system:" id (SystemTemplate), and this
// deployment's `template` row for every other.
//
// Both are resolved fresh at firing time, and for the same reason --
// fireTaskSchedule's own doc comment on why a template edited since the
// schedule last fired is what actually gets filed. A system template is
// simply edited by upgrading grain rather than through the UI.
func (s *Store) resolveTemplate(ctx context.Context, id string, stateRepo RepoRef) (*Template, error) {
	if IsSystemTemplateID(id) {
		return SystemTemplate(id, stateRepo), nil
	}
	return s.GetTemplate(ctx, id)
}

// ResolveTemplate resolves a template ID, checking system templates first and stored/declared templates second.
func (s *Store) ResolveTemplate(ctx context.Context, id string, stateRepo RepoRef) (*Template, error) {
	return s.resolveTemplate(ctx, id, stateRepo)
}

// FireScheduleNow fires sched out of band, because somebody asked for it
// now (ui.Client.RunScheduleNow, grain/task-227) -- the same firing the
// cadence makes, with two differences, both of which follow from it
// being an extra firing rather than a rescheduling:
//
//   - NextRunAt is left exactly where it was. The firing already
//     scheduled still happens when it was going to; running a chore by
//     hand at four in the afternoon does not move the 09:00 it also runs
//     at, the same way pausing and resuming a schedule keeps its firing
//     time rather than resetting it (ui.UpdateSchedule's own doc
//     comment). LastRunAt does move: this schedule really did fire.
//   - A paused schedule fires anyway. Enabled stops the *cadence* --
//     Store.DueSchedules never returns a disabled schedule -- and a
//     human asking for this one run is not the cadence. It stays paused
//     afterwards.
//
// The overlap policy still applies: a schedule set to OverlapSkip whose
// previous firing has not finished reports ScheduleFiring.Skipped rather
// than filing a second task, exactly as it would have on its own
// cadence. That is the whole point of the setting -- a chore that must
// not have two copies of itself running must not get one because the
// button was there -- and, unlike the cadence's own suppressed firing,
// the caller is told, so a human hears "the last run is still going"
// instead of watching nothing happen.
func (s *Store) FireScheduleNow(ctx context.Context, sched Schedule, now time.Time,
	stateRepo RepoRef) (ScheduleFiring, error) {

	// No location: nothing here reads the cadence, since nothing here
	// advances it.
	return s.fireSchedule(ctx, sched, now, nil, stateRepo, false, "")
}

// FireScheduleWake is FireScheduleNow on grain's own behalf rather than a
// person's: an extra firing that leaves the cadence where it is, made
// because something happened that the job should hear about now rather
// than at its next occurrence (orchestrator.wakePipelineSteward, on a
// confirmed red base branch). reason says what, and for a standing
// template (IsStandingTemplateID) it is the note the woken task is given
// (StandingWakeComment). A schedule with an ordinary template files its
// task as Run now would, and reason goes nowhere.
func (s *Store) FireScheduleWake(ctx context.Context, sched Schedule, now time.Time,
	stateRepo RepoRef, reason string) (ScheduleFiring, error) {

	return s.fireSchedule(ctx, sched, now, nil, stateRepo, false, reason)
}

// fireSchedule is FireSchedule and FireScheduleNow's shared body --
// advance is the whole difference between them (see FireScheduleNow's
// own doc comment), carried down to recordFiring.
func (s *Store) fireSchedule(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location, stateRepo RepoRef, advance bool, wake string) (ScheduleFiring, error) {

	firing, err := s.fireScheduleOnce(ctx, sched, now, loc, stateRepo, advance, wake)
	if err != nil || !advance {
		return firing, err
	}
	// The record of what this occurrence did (ScheduleSkip): a skip is
	// written down, and a firing settles whatever occurrence it is the
	// late firing of. Neither for "Run now", which is an extra firing
	// rather than an occurrence of the cadence.
	if firing.Skip != nil {
		created, err := s.RecordScheduleSkip(ctx, *firing.Skip)
		if err != nil {
			return firing, err
		}
		firing.SkipRecorded = created
		return firing, nil
	}
	if firing.TaskID != "" || firing.SuiteRunID != 0 || firing.WorkflowRun {
		// What this firing is the late firing of is sched.NextRunAt; a
		// row held from before it was stranded by a retime, not fired.
		if err := s.DropStrandedScheduleSkips(ctx, sched.ID, sched.NextRunAt, now); err != nil {
			return firing, err
		}
		if err := s.resolveHeldScheduleSkips(ctx, sched.ID, now, firing.TaskID,
			foldedOccurrences(sched, now, loc)); err != nil {
			return firing, err
		}
	}
	return firing, nil
}

// foldedOccurrences is how many occurrences after sched.NextRunAt have
// also come due by now: the ones a firing held past them absorbs, since
// recordFiring moves the cadence past all of them at once.
func foldedOccurrences(sched Schedule, now time.Time, loc *time.Location) int {
	if loc == nil {
		loc = sched.Location()
	}
	n := 0
	for next := sched.Recurrence.Next(sched.NextRunAt, loc); !next.After(now); next = sched.Recurrence.Next(next, loc) {
		n++
	}
	return n
}

// fireScheduleOnce is fireSchedule without the skip record: the firing
// itself, dispatched to whichever kind sched is.
func (s *Store) fireScheduleOnce(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location, stateRepo RepoRef, advance bool, wake string) (ScheduleFiring, error) {

	if sched.SuiteID != nil {
		return s.fireSuiteSchedule(ctx, sched, now, loc, advance)
	}
	if stem, ok := workflowReviewSchedule(sched); ok {
		return s.fireWorkflowSchedule(ctx, sched, stem, now, loc, stateRepo, advance)
	}
	return s.fireTaskSchedule(ctx, sched, now, loc, stateRepo, advance, wake)
}

// workflowReviewSchedule reports whether this schedule is one a
// workflow's own `on: schedule` cron was translated into, and the
// workflow's stem if it is. Schedule.TemplateID is the only field a
// schedule has that says what a firing *is*
// (WorkflowReviewTemplateID's own doc comment), so it is the only thing
// to ask.
func workflowReviewSchedule(sched Schedule) (string, bool) {
	if sched.TemplateID == nil {
		return "", false
	}
	return WorkflowReviewStem(*sched.TemplateID)
}

// offTargetRepos reports whether target is a repo this deployment may no
// longer dispatch work into -- the one rule (RepoAllowed) asked of the
// stored Config.TargetRepos, read fresh at firing time for the reason
// every other asker re-reads it: the list moves underneath work that is
// already stored (ui.UpdateSettings), and a schedule written months ago
// against a repo that was on the list is exactly the work it moves
// underneath.
//
// A firing with no repo at all answers no, the same reading
// orchestrator.reconcileTargetRepos takes of a task with a nil Target:
// there is nothing to clone and nothing to push, so the list has nothing
// to say about it, and a system template's own firing (a comment, a
// store write) must not stop because a deployment restricts its repos.
func (s *Store) offTargetRepos(ctx context.Context, target RepoRef) (bool, error) {
	if target == (RepoRef{}) {
		return false, nil
	}
	targetRepos, err := s.TargetRepos(ctx)
	if err != nil {
		return false, err
	}
	return !RepoAllowed(targetRepos, target), nil
}

// suppressOffTargetRepos is what a firing into a repo off
// Config.TargetRepos does instead of filing: nothing, with the cadence
// moved on to its next occurrence as though this one had happened
// (grain/task-430).
//
// Filing it was safe and noisy. Nothing between a schedule and a sandbox
// re-asked the list -- fireTaskSchedule and fireSuiteSchedule file
// already-approved, already-dispatchable work -- so the last look before
// dispatch parked each firing instead (orchestrator.reconcileTargetRepos,
// OffAllowlistAtDispatchComment), which left a deployment whose operator
// had already been told once, at the save that narrowed the list
// (StrandedWork), collecting a parked task per cadence saying it again.
//
// Suppressing here rather than disabling the schedule is the whole
// point, and it is why this is the fire path's answer and not the
// narrowing's: nothing is taken away from the operator. Enabled is
// untouched, Target is untouched, and the first cadence after the repo
// goes back on the list fires normally -- so a repo put back comes back
// with its nightly work still running, which a schedule disabled by a
// settings save would not (StrandedWork's own doc comment on why that
// narrowing deliberately disables nothing).
//
// NextRunAt moves and LastRunAt does not, and the two say different
// things on purpose. A suppressed occurrence is over -- retrying it on
// the next tick would ask the same list the same question every poll
// interval, and answer it the same way -- but nothing ran, so the
// schedule's own "last run" must not claim one did. That is the
// difference from an overlap-suppressed firing (ScheduleFiring.Skipped),
// which leaves both alone because the work is delayed rather than
// dropped: here, waiting changes nothing until a human edits Settings.
//
// A firing somebody asked for by hand (FireScheduleNow, advance false)
// moves neither, exactly as it moves neither when it fires: it is an
// extra firing, so there is no occurrence of the cadence to be over.
// pkg/ui refuses that one out loud instead (Client.RunScheduleNow),
// because somebody is waiting on it.
func (s *Store) suppressOffTargetRepos(ctx context.Context, sched Schedule, target RepoRef,
	now time.Time, loc *time.Location, advance bool) (ScheduleFiring, error) {

	if advance {
		next := nextOccurrence(sched, now, loc)
		if err := s.UpdateSchedule(ctx, sched.ID, func(sc *Schedule) error {
			sc.NextRunAt = next
			return nil
		}); err != nil {
			return ScheduleFiring{}, fmt.Errorf("advancing past a firing into a repo off the target repo list: %w", err)
		}
	}
	return ScheduleFiring{OffTargetRepos: &target, Skip: droppedSkip(sched, now, SkippedOffTargetRepos,
		target.String()+" is not on this deployment's target repo list")}, nil
}

// standingWorkOff reports whether this deployment has said it does not
// run this schedule's work in target: model.RunsWorkItem for the
// schedule's own item, so the repository default decides unless the
// schedule has an override of its own (grain/task-3332). A firing with
// no repo answers no, for offTargetRepos' reason: there is no repository
// whose work it is.
func (s *Store) standingWorkOff(ctx context.Context, sched Schedule, target RepoRef) (bool, error) {
	if target == (RepoRef{}) {
		return false, nil
	}
	cfg, err := s.GetRepoConfig(ctx, target)
	if err != nil {
		return false, fmt.Errorf("reading %s's work ownership: %w", target, err)
	}
	return !RunsWorkItem(cfg, ScheduleItem(sched.ID)), nil
}

// suppressStandingWorkOff is what a firing into a repository whose
// standing work this deployment does not run does instead of firing:
// nothing, with the cadence moved on as though the occurrence had
// happened -- suppressOffTargetRepos' bargain exactly, and for its
// reason. Holding NextRunAt where it was would re-ask every cycle and,
// worse, leave a firing owed that runs the moment the switch flips back,
// which is the catch-up an operator turning work off has not asked for.
//
// LastRunAt does not move, because nothing ran. The skip is said where
// the schedule's status is read (SuppressedFiring, which reports it for
// as long as it is true) and in the reconciler's log.
func (s *Store) suppressStandingWorkOff(ctx context.Context, sched Schedule, target RepoRef,
	now time.Time, loc *time.Location, advance bool) (ScheduleFiring, error) {

	if advance {
		next := nextOccurrence(sched, now, loc)
		if err := s.UpdateSchedule(ctx, sched.ID, func(sc *Schedule) error {
			sc.NextRunAt = next
			return nil
		}); err != nil {
			return ScheduleFiring{}, fmt.Errorf("advancing past a firing into a repo whose standing work is off: %w", err)
		}
	}
	return ScheduleFiring{StandingWorkOff: &target, Skip: droppedSkip(sched, now, SuppressedByStandingWork,
		StandingWorkOffReason(target))}, nil
}

// workflowGrainCIOff reports whether target has explicitly disabled Grain CI
// (CIModeGitHub or CIModeNone), in which case scheduled `.grain/workflows/*.yml`
// crons do not run on Grain CI.
func (s *Store) workflowGrainCIOff(ctx context.Context, target RepoRef) (bool, CIMode, error) {
	if target == (RepoRef{}) || IsConfigRepo(target) {
		return false, "", nil
	}
	cfg, err := s.GetRepoConfig(ctx, target)
	if err != nil {
		return false, "", fmt.Errorf("reading %s's CI mode: %w", target, err)
	}
	if cfg != nil && (cfg.CIMode == CIModeGitHub || cfg.CIMode == CIModeNone) {
		return true, cfg.CIMode, nil
	}
	return false, "", nil
}

func (s *Store) suppressWorkflowGrainCIOff(ctx context.Context, sched Schedule, target RepoRef,
	mode CIMode, now time.Time, loc *time.Location, advance bool) (ScheduleFiring, error) {

	if advance {
		next := nextOccurrence(sched, now, loc)
		if err := s.UpdateSchedule(ctx, sched.ID, func(sc *Schedule) error {
			sc.NextRunAt = next
			return nil
		}); err != nil {
			return ScheduleFiring{}, fmt.Errorf("advancing past a workflow firing into a repo with CI mode %s: %w", mode, err)
		}
	}
	reason := fmt.Sprintf("this repository uses %s CI mode", mode)
	if mode == CIModeNone {
		reason = "this repository has CI disabled (ci_mode: none)"
	}
	return ScheduleFiring{StandingWorkOff: &target, Skip: droppedSkip(sched, now, SuppressedByStandingWork, reason)}, nil
}

// fireTaskSchedule files sched's next task, unless its previous firing
// has not finished yet -- in which case this is a no-op (reported as
// ScheduleFiring.Skipped), tried again next cycle with NextRunAt left
// exactly where it was, so the check costs nothing and cannot skip a
// firing outright, only delay it. A schedule set to OverlapAllow
// (Schedule.Overlap) skips that check and files regardless; skipping is
// the default, and what every schedule did before the choice existed.
// This is the path for a schedule that files a single task;
// fireSuiteSchedule is the one for a schedule that runs a whole suite
// instead. Both also suppress a firing into a repo this deployment may
// no longer dispatch work into (suppressOffTargetRepos), which differs
// from the overlap check in that the occurrence really is over: nothing
// here waits for a list only a human can widen.
//
// The filed task lands already approved: docs/data-model.md's "the
// SCHEDULED special case dissolves" is why -- a schedule is itself a
// human's standing approval, written once and editable like any other
// declaration, so a firing needs no approval flag of its own the way
// the merge queue's own automatic repair needs none either.
//
// sched.TemplateID (bwsalmon/agents#516), if set, is resolved here --
// fresh, through resolveTemplate (this build's own template for a
// "system:" id, the store's row for every other), not against whatever
// sched.Title/Body/
// AutoMerge/Reads/Writes/Grants/CustomFields/NetworkAccess/NothingNeededAction
// already say -- so a
// template edited since this schedule last fired is what actually gets
// filed, not a stale copy. A template deleted out from under a schedule
// (ui.Client.DeleteTemplate tries to prevent this, but nothing stops a
// row from disappearing some other way) fails this one firing with a
// plain error, retried next
// cycle exactly like any other store error orchestrator.reconcileSchedule's
// own doc comment already tolerates -- not treated as reason to disable
// the schedule outright.
//
// sched.Target and sched.Base are the schedule's own, unless the
// template it fires from is bound to a repo of its own, in which case
// the binding wins (Template.FiringTarget). A binding is the template
// saying "this content only makes sense against this repo", so it has to
// beat a repo the schedule was pointed at separately -- and
// ui.scheduleContentFromTemplate applies the same rule when a schedule
// is created or repointed, so a schedule's own Target/Base are already
// the bound ones long before this fires.
func (s *Store) fireTaskSchedule(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location, stateRepo RepoRef, advance bool, wake string) (ScheduleFiring, error) {

	// What this firing carries, resolved by the one composition
	// algorithm every firing point shares (compose.go): the template's
	// content when this schedule fires one, its own inline fields when
	// it does not, placed against a bound template's repo or this
	// schedule's, and approved either way.
	comp, tmpl, err := s.composeScheduleTask(ctx, sched, stateRepo)
	if err != nil {
		return ScheduleFiring{}, err
	}

	// The composed target rather than sched.Target, because that is the
	// repo the filed task would actually be dispatched into: a template
	// bound to a repo of its own beats the schedule's
	// (Template.FiringTarget), and the schedule's own copy of that
	// binding is only resynced by a firing (recordFiring's sync below),
	// so a template rebound since the last one is a firing whose repo the
	// schedule does not yet know. Asking the schedule instead would
	// suppress a firing that was about to go somewhere perfectly
	// allowed.
	if comp.Target != nil {
		off, err := s.offTargetRepos(ctx, *comp.Target)
		if err != nil {
			return ScheduleFiring{}, err
		}
		if off {
			return s.suppressOffTargetRepos(ctx, sched, *comp.Target, now, loc, advance)
		}
		if off, err := s.standingWorkOff(ctx, sched, *comp.Target); err != nil || off {
			if err != nil {
				return ScheduleFiring{}, err
			}
			return s.suppressStandingWorkOff(ctx, sched, *comp.Target, now, loc, advance)
		}
	}

	// The overlap check comes after the two suppressions above rather
	// than before them: those advance the cadence, and this one holds
	// it. Asked first, a task still open from an earlier firing would
	// hold an occurrence that came due while the work was off, and fire
	// it as a catch-up the moment the task closed or the switch came back
	// on -- the backlog suppressStandingWorkOff exists not to owe -- and
	// the skip would name the task while SuppressedFiring named the
	// switch.
	tag := ScheduleFiringTag(sched.ID)
	standing := tmpl != nil && IsStandingTemplateID(tmpl.ID)
	// A standing template asks whatever the overlap policy says: it is
	// one task by definition, so OverlapAllow cannot mean a second one.
	if !sched.Overlap.Allows() || standing {
		// The task rather than a yes/no (HasOpenTaskWithTag), so the
		// skip can name what is holding it (ScheduleSkip).
		open, err := s.OpenTaskWithTag(ctx, tag)
		if err != nil {
			return ScheduleFiring{}, fmt.Errorf("checking for a previous unfinished firing: %w", err)
		}
		if open != nil && standing {
			if err := s.applyStandingDeclarations(ctx, open.ID, *tmpl, now); err != nil {
				return ScheduleFiring{}, err
			}
			// A standing job's open task is not a previous firing still
			// going, it is the job: woken if it is idle, and the firing
			// held exactly as an overlap is if it is not -- a run still
			// going is the wake already happening.
			woke, err := s.wakeStandingTask(ctx, *open, comp, wake, now)
			if err != nil {
				return ScheduleFiring{}, err
			}
			if woke {
				if err := s.recordFiring(ctx, sched, now, loc, advance, func(sc *Schedule) {
					sc.syncTemplateCache(*tmpl)
				}); err != nil {
					return ScheduleFiring{}, err
				}
				return ScheduleFiring{TaskID: open.ID, Woke: true}, nil
			}
		}
		if open != nil {
			return ScheduleFiring{Skipped: true,
				Skip: s.overlapSkip(ctx, sched, now, SuppressedByOpenTask, open.ID)}, nil
		}
	}

	id, err := s.putScheduleTask(ctx, comp, []string{tag}, now)
	if err != nil {
		return ScheduleFiring{}, err
	}
	if standing {
		if err := s.applyStandingDeclarations(ctx, id, *tmpl, now); err != nil {
			return ScheduleFiring{}, err
		}
	}
	if standing && wake != "" {
		// The first firing of a standing job can itself be an early wake
		// (FireScheduleWake), and the run is owed the reason either way.
		if _, err := s.AddComment(ctx, Comment{TaskID: id, Author: Attribution{Actor: SchedulePrincipal},
			Body: StandingWakeComment(wake), CreatedAt: now}); err != nil {
			return ScheduleFiring{}, fmt.Errorf("noting the wake on standing task %s: %w", id, err)
		}
	}

	if err := s.recordFiring(ctx, sched, now, loc, advance, func(sc *Schedule) {
		// Keeps the schedule's own display cache in sync with the
		// template it fired from (Schedule.TemplateID's own doc comment)
		// -- nothing to do for an inline schedule, whose own columns are
		// already what it fires.
		if tmpl == nil {
			return
		}
		sc.syncTemplateCache(*tmpl)
	}); err != nil {
		return ScheduleFiring{}, err
	}
	return ScheduleFiring{TaskID: id}, nil
}

// syncTemplateCache copies tmpl's content over sc's display cache
// (Schedule.TemplateID's own doc comment): what a firing does after it
// files, and what ResyncTemplateSchedules does when the template itself
// is saved.
func (sc *Schedule) syncTemplateCache(tmpl Template) {
	sc.Title, sc.Body = tmpl.FilledTitleBody()
	sc.AutoMerge, sc.Reads, sc.Writes, sc.Grants, sc.CustomFields, sc.NetworkAccess, sc.NothingNeededAction, sc.KeepRunning =
		tmpl.AutoMerge, tmpl.Reads, tmpl.Writes,
		tmpl.Grants, tmpl.CustomFields, tmpl.NetworkAccess, tmpl.NothingNeededAction, tmpl.KeepRunning
	// Model is not part of that cache: it is the schedule's own
	// choice, which beats the template's (Schedule.Model), and
	// copying the template's over it would erase that choice.
	// Target and Base are part of that cache only while the template
	// is bound: an unbound one composes against the schedule's own,
	// so this writes back what was already there.
	sc.Target, sc.Base = tmpl.FiringTarget(sc.Target, sc.Base)
}

// ResyncTemplateSchedules refreshes the display cache of every schedule
// that fires from tmpl, so an edited template shows up on its schedules
// straight away rather than after each one next fires. Until then the
// schedules list, the Run now confirmation and a refused template delete
// all named a schedule by the template's old title, while the preview
// beside them and the next firing already used the new one. A firing
// skipped for overlap does not resync either, so a busy schedule could
// stay stale for a long time.
func (s *Store) ResyncTemplateSchedules(ctx context.Context, tmpl Template) error {
	scheds, err := s.SchedulesUsingTemplate(ctx, tmpl.ID)
	if err != nil {
		return err
	}
	for _, sched := range scheds {
		if err := s.UpdateSchedule(ctx, sched.ID, func(sc *Schedule) error {
			if sc.TemplateID != nil && *sc.TemplateID == tmpl.ID {
				sc.syncTemplateCache(tmpl)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("resyncing schedule %s from template %s: %w", sched.ID, tmpl.ID, err)
		}
	}
	return nil
}

// putScheduleTask writes one composed firing as a task, under the tags
// its caller wants on it. Two callers: fireTaskSchedule above, filing
// what a schedule's cadence came due for, and FileWorkflowReview below,
// filing the review a finished workflow run is owed -- the second of
// which happens cycles after the firing it belongs to, which is the
// whole reason this is a step of its own rather than four lines inside
// the firing.
func (s *Store) putScheduleTask(ctx context.Context, comp Composition, tags []string,
	now time.Time) (string, error) {

	id, err := s.NewTaskID(ctx)
	if err != nil {
		return "", fmt.Errorf("allocating an id for %q: %w", comp.Title, err)
	}
	orderKey, err := s.OrderKeyForNewTask(ctx, false)
	if err != nil {
		return "", fmt.Errorf("allocating order key: %w", err)
	}
	task := comp.Task(id, SchedulePrincipal, ReasonSchedule, now)
	task.OrderKey = orderKey
	task.Tags = tags
	if err := s.PutTask(ctx, task); err != nil {
		return "", fmt.Errorf("filing task: %w", err)
	}
	return id, nil
}

// fireWorkflowSchedule fires a schedule translated out of a workflow's
// own `on: schedule` cron (grain/task-344), which is the one kind whose
// firing files nothing at the due tick.
//
// What it does instead is write the firing down (ScheduledWorkflowRun).
// The daemon's next cycle starts the CI run for it, and a later cycle
// still -- once that run has actually ended -- files the review task
// with the verdict already in its opening comment
// (orchestrator.reconcileScheduledWorkflows, grain/task-351). The
// run is not started here for the reason nothing in this package starts
// one: the store is underneath the CI engine, not beside it. That it is
// a *record* rather than a direct call is what makes the firing
// survivable -- a daemon that stops between the schedule coming due and
// the run starting comes back to a row saying a run is owed, where
// before it came back to a task about a run that was never started.
//
// # What a firing is, for overlap
//
// Both halves. A firing of one of these is unfinished until its run has
// ended *and* the review of that run has been read, so Overlap's "skip
// while the previous firing is unfinished" -- the default, and what
// stops a nightly stacking up -- covers the morning somebody is still
// reading as well as the night still running. Two questions, because the
// two halves are recorded in different places: this schedule's row here
// while it is between the run and the review, and its review task's own
// ScheduleFiringTag while somebody is reading it, which is the same
// question every other schedule asks.
//
// # Neither half waits for ever
//
// Both are bounded, because a firing that is never finished would
// otherwise stop the schedule outright: a suppressed firing files
// nothing, records nothing and leaves NextRunAt in the past, so the poll
// re-asks and re-skips every cycle and the suite simply stops running,
// with nothing saying so -- and the thing that would have noticed a
// suite that stopped running is this very review. The run half is
// bounded by orchestrator.scheduledWorkflowDeadline, which concludes a
// run nothing ended and files the review anyway. The review half is
// bounded here (workflowReviewHold): an ordinary agent task can stay
// open indefinitely -- parked on a question, waiting on a human -- and
// past the hold grain stops counting it as in flight and fires again,
// saying on the abandoned review itself that it has (grain/task-493).
//
// The policy is untouched by that: a nightly whose previous review is
// being read still does not get a second one filed on top of it
// (repodecl.workflowSchedule's OverlapSkip, which is not a translation
// detail but the rule). What is bounded is the waiting, not the rule.
func (s *Store) fireWorkflowSchedule(ctx context.Context, sched Schedule, stem string, now time.Time,
	loc *time.Location, stateRepo RepoRef, advance bool) (ScheduleFiring, error) {

	// Asked before a run is started rather than only before the review
	// is filed, and ahead of the overlap check for fireTaskSchedule's
	// reason (a suppression that advances the cadence beats one that
	// holds it): a repo this deployment may no longer dispatch work into
	// is not one to spend sandboxes on either (suppressOffTargetRepos).
	off, err := s.offTargetRepos(ctx, sched.Target)
	if err != nil {
		return ScheduleFiring{}, err
	}
	if off {
		return s.suppressOffTargetRepos(ctx, sched, sched.Target, now, loc, advance)
	}
	if off, err := s.standingWorkOff(ctx, sched, sched.Target); err != nil || off {
		if err != nil {
			return ScheduleFiring{}, err
		}
		return s.suppressStandingWorkOff(ctx, sched, sched.Target, now, loc, advance)
	}
	if off, mode, err := s.workflowGrainCIOff(ctx, sched.Target); err != nil || off {
		if err != nil {
			return ScheduleFiring{}, err
		}
		return s.suppressWorkflowGrainCIOff(ctx, sched, sched.Target, mode, now, loc, advance)
	}

	// The review this firing is going ahead over, if it found one: empty
	// unless a previous firing's review has been open past the hold, and
	// carried down to the note and to the caller below.
	var abandoned string
	if !sched.Overlap.Allows() {
		hold, err := s.workflowFiringInFlight(ctx, sched, now, loc)
		if err != nil {
			return ScheduleFiring{}, err
		}
		if hold.unfinished() {
			reason := SuppressedByOpenReview
			if hold.Run {
				reason = SuppressedByWorkflowRun
			}
			return ScheduleFiring{Skipped: true, OpenReview: hold.heldReview(),
				Skip: s.overlapSkip(ctx, sched, now, reason, hold.heldReview())}, nil
		}
		abandoned = hold.Review
	}

	// Written before the firing is recorded, so that a store this fails
	// against leaves the whole firing to the next cycle rather than an
	// advanced cadence with nobody told why: the report of an abandoned
	// review is the point of firing over it, and losing it would put
	// this back where it started. Retrying costs at worst a second note
	// on a review nobody is reading.
	if abandoned != "" {
		if err := s.noteAbandonedReview(ctx, sched, abandoned, now); err != nil {
			return ScheduleFiring{}, err
		}
	}
	if err := s.PutScheduledWorkflowRun(ctx, ScheduledWorkflowRun{
		ScheduleID: sched.ID,
		Stem:       stem,
		Repo:       sched.Target,
		// Empty for a schedule that names no base, and left empty: a
		// cron with no branch means the repository's default one, and
		// which branch that is is a question for GitHub rather than for
		// the store (orchestrator.scheduledWorkflowRef fills it in when
		// it starts the run).
		Branch:  sched.Base,
		FiredAt: now,
	}); err != nil {
		return ScheduleFiring{}, err
	}
	// The display cache these schedules keep is the template's title, and
	// that template is this build's own code (SystemTemplate) rather than
	// a row anybody edits -- so there is nothing here to resync, and the
	// firing is recorded for its timing alone.
	if err := s.recordFiring(ctx, sched, now, loc, advance, nil); err != nil {
		return ScheduleFiring{}, err
	}
	return ScheduleFiring{WorkflowRun: true, OpenReview: abandoned}, nil
}

// workflowFiringHold is what a workflow schedule's previous firing is
// doing to this one: the answer workflowFiringInFlight reads out of the
// store, in enough detail that a page can *say* which half is holding
// the cadence and until when (Store.SuppressedFiring) rather than only
// deciding whether to fire.
type workflowFiringHold struct {
	// Run is the run half: this schedule's last firing has a CI run
	// recorded that has not ended, or has ended with nobody sent to read
	// it yet (ScheduledWorkflowRun.Settled). Bounded one level up, by
	// orchestrator.scheduledWorkflowDeadline.
	Run bool
	// Review is the previous firing's review task when there is one
	// still open, whichever answer that leads to: what is holding the
	// cadence while Held is set, and the review grain has given up on
	// when it is not. Empty when the run half is what is unfinished (not
	// asked for in that case, since the answer is the same either way)
	// and when nothing is open at all.
	Review string
	// Held says that review is still inside the hold, and so still
	// counts as a firing in flight (workflowReviewHold).
	Held bool
	// Until is when that hold expires -- the instant grain will fire
	// over this review rather than waiting for it. Zero when there is no
	// open review, and when there is one whose filing time cannot be
	// measured (reviewFiledAt), which holds the cadence with no date to
	// put on it.
	Until time.Time
}

// unfinished is the question the firing itself asks: is the previous
// firing still in flight, either half of it.
func (h workflowFiringHold) unfinished() bool { return h.Run || h.Held }

// heldReview is the review holding the cadence, as against one grain has
// stopped waiting for: ScheduleFiring.OpenReview means the first of
// those beside Skipped and the second without it, so a caller reporting
// a suppressed firing must not name a review that is not the reason.
func (h workflowFiringHold) heldReview() string {
	if !h.Held {
		return ""
	}
	return h.Review
}

// workflowFiringInFlight reads whether this schedule's previous firing
// is still in flight -- see fireWorkflowSchedule's own doc comment on
// why that is two questions rather than one, and why neither of them
// waits for ever.
//
// It writes nothing, which is what lets Store.SuppressedFiring ask the
// same question the firing asks without firing anything.
func (s *Store) workflowFiringInFlight(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location) (workflowFiringHold, error) {

	row, err := s.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil {
		return workflowFiringHold{}, err
	}
	if row != nil && !row.Settled() {
		return workflowFiringHold{Run: true}, nil
	}
	open, err := s.OpenTaskWithTag(ctx, ScheduleFiringTag(sched.ID))
	if err != nil {
		return workflowFiringHold{}, fmt.Errorf("checking for a previous unfinished firing: %w", err)
	}
	if open == nil {
		return workflowFiringHold{}, nil
	}
	filed := reviewFiledAt(*open, sched)
	if filed.IsZero() {
		// Unmeasurable, so the caller keeps waiting rather than guessing
		// -- reviewFiledAt's own doc comment. No Until: there is no
		// instant to report, which is not the same as reporting one in
		// the past.
		return workflowFiringHold{Review: open.ID, Held: true}, nil
	}
	until := workflowReviewHold(sched, filed, loc)
	return workflowFiringHold{Review: open.ID, Held: now.Before(until), Until: until}, nil
}

// reviewFiledAt is when the review holding a cadence was filed, which is
// what the hold below is measured from.
//
// Task.CreatedAt is the answer whenever there is one -- the review is
// filed cycles after the firing it reports on (FileWorkflowReview), and
// how long *the review* has been sitting there is the question. A task
// written before that column existed falls back to the schedule's own
// last firing, which is the same instant to within a run: the review is
// filed by the firing recorded then. A schedule with neither has nothing
// to measure, and the caller keeps waiting rather than guessing -- an
// unmeasurable review is not evidence of an abandoned one.
func reviewFiledAt(review Task, sched Schedule) time.Time {
	if review.CreatedAt != nil {
		return *review.CreatedAt
	}
	if sched.LastRunAt != nil {
		return *sched.LastRunAt
	}
	return time.Time{}
}

// workflowReviewHoldCadences is how many of a schedule's own occurrences
// an unread review holds its cadence for before grain stops counting it
// as a firing in flight. Three nights of a nightly: long enough that a
// review filed on Friday and read on Monday morning is not walked over,
// short enough that a suite which has stopped running is reported within
// the week.
const workflowReviewHoldCadences = 3

// workflowReviewHoldFloor is the least that hold can ever be, whatever
// the cadence. A workflow with an hourly cron would otherwise abandon
// its own review before anybody had read the notification, and the point
// of the bound is an unattended halt rather than an impatient one.
const workflowReviewHoldFloor = 24 * time.Hour

// workflowReviewHold is the instant a review filed at filed stops
// holding sched's cadence: workflowReviewHoldCadences occurrences of the
// schedule's own recurrence after it was filed, and never sooner than
// workflowReviewHoldFloor.
//
// A multiple of the cadence rather than a fixed duration because what is
// being bounded is measured in firings -- "this schedule has missed
// three of its own nights" means the same thing to a nightly and to a
// weekly, where "36 hours" means a lost night to one and nothing at all
// to the other. It is orchestrator.scheduledWorkflowDeadline's argument
// one level up ("a run whose engine has forgotten to conclude it must
// not hold the cadence for ever, since nothing fires again until this
// firing is reviewed"), which applies word for word to a review nobody
// closed: an ordinary agent task can wait on a human indefinitely, and
// the halt it caused was invisible precisely because this review is the
// thing that would have noticed it.
func workflowReviewHold(sched Schedule, filed time.Time, loc *time.Location) time.Time {
	hold := filed
	for i := 0; i < workflowReviewHoldCadences; i++ {
		hold = sched.Recurrence.Next(hold, loc)
	}
	if floor := filed.Add(workflowReviewHoldFloor); hold.Before(floor) {
		return floor
	}
	return hold
}

// noteAbandonedReview writes on a review task that has held its
// schedule's cadence past the hold that grain has fired the schedule
// again anyway.
//
// On the review itself rather than anywhere else because that is where
// somebody is eventually going to look, and because the note is about
// that task: it was filed to read one night's run, nobody closed it, and
// what it says about that night is now several nights stale. The task is
// left exactly as it is otherwise -- not closed, not un-parked (a park
// is lifted by a reply through pkg/ui, never by a store write of
// grain's own), because deciding an unread report has been abandoned is
// not the same as deciding it was unimportant.
//
// Attributed to the schedule, like every task it files: this is the
// cadence speaking about itself.
func (s *Store) noteAbandonedReview(ctx context.Context, sched Schedule, reviewID string,
	now time.Time) error {

	if _, err := s.AddComment(ctx, Comment{
		TaskID:    reviewID,
		Author:    Attribution{Actor: SchedulePrincipal},
		Body:      AbandonedReviewComment(sched, now),
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("noting on %s that its schedule has fired again: %w", reviewID, err)
	}
	return nil
}

// AbandonedReviewComment is what grain writes on a review nobody closed,
// at the firing that goes ahead over it -- the sentence beside the rule,
// here rather than inside the store write so that the vocabulary is one
// thing and can be read by a test and by anybody wondering where the
// comment came from.
func AbandonedReviewComment(sched Schedule, now time.Time) string {
	return fmt.Sprintf("This review has been open long enough that grain has stopped waiting for it: "+
		"schedule %q fired again at %s rather than skipping another occurrence.\n\n"+
		"A firing of this schedule is not finished until its review has been read, and while one is "+
		"open every later firing is suppressed -- so a review nobody closes stops the schedule "+
		"altogether, which is the one failure this review exists to catch. After %d occurrences "+
		"(and at least %s) grain fires anyway and says so here.\n\n"+
		"Nothing has been done to this task: it is still open, and the run it reports on is still "+
		"worth reading. If it is waiting on an answer, it is still waiting.",
		sched.Title, now.UTC().Format(time.RFC3339), workflowReviewHoldCadences, workflowReviewHoldFloor)
}

// FileWorkflowReview files the task that reads one finished scheduled
// workflow run: the second half of a firing fireWorkflowSchedule started,
// made cycles later by orchestrator.reconcileScheduledWorkflows once the
// run has a verdict.
//
// It is the same composition a firing makes (composeScheduleTask), so
// what is filed is what the cadence would have filed and what
// ui.Client.ScheduleComposition previews -- the only differences are
// when, and the extra tag naming the firing it belongs to, which is what
// keeps a daemon restarted mid-write from filing two reviews for one
// night.
//
// sched is the schedule as it stands, or one the caller reconstructed
// from the firing's own row for a schedule whose declaration has gone
// since it fired: a workflow whose cron was removed this morning still
// owes a report on the run it started last night, and refusing to file
// one would make a removed cron the way to lose a night silently.
//
// The target-repo list is deliberately not re-asked here. The firing
// checked it, the run has already happened, and a review is a report
// about the past rather than work dispatched into a repository --
// suppressing it would leave a run nobody ever reads, which is the one
// outcome this whole mechanism exists to prevent.
//
// filed is false for a review that already existed: the id still comes
// back, so a caller interrupted after the task was written but before it
// could record that finishes the job rather than starting it again.
func (s *Store) FileWorkflowReview(ctx context.Context, sched Schedule, row ScheduledWorkflowRun,
	stateRepo RepoRef, now time.Time) (taskID string, filed bool, err error) {

	tag := ScheduledWorkflowFiringTag(row.ScheduleID, row.FiredAt)
	existing, err := s.TaskIDWithTag(ctx, tag)
	if err != nil {
		return "", false, fmt.Errorf("checking for an existing review of %s: %w", row.ScheduleID, err)
	}
	if existing != "" {
		return existing, false, nil
	}
	comp, _, err := s.composeScheduleTask(ctx, sched, stateRepo)
	if err != nil {
		return "", false, err
	}
	id, err := s.putScheduleTask(ctx, comp, []string{ScheduleFiringTag(row.ScheduleID), tag}, now)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// FiringCustomFields is what a firing actually carries for this
// deployment's declared fields, given what the schedule (or its
// template) says it should: the values still declared, canonicalized by
// their own declaration, and nothing else.
//
// Declarations are read here rather than trusted from when the schedule
// was written, because that is the whole difference between a value
// applied at firing time and one resolved once. A schedule written in
// March against a field retired in June is not an error to report to
// anybody -- there is no request in flight, no human waiting, and the
// firing it would fail is a chore that would then quietly stop running
// until somebody noticed. The value is dropped from that firing and the
// task is filed, which is exactly what the schedule saying nothing about
// that field would have done (FilterCustomFieldValues).
//
// A schedule carrying no values at all reads no declarations: the
// ordinary firing costs the query it always did.
//
// Exported because a schedule's firing is not the only thing filed from
// a template's stored values: orchestrator.fileReviewTask sifts a review
// template's the same way, for the same reason.
func (s *Store) FiringCustomFields(ctx context.Context, values map[string][]string) (map[string][]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	fields, err := s.ListCustomFields(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the declared custom fields: %w", err)
	}
	kept, _ := FilterCustomFieldValues(fields, values)
	return kept, nil
}

// fireSuiteSchedule starts one run of the suite sched names --
// bwsalmon/agents#642's own "run the suite against a repo and branch",
// started by sched's cadence instead of by a human clicking "Run…", and
// otherwise exactly the run ui.Client.CreateSuiteRun would have made:
// the suite decides its own items, mode, passes, approval and
// auto-merge, and sched decides only when it runs and what it runs
// against.
//
// The gate here is a run of this schedule that has not finished yet, not
// an open task: a suite run is a whole sequence of tasks over as many
// passes as its mode asks for, so "has the previous firing finished" is a
// question about the run, which is what Store.HasActiveRunForSchedule
// answers. Same reasoning as the task path's own tag check (and the same
// consequence: a suppressed firing leaves NextRunAt where it was, so it
// is delayed rather than skipped), and the same per-schedule choice about
// whether to make it at all: OverlapAllow starts a second run alongside
// the first, exactly as it files a second task there.
//
// A suite deleted out from under a schedule (ui.Client.DeleteSuite tries
// to prevent this, the same way DeleteTemplate does for a template) fails
// this one firing with a plain error, retried next cycle --
// fireTaskSchedule's own missing-template path, unchanged in shape.
func (s *Store) fireSuiteSchedule(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location, advance bool) (ScheduleFiring, error) {

	// sched.Target is the repo here, with nothing to resolve first: a
	// suite decides what it runs and this schedule decides what it runs
	// against, so the run started below is pointed at exactly this
	// (CreateScheduledSuiteRun). A run whose every task the list would
	// park is suppressed the same way a single task's firing is --
	// suppressOffTargetRepos on why nothing is disabled to do it. Both
	// are asked ahead of the overlap check, for fireTaskSchedule's reason.
	off, err := s.offTargetRepos(ctx, sched.Target)
	if err != nil {
		return ScheduleFiring{}, err
	}
	if off {
		return s.suppressOffTargetRepos(ctx, sched, sched.Target, now, loc, advance)
	}
	if off, err := s.standingWorkOff(ctx, sched, sched.Target); err != nil || off {
		if err != nil {
			return ScheduleFiring{}, err
		}
		return s.suppressStandingWorkOff(ctx, sched, sched.Target, now, loc, advance)
	}

	if !sched.Overlap.Allows() {
		active, err := s.HasActiveRunForSchedule(ctx, sched.ID)
		if err != nil {
			return ScheduleFiring{}, fmt.Errorf("checking for a previous unfinished firing: %w", err)
		}
		if active {
			return ScheduleFiring{Skipped: true,
				Skip: s.overlapSkip(ctx, sched, now, SuppressedBySuiteRun, "")}, nil
		}
	}

	suite, err := s.GetSuite(ctx, *sched.SuiteID)
	if err != nil {
		return ScheduleFiring{}, fmt.Errorf("resolving suite %s: %w", *sched.SuiteID, err)
	}
	if suite == nil {
		return ScheduleFiring{}, fmt.Errorf("suite %s no longer exists", *sched.SuiteID)
	}
	run, err := s.CreateScheduledSuiteRun(ctx, *suite, sched.Target, sched.Base, sched.ID, now)
	if err != nil {
		return ScheduleFiring{}, fmt.Errorf("starting a run of suite %s: %w", suite.ID, err)
	}

	if err := s.recordFiring(ctx, sched, now, loc, advance, func(sc *Schedule) {
		// The suite's own name, as this schedule's display cache -- the
		// same one-firing-behind sync a template-backed schedule keeps
		// (Schedule.SuiteID's own doc comment), so ui.scheduleFrom can
		// name what a schedule runs with no extra lookup.
		sc.Title = suite.Name
	}); err != nil {
		return ScheduleFiring{}, err
	}
	return ScheduleFiring{SuiteRunID: run.ID}, nil
}

// recordFiring records that sched fired at now, applying whatever
// display-cache sync the firing itself implies (sync, which may be nil),
// and -- for a firing its own cadence asked for (advance) -- moves it on
// to its next occurrence.
//
// Recurrence is validated at creation (ui.Client.CreateSchedule/
// UpdateSchedule), so Next always moves strictly forward -- the loop here
// is what gives a schedule paused (or a daemon down) through several
// missed occurrences exactly one firing on resume, resynced to its normal
// cadence, rather than one firing per missed occurrence or drift against
// wall-clock time (Recurrence.Next's own doc comment).
//
// A firing somebody asked for by hand (FireScheduleNow, advance false)
// leaves NextRunAt alone and needs no location at all: it is an extra
// firing, not a rescheduling, so the occurrence this schedule was already
// going to have still happens when it was going to.
func (s *Store) recordFiring(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location, advance bool, sync func(*Schedule)) error {

	next := sched.NextRunAt
	if advance {
		next = nextOccurrence(sched, now, loc)
	}
	if err := s.UpdateSchedule(ctx, sched.ID, func(sc *Schedule) error {
		sc.LastRunAt = &now
		if advance {
			sc.NextRunAt = next
		}
		if sync != nil {
			title := sc.Title
			sync(sc)
			// A schedule a repository declares has a name of its own --
			// the file's `name:`, or its stem (repodecl's parseSchedule)
			// -- rather than a copy of what it fires, and the next sync
			// of that repository writes it back. Overwriting it here
			// renamed the row to its template's title (or suite's name)
			// at every firing until the next poll renamed it back
			// (grain/task-1358).
			if IsRepoDeclaredID(sc.ID) && title != "" {
				sc.Title = title
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("recording the firing: %w", err)
	}
	return nil
}

// nextOccurrence is where sched's cadence lands once the occurrence that
// has just come round is over -- the loop recordFiring's own doc comment
// explains, kept here because a suppressed firing moves the cadence on
// without recording anything (suppressOffTargetRepos) and the two must
// land on the same occurrence.
func nextOccurrence(sched Schedule, now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = sched.Location()
	}
	next := sched.NextRunAt
	for !next.After(now) {
		next = sched.Recurrence.Next(next, loc)
	}
	return next
}
