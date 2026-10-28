package orchestrator_test

// Release names at the edges of what model.ValidReleaseName accepts, and
// one stored before it refused names git cannot use as a branch
// (docs/test-runs/2026-10-07-release-name-validation.md).

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// A deployment that stored "v3.0..x" before the name was refused still
// has the row. Every branch it names is one GitHub refuses, so the
// reconciler must say so once and stop asking: before, it made the same
// refused calls on every cycle, forever.
func TestSyncReleasesStopsAskingGitHubForAReleaseNamedBeforeTheNameWasRefused(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// CreateRelease refuses the name now, so write the row the way an
	// older build did: as an ordinary release, renamed in place.
	release, err := store.CreateRelease(ctx, repo, "v3x", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	const stored = "v3.0..x"
	if _, err := store.DB().ExecContext(ctx, "UPDATE `release` SET `name` = ? WHERE `id` = ?", stored, release.ID); err != nil {
		t.Fatalf("renaming release: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, "UPDATE `release_candidate` SET `branch` = ? WHERE `release_id` = ?",
		model.RCBranch(stored, 1), release.ID); err != nil {
		t.Fatalf("renaming candidate: %v", err)
	}

	for cycle := range 3 {
		if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
			t.Fatalf("cycle %d: SyncReleases: %v", cycle, err)
		}
	}
	if len(sim.Calls) != 0 {
		t.Fatalf("got %d GitHub calls for a release no branch can be created for, want none: %+v", len(sim.Calls), sim.Calls)
	}

	got, err := store.GetReleaseByID(ctx, release.ID)
	if err != nil || got == nil {
		t.Fatalf("release: (%+v, %v)", got, err)
	}
	if got.Status != model.ReleaseProvisioning || !strings.Contains(got.LastError, "not a branch name git accepts") {
		t.Fatalf("got release %q / %q, want provisioning with an error naming the name", got.Status, got.LastError)
	}
	current, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || current == nil {
		t.Fatalf("current candidate: %+v %v", current, err)
	}
	if !strings.Contains(current.LastError, "not a branch name git accepts") {
		t.Fatalf("got candidate error %q, want the same account as its release", current.LastError)
	}
}

// Names at the edges of the charset -- 64 characters, all digits, runs of
// '_' and '-', a single character -- provision, cut, promote and cut
// again into real branches, read back from the bare repository itself.
func TestSyncReleasesCreatesEveryBranchForNamesAtTheCharsetEdges(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	names := []string{
		strings.Repeat("a", 63) + "Z",
		"20261007",
		"a__--__b",
		"9_-.-_0",
		"x",
		"v3.0-lock",
	}
	sync := func() {
		t.Helper()
		if err := orchestrator.SyncReleases(ctx, store, client, baseTime); err != nil {
			t.Fatalf("SyncReleases: %v", err)
		}
	}
	for _, n := range names {
		if _, err := store.CreateRelease(ctx, repo, n, baseTime); err != nil {
			t.Fatalf("create release %q: %v", n, err)
		}
	}
	sync()
	for _, n := range names {
		if _, err := store.PromoteCandidate(ctx, repo, n); err != nil {
			t.Fatalf("promote %q: %v", n, err)
		}
	}
	sync()
	for _, n := range names {
		if _, err := store.CutCandidate(ctx, repo, n, baseTime); err != nil {
			t.Fatalf("cut %q: %v", n, err)
		}
	}
	sync()

	out, err := exec.Command("git", "ls-remote", "--heads", sim.BareRepo).Output()
	if err != nil {
		t.Fatalf("git ls-remote: %v", err)
	}
	heads := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			heads[strings.TrimPrefix(ref, "refs/heads/")] = true
		}
	}
	for _, n := range names {
		for _, b := range []string{n, n + ".latest", model.RCBranch(n, 1), model.RCBranch(n, 2)} {
			if !heads[b] {
				t.Errorf("branch %q is missing from the bare repository; it has %v", b, heads)
			}
		}
	}
}
