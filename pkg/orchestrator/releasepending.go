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

// The deployment noticing a release that waits for a person
// (design/playbook/releases.playbook.md §0.1 step 7, §0.2, gap 4).
//
// On a repository that releases from grain CI, the release train's own
// run raises release.pending:vX.Y.Z through the CI API
// (scripts/release-from-trunk.sh raise_pending). On one that releases
// from GitHub Actions (model.Store.ReleasesFromGitHubActions: ci_mode:
// github, or no CI Mode set and no grain CI in effect) the train runs on a
// hosted runner, which cannot reach this deployment -- on 11-01/02 that is
// why the Inbox never showed an Approve button. So the deployment looks,
// outbound: the vX.Y.Z-pending tag is the state, and this reconciler
// reads the repository's tags and raises the notification for every
// release staged and not yet promoted, with the -pending tag's message
// and the release notes in its body, and clears it once vX.Y.Z is tagged
// or the -pending tag is gone (approved or rejected).
//
// A person's answer records the dispatched decision in the store and clears
// any open notification at once (ui.DecideRelease) while the -pending tag is
// still there -- the promotion run has only just started. So a release
// commit a person answered within releasePendingAnswerHold is not raised
// again even when approved from the CLI or API before the first poll; one
// still pending at the same commit after that (its run failed) is, as the
// daily train run would raise it again on grain CI, and a re-cut candidate
// staged at a new commit is raised immediately.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// releasePendingPollInterval is how often one repository's tags are
	// read when no recent approval or rejection is in flight: one
	// matching-refs call, and a tag object per newly pending release.
	releasePendingPollInterval = 10 * time.Minute
	// releasePendingActivePollInterval is how often a repository's tags and
	// promote-release.yml runs are checked right after a person approves or
	// rejects a pending release, so a failed workflow_dispatch surfaces in
	// the Inbox within seconds rather than after releasePendingPollInterval.
	releasePendingActivePollInterval = 5 * time.Second
	// releaseApprovalWatchWindow is how long after a person's answer
	// releasePendingActivePollInterval stays in effect while the decision has
	// neither promoted the tag nor failed.
	releaseApprovalWatchWindow = 15 * time.Minute
	// releasePendingAnswerHold is how long a person's answer keeps the
	// notification down while the -pending tag outlives an unfailed run.
	releasePendingAnswerHold = model.ReleaseApprovalHold
	// releasePendingNotesLimit bounds the release notes quoted in the body.
	releasePendingNotesLimit = 3000
)

var releasePendingNotifier = model.PrincipalString(model.Principal{Kind: model.PrincipalAutomation, ID: "release-watch"})

var pendingTagPattern = regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+)-pending$`)
var retractedTagPattern = regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+)-retracted$`)
var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var breakingSubjectPattern = regexp.MustCompile(`^[A-Za-z]+(\([^)]*\))?!: `)
var mergePullRequestPattern = regexp.MustCompile(`^Merge pull request #[0-9]+ `)
var breakingTrailerPattern = regexp.MustCompile(`^BREAKING[ -]CHANGE:`)
var releaseMajorConfPattern = regexp.MustCompile(`^RELEASE_MAJOR="?([0-9]+)"?[ \t]*(?:#.*)?$`)
var releaseNeverPromoteQuotedPattern = regexp.MustCompile(`^RELEASE_NEVER_PROMOTE="([^"]*)"[ \t]*(?:#.*)?$`)
var releaseNeverPromoteBarePattern = regexp.MustCompile(`^RELEASE_NEVER_PROMOTE=([^"#\r\n]*?)[ \t]*(?:#.*)?$`)

type releaseCommitComparer interface {
	CompareCommits(owner, repo, base, head string) ([]github.Commit, error)
}

// releasePendingPolled is when each repository's tags were last read,
// keyed by the store (model.Store.ID) and the repository, so that two
// stores in one process -- tests running side by side, or one test's two
// -count iterations -- never hold each other's poll back.
var releasePendingPolled sync.Map // store id | repo string -> time.Time

func releasePendingKey(store *model.Store, repo model.RepoRef) string {
	return fmt.Sprintf("%d|%s", store.ID(), strings.ToLower(repo.String()))
}

// isGitOnlyRepo reports whether this deployment reaches repo over plain
// git only (SSH-only credentials, or a host with no forge API), asking
// the client -- or the wrapper around it, such as the daemon's
// StateRepoPullRequestClient -- through github.GitOnlySource.
func isGitOnlyRepo(client github.Client, repo model.RepoRef) bool {
	if src, ok := client.(github.GitOnlySource); ok {
		return src.GitOnly(repo.StoredOwner(), repo.Name) != ""
	}
	return false
}

// reconcileReleasePending reads, at most every
// releasePendingPollInterval, the tags of every repository this
// deployment releases from GitHub Actions, and raises or clears
// release.pending for each.
func reconcileReleasePending(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Client == nil {
		return nil
	}
	tags, ok := deps.Client.(github.ReleaseTagReader)
	if !ok {
		return nil // a forge connection that cannot read tags raises nothing
	}
	files, _ := deps.Client.(github.FileReader)
	actionsReader, _ := deps.Client.(github.ActionsReader)
	comparer, _ := deps.Client.(releaseCommitComparer)
	configs, err := deps.Store.ListRepoConfigs(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading repo configs: %w", err)
	}
	targets, err := deps.Store.TargetRepos(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading target repos: %w", err)
	}
	byRepo := make(map[string]*model.RepoConfig, len(configs))
	var repos []model.RepoRef
	add := func(r model.RepoRef) {
		// grain/config-<team> repositories (and their retired aliases) live
		// in this deployment, not on a forge: never send their tag reads out.
		if r.Owner == "" || r.Name == "" || model.IsConfigRepo(r) || gitproxy.RetiredConfigRepo(r.Host, r.Owner, r.Name) {
			return
		}
		if slices.ContainsFunc(repos, func(e model.RepoRef) bool { return e.Same(r) }) {
			return
		}
		repos = append(repos, r)
	}
	for i := range configs {
		cfg := &configs[i]
		byRepo[strings.ToLower(cfg.Repo.String())] = cfg
		add(cfg.Repo)
	}
	for _, raw := range targets {
		t := strings.TrimSpace(raw)
		if t == "" || strings.ContainsAny(t, "*?[") {
			continue
		}
		if r, err := model.ParseRepo(t); err == nil {
			add(r)
		}
	}
	var errs []error
	for _, repo := range repos {
		cfg := byRepo[strings.ToLower(repo.String())]
		if !model.RunsWorkItem(cfg, model.ReleaseItem) {
			continue
		}
		if allowed, err := repoAllowedForAnyTeam(ctx, deps.Store, repo); err != nil || !allowed {
			continue
		}
		if isGitOnlyRepo(deps.Client, repo) {
			continue // SSH-only credentials or a plain-git host: no forge API to poll
		}
		actions, err := deps.Store.ReleasesFromGitHubActions(ctx, repo)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: %w", err))
			continue
		}
		if !actions {
			continue
		}
		interval := releasePendingPollInterval
		if hasRecentActiveReleaseDecision(ctx, deps.Store, repo, now) {
			interval = releasePendingActivePollInterval
		}
		key := releasePendingKey(deps.Store, repo)
		if last, ok := releasePendingPolled.Load(key); ok && now.Sub(last.(time.Time)) < interval {
			continue
		}
		releasePendingPolled.Store(key, now)
		if err := watchPendingReleases(ctx, deps.Store, tags, files, comparer, actionsReader, repo, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// hasRecentActiveReleaseDecision reports whether repo has an unfailed
// release-approval dispatch within releaseApprovalWatchWindow of now.
func hasRecentActiveReleaseDecision(ctx context.Context, store *model.Store, repo model.RepoRef, now time.Time) bool {
	decisions, err := store.ListReleaseApprovalDecisions(ctx, repo)
	if err != nil {
		return false
	}
	for _, d := range decisions {
		if !d.Failed() && now.Sub(d.DecidedAt) < releaseApprovalWatchWindow {
			return true
		}
	}
	return false
}

// watchPendingReleases raises and clears release.pending for repo from
// its tags and any failed promote-release.yml run as they stand now.
func watchPendingReleases(ctx context.Context, store *model.Store, tags github.ReleaseTagReader,
	files github.FileReader, comparer releaseCommitComparer, actions github.ActionsReader, repo model.RepoRef, now time.Time) error {
	refs, err := tags.ListMatchingTags(repo.StoredOwner(), repo.Name, "v")
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's release tags: %w", repo, github.Readable(err))
	}
	released := map[string]bool{}
	retracted := map[string]bool{}
	pending := map[string]github.TagRef{}
	for _, t := range refs {
		if releaseTagPattern.MatchString(t.Name) {
			released[t.Name] = true
		} else if m := pendingTagPattern.FindStringSubmatch(t.Name); m != nil {
			pending[m[1]] = t
		} else if m := retractedTagPattern.FindStringSubmatch(t.Name); m != nil {
			retracted[m[1]] = true
		}
	}
	all, err := store.ListNotifications(ctx, model.NotificationFilter{Family: model.FamilyReleasePending, Limit: 1000})
	if err != nil {
		return fmt.Errorf("orchestrator: reading release notifications: %w", err)
	}
	decisions, err := store.ListReleaseApprovalDecisions(ctx, repo)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's release decisions: %w", repo, err)
	}
	var errs []error
	dispatchFailures := map[string]*promoteReleaseFailure{}
	if actions != nil {
		for version, tag := range pending {
			if released[version] {
				continue
			}
			idx := activeDecisionIndexForTag(tags, repo, version, tag, decisions, now)
			if idx < 0 {
				continue
			}
			d := decisions[idx]
			failure := checkPromoteReleaseFailure(actions, repo, version, d)
			if failure == nil {
				continue
			}
			failed := d.MarkFailed()
			if err := store.PutReleaseApprovalDecision(ctx, failed); err != nil {
				errs = append(errs, fmt.Errorf("orchestrator: marking %s %s decision failed: %w", repo, version, err))
				continue
			}
			decisions[idx] = failed
			dispatchFailures[version] = failure
		}
	}
	// Clear what is answered: an open notification about this repository
	// whose release is tagged, whose -pending tag is gone, or whose staged
	// commit already has an active (unfailed) dispatched decision.
	for _, n := range all {
		if n.ClearedAt != nil || n.Repo != repo.String() {
			continue
		}
		version := strings.TrimPrefix(n.Subject, model.FamilyReleasePending+":")
		tag, still := pending[version]
		if still && !released[version] {
			if d := activeDecisionForTag(tags, repo, version, tag, decisions, now); d != nil {
				by := model.PrincipalString(model.Principal{Kind: model.PrincipalHuman, ID: d.DecidedBy})
				if _, err := store.ClearNotification(ctx, n.Subject, by); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		if _, err := store.ClearNotification(ctx, n.Subject, releasePendingNotifier); err != nil {
			errs = append(errs, err)
		}
	}
	// Raise what waits (and attach any failed dispatch details to it).
	for version, tag := range pending {
		if released[version] {
			continue
		}
		subject := model.NotificationSubject(model.FamilyReleasePending, version)
		failure := dispatchFailures[version]
		if open := openBySubject(all, subject); open != nil {
			// The failure block heads the body (FormatBlock is prepended),
			// so only a body that starts with this header has it already;
			// the same words quoted in release notes further down do not.
			if failure == nil || strings.HasPrefix(open.Body, failure.Header()) {
				continue // raised already, by this or by the train's own run
			}
		}
		commit, message, err := peelPendingTag(tags, repo, tag)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading %s's %s tag: %w", repo, tag.Name, github.Readable(err)))
			continue
		}
		if releaseCommitAnswered(decisions, all, repo, version, commit, now) {
			continue
		}
		if failure == nil && actions != nil {
			if fd := failedDecisionForCommit(decisions, version, commit); fd != nil {
				failure = checkPromoteReleaseFailure(actions, repo, version, *fd)
			}
		}
		severity := model.NotificationWarning
		title := version + " is ready to promote"
		body := pendingReleaseBody(files, comparer, repo, released, retracted, version, commit, message)
		if failure != nil {
			severity = model.NotificationCritical
			title = fmt.Sprintf("%s is ready to promote (%s dispatch failed)", version, failure.DecisionVerb)
			body = failure.FormatBlock(repo) + "\n" + body
		}
		n := model.Notification{
			Subject:  subject,
			Severity: severity,
			Title:    title,
			Body:     body,
			Repo:     repo.String(),
			RaisedBy: releasePendingNotifier,
			Actions:  model.ReleasePendingActions,
		}
		// Re-check right before raising in case DecideRelease recorded a
		// decision while we read tag objects or release notes.
		if d, err := store.GetReleaseApprovalDecision(ctx, repo, version, commit); err != nil {
			errs = append(errs, err)
			continue
		} else if d != nil && d.ActiveAt(now) {
			continue
		}
		if _, err := raisePipelineNotification(ctx, store, n, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func openBySubject(all []model.Notification, subject string) *model.Notification {
	for i := range all {
		if all[i].Subject == subject && all[i].ClearedAt == nil {
			return &all[i]
		}
	}
	return nil
}

// peelPendingTag resolves the commit tag points at and, for an annotated
// tag, its message.
func peelPendingTag(tags github.ReleaseTagReader, repo model.RepoRef, tag github.TagRef) (commit, message string, err error) {
	if tag.ObjectType != "tag" {
		return tag.ObjectSHA, "", nil
	}
	at, err := tags.GetAnnotatedTag(repo.StoredOwner(), repo.Name, tag.ObjectSHA)
	if err != nil {
		return "", "", err
	}
	if at == nil {
		return tag.ObjectSHA, "", nil
	}
	return at.Commit, at.Message, nil
}

// activeDecisionIndexForTag returns the index of an active stored decision
// matching tag's commit, peeling tag only when version has an active decision
// on repo, or -1 if none.
func activeDecisionIndexForTag(tags github.ReleaseTagReader, repo model.RepoRef, version string, tag github.TagRef,
	decisions []model.ReleaseApprovalDecision, now time.Time) int {
	hasActiveForVersion := false
	for i := range decisions {
		if decisions[i].Version == version && decisions[i].ActiveAt(now) {
			hasActiveForVersion = true
			break
		}
	}
	if !hasActiveForVersion {
		return -1
	}
	commit, _, err := peelPendingTag(tags, repo, tag)
	if err != nil {
		return -1
	}
	for i := range decisions {
		if decisions[i].Version == version && decisions[i].Commit == commit && decisions[i].ActiveAt(now) {
			return i
		}
	}
	return -1
}

// activeDecisionForTag returns an active stored decision matching tag's
// commit, peeling tag only when version has an active decision on repo.
func activeDecisionForTag(tags github.ReleaseTagReader, repo model.RepoRef, version string, tag github.TagRef,
	decisions []model.ReleaseApprovalDecision, now time.Time) *model.ReleaseApprovalDecision {
	idx := activeDecisionIndexForTag(tags, repo, version, tag, decisions, now)
	if idx < 0 {
		return nil
	}
	return &decisions[idx]
}

// failedDecisionForCommit returns the failed decision recorded for (version,
// commit), if any.
func failedDecisionForCommit(decisions []model.ReleaseApprovalDecision, version, commit string) *model.ReleaseApprovalDecision {
	for i := range decisions {
		if decisions[i].Version == version && decisions[i].Commit == commit && decisions[i].Failed() {
			return &decisions[i]
		}
	}
	return nil
}

// releaseCommitAnswered reports whether (repo, version, commit) was already
// answered within releasePendingAnswerHold -- either recorded in
// release_approval (CLI, API, or Inbox Approve/Reject) or dismissed directly
// on the notification row by a person after any earlier recorded decision.
func releaseCommitAnswered(decisions []model.ReleaseApprovalDecision, all []model.Notification,
	repo model.RepoRef, version, commit string, now time.Time) bool {
	var latestDecisionAt time.Time
	for _, d := range decisions {
		if d.Version != version {
			continue
		}
		if d.Commit == commit && d.ActiveAt(now) {
			return true
		}
		if d.DecidedAt.After(latestDecisionAt) {
			latestDecisionAt = d.DecidedAt
		}
	}
	return releaseAnsweredAfter(all, repo, version, latestDecisionAt, now)
}

// releaseAnsweredAfter reports whether a person cleared repo's version
// notification within releasePendingAnswerHold on a raise younger than after.
// When after is non-zero (an earlier commit or expired hold recorded a
// decision), a clear from before the subsequent raise belonged to that earlier
// decision rather than to what is staged now.
func releaseAnsweredAfter(all []model.Notification, repo model.RepoRef, version string, after, now time.Time) bool {
	subject := model.NotificationSubject(model.FamilyReleasePending, version)
	for _, n := range all {
		if n.Subject != subject || n.ClearedAt == nil {
			continue
		}
		if n.Repo != "" && n.Repo != repo.String() {
			continue
		}
		if !after.IsZero() && !n.LastRaisedAt.After(after) {
			continue
		}
		if strings.HasPrefix(n.ClearedBy, string(model.PrincipalHuman)+":") && now.Sub(*n.ClearedAt) < releasePendingAnswerHold {
			return true
		}
	}
	return false
}

// pendingReleaseBody is what the notification says: any breaking-change
// gate warning (when the pending release carries breaking changes outside
// the major release RELEASE_MAJOR asks for, so approving it needs
// allow_breaking: true), what the -pending tag says of itself, and the
// release notes, from docs/releases/<v>.md on trunk when they have merged.
func pendingReleaseBody(files github.FileReader, comparer releaseCommitComparer, repo model.RepoRef,
	released, retracted map[string]bool, version, commit, tagMessage string) string {
	notesPath := "docs/releases/" + version + ".md"
	var notes []byte
	found := false
	if files != nil {
		notes, found, _ = files.ReadFile(repo.StoredOwner(), repo.Name, "main", notesPath)
	}
	breaking := pendingReleaseBreakingWarning(files, comparer, repo, released, retracted, version, commit, tagMessage, string(notes))

	var b strings.Builder
	fmt.Fprintf(&b, "%s is staged as %s-pending on %s and waits for a person: Approve tags %s, moves latest onto it "+
		"and publishes its images (GitHub Actions promote-release.yml); Reject drops it.\n\n", version, version, repo, version)
	if breaking != "" {
		b.WriteString(breaking + "\n\n")
	}
	if msg := strings.TrimSpace(tagMessage); msg != "" {
		b.WriteString(msg + "\n\n")
	}
	if !found {
		fmt.Fprintf(&b, "Release notes: %s has not merged yet; merge the release-notes task's pull request before approving.\n", notesPath)
		return b.String()
	}
	text := strings.TrimSpace(string(notes))
	if len(text) > releasePendingNotesLimit {
		text = text[:releasePendingNotesLimit] + "\n\n… (the rest is in " + notesPath + ")"
	}
	fmt.Fprintf(&b, "Release notes (%s):\n\n%s\n", notesPath, text)
	return b.String()
}

var (
	actionsTimestampPrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z\s+`)
	anyReleaseVersionToken = regexp.MustCompile(`\bv[0-9]+\.[0-9]+\.[0-9]+\b`)
)

type promoteReleaseFailure struct {
	RunRef           string
	HTMLURL          string
	DecisionVerb     string
	Reason           string
	NeedsApprovalKey bool
}

func (f *promoteReleaseFailure) Header() string {
	return fmt.Sprintf("DISPATCH FAILED (%s): %s", f.RunRef, f.Reason)
}

func (f *promoteReleaseFailure) FormatBlock(repo model.RepoRef) string {
	var b strings.Builder
	b.WriteString(f.Header() + "\n")
	if f.NeedsApprovalKey {
		fmt.Fprintf(&b, "Remedy: set the repository variable GRAIN_RELEASE_APPROVAL_KEY on %s to the publicKey from GET /api/release-approval-key (or `grain release approval-key`), then approve or reject again.\n", repo)
	}
	if f.HTMLURL != "" {
		fmt.Fprintf(&b, "Run URL: %s\n", f.HTMLURL)
	}
	return b.String()
}

// checkPromoteReleaseFailure inspects repo's recent promote-release.yml runs
// after d.DecidedAt and reports a failure when the dispatched run concluded
// unsuccessfully without promoting or dropping version.
func checkPromoteReleaseFailure(actions github.ActionsReader, repo model.RepoRef, version string,
	d model.ReleaseApprovalDecision) *promoteReleaseFailure {
	workflow := d.Workflow
	if workflow == "" {
		workflow = "promote-release.yml"
	}
	runs, err := actions.ListActionsRuns(repo.StoredOwner(), repo.Name, github.ActionsRunsQuery{
		Workflow: workflow,
		Branch:   "main",
		Since:    d.DecidedAt.Add(-time.Minute),
		Limit:    10,
	})
	if err != nil || len(runs) == 0 {
		return nil
	}
	cutoff := d.DecidedAt.Add(-5 * time.Second)
	for _, r := range runs {
		if r.Event != "" && r.Event != "workflow_dispatch" {
			continue
		}
		if r.StartedAt != nil && r.StartedAt.Before(cutoff) && (r.UpdatedAt == nil || r.UpdatedAt.Before(d.DecidedAt)) {
			continue
		}
		if mentionsDifferentVersion(r.DisplayTitle, version) {
			continue
		}
		if r.Status != "completed" {
			return nil // the newest dispatch run is still in progress
		}
		switch r.Conclusion {
		case "", "success", "neutral", "skipped":
			return nil
		}
		jobs, _ := actions.ListActionsJobs(repo.StoredOwner(), repo.Name, r.ID)
		reason := ""
		needsKey := false
		wrongVersion := false
		for _, j := range jobs {
			if j.Conclusion == "" || j.Conclusion == "success" || j.Conclusion == "neutral" || j.Conclusion == "skipped" {
				continue
			}
			if mentionsDifferentVersion(j.Name, version) {
				wrongVersion = true
				break
			}
			logText, _ := actions.ActionsJobLog(repo.StoredOwner(), repo.Name, j.ID)
			if mentionsDifferentVersionInPromoteLog(logText, version) {
				wrongVersion = true
				break
			}
			if strings.Contains(logText, "GRAIN_RELEASE_APPROVAL_KEY") {
				needsKey = true
			}
			if extracted := extractPromoteReleaseReason(logText); extracted != "" {
				reason = extracted
				break
			}
			if reason == "" && j.Name != "" {
				reason = fmt.Sprintf("job %q ended with %s", j.Name, j.Conclusion)
			}
		}
		if wrongVersion {
			continue
		}
		if reason == "" {
			reason = fmt.Sprintf("%s ended with %s", workflow, r.Conclusion)
		}
		if strings.Contains(reason, "GRAIN_RELEASE_APPROVAL_KEY") {
			needsKey = true
		}
		return &promoteReleaseFailure{
			RunRef:           fmt.Sprintf("gha:%s:%s:%d", repo.StoredOwner(), repo.Name, r.ID),
			HTMLURL:          r.HTMLURL,
			DecisionVerb:     d.DecisionVerb(),
			Reason:           reason,
			NeedsApprovalKey: needsKey,
		}
	}
	return nil
}

func mentionsDifferentVersion(text, version string) bool {
	if text == "" || strings.Contains(text, version) {
		return false
	}
	return anyReleaseVersionToken.MatchString(text)
}

func mentionsDifferentVersionInPromoteLog(logText, version string) bool {
	if logText == "" || strings.Contains(logText, version) {
		return false
	}
	for _, line := range strings.Split(logText, "\n") {
		if strings.Contains(line, "promote-release:") || strings.Contains(line, "verify-release-approval:") ||
			strings.Contains(line, "EXPECT_VERSION") {
			if anyReleaseVersionToken.MatchString(line) {
				return true
			}
		}
	}
	return false
}

// pendingReleaseBreakingWarning mirrors scripts/release-from-trunk.sh and
// scripts/release-notes.sh check-breaking: when version carries breaking
// changes since the newest non-retracted, non-RELEASE_NEVER_PROMOTE
// release before it without being a major bump RELEASE_MAJOR asks for,
// promote-release.yml refuses an approval unless allow_breaking is true.
func pendingReleaseBreakingWarning(files github.FileReader, comparer releaseCommitComparer, repo model.RepoRef,
	released, retracted map[string]bool, version, commit, tagMessage, mergedNotes string) string {
	for _, line := range strings.Split(tagMessage, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "BREAKING CHANGES:") ||
			(strings.Contains(l, "carries breaking changes") && strings.Contains(l, "is not the major release RELEASE_MAJOR asks for")) {
			return ""
		}
	}
	confMajor, hasConfMajor, neverPromotes := readReleaseConf(files, repo, commit)
	notesBase := newestNotesBase(released, retracted, neverPromotes, version)
	if notesBase == "" {
		return ""
	}
	prevMajor, okPrev := parseSemverMajor(notesBase)
	newMajor, okNew := parseSemverMajor(version)
	if okPrev && okNew && hasConfMajor && newMajor > prevMajor && confMajor >= newMajor {
		return ""
	}
	hasBreaking := false
	compared := false
	if comparer != nil && commit != "" {
		if commits, err := comparer.CompareCommits(repo.StoredOwner(), repo.Name, notesBase, commit); err == nil {
			compared = true
			hasBreaking = slices.ContainsFunc(commits, commitIsBreaking)
		}
	}
	if !compared && strings.Contains("\n"+strings.ReplaceAll(mergedNotes, "\r\n", "\n")+"\n", "\n## Breaking changes\n") {
		hasBreaking = true
	}
	if !hasBreaking {
		return ""
	}
	return fmt.Sprintf("BREAKING CHANGES: %s carries breaking changes since %s and is not the major release RELEASE_MAJOR asks for; approving it needs allow_breaking: true.",
		version, notesBase)
}

func readReleaseConf(files github.FileReader, repo model.RepoRef, commit string) (major int, hasMajor bool, never map[string]bool) {
	never = map[string]bool{}
	if files == nil {
		return 0, false, never
	}
	var raw []byte
	var ok bool
	if commit != "" {
		raw, ok, _ = files.ReadFile(repo.StoredOwner(), repo.Name, commit, "scripts/release.conf")
	}
	if !ok {
		raw, ok, _ = files.ReadFile(repo.StoredOwner(), repo.Name, "main", "scripts/release.conf")
	}
	if !ok {
		return 0, false, never
	}
	var neverRaw string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		l := strings.TrimSpace(line)
		if m := releaseMajorConfPattern.FindStringSubmatch(l); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				major, hasMajor = n, true
			}
		} else if m := releaseNeverPromoteQuotedPattern.FindStringSubmatch(l); m != nil {
			neverRaw = m[1]
		} else if m := releaseNeverPromoteBarePattern.FindStringSubmatch(l); m != nil {
			neverRaw = m[1]
		}
	}
	for _, tag := range strings.Fields(neverRaw) {
		never[tag] = true
	}
	return major, hasMajor, never
}

func newestNotesBase(released, retracted, never map[string]bool, target string) string {
	best := ""
	for v := range released {
		if retracted[v] || never[v] || !semverLess(v, target) {
			continue
		}
		if best == "" || semverLess(best, v) {
			best = v
		}
	}
	return best
}

func parseSemverParts(v string) ([3]int, bool) {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func parseSemverMajor(v string) (int, bool) {
	parts, ok := parseSemverParts(v)
	return parts[0], ok
}

func semverLess(a, b string) bool {
	pa, oka := parseSemverParts(a)
	pb, okb := parseSemverParts(b)
	if !oka || !okb {
		return a < b
	}
	for i := range 3 {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func extractPromoteReleaseReason(logText string) string {
	if strings.TrimSpace(logText) == "" {
		return ""
	}
	var cleaned []string
	for _, raw := range strings.Split(logText, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		line = actionsTimestampPrefix.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "##[group]") || strings.HasPrefix(line, "##[endgroup]") ||
			strings.HasPrefix(line, "##[command]") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	stripErr := func(s string) string {
		return strings.TrimSpace(strings.TrimPrefix(s, "##[error]"))
	}
	capLen := func(s string) string {
		if len(s) > 400 {
			return s[:400] + "…"
		}
		return s
	}
	for _, line := range cleaned {
		s := stripErr(line)
		if idx := strings.Index(s, "verify-release-approval: refused:"); idx >= 0 {
			return capLen(s[idx:])
		}
	}
	for _, line := range cleaned {
		s := stripErr(line)
		if strings.Contains(s, "RELEASE_ALLOW_BREAKING") || strings.Contains(s, "carries breaking changes") ||
			strings.Contains(s, "short_soak_ok") || strings.Contains(s, "SHORT SOAK") {
			return capLen(s)
		}
	}
	for _, line := range cleaned {
		s := stripErr(line)
		if (strings.HasPrefix(s, "promote-release:") && !strings.Contains(s, "approved ") && !strings.Contains(s, "in grain's Inbox")) ||
			strings.Contains(s, "release-from-trunk: ERROR:") || strings.Contains(s, "refused:") {
			return capLen(s)
		}
	}
	for _, line := range cleaned {
		if after, ok := strings.CutPrefix(line, "##[error]"); ok {
			s := strings.TrimSpace(after)
			if s != "" && !strings.HasPrefix(s, "Process completed with exit code") {
				return capLen(s)
			}
		}
	}
	for i := len(cleaned) - 1; i >= 0; i-- {
		s := stripErr(cleaned[i])
		if s != "" && !strings.HasPrefix(s, "Process completed with exit code") {
			return capLen(s)
		}
	}
	return ""
}

func commitIsBreaking(c github.Commit) bool {
	msg := strings.ReplaceAll(c.Message, "\r\n", "\n")
	subject, rest, _ := strings.Cut(msg, "\n")
	if breakingSubjectPattern.MatchString(subject) {
		return true
	}
	if mergePullRequestPattern.MatchString(subject) {
		for _, line := range strings.Split(rest, "\n") {
			t := strings.TrimSpace(line)
			if t == "" {
				continue
			}
			if breakingSubjectPattern.MatchString(t) {
				return true
			}
			break
		}
	}
	for _, line := range strings.Split(msg, "\n") {
		if breakingTrailerPattern.MatchString(line) {
			return true
		}
	}
	return false
}
