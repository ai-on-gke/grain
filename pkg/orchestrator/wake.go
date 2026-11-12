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

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// DefaultCIWakeStallBound is how long a task deferred on a resume_later CI
// wake may wait, whatever CI is or is not doing on its branch, before
// grain's backstop undefers it anyway with a note, raises a stall
// notification and wakes the pipeline steward (Config.CIWakeBackstop
// overrides it).
const DefaultCIWakeStallBound = 6 * time.Hour

var (
	ciWakeStallBound = DefaultCIWakeStallBound
	wakeNotifier     = model.PrincipalString(model.Principal{Kind: model.PrincipalAutomation, ID: "wake"})
)

// reconcileWake picks back up every task whose wake time has arrived --
// a deferral that named a date (model.Task.DeferredUntil) rather than
// the indefinite kind, which nothing here touches -- and every task held
// by resume_later on a CI verdict (grain CI on ci_mode: grain, GitHub
// Actions / check runs on ci_mode: github) whose verdict has arrived or
// whose wait has outlasted the backstop (ciWakeBackstop).
//
// Undeferring is the whole of it: the task returns to whichever of
// 'proposed' and 'queued' its own approval implies, exactly as Undefer's
// button does, and it is the dispatcher that decides whether a queued
// one runs. So a wake time is a floor -- "not before" -- and never a
// promise that anything starts at that instant.
//
// It runs immediately after "schedule" and before "dispatch", which is
// the same latency preference the reconcilers around it are ordered by
// (Reconcilers' own doc comment): a task woken this cycle is dispatchable
// on this same tick rather than on the next one. Nothing depends on that
// ordering -- a wake this pass misses is a wake the next pass makes,
// since the condition it reads is a timestamp in the past and stays one.
//
// One task failing to wake does not stop the others, for the reason
// reconcileSchedule keeps firing schedules past one that failed.
func reconcileWake(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	due, err := deps.Store.DueDeferrals(ctx, now)
	if err != nil {
		return fmt.Errorf("orchestrator: listing due deferrals: %w", err)
	}
	var errs []error
	for _, id := range due {
		if err := deps.Store.SetDeferred(ctx, id, nil, nil, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: waking task %s: %w", id, err))
			continue
		}
		log.Printf("orchestrator: task %s has reached its wake time and is picked back up", id)
	}

	// A resume_later that waits on a message is released as the message
	// is written (model.Store.WakeOnMessage), but not every post reaches
	// that call -- one written while the run was still ending, or relayed
	// from a pull request -- so the posts already there are read too.
	posted, err := deps.Store.DueMessageResumes(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("orchestrator: listing tasks posted to since their resume_later: %w", err))
	}
	for _, id := range posted {
		if err := deps.Store.SetDeferred(ctx, id, nil, nil, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: waking task %s on the message it waited for: %w", id, err))
			continue
		}
		log.Printf("orchestrator: task %s has been posted to since its resume_later and is picked back up", id)
	}

	// A resume_later that waits on CI is a deferral too, released when its
	// gating CI verdict arrives (GitHub Actions / check runs on ci_mode:
	// github, grain CI on ci_mode: grain) -- success, failure or
	// cancelled alike -- when the branch moves to a new head or the run
	// it named is gone, or when the wait outlasts the backstop.
	held, err := deps.Store.HeldCIResumes(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("orchestrator: listing tasks waiting on CI: %w", err))
		return errors.Join(errs...)
	}
	for _, h := range held {
		verdictReady, _, evalErr := evaluateCIResume(ctx, deps, h, now)
		if evalErr != nil {
			// Reported, and not a reason to skip the backstop below: a
			// verdict grain cannot read, cycle after cycle, is exactly the
			// wait it exists for.
			errs = append(errs, fmt.Errorf("orchestrator: checking CI wake for task %s: %w", h.TaskID, evalErr))
			verdictReady = false
		}
		if verdictReady {
			if err := deps.Store.SetDeferred(ctx, h.TaskID, nil, nil, now); err != nil {
				errs = append(errs, fmt.Errorf("orchestrator: waking task %s on its CI verdict: %w", h.TaskID, err))
				continue
			}
			_ = clearPipelineNotification(ctx, deps.Store, model.NotificationSubject(model.FamilyCIWakeStalled, h.TaskID), wakeNotifier)
			log.Printf("orchestrator: task %s has the CI verdict it waited for and is picked back up", h.TaskID)
			continue
		}
		if ciWakeStalled(h, now, ciWakeBackstop(deps)) {
			if err := escalateStalledCIWake(ctx, deps, h, now); err != nil {
				errs = append(errs, fmt.Errorf("orchestrator: escalating stalled CI wake for task %s: %w", h.TaskID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// ciWakeBackstop is how long a CI wait may hold its task before it is
// woken regardless: Config.CIWakeBackstop, or DefaultCIWakeStallBound
// when that is zero. Negative disables the backstop.
func ciWakeBackstop(deps Deps) time.Duration {
	if deps.Config.CIWakeBackstop != 0 {
		return deps.Config.CIWakeBackstop
	}
	return ciWakeStallBound
}

// ciWakeStalled reports whether h has waited past bound. It is a
// backstop, not a judgement of whether CI is busy: a wait whose verdict
// grain failed to see -- a check that never reports, a forge read that
// keeps failing, a condition grain cannot evaluate -- must not hold its
// task for days, which is what an "only with no CI activity" rule let it
// do.
func ciWakeStalled(h model.HeldCIResume, now time.Time, bound time.Duration) bool {
	return bound > 0 && now.Sub(h.Since) >= bound
}

func escalateStalledCIWake(ctx context.Context, deps Deps, h model.HeldCIResume, now time.Time) error {
	wait := now.Sub(h.Since)
	age := formatStallAge(wait)
	branchDesc := h.Branch
	if h.Repo != (model.RepoRef{}) {
		branchDesc = fmt.Sprintf("%s:%s", h.Repo, h.Branch)
	}
	if err := deps.Store.SetDeferred(ctx, h.TaskID, nil, nil, now); err != nil {
		return err
	}
	commentBody := fmt.Sprintf(
		"grain: this task waited on CI (`%s`) on `%s` for %s, past the %s backstop on a "+
			"resume_later CI wait, with no verdict grain could see -- so grain undeferred it "+
			"anyway and raised a stall notification. Check CI's state on the branch before "+
			"assuming it passed.",
		h.TargetCI, branchDesc, humanDuration(wait), humanDuration(ciWakeBackstop(deps)),
	)
	if _, err := deps.Store.AddComment(ctx, model.Comment{
		TaskID:    h.TaskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      commentBody,
		CreatedAt: now,
	}); err != nil {
		return err
	}
	n := model.Notification{
		Subject:  model.NotificationSubject(model.FamilyCIWakeStalled, h.TaskID),
		Family:   model.FamilyCIWakeStalled,
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("Task %s is stalled waiting on CI (%s, %s)", h.TaskID, h.TargetCI, age),
		Body: fmt.Sprintf(
			"[Task #%s](/tasks/%s) sat deferred on `resume_later` waiting for CI (`%s`) on `%s` "+
				"for %s with no verdict grain could see; its backstop undeferred the task and escalated it.",
			h.TaskID, h.TaskID, h.TargetCI, branchDesc, age,
		),
		Repo:     h.Repo.String(),
		TaskID:   h.TaskID,
		RaisedBy: wakeNotifier,
		ClearsWhen: &model.NotificationClear{
			Kind: model.ClearsOnTask, Task: h.TaskID, Until: "closed",
		},
		Actions: []string{"open-task", "investigate"},
	}
	if err := ensurePipelineNotification(ctx, deps.Store, n, now); err != nil {
		return err
	}
	wakePipelineSteward(ctx, deps,
		fmt.Sprintf("task %s stalled waiting on CI (%s) for %s with no verdict seen on %s", h.TaskID, h.TargetCI, age, branchDesc),
		now)
	log.Printf("orchestrator: task %s stalled waiting on CI (%s) for %s with no verdict seen; raised notification and undeferred",
		h.TaskID, h.TargetCI, age)
	return nil
}

// evaluateCIResume checks whether h's CI wake condition is satisfied, and
// returns the latest CI activity timestamp observed on h's branch or run.
func evaluateCIResume(ctx context.Context, deps Deps, h model.HeldCIResume, now time.Time) (bool, *time.Time, error) {
	latestActivity, err := deps.Store.LatestCIActivityAt(ctx, h)
	if err != nil {
		return false, nil, err
	}
	noteActivity := func(t *time.Time) {
		if t == nil || t.IsZero() {
			return
		}
		u := t.UTC()
		if latestActivity == nil || u.After(*latestActivity) {
			latestActivity = &u
		}
	}

	switch h.CIMode {
	case model.CIModeGitHub:
		if deps.Client != nil && !gitHubCIBudgetLow(now) && h.Repo != (model.RepoRef{}) {
			liveSettled, livePending, err := pollGitHubCIResume(ctx, deps, h, now, noteActivity)
			if err != nil {
				log.Printf("orchestrator: polling GitHub CI for deferred task %s (%s): %v", h.TaskID, h.Repo, err)
			} else if liveSettled {
				return true, latestActivity, nil
			} else if livePending {
				// Checks or workflow runs are still in progress with no failure yet.
				return false, latestActivity, nil
			}
		}
		due, err := deps.Store.DueCIResume(ctx, h)
		return due, latestActivity, err

	case model.CIModeGrain:
		due, err := deps.Store.DueCIResume(ctx, h)
		if err != nil || due {
			return due, latestActivity, err
		}
		if h.TargetCI != model.ResumeOnMyBranch {
			if deps.CI != nil {
				if r, ok := deps.CI.Get(h.TargetCI); ok {
					noteActivity(r.StartedAt)
					noteActivity(r.FinishedAt)
					return r.Conclusion != "", latestActivity, nil
				}
			}
			// The runner no longer has the run, and the store has no
			// verdict for it (DueCIResume above): either the store never
			// had it, or it was in flight when the runner lost it -- a
			// restart -- and nothing will ever conclude it. Either way
			// there is no verdict left to wait for. With no runner to
			// ask, only a run the store does not have at all is gone.
			if deps.CI != nil {
				return true, latestActivity, nil
			}
			exists, err := deps.Store.HasCIRun(ctx, h.TargetCI)
			if err != nil {
				return false, latestActivity, err
			}
			return !exists, latestActivity, nil
		}
		if deps.CI != nil {
			page := deps.CI.Find(ci.RunFilter{Repo: h.Repo.String(), Ref: h.Branch}, 5)
			for _, r := range page.Runs {
				noteActivity(r.StartedAt)
				noteActivity(r.FinishedAt)
				if r.Conclusion != "" && r.FinishedAt != nil && r.FinishedAt.After(h.Since) {
					return true, latestActivity, nil
				}
			}
		}
		return false, latestActivity, nil

	default:
		return false, latestActivity, nil
	}
}

func pollGitHubCIResume(
	ctx context.Context,
	deps Deps,
	h model.HeldCIResume,
	now time.Time,
	noteActivity func(*time.Time),
) (settled bool, pending bool, err error) {
	// The branch head, read once for both forms of the wait: a branch that
	// has moved off the head the wait was taken at (model.ResumeOn.Head),
	// or that is gone, is a verdict of its own -- whatever was being
	// waited for is stale, and the task's next run is what should look.
	var headSHA string
	head, herr := deps.Client.GetBranchHead(h.Repo.Owner, h.Repo.Name, h.Branch)
	if herr == nil && head != nil {
		headSHA = strings.TrimSpace(head.SHA)
	}
	if h.Head != "" && herr == nil {
		if headSHA == "" || !strings.EqualFold(headSHA, h.Head) {
			return true, false, nil
		}
	}

	if h.TargetCI != model.ResumeOnMyBranch {
		_, runID, ok := model.ParseGitHubActionsRunID(h.TargetCI)
		if !ok {
			n, perr := strconv.ParseInt(strings.TrimSpace(h.TargetCI), 10, 64)
			if perr != nil || n <= 0 {
				return false, false, nil
			}
			runID = n
		}
		ar, hasAR := deps.Client.(github.ActionsReader)
		if !hasAR {
			return false, false, nil
		}
		wfRun, gerr := ar.GetActionsRun(h.Repo.Owner, h.Repo.Name, runID)
		if gerr != nil {
			return false, false, gerr
		}
		if wfRun == nil {
			// GitHub no longer has the run (deleted, or past retention):
			// there is no verdict left to wait for.
			return true, false, nil
		}
		noteActivity(wfRun.StartedAt)
		noteActivity(wfRun.UpdatedAt)
		// "completed" with any conclusion -- success, failure, cancelled,
		// timed_out, skipped -- is the verdict.
		if strings.EqualFold(wfRun.Status, "completed") {
			recordConcludedActionsRunJob(ctx, deps.Store, h.Repo, h.Branch, *wfRun, now)
			return true, false, nil
		}
		return false, true, nil
	}

	// h.TargetCI == "my-branch": inspect branch head check runs (via
	// ListCheckRuns + recordPolledChecks) and GitHub Actions workflow runs
	// (via ActionsReader, the same source as `grain ci list --provider github-actions`).
	var (
		extTotal     int
		extPending   int
		extFailed    int
		extAfterHold int
	)
	if headSHA != "" {
		checks, cerr := deps.Client.ListCheckRuns(h.Repo.Owner, h.Repo.Name, headSHA)
		if cerr != nil {
			return false, false, cerr
		}
		if len(checks) > 0 {
			recordPolledChecks(ctx, deps.Store, h.Repo, h.Branch, headSHA, checks, now)
		}
		for _, c := range checks {
			if isGrainOwnCheck(c.Name) {
				continue
			}
			extTotal++
			noteActivity(c.StartedAt)
			noteActivity(c.CompletedAt)
			if !strings.EqualFold(c.Status, "completed") {
				extPending++
				continue
			}
			concl := ""
			if c.Conclusion != nil {
				concl = strings.ToLower(strings.TrimSpace(*c.Conclusion))
			}
			if checkConclusionFailed(concl) {
				extFailed++
			}
			completedAt := now
			if c.CompletedAt != nil {
				completedAt = c.CompletedAt.UTC()
			}
			if completedAt.After(h.Since) {
				extAfterHold++
			}
		}
	}

	var (
		wfTotal     int
		wfPending   int
		wfFailed    int
		wfAfterHold int
	)
	if ar, ok := deps.Client.(github.ActionsReader); ok {
		runs, rerr := ar.ListActionsRuns(h.Repo.Owner, h.Repo.Name, github.ActionsRunsQuery{Branch: h.Branch, Limit: 20})
		if rerr == nil && len(runs) > 0 {
			hasHeadMatch := false
			if headSHA != "" {
				for _, r := range runs {
					if strings.EqualFold(strings.TrimSpace(r.HeadSHA), headSHA) {
						hasHeadMatch = true
						break
					}
				}
			}
			for _, r := range runs {
				if headSHA != "" && hasHeadMatch && !strings.EqualFold(strings.TrimSpace(r.HeadSHA), headSHA) {
					continue
				}
				if headSHA == "" || !hasHeadMatch {
					// Without a matching head SHA, consider only runs active after h.Since.
					latest := r.UpdatedAt
					if latest == nil {
						latest = r.StartedAt
					}
					if latest != nil && !latest.After(h.Since) && r.Status == "completed" {
						continue
					}
				}
				wfTotal++
				noteActivity(r.StartedAt)
				noteActivity(r.UpdatedAt)
				if r.Status != "completed" {
					wfPending++
					continue
				}
				if r.Conclusion == "" {
					wfPending++
					continue
				}
				recordConcludedActionsRunJob(ctx, deps.Store, h.Repo, h.Branch, r, now)
				concl := strings.ToLower(strings.TrimSpace(r.Conclusion))
				if checkConclusionFailed(concl) {
					wfFailed++
				}
				finishedAt := now
				if r.UpdatedAt != nil {
					finishedAt = r.UpdatedAt.UTC()
				}
				if finishedAt.After(h.Since) {
					wfAfterHold++
				}
			}
		}
	}

	// Any failure settles the wait immediately, matching wait_for_checks.
	if extFailed > 0 && (headSHA != "" || extAfterHold > 0) {
		return true, false, nil
	}
	if wfFailed > 0 && (headSHA != "" || wfAfterHold > 0) {
		return true, false, nil
	}
	if extPending > 0 || wfPending > 0 {
		return false, true, nil
	}
	if (extTotal > 0 && (headSHA != "" || extAfterHold > 0)) || (wfTotal > 0 && (headSHA != "" || wfAfterHold > 0)) {
		return true, false, nil
	}
	return false, false, nil
}

func checkConclusionFailed(concl string) bool {
	if model.GitHubCIFailed(concl) {
		return true
	}
	switch concl {
	case "cancelled", "action_required":
		return true
	}
	return false
}

func recordConcludedActionsRunJob(ctx context.Context, store *model.Store, repo model.RepoRef, branch string, r github.WorkflowRun, now time.Time) {
	if store == nil || r.ID == 0 || r.Status != "completed" || strings.TrimSpace(r.Conclusion) == "" {
		return
	}
	if existing, err := store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{Repo: repo.String(), RunID: r.ID, Limit: 1}); err == nil && len(existing) > 0 {
		return
	}
	ref := strings.TrimSpace(r.HeadBranch)
	if ref == "" {
		ref = branch
	}
	attempt := r.RunAttempt
	if attempt <= 0 {
		attempt = 1
	}
	_ = store.PutGitHubCIJob(ctx, model.GitHubCIJob{
		Repo:        repo.String(),
		CheckRunID:  r.ID,
		Source:      model.GitHubCISource,
		App:         model.GitHubActionsApp,
		Ref:         ref,
		Commit:      r.HeadSHA,
		Workflow:    r.Name,
		RunID:       r.ID,
		RunAttempt:  attempt,
		JobName:     firstNonEmpty(r.Name, r.Path, "workflow"),
		Conclusion:  strings.ToLower(strings.TrimSpace(r.Conclusion)),
		StartedAt:   r.StartedAt,
		CompletedAt: r.UpdatedAt,
		RunURL:      r.HTMLURL,
		JobURL:      r.HTMLURL,
		Via:         "poll",
		UpdatedAt:   now,
	})
}

// sendBackFromResumeLater undefers task when it is resting on its own
// resume_later and grain is sending it back to an agent (why: the merge
// queue's repair, new review threads): model.Store.ReleaseResumeLater.
// Without it the send-back's RetryRequestedAt sat under the deferral, so
// nothing ran, and the queue gave up on a repair it had never started.
func sendBackFromResumeLater(ctx context.Context, store *model.Store, taskID, why string, now time.Time) error {
	released, err := store.ReleaseResumeLater(ctx, taskID, now)
	if err != nil {
		return fmt.Errorf("orchestrator: taking %s off its resume_later for %s: %w", taskID, why, err)
	}
	if released {
		_ = clearPipelineNotification(ctx, store, model.NotificationSubject(model.FamilyCIWakeStalled, taskID), wakeNotifier)
		log.Printf("orchestrator: task %s was resting on resume_later; %s sends it back to an agent now", taskID, why)
	}
	return nil
}
