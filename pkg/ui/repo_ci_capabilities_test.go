package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

func TestRepoCICapabilitiesAPIKeepsTaskDefaultsIndependent(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	original := model.RepoConfig{Repo: repo, DefaultCapabilities: []string{"gcp-key"}, PromptExtension: "Instructions", SetupCommand: "make deps"}
	if err := c.Store.PutRepoConfig(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	path := "/api/repos/acme/widgets/ci-capabilities"
	rec := do(t, srv, http.MethodPut, path, `{"ciCapabilities":["github-sandbox","github-sandbox"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.RepoDefaults](t, rec)
	if !reflect.DeepEqual(got.CICapabilities, []string{"github-sandbox"}) || !reflect.DeepEqual(got.DefaultCapabilities, original.DefaultCapabilities) || got.PromptExtension != original.PromptExtension || got.SetupCommand != original.SetupCommand {
		t.Fatalf("saved: %+v", got)
	}
	// package-publish is here beside gcp-key because it is the grant a
	// release workflow is turned on with, and a capability the picker
	// does not offer is one SetRepoCICapabilities refuses to save.
	for _, id := range []string{"gcp-key", "package-publish"} {
		if !slices.Contains(got.AvailableCICapabilities, id) {
			t.Fatalf("CI picker does not offer %q: %v", id, got.AvailableCICapabilities)
		}
	}
	if slices.Contains(got.AvailableCICapabilities, "self-debug") {
		t.Fatalf("CI picker offers %v", got.AvailableCICapabilities)
	}
	rec = do(t, srv, http.MethodGet, path, "")
	if read := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(read, got) {
		t.Fatalf("GET: %+v", read)
	}
	// Cross-repo read and write capabilities should be accepted and deduplicated.
	rec = do(t, srv, http.MethodPut, path, `{"ciCapabilities":["repo-read:acme/tools","repo-read:acme/tools","repo-write:acme/tools","repo-write:acme/tools"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT repo-read/write: %d %s", rec.Code, rec.Body)
	}
	if gotRepoReadWrite := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(gotRepoReadWrite.CICapabilities, []string{"repo-read:acme/tools", "repo-write:acme/tools"}) {
		t.Fatalf("saved repo-read/write: %+v", gotRepoReadWrite)
	}
	for _, id := range []string{"self-debug", "unknown", "github-credential:default", "repo-read:invalid", "repo-write:invalid"} {
		rec = do(t, srv, http.MethodPut, path, `{"ciCapabilities":["`+id+`"]}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d", id, rec.Code)
		}
	}
	rec = do(t, srv, http.MethodGet, "/api/config", "")
	cfg := decode[map[string]any](t, rec)
	if !reflect.DeepEqual(cfg["reposWithCICapabilities"], []any{"acme/widgets"}) {
		t.Fatalf("repo missing: %v", cfg["reposWithCICapabilities"])
	}
	if _, err := c.SetRepoDefaultCapabilities(context.Background(), repo.String(), nil); err != nil {
		t.Fatal(err)
	}
	got, err := c.RepoDefaults(context.Background(), repo.String())
	if err != nil || len(got.CICapabilities) != 2 {
		t.Fatalf("task defaults wiped CI: %+v %v", got, err)
	}
	rec = do(t, srv, http.MethodPut, path, `{"ciCapabilities":[]}`)
	if rec.Code != http.StatusOK || len(decode[ui.RepoDefaults](t, rec).CICapabilities) != 0 {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
}

func TestConfigIncludesCICapabilitiesCatalogWithCIOnlyCapabilities(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config: %d %s", rec.Code, rec.Body)
	}
	var cfg struct {
		Capabilities   []ui.Capability `json:"capabilities"`
		CICapabilities []ui.Capability `json:"ciCapabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}

	// Task capabilities must deliberately exclude CI-only capabilities
	for _, c := range cfg.Capabilities {
		if c.ID == "package-publish" || c.ID == "registry-push" {
			t.Fatalf("task capabilities offer CI-only capability %q: %+v", c.ID, c)
		}
	}

	// CI capabilities catalog must include CI-only capabilities with their names and descriptions
	for _, id := range []string{"package-publish", "registry-push"} {
		idx := slices.IndexFunc(cfg.CICapabilities, func(c ui.Capability) bool { return c.ID == id })
		if idx < 0 {
			t.Fatalf("ciCapabilities does not offer %q: %+v", id, cfg.CICapabilities)
		}
		item := cfg.CICapabilities[idx]
		if item.Name == "" || item.Description == "" {
			t.Fatalf("ciCapabilities %q missing name or description: %+v", id, item)
		}
	}
}
