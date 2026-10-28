package ui_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestSetRepoCIModeStoresAndValidates(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	// Default is github
	read, err := c.RepoDefaults(ctx, "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if read.CIMode != "github" {
		t.Fatalf("ciMode = %q for unconfigured repo, want github", read.CIMode)
	}

	// Set to grain
	got, err := c.SetRepoCIMode(ctx, "acme/widgets", "grain")
	if err != nil {
		t.Fatalf("SetRepoCIMode grain: %v", err)
	}
	if got.CIMode != "grain" || !got.IgnoreGitHubCI {
		t.Fatalf("after SetRepoCIMode grain: CIMode=%q IgnoreGitHubCI=%v", got.CIMode, got.IgnoreGitHubCI)
	}

	// Set to none
	got, err = c.SetRepoCIMode(ctx, "acme/widgets", "none")
	if err != nil {
		t.Fatalf("SetRepoCIMode none: %v", err)
	}
	if got.CIMode != "none" || got.IgnoreGitHubCI {
		t.Fatalf("after SetRepoCIMode none: CIMode=%q IgnoreGitHubCI=%v", got.CIMode, got.IgnoreGitHubCI)
	}

	// Set to github
	got, err = c.SetRepoCIMode(ctx, "acme/widgets", "github")
	if err != nil {
		t.Fatalf("SetRepoCIMode github: %v", err)
	}
	if got.CIMode != "github" || got.IgnoreGitHubCI {
		t.Fatalf("after SetRepoCIMode github: CIMode=%q IgnoreGitHubCI=%v", got.CIMode, got.IgnoreGitHubCI)
	}

	// Invalid value rejected
	_, err = c.SetRepoCIMode(ctx, "acme/widgets", "invalid-mode")
	if err == nil || !strings.Contains(err.Error(), "ciMode") {
		t.Fatalf("expected error for invalid CIMode, got %v", err)
	}
}

func TestRepoSettingsAndCIModeHTTPHandlers(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	// PUT /api/repos/acme/widgets/ci-mode
	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/ci-mode", `{"ciMode":"grain"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put ci-mode status = %d: %s", rec.Code, rec.Body)
	}
	saved := decode[ui.RepoDefaults](t, rec)
	if saved.CIMode != "grain" {
		t.Fatalf("saved.CIMode = %q, want grain", saved.CIMode)
	}

	// GET /api/repos/acme/widgets/ci-mode
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/ci-mode", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get ci-mode status = %d: %s", rec.Code, rec.Body)
	}
	got := decode[ui.RepoDefaults](t, rec)
	if got.CIMode != "grain" {
		t.Fatalf("got.CIMode = %q, want grain", got.CIMode)
	}

	// The per-repository admin list is gone (schema 27): its routes are
	// not served, and an older client's `admins` in a settings PUT is
	// accepted and ignored.
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec = do(t, srv, method, "/api/repos/acme/widgets/admins", `{"admins":["alice@example.com"]}`)
		if rec.Code < 400 {
			t.Fatalf("%s admins status = %d, want the route gone: %s", method, rec.Code, rec.Body)
		}
	}
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/settings", `{"admins":["alice@example.com"],"ciMode":"grain"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put settings with admins status = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"admins"`) {
		t.Fatalf("settings response still carries admins: %s", rec.Body)
	}

	// PUT /api/repos/acme/widgets/settings
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/settings", `{"ciMode":"none","promptExtension":"extra instructions"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put settings status = %d: %s", rec.Code, rec.Body)
	}
	saved = decode[ui.RepoDefaults](t, rec)
	if saved.CIMode != "none" || saved.PromptExtension != "extra instructions" {
		t.Fatalf("saved settings = %+v", saved)
	}

	// GET /api/repos/acme/widgets/settings
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings status = %d: %s", rec.Code, rec.Body)
	}
	got = decode[ui.RepoDefaults](t, rec)
	if got.CIMode != "none" || got.PromptExtension != "extra instructions" {
		t.Fatalf("got settings = %+v", got)
	}
}

// Every person is the deployment's owner -- whoever holds the access
// code is -- so any person may change a repository's settings, while an
// agent (a run's MCP token, a sandbox) may
// not, and over HTTP is told so with a 403 (checkRepoPerson).
func TestRepoSettingsArePeoplesAndNotAgents(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	clientAs := func(actor model.Principal) *ui.Client {
		return ui.NewClient(ui.Config{
			Actor:         actor,
			DefaultTarget: &repo,
			Capabilities:  ui.OfferedCapabilities(),
		}, store)
	}
	for _, name := range []string{"carol@example.com", "eve@example.com"} {
		person := clientAs(ui.DefaultActor(name))
		if _, err := person.SetRepoCIMode(ctx, "acme/widgets", "grain"); err != nil {
			t.Fatalf("%s SetRepoCIMode: %v", name, err)
		}
		if _, err := person.SetRepoPromptExtension(ctx, "acme/widgets", name+"'s instructions"); err != nil {
			t.Fatalf("%s SetRepoPromptExtension: %v", name, err)
		}
	}

	agent := clientAs(model.Principal{Kind: model.PrincipalAgent, ID: "run-1"})
	var fe *ui.ForbiddenError
	if _, err := agent.SetRepoCIMode(ctx, "acme/widgets", "none"); !errors.As(err, &fe) || !strings.Contains(err.Error(), "not a person") {
		t.Fatalf("agent SetRepoCIMode: err = %v, want a ForbiddenError saying it is not a person", err)
	}
	rec := do(t, ui.NewServerWithClient(agent), http.MethodPut, "/api/repos/acme/widgets/ci-mode", `{"ciMode":"none"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not a person") {
		t.Fatalf("agent PUT ci-mode = %d %s, want 403", rec.Code, rec.Body)
	}
}
