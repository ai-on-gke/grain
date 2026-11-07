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
// from GitHub Actions (ci_mode: github) the train runs on a hosted
// runner, which cannot reach this deployment -- on 11-01/02 that is why
// the Inbox never showed an Approve button. So the deployment looks,
// outbound: the vX.Y.Z-pending tag is the state, and this reconciler
// reads the repository's tags and raises the notification for every
// release staged and not yet promoted, with the -pending tag's message
// and the release notes in its body, and clears it once vX.Y.Z is tagged
// or the -pending tag is gone (approved or rejected).
//
// A person's answer clears the notification at once (ui.DecideRelease)
// while the -pending tag is still there -- the promotion run has only
// just started. So a release a person answered within
// releasePendingAnswerHold is not raised again; one still pending after
// that (its run failed) is, as the daily train run would raise it again
// on grain CI.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// releasePendingPollInterval is how often one repository's tags are
	// read: one matching-refs call, and a tag object per newly pending
	// release.
	releasePendingPollInterval = 10 * time.Minute
	// releasePendingAnswerHold is how long a person's answer keeps the
	// notification down while the -pending tag outlives it.
	releasePendingAnswerHold = 24 * time.Hour
	// releasePendingNotesLimit bounds the release notes quoted in the body.
	releasePendingNotesLimit = 3000
)

var releasePendingNotifier = model.PrincipalString(model.Principal{Kind: model.PrincipalAutomation, ID: "release-watch"})

var pendingTagPattern = regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+)-pending$`)
var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// releasePendingPolled is when each repository's tags were last read,
// keyed by the store (model.Store.ID) and the repository, so that two
// stores in one process -- tests running side by side, or one test's two
// -count iterations -- never hold each other's poll back.
var releasePendingPolled sync.Map // store id | repo string -> time.Time

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
	configs, err := deps.Store.ListRepoConfigs(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading repo configs: %w", err)
	}
	var errs []error
	for _, cfg := range configs {
		if !model.RunsWorkItem(&cfg, model.ReleaseItem) {
			continue
		}
		if allowed, err := repoAllowedForAnyTeam(ctx, deps.Store, cfg.Repo); err != nil || !allowed {
			continue
		}
		eff, err := deps.Store.EffectiveRepoConfig(ctx, cfg.Repo)
		if err != nil || eff.CIMode != model.CIModeGitHub {
			continue
		}
		key := fmt.Sprintf("%d|%s", deps.Store.ID(), cfg.Repo.String())
		if last, ok := releasePendingPolled.Load(key); ok && now.Sub(last.(time.Time)) < releasePendingPollInterval {
			continue
		}
		releasePendingPolled.Store(key, now)
		if err := watchPendingReleases(ctx, deps.Store, tags, files, cfg.Repo, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// watchPendingReleases raises and clears release.pending for repo from
// its tags as they stand now.
func watchPendingReleases(ctx context.Context, store *model.Store, tags github.ReleaseTagReader,
	files github.FileReader, repo model.RepoRef, now time.Time) error {
	refs, err := tags.ListMatchingTags(repo.Owner, repo.Name, "v")
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's release tags: %w", repo, github.Readable(err))
	}
	released := map[string]bool{}
	pending := map[string]github.TagRef{}
	for _, t := range refs {
		if releaseTagPattern.MatchString(t.Name) {
			released[t.Name] = true
		} else if m := pendingTagPattern.FindStringSubmatch(t.Name); m != nil {
			pending[m[1]] = t
		}
	}
	all, err := store.ListNotifications(ctx, model.NotificationFilter{Family: model.FamilyReleasePending, Limit: 1000})
	if err != nil {
		return fmt.Errorf("orchestrator: reading release notifications: %w", err)
	}
	var errs []error
	// Clear what is answered: an open notification about this repository
	// whose release is tagged, or whose -pending tag is gone.
	for _, n := range all {
		if n.ClearedAt != nil || n.Repo != repo.String() {
			continue
		}
		version := strings.TrimPrefix(n.Subject, model.FamilyReleasePending+":")
		if _, still := pending[version]; still && !released[version] {
			continue
		}
		if _, err := store.ClearNotification(ctx, n.Subject, releasePendingNotifier); err != nil {
			errs = append(errs, err)
		}
	}
	// Raise what waits.
	for version, tag := range pending {
		if released[version] || releaseAnswered(all, version, now) {
			continue
		}
		subject := model.NotificationSubject(model.FamilyReleasePending, version)
		if open := openBySubject(all, subject); open != nil {
			continue // raised already, by this or by the train's own run
		}
		n := model.Notification{
			Subject:  subject,
			Severity: model.NotificationWarning,
			Title:    version + " is ready to promote",
			Body:     pendingReleaseBody(tags, files, repo, version, tag),
			Repo:     repo.String(),
			RaisedBy: releasePendingNotifier,
			Actions:  model.ReleasePendingActions,
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

// releaseAnswered reports whether a person cleared version's
// notification -- approved or rejected it -- within
// releasePendingAnswerHold.
func releaseAnswered(all []model.Notification, version string, now time.Time) bool {
	subject := model.NotificationSubject(model.FamilyReleasePending, version)
	for _, n := range all {
		if n.Subject != subject || n.ClearedAt == nil {
			continue
		}
		if strings.HasPrefix(n.ClearedBy, string(model.PrincipalHuman)+":") && now.Sub(*n.ClearedAt) < releasePendingAnswerHold {
			return true
		}
	}
	return false
}

// pendingReleaseBody is what the notification says: what the -pending
// tag says of itself, and the release notes, from docs/releases/<v>.md
// on trunk when they have merged.
func pendingReleaseBody(tags github.ReleaseTagReader, files github.FileReader, repo model.RepoRef,
	version string, tag github.TagRef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is staged as %s-pending on %s and waits for a person: Approve tags %s, moves latest onto it "+
		"and publishes its images (GitHub Actions promote-release.yml); Reject drops it.\n\n", version, version, repo, version)
	if tag.ObjectType == "tag" {
		if at, err := tags.GetAnnotatedTag(repo.Owner, repo.Name, tag.ObjectSHA); err == nil && at != nil {
			if msg := strings.TrimSpace(at.Message); msg != "" {
				b.WriteString(msg + "\n\n")
			}
		}
	}
	notesPath := "docs/releases/" + version + ".md"
	var notes []byte
	found := false
	if files != nil {
		notes, found, _ = files.ReadFile(repo.Owner, repo.Name, "main", notesPath)
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
