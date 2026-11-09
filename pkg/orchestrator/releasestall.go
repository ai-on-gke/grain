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
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A merge that starts no release, said out loud (releases gap 6,
// design/playbook/releases.playbook.md §0).
//
// startDueBaseBranchRelease skips a branch for half a dozen reasons --
// grain cannot read which branch is trunk, the branch has no
// model.BaseBranchCI row to follow, the release run cannot be started,
// a run in flight that never ends -- and every one of them used to be
// silent: at most a sayOnce line in the daemon log. All of 2026-11-02
// the Google edition's merges to `main` published nothing, and the
// director started each of its release runs by hand, because nothing
// anybody reads said a release was owed and not started.
//
// So this reads the outcome rather than the gates: for every repository
// grain releases (baseBranchReleaseWanted) whose release workflow
// answers `release`, is trunk's tip released, or being released? A tip
// that has been owed for releaseNotStartingAfter raises one
// pipeline.release-not-starting notification for the repository, naming
// the skip that is holding it, and the notification clears once a
// release of the tip starts. Reading the outcome is what makes the next
// unknown skip visible too: it is reported as "nothing has started a
// release" rather than not at all.
//
// A release that starts and fails is not this: that is
// pipeline.not-publishing's (reconcilePublishingNotifications).

const (
	// releaseNotStartingAfter is how long trunk's tip may go without a
	// release before it is reported. A merge is normally released on the
	// next cycle; this is long enough for "one per cycle" behind other
	// repositories and a forge hiccup to pass on their own.
	releaseNotStartingAfter = 30 * time.Minute
	// releaseStallCheckEvery bounds the forge reads this costs: the
	// default branch and its tip, per repository grain releases.
	releaseStallCheckEvery = 5 * time.Minute
	// releaseStuckAfter is when a release still in flight is taken to be
	// wedged: past ci.DefaultMaxRunTime, a run the service should have
	// ended by now, and while it is in flight no newer tip is released.
	releaseStuckAfter = ci.DefaultMaxRunTime + time.Hour
)

// releaseStall is what one repository's last check found.
type releaseStall struct {
	checkedAt time.Time
	// branch and tip are trunk and its head as last read ("" when they
	// could not be), and since is when that tip was first seen owed.
	branch, tip string
	since       time.Time
	// reason is why it is not released, "" when nothing is owed.
	reason string
	// raised is whether the notification is open for it.
	raised bool
	// workflow caches, for tip, whether a release workflow answers.
	workflowFor string
	workflow    bool
}

var (
	// releaseStalls is the last check per store and repository, keyed by
	// releaseStallKey; releaseSkips is why startBaseBranchRelease last
	// declined a branch, keyed by releaseSkipKey and cleared when it
	// starts one. Both are per store so that tests running in parallel
	// against the same repository name do not see each other's.
	releaseStalls sync.Map
	releaseSkips  sync.Map
	releaseStallM sync.Mutex
)

func releaseStallKey(store *model.Store, repo model.RepoRef) string {
	return fmt.Sprintf("%d|%s", store.ID(), strings.ToLower(repo.String()))
}

func releaseSkipKey(store *model.Store, repo model.RepoRef, branch string) string {
	return fmt.Sprintf("%d|%s", store.ID(), strings.ToLower(model.BranchKey(repo, branch)))
}

// noteReleaseSkip records why startBaseBranchRelease did not start a
// release of repo's branch this cycle, for the stall check to name; an
// empty why clears it.
func noteReleaseSkip(store *model.Store, repo model.RepoRef, branch, why string) {
	key := releaseSkipKey(store, repo, branch)
	if why == "" {
		releaseSkips.Delete(key)
		return
	}
	releaseSkips.Store(key, why)
}

func releaseSkipOf(store *model.Store, repo model.RepoRef, branch string) string {
	if v, ok := releaseSkips.Load(releaseSkipKey(store, repo, branch)); ok {
		return v.(string)
	}
	return ""
}

// reconcileReleaseStalls raises pipeline.release-not-starting for each
// repository grain releases whose trunk tip has been owed a release for
// releaseNotStartingAfter, and clears the ones that are not owed any
// more.
func reconcileReleaseStalls(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.CI == nil {
		return nil
	}
	configs, err := deps.Store.ListRepoConfigs(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading repo configs: %w", err)
	}
	releaseStallM.Lock()
	defer releaseStallM.Unlock()
	var errs []error
	current := map[string]bool{}
	for i := range configs {
		repo := configs[i].Repo
		wanted, err := baseBranchReleaseWanted(ctx, deps.Store, repo)
		if err != nil || !wanted {
			continue
		}
		if allowed, err := repoAllowedForAnyTeam(ctx, deps.Store, repo); err != nil || !allowed {
			continue
		}
		subject := model.NotificationSubject(model.FamilyReleaseNotStarting, repo.String())
		key := releaseStallKey(deps.Store, repo)
		var st releaseStall
		if v, ok := releaseStalls.Load(key); ok {
			st = v.(releaseStall)
		}
		if !st.checkedAt.IsZero() && now.Sub(st.checkedAt) < releaseStallCheckEvery {
			if st.raised {
				current[subject] = true
			}
			continue
		}
		branch, tip, reason, err := releaseStallReason(ctx, deps, &configs[i], now)
		if err != nil {
			errs = append(errs, err)
			if st.raised {
				current[subject] = true
			}
			continue
		}
		if reason == "" {
			releaseStalls.Store(key, releaseStall{checkedAt: now})
			continue
		}
		if st.since.IsZero() || st.branch != branch || st.tip != tip {
			st = releaseStall{since: now, branch: branch, tip: tip}
		}
		st.checkedAt, st.reason = now, reason
		if now.Sub(st.since) < releaseNotStartingAfter {
			st.raised = false
			releaseStalls.Store(key, st)
			continue
		}
		if st.workflowFor != tip || tip == "" {
			st.workflowFor, st.workflow = tip, releaseWorkflowAnswers(ctx, deps, repo, branch, tip)
		}
		if !st.workflow {
			// Nothing in the repository answers `release`, so there is
			// nothing a merge there owes: not a stall.
			st.raised = false
			releaseStalls.Store(key, st)
			continue
		}
		current[subject] = true
		st.raised = true
		releaseStalls.Store(key, st)
		errs = append(errs, ensurePipelineNotification(ctx, deps.Store, model.Notification{
			Subject:  subject,
			Severity: model.NotificationWarning,
			Title:    releaseStallTitle(repo, branch),
			Body:     releaseStallBody(repo, branch, tip, reason, now.Sub(st.since)),
			Repo:     repo.String(),
			RaisedBy: releaseNotifier,
			Actions:  []string{"investigate"},
		}, now))
	}
	open, err := deps.Store.ListNotifications(ctx, model.NotificationFilter{
		Open: true, Family: model.FamilyReleaseNotStarting, Limit: 1000,
	})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, n := range open {
		if current[n.Subject] {
			continue
		}
		if _, err := deps.Store.ClearNotification(ctx, n.Subject, releaseNotifier); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("orchestrator: cleared notification %s: its trunk is released, or being released", n.Subject)
	}
	return errors.Join(errs...)
}

// releaseStallReason is why repo's trunk tip is owed a release and has
// none started, or "" when it is released, being released, or was
// released and failed (pipeline.not-publishing's). branch and tip are
// trunk and its head, "" where they could not be read. An error is a
// store error, which says nothing about the repository.
func releaseStallReason(ctx context.Context, deps Deps, cfg *model.RepoConfig,
	now time.Time) (branch, tip, reason string, err error) {

	repo := cfg.Repo
	if deps.Client == nil {
		return "", "", "", nil
	}
	branch, derr := deps.Client.DefaultBranch(repo.StoredOwner(), repo.Name)
	if derr != nil {
		return "", "", fmt.Sprintf("grain cannot read its default branch from %s, so it cannot tell which "+
			"branch is trunk and releases none of them: %v", repo.GitHost(), derr), nil
	}
	if branch == "" {
		return "", "", fmt.Sprintf("%s reports no default branch for it, so grain cannot tell which branch "+
			"is trunk and releases none of them", repo.GitHost()), nil
	}
	tip, terr := branchTip(ctx, deps, repo, branch)
	if terr != nil {
		return branch, "", fmt.Sprintf("grain cannot read the tip of `%s`: %v", branch, terr), nil
	}
	if tip == "" {
		return branch, "", fmt.Sprintf("`%s` has no head on %s", branch, repo.GitHost()), nil
	}
	rel, err := deps.Store.GetBaseBranchRelease(ctx, repo, branch)
	if err != nil {
		return branch, tip, "", err
	}
	if rel != nil && rel.InFlight() {
		if age := now.Sub(rel.StartedAt); age >= releaseStuckAfter {
			return branch, tip, fmt.Sprintf("release run `%s`, started %s ago for %s, has never finished, and "+
				"while it is in flight no newer tip of `%s` is released: cancel it, and the next cycle starts "+
				"one for %s", rel.RunID, humanDuration(age.Round(time.Minute)), shortSHA(rel.Commit), branch,
				shortSHA(tip)), nil
		}
		return branch, tip, "", nil
	}
	if rel != nil && rel.Commit == tip {
		return branch, tip, "", nil
	}
	if why := releaseSkipOf(deps.Store, repo, branch); why != "" {
		return branch, tip, why, nil
	}
	row, err := deps.Store.GetBaseBranchCI(ctx, repo, branch)
	if err != nil {
		return branch, tip, "", err
	}
	if row == nil {
		why := fmt.Sprintf("grain has no base-branch row for `%s`, and a release follows the branches "+
			"base-branch CI watches: no merge grain recorded has enrolled it", branch)
		if !model.RunsWorkItem(cfg, model.BaseBranchCIItem) {
			why += fmt.Sprintf(", because this deployment does not run the repository's %s work item",
				model.BaseBranchCIItem)
		} else {
			why += " (were its merges recorded on this forge?). Ticking \"watch the default branch\" on " +
				"the repository's settings enrols it without a merge"
		}
		return branch, tip, why, nil
	}
	last := "it has never released `" + branch + "`"
	if rel != nil {
		last = fmt.Sprintf("the last release it started was of %s", shortSHA(rel.Commit))
	}
	return branch, tip, fmt.Sprintf("nothing has started a release of `%s` at %s, and no skip of grain's "+
		"says why: %s", branch, shortSHA(tip), last), nil
}

// releaseWorkflowAnswers reports whether a release of repo's branch at
// tip would run anything: a workflow there answers `release`. Where
// trunk could not be read, its workflows are read at a branch grain
// watches for the repository instead -- the CI source reads them over
// git rather than the forge's API. Where they cannot be read at all, a
// release this repository has ever started that found something to run
// is taken as the answer.
func releaseWorkflowAnswers(ctx context.Context, deps Deps, repo model.RepoRef, branch, tip string) bool {
	if branch == "" {
		if rows, err := deps.Store.ListBaseBranchCI(ctx); err == nil {
			for _, row := range rows {
				if row.Repo.Same(repo) && row.Branch != "" {
					branch, tip = row.Branch, ""
					break
				}
			}
		}
	}
	if branch != "" {
		if options, err := deps.CI.WorkflowOptions(ctx, baseBranchReleaseRequest(repo, branch, tip)); err == nil {
			return len(options) > 0
		}
	}
	rows, err := deps.Store.ListBaseBranchRelease(ctx)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.Repo.Same(repo) && row.RunID != "" && row.Conclusion != string(ci.ConclusionSkipped) {
			return true
		}
	}
	return false
}

func releaseStallTitle(repo model.RepoRef, branch string) string {
	if branch == "" {
		return fmt.Sprintf("Merges into %s are not starting its release", repo)
	}
	return fmt.Sprintf("Merges into %s %s are not starting its release", repo, branch)
}

func releaseStallBody(repo model.RepoRef, branch, tip, reason string, owed time.Duration) string {
	var b strings.Builder
	if branch != "" && tip != "" {
		fmt.Fprintf(&b, "`%s` on %s is at %s, and grain has started no release of it in %s: ",
			branch, repo, shortSHA(tip), humanDuration(owed.Round(time.Minute)))
	} else {
		fmt.Fprintf(&b, "grain has started no release of %s in %s: ", repo, humanDuration(owed.Round(time.Minute)))
	}
	b.WriteString(reason)
	b.WriteString(".\n\nThe repository's release workflow answers `release` and its releases are on, so every " +
		"merge until this is fixed publishes nothing. A release can be started by hand from the repository's " +
		"release pane meanwhile; this clears once grain starts one itself.")
	return b.String()
}
