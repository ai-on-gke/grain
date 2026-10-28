package ui_test

// The API surface of the merge queue's strict rule (model.RepoConfig.
// RequireBranchUpToDate, grain/task-198): the switch, and what flipping
// it must not destroy in the row it shares. What the queue *does* with
// it -- merging the base into a clean-but-behind head and waiting for CI
// on what that makes -- is pinned in pkg/orchestrator.

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestSetRepoRequireBranchUpToDateStoresTheSwitch(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	// Off is the default, and a repo with no row of its own is in it.
	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if read.RequireBranchUpToDate {
		t.Fatal("requireBranchUpToDate = true for a repo nobody has configured")
	}

	got, err := c.SetRepoRequireBranchUpToDate(ctx, "acme/widgets", true)
	if err != nil {
		t.Fatalf("turning the strict rule on: %v", err)
	}
	if !got.RequireBranchUpToDate {
		t.Fatal("requireBranchUpToDate = false after turning it on")
	}
	if read, err = c.RepoDefaults(ctx, "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	if !read.RequireBranchUpToDate {
		t.Fatal("requireBranchUpToDate = false on a re-read, want the stored switch")
	}

	// And off again, which for a repo that says nothing else leaves no
	// row at all (model.RepoConfig.Empty) -- still a clean false rather
	// than an error.
	if got, err = c.SetRepoRequireBranchUpToDate(ctx, "acme/widgets", false); err != nil {
		t.Fatalf("turning the strict rule off: %v", err)
	}
	if got.RequireBranchUpToDate {
		t.Fatal("requireBranchUpToDate = true after turning it off")
	}
}

// Every per-repo setting shares one row and Store.PutRepoConfig replaces
// that row wholesale, so the newest switch has to preserve what its
// neighbours wrote, and they have to preserve it.
func TestRequireBranchUpToDateSurvivesTheOtherRepoSaves(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	if _, err := c.SetRepoRequireBranchUpToDate(ctx, "acme/widgets", true); err != nil {
		t.Fatal(err)
	}
	got, err := c.SetRepoSetupCommand(ctx, "acme/widgets", "make deps")
	if err != nil {
		t.Fatal(err)
	}
	if !got.RequireBranchUpToDate {
		t.Fatal("requireBranchUpToDate = false after saving a setup command, want it untouched")
	}
	if got, err = c.SetRepoDeclarations(ctx, "acme/widgets", true); err != nil {
		t.Fatal(err)
	}
	if !got.RequireBranchUpToDate {
		t.Fatal("requireBranchUpToDate = false after saving declarations, want it untouched")
	}

	// And the other way round: flipping this switch keeps both.
	if got, err = c.SetRepoRequireBranchUpToDate(ctx, "acme/widgets", false); err != nil {
		t.Fatal(err)
	}
	if got.SetupCommand != "make deps" || !got.Declarations {
		t.Fatalf("defaults = %+v after flipping the strict rule, want the rest untouched", got)
	}
}

func TestRepoRequireBranchUpToDateGetAndPut(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/require-branch-up-to-date",
		`{"requireBranchUpToDate":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoDefaults](t, rec)
	if saved.Repo != "acme/widgets" || !saved.RequireBranchUpToDate {
		t.Fatalf("put response = %+v, want acme/widgets with the strict rule on", saved)
	}

	// Every one of these routes reports the same whole-defaults document
	// (ui.RepoDefaults), so a pane editing one field sees what the others
	// hold.
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/require-branch-up-to-date", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(got, saved) {
		t.Fatalf("get = %+v, want %+v", got, saved)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/capabilities", "")
	if got := decode[ui.RepoDefaults](t, rec); !got.RequireBranchUpToDate {
		t.Fatal("the capabilities route reports requireBranchUpToDate = false, want the stored switch")
	}

	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/require-branch-up-to-date",
		`{"requireBranchUpToDate":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); got.RequireBranchUpToDate {
		t.Fatal("requireBranchUpToDate = true after turning it off")
	}
}
