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

// The mechanical half of a red release candidate
// (design/playbook/releases.playbook.md §3, §4, §5 and §12; implementation
// gap 8). Stage 2's reconciler (releasequalification.go) calls in here as
// it reaches each verdict:
//
//   - pipeline.rc-failed:<rc_tag> when a candidate is confirmed red, with
//     the failing items and the commit range `<floor>..<rc_sha>` to
//     triage. It wakes the pipeline steward, which files the fixes or
//     reverts as tasks titled model.RCBlockerMarker(rc_tag).
//   - pipeline.release-escalated:<version> when the red candidate was the
//     version's ReleaseMaxRCAttempts-th: the train cuts no more of it.
//   - pipeline.release-infra-stalled:<rc_tag> when an item used its
//     infrastructure re-runs, or has been blocked on infrastructure for
//     ReleaseInfraStalledAfter. The candidate stays qualifying.
//   - pipeline.flake-budget:<repo>:<item> when one item spent
//     FlakeBudgetRetries flake retries inside FlakeBudgetWindow.
//
// and, as soon as qualification fails (before grain/release-qualification
// is posted completed) and every cycle thereafter, syncRedRCHolds keeps the
// hold: check run grain/rc-hold on a red candidate's commit starts in
// progress ("triage pending") for up to ReleaseHoldTriageGrace, stays in
// progress while any blocker linked to it is open, and completes when the
// pipeline steward triages with no blocker, the triage window elapses, or
// every blocker has landed -- the train cuts rc.(N+1) after that commit, so
// that is where it reads the hold. Each notification is cleared by the same
// code once its condition is gone: a newer candidate cut, or the version
// promoted.
//
// What is judgement -- which commit broke it, fix or revert, whether
// rc.2 broke on trunk churn -- is the steward's, never this file's.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// releaseQualificationNotifier raises and clears every notification here.
var releaseQualificationNotifier = model.PrincipalString(model.Principal{Kind: model.PrincipalAutomation, ID: "release-qualification"})

// What the reconciler remembers between cycles that the store does not:
// when each item went blocked on infrastructure, and when each item's
// flake retries were started. Both are re-learnt after a restart, which
// at worst restarts a two-hour or a day's clock.
var (
	redRCMu         sync.Mutex
	redRCInfraSince = map[string]time.Time{}   // store|repo@rc|item -> since
	redRCFlakes     = map[string][]time.Time{} // store|repo|item -> retries started
)

func redRCKey(store *model.Store, parts ...string) string {
	return fmt.Sprintf("%d|%s", store.ID(), strings.ToLower(strings.Join(parts, "|")))
}

func tagCommits(tags []github.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		out[t.Name] = t.SHA
	}
	return out
}

// superseded reports whether a candidate newer than rc has been cut, or
// rc's version promoted: either way rc is no longer the one the train is
// working on.
func superseded(tags map[string]string, rc model.RCTag) bool {
	if _, ok := tags[rc.Version()]; ok {
		return true
	}
	for name := range tags {
		if c, ok := model.ParseRCTag(name); ok && rc.Less(c) {
			return true
		}
	}
	return false
}

// raiseRCFailed raises pipeline.rc-failed for q's candidate, posts the
// initial triage-pending grain/rc-hold check run on q's commit before the
// completed qualification verdict goes up, and raises
// pipeline.release-escalated for its version when it was the last attempt.
// stage says which stage failed it, Stage 2 (stage2Failure) or Stage 1
// (stage1Failure); lines are the failing summary.
func (q *rcQualification) raiseRCFailed(ctx context.Context, tags map[string]string, rc model.RCTag, stage rcFailure, lines []string) error {
	subject := model.NotificationSubject(model.FamilyRCFailed, q.rcTag)
	escalate := rc.RC >= model.ReleaseMaxRCAttempts
	grace := releaseHoldGrace()
	if err := ensurePipelineNotification(ctx, q.store, model.Notification{
		Subject:  subject,
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("Release candidate %s of %s failed qualification", q.rcTag, q.repo),
		Body:     rcFailedBody(q.repo, stage, tags, rc, q.sha, lines, escalate, grace),
		Repo:     q.repo.String(),
		RaisedBy: releaseQualificationNotifier,
		Actions:  []string{"investigate"},
	}, q.now); err != nil {
		return err
	}
	if !escalate {
		n, err := q.store.OpenNotification(ctx, subject)
		if err != nil {
			return err
		}
		return syncRCHold(ctx, q.store, q.forge, q.repo, tags, q.rcTag, n, q.now)
	}
	body, err := releaseEscalatedBody(ctx, q.store, q.repo, rc)
	if err != nil {
		return err
	}
	return ensurePipelineNotification(ctx, q.store, model.Notification{
		Subject:  model.NotificationSubject(model.FamilyReleaseEscalated, rc.Version()),
		Severity: model.NotificationCritical,
		Title:    fmt.Sprintf("%s of %s failed %d release candidates; the train cuts no more of it", rc.Version(), q.repo, rc.RC),
		Body:     body,
		Repo:     q.repo.String(),
		RaisedBy: releaseQualificationNotifier,
		Actions:  []string{"investigate"},
	}, q.now)
}

// rcFailure is which stage of qualification failed a candidate: what
// pipeline.rc-failed says it failed, and the heading of its failing lines.
type rcFailure struct {
	what    string
	heading string
}

// stage2Failure is a red grain/release-qualification verdict (check).
func stage2Failure(check string) rcFailure {
	return rcFailure{what: fmt.Sprintf("Stage 2 qualification (%s)", check), heading: "Qualification"}
}

// stage1Failure is a red Stage 1 run (workflow or status, by name).
func stage1Failure(name string) rcFailure {
	return rcFailure{what: fmt.Sprintf("Stage 1 qualification (%s)", name), heading: fmt.Sprintf("Stage 1 (%s)", name)}
}

func rcFailedBody(repo model.RepoRef, stage rcFailure, tags map[string]string, rc model.RCTag, sha string, lines []string, escalate bool, grace time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "`%s` (%s) of %s failed %s: attempt %d of RELEASE_MAX_RC_ATTEMPTS (%d).\n\n",
		rc, shortSHA(sha), repo, stage.what, rc.RC, model.ReleaseMaxRCAttempts)
	if name, floor, ok := model.ReleaseFloor(tags, rc); ok {
		fmt.Fprintf(&b, "Commits to triage: `%s..%s` (everything since %s).\n", floor, sha, name)
	} else {
		fmt.Fprintf(&b, "Commits to triage: everything up to `%s` (no release before %s).\n", sha, rc.Version())
	}
	if name, prev, ok := model.PreviousCandidate(tags, rc); ok {
		fmt.Fprintf(&b, "New since %s: `%s..%s`.\n", name, prev, sha)
	}
	b.WriteString("\n" + stage.heading + ":\n")
	for _, l := range lines {
		b.WriteString("- " + l + "\n")
	}
	if escalate {
		fmt.Fprintf(&b, "\nThis was the version's last attempt: pipeline.release-escalated:%s is raised, and the "+
			"train cuts no more of %s until a person resets or advances it.", rc.Version(), rc.Version())
		return b.String()
	}
	next := rc
	next.RC++
	marker := model.RCBlockerMarker(rc.String())
	if grace > 0 {
		fmt.Fprintf(&b, "\nThe train tags %s-discarded and cuts %s from the next known-good commit after it. Check run %s "+
			"on %s is in progress and holds %s while triage is pending (up to %s, or until the pipeline steward triages %s "+
			"with no blocker) and while any task titled `%s ...` is open: title each fix or revert that must land first `%s ...`.",
			rc, next, model.ReleaseHoldCheckName, shortSHA(sha), next, humanDuration(grace), rc, marker, marker)
	} else {
		fmt.Fprintf(&b, "\nThe train tags %s-discarded and cuts %s from the next known-good commit after it. Title each "+
			"fix or revert that must land first `%s ...`: while any such task is open, check run %s on %s holds %s.",
			rc, next, marker, model.ReleaseHoldCheckName, shortSHA(sha), next)
	}
	return b.String()
}

// releaseEscalatedBody names every red candidate of rc's version, with
// what failed in each, read back from their pipeline.rc-failed
// notifications.
func releaseEscalatedBody(ctx context.Context, store *model.Store, repo model.RepoRef, rc model.RCTag) (string, error) {
	failed, err := failedCandidates(ctx, store, repo, rc.Version())
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s of %s has used RELEASE_MAX_RC_ATTEMPTS (%d) release candidates, and %s failed qualification "+
		"too. The train cuts no more of %s until a person resets it (cuts another candidate of it by hand) or "+
		"advances it to the next version.\n", rc.Version(), repo, model.ReleaseMaxRCAttempts, rc, rc.Version())
	for _, n := range failed {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", strings.TrimPrefix(n.Subject, model.FamilyRCFailed+":"), n.Body)
	}
	return b.String(), nil
}

// failedCandidates is every pipeline.rc-failed raised for repo's version,
// cleared or not, oldest candidate first.
func failedCandidates(ctx context.Context, store *model.Store, repo model.RepoRef, version string) ([]model.Notification, error) {
	all, err := store.ListNotifications(ctx, model.NotificationFilter{Family: model.FamilyRCFailed, Limit: 1000})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []model.Notification
	for _, n := range all {
		rc, ok := model.ParseRCTag(strings.TrimPrefix(n.Subject, model.FamilyRCFailed+":"))
		if !ok || rc.Version() != version || !strings.EqualFold(n.Repo, repo.String()) || seen[n.Subject] {
			continue
		}
		seen[n.Subject] = true
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := model.ParseRCTag(strings.TrimPrefix(out[i].Subject, model.FamilyRCFailed+":"))
		b, _ := model.ParseRCTag(strings.TrimPrefix(out[j].Subject, model.FamilyRCFailed+":"))
		return a.Less(b)
	})
	return out, nil
}

// raiseInfraStalled raises pipeline.release-infra-stalled for q's
// candidate. When concluded is true, Stage 2 has used all of an item's
// infrastructure re-runs and is posting a completed neutral check run: any
// earlier acknowledgement from a mid-run 2-hour warning is cleared so the
// candidate waits for a person to repair the infrastructure and acknowledge
// or clear the stall before starting a fresh Stage 2 attempt.
func (q *rcQualification) raiseInfraStalled(ctx context.Context, why string, concluded bool) error {
	subject := model.NotificationSubject(model.FamilyReleaseInfraStalled, q.notifyKey)
	body := fmt.Sprintf("`%s` (%s) is still qualifying, and is not discarded: %s.\n\nAn infrastructure verdict "+
		"never counts against a candidate, so nothing here is the release's fault. Repair the runner, quota or "+
		"credential the item keeps failing on; this clears once the candidate qualifies, fails, or is "+
		"superseded.", q.rcTag, shortSHA(q.sha), why)
	if concluded {
		q.clearInfraSince()
		body = fmt.Sprintf("`%s` (%s) is left in qualification (not discarded) with a `neutral` Stage 2 check run: %s.\n\n"+
			"An infrastructure verdict never counts against a candidate, so nothing here is the release's fault. "+
			"Repair the runner, quota or credential the item kept failing on, then acknowledge or clear this "+
			"notification to start a fresh Stage 2 qualification attempt on `%s` (or this clears if `%s` is superseded).",
			q.rcTag, shortSHA(q.sha), why, q.rcTag, q.rcTag)
	}
	n := model.Notification{
		Subject:  subject,
		Severity: model.NotificationWarning,
		Title:    fmt.Sprintf("Qualification of %s on %s is stalled on infrastructure", q.rcTag, q.repo),
		Body:     body,
		Repo:     q.repo.String(),
		RaisedBy: releaseQualificationNotifier,
		Actions:  []string{"investigate"},
	}
	if !concluded {
		return ensurePipelineNotification(ctx, q.store, n, q.now)
	}
	open, err := q.store.OpenNotification(ctx, subject)
	if err != nil {
		return err
	}
	if open != nil && open.AckedAt != nil {
		if err := clearPipelineNotification(ctx, q.store, subject, releaseQualificationNotifier); err != nil {
			return err
		}
		open = nil
	}
	if open != nil && open.Severity.Rank() >= n.Severity.Rank() && open.Body == n.Body {
		return nil
	}
	_, err = raisePipelineNotification(ctx, q.store, n, q.now)
	return err
}

func (q *rcQualification) clearInfraSince() {
	prefix := redRCKey(q.store, q.tag, "")
	redRCMu.Lock()
	for k := range redRCInfraSince {
		if strings.HasPrefix(k, prefix) {
			delete(redRCInfraSince, k)
		}
	}
	redRCMu.Unlock()
}

// trackInfra raises pipeline.release-infra-stalled once an item has been
// blocked on infrastructure for ReleaseInfraStalledAfter, and clears it
// when nothing is. An item is blocked while its newest judged attempt is
// an infrastructure verdict, its re-run still pending included.
func (q *rcQualification) trackInfra(ctx context.Context, items []string, attempts map[string][]model.ItemVerdict) error {
	redRCMu.Lock()
	var stalled []string
	for _, item := range items {
		key := redRCKey(q.store, q.tag, item)
		if !infraBlocked(attempts[item]) {
			delete(redRCInfraSince, key)
			continue
		}
		since, ok := redRCInfraSince[key]
		if !ok {
			since = q.now
			redRCInfraSince[key] = since
		}
		if q.now.Sub(since) >= model.ReleaseInfraStalledAfter {
			stalled = append(stalled, fmt.Sprintf("%s has been blocked on infrastructure for %s",
				item, humanDuration(q.now.Sub(since).Round(time.Minute))))
		}
	}
	redRCMu.Unlock()
	if len(stalled) == 0 {
		return clearPipelineNotification(ctx, q.store,
			model.NotificationSubject(model.FamilyReleaseInfraStalled, q.notifyKey), releaseQualificationNotifier)
	}
	return q.raiseInfraStalled(ctx, strings.Join(stalled, "; "), false)
}

func infraBlocked(history []model.ItemVerdict) bool {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Result == model.ItemPending {
			continue
		}
		return history[i].Result == model.ItemInfra
	}
	return false
}

// noteFlakeRetries records the flake retries rerun starts -- the items
// whose newest attempt was a product failure -- and raises
// pipeline.flake-budget for each item that has now spent its budget. The
// notification clears itself a window after its last raise.
func (q *rcQualification) noteFlakeRetries(ctx context.Context, rerun []string, attempts map[string][]model.ItemVerdict) error {
	var errs []error
	for _, item := range rerun {
		h := attempts[item]
		if len(h) == 0 || h[len(h)-1].Result != model.ItemFail {
			continue
		}
		errs = append(errs, q.spendFlakeRetry(ctx, item, fmt.Sprintf("Release qualification item %s of %s is flaking", item, q.repo),
			"release qualification", h[len(h)-1].Reason))
	}
	return errors.Join(errs...)
}

// spendFlakeRetry records one flake retry of target -- a Stage 2 item, or
// a Stage 1 job -- on q's candidate, and raises
// pipeline.flake-budget:<repo>:<target> once target has spent
// FlakeBudgetRetries of them inside FlakeBudgetWindow. where says what
// retried it, reason what the failure was.
func (q *rcQualification) spendFlakeRetry(ctx context.Context, target, title, where, reason string) error {
	redRCMu.Lock()
	key := redRCKey(q.store, q.repo.String(), target)
	var kept []time.Time
	for _, at := range redRCFlakes[key] {
		if q.now.Sub(at) < model.FlakeBudgetWindow {
			kept = append(kept, at)
		}
	}
	kept = append(kept, q.now)
	redRCFlakes[key] = kept
	redRCMu.Unlock()
	if len(kept) < model.FlakeBudgetRetries {
		return nil
	}
	return ensurePipelineNotification(ctx, q.store, model.Notification{
		Subject:  model.NotificationSubject(model.FamilyFlakeBudget, q.repo.String()+":"+target),
		Severity: model.NotificationWarning,
		Title:    title,
		Body: fmt.Sprintf("%s failed and was retried %d times in the last %s of %s, most "+
			"recently on %s (%s). A flake retry is spent per candidate, so a flaky one makes red candidates "+
			"out of healthy ones: de-flake it, or quarantine what it tests.", target, len(kept),
			humanDuration(model.FlakeBudgetWindow), where, q.rcTag, reason),
		Repo:       q.repo.String(),
		RaisedBy:   releaseQualificationNotifier,
		Actions:    []string{"investigate"},
		ClearsWhen: &model.NotificationClear{Kind: model.ClearsOnTTL, TTL: model.FlakeBudgetWindow.String()},
	}, q.now)
}

// releaseHoldGrace is how long grain/rc-hold waits for triage before any
// [rc-blocker <rc>] task exists: model.ReleaseHoldTriageGrace (1h) unless
// GRAIN_RC_HOLD_GRACE sets a non-negative duration.
func releaseHoldGrace() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GRAIN_RC_HOLD_GRACE")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
			return d
		}
	}
	return model.ReleaseHoldTriageGrace
}

// syncRedRCHolds keeps every open red-candidate notification of repo
// current: the hold check run on each red candidate's commit, and
// clearing what a newer candidate or a promotion has made moot. current
// is the candidate now qualifying, "" for none.
func syncRedRCHolds(ctx context.Context, store *model.Store, forge ReleaseQualificationForge, repo model.RepoRef,
	tags map[string]string, current string, now time.Time) error {

	open, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true, Limit: 1000})
	if err != nil {
		return err
	}
	var errs []error
	seenRC := map[string]bool{}
	for _, n := range open {
		if !strings.EqualFold(n.Repo, repo.String()) {
			continue
		}
		family, key, _ := strings.Cut(n.Subject, ":")
		switch family {
		case model.FamilyRCFailed:
			seenRC[key] = true
			nCopy := n
			errs = append(errs, syncRCHold(ctx, store, forge, repo, tags, key, &nCopy, now))
		case model.FamilyReleaseInfraStalled:
			// Only an rc tag's: a synced-release gate's are keyed
			// <stem>@<release>, and cleared by that gate itself
			// (clearStaleInfraStalled).
			if _, isRC := model.ParseRCTag(key); isRC && key != current {
				errs = append(errs, clearPipelineNotification(ctx, store, n.Subject, releaseQualificationNotifier))
			}
		case model.FamilyReleaseEscalated:
			lifted, err := escalationLifted(ctx, store, repo, tags, key)
			if err != nil {
				errs = append(errs, err)
			} else if lifted {
				errs = append(errs, clearPipelineNotification(ctx, store, n.Subject, releaseQualificationNotifier))
			}
		}
	}
	// An operator acknowledging pipeline.rc-failed:<rc> in the Inbox closes
	// the notification before a newer candidate is cut: visit any
	// non-superseded candidate whose rc-failed notification was already
	// cleared so its triage-pending hold is completed too (or kept while
	// blockers remain open).
	var activeRCs []string
	for name := range tags {
		if rc, ok := model.ParseRCTag(name); ok && !seenRC[name] && !superseded(tags, rc) {
			activeRCs = append(activeRCs, name)
		}
	}
	if len(activeRCs) > 0 {
		closed, err := store.ListNotifications(ctx, model.NotificationFilter{Family: model.FamilyRCFailed, Limit: 100})
		if err != nil {
			errs = append(errs, err)
		} else {
			byKey := map[string]model.Notification{}
			for _, n := range closed {
				if !strings.EqualFold(n.Repo, repo.String()) {
					continue
				}
				_, key, _ := strings.Cut(n.Subject, ":")
				if prev, ok := byKey[key]; !ok || n.ID > prev.ID {
					byKey[key] = n
				}
			}
			sort.Strings(activeRCs)
			for _, rcTag := range activeRCs {
				if n, ok := byKey[rcTag]; ok {
					nCopy := n
					errs = append(errs, syncRCHold(ctx, store, forge, repo, tags, rcTag, &nCopy, now))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// syncRCHold posts grain/rc-hold on rcTag's commit while triage is pending or
// blockers linked to it are open, completes it once triage needs no blocker,
// the triage window elapses, or every blocker has landed, and clears
// pipeline.rc-failed once a newer candidate is cut or the version is promoted.
func syncRCHold(ctx context.Context, store *model.Store, forge ReleaseQualificationForge, repo model.RepoRef,
	tags map[string]string, rcTag string, n *model.Notification, now time.Time) error {

	rc, ok := model.ParseRCTag(rcTag)
	sha := tags[rcTag]
	if !ok || sha == "" {
		return nil
	}
	marker := model.RCBlockerMarker(rcTag)
	blockers, err := store.OpenTasksTitled(ctx, marker)
	if err != nil {
		return err
	}
	hadBlockers, err := store.HasTasksTitled(ctx, marker)
	if err != nil {
		return err
	}
	done := superseded(tags, rc)
	clear := func() error {
		if !done || (n != nil && n.ClearedAt != nil) {
			return nil
		}
		return clearPipelineNotification(ctx, store, model.NotificationSubject(model.FamilyRCFailed, rcTag), releaseQualificationNotifier)
	}
	// An escalated version's last attempt cuts no next candidate (Phase 2
	// stops at RELEASE_MAX_RC_ATTEMPTS), so no hold is posted for it.
	if rc.RC >= model.ReleaseMaxRCAttempts {
		return clear()
	}
	// Only a hold grain's own App posted is grain's, as with the verdict
	// (latestCheckRun); a connection grain cannot name posts none.
	app, err := forge.CheckRunApp(repo.Owner, repo.Name)
	if err != nil || app == "" {
		return errors.Join(err, clear())
	}
	checks, err := forge.ListCheckRuns(repo.Owner, repo.Name, sha)
	if err != nil {
		return fmt.Errorf("reading checks on %s: %w", rcTag, err)
	}
	extID := "rc-hold:" + rcTag
	hold := latestCheckRun(checks, model.ReleaseHoldCheckName, app, extID)
	held := hold != nil && hold.Status != "completed"

	grace := releaseHoldGrace()
	nOpen := n != nil && n.ClearedAt == nil
	withinGrace := nOpen && grace > 0 && now.Sub(n.RaisedAt) < grace
	stewardDone := false
	if !done && len(blockers) == 0 && !hadBlockers && n != nil {
		stewardDone, err = stewardTriagedCandidate(ctx, store, rcTag, n.RaisedAt)
		if err != nil {
			return err
		}
	}
	triagePending := !done && len(blockers) == 0 && !hadBlockers && !stewardDone && withinGrace

	next := rc
	next.RC++
	subject := model.NotificationSubject(model.FamilyRCFailed, rcTag)
	run := github.CheckRunWrite{Name: model.ReleaseHoldCheckName, HeadSHA: sha, ExternalID: extID}
	stamp := now.UTC()
	updateExisting := false
	switch {
	case done && held:
		updateExisting = true
		run.Status, run.Conclusion, run.CompletedAt = "completed", model.ConclusionNeutral, &stamp
		run.Output = &github.CheckRunOutput{Title: "Superseded",
			Summary: fmt.Sprintf("A newer candidate than %s is cut, or %s is promoted; nothing is held.", rcTag, rc.Version())}
	case !done && len(blockers) > 0:
		summary := fmt.Sprintf("%s is held until these blockers of %s land: task %s.", next, rcTag,
			strings.Join(blockers, ", task "))
		if !held || !strings.Contains(hold.Output, summary) {
			updateExisting = held
			run.Status = "in_progress"
			if !held {
				run.StartedAt = &stamp
			}
			run.Output = &github.CheckRunOutput{Title: fmt.Sprintf("Holding %s", next), Summary: summary}
		}
	case triagePending && !held:
		if hold == nil {
			run.Status, run.StartedAt = "in_progress", &stamp
			run.Output = &github.CheckRunOutput{
				Title: fmt.Sprintf("Holding %s (triage pending)", next),
				Summary: fmt.Sprintf("%s failed qualification; %s is held while %s awaits triage (up to %s) or tasks titled %s remain open.",
					rcTag, next, subject, humanDuration(grace), marker),
			}
		}
	case !done && len(blockers) == 0 && !triagePending && held:
		updateExisting = true
		switch {
		case hadBlockers:
			run.Status, run.Conclusion, run.CompletedAt = "completed", model.ConclusionSuccess, &stamp
			run.Output = &github.CheckRunOutput{Title: "Blockers landed",
				Summary: fmt.Sprintf("Every task titled %s has landed; %s may be cut.", marker, next)}
		case stewardDone:
			run.Status, run.Conclusion, run.CompletedAt = "completed", model.ConclusionSuccess, &stamp
			run.Output = &github.CheckRunOutput{Title: "Triaged with no blocker",
				Summary: fmt.Sprintf("The pipeline steward triaged %s with no task titled %s; %s may be cut.", rcTag, marker, next)}
		case !nOpen:
			run.Status, run.Conclusion, run.CompletedAt = "completed", model.ConclusionSuccess, &stamp
			run.Output = &github.CheckRunOutput{Title: "Triage acknowledged",
				Summary: fmt.Sprintf("%s was cleared with no open task titled %s; %s may be cut.", subject, marker, next)}
		default:
			run.Status, run.Conclusion, run.CompletedAt = "completed", model.ConclusionNeutral, &stamp
			run.Output = &github.CheckRunOutput{Title: "Triage window elapsed",
				Summary: fmt.Sprintf("No task titled %s was filed within %s of %s failing; %s may be cut.",
					marker, humanDuration(grace), rcTag, next)}
		}
	}
	if run.Status != "" {
		if updateExisting {
			err = forge.UpdateCheckRun(repo.Owner, repo.Name, hold.ID, run)
		} else {
			_, err = forge.CreateCheckRun(repo.Owner, repo.Name, run)
		}
		if err != nil {
			return fmt.Errorf("posting %s on %s: %w", model.ReleaseHoldCheckName, rcTag, err)
		}
	}
	return clear()
}

// stewardTriagedCandidate reports whether the pipeline steward's own run
// posted a closing report naming rcTag at or after since: triage that ended
// with no blocker filed, lifting the triage-pending hold early. An earlier
// wake finishing an unrelated report after since does not name rcTag and
// leaves the hold in place.
func stewardTriagedCandidate(ctx context.Context, store *model.Store, rcTag string, since time.Time) (bool, error) {
	ids, err := store.OpenTasksWithTag(ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID))
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		comments, err := store.Comments(ctx, id)
		if err != nil {
			return false, err
		}
		for _, c := range comments {
			if !c.CreatedAt.Before(since) && ownRunComment(c, id) && mentionsRCTag(c.Body, rcTag) {
				return true, nil
			}
		}
	}
	return false, nil
}

func mentionsRCTag(body, rcTag string) bool {
	for s := body; ; {
		i := strings.Index(s, rcTag)
		if i < 0 {
			return false
		}
		rest := s[i+len(rcTag):]
		if len(rest) == 0 || rest[0] < '0' || rest[0] > '9' {
			return true
		}
		s = rest
	}
}

// escalationLifted reports whether a person has reset or advanced the
// train past version's escalation: the version is promoted, or a
// candidate newer than its last red one has been cut.
func escalationLifted(ctx context.Context, store *model.Store, repo model.RepoRef, tags map[string]string, version string) (bool, error) {
	if _, ok := tags[version]; ok {
		return true, nil
	}
	failed, err := failedCandidates(ctx, store, repo, version)
	if err != nil || len(failed) == 0 {
		return false, err
	}
	last, _ := model.ParseRCTag(strings.TrimPrefix(failed[len(failed)-1].Subject, model.FamilyRCFailed+":"))
	return superseded(tags, last), nil
}
