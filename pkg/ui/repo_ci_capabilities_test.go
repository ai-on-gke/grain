// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestRepoCICapabilitiesAPIKeepsTaskDefaultsIndependent(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	original := model.RepoConfig{Repo: repo, DefaultCapabilities: []string{model.GCPKeySandboxAgentGrant}, PromptExtension: "Instructions", SetupCommand: "make deps"}
	if err := c.Store.PutRepoConfig(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	path := "/api/repos/acme/widgets/ci-capabilities"
	rec := do(t, srv, http.MethodPut, path, `{"ciCapabilities":["https://grain.dev/capabilities/github#sandbox-repo={}","https://grain.dev/capabilities/github#sandbox-repo={}"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.RepoDefaults](t, rec)
	if !reflect.DeepEqual(got.CICapabilities, []string{model.GitHubSandboxRepoGrant}) || !reflect.DeepEqual(got.DefaultCapabilities, original.DefaultCapabilities) || got.PromptExtension != original.PromptExtension || got.SetupCommand != original.SetupCommand {
		t.Fatalf("saved: %+v", got)
	}
	// package-publish is here beside gcp-key because it is the grant a
	// release workflow is turned on with, and a capability the picker
	// does not offer is one SetRepoCICapabilities refuses to save.
	for _, id := range []string{model.GitHubSandboxRepoGrant, model.PackagePublishRef + "={}"} {
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
	if gotRepoReadWrite := decode[ui.RepoDefaults](t, rec); !reflect.DeepEqual(gotRepoReadWrite.CICapabilities, []string{model.RepoReadGrant(model.RepoRef{Owner: "acme", Name: "tools"}), model.RepoWriteGrant(model.RepoRef{Owner: "acme", Name: "tools"})}) {
		t.Fatalf("saved repo-read/write: %+v", gotRepoReadWrite)
	}
	for _, id := range []string{
		"self-debug",
		"unknown",
		"api:tasks:read",
		model.APIRef("tasks:read"),
		model.APIGrantString("tasks:read", "acme/widgets"),
		"github-credential:default",
		model.RepoCredentialGrant("default"),
		"repo-read:invalid",
		"repo-write:invalid",
	} {
		rec = do(t, srv, http.MethodPut, path, `{"ciCapabilities":[`+mustJSON(t, id)+`]}`)
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

	// An unsupported bare id already stored in repo_config.ci_capabilities (e.g.
	// from an older schema) does not block editing unrelated CI grants on the repo.
	if err := c.Store.PutRepoConfig(context.Background(), model.RepoConfig{
		Repo:           repo,
		CICapabilities: []string{"self-debug", model.RepoReadGrant(model.RepoRef{Owner: "acme", Name: "tools"})},
	}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, srv, http.MethodPut, path, `{"ciCapabilities":["self-debug","repo-read:acme/tools","repo-read:acme/other"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT preserving stored bare id: %d %s", rec.Code, rec.Body)
	}
	wantPreserved := []string{
		"self-debug",
		model.RepoReadGrant(model.RepoRef{Owner: "acme", Name: "tools"}),
		model.RepoReadGrant(model.RepoRef{Owner: "acme", Name: "other"}),
	}
	if gotPreserved := decode[ui.RepoDefaults](t, rec).CICapabilities; !reflect.DeepEqual(gotPreserved, wantPreserved) {
		t.Fatalf("preserved ciCapabilities = %v, want %v", gotPreserved, wantPreserved)
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
	for _, id := range []string{model.PackagePublishRef + "={}", model.RegistryPushRef + "={}"} {
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
