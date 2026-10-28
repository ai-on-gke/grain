package postgres_test

// Release management's own store tests (bwsalmon/agents#571), against a
// real embedded SQLite database -- the same discipline store_test.go
// holds every other Store method to, for the same reason its own doc
// comment gives: this proves SQLite accepts the DDL and the queries
// answer, not merely that grain's own Go logic is self-consistent.

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

var (
	widgets = model.RepoRef{Owner: "acme", Name: "widgets"}
	gadgets = model.RepoRef{Owner: "acme", Name: "gadgets"}
)

func TestCreateReleaseRejectsAnInvalidName(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, err := store.CreateRelease(ctx, widgets, "", now); !errors.Is(err, model.ErrInvalidReleaseName) {
		t.Fatalf("got %v, want ErrInvalidReleaseName for an empty name", err)
	}
	if _, err := store.CreateRelease(ctx, widgets, "myfeat.latest", now); !errors.Is(err, model.ErrInvalidReleaseName) {
		t.Fatalf("got %v, want ErrInvalidReleaseName for a name colliding with the latest suffix", err)
	}
	if _, err := store.CreateRelease(ctx, widgets, "myfeat.rc.3", now); !errors.Is(err, model.ErrInvalidReleaseName) {
		t.Fatalf("got %v, want ErrInvalidReleaseName for a name colliding with the rc suffix", err)
	}
	// Names git check-ref-format refuses as a branch, though every
	// character is in the safe set: the release's own name is its prod
	// branch, so none of them could ever be created on GitHub -- and
	// git refuses a branch named exactly "HEAD" too.
	for _, name := range []string{"v3.0..x", "v3.", "v3.lock", "HEAD"} {
		if _, err := store.CreateRelease(ctx, widgets, name, now); !errors.Is(err, model.ErrInvalidReleaseName) {
			t.Fatalf("got %v, want ErrInvalidReleaseName for %q", err, name)
		}
	}
	if _, err := store.CreateRelease(ctx, widgets, "v3.0.1-lock", now); err != nil {
		t.Fatalf("CreateRelease(v3.0.1-lock) = %v, want a release", err)
	}
}

func TestCreateReleaseAlsoCutsItsOwnFirstCandidate(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if r.Repo != widgets || r.Name != "myfeat" || r.Status != model.ReleaseProvisioning {
		t.Fatalf("got %+v", r)
	}
	if r.LatestBranch() != "myfeat.latest" || r.ProdBranch() != "myfeat" {
		t.Fatalf("got latest %q prod %q, want myfeat.latest / myfeat", r.LatestBranch(), r.ProdBranch())
	}

	first, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || first == nil {
		t.Fatalf("first candidate: (%+v, %v)", first, err)
	}
	want := model.Candidate{
		ID: first.ID, Repo: widgets, ReleaseID: r.ID, Number: 1,
		Branch: "myfeat.rc.1", Status: model.CandidateCutting, CreatedAt: now,
	}
	if !reflect.DeepEqual(*first, want) {
		t.Fatalf("got %+v, want %+v", *first, want)
	}
}

func TestCreateReleaseRefusesAnUnmergedNameCollision(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, err := store.CreateRelease(ctx, widgets, "myfeat", now); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := store.CreateRelease(ctx, widgets, "myfeat", now); !errors.Is(err, model.ErrReleaseNameInUse) {
		t.Fatalf("got %v, want ErrReleaseNameInUse", err)
	}
	// A different repo, or a different name on the same repo, is fine.
	if _, err := store.CreateRelease(ctx, gadgets, "myfeat", now); err != nil {
		t.Fatalf("same name, different repo: %v", err)
	}
	if _, err := store.CreateRelease(ctx, widgets, "otherfeat", now); err != nil {
		t.Fatalf("different name, same repo: %v", err)
	}
}

func TestCreateReleaseAllowsReusingANameOnceItHasMerged(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	first, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, first.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	if _, err := store.RequestReleaseMerge(ctx, widgets, "myfeat"); err != nil {
		t.Fatalf("request merge: %v", err)
	}
	if err := store.MarkReleaseMerged(ctx, first.ID, "https://example/pr/1", now.Add(time.Hour)); err != nil {
		t.Fatalf("mark merged: %v", err)
	}

	second, err := store.CreateRelease(ctx, widgets, "myfeat", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("want a fresh release row, not the merged one reused")
	}

	current, err := store.GetRelease(ctx, widgets, "myfeat")
	if err != nil || current == nil {
		t.Fatalf("current: (%+v, %v)", current, err)
	}
	if current.ID != second.ID {
		t.Fatalf("got %+v, want the second (unmerged) release as current", *current)
	}

	// The second release's own first candidate starts back at number 1,
	// not continuing the first release's own sequence.
	secondFirst, err := store.CurrentCandidateForRelease(ctx, second.ID)
	if err != nil || secondFirst == nil {
		t.Fatalf("second release's first candidate: (%+v, %v)", secondFirst, err)
	}
	if secondFirst.Number != 1 || secondFirst.Branch != "myfeat.rc.1" {
		t.Fatalf("got %+v, want number 1, branch myfeat.rc.1", *secondFirst)
	}
}

func TestListReleasesOrdersNewestFirst(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, err := store.CreateRelease(ctx, widgets, "myfeat", now); err != nil {
		t.Fatalf("create myfeat: %v", err)
	}
	if _, err := store.CreateRelease(ctx, widgets, "2.1", now.Add(time.Hour)); err != nil {
		t.Fatalf("create 2.1: %v", err)
	}
	got, err := store.ListReleases(ctx, widgets)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].Name != "2.1" || got[1].Name != "myfeat" {
		t.Fatalf("got %+v, want [2.1, myfeat] in that order", got)
	}
}

func TestPendingReleasesSpansEveryRepo(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	w, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create widgets release: %v", err)
	}
	g, err := store.CreateRelease(ctx, gadgets, "2.1", now)
	if err != nil {
		t.Fatalf("create gadgets release: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, g.ID); err != nil {
		t.Fatalf("mark gadgets provisioned: %v", err)
	}
	if _, err := store.RequestReleaseMerge(ctx, gadgets, "2.1"); err != nil {
		t.Fatalf("request gadgets merge: %v", err)
	}

	pending, err := store.PendingReleases(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("got %d pending, want 2: %+v", len(pending), pending)
	}
	if pending[0].ID != w.ID || pending[0].Status != model.ReleaseProvisioning {
		t.Fatalf("got %+v, want widgets still provisioning first", pending[0])
	}
	if pending[1].ID != g.ID || pending[1].Status != model.ReleaseMergeRequested {
		t.Fatalf("got %+v, want gadgets merge-requested second", pending[1])
	}
}

func TestRequestReleaseMergeRequiresAnActiveRelease(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, err := store.RequestReleaseMerge(ctx, widgets, "myfeat"); !errors.Is(err, model.ErrNoRelease) {
		t.Fatalf("got %v, want ErrNoRelease", err)
	}

	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.RequestReleaseMerge(ctx, widgets, "myfeat"); !errors.Is(err, model.ErrReleaseNotActive) {
		t.Fatalf("got %v, want ErrReleaseNotActive while still provisioning", err)
	}

	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	if _, err := store.RequestReleaseMerge(ctx, widgets, "myfeat"); err != nil {
		t.Fatalf("request merge: %v", err)
	}
	if _, err := store.RequestReleaseMerge(ctx, widgets, "myfeat"); !errors.Is(err, model.ErrReleaseAlreadyMergeRequested) {
		t.Fatalf("got %v, want ErrReleaseAlreadyMergeRequested", err)
	}
}

func TestCutCandidateRequiresAnActiveRelease(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, err := store.CutCandidate(ctx, widgets, "myfeat", now); !errors.Is(err, model.ErrNoRelease) {
		t.Fatalf("got %v, want ErrNoRelease", err)
	}
	if _, err := store.CreateRelease(ctx, widgets, "myfeat", now); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Still provisioning: the first candidate already exists, so a second
	// cut request is refused for the same reason a second cut against any
	// already-active, unpromoted candidate is.
	if _, err := store.CutCandidate(ctx, widgets, "myfeat", now); !errors.Is(err, model.ErrReleaseNotActive) {
		t.Fatalf("got %v, want ErrReleaseNotActive while still provisioning", err)
	}
}

// CutCandidate refuses a fresh cut while the release's current candidate
// has not been promoted yet -- the issue's own "current rc" is singular.
func TestCutCandidateRefusesASecondCandidateWhileOneIsUnpromoted(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	// The first candidate (cut by CreateRelease) is still unpromoted.
	if _, err := store.CutCandidate(ctx, widgets, "myfeat", now); !errors.Is(err, model.ErrCandidateActive) {
		t.Fatalf("got %v, want ErrCandidateActive", err)
	}
}

// Once a candidate promotes, cutting a fresh one allocates the next
// number rather than reusing or resetting it.
func TestCutCandidateAfterPromotionAllocatesTheNextNumber(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	first, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || first == nil {
		t.Fatalf("first candidate: (%+v, %v)", first, err)
	}
	if err := store.MarkCandidateCut(ctx, first.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	if _, err := store.PromoteCandidate(ctx, widgets, "myfeat"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := store.MarkCandidatePromoted(ctx, first.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("mark promoted: %v", err)
	}

	second, err := store.CutCandidate(ctx, widgets, "myfeat", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("second cut: %v", err)
	}
	if second.Number != 2 {
		t.Fatalf("got number %d, want 2", second.Number)
	}
	if second.Branch != "myfeat.rc.2" {
		t.Fatalf("got branch %q, want myfeat.rc.2", second.Branch)
	}
}

func TestPromoteCandidateRequiresACandidate(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, err := store.PromoteCandidate(ctx, widgets, "myfeat"); !errors.Is(err, model.ErrNoRelease) {
		t.Fatalf("got %v, want ErrNoRelease", err)
	}
}

// A candidate still cutting (its branch not yet live on GitHub, per the
// releases reconciler) cannot be promoted out from under that in-flight
// step.
func TestPromoteCandidateRequiresItToHaveFinishedCutting(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	if _, err := store.PromoteCandidate(ctx, widgets, "myfeat"); !errors.Is(err, model.ErrCandidateNotReady) {
		t.Fatalf("got %v, want ErrCandidateNotReady", err)
	}
}

// "It cannot be promoted twice."
func TestPromoteCandidateRefusesASecondPromotion(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	c, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || c == nil {
		t.Fatalf("candidate: (%+v, %v)", c, err)
	}
	if err := store.MarkCandidateCut(ctx, c.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	promoted, err := store.PromoteCandidate(ctx, widgets, "myfeat")
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}
	if promoted.Status != model.CandidatePromoting {
		t.Fatalf("got status %q, want promoting", promoted.Status)
	}
	if err := store.MarkCandidatePromoted(ctx, c.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("mark promoted: %v", err)
	}

	if _, err := store.PromoteCandidate(ctx, widgets, "myfeat"); !errors.Is(err, model.ErrAlreadyPromoted) {
		t.Fatalf("got %v, want ErrAlreadyPromoted", err)
	}
}

// The qualifications reconciler's auto-promotion reads a candidate,
// sees its qualification succeed and then promotes it by ID. If a person
// promotes that candidate and cuts a newer one in between, the newer one
// must not be promoted on the strength of the older one's qualification.
func TestPromoteCandidateIfCurrentRefusesASupersededCandidate(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	c, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || c == nil {
		t.Fatalf("candidate: (%+v, %v)", c, err)
	}
	if err := store.MarkCandidateCut(ctx, c.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}

	// The reconciler has read c here. Meanwhile a person promotes c and
	// cuts c2, which finishes cutting before the reconciler writes.
	if _, err := store.PromoteCandidate(ctx, widgets, "myfeat"); err != nil {
		t.Fatalf("promote by hand: %v", err)
	}
	if err := store.MarkCandidatePromoted(ctx, c.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("mark promoted: %v", err)
	}
	c2, err := store.CutCandidate(ctx, widgets, "myfeat", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("cut second candidate: %v", err)
	}
	if err := store.MarkCandidateCut(ctx, c2.ID); err != nil {
		t.Fatalf("mark second cut: %v", err)
	}

	if _, err := store.PromoteCandidateIfCurrent(ctx, widgets, "myfeat", c.ID); !errors.Is(err, model.ErrCandidateSuperseded) {
		t.Fatalf("promoting the superseded candidate: got %v, want ErrCandidateSuperseded", err)
	}
	current, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || current == nil {
		t.Fatalf("current candidate: (%+v, %v)", current, err)
	}
	if current.ID != c2.ID || current.Status != model.CandidateActive {
		t.Fatalf("current candidate is %d %q, want %d still active", current.ID, current.Status, c2.ID)
	}

	promoted, err := store.PromoteCandidateIfCurrent(ctx, widgets, "myfeat", c2.ID)
	if err != nil {
		t.Fatalf("promoting the current candidate by ID: %v", err)
	}
	if promoted.ID != c2.ID || promoted.Status != model.CandidatePromoting {
		t.Fatalf("got %d %q, want %d promoting", promoted.ID, promoted.Status, c2.ID)
	}
}

func TestPendingCandidatesSpansEveryRepo(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	w, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create widgets release: %v", err)
	}
	g, err := store.CreateRelease(ctx, gadgets, "2.1", now)
	if err != nil {
		t.Fatalf("create gadgets release: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, g.ID); err != nil {
		t.Fatalf("mark gadgets provisioned: %v", err)
	}
	gc, err := store.CurrentCandidateForRelease(ctx, g.ID)
	if err != nil || gc == nil {
		t.Fatalf("gadgets candidate: (%+v, %v)", gc, err)
	}
	if err := store.MarkCandidateCut(ctx, gc.ID); err != nil {
		t.Fatalf("mark gadgets candidate cut: %v", err)
	}
	if _, err := store.PromoteCandidate(ctx, gadgets, "2.1"); err != nil {
		t.Fatalf("promote gadgets: %v", err)
	}

	wc, err := store.CurrentCandidateForRelease(ctx, w.ID)
	if err != nil || wc == nil {
		t.Fatalf("widgets candidate: (%+v, %v)", wc, err)
	}

	pending, err := store.PendingCandidates(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("got %d pending, want 2: %+v", len(pending), pending)
	}
	if pending[0].ID != wc.ID || pending[0].Status != model.CandidateCutting {
		t.Fatalf("got %+v, want widgets still cutting first", pending[0])
	}
	if pending[1].ID != gc.ID || pending[1].Status != model.CandidatePromoting {
		t.Fatalf("got %+v, want gadgets promoting second", pending[1])
	}
}

func TestMarkCandidateErrorRecordsTheMessageWithoutChangingStatus(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	c, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || c == nil {
		t.Fatalf("candidate: (%+v, %v)", c, err)
	}
	if err := store.MarkCandidateError(ctx, c.ID, "latest branch not found"); err != nil {
		t.Fatalf("mark error: %v", err)
	}
	got, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || got == nil {
		t.Fatalf("current: (%+v, %v)", got, err)
	}
	if got.Status != model.CandidateCutting {
		t.Fatalf("got status %q, want still cutting", got.Status)
	}
	if got.LastError != "latest branch not found" {
		t.Fatalf("got error %q, want %q", got.LastError, "latest branch not found")
	}

	// A later success clears it.
	if err := store.MarkCandidateCut(ctx, c.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	got, err = store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || got == nil {
		t.Fatalf("current: (%+v, %v)", got, err)
	}
	if got.LastError != "" {
		t.Fatalf("got error %q, want cleared", got.LastError)
	}
}

func TestMarkReleaseErrorRecordsTheMessageWithoutChangingStatus(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.MarkReleaseError(ctx, r.ID, "default branch not found"); err != nil {
		t.Fatalf("mark error: %v", err)
	}
	got, err := store.GetRelease(ctx, widgets, "myfeat")
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if got.Status != model.ReleaseProvisioning {
		t.Fatalf("got status %q, want still provisioning", got.Status)
	}
	if got.LastError != "default branch not found" {
		t.Fatalf("got error %q, want %q", got.LastError, "default branch not found")
	}

	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	got, err = store.GetRelease(ctx, widgets, "myfeat")
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if got.LastError != "" {
		t.Fatalf("got error %q, want cleared", got.LastError)
	}
}

// The other way a release reaches merged: its prod branch carried
// nothing the default branch did not already have, so there is no pull
// request to record and a note saying so instead. Terminal like any
// other merge -- out of PendingReleases, and its name free again --
// which is the whole point of settling it here rather than leaving it in
// merge_requested for the reconciler to retry forever.
func TestMarkReleaseNothingToMergeSettlesTheReleaseWithANoteAndNoPullRequest(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	if _, err := store.RequestReleaseMerge(ctx, widgets, "myfeat"); err != nil {
		t.Fatalf("request merge: %v", err)
	}
	if err := store.MarkReleaseError(ctx, r.ID, "422 from GitHub"); err != nil {
		t.Fatalf("mark error: %v", err)
	}

	const note = "myfeat carried no commits main did not already have"
	if err := store.MarkReleaseNothingToMerge(ctx, r.ID, note, now.Add(time.Hour)); err != nil {
		t.Fatalf("mark nothing to merge: %v", err)
	}

	got, err := store.GetRelease(ctx, widgets, "myfeat")
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if got.Status != model.ReleaseMerged {
		t.Fatalf("got status %q, want merged", got.Status)
	}
	if got.MergeNote != note {
		t.Fatalf("got note %q, want %q", got.MergeNote, note)
	}
	if got.PullRequestURL != "" {
		t.Fatalf("got pull request URL %q, want none", got.PullRequestURL)
	}
	if got.LastError != "" {
		t.Fatalf("got error %q, want the earlier 422 cleared -- nothing here failed", got.LastError)
	}
	if got.MergedAt == nil || !got.MergedAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("got MergedAt %v, want %v", got.MergedAt, now.Add(time.Hour))
	}

	pending, err := store.PendingReleases(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("got %+v pending, want the settled release out of the reconciler's way", pending)
	}
	if _, err := store.CreateRelease(ctx, widgets, "myfeat", now.Add(2*time.Hour)); err != nil {
		t.Fatalf("expected the name to be free again: %v", err)
	}
}

func TestListCandidatesReturnsNewestFirst(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	first, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || first == nil {
		t.Fatalf("first candidate: (%+v, %v)", first, err)
	}
	if err := store.MarkCandidateCut(ctx, first.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	if _, err := store.PromoteCandidate(ctx, widgets, "myfeat"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := store.MarkCandidatePromoted(ctx, first.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("mark promoted: %v", err)
	}
	second, err := store.CutCandidate(ctx, widgets, "myfeat", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("second cut: %v", err)
	}

	list, err := store.ListCandidates(ctx, widgets, "myfeat")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("got %+v, want [second, first]", list)
	}
}

func TestListCandidatesReturnsNilForAnUnknownRelease(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	list, err := store.ListCandidates(ctx, widgets, "myfeat")
	if err != nil || list != nil {
		t.Fatalf("got (%+v, %v), want (nil, nil)", list, err)
	}
}

func TestDeleteReleaseRemovesReleaseAndCandidates(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	// Deleting a non-existent release returns ErrNoRelease.
	if err := store.DeleteRelease(ctx, widgets, "missing"); !errors.Is(err, model.ErrNoRelease) {
		t.Fatalf("got %v, want ErrNoRelease", err)
	}

	// Create a release (which cuts candidate 1).
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}

	// Also verify it's in PendingReleases and PendingCandidates.
	pendingR, err := store.PendingReleases(ctx)
	if err != nil || len(pendingR) == 0 {
		t.Fatalf("pending releases: (%+v, %v)", pendingR, err)
	}
	pendingC, err := store.PendingCandidates(ctx)
	if err != nil || len(pendingC) == 0 {
		t.Fatalf("pending candidates: (%+v, %v)", pendingC, err)
	}

	// Delete it.
	if err := store.DeleteRelease(ctx, widgets, "myfeat"); err != nil {
		t.Fatalf("delete release: %v", err)
	}

	// GetRelease returns nil.
	got, err := store.GetRelease(ctx, widgets, "myfeat")
	if err != nil || got != nil {
		t.Fatalf("got (%+v, %v), want (nil, nil)", got, err)
	}

	// ListReleases for widgets is empty.
	releases, err := store.ListReleases(ctx, widgets)
	if err != nil || len(releases) != 0 {
		t.Fatalf("releases = %+v, want empty", releases)
	}

	// ListCandidates for myfeat is nil.
	candidates, err := store.ListCandidates(ctx, widgets, "myfeat")
	if err != nil || candidates != nil {
		t.Fatalf("candidates = %+v, want nil", candidates)
	}

	// PendingReleases and PendingCandidates no longer include it.
	pendingR, err = store.PendingReleases(ctx)
	if err != nil || len(pendingR) != 0 {
		t.Fatalf("pending releases after delete: %+v", pendingR)
	}
	pendingC, err = store.PendingCandidates(ctx)
	if err != nil || len(pendingC) != 0 {
		t.Fatalf("pending candidates after delete: %+v", pendingC)
	}

	// Verify direct DB counts.
	var relCount, candCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "release" WHERE "id" = $1`, r.ID).Scan(&relCount); err != nil {
		t.Fatal(err)
	}
	if relCount != 0 {
		t.Fatalf("release rows in db = %d, want 0", relCount)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "release_candidate" WHERE "release_id" = $1`, r.ID).Scan(&candCount); err != nil {
		t.Fatal(err)
	}
	if candCount != 0 {
		t.Fatalf("candidate rows in db = %d, want 0", candCount)
	}

	// The release name is now free to be re-created.
	if _, err := store.CreateRelease(ctx, widgets, "myfeat", now.Add(time.Hour)); err != nil {
		t.Fatalf("re-creating release after delete: %v", err)
	}
}
