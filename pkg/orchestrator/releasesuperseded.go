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
	"fmt"
	"log"
	"path"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A release run a newer merge superseded is not a failed release
// (grain/task-3334).
//
// A GitHub Actions release workflow that keeps one run in flight and one
// pending per branch -- ai-on-gke/grain's release.yml does, with
// cancel-in-progress off on main -- cancels every pending run but the
// newest when merges land faster than a release finishes. On 2026-11-06
// nine merges in seven minutes ended eight release runs `cancelled`, and
// each was counted as a release that published nothing: a not-publishing
// alert and a release-fix task for a branch whose newest release went on
// to publish. The newest commit contains every commit it superseded, so
// the branch lost nothing; only those intermediate commits have no images
// of their own, and scripts/release-images.sh builds one on demand if it
// is ever tagged or promoted.
//
// So a `cancelled` release run is classified superseded when a newer run
// of the same workflow on the same branch exists -- queued, running or
// done -- whose commit is a descendant of the cancelled one's. A
// superseded run files no triage, counts no failure, and is recorded on
// the row as "superseded by <sha>". It does mark the branch as having
// published nothing since (model.BaseBranchRelease.FailingSince with no
// Failures), which is what lets ui.BaseBranchPublishing still say so if
// nothing publishes for model.BaseBranchNotPublishingFor while merges keep
// superseding each other's releases.

// onDemandReleaseRunSuffix ends the run-name of a release.yml run
// dispatched to build one commit on demand: "release <sha> (on demand)".
const onDemandReleaseRunSuffix = "(on demand)"

// supersedingRelease is the run that superseded a cancelled release run.
type supersedingRelease struct {
	Commit string
	RunID  string // "" when only the branch's tip could be read
}

// gitHubReleaseRunsOf lists the recent runs of run's workflow on branch,
// newest first. ok is false when the client cannot list Actions runs or
// the listing failed.
func gitHubReleaseRunsOf(deps Deps, run ConcludedGitHubWorkflowRun, branch string) ([]github.WorkflowRun, bool) {
	reader, ok := deps.Client.(github.ActionsReader)
	if !ok {
		return nil, false
	}
	workflow := ""
	if p := strings.TrimSpace(run.WorkflowPath); p != "" {
		workflow = path.Base(p)
	}
	runs, err := reader.ListActionsRuns(run.Repo.StoredOwner(), run.Repo.Name, github.ActionsRunsQuery{
		Branch: branch, Workflow: workflow, Limit: 50,
	})
	if err != nil {
		log.Printf("orchestrator: listing %s runs on %s %s to classify run %d: %v",
			firstNonEmpty(run.WorkflowName, run.WorkflowPath), run.Repo, branch, run.RunID, err)
		return nil, false
	}
	return runs, true
}

// sameGitHubWorkflow reports whether r is a run of run's workflow.
func sameGitHubWorkflow(r github.WorkflowRun, run ConcludedGitHubWorkflowRun) bool {
	if r.Path != "" && run.WorkflowPath != "" {
		return strings.EqualFold(strings.TrimSpace(r.Path), strings.TrimSpace(run.WorkflowPath))
	}
	return strings.EqualFold(strings.TrimSpace(r.Name), strings.TrimSpace(run.WorkflowName))
}

// isOnDemandReleaseRun reports whether r is an on-demand build of one
// commit rather than a release of its branch's head.
func isOnDemandReleaseRun(r github.WorkflowRun) bool {
	return strings.EqualFold(strings.TrimSpace(r.Event), "workflow_dispatch") &&
		strings.HasSuffix(strings.TrimSpace(r.DisplayTitle), onDemandReleaseRunSuffix)
}

// isOnDemandGitHubReleaseRun reports whether the concluded run is an
// on-demand build (release.yml dispatched with `commit: <sha>`). GitHub
// records such a run against the dispatching branch and that branch's
// head at the time, but it built and published an older commit: it says
// nothing about whether the branch's releases are working, so it is not
// recorded as one.
func isOnDemandGitHubReleaseRun(deps Deps, run ConcludedGitHubWorkflowRun, branch string) bool {
	if !strings.EqualFold(strings.TrimSpace(run.Event), "workflow_dispatch") {
		return false
	}
	runs, ok := gitHubReleaseRunsOf(deps, run, branch)
	if !ok {
		return false
	}
	for _, r := range runs {
		if r.ID == run.RunID {
			return isOnDemandReleaseRun(r)
		}
	}
	return false
}

// supersedingGitHubReleaseRun finds the newer run that superseded a
// cancelled release run: the newest run of the same workflow on the same
// branch, started after it, whose commit is a descendant of its commit.
// Where the client cannot list Actions runs, the branch's tip stands in
// for it -- the release workflow runs on every push, so a tip that has
// moved past the commit started a newer run.
func supersedingGitHubReleaseRun(ctx context.Context, deps Deps, run ConcludedGitHubWorkflowRun,
	branch string) (supersedingRelease, bool) {

	if deps.Client == nil || run.HeadSHA == "" {
		return supersedingRelease{}, false
	}
	owner, name := run.Repo.StoredOwner(), run.Repo.Name
	descends := func(sha string) bool {
		if sha == "" || strings.EqualFold(sha, run.HeadSHA) {
			return false
		}
		cmp, err := deps.Client.CompareRefs(owner, name, run.HeadSHA, sha)
		return err == nil && cmp.Status == "ahead"
	}
	if runs, ok := gitHubReleaseRunsOf(deps, run, branch); ok {
		var best github.WorkflowRun
		for _, r := range runs {
			if r.ID <= run.RunID || r.ID <= best.ID || !sameGitHubWorkflow(r, run) ||
				(r.HeadBranch != "" && strings.TrimPrefix(r.HeadBranch, "refs/heads/") != branch) ||
				isOnDemandReleaseRun(r) || isGitHubWorkflowRunForeignHead(run.Repo, r.HeadRepo) ||
				!isGitHubBranchWorkflowEvent(r.Event) {
				continue
			}
			if descends(r.HeadSHA) {
				best = r
			}
		}
		if best.ID == 0 {
			return supersedingRelease{}, false
		}
		return supersedingRelease{Commit: best.HeadSHA, RunID: strconv.FormatInt(best.ID, 10)}, true
	}
	tip, err := branchTip(ctx, deps, run.Repo, branch)
	if err != nil || !descends(tip) {
		return supersedingRelease{}, false
	}
	return supersedingRelease{Commit: tip}, true
}

// supersededDetail is what the row, the release pane and the CLI say of a
// superseded run.
func supersededDetail(by supersedingRelease) string {
	run := ""
	if by.RunID != "" {
		run = " (run " + by.RunID + ")"
	}
	return fmt.Sprintf(model.ReleaseSupersededDetailPrefix+"%s%s: a newer merge's release replaced this one before it could run, "+
		"so this commit publishes nothing of its own and nothing failed -- if it is ever tagged or promoted, "+
		"its images are built on demand", shortSHA(by.Commit), run)
}

// recordSupersededGitHubRelease records a release run a newer merge
// superseded: no failure counted, no triage filed, and a row that a newer
// run has already written is left as it is.
func recordSupersededGitHubRelease(ctx context.Context, deps Deps, existing *model.BaseBranchRelease,
	row model.BaseBranchRelease, run ConcludedGitHubWorkflowRun, by supersedingRelease) error {

	log.Printf("orchestrator: GitHub release run %d of %s %s at %s was cancelled, %s",
		run.RunID, run.Repo, row.Branch, shortSHA(run.HeadSHA), supersededDetail(by))
	if existing != nil && existing.RunID != "" {
		if prev, err := strconv.ParseInt(existing.RunID, 10, 64); err == nil && prev > run.RunID {
			// A newer run is already on the row and keeps it. If that one
			// has not published either, nothing has published since this
			// older run's merge, which is the earlier of the two.
			if existing.FailingSince != nil && row.StartedAt.Before(*existing.FailingSince) {
				kept := *existing
				at := row.StartedAt
				kept.FailingSince = &at
				return deps.Store.PutBaseBranchRelease(ctx, kept)
			}
			return nil
		}
	}
	row.Detail = supersededDetail(by)
	// Nothing has published since this run's merge -- the run that
	// superseded it will, or the branch's FailingSince says how long it
	// has been since anything did.
	if row.FailingSince == nil {
		at := row.StartedAt
		row.FailingSince = &at
	}
	return deps.Store.PutBaseBranchRelease(ctx, row)
}
