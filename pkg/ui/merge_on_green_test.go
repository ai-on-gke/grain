package ui_test

// The API surface of the merge queue's merge-on-green switch
// (model.RepoConfig.MergeOnGreen, grain/task-545). What the queue does
// with it -- letting a green entry merge past one whose CI is still
// running -- is pinned in pkg/orchestrator.

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

func TestSetRepoMergeOnGreenStoresTheSwitch(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if read.MergeOnGreen {
		t.Fatal("mergeOnGreen = true for a repo nobody has configured")
	}

	if _, err := c.SetRepoSetupCommand(ctx, "acme/widgets", "make deps"); err != nil {
		t.Fatal(err)
	}
	got, err := c.SetRepoMergeOnGreen(ctx, "acme/widgets", true)
	if err != nil {
		t.Fatalf("turning merge-on-green on: %v", err)
	}
	if !got.MergeOnGreen || got.SetupCommand != "make deps" {
		t.Fatalf("defaults = %+v, want merge-on-green on and the setup command untouched", got)
	}
	if got, err = c.SetRepoSetupCommand(ctx, "acme/widgets", "make all"); err != nil {
		t.Fatal(err)
	}
	if !got.MergeOnGreen {
		t.Fatal("mergeOnGreen = false after saving a setup command, want it untouched")
	}

	enabled := false
	if got, err = c.SetRepoSettings(ctx, "acme/widgets", ui.SetRepoSettingsRequest{MergeOnGreen: &enabled}); err != nil {
		t.Fatal(err)
	}
	if got.MergeOnGreen {
		t.Fatal("mergeOnGreen = true after the settings route turned it off")
	}
}

func TestRepoMergeOnGreenGetAndPut(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/merge-on-green", `{"mergeOnGreen":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoDefaults](t, rec)
	if saved.Repo != "acme/widgets" || !saved.MergeOnGreen {
		t.Fatalf("put response = %+v, want acme/widgets with merge-on-green on", saved)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/merge-on-green", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(got, saved) {
		t.Fatalf("get = %+v, want %+v", got, saved)
	}
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/merge-on-green", `{"mergeOnGreen":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); got.MergeOnGreen {
		t.Fatal("mergeOnGreen = true after turning it off")
	}
}

func TestRepoBaseRepairBypassesHumanGatesGetAndPut(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/base-repair-bypasses-human-gates", `{"baseRepairBypassesHumanGates":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoDefaults](t, rec)
	if saved.Repo != "acme/widgets" || !saved.BaseRepairBypassesHumanGates {
		t.Fatalf("put response = %+v, want acme/widgets with base-repair-bypasses-human-gates on", saved)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/base-repair-bypasses-human-gates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(got, saved) {
		t.Fatalf("get = %+v, want %+v", got, saved)
	}
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/base-repair-bypasses-human-gates", `{"baseRepairBypassesHumanGates":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); got.BaseRepairBypassesHumanGates {
		t.Fatal("baseRepairBypassesHumanGates = true after turning it off")
	}
}
