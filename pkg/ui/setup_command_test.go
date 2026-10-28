package ui_test

// The API surface of a repo's setup command (grain/task-154): the one
// place a human writes it, and what writing it must not destroy beside
// it. What a *run* does with it -- running it in the fresh checkout and
// telling the agent how it went -- is pinned in pkg/orchestrator.

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

func TestSetRepoSetupCommandStoresAndTrimsIt(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	got, err := c.SetRepoSetupCommand(ctx, "acme/widgets", "  make deps\n")
	if err != nil {
		t.Fatalf("setting a repo's setup command: %v", err)
	}
	if got.SetupCommand != "make deps" {
		t.Fatalf("setupCommand = %q, want it trimmed", got.SetupCommand)
	}

	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if read.SetupCommand != "make deps" {
		t.Fatalf("setupCommand = %q on a re-read, want the stored one", read.SetupCommand)
	}

	// Empty is a real value, not "leave it alone": it is how a repo goes
	// back to needing no setup at all, at which point it may have no row
	// here at all (model.RepoConfig.Empty).
	if got, err = c.SetRepoSetupCommand(ctx, "acme/widgets", ""); err != nil {
		t.Fatalf("clearing a repo's setup command: %v", err)
	}
	if got.SetupCommand != "" {
		t.Fatalf("setupCommand = %q after clearing it, want empty", got.SetupCommand)
	}
}

// All three per-repo settings share one row, and Store.PutRepoConfig
// replaces that row wholesale -- so the newest setter has to preserve
// what the other two wrote, and they have to preserve it.
func TestSetupCommandSurvivesTheOtherRepoSaves(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	if _, err := c.SetRepoSetupCommand(ctx, "acme/widgets", "make deps"); err != nil {
		t.Fatal(err)
	}
	got, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"gcp-key"})
	if err != nil {
		t.Fatal(err)
	}
	if got.SetupCommand != "make deps" {
		t.Fatalf("setupCommand = %q after saving capabilities, want it untouched", got.SetupCommand)
	}
	if got, err = c.SetRepoPromptExtension(ctx, "acme/widgets", "Migrations live in db/."); err != nil {
		t.Fatal(err)
	}
	if got.SetupCommand != "make deps" {
		t.Fatalf("setupCommand = %q after saving a prompt extension, want it untouched", got.SetupCommand)
	}

	// And the other way round: writing the setup command keeps both.
	got, err = c.SetRepoSetupCommand(ctx, "acme/widgets", "make deps && npm ci")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("defaultCapabilities = %v after saving a setup command, want them untouched",
			got.DefaultCapabilities)
	}
	if got.PromptExtension != "Migrations live in db/." {
		t.Fatalf("promptExtension = %q after saving a setup command, want it untouched", got.PromptExtension)
	}
}

func TestRepoSetupCommandGetAndPut(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/setup-command",
		`{"setupCommand":"make deps"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoDefaults](t, rec)
	if saved.Repo != "acme/widgets" || saved.SetupCommand != "make deps" {
		t.Fatalf("put response = %+v, want acme/widgets with its setup command", saved)
	}

	// All three routes report the same whole-defaults document
	// (ui.RepoDefaults), so a pane editing one field sees what the others
	// hold and knows what it is about to replace.
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/setup-command", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(got, saved) {
		t.Fatalf("get = %+v, want %+v", got, saved)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/capabilities", "")
	if got := decode[ui.RepoDefaults](t, rec); got.SetupCommand != saved.SetupCommand {
		t.Fatalf("the capabilities route reports setupCommand = %q, want the same %q",
			got.SetupCommand, saved.SetupCommand)
	}

	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/setup-command", `{"setupCommand":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); got.SetupCommand != "" {
		t.Fatalf("setupCommand = %q after clearing it, want empty", got.SetupCommand)
	}
}

// GET /api/config carries the names of the repos that have one, and
// nothing else about it: the frontend needs the names to put such a repo
// on the repos page at all (state.js's repoRows), and a shell command per
// repo on every poll of every open tab is what the per-repo route is for.
func TestConfigNamesTheReposWithASetupCommand(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	if _, err := client.SetRepoSetupCommand(t.Context(), "acme/widgets", "make deps"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		ReposWithSetupCommand []string `json:"reposWithSetupCommand"`
	}](t, rec)
	if !reflect.DeepEqual(got.ReposWithSetupCommand, []string{"acme/widgets"}) {
		t.Fatalf("reposWithSetupCommand = %v, want [acme/widgets]", got.ReposWithSetupCommand)
	}
}
