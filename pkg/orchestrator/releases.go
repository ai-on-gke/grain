package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// SyncReleases performs the GitHub-side effect every release or release
// candidate still declares but has not yet had carried out --
// bwsalmon/agents#571's create/cut/promote/request-merge, the same
// declarative-intent-then-reconcile split SyncPullRequests already holds
// pkg/ui to: ui.Client's CreateRelease, CutCandidate, PromoteCandidate
// and RequestReleaseMerge only ever write a row, and this is what
// actually creates and moves branches, and opens a merge-back pull
// request, once per cycle, until each reports success.
//
// Releases are synced before candidates: CreateRelease already cuts a
// release's own first candidate (status cutting) in the same transaction
// that inserts the release row (status provisioning), and provisioning a
// release is what creates its own LatestBranch -- the branch that first
// cut needs to exist before it can be created from. Running releases
// first means a release created this very cycle has its first rc live by
// the end of the same cycle, rather than waiting one more tick for
// LatestBranch to exist.
//
// Level-triggered and idempotent like every other reconciler here
// (cycle.go's own doc comment on Reconciler): a release's or a
// candidate's Status only ever advances once its GitHub-side effect is
// confirmed, so a cycle that fails partway through leaves nothing for the
// next cycle to undo, only to retry.
func SyncReleases(ctx context.Context, store *model.Store, client github.Client, now time.Time) error {
	var errs []error

	releases, err := store.PendingReleases(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading pending releases: %w", err)
	}
	for _, r := range releases {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			return errors.Join(errs...)
		}
		if err := syncRelease(ctx, store, client, r, now); err != nil {
			errs = append(errs, err)
		}
	}

	candidates, err := store.PendingCandidates(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("orchestrator: reading pending release candidates: %w", err))
		return errors.Join(errs...)
	}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := syncCandidate(ctx, store, client, c, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// syncRelease advances one release by exactly the step its own Status
// names. A GitHub call failing here is recorded onto the release's own
// LastError and reported as nil, not returned -- syncCandidate's own doc
// comment on why a release- or candidate-specific failure must not cost
// every other pending one this cycle applies equally here.
func syncRelease(ctx context.Context, store *model.Store, client github.Client, r model.Release, now time.Time) error {
	if !model.ValidReleaseName(r.Name) {
		return refuseUnbranchableRelease(ctx, store, r)
	}
	switch r.Status {
	case model.ReleaseProvisioning:
		return provisionOnGitHub(ctx, store, client, r)
	case model.ReleaseMergeRequested:
		return requestMergeOnGitHub(ctx, store, client, r, now)
	default:
		return nil
	}
}

// provisionOnGitHub is the issue's own "create a new release branch":
// r's own LatestBranch is created at the repo's current default branch
// tip. The release's own first candidate, already recorded by
// CreateRelease, is left for the ordinary pending-candidate loop below to
// cut from it -- syncing releases before candidates (SyncReleases' own
// doc comment) is what lets that happen in this same cycle rather than
// the next one.
func provisionOnGitHub(ctx context.Context, store *model.Store, client github.Client, r model.Release) error {
	owner, repo := r.Repo.Owner, r.Repo.Name

	def, err := client.DefaultBranch(owner, repo)
	if err != nil {
		return recordReleaseError(ctx, store, r.ID, fmt.Errorf("reading default branch: %w", err))
	}
	head, err := client.GetBranchHead(owner, repo, def)
	if err != nil {
		return recordReleaseError(ctx, store, r.ID, fmt.Errorf("reading default branch %q: %w", def, err))
	}
	if head == nil {
		return recordReleaseError(ctx, store, r.ID, fmt.Errorf("default branch %q does not exist", def))
	}
	if prodIsDefaultBranch(r, def) {
		if msg := prodIsDefaultBranchError(r, def); r.LastError != msg {
			return recordReleaseError(ctx, store, r.ID, errors.New(msg))
		}
		return nil
	}
	// The release's branches are named after it, so either may already
	// exist as somebody else's branch -- a release called "candidate" or
	// "stable" -- and this provisioning (LatestBranch) or a later
	// promotion (ProdBranch) would force-move it. Refused only when that
	// would drop commits: a retried provisioning's own LatestBranch, or a
	// reused name's old prod branch already merged back, sit behind the
	// default branch and lose nothing.
	for _, branch := range []string{r.ProdBranch(), r.LatestBranch()} {
		ahead, err := commitsOnlyOn(client, owner, repo, branch, def)
		if err != nil {
			return recordReleaseError(ctx, store, r.ID, fmt.Errorf("checking existing branch %q against %q: %w", branch, def, err))
		}
		if len(ahead) > 0 {
			if msg := branchInTheWayError(r, branch, def, ahead); r.LastError != msg {
				return recordReleaseError(ctx, store, r.ID, errors.New(msg))
			}
			return nil
		}
	}
	if err := setBranch(client, owner, repo, r.LatestBranch(), head.SHA); err != nil {
		return recordReleaseError(ctx, store, r.ID, fmt.Errorf("creating latest branch %q: %w", r.LatestBranch(), err))
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		return fmt.Errorf("orchestrator: marking release %d provisioned: %w", r.ID, err)
	}
	return nil
}

// requestMergeOnGitHub is the issue's own "the prod branch can be merged
// back into the default branch when ready": it opens (or, on a retried
// cycle, finds) a pull request from r's own ProdBranch into the repo's
// current default branch, and records that pull request's URL.
//
// Nothing here waits for that pull request to actually be merged on
// GitHub -- Release's own doc comment on why ReleaseMerged is terminal --
// so this reconciler's job ends the moment the pull request exists.
//
// Unless there is no pull request to open. A prod branch that carries no
// commits the default branch does not already have is refused by GitHub
// with a 422, forever (github.IsNoCommitsBetween), and every other
// failure here is recorded onto LastError for the next cycle to retry --
// which for this one means retrying a call that can never succeed, on
// every cycle, with nothing to cap it the way a task's own failure streak
// caps a task. So it is recognised instead, and settled:
// nothingToMergeBack. Two ways, the same two EnsurePullRequest reads it:
// the compare below, before asking, and the refusal itself, for the cycle
// where the compare could not be read or the branches moved in between.
func requestMergeOnGitHub(ctx context.Context, store *model.Store, client github.Client, r model.Release, now time.Time) error {
	owner, repo := r.Repo.Owner, r.Repo.Name

	def, err := client.DefaultBranch(owner, repo)
	if err != nil {
		return recordReleaseError(ctx, store, r.ID, fmt.Errorf("reading default branch: %w", err))
	}

	pr, err := client.FindOpenPullRequestForBranch(owner, repo, r.ProdBranch())
	if err != nil {
		return recordReleaseError(ctx, store, r.ID, fmt.Errorf("checking for an existing merge pull request: %w", err))
	}
	if pr == nil {
		// known false -- no compare to be had, or a read that failed --
		// is not an empty branch: the pull request is attempted anyway,
		// and the 422 below is the second line of defence. See
		// branchCommits' own doc comment on why the two are kept apart.
		commits, known := branchCommits(client, r.Repo, def, r.ProdBranch())
		if known && len(commits) == 0 {
			return nothingToMergeBack(ctx, store, r, def, now)
		}
		title := fmt.Sprintf("Merge release %s into %s", r.Name, def)
		body := fmt.Sprintf("Promotes release %q (branch %q) back into %s.", r.Name, r.ProdBranch(), def)
		created, err := client.CreatePullRequest(owner, repo, r.ProdBranch(), def, title, body)
		if err != nil && github.IsNoCommitsBetween(err) {
			return nothingToMergeBack(ctx, store, r, def, now)
		}
		if err != nil {
			return recordReleaseError(ctx, store, r.ID, fmt.Errorf("opening merge pull request: %w", err))
		}
		pr = &created
	}

	if err := store.MarkReleaseMerged(ctx, r.ID, pr.HTMLURL, now); err != nil {
		return fmt.Errorf("orchestrator: marking release %d merged: %w", r.ID, err)
	}
	return nil
}

// nothingToMergeBack settles a release whose ProdBranch adds nothing to
// def: there is no pull request to open, and there never will be, so the
// release is recorded as merged with a note saying why it has no pull
// request of its own rather than left in merge_requested for the next
// cycle to ask GitHub the same refused question again.
//
// Merged, and not an error, because that is what actually happened: the
// release's commits are already on the default branch, which is the whole
// of what merging back was for. A release cut from the default branch and
// never merged into is exactly this shape, and so is one whose commits
// reached the default branch some other way -- a hotfix cherry-picked, or
// the prod branch merged by hand -- and none of those is a fault anyone
// needs to fix. See Store.MarkReleaseNothingToMerge on why the name is
// freed rather than held by a terminal error status.
func nothingToMergeBack(ctx context.Context, store *model.Store, r model.Release, def string, now time.Time) error {
	log.Printf("orchestrator: release %s of %s has nothing to merge back: %s carries no commits %q "+
		"does not already have, so no merge pull request can be opened for it",
		r.Name, r.Repo, r.ProdBranch(), def)

	note := fmt.Sprintf("%s carried no commits %s did not already have, so GitHub had no pull request "+
		"to open -- everything on this release is already on %s, and it is recorded as merged.",
		r.ProdBranch(), def, def)
	if err := store.MarkReleaseNothingToMerge(ctx, r.ID, note, now); err != nil {
		return fmt.Errorf("orchestrator: marking release %d as having nothing to merge: %w", r.ID, err)
	}
	return nil
}

// syncCandidate advances one candidate by exactly the step its own
// Status names. A GitHub call failing here is recorded onto the
// candidate's own LastError and reported as nil, not returned: it is
// candidate-specific and, per Reconciler's own doc comment, must not cost
// every other pending candidate this cycle -- the next cycle simply tries
// the same step again. Only a store failure -- reading or writing the
// record of what to do next -- is a cycle-level error worth returning.
func syncCandidate(ctx context.Context, store *model.Store, client github.Client, c model.Candidate, now time.Time) error {
	release, err := store.GetReleaseByID(ctx, c.ReleaseID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading release %d for candidate %d: %w", c.ReleaseID, c.ID, err)
	}
	if release == nil {
		// The release this candidate belongs to is gone. Nothing to retry
		// towards -- record it and wait for a human to notice.
		return recordCandidateError(ctx, store, c.ID, fmt.Errorf("release %d no longer exists", c.ReleaseID))
	}
	if !model.ValidReleaseName(release.Name) {
		// refuseUnbranchableRelease's own reasoning: the candidate's branch
		// is derived from the same name, so GitHub can never create it.
		if msg := unbranchableReleaseError(*release); c.LastError != msg {
			return recordCandidateError(ctx, store, c.ID, errors.New(msg))
		}
		return nil
	}

	switch c.Status {
	case model.CandidateCutting:
		// Not before provisioning has made LatestBranch the release's own:
		// until then a branch by that name is somebody else's, which
		// provisioning may be refusing to move, and cutting from it would
		// carry that branch's commits into the release.
		if release.Status == model.ReleaseProvisioning {
			return nil
		}
		return cutOnGitHub(ctx, store, client, *release, c)
	case model.CandidatePromoting:
		return promoteOnGitHub(ctx, store, client, *release, c, now)
	default:
		return nil
	}
}

// cutOnGitHub is the issue's own "creates an rc branch with the
// appropriate name": c.Branch (release.Name + ".rc." + c.Number) is
// created at release's own LatestBranch's current tip -- the issue's own
// "All agent commits go here," so a fresh cut always starts from
// whatever has landed on LatestBranch since the previous one.
func cutOnGitHub(ctx context.Context, store *model.Store, client github.Client, release model.Release, c model.Candidate) error {
	owner, repo := c.Repo.Owner, c.Repo.Name

	head, err := client.GetBranchHead(owner, repo, release.LatestBranch())
	if err != nil {
		return recordCandidateError(ctx, store, c.ID, fmt.Errorf("reading latest branch %q: %w", release.LatestBranch(), err))
	}
	if head == nil {
		return recordCandidateError(ctx, store, c.ID, fmt.Errorf("latest branch %q does not exist", release.LatestBranch()))
	}

	exists, err := client.BranchExists(owner, repo, c.Branch)
	if err != nil {
		return recordCandidateError(ctx, store, c.ID, fmt.Errorf("checking candidate branch %q: %w", c.Branch, err))
	}
	if !exists {
		if err := client.CreateBranch(owner, repo, c.Branch, head.SHA); err != nil {
			return recordCandidateError(ctx, store, c.ID, fmt.Errorf("creating candidate branch %q: %w", c.Branch, err))
		}
	}

	if err := store.MarkCandidateCut(ctx, c.ID); err != nil {
		return fmt.Errorf("orchestrator: marking candidate %d cut: %w", c.ID, err)
	}
	return nil
}

// promoteOnGitHub is the issue's own "when an rc is sufficiently tested
// it can be promoted": release's own ProdBranch is moved to c.Branch's
// own tip. There is no separate permanent branch created here the way
// bwsalmon/agents#398 once cut one at promotion time -- ProdBranch
// (release.Name, unadorned) is the only branch a promotion touches.
func promoteOnGitHub(ctx context.Context, store *model.Store, client github.Client, release model.Release, c model.Candidate, now time.Time) error {
	owner, repo := c.Repo.Owner, c.Repo.Name

	head, err := client.GetBranchHead(owner, repo, c.Branch)
	if err != nil {
		return recordCandidateError(ctx, store, c.ID, fmt.Errorf("reading candidate branch %q: %w", c.Branch, err))
	}
	if head == nil {
		return recordCandidateError(ctx, store, c.ID, fmt.Errorf("candidate branch %q does not exist", c.Branch))
	}

	// setBranch force-moves an existing branch, so a release whose prod
	// branch is the repo's default branch would have this promotion reset
	// the default branch to the candidate, dropping whatever landed there
	// since the release was cut. Provisioning refuses such a release now,
	// but one provisioned before it did still reaches here.
	def, err := client.DefaultBranch(owner, repo)
	if err != nil {
		return recordCandidateError(ctx, store, c.ID, fmt.Errorf("reading default branch: %w", err))
	}
	if prodIsDefaultBranch(release, def) {
		if msg := prodIsDefaultBranchError(release, def); c.LastError != msg {
			return recordCandidateError(ctx, store, c.ID, errors.New(msg))
		}
		return nil
	}
	// The same force-move drops whatever the prod branch carries that the
	// candidate does not, which is what a branch the release's name
	// collided with does -- one provisioning predates refusing, or one
	// created since. Only before this release's own first promotion:
	// after it, the prod branch is the release's own, and a later
	// candidate cut from LatestBranch may rightly lack a commit pushed
	// straight onto the rc it last promoted.
	promoted, err := releaseHasPromoted(ctx, store, release)
	if err != nil {
		return err
	}
	if !promoted {
		ahead, err := commitsOnlyOn(client, owner, repo, release.ProdBranch(), c.Branch)
		if err != nil {
			return recordCandidateError(ctx, store, c.ID, fmt.Errorf("checking prod branch %q against %q: %w", release.ProdBranch(), c.Branch, err))
		}
		if len(ahead) > 0 {
			if msg := prodAheadOfCandidateError(release, c, ahead); c.LastError != msg {
				return recordCandidateError(ctx, store, c.ID, errors.New(msg))
			}
			return nil
		}
	}

	if err := setBranch(client, owner, repo, release.ProdBranch(), head.SHA); err != nil {
		return recordCandidateError(ctx, store, c.ID, fmt.Errorf("promoting to prod branch %q: %w", release.ProdBranch(), err))
	}

	if err := store.MarkCandidatePromoted(ctx, c.ID, now); err != nil {
		return fmt.Errorf("orchestrator: marking candidate %d promoted: %w", c.ID, err)
	}
	return nil
}

// setBranch creates branch at sha if it does not exist yet, or force-moves
// it there if it does -- shared by a release's own LatestBranch
// provisioning and a candidate's own promotion, each of which may find
// its target branch either freshly minted or already real.
func setBranch(client github.Client, owner, repo, branch, sha string) error {
	exists, err := client.BranchExists(owner, repo, branch)
	if err != nil {
		return err
	}
	if exists {
		return client.UpdateBranch(owner, repo, branch, sha, true)
	}
	return client.CreateBranch(owner, repo, branch, sha)
}

// prodIsDefaultBranch is whether r's own ProdBranch -- its bare name --
// is the repo's default branch: a release called "main" on a repo whose
// default is main. Compared without case, since GitHub resolves a branch
// name that way in the calls setBranch makes.
func prodIsDefaultBranch(r model.Release, defaultBranch string) bool {
	return defaultBranch != "" && strings.EqualFold(r.ProdBranch(), defaultBranch)
}

// prodIsDefaultBranchError is what such a release, or its candidate, records
// as its LastError instead of touching the default branch.
func prodIsDefaultBranchError(r model.Release, defaultBranch string) string {
	return fmt.Sprintf("release %q would use the default branch %q as its prod branch, and promoting it would "+
		"force-move %q to the candidate; nothing is done for it -- delete it and create the release under another name",
		r.Name, defaultBranch, defaultBranch)
}

// commitsOnlyOn returns the commits branch carries that onto does not --
// what force-moving branch to onto would drop -- or none when branch does
// not exist. GitHub caps the list at 250 commits, which is plenty for
// telling none from some.
func commitsOnlyOn(client github.Client, owner, repo, branch, onto string) ([]github.Commit, error) {
	exists, err := client.BranchExists(owner, repo, branch)
	if err != nil || !exists {
		return nil, err
	}
	return client.CompareCommits(owner, repo, onto, branch)
}

// releaseHasPromoted is whether any candidate of r has been promoted --
// whether r's ProdBranch is a branch r itself put there.
func releaseHasPromoted(ctx context.Context, store *model.Store, r model.Release) (bool, error) {
	candidates, err := store.ListCandidates(ctx, r.Repo, r.Name)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading candidates of release %d: %w", r.ID, err)
	}
	for _, c := range candidates {
		if c.ReleaseID == r.ID && c.Status == model.CandidatePromoted {
			return true, nil
		}
	}
	return false, nil
}

// branchInTheWayError is what a release records when one of its own
// branch names is already taken by a branch carrying commits def lacks.
func branchInTheWayError(r model.Release, branch, def string, ahead []github.Commit) string {
	return fmt.Sprintf("branch %q already exists and carries %s %q does not (newest %s); release %q would "+
		"force-move it and drop them, so nothing is done for it -- create the release under another name, or "+
		"delete or merge %q if it is a leftover",
		branch, commitCount(ahead), def, shortSHA(ahead[len(ahead)-1].SHA), r.Name, branch)
}

// prodAheadOfCandidateError is what a candidate records when promoting it
// would drop commits only the prod branch has.
func prodAheadOfCandidateError(r model.Release, c model.Candidate, ahead []github.Commit) string {
	return fmt.Sprintf("prod branch %q carries %s candidate %q does not (newest %s), and promoting would "+
		"force-move it and drop them; this release has never promoted to it, so it is not the release's own -- "+
		"nothing is done: delete or merge %q if it is a leftover, or create the release under another name",
		r.ProdBranch(), commitCount(ahead), c.Branch, shortSHA(ahead[len(ahead)-1].SHA), r.ProdBranch())
}

func commitCount(commits []github.Commit) string {
	if len(commits) == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", len(commits))
}

// unbranchableReleaseError is what a release whose name git refuses as a
// branch, and each of its candidates, records as its LastError.
func unbranchableReleaseError(r model.Release) string {
	return fmt.Sprintf("release name %q is not a branch name git accepts, so none of its branches can ever be "+
		"created; it was stored before grain refused such names, and nothing more is attempted for it", r.Name)
}

// refuseUnbranchableRelease settles a release stored before
// model.ValidReleaseName refused names such as "v3.0..x": every branch it
// would ever create -- ProdBranch, LatestBranch, each RCBranch -- is
// derived from that name, and GitHub refuses all of them, so asking
// again every cycle only spends API calls on a 422 that can never
// change. It records why on the release once, logs it once, and makes
// no GitHub call at all.
func refuseUnbranchableRelease(ctx context.Context, store *model.Store, r model.Release) error {
	msg := unbranchableReleaseError(r)
	if r.LastError == msg {
		return nil
	}
	log.Printf("orchestrator: release %s of %s: %s", r.Name, r.Repo, msg)
	return recordReleaseError(ctx, store, r.ID, errors.New(msg))
}

// recordReleaseError saves err onto the release's own LastError for a UI
// to surface, and reports success to its own caller -- syncRelease's own
// doc comment on why a release-specific failure must not read as a
// cycle-level one.
func recordReleaseError(ctx context.Context, store *model.Store, id int64, err error) error {
	if markErr := store.MarkReleaseError(ctx, id, err.Error()); markErr != nil {
		return fmt.Errorf("orchestrator: recording error on release %d (%v): %w", id, err, markErr)
	}
	return nil
}

// recordCandidateError saves err onto the candidate's own LastError for a
// UI to surface, and reports success to its own caller -- see
// syncCandidate's own doc comment on why a candidate-specific failure
// must not cost every other pending candidate this cycle.
func recordCandidateError(ctx context.Context, store *model.Store, id int64, err error) error {
	if markErr := store.MarkCandidateError(ctx, id, err.Error()); markErr != nil {
		return fmt.Errorf("orchestrator: recording error on candidate %d (%v): %w", id, err, markErr)
	}
	return nil
}
