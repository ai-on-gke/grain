package orchestrator_test

// SyncReleases' own tests (bwsalmon/agents#571), against a real bare git
// repo behind githubsim.Sim the same way sync_test.go tests
// SyncPullRequests -- release management performs real git operations
// (creating and moving branches), so a FakeTransport standing in for
// GitHub would only prove this package's own request-building, not that
// the branch it asked GitHub to create actually exists afterwards.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func mustHead(t *testing.T, client *github.RESTClient, owner, repo, branch string) string {
	t.Helper()
	head, err := client.GetBranchHead(owner, repo, branch)
	if err != nil {
		t.Fatalf("reading %s: %v", branch, err)
	}
	if head == nil {
		t.Fatalf("branch %q does not exist", branch)
	}
	return head.SHA
}

func TestSyncReleasesProvisionsLatestBranchAndCutsTheFirstCandidate(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	release, err := store.CreateRelease(ctx, repo, "myfeat", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}

	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncReleases: %v", err)
	}

	mainHead := mustHead(t, client, "acme", "widgets", "main")
	if got := mustHead(t, client, "acme", "widgets", "myfeat.latest"); got != mainHead {
		t.Fatalf("got latest branch tip %q, want it pinned at main's own tip %q", got, mainHead)
	}
	if got := mustHead(t, client, "acme", "widgets", "myfeat.rc.1"); got != mainHead {
		t.Fatalf("got first candidate branch tip %q, want %q", got, mainHead)
	}

	gotRelease, err := store.GetReleaseByID(ctx, release.ID)
	if err != nil || gotRelease == nil {
		t.Fatalf("release: (%+v, %v)", gotRelease, err)
	}
	if gotRelease.Status != model.ReleaseActive {
		t.Fatalf("got release status %q, want active", gotRelease.Status)
	}

	current, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || current == nil {
		t.Fatalf("current candidate: %+v %v", current, err)
	}
	if current.Status != model.CandidateActive {
		t.Fatalf("got candidate status %q, want active", current.Status)
	}
	if current.LastError != "" {
		t.Fatalf("got error %q, want none", current.LastError)
	}
	if len(sim.Calls) == 0 {
		t.Fatal("expected SyncReleases to have called out to GitHub")
	}
}

func TestSyncReleasesIsIdempotentOnceProvisioned(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if _, err := store.CreateRelease(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("first SyncReleases: %v", err)
	}
	// A second run finds nothing pending -- the release already advanced
	// to active and its first candidate to active -- and must not error
	// re-creating branches that already exist.
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("second SyncReleases: %v", err)
	}
}

func TestSyncReleasesRecordsAnErrorWhenTheDefaultBranchIsMissing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if _, err := store.CreateRelease(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("create release: %v", err)
	}

	// DefaultBranch's own endpoint just reports the configured name
	// (githubsim's own doc comment) without checking it actually exists,
	// so deleting the ref out from under it -- something a human
	// force-pushing or renaming a repo's default branch could do for
	// real -- is what reproduces "the default branch does not exist"
	// without giving CreateRelease itself some separate, made-up branch
	// name to misconfigure the way bwsalmon/agents#398's own
	// ReleaseConfig.ProdBranch let a test do.
	run(t, sim.BareRepo, "git", "update-ref", "-d", "refs/heads/main")

	// A release-specific failure must not surface as a cycle error -- see
	// syncRelease's own doc comment on why -- only get recorded onto the
	// row for a UI to show, so the next cycle can simply retry it.
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncReleases: %v", err)
	}

	current, err := store.GetRelease(ctx, repo, "myfeat")
	if err != nil || current == nil {
		t.Fatalf("current release: %+v %v", current, err)
	}
	if current.Status != model.ReleaseProvisioning {
		t.Fatalf("got status %q, want still provisioning", current.Status)
	}
	if current.LastError == "" {
		t.Fatal("expected an error recorded on the release")
	}
}

// A release's prod branch is its bare name, and promotion force-moves it,
// so a release named for the default branch would reset that branch to
// its candidate. Provisioning refuses one before it cuts anything
// (2026-10-11 repository/release walkthrough).
func TestSyncReleasesRefusesToProvisionAReleaseNamedForTheDefaultBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	release, err := store.CreateRelease(ctx, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
			t.Fatalf("SyncReleases: %v", err)
		}
	}

	got, err := store.GetReleaseByID(ctx, release.ID)
	if err != nil || got == nil {
		t.Fatalf("release: (%+v, %v)", got, err)
	}
	if got.Status != model.ReleaseProvisioning {
		t.Fatalf("got release status %q, want it left provisioning", got.Status)
	}
	if !strings.Contains(got.LastError, `default branch "main"`) {
		t.Fatalf("got LastError %q, want it to name the default branch", got.LastError)
	}
	if exists, err := client.BranchExists("acme", "widgets", "main.latest"); err != nil || exists {
		t.Fatalf("main.latest exists=%v (%v), want nothing created", exists, err)
	}
}

// One provisioned before that refusal existed must still never have its
// promotion move the default branch.
func TestSyncReleasesNeverPromotesOntoTheDefaultBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	release, err := store.CreateRelease(ctx, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, release.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	candidate, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || candidate == nil {
		t.Fatalf("candidate: (%+v, %v)", candidate, err)
	}
	pushBranch(t, sim.BareRepo, candidate.Branch)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	// Work lands on the default branch after the candidate was cut: the
	// commit a force-move to the candidate would throw away.
	pushBranch(t, sim.BareRepo, "main")
	mainHead := mustHead(t, client, "acme", "widgets", "main")

	if _, err := store.PromoteCandidate(ctx, repo, "main"); err != nil {
		t.Fatalf("promote candidate: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncReleases: %v", err)
	}

	if got := mustHead(t, client, "acme", "widgets", "main"); got != mainHead {
		t.Fatalf("default branch moved from %q to %q", mainHead, got)
	}
	current, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || current == nil {
		t.Fatalf("current candidate: %+v %v", current, err)
	}
	if current.Status == model.CandidatePromoted {
		t.Fatal("candidate marked promoted, want the promotion refused")
	}
	if !strings.Contains(current.LastError, `default branch "main"`) {
		t.Fatalf("got LastError %q, want it to name the default branch", current.LastError)
	}
}

// A release's branches are named after it, and the prod branch can as
// easily collide with some other existing branch -- a release called
// "stable" or "candidate" -- whose first promotion would reset it to the
// rc and drop whatever only it had (2026-10-12 suite release batch
// walkthrough, finding 2). Provisioning refuses one before it cuts
// anything, and goes ahead once the branch is out of the way.
func TestSyncReleasesRefusesToProvisionOverAnotherBranchsCommits(t *testing.T) {
	t.Parallel()
	for _, branch := range []string{"stable", "stable.latest"} {
		t.Run(branch, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			sim, client := newSim(t, "acme", "widgets", "main")
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}

			// A commit on top of main that no other branch has.
			pushBranch(t, sim.BareRepo, branch)
			own := mustHead(t, client, "acme", "widgets", branch)

			release, err := store.CreateRelease(ctx, repo, "stable", baseTime)
			if err != nil {
				t.Fatalf("create release: %v", err)
			}
			for i := 0; i < 2; i++ {
				if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
					t.Fatalf("SyncReleases: %v", err)
				}
			}

			got, err := store.GetReleaseByID(ctx, release.ID)
			if err != nil || got == nil {
				t.Fatalf("release: (%+v, %v)", got, err)
			}
			if got.Status != model.ReleaseProvisioning {
				t.Fatalf("got release status %q, want it left provisioning", got.Status)
			}
			if !strings.Contains(got.LastError, `branch "`+branch+`" already exists`) {
				t.Fatalf("got LastError %q, want it to name %q", got.LastError, branch)
			}
			if got := mustHead(t, client, "acme", "widgets", branch); got != own {
				t.Fatalf("%s moved from %q to %q", branch, own, got)
			}
			if exists, err := client.BranchExists("acme", "widgets", "stable.rc.1"); err != nil || exists {
				t.Fatalf("stable.rc.1 exists=%v (%v), want nothing cut", exists, err)
			}

			run(t, sim.BareRepo, "git", "update-ref", "-d", "refs/heads/"+branch)
			if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
				t.Fatalf("SyncReleases: %v", err)
			}
			got, err = store.GetReleaseByID(ctx, release.ID)
			if err != nil || got == nil {
				t.Fatalf("release: (%+v, %v)", got, err)
			}
			if got.Status != model.ReleaseActive || got.LastError != "" {
				t.Fatalf("got release %q (%q), want it active once %s is gone", got.Status, got.LastError, branch)
			}
		})
	}
}

// A prod branch that adds nothing to the default branch -- a release name
// reused after the last release under it merged back -- loses nothing to
// a force-move, so it does not hold up a new release under that name.
func TestSyncReleasesReusesAProdBranchAlreadyMergedBack(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	run(t, sim.BareRepo, "git", "branch", "stable", "main")
	pushBranch(t, sim.BareRepo, "main")

	release, err := store.CreateRelease(ctx, repo, "stable", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncReleases: %v", err)
	}
	if _, err := store.PromoteCandidate(ctx, repo, "stable"); err != nil {
		t.Fatalf("promote candidate: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncReleases: %v", err)
	}

	current, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || current == nil {
		t.Fatalf("current candidate: %+v %v", current, err)
	}
	if current.Status != model.CandidatePromoted {
		t.Fatalf("got status %q (%q), want promoted", current.Status, current.LastError)
	}
	if got, want := mustHead(t, client, "acme", "widgets", "stable"), mustHead(t, client, "acme", "widgets", current.Branch); got != want {
		t.Fatalf("got prod tip %q, want the candidate's %q", got, want)
	}
}

// One provisioned before that refusal existed, or whose prod branch was
// created after it provisioned, must still never have its first
// promotion drop that branch's own commits.
func TestSyncReleasesNeverPromotesOverAnotherBranchsCommits(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	release, err := store.CreateRelease(ctx, repo, "stable", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("provisioning SyncReleases: %v", err)
	}
	pushBranch(t, sim.BareRepo, "stable")
	own := mustHead(t, client, "acme", "widgets", "stable")

	if _, err := store.PromoteCandidate(ctx, repo, "stable"); err != nil {
		t.Fatalf("promote candidate: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
			t.Fatalf("SyncReleases: %v", err)
		}
	}

	if got := mustHead(t, client, "acme", "widgets", "stable"); got != own {
		t.Fatalf("stable moved from %q to %q", own, got)
	}
	current, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || current == nil {
		t.Fatalf("current candidate: %+v %v", current, err)
	}
	if current.Status == model.CandidatePromoted {
		t.Fatal("candidate marked promoted, want the promotion refused")
	}
	if !strings.Contains(current.LastError, `prod branch "stable" carries 1 commit`) {
		t.Fatalf("got LastError %q, want it to name the prod branch", current.LastError)
	}
}

// Once a release has promoted, its prod branch is its own: the next
// candidate, cut from LatestBranch, may lack a commit pushed straight
// onto the rc promoted before it, and still promotes as it always has.
func TestSyncReleasesKeepsPromotingOntoItsOwnProdBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	release, err := store.CreateRelease(ctx, repo, "myfeat", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("provisioning SyncReleases: %v", err)
	}
	pushBranch(t, sim.BareRepo, "myfeat.rc.1")
	if _, err := store.PromoteCandidate(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("promote rc.1: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncReleases: %v", err)
	}

	if _, err := store.CutCandidate(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("cut rc.2: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncReleases: %v", err)
	}
	if _, err := store.PromoteCandidate(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("promote rc.2: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncReleases: %v", err)
	}

	current, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || current == nil {
		t.Fatalf("current candidate: %+v %v", current, err)
	}
	if current.Branch != "myfeat.rc.2" || current.Status != model.CandidatePromoted {
		t.Fatalf("got %s %q (%q), want myfeat.rc.2 promoted", current.Branch, current.Status, current.LastError)
	}
	if got, want := mustHead(t, client, "acme", "widgets", "myfeat"), mustHead(t, client, "acme", "widgets", "myfeat.rc.2"); got != want {
		t.Fatalf("got prod tip %q, want rc.2's %q", got, want)
	}
}

func TestSyncReleasesPromotesTheCandidateToTheProdBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	release, err := store.CreateRelease(ctx, repo, "myfeat", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("provisioning SyncReleases: %v", err)
	}
	candidate, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || candidate == nil {
		t.Fatalf("candidate: (%+v, %v)", candidate, err)
	}

	// Push a real commit onto the candidate's own branch, straight to the
	// bare repo -- standing in for whatever lands on an rc branch between
	// cutting it and promoting it -- so promoting is a real, visible
	// fast-forward of the prod branch rather than a no-op moving it to
	// its own tip.
	pushBranch(t, sim.BareRepo, candidate.Branch)

	if _, err := store.PromoteCandidate(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("promote candidate: %v", err)
	}

	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("promoting SyncReleases: %v", err)
	}

	candidateHead := mustHead(t, client, "acme", "widgets", candidate.Branch)
	if got := mustHead(t, client, "acme", "widgets", "myfeat"); got != candidateHead {
		t.Fatalf("got prod tip %q, want it moved to the candidate's own tip %q", got, candidateHead)
	}

	current, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || current == nil {
		t.Fatalf("current candidate: %+v %v", current, err)
	}
	if current.Status != model.CandidatePromoted {
		t.Fatalf("got status %q, want promoted", current.Status)
	}
	if current.PromotedAt == nil {
		t.Fatal("expected PromotedAt to be set")
	}

	// "It cannot be promoted twice."
	if _, err := store.PromoteCandidate(ctx, repo, "myfeat"); err == nil {
		t.Fatal("expected promoting an already-promoted candidate to fail")
	}
}

func TestSyncReleasesOpensAMergeBackPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	release, err := store.CreateRelease(ctx, repo, "myfeat", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("provisioning SyncReleases: %v", err)
	}
	candidate, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || candidate == nil {
		t.Fatalf("candidate: (%+v, %v)", candidate, err)
	}
	pushBranch(t, sim.BareRepo, candidate.Branch)
	if _, err := store.PromoteCandidate(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("promote candidate: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("promoting SyncReleases: %v", err)
	}

	if _, err := store.RequestReleaseMerge(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("request merge: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("merging SyncReleases: %v", err)
	}

	current, err := store.GetRelease(ctx, repo, "myfeat")
	if err != nil || current == nil {
		t.Fatalf("current release: %+v %v", current, err)
	}
	if current.Status != model.ReleaseMerged {
		t.Fatalf("got status %q, want merged", current.Status)
	}
	if current.PullRequestURL == "" {
		t.Fatal("expected a pull request URL to be recorded")
	}
	if current.MergedAt == nil {
		t.Fatal("expected MergedAt to be set")
	}

	// Once merged, the name is free again.
	if _, err := store.CreateRelease(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("expected the release name to be reusable once merged: %v", err)
	}
}

// A release whose prod branch carries nothing the default branch does not
// already have -- one cut from the default branch and never merged into,
// or one whose commits reached the default branch some other way -- gets
// GitHub's permanent "No commits between ..." 422 for its merge-back pull
// request. Recording that onto LastError left the release in
// merge_requested, so the next cycle asked the same refused question, and
// the one after that, with nothing to cap it the way a task's own failure
// streak caps a task. It settles instead.
func TestSyncReleasesSettlesAReleaseWithNothingToMergeBack(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if _, err := store.CreateRelease(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("provisioning SyncReleases: %v", err)
	}
	// No pushBranch: nothing lands on the candidate between cutting it
	// and promoting it, so the prod branch this promotion creates sits at
	// exactly main's own tip -- the shape of a release nobody committed
	// to, and of one whose commits reached main some other way.
	if _, err := store.PromoteCandidate(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("promote candidate: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("promoting SyncReleases: %v", err)
	}
	if got := mustHead(t, client, "acme", "widgets", "myfeat"); got != mustHead(t, client, "acme", "widgets", "main") {
		t.Fatalf("expected the prod branch to sit at main's own tip, got %q", got)
	}

	if _, err := store.RequestReleaseMerge(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("request merge: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("merging SyncReleases: %v", err)
	}

	current, err := store.GetRelease(ctx, repo, "myfeat")
	if err != nil || current == nil {
		t.Fatalf("current release: %+v %v", current, err)
	}
	if current.Status != model.ReleaseMerged {
		t.Fatalf("got status %q, want merged -- a release with nothing to merge back is as merged as it gets", current.Status)
	}
	if current.PullRequestURL != "" {
		t.Fatalf("got pull request URL %q, want none: GitHub opens no pull request for this", current.PullRequestURL)
	}
	if current.MergeNote == "" {
		t.Fatal("expected a note saying why there is no pull request, so a human need not translate a 422")
	}
	if current.LastError != "" {
		t.Fatalf("got error %q, want none: nothing about this release failed", current.LastError)
	}
	if current.MergedAt == nil {
		t.Fatal("expected MergedAt to be set")
	}
	if len(sim.PullRequests) != 0 {
		t.Fatalf("got %d pull requests, want none opened", len(sim.PullRequests))
	}
	// Not even asked for: reading the compare first is what turns this
	// into an answer rather than a refusal to interpret.
	for _, c := range sim.Calls {
		if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/pulls") {
			t.Fatal("asked GitHub to open a pull request it can only refuse, want the compare read first")
		}
	}

	// The retry this ending exists to stop: nothing is pending any more,
	// so a later cycle asks GitHub nothing at all about this release.
	before := len(sim.Calls)
	if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
		t.Fatalf("second merging SyncReleases: %v", err)
	}
	if got := len(sim.Calls) - before; got != 0 {
		t.Fatalf("the next cycle made %d GitHub call(s) for a settled release, want none", got)
	}

	// And, as with any other merged release, the name is free again.
	if _, err := store.CreateRelease(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("expected the release name to be reusable: %v", err)
	}
}

// The same ending without the compare: a client that cannot read the
// compare endpoint (blindCompareClient) asks GitHub for the pull request
// and is refused, and that refusal has to be read as the same condition.
// Otherwise the loop this fixes comes back on every cycle where the
// compare happens to fail.
func TestSyncReleasesSettlesFromGitHubsOwnRefusalToo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	blind := blindCompareClient{Client: client}

	if _, err := store.CreateRelease(ctx, repo, "myfeat", baseTime); err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, blind, baseTime); err != nil {
		t.Fatalf("provisioning SyncReleases: %v", err)
	}
	if _, err := store.PromoteCandidate(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("promote candidate: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, blind, baseTime); err != nil {
		t.Fatalf("promoting SyncReleases: %v", err)
	}
	if _, err := store.RequestReleaseMerge(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("request merge: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, blind, baseTime); err != nil {
		t.Fatalf("merging SyncReleases: %v", err)
	}

	current, err := store.GetRelease(ctx, repo, "myfeat")
	if err != nil || current == nil {
		t.Fatalf("current release: %+v %v", current, err)
	}
	if current.Status != model.ReleaseMerged {
		t.Fatalf("got status %q, want merged: GitHub's own 422 went unrecognised", current.Status)
	}
	if current.MergeNote == "" || current.LastError != "" {
		t.Fatalf("got note %q and error %q, want the note alone", current.MergeNote, current.LastError)
	}
	if len(sim.PullRequests) != 0 {
		t.Fatalf("got %d pull requests, want none opened", len(sim.PullRequests))
	}
}

// The guard on both of those: a compare that cannot be read must not by
// itself settle a release. A prod branch that really is ahead has its
// merge-back pull request opened exactly as before, whatever the compare
// endpoint says -- reading a failed read as "nothing to merge" would
// quietly retire releases over an API hiccup.
func TestSyncReleasesStillOpensAMergePullRequestWhenTheCompareCannotBeRead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	blind := blindCompareClient{Client: client}

	release, err := store.CreateRelease(ctx, repo, "myfeat", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, blind, baseTime); err != nil {
		t.Fatalf("provisioning SyncReleases: %v", err)
	}
	candidate, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || candidate == nil {
		t.Fatalf("candidate: (%+v, %v)", candidate, err)
	}
	pushBranch(t, sim.BareRepo, candidate.Branch)
	if _, err := store.PromoteCandidate(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("promote candidate: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, blind, baseTime); err != nil {
		t.Fatalf("promoting SyncReleases: %v", err)
	}
	if _, err := store.RequestReleaseMerge(ctx, repo, "myfeat"); err != nil {
		t.Fatalf("request merge: %v", err)
	}
	if err := orchestrator.SyncReleases(ctx, store, blind, baseTime); err != nil {
		t.Fatalf("merging SyncReleases: %v", err)
	}

	current, err := store.GetRelease(ctx, repo, "myfeat")
	if err != nil || current == nil {
		t.Fatalf("current release: %+v %v", current, err)
	}
	if current.PullRequestURL == "" {
		t.Fatalf("got no pull request URL, want the one this merge opened (note %q, error %q)",
			current.MergeNote, current.LastError)
	}
	if current.MergeNote != "" {
		t.Fatalf("got note %q, want none: this release had something to merge back", current.MergeNote)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("got %d pull requests, want the one this merge opened", len(sim.PullRequests))
	}
}
