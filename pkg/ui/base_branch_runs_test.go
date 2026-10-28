package ui_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The operator control the sentence about a red base branch never had
// (grain/task-480): ask for that branch's own check to be run now.
//
// What it must do is record the ask on the branch's row and start
// nothing at all -- the reconciler owns the one-run-in-flight bound, so
// an endpoint that started its own run would be a second way past it.
func TestAskingForABaseBranchRunRecordsItAndStartsNothing(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{}
	srv, _, store := releaseServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	// A branch whose last run the operator themselves cancelled: the one
	// verdict grain will not re-ask about by itself, which is the case
	// this button exists for.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "run-1", Commit: "abc123abc123abc",
		StartedAt: baseTime, Conclusion: string(ci.ConclusionCancelled),
		Detail: "cancelled by alice", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/base-branch-runs", `{"branch":"main"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.BaseBranchRunRequested](t, rec)
	if got.Branch != "main" || got.Commit != "abc123abc123abc" || got.RequestedBy != "alice" {
		t.Fatalf("response = %+v, want the request recorded against main's own commit", got)
	}
	if got.RunInFlight {
		t.Fatalf("response = %+v, want no run in flight for a branch whose run concluded", got)
	}
	if got.Detail == "" {
		t.Fatal("nothing says what happens next, which is the whole difference between this button and one that does nothing")
	}
	if len(runner.started) != 0 {
		t.Fatalf("the endpoint started %d runs itself, want the reconciler to start it", len(runner.started))
	}

	row, err := store.GetBaseBranchCI(t.Context(), repo, "main")
	if err != nil || row == nil {
		t.Fatalf("reading main's row: %+v (%v)", row, err)
	}
	if row.RunRequestedAt == nil || row.RunRequestedBy != "alice" {
		t.Fatalf("main's row = %+v, want an operator's request recorded on it", row)
	}
	// And nothing else about the row moved: the verdict, the run and the
	// commit are still what they were, for the reconciler to replace.
	if row.RunID != "run-1" || row.Conclusion != string(ci.ConclusionCancelled) {
		t.Fatalf("main's row = %+v, want the recorded run left alone", row)
	}
}

// A request made while a branch already has a run going is recorded
// rather than refused -- one run judges a branch at a time, so the
// answer is "after that one", and the response has to say so.
func TestAskingForABaseBranchRunWhileOneIsGoingSaysWhenItStarts(t *testing.T) {
	t.Parallel()
	srv, _, store := releaseServer(t, &fakeCIRunner{})
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "run-1", Commit: "abc123abc123abc",
		StartedAt: baseTime, Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/base-branch-runs", `{"branch":"main"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.BaseBranchRunRequested](t, rec)
	if !got.RunInFlight {
		t.Fatalf("response = %+v, want it to say a run is already judging this branch", got)
	}
	row, err := store.GetBaseBranchCI(t.Context(), repo, "main")
	if err != nil || row == nil || row.RunRequestedAt == nil {
		t.Fatalf("main's row = %+v (%v), want the request kept for the cycle after this run", row, err)
	}
}

// The branch is optional where there is only one to mean: the surface
// this is pressed from knows the repository and a task's own base, which
// is empty for "this repository's default branch".
func TestAskingForABaseBranchRunWithoutNamingTheBranch(t *testing.T) {
	t.Parallel()
	srv, _, store := releaseServer(t, &fakeCIRunner{})
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "abc123abc123abc", StartedAt: baseTime,
		RunID: "run-1", Conclusion: string(ci.ConclusionFailure), Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/base-branch-runs", `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	if got := decode[ui.BaseBranchRunRequested](t, rec); got.Branch != "main" {
		t.Fatalf("response = %+v, want the one watched branch resolved", got)
	}

	// With a second watched branch there is no single answer, so the
	// question comes back rather than a guess -- naming both.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "release-2", Commit: "def456def456def", StartedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, srv, http.MethodPost, "/api/repos/acme/widgets/base-branch-runs", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST with two watched branches: %d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{"main", "release-2"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("the refusal does not name %q: %s", want, rec.Body)
		}
	}
}

// A branch grain has no row for is a branch nothing has ever judged, and
// a run asked for here would not change that: the reconciler walks rows.
// So it is a 404 that says how a branch comes to be watched.
func TestAskingForABaseBranchRunOnABranchNobodyWatches(t *testing.T) {
	t.Parallel()
	srv, _, _ := releaseServer(t, &fakeCIRunner{})

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/base-branch-runs", `{"branch":"main"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "watch the default branch") {
		t.Fatalf("the refusal does not say how a branch comes to be watched: %s", rec.Body)
	}
}

// A repository whose base-branch check this deployment does not run is
// refused out loud (grain/task-3332): recording the ask would leave it for
// a reconciler that clears it unanswered.
func TestAskingForABaseBranchRunWithTheCheckSwitchedOffIsRefused(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{}
	srv, _, store := releaseServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain, Work: model.WorkOwnership{Default: model.OwnershipOff},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "abc123abc123abc", StartedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/repos/acme/widgets/base-branch-runs", `{"branch":"main"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "standing work") {
		t.Fatalf("POST: %d %s, want a refusal naming the standing work switch", rec.Code, rec.Body)
	}
	row, err := store.GetBaseBranchCI(t.Context(), repo, "main")
	if err != nil || row == nil || row.RunRequestedAt != nil {
		t.Fatalf("main's row = %+v (%v), want no request recorded", row, err)
	}
}

// Asking for a base-branch run needs what requesting a branch does
// (requireRepoPerson): an agent is refused with a 403 -- before the body
// is read, so a malformed one does not turn the refusal into a 400 --
// and nothing is recorded on the row, while any person may ask.
func TestAskingForABaseBranchRunNeedsAPerson(t *testing.T) {
	t.Parallel()
	users := []struct {
		user    string
		allowed bool
	}{
		{agentPrefix + "run-1", false}, {"zed", true}, {"owner", true},
	}
	for _, u := range users {
		t.Run(u.user, func(t *testing.T) {
			t.Parallel()
			srv, store := authzReleaseServer(t)
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
				Repo: repo, Branch: "main", RunID: "run-1", Commit: "abc123abc123abc",
				StartedAt: baseTime, Conclusion: string(ci.ConclusionCancelled), Attempts: 1,
			}); err != nil {
				t.Fatal(err)
			}

			if !u.allowed {
				rec := doAsCaller(t, srv, u.user, http.MethodPost, "/api/repos/acme/widgets/base-branch-runs", `{not json`)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("%s with a malformed body: %d %s, want 403", u.user, rec.Code, rec.Body)
				}
			}
			rec := doAsCaller(t, srv, u.user, http.MethodPost, "/api/repos/acme/widgets/base-branch-runs", `{"branch":"main"}`)
			want := http.StatusForbidden
			if u.allowed {
				want = http.StatusAccepted
			}
			if rec.Code != want {
				t.Fatalf("%s: %d %s, want %d", u.user, rec.Code, rec.Body, want)
			}
			row, err := store.GetBaseBranchCI(t.Context(), repo, "main")
			if err != nil || row == nil {
				t.Fatalf("reading main's row: %+v (%v)", row, err)
			}
			if recorded := row.RunRequestedAt != nil; recorded != u.allowed {
				t.Fatalf("%s: main's row = %+v, want a request recorded only for an allowed actor", u.user, row)
			}
		})
	}
}
