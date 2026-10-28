package ui_test

// AddTargetRepo/RemoveTargetRepo's own tests, plus /api/repos' HTTP
// surface -- the repos pane's add/remove (bwsalmon/agents#473), which
// settings_test.go's own UpdateSettings tests don't cover: this exercises
// them as the single-repo mutations the repos pane actually calls, not
// as a whole-array PUT.

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestAddTargetRepoRejectsAMalformedRepo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	_, err := c.AddTargetRepo(ctx, "not-a-repo")
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
}

// RemoveTargetRepo never reaches UpdateSettings at all here: an
// unconfigured deployment's TargetRepos is empty, so removing anything
// from it is the same no-op RemoveTargetRepoRemovesAndIsIdempotentWhenAbsent
// already covers for a configured one -- unlike AddTargetRepo above,
// there is no way for a repo to be present to remove before settings
// have ever been saved.
func TestRemoveTargetRepoIsANoOpOnAnUnconfiguredDeployment(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	got, err := c.RemoveTargetRepo(ctx, "acme/widgets")
	if err != nil {
		t.Fatalf("RemoveTargetRepo on an unconfigured deployment: %v", err)
	}
	if got.Configured {
		t.Fatalf("settings = %+v, want still Configured false", got)
	}
}

// TestAddTargetRepoIsVisibleThroughConfigInTheSameProcess guards against
// the gap Client.setTargetRepos closes: Config.TargetRepos used to only
// ever be set once, at NewClient, so a change UpdateSettings wrote to the
// store stayed invisible to anything reading Client.Config directly (GET
// /api/config, CreateTask's own allowlist check) until a restart. Adding
// a repo through the repos pane has to be visible immediately, or the
// pane would look broken the moment anyone actually used it.
func TestAddTargetRepoIsVisibleThroughConfigInTheSameProcess(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/settings",
		`{"pollInterval":"30s","maxWorkers":1,"geminiModel":"gemini-2.5-pro","claudeModel":"claude-sonnet-5","githubHost":"github.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first-time settings save status = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add repo status = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodGet, "/api/config", "")
	cfg := decode[map[string]any](t, rec)
	got, _ := cfg["targetRepos"].([]any)
	if len(got) != 1 || got[0] != "acme/widgets" {
		t.Fatalf("GET /api/config targetRepos = %v, want [acme/widgets]", cfg["targetRepos"])
	}
}

func TestAddRepoRejectsAMalformedRepoWith400(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	do(t, srv, http.MethodPut, "/api/settings",
		`{"pollInterval":"30s","maxWorkers":1,"geminiModel":"gemini-2.5-pro","claudeModel":"claude-sonnet-5","githubHost":"github.com"}`)

	rec := do(t, srv, http.MethodPost, "/api/repos", `{"repo":"not-a-repo"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// grain/task-24: a repo's own default capability set -- the per-repo
// layer of what Settings chooses deployment-wide. Stored as given, and
// reported back alongside the deployment layer it composes with and the
// union a task filed here would actually start with, since the repo's
// own list says nothing useful on its own.
func TestSetRepoDefaultCapabilitiesStoresAndReportsAllThreeSets(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	putDefaultCapabilities(t, ctx, store, "gemini-key")

	got, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"gcp-key"})
	if err != nil {
		t.Fatalf("setting a repo's default capabilities: %v", err)
	}
	if !reflect.DeepEqual(got.DefaultCapabilities, []string{"gcp-key"}) {
		t.Errorf("defaultCapabilities = %v, want [gcp-key]", got.DefaultCapabilities)
	}
	if !reflect.DeepEqual(got.DeploymentDefaultCapabilities, []string{"gemini-key"}) {
		t.Errorf("deploymentDefaultCapabilities = %v, want [gemini-key]", got.DeploymentDefaultCapabilities)
	}
	if !reflect.DeepEqual(got.EffectiveDefaultCapabilities, []string{"gemini-key", "gcp-key"}) {
		t.Errorf("effectiveDefaultCapabilities = %v, want [gemini-key gcp-key]: the deployment's, then this repo's",
			got.EffectiveDefaultCapabilities)
	}

	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatalf("reading them back: %v", err)
	}
	if !reflect.DeepEqual(read, got) {
		t.Fatalf("read back = %+v, want what the write returned %+v", read, got)
	}

	// A repo nobody has configured is not an error: it adds nothing,
	// which is exactly what it contributes.
	none, err := c.RepoDefaults(ctx, "acme/gadgets")
	if err != nil {
		t.Fatalf("reading an unconfigured repo: %v", err)
	}
	if len(none.DefaultCapabilities) != 0 {
		t.Errorf("defaultCapabilities = %v, want none", none.DefaultCapabilities)
	}
	if !reflect.DeepEqual(none.EffectiveDefaultCapabilities, []string{"gemini-key"}) {
		t.Errorf("effectiveDefaultCapabilities = %v, want the deployment's [gemini-key]",
			none.EffectiveDefaultCapabilities)
	}
}

// Emptying the set is how a repo stops adding anything -- and it leaves
// no row behind to be listed or wondered about (model.RepoConfig.Empty).
func TestSetRepoDefaultCapabilitiesToNoneClearsTheRepo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if _, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"gcp-key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", nil); err != nil {
		t.Fatalf("clearing a repo's default capabilities: %v", err)
	}
	configs, err := store.ListRepoConfigs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Fatalf("repo configs = %+v, want none left", configs)
	}
}

// Rejected on the way in, the same way UpdateSettings rejects an unknown
// id for the deployment-wide set: this is somebody choosing a set, and a
// choice that could never take effect should be refused while whoever
// made it is still looking at it. Duplicates are a picker artifact, not
// a mistake, so they are folded rather than refused.
func TestSetRepoDefaultCapabilitiesValidates(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	if _, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"nope"}); err == nil {
		t.Fatal("setting an unknown capability: want a validation error, got nil")
	} else {
		var ve *ui.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("error = %v, want a *ui.ValidationError", err)
		}
	}
	if _, err := c.SetRepoDefaultCapabilities(ctx, "not-a-repo", []string{"gcp-key"}); err == nil {
		t.Fatal("setting defaults on a malformed repo: want a validation error, got nil")
	}

	got, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"gcp-key", "gcp-key"})
	if err != nil {
		t.Fatalf("setting a duplicated id: %v", err)
	}
	if !reflect.DeepEqual(got.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("defaultCapabilities = %v, want [gcp-key] once", got.DefaultCapabilities)
	}
}

// grain/task-43: the per-repo counterpart of TestUpdateSettingsAccepts
// DroppingARetiredDefaultCapability. A set that drops a stored id this
// build no longer offers is accepted, though one that adds an unknown id
// is not (TestSetRepoDefaultCapabilitiesValidates) -- what is validated
// is the set being written, not the set already there, which is the only
// reason the repos pane's own picker can offer a retired id a row to be
// unticked with (capabilityRows, ui/src/state.js) and have the save that
// follows go through.
func TestSetRepoDefaultCapabilitiesAcceptsDroppingARetiredOne(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo, err := model.ParseRepo("acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	// Written straight to the store: SetRepoDefaultCapabilities itself
	// would never have accepted "scratch-repo", so this is the state an
	// upgrade that retired an id leaves behind.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, DefaultCapabilities: []string{"gcp-key", "scratch-repo"},
	}); err != nil {
		t.Fatal(err)
	}

	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.DefaultCapabilities, []string{"gcp-key", "scratch-repo"}) {
		t.Fatalf("defaultCapabilities = %v, want it reported as stored: an operator can only clear one they can see",
			read.DefaultCapabilities)
	}
	if !reflect.DeepEqual(read.EffectiveDefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("effectiveDefaultCapabilities = %v, want [gcp-key]: a retired id grants nothing",
			read.EffectiveDefaultCapabilities)
	}

	got, err := c.SetRepoDefaultCapabilities(ctx, "acme/widgets", []string{"gcp-key"})
	if err != nil {
		t.Fatalf("dropping a retired default capability: %v", err)
	}
	if !reflect.DeepEqual(got.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("defaultCapabilities = %v, want [gcp-key] with the retired id gone", got.DefaultCapabilities)
	}
	if read, err = c.RepoDefaults(ctx, "acme/widgets"); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(read.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("defaultCapabilities = %v after a re-read, want [gcp-key]: the retired id is gone for good",
			read.DefaultCapabilities)
	}
}

func TestRepoCapabilitiesGetAndPut(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/capabilities",
		`{"defaultCapabilities":["gcp-key"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoDefaults](t, rec)
	if saved.Repo != "acme/widgets" || !reflect.DeepEqual(saved.DefaultCapabilities, []string{"gcp-key"}) {
		t.Fatalf("put response = %+v, want acme/widgets with [gcp-key]", saved)
	}

	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/capabilities", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(got, saved) {
		t.Fatalf("get = %+v, want %+v", got, saved)
	}

	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/capabilities",
		`{"defaultCapabilities":["nope"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown capability status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestRemoveTargetRepoCleansUpRepoAndDropsFromList(t *testing.T) {
	t.Parallel()
	c, ctx := testHTTPClient(t)

	// Configure deployment with two target repos: bwsalmon/grain and gke-labs/granule
	init := firstSettings()
	if _, err := c.UpdateSettings(ctx, init); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if _, err := c.AddTargetRepo(ctx, "bwsalmon/grain"); err != nil {
		t.Fatalf("AddTargetRepo: %v", err)
	}
	if _, err := c.AddTargetRepo(ctx, "gke-labs/granule"); err != nil {
		t.Fatalf("AddTargetRepo: %v", err)
	}

	// Add defaults, prompt extension to gke-labs/granule
	if _, err := c.SetRepoDefaultCapabilities(ctx, "gke-labs/granule", []string{"self-debug"}); err != nil {
		t.Fatalf("SetRepoDefaultCapabilities: %v", err)
	}
	if _, err := c.SetRepoPromptExtension(ctx, "gke-labs/granule", "Custom prompt"); err != nil {
		t.Fatalf("SetRepoPromptExtension: %v", err)
	}

	// Create and close a task in gke-labs/granule
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Done task", Repo: "gke-labs/granule", Approved: true,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify gke-labs/granule appears in repos list before removal
	reposBefore, err := c.ListRepos(ctx)
	if err != nil {
		t.Fatalf("ListRepos before removal: %v", err)
	}
	found := false
	for _, r := range reposBefore {
		if r.Repo == "gke-labs/granule" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("gke-labs/granule not found in repos before removal: %+v", reposBefore)
	}

	// Remove gke-labs/granule
	if _, err := c.RemoveTargetRepo(ctx, "gke-labs/granule"); err != nil {
		t.Fatalf("RemoveTargetRepo: %v", err)
	}

	// Verify gke-labs/granule no longer appears in repos list
	reposAfter, err := c.ListRepos(ctx)
	if err != nil {
		t.Fatalf("ListRepos after removal: %v", err)
	}
	for _, r := range reposAfter {
		if r.Repo == "gke-labs/granule" {
			t.Fatalf("gke-labs/granule still in repos list after removal: %+v", reposAfter)
		}
	}

	// Verify defaults were cleared
	defs, err := c.RepoDefaults(ctx, "gke-labs/granule")
	if err != nil {
		t.Fatalf("RepoDefaults: %v", err)
	}
	if len(defs.DefaultCapabilities) != 0 || defs.PromptExtension != "" {
		t.Errorf("repo defaults after removal = %+v, want empty", defs)
	}
}
