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
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type stubPackageCredentials struct{}

func (stubPackageCredentials) PackageCredential(registry, owner, repo string) (packagepublish.Credential, error) {
	return packagepublish.Credential{Registry: "ghcr.io", Username: "bot", Token: "tok", Name: "stub"}, nil
}

// ciAPIRegistry is a registry holding what the deployment's does for a
// CI job's api grants and event-gated capabilities: release-gate-read
// and package-publish registered by name, repo-write dynamic provider,
// and every api grant resolved, for CI, to its token-minting provider.
func ciAPIRegistry(gate *ciapi.Endpoint) *model.CapabilityRegistry {
	reg := model.NewCapabilityRegistry(
		ciapi.NewAlias(gate, ciapi.Config{}, model.ReleaseGateReadCapability,
			ciapi.Aliases[model.ReleaseGateReadCapability]),
		packagepublish.New(packagepublish.Config{Credentials: stubPackageCredentials{}}),
	)
	reg.RegisterDynamic(ui.AgentAPIDynamicProvider)
	reg.RegisterCIDynamic(ciapi.DynamicProvider(gate, ciapi.Config{}))
	return reg
}

// release-gate-read (design/design/release-from-trunk.design.md §6, RFT-3) and
// any other api grant a job declares are granted to the cron grain
// itself fires and to trusted events, and to nothing an untrusted tree
// can ask for: a pull request's, an ordinary push's, or an agent's
// withheld workflow_dispatch/release run gets no token.
func TestAPIGrantsAreGrantedToTrustedSchedulesAndReleasesOnly(t *testing.T) {
	for _, id := range []string{
		model.ReleaseGateReadCapability,
		"api:release-gate-read",
		"api:ci:read",
		model.APIRef(model.ReleaseGateReadCapability),
		model.APIRef("ci:read"),
	} {
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
		event    ci.Event
		trusted  bool
		withhold bool
		want     bool
	}{
		{ci.EventSchedule, true, false, true},
		{ci.EventSchedule, false, false, false},
		{ci.EventRelease, false, false, true},
		{ci.EventRelease, false, true, false},
		{ci.EventManual, false, false, true},
		{ci.EventManual, false, true, false},
		{ci.EventPush, false, false, false},
		{ci.EventPullRequest, false, false, false},
	} {
		_, resolved, err := ResolveCICapabilities(context.Background(), reg, nil, repo, "ci-job", tc.event, tc.trusted, ids, tc.withhold)
		if err != nil {
			t.Fatalf("%s (trusted %v, withhold %v): %v", tc.event, tc.trusted, tc.withhold, err)
		}
		var granted bool
		for _, r := range resolved {
			if r.Grant.Capability == model.StoredGrantSpelling(id) && !r.Resolution.Refused {
				granted = true
			}
		}
		if granted != tc.want {
			t.Errorf("%s (trusted schedule %v, withhold %v): granted %v (%+v), want %v",
				tc.event, tc.trusted, tc.withhold, granted, resolved, tc.want)
		}
		cc, resolved, _ := ResolveCICapabilities(context.Background(), reg, nil, repo, "ci-job", tc.event, tc.trusted, ids, tc.withhold)
		materialized, err := model.MaterializeGrants(context.Background(), reg, cc, resolved)
		if err != nil {
			t.Fatalf("%s: materializing: %v", tc.event, err)
		}
		minted := false
		for _, m := range materialized {
			minted = minted || m.Materialization.Lease != nil
		}
		if minted != tc.want || (gate.Tokens.Len() == 1) != tc.want {
			t.Errorf("%s (trusted schedule %v, withhold %v): minted %v with %d tokens, want %v",
				tc.event, tc.trusted, tc.withhold, minted, gate.Tokens.Len(), tc.want)
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
		if r.Grant.Capability == model.StoredGrantSpelling(id) && !r.Resolution.Refused {
			t.Errorf("another repository's trusted schedule was not refused: %+v", resolved)
		}
	}
}

// Event-gated capabilities (repo-write:* and package-publish) are
// withheld when a workflow_dispatch or release request is marked as not
// a person's, while human dispatches, trusted releases, and trusted
// schedules still receive them.
func TestEventGatedCapabilitiesAreWithheldFromNonHumanDispatches(t *testing.T) {
	gate := ciapi.NewEndpoint()
	gate.SetURL("http://192.0.2.1:9000")
	reg := ciAPIRegistry(gate)
	repo := model.GrainSourceRepo()

	for _, tc := range []struct {
		id              string
		trustedSchedule bool
	}{
		{"repo-write:bwsalmon/grain", true},
		{model.RepoWriteGrant(model.RepoRef{Owner: "bwsalmon", Name: "grain"}), true},
		{packagepublish.CapabilityName, true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			for _, c := range []struct {
				name     string
				event    ci.Event
				trusted  bool
				withhold bool
				want     bool
			}{
				{"human workflow_dispatch", ci.EventManual, false, false, true},
				{"agent workflow_dispatch withheld", ci.EventManual, false, true, false},
				{"trusted release", ci.EventRelease, false, false, true},
				{"agent release withheld", ci.EventRelease, false, true, false},
				{"trusted schedule", ci.EventSchedule, true, false, tc.trustedSchedule},
				{"untrusted schedule", ci.EventSchedule, false, false, false},
				{"push", ci.EventPush, false, false, false},
				{"pull_request", ci.EventPullRequest, false, false, false},
			} {
				var granted bool
				if _, ok := model.CIGrantRuleFor(tc.id); ok {
					// The release server's method: its declared rule
					// decides whether the job's pass carries it, and a
					// withheld run is handed none of it.
					granted = len(ciCapabilityRefs([]string{tc.id}, c.event, c.trusted, c.withhold)) == 1
				} else if model.IsRepoAccessGrant(tc.id) {
					// No provider: the grant's own gate decides the
					// proxy scope grantRepoAccess widens.
					granted = ciRepoAccessAdmitted(tc.id, c.event, c.trusted, c.withhold)
				} else {
					_, resolved, err := ResolveCICapabilities(context.Background(), reg, nil, repo, "c1234-0",
						c.event, c.trusted, []string{tc.id}, c.withhold)
					if err != nil {
						t.Fatalf("%s: %v", c.name, err)
					}
					for _, r := range resolved {
						if r.Grant.Capability == tc.id && !r.Resolution.Refused {
							granted = true
						}
					}
				}
				if granted != c.want {
					t.Errorf("%s: granted=%v, want %v", c.name, granted, c.want)
				}
			}
		})
	}

	if ciHoldsRepoAccess(ci.EventManual, false, []string{"repo-write:bwsalmon/grain"}, true) {
		t.Error("ciHoldsRepoAccess admitted repo-write on a withheld manual run")
	}
	if !ciHoldsRepoAccess(ci.EventManual, false, []string{"repo-write:bwsalmon/grain"}, false) {
		t.Error("ciHoldsRepoAccess refused repo-write on a human manual run")
	}
}

// A dangerous scope is refused to a CI job outright, on the trusted
// events that would carry any other api grant.
func TestADangerousAPIScopeIsRefusedInCI(t *testing.T) {
	gate := ciapi.NewEndpoint()
	gate.SetURL("http://192.0.2.1:9000")
	reg := ciAPIRegistry(gate)
	for _, id := range []string{
		"api:settings:write", "api:task-admin", "api:secrets:read", "api:deployment:restart",
		model.APIRef("settings:write"), model.APIRef("task-admin"), model.APIRef("secrets:read"), model.APIRef("deployment:restart"),
		model.APIRef("ci:read") + `={"bogus":1}`,
	} {
		cc, resolved, err := ResolveCICapabilities(context.Background(), reg, nil, model.GrainSourceRepo(), "ci-job",
			ci.EventRelease, false, []string{id})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		found := false
		for _, r := range resolved {
			if r.Grant.Capability == model.StoredGrantSpelling(id) {
				found = true
				if !r.Resolution.Refused {
					t.Errorf("%s was granted to a release job", id)
				}
			}
		}
		if !found {
			t.Errorf("%s was silently skipped instead of resolved as refused", id)
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

func TestTheDeploymentServesReleaseGateReadToCIOnly(t *testing.T) {
	// Slice 4 of design/design/unified-grants.design.md retired the static
	// release-gate-read provider: the bare name and its api reference both
	// resolve through ciapi.DynamicProvider, for a CI job only.
	if got := CapabilityProviders(Config{}, nil, nil, nil, nil); len(got) != 0 {
		t.Errorf("CapabilityProviders = %d providers, want none (release-gate-read is a CI alias, not a provider)", len(got))
	}
	full := newCapabilityRegistryWithStore(nil)
	for _, name := range []string{model.ReleaseGateReadCapability, model.APIRef(model.ReleaseGateReadCapability), "api:ci:read"} {
		if p, ok := full.LookupFor(name, true); !ok || !p.Spec().SupportsCI() {
			t.Errorf("%s has no CI provider in the deployment's registry", name)
		}
	}
	if _, ok := full.Lookup(model.ReleaseGateReadCapability); ok {
		t.Errorf("%s resolves outside CI; it is a CI job's alias only", model.ReleaseGateReadCapability)
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
	for _, target := range []string{"/api/tasks?repo=ai-on-gke/grain&limit=100", "/api/ci/runs?ref=main&limit=100",
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

// A CI job's capability context carries the job's own deadline (its run's
// ceiling, ctx's deadline, and the teardown after it), so what a provider
// mints for the job is bounded by the job rather than by the provider's
// longest lease: the walk's package-publish refresh tokens were all good
// for 6h whatever the run's ceiling, because nothing set it
// (docs/test-runs/2026-11-03-capabilities-ci-repo.md, F3). A job with no
// deadline still states none.
func TestACIJobsCapabilityContextCarriesItsDeadline(t *testing.T) {
	gate := ciapi.NewEndpoint()
	reg := ciAPIRegistry(gate)
	refresh := packagepublish.NewRefresher()
	refresh.Log = func(string, ...any) {}
	reg.Register(packagepublish.New(packagepublish.Config{
		Credentials: stubPackageCredentials{}, Refresh: refresh,
		RefreshURL: func() string { return "http://192.0.2.1:9000" },
	}))
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	end := time.Now().Add(90 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), end)
	defer cancel()
	cc, resolved, err := ResolveCICapabilities(ctx, reg, nil, repo, "c1234-0", ci.EventManual, false,
		[]string{packagepublish.CapabilityName})
	if err != nil {
		t.Fatal(err)
	}
	if want := end.Add(ciReleaseTimeout).UTC(); !cc.Deadline.Equal(want) {
		t.Errorf("Deadline = %v, want the job's %v", cc.Deadline, want)
	}
	materialized, err := model.MaterializeGrants(context.Background(), reg, cc, resolved)
	if err != nil {
		t.Fatal(err)
	}
	leased := false
	for _, m := range materialized {
		if l := m.Materialization.Lease; l != nil && l.Capability == packagepublish.CapabilityName {
			leased = true
			if l.ExpiresAt == nil || !l.ExpiresAt.Equal(cc.Deadline) {
				t.Errorf("package-publish lease expires %v, want the job's deadline %v", l.ExpiresAt, cc.Deadline)
			}
		}
	}
	if !leased {
		t.Errorf("no package-publish lease was minted: %+v", materialized)
	}

	cc, _, err = ResolveCICapabilities(context.Background(), reg, nil, repo, "c1234-1", ci.EventManual, false,
		[]string{packagepublish.CapabilityName})
	if err != nil {
		t.Fatal(err)
	}
	if !cc.Deadline.IsZero() {
		t.Errorf("Deadline = %v for a job with no deadline, want none", cc.Deadline)
	}
}

func TestCheckCIJobRepoAccessPolicyEnforcesPolicyOnCIAPIGrants(t *testing.T) {
	pol := model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: model.APIRef("ci:read"), Deny: true},
			{Capability: model.APIRef(model.ReleaseGateReadCapability), Deny: true},
		},
	}
	for _, capID := range []string{"api:ci:read", model.APIRef("ci:read"), model.ReleaseGateReadCapability} {
		if err := CheckCIJobRepoAccessPolicy(pol, nil, ci.SandboxRequest{
			Repo: "ai-on-gke/grain", Event: ci.EventRelease, Capabilities: []string{capID},
		}); err == nil {
			t.Errorf("release job with %q admitted despite CapabilityPolicy deny rule", capID)
		}
		// On pull_request, ciapi is withheld by its event gate and must not fail PR runs.
		if err := CheckCIJobRepoAccessPolicy(pol, nil, ci.SandboxRequest{
			Repo: "ai-on-gke/grain", Event: ci.EventPullRequest, Untrusted: true, Capabilities: []string{capID},
		}); err != nil {
			t.Errorf("pull_request job with withheld %q failed policy check: %v", capID, err)
		}
	}
	if refs := ciCapabilityRefs([]string{model.APIRef("ci:read")}, ci.EventRelease, false, false); len(refs) != 0 {
		t.Errorf("ciCapabilityRefs included ciapi ref %+v in protocol pass grants", refs)
	}
}
