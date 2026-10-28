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

package demo

// demo_base_branch.go is `grain demo -github-sim -fake-ci -base-branch-ci`:
// a base branch of the simulated repo turned red or green by grain's own
// base-branch reconciler, on a verdict the walkthrough chooses.
//
// Without it nothing in a demo judges a base branch: no orchestrator cycle
// runs, and -fake-ci's runs never conclude by themselves. So no walkthrough
// or e2e spec could watch grain confirm a branch red, or close a repair
// of it once the branch is green again (closeBaseRepairsOnGreen). Filing
// a repair is system:pipeline-steward's now, not the reconciler's, and
// the demo runs no steward, so a red drive files none and holds nothing.
//
// The one thing stubbed is the verdict. POST /api/e2e/base-branch/red and
// /green, with a body of {"branch": "main"}, ask for a run on the branch
// as the Branches pane's "Run now" does (model.Store.RequestBaseBranchCIRun)
// and then run the real "base-branch-ci" reconciler, finishing each
// -fake-ci run it starts for that branch -- the run at the simulated
// forge's tip, and for red the confirmation of its failing job too -- with
// failure or success, until the branch is confirmed red or judged green.
// Nothing is dispatched: the demo still runs no agents.
//
// A branch the reconciler will not judge -- one whose repository has
// its base-branch check switched off, say -- concludes as something other
// than the verdict asked for, and the hook answers 409 with the row's own
// detail. A branch that already has the verdict asked for is not judged
// again; the hook only runs the reconciler over it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// demoBaseBranchJob is the job a -fake-ci base-branch run is given when
// it is finished: the run is started with no jobs of its own (the stub
// cannot read a workflow), and a failure with a failing job is what
// grain's confirmation re-runs.
const demoBaseBranchJob = "test"

// demoBaseBranchMaxSteps bounds one drive. Red takes four reconciler
// passes (start, collect, confirm, collect the confirmation) and green
// three (start, collect, close the repairs); a drive still going after
// this many is a reconciler that is not converging, said rather than
// spun on.
const demoBaseBranchMaxSteps = 10

// demoBaseBranch is the hook's state: the reconciler's Deps, and a lock
// so two drives never finish each other's runs.
type demoBaseBranch struct {
	mu   sync.Mutex
	deps orchestrator.Deps
	ci   *demoFakeCI
	repo model.RepoRef
}

// configureBaseBranchDemo returns the hook over store, the -fake-ci stub
// and -github-sim's client, which is where the reconciler reads a
// branch's tip from.
func configureBaseBranchDemo(store *model.Store, repo model.RepoRef, fake *demoFakeCI, client github.Client) *demoBaseBranch {
	return &demoBaseBranch{
		deps: orchestrator.Deps{Store: store, Client: client, CI: fake},
		ci:   fake,
		repo: repo,
	}
}

// demoBaseBranchResult is what the hook answers: the branch's row once
// the drive has settled it.
type demoBaseBranchResult struct {
	Repo         string `json:"repo"`
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
	Conclusion   string `json:"conclusion"`
	Confirmation string `json:"confirmation,omitempty"`
	RepairTaskID string `json:"repairTaskId,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

// errDemoBaseBranchRefused is a drive the reconciler would not judge:
// grain CI is off for the repository, or the run could not be asked for.
var errDemoBaseBranchRefused = errors.New("grain did not judge the branch")

// drive turns branch red (conclusion failure) or green (success), as
// the file's own doc comment describes, and returns its row.
func (d *demoBaseBranch) drive(ctx context.Context, branch string, conclusion ci.Conclusion) (demoBaseBranchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	store := d.deps.Store
	row, err := store.GetBaseBranchCI(ctx, d.repo, branch)
	if err != nil {
		return demoBaseBranchResult{}, err
	}
	if row == nil {
		// The row a merge into the branch would have written
		// (noteBaseBranchMerge): main has one from -fake-ci, and any
		// other branch is watched from its first verdict here.
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
			Repo: d.repo, Branch: branch, StartedAt: time.Now().UTC(),
		}); err != nil {
			return demoBaseBranchResult{}, err
		}
	}
	if row == nil || row.RunID == "" || (row.Conclusion != "" && !demoBaseBranchSettled(row, conclusion)) {
		// Ask again -- the same commit included, which is the only way a
		// red tip can be judged green. A branch that already has the
		// verdict asked for is not re-judged: re-asking a red commit
		// would drop the repair its row names. It gets the passes below.
		if _, err := store.RequestBaseBranchCIRun(ctx, d.repo, branch, time.Now().UTC(), "grain demo"); err != nil {
			return demoBaseBranchResult{}, err
		}
	}

	closing := false
settle:
	for step := 0; ; step++ {
		if step == demoBaseBranchMaxSteps {
			return demoBaseBranchResult{}, fmt.Errorf("%s %s is not settled after %d reconciler passes", d.repo, branch, step)
		}
		now := time.Now().UTC()
		if err := runDemoReconcilers(ctx, d.deps, now, "base-branch-ci"); err != nil {
			return demoBaseBranchResult{}, err
		}
		if row, err = store.GetBaseBranchCI(ctx, d.repo, branch); err != nil {
			return demoBaseBranchResult{}, err
		}
		if row == nil {
			return demoBaseBranchResult{}, fmt.Errorf("%s %s has no base branch row", d.repo, branch)
		}
		switch {
		case row.RunID != "" && row.Conclusion == "":
			if _, err := d.ci.Finish(row.RunID, conclusion, demoBaseBranchJob); err != nil {
				return demoBaseBranchResult{}, err
			}
			continue
		case row.Confirmation == model.BaseCIConfirming && row.ConfirmRunID != "":
			if _, err := d.ci.Finish(row.ConfirmRunID, conclusion, demoBaseBranchJob); err != nil {
				return demoBaseBranchResult{}, err
			}
			continue
		case row.Conclusion != string(conclusion):
			detail := row.Detail
			if detail == "" && row.RunID == "" {
				// A stood-down row says nothing of its own
				// (standDownBaseBranchCI), which made this answer
				// `concluded "": ` and no more.
				cfg, err := store.GetRepoConfig(ctx, d.repo)
				if err != nil {
					return demoBaseBranchResult{}, err
				}
				if !model.RunsWorkItem(cfg, model.BaseBranchCIItem) {
					detail = fmt.Sprintf("no run was started: this deployment doesn't run %s's base-branch check (its %q work item, or the repository's standing work, is off)",
						d.repo, model.BaseBranchCIItem)
				}
			}
			return demoBaseBranchResult{}, fmt.Errorf("%w: %s %s concluded %q: %s",
				errDemoBaseBranchRefused, d.repo, branch, row.Conclusion, detail)
		case !demoBaseBranchSettled(row, conclusion):
			continue // a failure's confirmation is still to be asked for
		case conclusion == ci.ConclusionSuccess && !closing:
			// Green is collected on one pass and acted on at the start
			// of the next (closeBaseRepairsOnGreen), so one more.
			closing = true
			continue
		}
		break settle
	}
	if row, err = store.GetBaseBranchCI(ctx, d.repo, branch); err != nil {
		return demoBaseBranchResult{}, err
	}
	return demoBaseBranchResult{
		Repo: d.repo.String(), Branch: branch, Commit: row.Commit,
		Conclusion: row.Conclusion, Confirmation: row.Confirmation,
		RepairTaskID: row.RepairTaskID, Detail: row.Detail,
	}, nil
}

// demoBaseBranchSettled reports whether row is where a drive towards
// conclusion ends: confirmed red, or judged green.
func demoBaseBranchSettled(row *model.BaseBranchCI, conclusion ci.Conclusion) bool {
	if conclusion == ci.ConclusionFailure {
		return row.Confirmed()
	}
	return row.Conclusion == string(conclusion)
}

// runDemoReconcilers runs the named reconcilers of orchestrator.Reconcilers
// once, in the order given, each error named after its reconciler as
// orchestrator.RunCycle names it.
func runDemoReconcilers(ctx context.Context, deps orchestrator.Deps, now time.Time, names ...string) error {
	var errs []error
	for _, name := range names {
		for _, r := range orchestrator.Reconcilers() {
			if r.Name != name {
				continue
			}
			if err := r.Reconcile(ctx, deps, now); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// baseBranchDemoHandler serves POST /api/e2e/base-branch/red and
// /api/e2e/base-branch/green, with a body of {"branch": name}, to
// loopback callers only. Everything else goes to app.
func baseBranchDemoHandler(app http.Handler, d *demoBaseBranch) http.Handler {
	const prefix = "/api/e2e/base-branch/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, prefix) {
			app.ServeHTTP(w, r)
			return
		}
		var conclusion ci.Conclusion
		switch strings.TrimPrefix(r.URL.Path, prefix) {
		case "red":
			conclusion = ci.ConclusionFailure
		case "green":
			conclusion = ci.ConclusionSuccess
		default:
			app.ServeHTTP(w, r)
			return
		}
		if !loopbackRequest(r) {
			http.Error(w, "the base branch hook only answers loopback callers", http.StatusForbidden)
			return
		}
		var body struct {
			Branch string `json:"branch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Branch) == "" {
			http.Error(w, "the body is {\"branch\": ...}", http.StatusBadRequest)
			return
		}
		res, err := d.drive(r.Context(), strings.TrimSpace(body.Branch), conclusion)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errDemoBaseBranchRefused) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
}

// loopbackRequest reports whether r came from a loopback address.
func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
