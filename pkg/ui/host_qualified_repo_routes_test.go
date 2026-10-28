package ui_test

// A repo off the default host -- git.forge.example/experimental/
// bsalmon-test -- is routed as /api/repos/{owner}/{name} with the host in
// the owner segment, escaped: model.RepoRef.StoredOwner, which is what
// the UI's repoApiRoot and the HTTP client's repoAPIPath both send. These
// walk one of each kind of repo-scoped handler through that spelling, so
// none of them reads the owner segment as a bare owner and drops the host
// (or, the other way about, reads "git.forge.example/experimental" as
// the whole repo).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const (
	depotRepo = "git.forge.example/experimental/bsalmon-test"
	depotRoot = "/api/repos/git.forge.example%2Fexperimental/bsalmon-test"
)

var depotRef = model.RepoRef{Host: "git.forge.example", Owner: "experimental", Name: "bsalmon-test"}

func TestHostQualifiedRepoRoutesKeepTheHost(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()

	// Each route answers about the repo on its own host, never with
	// ParseRepo's complaint about a two-part "host/owner".
	for _, path := range []string{
		"/settings", "/ci-capabilities", "/release", "/work-ownership", "/branches", "/releases", "/workflows",
	} {
		rec := do(t, srv, http.MethodGet, depotRoot+path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	rec := do(t, srv, http.MethodGet, depotRoot+"/github-branches", "")
	if strings.Contains(rec.Body.String(), "repo must be owner/name") {
		t.Errorf("GET /github-branches read the host as the owner: %d %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodPut, depotRoot+"/ci-capabilities", `{"ciCapabilities":["gcp-key"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /ci-capabilities: %d %s", rec.Code, rec.Body)
	}
	stored, err := c.Store.GetRepoConfig(ctx, depotRef)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || !reflect.DeepEqual(stored.CICapabilities, []string{"gcp-key"}) {
		t.Fatalf("stored config for %s = %+v, want gcp-key saved under the host-qualified repo", depotRepo, stored)
	}
	if other, err := c.Store.GetRepoConfig(ctx, model.RepoRef{Owner: "experimental", Name: "bsalmon-test"}); err != nil || (other != nil && len(other.CICapabilities) > 0) {
		t.Fatalf("the default-host experimental/bsalmon-test got the setting too: %+v, %v", other, err)
	}

	rec = do(t, srv, http.MethodGet, depotRoot+"/work-ownership", "")
	if got := decode[ui.RepoWorkOwnership](t, rec); got.Repo != depotRepo {
		t.Fatalf("work-ownership repo = %q, want %q", got.Repo, depotRepo)
	}

	rec = do(t, srv, http.MethodPost, depotRoot+"/branches", `{"name":"myfeat"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /branches: %d %s", rec.Code, rec.Body)
	}
	if got := decode[ui.Branch](t, rec); got.Repo != depotRepo {
		t.Fatalf("branch repo = %q, want %q", got.Repo, depotRepo)
	}
	branches, err := c.ListBranches(ctx, depotRef)
	if err != nil || len(branches) != 1 || branches[0].Name != "myfeat" {
		t.Fatalf("branches of %s = %+v, %v", depotRepo, branches, err)
	}

	rec = do(t, srv, http.MethodPost, depotRoot+"/releases", `{"name":"r1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /releases: %d %s", rec.Code, rec.Body)
	}
	if got := decode[ui.Release](t, rec); got.Repo != depotRepo {
		t.Fatalf("release repo = %q, want %q", got.Repo, depotRepo)
	}
}

// The HTTP client sends a host-qualified repo the way the server reads
// it back: one round trip through each proves the path it builds.
func TestHTTPClientRoutesAHostQualifiedRepo(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	hc := serverAccessClient(srv, ts.URL)
	ctx := context.Background()

	if _, err := hc.SetRepoCICapabilities(ctx, depotRepo, []string{"gcp-key"}); err != nil {
		t.Fatal(err)
	}
	stored, err := c.Store.GetRepoConfig(ctx, depotRef)
	if err != nil || stored == nil || !reflect.DeepEqual(stored.CICapabilities, []string{"gcp-key"}) {
		t.Fatalf("stored config for %s = %+v, %v", depotRepo, stored, err)
	}
	got, err := hc.RepoDefaults(ctx, depotRepo)
	if err != nil || !reflect.DeepEqual(got.CICapabilities, []string{"gcp-key"}) {
		t.Fatalf("RepoDefaults(%s) = %+v, %v", depotRepo, got, err)
	}
}
