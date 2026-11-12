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
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// SetExternalCheckStallThreshold replaces forge.DefaultExternalCheckStallThreshold
// and returns a function that restores the previous value.
func SetExternalCheckStallThreshold(d time.Duration) func() {
	return forge.SetExternalCheckStallThreshold(d)
}

const externalStallEscalatedOnly = "escalated-without-nudge"

// externalNudgeMatchesHead reports whether obs records an external-check nudge
// (or unsupported-rerequest escalation) covering headSHA -- either on headSHA
// itself or on the merge commit that nudging the previous head produced.
func externalNudgeMatchesHead(obs *model.Observation, headSHA string) bool {
	if obs == nil || obs.MergeQueueStallRetryAt == nil || headSHA == "" {
		return false
	}
	recorded := obs.MergeQueueStallRetryHead
	if recorded == headSHA {
		return true
	}
	if before, after, ok := strings.Cut(recorded, "->"); ok {
		return before == headSHA || after == headSHA
	}
	return false
}

// externalNudgedOnce reports whether headSHA has already been nudged once (by
// merging the base branch or re-requesting the check).
func externalNudgedOnce(obs *model.Observation, headSHA string) bool {
	return externalNudgeMatchesHead(obs, headSHA) &&
		obs.MergeQueueStallRetryWhy != "" &&
		obs.MergeQueueStallRetryWhy != externalStallEscalatedOnly
}

// externalPendingRowNote formats the merge-queue row note when only external
// checks are holding e pending: e.g. "`cla/google` pending 52m; nudged once".
func externalPendingRowNote(e queueEntry) (string, bool) {
	if len(e.externalPending) == 0 {
		return "", false
	}
	names := externalCheckNamesCode(e.externalPending)
	wait := maxDuration(e.externalPendingFor, e.pendingFor)
	note := names + " pending"
	if wait >= time.Minute {
		note = fmt.Sprintf("%s pending %s", names, roundWait(wait))
	}
	if externalNudgedOnce(e.obs, e.detail.HeadSHA) {
		note += "; nudged once"
	}
	return note, true
}

// reconcileExternalCheckStall handles third-party status checks stuck pending
// on a pull request's head commit:
//  1. Clears any stale nudge record or pipeline.merge-stalled notification when
//     the check reports or a fresh non-nudge commit is pushed.
//  2. Nudges once per head when past the external stall threshold (merges the
//     base branch if behind; otherwise re-requests the check without pushing).
//  3. Escalates via pipeline.merge-stalled:<taskID> when re-request is not
//     supported on an up-to-date branch or when the check stays pending a
//     second threshold after the nudge.
//
// Returns mergedBase=true when a base-branch merge moved the PR head so the
// caller stops acting on the old head for the rest of this cycle.
func reconcileExternalCheckStall(
	ctx context.Context,
	store *model.Store,
	client github.Client,
	e *queueEntry,
	task model.Task,
	isMergeHead, blocked, repairing, working, stacked, rechecking bool,
	review reviewState,
	lane laneState,
	now time.Time,
) (mergedBase bool, err error) {
	ref := e.ref
	detail := e.detail
	health := e.health

	// Clear stale external-stall state if the PR is no longer pending or its
	// head moved to a fresh commit unrelated to the recorded nudge.
	if e.obs != nil && e.obs.MergeQueueStallRetryAt != nil {
		freshHead := detail.HeadSHA != "" && !externalNudgeMatchesHead(e.obs, detail.HeadSHA)
		if health != model.PrPending || freshHead {
			if err := clearExternalCheckStall(ctx, store, task.ID, e.obs, !blocked, now); err != nil {
				return false, err
			}
		}
	}

	if health != model.PrPending || !e.externalStalled || len(e.externalPending) == 0 {
		return false, nil
	}

	// Never nudge a PR whose run is in flight, that a person is working on,
	// or that is awaiting a Submit / another gate (unless only this check holds it).
	if e.obs == nil || e.obs.CompletedAt == nil || !task.AutoMerge ||
		stacked || blocked || working || repairing || e.turnStopped || e.paused ||
		rechecking || review.pending || awaitingGitHubApproval(ref) {
		return false, nil
	}

	wait := maxDuration(e.externalPendingFor, e.pendingFor)
	if externalNudgeMatchesHead(e.obs, detail.HeadSHA) {
		if e.obs.MergeQueueStallRetryWhy == externalStallEscalatedOnly {
			return false, raiseExternalCheckStallNotification(ctx, store, task, ref, detail, e.externalPending, wait, false, now)
		}
		if e.externalStallThreshold > 0 && !now.Before(e.obs.MergeQueueStallRetryAt.Add(e.externalStallThreshold)) {
			totalWait := wait
			if sinceNudge := now.Sub(*e.obs.MergeQueueStallRetryAt) + e.externalStallThreshold; sinceNudge > totalWait {
				totalWait = sinceNudge
			}
			return false, raiseExternalCheckStallNotification(ctx, store, task, ref, detail, e.externalPending, totalWait, true, now)
		}
		return false, nil
	}

	// Step 2: Nudge once per head.
	// First, if the branch is in this repository and behind its base, merge
	// the base into the PR branch. Never call MergeBranch for a fork PR,
	// whose HeadRef could collide with an unrelated branch in ref.Repo.
	if sameRepoHeadBranch(ref.Repo, detail) {
		result, mergeErr := client.MergeBranch(
			ref.Repo.StoredOwner(), ref.Repo.Name,
			detail.HeadRef, detail.BaseRef, refreshCommitMessage(detail),
		)
		switch {
		case mergeErr == nil && result.Merged:
			headRecord := detail.HeadSHA
			if result.SHA != "" {
				headRecord = detail.HeadSHA + "->" + result.SHA
			}
			why := fmt.Sprintf("merged %s into %s", detail.BaseRef, detail.HeadRef)
			if err := recordExternalNudge(ctx, store, task.ID, e.obs, headRecord, why, now); err != nil {
				return false, err
			}
			msg := fmt.Sprintf("%s was pending for %s on `%s`; merged `%s` to re-trigger it.",
				externalCheckNamesCode(e.externalPending), formatStallAge(wait), shortCommitSHA(detail.HeadSHA), detail.BaseRef)
			if err := postExternalNudgeNote(ctx, store, client, task.ID, ref, msg, now); err != nil {
				return true, err
			}
			status := statusOf(*e, isMergeHead, blocked, review, lane)
			status.reason = mergeReasonOf(*e, status.state, e.runner)
			if err := setMergeQueueState(ctx, store, e.obs, task.ID, status, now); err != nil {
				return true, err
			}
			return true, nil
		case mergeErr == nil:
			// 204: branch is already up to date with base; push nothing and
			// try re-requesting the check in place below.
		case github.IsMergeConflict(mergeErr):
			// Next cycle reads PrConflicted and sends it through normal repair.
			return false, nil
		case notFound(mergeErr):
			// Fork or unreachable head branch; try re-requesting in place below.
		default:
			return false, fmt.Errorf("orchestrator: merging %q into %q to re-trigger stalled check on %s: %w",
				detail.BaseRef, detail.HeadRef, ref, mergeErr)
		}
	}

	// Branch is already up to date (or head ref cannot be merged into): push
	// nothing, and re-request the check if the forge and app allow it.
	rerequested, err := rerequestExternalChecks(client, ref.Repo.StoredOwner(), ref.Repo.Name, e.externalPending)
	if err != nil {
		return false, fmt.Errorf("orchestrator: re-requesting stalled check on %s: %w", ref, err)
	}
	if rerequested {
		if err := recordExternalNudge(ctx, store, task.ID, e.obs, detail.HeadSHA, "re-requested check", now); err != nil {
			return false, err
		}
		msg := fmt.Sprintf("%s was pending for %s on `%s`; re-requested it.",
			externalCheckNamesCode(e.externalPending), formatStallAge(wait), shortCommitSHA(detail.HeadSHA))
		if err := postExternalNudgeNote(ctx, store, client, task.ID, ref, msg, now); err != nil {
			return false, err
		}
		status := statusOf(*e, isMergeHead, blocked, review, lane)
		status.reason = mergeReasonOf(*e, status.state, e.runner)
		if err := setMergeQueueState(ctx, store, e.obs, task.ID, status, now); err != nil {
			return false, err
		}
		return false, nil
	}

	// Step 3 fallback: branch is already up to date and the forge/app does not
	// support re-requesting this check. Do not push empty commits; escalate now.
	if err := recordExternalNudge(ctx, store, task.ID, e.obs, detail.HeadSHA, externalStallEscalatedOnly, now); err != nil {
		return false, err
	}
	return false, raiseExternalCheckStallNotification(ctx, store, task, ref, detail, e.externalPending, wait, false, now)
}

func rerequestExternalChecks(client github.Client, owner, repo string, pending []github.CheckRun) (bool, error) {
	triedSuites := map[int64]bool{}
	rerequested := false
	for _, c := range pending {
		if c.CheckSuiteID > 0 && !triedSuites[c.CheckSuiteID] {
			triedSuites[c.CheckSuiteID] = true
			err := client.RerequestCheckSuite(owner, repo, c.CheckSuiteID)
			if err == nil {
				rerequested = true
				continue
			}
			if !isRerequestUnsupported(err) {
				return rerequested, err
			}
		}
		if c.ID > 0 {
			err := client.RerequestCheckRun(owner, repo, c.ID)
			if err == nil {
				rerequested = true
				continue
			}
			if !isRerequestUnsupported(err) {
				return rerequested, err
			}
		}
	}
	return rerequested, nil
}

// sameRepoHeadBranch reports whether detail names a head branch in repo itself
// distinct from its base branch -- so MergeBranch(repo, HeadRef, BaseRef)
// writes only to the pull request's own head branch and never to a colliding
// branch in repo when the pull request was opened from a fork.
func sameRepoHeadBranch(repo model.RepoRef, detail github.PullRequestDetail) bool {
	if detail.HeadRef == "" || detail.BaseRef == "" || detail.HeadRef == detail.BaseRef {
		return false
	}
	if detail.HeadRepo != "" && !strings.EqualFold(detail.HeadRepo, repo.String()) {
		return false
	}
	return true
}

func isRerequestUnsupported(err error) bool {
	var ghErr *github.Error
	if !errors.As(err, &ghErr) {
		return false
	}
	switch ghErr.Status {
	case 403, 404, 405, 422, 501:
		return true
	default:
		return false
	}
}

func recordExternalNudge(ctx context.Context, store *model.Store, taskID string, obs *model.Observation, head, why string, now time.Time) error {
	at := now
	if obs != nil {
		obs.MergeQueueStallRetryAt = &at
		obs.MergeQueueStallRetryHead = head
		obs.MergeQueueStallRetryWhy = why
	}
	return observeField(ctx, store, taskID, now, func(o *model.Observation) {
		t := now
		o.MergeQueueStallRetryAt = &t
		o.MergeQueueStallRetryHead = head
		o.MergeQueueStallRetryWhy = why
	})
}

func clearExternalCheckStall(ctx context.Context, store *model.Store, taskID string, obs *model.Observation, clearNotif bool, now time.Time) error {
	if obs != nil {
		obs.MergeQueueStallRetryAt = nil
		obs.MergeQueueStallRetryHead = ""
		obs.MergeQueueStallRetryWhy = ""
	}
	if err := observeField(ctx, store, taskID, now, func(o *model.Observation) {
		o.MergeQueueStallRetryAt = nil
		o.MergeQueueStallRetryHead = ""
		o.MergeQueueStallRetryWhy = ""
	}); err != nil {
		return err
	}
	if clearNotif {
		subject := model.NotificationSubject(model.FamilyMergeStalled, taskID)
		if err := clearPipelineNotification(ctx, store, subject, mergeQueueNotifier); err != nil {
			return err
		}
	}
	return nil
}

func postExternalNudgeNote(ctx context.Context, store *model.Store, client github.Client, taskID string, ref model.PullRequestRef, msg string, now time.Time) error {
	if _, err := client.CreateComment(ref.Repo.StoredOwner(), ref.Repo.Name, ref.Number, msg); err != nil {
		return fmt.Errorf("orchestrator: commenting on %s after check nudge: %w", ref, err)
	}
	return queueComment(ctx, store, taskID, msg, now)
}

func raiseExternalCheckStallNotification(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	ref model.PullRequestRef,
	detail github.PullRequestDetail,
	pending []github.CheckRun,
	wait time.Duration,
	nudged bool,
	now time.Time,
) error {
	short := shortCommitSHA(detail.HeadSHA)
	age := formatStallAge(wait)
	codeNames := externalCheckNamesCode(pending)
	plainNames := externalCheckNamesPlain(pending)

	body := fmt.Sprintf("%s on [Task #%s](/tasks/%s) has had %s pending for %s on `%s`",
		ref, task.ID, task.ID, codeNames, age, short)
	if nudged {
		body += " and it remained pending past the threshold after one automatic nudge."
	} else {
		body += "; the branch is already up to date with its base and the forge could not re-request the check."
	}

	n := model.Notification{
		Subject:  model.NotificationSubject(model.FamilyMergeStalled, task.ID),
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("%s stalled on %s (%s on %s)", ref, plainNames, age, short),
		Body:     body,
		Repo:     ref.Repo.String(),
		TaskID:   task.ID,
		RaisedBy: mergeQueueNotifier,
		ClearsWhen: &model.NotificationClear{
			Kind: model.ClearsOnTask, Task: task.ID, Until: "closed",
		},
		Actions: []string{"open-task", "investigate"},
	}
	return ensurePipelineNotification(ctx, store, n, now)
}

func externalCheckNamesCode(checks []github.CheckRun) string {
	names := externalCheckNamesList(checks)
	return "`" + strings.Join(names, "`, `") + "`"
}

func externalCheckNamesPlain(checks []github.CheckRun) string {
	return strings.Join(externalCheckNamesList(checks), ", ")
}

func externalCheckNamesList(checks []github.CheckRun) []string {
	seen := map[string]bool{}
	var names []string
	for _, c := range checks {
		name := c.Name
		if name == "" {
			name = "(unnamed check)"
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

func shortCommitSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func formatStallAge(d time.Duration) string {
	if d >= time.Minute {
		return roundWait(d)
	}
	if d <= 0 {
		return "0s"
	}
	return d.Round(time.Second).String()
}

func maxDuration(a, b time.Duration) time.Duration {
	if b > a {
		return b
	}
	return a
}
