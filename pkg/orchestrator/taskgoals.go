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

package orchestrator

// Goal-based completion (docs/design/durable-tasks.md#DT-4).
//
// reconcileTaskGoals checks every open task's goal (model.TaskGoal) each
// cycle. A goal met with on_met close closes its task with outcome
// goal-met and a note naming the evidence; a live run is told first, by
// addendum, and cancelled once the pause grace (model.PauseGracePeriod)
// has passed. A goal met with on_met rest puts its task to rest.
//
// It also accounts progress, once per finished run: a push, a task
// action, the goal's own measure moving, or a person posting to the task
// is progress, and a run with none of them is a resume without it. A
// goal past its limit (TaskGoal.AtLimit) raises task.no-progress:<task>
// and holds the task (on_limit hold) or backs it off (on_limit notify).
//
// The other half is in the finish path (restOnGoal): an agent ending a
// goal task's run never completes it.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// goalChecker is who reconcileTaskGoals acts as.
var goalChecker = model.Principal{Kind: model.PrincipalAutomation, ID: "task-goals"}

// errGoalMet is the cancellation cause of a run stopped because its
// task's goal was met.
var errGoalMet = errors.New("orchestrator: run stopped because its task's goal was met")

// goalTaskActionTools are the tools whose use by a run is a task action:
// filing, closing, merging, reordering or retrying a task.
var goalTaskActionTools = []string{"propose_task", "retrigger_ci", "revert_task", "revert_merge"}

// goalReading is what one check of a goal saw.
type goalReading struct {
	met bool
	// evidence is what the check saw, in words: why it is met, or why not.
	evidence string
	// measure is the goal's own measure, compared between checks to tell
	// whether it moved.
	measure string
}

// checkGoal reads g's condition as it stands.
func checkGoal(ctx context.Context, deps Deps, g model.TaskGoal, now time.Time) (goalReading, error) {
	store := deps.Store
	switch g.Kind {
	case model.GoalBaseGreen:
		repo, err := model.ParseRepo(g.Spec.Repo)
		if err != nil {
			return goalReading{}, err
		}
		row, err := store.GetBaseBranchCI(ctx, repo, g.Spec.Branch)
		if err != nil || row == nil {
			return goalReading{evidence: fmt.Sprintf("no grain CI verdict on `%s` of %s yet", g.Spec.Branch, repo)}, err
		}
		return baseGreenReading(ctx, store, g, *row, now)
	case model.GoalPRMerged:
		taskID := g.Spec.Task
		what := "task " + taskID + "'s pull request"
		if taskID == "" {
			ref := fmt.Sprintf("%s#%d", g.Spec.Repo, g.Spec.Number)
			what = ref
			id, err := store.TaskForPullRequest(ctx, ref)
			if err != nil {
				return goalReading{}, err
			}
			if id == "" {
				return goalReading{evidence: ref + " is not a pull request grain tracks", measure: "unknown"}, nil
			}
			taskID = id
		}
		obs, err := store.GetObservation(ctx, taskID)
		if err != nil {
			return goalReading{}, err
		}
		switch {
		case obs != nil && obs.PrMergedAt != nil:
			return goalReading{met: true, measure: "merged",
				evidence: fmt.Sprintf("%s merged at %s", what, obs.PrMergedAt.UTC().Format(time.RFC3339))}, nil
		case obs != nil && obs.PrClosedAt != nil:
			return goalReading{measure: "closed", evidence: what + " closed without merging"}, nil
		case obs != nil && obs.PrOpenedAt != nil:
			return goalReading{measure: "open", evidence: what + " is open"}, nil
		}
		return goalReading{measure: "none", evidence: what + " has not been opened"}, nil
	case model.GoalTasksClosed:
		done := 0
		var open []string
		for _, id := range g.Spec.Tasks {
			ok, err := goalTaskReached(ctx, store, id, g.Spec.Until)
			if err != nil {
				return goalReading{}, err
			}
			if ok {
				done++
			} else {
				open = append(open, id)
			}
		}
		measure := fmt.Sprintf("%d/%d", done, len(g.Spec.Tasks))
		if len(open) == 0 {
			return goalReading{met: true, measure: measure,
				evidence: fmt.Sprintf("all %d task(s) %s: %s", done, g.Spec.Until, strings.Join(g.Spec.Tasks, ", "))}, nil
		}
		return goalReading{measure: measure,
			evidence: fmt.Sprintf("%s %s; still waiting on %s", measure, g.Spec.Until, strings.Join(open, ", "))}, nil
	case model.GoalPipelineHealthy:
		open, err := openPipelineNotifications(ctx, store, g.Spec.Repos)
		if err != nil {
			return goalReading{}, err
		}
		measure := fmt.Sprintf("%d", len(open))
		if len(open) > 0 {
			return goalReading{measure: measure,
				evidence: fmt.Sprintf("%d pipeline notification(s) open: %s", len(open), strings.Join(open, ", "))}, nil
		}
		quiet, why, err := pipelineStewardQuiet(ctx, deps, now)
		if err != nil {
			return goalReading{}, err
		}
		if !quiet {
			return goalReading{measure: measure, evidence: why}, nil
		}
		return goalReading{met: true, measure: measure, evidence: "the pipeline is quiet and no pipeline notification is open"}, nil
	}
	// person-closes: only a person closing the task ends it.
	return goalReading{evidence: "waiting for a person to close it"}, nil
}

// baseGreenReading is a base-green goal's reading of the branch's
// recorded grain CI verdict.
func baseGreenReading(ctx context.Context, store *model.Store, g model.TaskGoal, row model.BaseBranchCI,
	now time.Time) (goalReading, error) {

	measure := strings.Join([]string{row.Commit, row.Conclusion, string(row.Confirmation), row.Detail}, "|")
	green := !baseCIInFlight(row) && !row.Broken() &&
		(row.Conclusion == string(ci.ConclusionSuccess) || row.Confirmation == model.BaseCICleared)
	if !green {
		what := "red"
		if baseCIInFlight(row) {
			what = "being checked"
		}
		evidence := fmt.Sprintf("`%s` on %s is %s at %s", row.Branch, row.Repo, what, shortSHA(row.Commit))
		if row.Detail != "" && what == "red" {
			evidence += ": " + row.Detail
		}
		return goalReading{measure: measure, evidence: evidence}, nil
	}
	evidence := fmt.Sprintf("`%s` on %s green at %s (grain CI run %s)", row.Branch, row.Repo, shortSHA(row.Commit), row.RunID)
	if row.Confirmation == model.BaseCICleared {
		evidence = fmt.Sprintf("the failure on `%s` of %s at %s did not reproduce (%s)",
			row.Branch, row.Repo, shortSHA(row.Commit), row.ConfirmDetail)
	}
	if g.Spec.Publishing {
		rel, err := store.GetBaseBranchRelease(ctx, row.Repo, row.Branch)
		if err != nil {
			return goalReading{}, err
		}
		if rel != nil {
			if p, behind := model.BaseBranchPublishingOf(*rel, now); behind {
				return goalReading{measure: measure,
					evidence: fmt.Sprintf("%s, but it has published nothing since (%d release(s) failing)", evidence, p.Failures)}, nil
			}
			if rel.Commit != "" {
				evidence += ", release published at " + shortSHA(rel.Commit)
			}
		}
	}
	return goalReading{met: true, measure: measure, evidence: evidence}, nil
}

// goalTaskReached reports whether task id has closed, or with until
// "merged", merged.
func goalTaskReached(ctx context.Context, store *model.Store, id, until string) (bool, error) {
	if until == "merged" {
		obs, err := store.GetObservation(ctx, id)
		return err == nil && obs != nil && obs.PrMergedAt != nil, err
	}
	st, err := store.State(ctx, id)
	return st == model.StateClosed, err
}

// openPipelineNotifications is the subjects of the open notifications in
// the pipeline.* family, about one of repos when it names any.
func openPipelineNotifications(ctx context.Context, store *model.Store, repos []string) ([]string, error) {
	all, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range all {
		if !strings.HasPrefix(n.Family, "pipeline.") {
			continue
		}
		if len(repos) > 0 && n.Repo != "" && !slices.Contains(repos, n.Repo) {
			continue
		}
		out = append(out, n.Subject)
	}
	return out, nil
}

// reconcileTaskGoals is the cycle's goal arm.
func reconcileTaskGoals(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	var errs []error
	if err := ensureRepairGoals(ctx, deps.Store, now); err != nil {
		errs = append(errs, err)
	}
	goals, err := deps.Store.OpenTaskGoals(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, g := range goals {
		if err := reconcileTaskGoal(ctx, deps, g, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: task %s's goal: %w", g.TaskID, err))
		}
	}
	return errors.Join(errs...)
}

// ensureRepairGoals declares base-green, on_met close, on every open
// base-branch repair -- grain's own and every fixes_base_branch one --
// that has no goal yet: the one-time pass the design's migration asks
// for, and the backstop for a repair marked by any path that does not
// declare it itself.
func ensureRepairGoals(ctx context.Context, store *model.Store, now time.Time) error {
	ids, err := store.OpenTasksWithTagPrefix(ctx, model.BaseRepairTagPrefix)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if g, err := store.TaskGoal(ctx, id); err != nil || g != nil {
			errs = append(errs, err)
			continue
		}
		task, err := store.GetTask(ctx, id)
		if err != nil || task == nil {
			errs = append(errs, err)
			continue
		}
		if err := declareRepairGoal(ctx, store, *task, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// declareRepairGoal gives a base-branch repair its base-green goal.
func declareRepairGoal(ctx context.Context, store *model.Store, task model.Task, now time.Time) error {
	repo, branch, ok := model.BaseRepairBranchOf(task)
	if !ok {
		return nil
	}
	return store.SetTaskGoal(ctx, task.ID, model.GoalDecl{
		Kind: model.GoalBaseGreen, Spec: model.GoalSpec{Repo: repo.String(), Branch: branch},
		OnMet: model.GoalCloseOnMet,
	}, model.PrincipalString(goalChecker), now)
}

// checkBaseGreenGoals is the goal check for every open base-green goal
// on row's branch, against row itself: what a verdict landing on a base
// branch does to the tasks waiting for it to be green, on the same tick
// (closeBaseRepairsOnGreen).
func checkBaseGreenGoals(ctx context.Context, deps Deps, row model.BaseBranchCI, now time.Time) error {
	goals, err := deps.Store.OpenTaskGoals(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, g := range goals {
		if g.Kind != model.GoalBaseGreen || g.Spec.Repo != row.Repo.String() || g.Spec.Branch != row.Branch {
			continue
		}
		task, err := deps.Store.GetTask(ctx, g.TaskID)
		if err != nil || task == nil {
			errs = append(errs, err)
			continue
		}
		state, err := deps.Store.State(ctx, g.TaskID)
		if err != nil || state == model.StateClosed || state == "" {
			errs = append(errs, err)
			continue
		}
		reading, err := baseGreenReading(ctx, deps.Store, g, row, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := actOnGoal(ctx, deps, *task, state, g, reading, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: task %s's goal: %w", g.TaskID, err))
		}
	}
	return errors.Join(errs...)
}

// reconcileTaskGoal checks one goal and acts on what it finds.
func reconcileTaskGoal(ctx context.Context, deps Deps, g model.TaskGoal, now time.Time) error {
	store := deps.Store
	task, err := store.GetTask(ctx, g.TaskID)
	if err != nil || task == nil {
		return err
	}
	state, err := store.State(ctx, g.TaskID)
	if err != nil {
		return err
	}
	if state == model.StateClosed || state == "" {
		return nil
	}
	reading, err := checkGoal(ctx, deps, g, now)
	if err != nil {
		return err
	}
	return actOnGoal(ctx, deps, *task, state, g, reading, now)
}

// actOnGoal is what a goal's reading does to its task: close or rest it
// when it is met, account its progress, and apply its limit.
func actOnGoal(ctx context.Context, deps Deps, task model.Task, state model.State, g model.TaskGoal,
	reading goalReading, now time.Time) error {

	store := deps.Store
	running := state == model.StateRunning
	before := g
	if err := accountGoalProgress(ctx, store, &g, reading, now); err != nil {
		return err
	}
	if reading.met {
		return meetGoal(ctx, deps, task, g, reading, running, now)
	}
	g.MetAt, g.Evidence = nil, reading.evidence
	if err := applyGoalLimit(ctx, store, task, &g, before, running, now); err != nil {
		return err
	}
	// A task resting on its goal is given another look when the goal's
	// own measure moves -- the failing jobs changed, a task closed --
	// unless it is held at its limit.
	if !running && before.Measure != "" && reading.measure != before.Measure && g.LimitNotifiedAt == nil &&
		task.DeferredAt != nil && task.DeferredUntil == nil {
		resting, err := restingOnGoal(ctx, store, task.ID)
		if err != nil {
			return err
		}
		if resting {
			log.Printf("orchestrator: task %s's goal moved (%s); waking it", task.ID, reading.evidence)
			if err := store.SetDeferred(ctx, task.ID, nil, nil, now); err != nil {
				return err
			}
		}
	}
	return store.SaveTaskGoalProgress(ctx, g)
}

// restingOnGoal reports whether taskID's latest open suspension is the
// one restOnGoal left: requested, resuming on the goal.
func restingOnGoal(ctx context.Context, store *model.Store, taskID string) (bool, error) {
	sus, err := store.Suspensions(ctx, taskID)
	if err != nil || len(sus) == 0 {
		return false, err
	}
	last := sus[len(sus)-1]
	if last.ResumedAt != nil || last.Kind != model.SuspendRequested {
		return false, nil
	}
	on, err := model.ParseResumeOn(last.ResumeOn)
	return err == nil && on.Goal, nil
}

// meetGoal is what a met goal does: close or rest its task, telling a
// live run first.
func meetGoal(ctx context.Context, deps Deps, task model.Task, g model.TaskGoal, reading goalReading,
	running bool, now time.Time) error {

	store := deps.Store
	firstMet := g.MetAt == nil
	if firstMet {
		g.MetAt, g.Evidence = &now, reading.evidence
	}
	if g.OnMet == model.GoalRestOnMet {
		if firstMet {
			if err := goalComment(ctx, store, task.ID, fmt.Sprintf("grain: this task's goal (%s) is met: %s. "+
				"The task rests here until a person or a message moves it.", g.Kind, g.Evidence), now); err != nil {
				return err
			}
			if !running && task.DeferredAt == nil {
				if err := store.SetDeferred(ctx, task.ID, &now, nil, now); err != nil {
					return err
				}
			}
		}
		return store.SaveTaskGoalProgress(ctx, g)
	}
	if running {
		if firstMet {
			if err := goalComment(ctx, store, task.ID, fmt.Sprintf("grain: this task's goal (%s) is met: %s. "+
				"Commit, push and write your handover: this run will be stopped and the task closed in %s.",
				g.Kind, g.Evidence, humanDuration(model.PauseGracePeriod)), now); err != nil {
				return err
			}
		} else if now.Sub(*g.MetAt) >= model.PauseGracePeriod &&
			deps.Config.TaskCancellers.Cancel(task.ID, errGoalMet) {
			log.Printf("orchestrator: run of task %s stopped: its goal is met", task.ID)
		}
		return store.SaveTaskGoalProgress(ctx, g)
	}
	g.Outcome = model.GoalMetOutcome
	if err := store.SaveTaskGoalProgress(ctx, g); err != nil {
		return err
	}
	return closeOnGoal(ctx, store, task, g, now)
}

// closeOnGoal closes task with outcome goal-met and a note naming the
// evidence.
func closeOnGoal(ctx context.Context, store *model.Store, task model.Task, g model.TaskGoal, now time.Time) error {
	if err := observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.ClosedAt = &now
		if o.CompletedAt == nil {
			o.CompletedAt = &now
		}
	}); err != nil {
		return err
	}
	log.Printf("orchestrator: closing %s: its goal is met (%s)", task.ID, g.Evidence)
	if err := goalComment(ctx, store, task.ID, fmt.Sprintf("Closed (%s): this task's goal (%s) is met -- %s.",
		model.GoalMetOutcome, g.Kind, g.Evidence), now); err != nil {
		return err
	}
	return clearPipelineNotification(ctx, store, model.NotificationSubject(model.FamilyNoProgress, task.ID),
		model.PrincipalString(goalChecker))
}

func goalComment(ctx context.Context, store *model.Store, taskID, body string, now time.Time) error {
	_, err := store.AddComment(ctx, model.Comment{
		TaskID: taskID, Author: model.Attribution{Actor: goalChecker}, Body: body, CreatedAt: now,
	})
	return err
}

// accountGoalProgress counts g's newest finished run, once, and folds
// in progress that needs no run: the measure moving, a person posting.
func accountGoalProgress(ctx context.Context, store *model.Store, g *model.TaskGoal, reading goalReading,
	now time.Time) error {

	runs, err := store.Runs(ctx, g.TaskID)
	if err != nil {
		return err
	}
	var latest *model.Run
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].FinishedAt != nil {
			latest = &runs[i]
			break
		}
	}
	// The first check of a goal is its baseline: a run that finished
	// before it is not counted against it.
	if g.Measure == "" && g.CountedRunID == "" {
		g.Measure = reading.measure
		if latest != nil {
			g.CountedRunID = latest.ID
		}
		return nil
	}
	progress := reading.measure != g.Measure
	g.Measure = reading.measure
	posted, err := personPostedSince(ctx, store, g.TaskID, g.ProgressAt)
	if err != nil {
		return err
	}
	progress = progress || posted
	newRun := latest != nil && latest.ID != g.CountedRunID
	if newRun {
		g.CountedRunID = latest.ID
		did, err := store.RunMadeProgress(ctx, latest.ID, goalTaskActionTools)
		if err != nil {
			return err
		}
		progress = progress || did
	}
	switch {
	case progress:
		g.ProgressAt, g.ResumesSinceProgress = now, 0
		if g.LimitNotifiedAt != nil {
			g.LimitNotifiedAt = nil
			return clearPipelineNotification(ctx, store, model.NotificationSubject(model.FamilyNoProgress, g.TaskID),
				model.PrincipalString(goalChecker))
		}
	case newRun:
		g.ResumesSinceProgress++
	}
	return nil
}

// personPostedSince reports whether a person has posted to taskID after
// since.
func personPostedSince(ctx context.Context, store *model.Store, taskID string, since time.Time) (bool, error) {
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		return false, err
	}
	for _, c := range comments {
		if c.Author.Actor.Kind == model.PrincipalHuman && c.CreatedAt.After(since) {
			return true, nil
		}
	}
	return false, nil
}

// applyGoalLimit raises task.no-progress once g is past its limit and
// holds the task or backs it off; and resets g when a person has acted
// on the notification (cleared or acknowledged it, or let a held task
// continue).
func applyGoalLimit(ctx context.Context, store *model.Store, task model.Task, g *model.TaskGoal,
	before model.TaskGoal, running bool, now time.Time) error {

	subject := model.NotificationSubject(model.FamilyNoProgress, task.ID)
	if g.LimitNotifiedAt != nil {
		open, err := store.OpenNotification(ctx, subject)
		if err != nil {
			return err
		}
		continued := g.OnLimit == model.GoalHoldOnLimit && !running && task.DeferredAt == nil
		if open == nil || open.AckedAt != nil || continued {
			// Acted on: Continue, Close, Reply, Change model all end here.
			g.ProgressAt, g.ResumesSinceProgress, g.LimitNotifiedAt = now, 0, nil
			if open != nil {
				return clearPipelineNotification(ctx, store, subject, model.PrincipalString(goalChecker))
			}
			return nil
		}
		// notify: past the limit it keeps going, backing off each resume.
		if g.OnLimit == model.GoalNotifyOnLimit && !running && g.CountedRunID != before.CountedRunID {
			until := now.Add(model.GoalLimitBackoff(*g, 15*time.Minute))
			return store.SetDeferred(ctx, task.ID, &now, &until, now)
		}
		return nil
	}
	if running || !g.AtLimit(now) {
		return nil
	}
	g.LimitNotifiedAt = &now
	if _, err := raisePipelineNotification(ctx, store, model.Notification{
		Subject:  subject,
		Family:   model.FamilyNoProgress,
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("Task %s is not making progress towards its goal", task.ID),
		Body:     noProgressBody(ctx, store, task, *g),
		TaskID:   task.ID,
		RaisedBy: model.PrincipalString(goalChecker),
		ClearsWhen: &model.NotificationClear{
			Kind: model.ClearsOnTask, Task: task.ID, Until: "closed",
		},
	}, now); err != nil {
		return err
	}
	if g.OnLimit == model.GoalNotifyOnLimit {
		until := now.Add(model.GoalLimitBackoff(*g, 15*time.Minute))
		return store.SetDeferred(ctx, task.ID, &now, &until, now)
	}
	return store.SetDeferred(ctx, task.ID, &now, nil, now)
}

// noProgressBody is task.no-progress's body: the goal, the limit it
// reached, and the last three runs.
func noProgressBody(ctx context.Context, store *model.Store, task model.Task, g model.TaskGoal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Task #%s](/tasks/%s) has a `%s` goal that is not met (%s). ", task.ID, task.ID, g.Kind, g.Evidence)
	fmt.Fprintf(&b, "It has resumed %d time(s) without progress since %s (limit: %d resumes or %s). ",
		g.ResumesSinceProgress, g.ProgressAt.UTC().Format(time.RFC3339), g.MaxResumes, humanDuration(g.MaxStall))
	if g.OnLimit == model.GoalHoldOnLimit {
		b.WriteString("It is held until a person acts on it: Continue (Undefer), Close, change its model, or reply.")
	} else {
		b.WriteString("It keeps going, backing off between resumes.")
	}
	runs, err := store.Runs(ctx, task.ID)
	if err == nil && len(runs) > 0 {
		b.WriteString("\n\nLast runs:")
		for i := len(runs) - 1; i >= 0 && i >= len(runs)-3; i-- {
			r := runs[i]
			fmt.Fprintf(&b, "\n- %s: %s", r.ID, r.Outcome)
			if r.Detail != "" {
				fmt.Fprintf(&b, " -- %s", truncate(r.Detail, 200))
			}
		}
	}
	return b.String()
}

// restOnGoal is how a goal task's run ends when its agent ends it --
// comment_on_issue, a plain finish, nothing_needed: the task is not
// completed but suspended (requested), resting on its goal or a message.
// It reports whether task has a goal, and so whether it rested.
func restOnGoal(ctx context.Context, store *model.Store, task model.Task, runID string, now time.Time) (bool, error) {
	g, err := store.TaskGoal(ctx, task.ID)
	if err != nil || g == nil {
		return false, err
	}
	at := now
	if runs, err := store.Runs(ctx, task.ID); err == nil {
		for _, r := range runs {
			if r.ID == runID {
				if r.StartedAt.After(at) {
					at = r.StartedAt
				}
				if r.FinishedAt != nil && r.FinishedAt.After(at) {
					at = *r.FinishedAt
				}
				break
			}
		}
	}
	on := model.ResumeOn{Message: true, Goal: g.Kind != model.GoalPersonCloses}
	if err := store.Suspend(ctx, model.Suspension{
		RunID: runID, TaskID: task.ID, Kind: model.SuspendRequested, CreatedAt: at,
		Detail:   fmt.Sprintf("the agent ended its run; the task rests on its %s goal", g.Kind),
		ResumeOn: on.JSON(),
	}); err != nil {
		return true, fmt.Errorf("orchestrator: resting %s on its goal: %w", task.ID, err)
	}
	return true, store.SetDeferred(ctx, task.ID, &at, nil, at)
}
