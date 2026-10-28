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
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// SyncSuites advances every active suite run by exactly the step it is
// missing (bwsalmon/agents#642): if its current pass has not finished
// yet, nothing -- next cycle checks again; if it has, either stop the
// run (SuiteRunSucceeded or SuiteRunFailed) or fire the next pass,
// whichever model.OutcomeOfPass and the run's own
// Mode/Count/MaxPasses say to do.
//
// Level-triggered like every other reconciler here: ActiveSuiteRuns
// only ever names a run this cycle still has something to check, so a
// run already Succeeded or Failed is never read again, and one run's
// own store error does not stop this cycle from advancing the others.
func SyncSuites(ctx context.Context, store *model.Store, now time.Time) error {
	return syncSuites(ctx, store, nil, now)
}

// syncSuites is SyncSuites with reach, which the reconcile loop passes
// (Deps.GitReach) so a pass whose team cannot reach a repository it
// names is parked as it is filed (parkUnreached).
func syncSuites(ctx context.Context, store *model.Store, reach *gitproxy.Reach, now time.Time) error {
	runs, err := store.ActiveSuiteRuns(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing active suite runs: %w", err)
	}
	var errs []error
	for _, run := range runs {
		if err := syncSuiteRun(ctx, store, reach, run, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: advancing suite run %d: %w", run.ID, err))
		}
	}
	return errors.Join(errs...)
}

// syncSuiteRun decides run's own next step from its current pass's
// outcome:
//
// - PassPending: the pass has not finished (or, for a
// RequireApproval run, a task in it is still waiting on a human's
// approval) -- do nothing this cycle.
// - PassFailed: at least one task in the pass failed or closed without
// completing -- the run stops here, SuiteRunFailed.
// - PassClean or PassChanged: every task in the pass completed. A
// SuiteCount run fires another pass until it has run Count of them,
// then stops, succeeded, whatever any pass produced. A SuiteUntilClean
// run stops the moment a pass is PassClean (bwsalmon/agents#642's own
// "run the tasks until they generate no issues or repo changes"), or
// fires another pass, up to MaxPasses -- reaching MaxPasses without
// ever seeing a clean pass is reported as SuiteRunFailed, not silently
// accepted, so a suite that never converges is visible rather than
// looking the same as one that succeeded on its very last allowed pass.
func syncSuiteRun(ctx context.Context, store *model.Store, reach *gitproxy.Reach, run model.SuiteRun,
	now time.Time) error {

	pass := run.CurrentPass()
	outcome := model.OutcomeOfPass(run.PassTasks(pass))

	switch outcome {
	case model.PassPending:
		return nil
	case model.PassFailed:
		return store.CompleteSuiteRun(ctx, run.ID, model.SuiteRunFailed,
			fmt.Sprintf("pass %d had a task that failed, or closed without completing", pass), now)
	}

	switch run.Mode {
	case model.SuiteCount:
		if pass >= run.Count {
			return store.CompleteSuiteRun(ctx, run.ID, model.SuiteRunSucceeded, "", now)
		}
	case model.SuiteUntilClean:
		if outcome == model.PassClean {
			return store.CompleteSuiteRun(ctx, run.ID, model.SuiteRunSucceeded, "", now)
		}
		if pass >= run.MaxPasses {
			return store.CompleteSuiteRun(ctx, run.ID, model.SuiteRunFailed,
				fmt.Sprintf("reached %d passes without one that produced no repo change or follow-up task", run.MaxPasses), now)
		}
	default:
		return fmt.Errorf("suite run %d has an unknown mode %q", run.ID, run.Mode)
	}

	next, err := store.FireNextPass(ctx, run, now)
	var missing *model.MissingSuiteTemplateError
	if errors.As(err, &missing) {
		// A template the run's items name is gone, and no later cycle
		// brings it back: retrying left the run "active" for good with
		// nothing on it to say why (2026-10-11 walkthrough). Ended here
		// instead, with the reason on the run, where its pane shows it.
		return store.CompleteSuiteRun(ctx, run.ID, model.SuiteRunFailed,
			fmt.Sprintf("pass %d could not be filed: %v", pass+1, missing), now)
	}
	if err != nil {
		return err
	}
	return parkUnreached(ctx, store, reach, suiteRunTaskIDs(next, next.CurrentPass()), now)
}
