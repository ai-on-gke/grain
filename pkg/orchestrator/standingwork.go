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
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A repository's standing work, switched off (grain/task-3332).
//
// model.WorkOwnership has said for a while whether this deployment runs
// a repository's work, and until this only the release reconciler asked
// it. So an operator who turned gke-labs/bouncer's release off on Oct 12
// watched the base-branch check go on running nightly.yml's thirteen jobs
// on every new tip of `main`, every run erroring on a team refusal and
// being retried, because nothing on that path read the decision.
//
// Every piece of standing work grain does for a repository now asks
// model.RunsWorkItem before it starts, each under an item of its own so a
// per-item override can turn one piece back on, and all of them following
// the repository default -- which is what makes Default off the one
// switch for "this deployment runs no automated work here":
//
//   - schedules -- declared, settings-owned and workflow crons -- under
//     their own ids (model.Store.suppressStandingWorkOff, and
//     startScheduledWorkflowRun for a firing already recorded);
//   - the release (model.ReleaseItem, baseBranchReleaseWanted);
//   - the base-branch check (model.BaseBranchCIItem, baseBranchCIWanted
//     and standDownBaseBranchCI below);
//   - post-merge dispatch jobs (model.PostMergeItem,
//     startDueBaseBranchDispatch);
//   - automatic ports (model.PortsItem, filePorts);
//   - triage and repair tasks grain files on its own (model.TriageItem,
//     fileBaseBranchReleaseTriage and fileBaseBranchRepair).
//
// What it does not stop is anything a person asked for: a task filed by
// hand against the repository, its pull request's CI, the merge queue
// for it and the repair of that pull request. None of those is standing
// work, and none of them asks.

// runsStandingWork is model.RunsWorkItem for a repository read out of the
// store -- the one question every path above asks, spelled once. A
// deployment with no store has no record to say no in, so it runs the
// work, which is what an unset record resolves to anyway.
func runsStandingWork(ctx context.Context, store *model.Store, repo model.RepoRef,
	item model.WorkItem) (bool, error) {

	if store == nil {
		return true, nil
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's work ownership: %w", repo, err)
	}
	return model.RunsWorkItem(cfg, item), nil
}

// baseBranchCIOff reports whether this deployment has said it does not
// run repo's base-branch check (model.BaseBranchCIItem). The deployment's
// own configuration repository never is off: it is judged whether or not
// anybody opted it in (grainci.RequiredFor), because merging a dump grain
// cannot import can stop the deployment on its next start, and a switch
// that could turn that off is not one to offer.
func baseBranchCIOff(ctx context.Context, store *model.Store, repo model.RepoRef) (bool, error) {
	if grainci.RequiredFor(repo) {
		return false, nil
	}
	runs, err := runsStandingWork(ctx, store, repo, model.BaseBranchCIItem)
	return !runs, err
}

// standDownBaseBranchCI is what a branch whose repository's base-branch
// check has been switched off becomes: a row with no run, no verdict and
// nothing owed, which is the shape every reader of model.BaseBranchCI
// already reads as "nothing to wait for and nothing to blame".
//
// The row is kept rather than deleted, because it is also the list of
// branches grain watches -- a release switched back on by a per-item
// override follows it (startDueBaseBranchRelease), and so do post-merge
// dispatches -- and its commit is the base a task's own required run is
// compared against (startRequiredCIRun). Everything that made it a
// verdict goes:
//
//   - a run in flight, or a confirmation, is cancelled -- it is the work
//     the switch is there to stop;
//   - the conclusion and its detail, so a red row stops being red to the
//     merge queue (brokenBaseBranch) and the pipeline steward, and an
//     errored one stops being retried (baseCIRetryDue);
//   - pending merges and an operator's request for a run, which would
//     otherwise start one the moment it is read;
//   - the repair the row names. The repair task itself is left alone --
//     somebody may be in the middle of it.
//
// Writes nothing for a row already in that shape, so a branch that stays
// off costs one comparison a cycle.
func standDownBaseBranchCI(ctx context.Context, deps Deps, row model.BaseBranchCI, now time.Time) error {
	return standDownBaseBranchCIWithReason(ctx, deps, row, model.StandingWorkOffReason(row.Repo), now)
}

func standDownBaseBranchCIWithReason(ctx context.Context, deps Deps, row model.BaseBranchCI, why string, now time.Time) error {
	idle := model.BaseBranchCI{Repo: row.Repo, Branch: row.Branch, Commit: row.Commit, StartedAt: row.StartedAt}
	if row.RunID == "" && row.Conclusion == "" && row.Confirmation == "" && row.ConfirmRunID == "" &&
		row.PendingTaskID == "" && row.PendingMerges == 0 && row.RunRequestedAt == nil &&
		row.RepairTaskID == "" && row.Detail == "" {
		return nil
	}
	reason := "cancelled: " + why
	if deps.CI != nil {
		if baseCIInFlight(row) {
			deps.CI.Cancel(row.RunID, reason, ci.CancelBySystem)
		}
		if row.ConfirmRunID != "" && row.Confirmation == model.BaseCIConfirming {
			deps.CI.Cancel(row.ConfirmRunID, reason, ci.CancelBySystem)
		}
	}
	log.Printf("orchestrator: not judging %s %s: %s; it holds nothing and owes no run until that is turned back on",
		row.Repo, row.Branch, why)
	return deps.Store.PutBaseBranchCI(ctx, idle)
}
