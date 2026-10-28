package cli

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Which of a repository's jobs grain may dispatch to fill a build input
// its own CI then reads (ciCacheFillingJobs).
//
// There are two places that ask -- the post-merge dispatch on a base
// branch (orchestrator.startDueBaseBranchDispatch) and the single-flight
// warm on a branch whose guest key nothing has published
// (ci.GuestWarmer, grain/task-410) -- and the thing worth pinning is
// that they get the same answer. A deployment whose base branch is
// warmed and whose branches are not would be the one repository's cache
// following `main` and every branch still building a guest inline,
// which is most of the cost the default was added to remove.
func TestCacheFillingJobsIncludeTheOneGrainAddsForItself(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	const ourGuest = "ghcr.io/bwsalmon/grain/guest:latest"

	// Releases off and nothing named: the state pressing one button
	// leaves behind, and the branch warm has to know about it too.
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, CICapabilities: []string{model.CapabilityPackagePublish},
		Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	jobs := daemon.CiCacheFillingJobs(store, ourGuest)
	if got := jobs("bwsalmon/grain"); !slices.Equal(got, []string{model.SandboxGuestCacheJob}) {
		t.Fatalf("the branch warm may dispatch %v, want %s: the base branch and the branches have "+
			"to agree about one repository, or the cache follows main and nothing else",
			got, model.SandboxGuestCacheJob)
	}

	// And a deployment running somebody else's guest reads the stored
	// list and nothing more, which is every other deployment.
	elsewhere := daemon.CiCacheFillingJobs(store, "ghcr.io/other/project/guest:latest")
	if got := elsewhere("bwsalmon/grain"); len(got) != 0 {
		t.Fatalf("the branch warm may dispatch %v for a repository this deployment depends on "+
			"nothing of", got)
	}
}
