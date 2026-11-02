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

package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"net/http"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Running a base branch's own check by hand (grain/task-480).
//
// The merge queue's account of a red base branch states the problem and
// had no verb in it: "grain CI is failing on `main` at abc123, which is
// the tree this change will land on" (orchestrator.baseBrokenStatus).
// Grain re-asks that question by itself where the recorded verdict is
// about nothing -- the tip has moved, or the run errored, was cancelled
// (by grain or by an operator) or was lost with the daemon -- up to
// orchestrator's ciMaximumAttempts (orchestrator.baseCIRetryDue). Past
// that bound the commit is judged by nothing until somebody merges again,
// and this is how a person asks again without waiting for a merge.
//
// # It asks; it does not start
//
// Nothing here calls ci.Service.Start. The request is written on the
// branch's own row (model.BaseBranchCI.RunRequestedAt) and the
// base-branch reconciler starts the run on its next cycle, which is what
// keeps the one-run-in-flight bound and the line that says what a run is
// made of in the one place that has always had them
// (orchestrator.startBaseBranchCI). A handler that started its own run
// would be a second way past that bound, and the bound is what stopped
// several full suites of this deployment's own `main` stacking on top of
// each other (grain/task-334).
//
// So a request made while a run is already going is not refused: it is
// recorded, and taken up on the cycle after that run concludes. The
// response says which of the two happened, in words, because "nothing
// appears to have happened" is the failure mode of every button that
// hands its work to a reconciler.
//
// # Only a branch grain is already watching
//
// The row is the opt-in, here as in the reconciler: a branch with no
// row is one nothing has ever judged, and enrolling one is a settings
// decision (model.RepoConfig.WatchDefaultBranch) rather than something a
// button in a task's pane should take. A request for a branch with no
// row is a 404 that says so.
//
// The grain-ci capability is deliberately not re-checked. A row exists
// because something granted it once, and the reconciler goes on judging
// such a branch whatever the deployment's default capability set says
// today (reconcileOneBaseBranchCI's own note on that) -- a check here
// would refuse a run for a branch grain would have started one for
// anyway.

// BaseBranchRunRequest is what an operator asks for: a base-branch run on
// one branch of one repository. Branch may be empty where the repository
// has exactly one base branch grain is watching, which is the ordinary
// case and lets a caller that knows the repository but not the branch --
// a task whose own base is the repository's default -- ask anyway.
type BaseBranchRunRequest struct {
	Branch string `json:"branch,omitempty"`
}

// BaseBranchRunRequested is what became of that ask: the row as it stands
// once the request is on it, and a sentence saying when the run starts.
type BaseBranchRunRequested struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// Commit is the commit the run will judge -- the one the row is
	// about, which is the tip as grain last read it. The reconciler
	// starts the run against the branch's tip at the moment it takes the
	// request up, so a branch that moves in between is judged where it
	// then is, which is the answer that was wanted either way.
	Commit string `json:"commit,omitempty"`
	// RequestedAt and RequestedBy are the request as recorded on the row.
	RequestedAt time.Time `json:"requestedAt"`
	RequestedBy string    `json:"requestedBy,omitempty"`
	// RunInFlight is true when this branch already has a run with no
	// verdict, which is the one case where the answer is "not yet": one
	// run judges a branch at a time, so the requested one starts after
	// that one concludes.
	RunInFlight bool `json:"runInFlight"`
	// Detail is the whole of it in a sentence, for a pane that has room
	// for one and for an operator who should not have to read two
	// booleans to find out whether their click did anything.
	Detail string `json:"detail"`
}

// RequestBaseBranchRun records an operator's ask for a base-branch run on
// repo's branch. See this file's own header for why it records rather
// than starts.
func (c *Client) RequestBaseBranchRun(ctx context.Context, repo string,
	req BaseBranchRunRequest) (BaseBranchRunRequested, error) {

	parsed, err := c.parseRepo(repo)
	if err != nil {
		return BaseBranchRunRequested{}, validationErrorf("repo: %v", err)
	}
	// Only a system admin or one of the repository's admins may ask
	// (requireRepoPerson): anybody else is refused with a 403 before the
	// row is read or anything is recorded.
	if err := c.requireRepoPerson(ctx, parsed); err != nil {
		return BaseBranchRunRequested{}, err
	}
	// A repository whose base-branch check this deployment does not run
	// is refused out loud rather than recorded: the reconciler would
	// clear the request unanswered (orchestrator.standDownBaseBranchCI),
	// and a button that does nothing visible is what grain/task-509 was
	// about. The switch is on the same page.
	cfg, err := c.Store.GetRepoConfig(ctx, parsed)
	if err != nil {
		return BaseBranchRunRequested{}, err
	}
	if baseCheckSwitchedOff(cfg, parsed) {
		return BaseBranchRunRequested{}, validationErrorf("%s, and its base-branch check is part of it: "+
			"turn the repository's standing work, or its base-ci item, back on to run it",
			model.StandingWorkOffReason(parsed))
	}
	branch, err := c.baseBranchToRun(ctx, parsed, strings.TrimSpace(req.Branch))
	if err != nil {
		return BaseBranchRunRequested{}, err
	}
	now := c.now()
	row, err := c.Store.RequestBaseBranchCIRun(ctx, parsed, branch, now, c.requestActor(ctx).Actor.ID)
	if err != nil {
		return BaseBranchRunRequested{}, err
	}
	if row == nil {
		return BaseBranchRunRequested{}, baseBranchNotWatched(parsed, branch)
	}
	out := BaseBranchRunRequested{
		Repo: parsed.String(), Branch: row.Branch, Commit: row.Commit,
		RequestedAt: now, RequestedBy: row.RunRequestedBy,
		RunInFlight: row.RunID != "" && row.Conclusion == "",
	}
	out.Detail = baseBranchRunRequestedDetail(out)
	return out, nil
}

// baseBranchToRun settles which branch a request is about: the one it
// named, or -- where it named none -- the single base branch grain is
// watching for this repository.
//
// Naming none is not sloppiness. The surface this button is on most is a
// queued change's own row, where what is known is the repository and the
// task's base, and a task's base is empty for the common case of "this
// repository's default branch" (ui.Task.Base). A repository with several
// watched base branches -- a release train beside `main` -- gets the
// question back rather than a guess, naming the branches so the caller
// can ask again.
func (c *Client) baseBranchToRun(ctx context.Context, repo model.RepoRef, branch string) (string, error) {
	if branch != "" {
		return branch, nil
	}
	watched, err := c.watchedBaseBranches(ctx, repo)
	if err != nil {
		return "", err
	}
	switch len(watched) {
	case 0:
		return "", baseBranchNotWatched(repo, "")
	case 1:
		return watched[0], nil
	default:
		return "", validationErrorf("branch: %s has several base branches grain is watching (%s): say which",
			repo, strings.Join(watched, ", "))
	}
}

// watchedBaseBranches is the branches of one repository grain has a
// base-branch row for, in the order the store lists them. It is the set a
// run can be asked for, and what the repository's own pane offers rather
// than a box to type a branch name into.
//
// A task's branch is left out even when it has a row: it is never a base
// (grain/task-949), and the orchestrator clears such a row on its next
// cycle.
func (c *Client) watchedBaseBranches(ctx context.Context, repo model.RepoRef) ([]string, error) {
	rows, err := c.Store.ListBaseBranchCI(ctx)
	if err != nil {
		return nil, err
	}
	branches := []string{}
	for _, row := range rows {
		if row.Repo != repo {
			continue
		}
		isTask, err := c.Store.IsTaskBranch(ctx, row.Branch)
		if err != nil {
			return nil, err
		}
		if !isTask {
			branches = append(branches, row.Branch)
		}
	}
	return branches, nil
}

// baseBranchNotWatched is the 404 for a branch grain has no row for: a
// branch nothing has ever judged, which a run started from here would not
// change (the row is what the reconciler walks). It names the way in,
// since "not found" about a branch that plainly exists is otherwise a
// puzzle.
func baseBranchNotWatched(repo model.RepoRef, branch string) error {
	where := repo.String()
	if branch != "" {
		where += " " + branch
	}
	return &NotFoundError{message: fmt.Sprintf(
		"grain has no base-branch record for %s, so there is nothing here to re-run: a branch is "+
			"watched once grain has merged into it, or once %q is ticked for this repository",
		where, "watch the default branch")}
}

// baseBranchRunRequestedDetail says what happens next, in the words the
// pane shows: a reconciler picks this up, so the honest answer is "on the
// next cycle" and not "started".
func baseBranchRunRequestedDetail(req BaseBranchRunRequested) string {
	at := ""
	if req.Commit != "" {
		at = " at " + shortCommit(req.Commit)
	}
	if req.RunInFlight {
		return fmt.Sprintf("A base-branch run is already judging %s %s%s. One run judges a branch at "+
			"a time, so the run you asked for starts once that one has a verdict.",
			req.Repo, req.Branch, at)
	}
	return fmt.Sprintf("grain will start a base-branch run for %s %s%s on its next cycle, and the "+
		"branch's row is replaced by that run's own.", req.Repo, req.Branch, at)
}

func (s *Server) handleRequestBaseBranchRun(w http.ResponseWriter, r *http.Request) {
	// Authorized before the body is read, so a reader who may not ask
	// gets the 403 whatever they sent; RequestBaseBranchRun checks again
	// for its other callers.
	repo, err := s.parseRepoPath(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if err := s.tasks.requireRepoPerson(r.Context(), repo); err != nil {
		writeClientError(w, err)
		return
	}
	var req BaseBranchRunRequest
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.RequestBaseBranchRun(r.Context(), repo.String(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	// Accepted rather than OK, and for once literally: nothing has been
	// started here, and the run this asks for belongs to the next
	// base-branch cycle.
	writeJSON(w, http.StatusAccepted, out)
}
