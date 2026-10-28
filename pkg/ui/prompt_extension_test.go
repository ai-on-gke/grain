package ui_test

// The API surface of the prompt extension (grain/task-114): the
// deployment-wide setting, a repo's own layer on top of it, and a task's
// own override of both. What a *run* is actually told is pinned in
// pkg/orchestrator; this is about the three places a human writes it and
// what each of them may quietly destroy.

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestUpdateSettingsStoresAndTrimsThePromptExtension(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	padded := "  Run `make lint` before you push.\n"
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{PromptExtension: &padded})
	if err != nil {
		t.Fatalf("saving a prompt extension: %v", err)
	}
	if got.PromptExtension != "Run `make lint` before you push." {
		t.Fatalf("promptExtension = %q, want it trimmed", got.PromptExtension)
	}

	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.PromptExtension != got.PromptExtension {
		t.Fatalf("promptExtension = %q on a re-read, want the stored %q", read.PromptExtension, got.PromptExtension)
	}

	// Empty is a real value here, not "leave it alone": it is how a
	// deployment stops adding anything to its runs' prompts.
	empty := ""
	if got, err = c.UpdateSettings(ctx, ui.UpdateSettingsRequest{PromptExtension: &empty}); err != nil {
		t.Fatalf("clearing the prompt extension: %v", err)
	}
	if got.PromptExtension != "" {
		t.Fatalf("promptExtension = %q after clearing it, want empty", got.PromptExtension)
	}
}

func TestSetRepoPromptExtensionReportsAllThreeLayers(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	deployment := "Run `make lint` before you push."
	seed := firstSettings()
	seed.PromptExtension = &deployment
	if _, err := c.UpdateSettings(ctx, seed); err != nil {
		t.Fatal(err)
	}

	got, err := c.SetRepoPromptExtension(ctx, "acme/widgets", "  Migrations live in db/.  ")
	if err != nil {
		t.Fatalf("setting a repo's own prompt extension: %v", err)
	}
	if got.PromptExtension != "Migrations live in db/." {
		t.Fatalf("promptExtension = %q, want it trimmed", got.PromptExtension)
	}
	if got.DeploymentPromptExtension != deployment {
		t.Fatalf("deploymentPromptExtension = %q, want %q", got.DeploymentPromptExtension, deployment)
	}
	// The effective text is the composition dispatch itself applies:
	// appended, deployment first, since a repo adds and never replaces.
	want := deployment + "\n\nMigrations live in db/."
	if got.EffectivePromptExtension != want {
		t.Fatalf("effectivePromptExtension = %q, want %q", got.EffectivePromptExtension, want)
	}

	// Cleared, the repo contributes nothing and the deployment's own is
	// all that is left -- including for a repo whose row is now gone
	// entirely (model.RepoConfig.Empty).
	if got, err = c.SetRepoPromptExtension(ctx, "acme/widgets", ""); err != nil {
		t.Fatalf("clearing a repo's own prompt extension: %v", err)
	}
	if got.PromptExtension != "" || got.EffectivePromptExtension != deployment {
		t.Fatalf("after clearing: own = %q, effective = %q, want empty and %q",
			got.PromptExtension, got.EffectivePromptExtension, deployment)
	}
}

// The two per-repo settings share one row, and Store.PutRepoConfig
// replaces that row wholesale -- so each setter has to preserve the
// other's field or saving one pane silently wipes the other's work.
func TestRepoDefaultsSurviveEachOthersSaves(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	if _, err := c.SetRepoPromptExtension(ctx, "acme/widgets", "Migrations live in db/."); err != nil {
		t.Fatal(err)
	}
	got, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"gcp-key"})
	if err != nil {
		t.Fatal(err)
	}
	if got.PromptExtension != "Migrations live in db/." {
		t.Fatalf("promptExtension = %q after saving capabilities, want it untouched", got.PromptExtension)
	}

	got, err = c.SetRepoPromptExtension(ctx, "acme/widgets", "Migrations live in db/ -- read db/README.md.")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("defaultCapabilities = %v after saving a prompt extension, want them untouched",
			got.DefaultCapabilities)
	}
}

func TestRepoPromptExtensionGetAndPut(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/prompt-extension",
		`{"promptExtension":"Migrations live in db/."}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoDefaults](t, rec)
	if saved.Repo != "acme/widgets" || saved.PromptExtension != "Migrations live in db/." {
		t.Fatalf("put response = %+v, want acme/widgets with its own prompt extension", saved)
	}

	// Both routes report the same whole-defaults document (ui.RepoDefaults),
	// so a pane editing either field sees what the other holds.
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/prompt-extension", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(got, saved) {
		t.Fatalf("get = %+v, want %+v", got, saved)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/capabilities", "")
	if got := decode[ui.RepoDefaults](t, rec); got.PromptExtension != saved.PromptExtension {
		t.Fatalf("the capabilities route reports promptExtension = %q, want the same %q",
			got.PromptExtension, saved.PromptExtension)
	}

	// Cleared through the same route, which is how a repo stops adding
	// anything of its own -- and the point at which it may have no row
	// here at all (model.RepoConfig.Empty).
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/prompt-extension", `{"promptExtension":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); got.PromptExtension != "" {
		t.Fatalf("promptExtension = %q after clearing it, want empty", got.PromptExtension)
	}
}

func TestTaskPromptExtensionOverrideRoundTrips(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:           "Regenerate the client",
		Repo:            "acme/widgets",
		PromptExtension: "  Ignore the house rules: this task rewrites them.  ",
	})
	if err != nil {
		t.Fatalf("filing a task with an override: %v", err)
	}
	if task.PromptExtension != "Ignore the house rules: this task rewrites them." {
		t.Fatalf("promptExtension = %q, want it trimmed", task.PromptExtension)
	}

	// Editable afterwards, and clearable back to "no override" -- the
	// same meaningful empty string agentFramework has.
	edited := ""
	got, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{PromptExtension: &edited})
	if err != nil {
		t.Fatalf("clearing the override: %v", err)
	}
	if got.PromptExtension != "" {
		t.Fatalf("promptExtension = %q after clearing it, want empty", got.PromptExtension)
	}

	// A task filed with none has none: an override is opt-in, and
	// nothing seeds one from the deployment's own text (which is read at
	// dispatch instead -- model.Config.PromptExtension).
	deployment := "Run `make lint` before you push."
	seed := firstSettings()
	seed.PromptExtension = &deployment
	if _, err := c.UpdateSettings(ctx, seed); err != nil {
		t.Fatal(err)
	}
	plain, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Ordinary work", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.PromptExtension != "" {
		t.Fatalf("promptExtension = %q on a task nobody gave one, want empty", plain.PromptExtension)
	}
}
