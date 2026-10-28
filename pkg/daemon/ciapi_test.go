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

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/ciapi"
	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ciAPIRegistry is a registry holding what the deployment's does for a
// CI job's api grants: release-gate-read registered by name, and every
// api grant resolved, for CI, to its token-minting provider.
func ciAPIRegistry(gate *ciapi.Endpoint) *model.CapabilityRegistry {
	reg := model.NewCapabilityRegistry(ciapi.NewAlias(gate, ciapi.Config{}, model.ReleaseGateReadCapability,
		ciapi.Aliases[model.ReleaseGateReadCapability]))
	reg.RegisterDynamic(ui.AgentAPIDynamicProvider)
	reg.RegisterCIDynamic(ciapi.DynamicProvider(gate, ciapi.Config{}))
	return reg
}

// release-gate-read (docs/design/release-from-trunk.md §6, RFT-3) and
// any other api grant a job declares are granted to the cron grain
// itself fires and to trusted events, and to nothing an untrusted tree
// can ask for: a pull request's or an ordinary push's run gets no token.
func TestAPIGrantsAreGrantedToTrustedSchedulesAndReleasesOnly(t *testing.T) {
	for _, id := range []string{model.ReleaseGateReadCapability, "api:release-gate-read", "api:ci:read"} {
		t.Run(id, func(t *testing.T) { testTrustedOnly(t, id) })
	}
}

func testTrustedOnly(t *testing.T, id string) {
	gate := ciapi.NewEndpoint()
	gate.SetURL("http://192.0.2.1:9000")
	reg := ciAPIRegistry(gate)
	repo := model.GrainSourceRepo()
	ids := []string{id}

	for _, tc := range []struct {
		event   ci.Event
		trusted bool
		want    bool
	}{
		{ci.EventSchedule, true, true},
		{ci.EventSchedule, false, false},
		{ci.EventRelease, false, true},
		{ci.EventManual, false, true},
		{ci.EventPush, false, false},
		{ci.EventPullRequest, false, false},
	} {
		_, resolved, err := ResolveCICapabilities(context.Background(), reg, nil, repo, "ci-job", tc.event, tc.trusted, ids)
		if err != nil {
			t.Fatalf("%s (trusted %v): %v", tc.event, tc.trusted, err)
		}
		var granted bool
		for _, r := range resolved {
			if r.Grant.Capability == id && !r.Resolution.Refused {
				granted = true
			}
		}
		if granted != tc.want {
			t.Errorf("%s (trusted schedule %v): granted %v (%+v), want %v", tc.event, tc.trusted, granted, resolved, tc.want)
		}
		cc, resolved, _ := ResolveCICapabilities(context.Background(), reg, nil, repo, "ci-job", tc.event, tc.trusted, ids)
		materialized, err := model.MaterializeGrants(context.Background(), reg, cc, resolved)
		if err != nil {
			t.Fatalf("%s: materializing: %v", tc.event, err)
		}
		minted := false
		for _, m := range materialized {
			minted = minted || m.Materialization.Lease != nil
		}
		if minted != tc.want || (gate.Tokens.Len() == 1) != tc.want {
			t.Errorf("%s (trusted schedule %v): minted %v with %d tokens, want %v", tc.event, tc.trusted, minted, gate.Tokens.Len(), tc.want)
		}
		for _, m := range materialized {
			if m.Materialization.Lease != nil {
				_ = m.Provider.Revoke(context.Background(), cc, *m.Materialization.Lease)
			}
		}
	}

	other := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, resolved, err := ResolveCICapabilities(context.Background(), reg, nil, other, "ci-job", ci.EventSchedule, true, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resolved {
		if r.Grant.Capability == id && !r.Resolution.Refused {
			t.Errorf("another repository's trusted schedule was not refused: %+v", resolved)
		}
	}
}

// A dangerous scope is refused to a CI job outright, on the trusted
// events that would carry any other api grant.
func TestADangerousAPIScopeIsRefusedInCI(t *testing.T) {
	gate := ciapi.NewEndpoint()
	gate.SetURL("http://192.0.2.1:9000")
	reg := ciAPIRegistry(gate)
	for _, id := range []string{"api:settings:write", "api:task-admin", "api:secrets:read", "api:deployment:restart"} {
		cc, resolved, err := ResolveCICapabilities(context.Background(), reg, nil, model.GrainSourceRepo(), "ci-job",
			ci.EventRelease, false, []string{id})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		for _, r := range resolved {
			if r.Grant.Capability == id && !r.Resolution.Refused {
				t.Errorf("%s was granted to a release job", id)
			}
		}
		if _, err := model.MaterializeGrants(context.Background(), reg, cc, resolved); err != nil {
			t.Fatal(err)
		}
	}
	if n := gate.Tokens.Len(); n != 0 {
		t.Errorf("%d tokens minted for dangerous scopes", n)
	}
}

// A task's api grant is still the routes its agent may call, not a
// token: the CI provider answers CI lookups only.
func TestATasksAPIGrantIsNotTheCIProvider(t *testing.T) {
	reg := ciAPIRegistry(ciapi.NewEndpoint())
	task, ok := reg.Lookup("api:ci:read")
	if !ok || task.Spec().Provision != model.ProvisionGrant {
		t.Errorf("a task's api:ci:read = %+v, want the agent API's pure grant", task)
	}
	job, ok := reg.LookupFor("api:ci:read", true)
	if !ok || job.Spec().Provision != model.ProvisionMint || !job.Spec().CIOnly {
		t.Errorf("a CI job's api:ci:read = %+v, want the minted CI provider", job)
	}
}

func TestTheDeploymentRegistersReleaseGateRead(t *testing.T) {
	reg := model.NewCapabilityRegistry(CapabilityProviders(Config{}, nil, nil, nil, nil)...)
	provider, ok := reg.Lookup(model.ReleaseGateReadCapability)
	if !ok {
		t.Fatalf("%s has no provider in the deployment's registry", model.ReleaseGateReadCapability)
	}
	if !provider.Spec().SupportsCI() {
		t.Errorf("%s is not offered to CI", model.ReleaseGateReadCapability)
	}
	full := newCapabilityRegistryWithStore(nil, localscripts.Config{})
	if p, ok := full.LookupFor("api:ci:read", true); !ok || !p.Spec().SupportsCI() {
		t.Errorf("api:ci:read has no CI provider in the deployment's registry")
	}
}

// The gate answers with grain's own API handler, in process: a request it
// forwards is the deployment's own, so the access gate in front of every
// API route lets it in without the caller ever holding an access code.
func TestTheCIAPIIsAnsweredByGrainsOwnAPI(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo := model.GrainSourceRepo()
	if _, err := store.SyncRepoDeclarations(context.Background(), repo, "abc123", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path: model.TemplateDeclarationDir + "/release-notes.yml",
			Template: model.Template{
				ID:     model.RepoDeclaredID(model.DeclaredTemplate, repo, "release-notes"),
				Name:   "release-notes",
				Title:  "Release notes for {{version}}",
				Body:   "Write release notes for {{version}}",
				Target: &repo,
				Fields: []model.TemplateField{{Name: "version", Required: true}},
			},
		}},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatal(err)
	}
	gate := ciapi.NewEndpoint()
	gate.Log = func(string, ...any) {}
	gate.SetURL("http://192.0.2.1:9000")
	gate.SetUpstream(ui.NewServer(ui.Config{Actor: ui.DefaultActor("deployment")}, store))
	preset, _, _ := model.ParseAPIGrant("api:" + model.ReleaseGateReadCapability)
	_, token, err := gate.Tokens.Mint(ciapi.Grant{Repo: repo, RunID: "ci-job", Expires: time.Now().Add(time.Hour),
		Scopes: preset.Scopes, Templates: preset.Templates})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/tasks?repo=bwsalmon/grain&limit=100", "/api/ci/runs?ref=main&limit=100",
		"/api/persistent-sandboxes"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "10.0.0.5:4242"
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d %s, want grain's API to answer it", target, w.Code, w.Body)
		}
	}
	// Filing from-template creates an approved (queued) task in grain's store,
	// ready for dispatch without waiting in Proposed for a human.
	postReq := httptest.NewRequest(http.MethodPost, "/api/tasks/from-template", strings.NewReader(`{"fields":{"version":"v2.4.0"}}`))
	postReq.RemoteAddr = "10.0.0.5:4242"
	postReq.Header.Set("Authorization", "Bearer "+token)
	postW := httptest.NewRecorder()
	gate.ServeHTTP(postW, postReq)
	if postW.Code != http.StatusCreated {
		t.Fatalf("POST /api/tasks/from-template = %d %s, want 201 Created", postW.Code, postW.Body)
	}
	var created struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		State    string `json:"state"`
		Approved bool   `json:"approved"`
	}
	if err := json.Unmarshal(postW.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Approved || created.State != string(model.StateQueued) || created.Title != "Release notes for v2.4.0" {
		t.Errorf("filed release-notes task = %+v, want an approved queued task titled 'Release notes for v2.4.0'", created)
	}
	// A route outside the token's scopes is refused, though grain's API
	// answers it.
	for _, target := range []string{"/api/settings", "/api/config", "/api/templates"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want it refused", target, w.Code)
		}
	}
	// The same request straight at the API, from the sandbox's address,
	// is refused: the token is what got it in, not where it came from.
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.RemoteAddr = "10.0.0.5:4242"
	w := httptest.NewRecorder()
	ui.NewServer(ui.Config{Actor: ui.DefaultActor("deployment")}, store).ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Errorf("grain's API answered a sandbox's own request without an access code")
	}
}
