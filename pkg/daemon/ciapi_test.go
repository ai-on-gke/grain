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
	"fmt"
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
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
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

// grain_config.ci_api_repos offers a CI job's api grants to the
// repositories it lists, beside grain's own: a listed repository's
// trusted job is honoured, an unlisted one's is still refused, and only
// on the events that were always trusted.
func TestCIAPIReposOffersTheAPIToTheReposItLists(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	listed := model.RepoRef{Host: "git.example.com", Owner: "acme", Name: "widgets"}
	unlisted := model.RepoRef{Owner: "acme", Name: "gadgets"}
	ids := []string{"api:tasks:read", "api:tasks:from-template"}
	// newCapabilityRegistryWithStore resolves into the process's CIAPI,
	// which refuses every job while it is not served.
	was := CIAPI.URL()
	CIAPI.SetURL("http://192.0.2.1:9000")
	t.Cleanup(func() { CIAPI.SetURL(was) })

	granted := func(t *testing.T, repo model.RepoRef, event ci.Event, trusted bool) bool {
		t.Helper()
		_, resolved, err := ResolveCICapabilities(ctx, newCapabilityRegistryWithStore(store), nil, repo, "ci-job", event, trusted, ids)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range resolved {
			if !r.Resolution.Refused {
				n++
			} else {
				t.Logf("%s: %+v", repo, r.Resolution)
			}
		}
		return n == len(ids)
	}

	if granted(t, listed, ci.EventSchedule, true) {
		t.Errorf("%s's trusted schedule was honoured before ci_api_repos listed it", listed)
	}
	if !granted(t, model.GrainSourceRepo(), ci.EventSchedule, true) {
		t.Errorf("grain's own trusted schedule was refused with ci_api_repos empty")
	}

	if err := store.PutConfig(ctx, model.Config{CIAPIRepos: []string{"git.example.com/acme/widgets", "not a repo"}}); err != nil {
		t.Fatal(err)
	}
	// Read per registry build, so this is the next job after a reload.
	for _, tc := range []struct {
		repo    model.RepoRef
		event   ci.Event
		trusted bool
		want    bool
	}{
		{listed, ci.EventSchedule, true, true},
		{listed, ci.EventManual, false, true},
		{listed, ci.EventRelease, false, true},
		{listed, ci.EventSchedule, false, false},
		{listed, ci.EventPush, false, false},
		{listed, ci.EventPullRequest, false, false},
		{unlisted, ci.EventSchedule, true, false},
		{unlisted, ci.EventManual, false, false},
		{model.GrainSourceRepo(), ci.EventSchedule, true, true},
	} {
		if got := granted(t, tc.repo, tc.event, tc.trusted); got != tc.want {
			t.Errorf("%s %s (trusted schedule %v): granted %v, want %v", tc.repo, tc.event, tc.trusted, got, tc.want)
		}
	}

	// The CI scope allowlist still applies to a listed repository.
	_, resolved, err := ResolveCICapabilities(ctx, newCapabilityRegistryWithStore(store), nil, listed, "ci-job", ci.EventManual, false, []string{"api:operator"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resolved {
		if !r.Resolution.Refused {
			t.Errorf("a listed repository's job was granted %s: %+v", r.Grant.Capability, r)
		}
	}
}

// A non-grain repository opted in via ci_api_repos gets a real API token
// held strictly to its own repository across grain's actual ui.Server and
// namespace handlers (including /api/fs/task, /api/tasks/{id}/* sub-routes,
// attachments, events, states, and repository-less tasks), and its
// POST /api/tasks/from-template filings obey DeclarationAcceptance holds,
// person-only settings, and repository bounds.
func TestAListedRepositorysCIAPITokenIsIsolatedEndToEnd(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	grainRepo := model.GrainSourceRepo()
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Now().UTC()

	api := &inProcessAPI{}
	ns := &namespace.Server{
		NewRedactor: func() *redact.Redactor { return BuildNamespaceRedactor(dataDir) },
	}
	WireSandboxNamespace(ns, SandboxNamespaceConfig{
		DataDir:      dataDir,
		Store:        store,
		InsecureHTTP: true,
		API:          api,
	})
	client := ui.NewClient(ui.Config{
		DataDir:      dataDir,
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
		PullRequests: PullRequestGate{},
	}, store)
	uiSrv := ui.NewServerWithClient(client)
	uiSrv.SetNamespace(func() *namespace.Server { return ns })
	api.Set(uiSrv)

	detailOf := func(id string) ui.TaskDetail {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+id, nil)
		req.RemoteAddr = ""
		w := httptest.NewRecorder()
		uiSrv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("in-process GET /api/tasks/%s = %d %s", id, w.Code, w.Body)
		}
		var d ui.TaskDetail
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	// Seed a grain task (with an attachment and comment), a repository-less
	// standalone task, and an acme/widgets task.
	grainTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Grain secret task",
		Description: "sensitive grain prompt body",
		Repo:        grainRepo.String(),
		Attachments: []ui.AttachmentUpload{{Filename: "secret.txt", ContentType: "text/plain", Content: "Z3JhaW4tc2VjcmV0"}},
	})
	if err != nil {
		t.Fatalf("creating grain task: %v", err)
	}
	grainDetail := detailOf(grainTask.ID)
	if len(grainDetail.Attachments) != 1 {
		t.Fatalf("grain task attachments = %+v", grainDetail.Attachments)
	}
	noRepoTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Standalone operator task",
		Description: "sensitive standalone body",
		NoRepo:      true,
	})
	if err != nil {
		t.Fatalf("creating no-repo task: %v", err)
	}
	widgetsTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Widgets own task",
		Description: "widgets task body",
		Repo:        widgets.String(),
		Attachments: []ui.AttachmentUpload{{Filename: "widgets.txt", ContentType: "text/plain", Content: "d2lkZ2V0cy1kYXRh"}},
	})
	if err != nil {
		t.Fatalf("creating widgets task: %v", err)
	}
	widgetsDetail := detailOf(widgetsTask.ID)
	if len(widgetsDetail.Attachments) != 1 {
		t.Fatalf("widgets task attachments = %+v", widgetsDetail.Attachments)
	}

	// Declare templates in acme/widgets: one plain, one asking for a
	// dangerous scope, one asking for bypass admission, one with keep_running,
	// one carrying a declared push set, and one reaching ai-on-gke/grain.
	adminGrant := "api:capabilities:admin"
	adminTmplID := model.RepoDeclaredID(model.DeclaredTemplate, widgets, "admin-op")
	if _, err := store.SyncRepoDeclarations(ctx, widgets, "c1", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{
			{
				Path: model.TemplateDeclarationDir + "/triage.yml",
				Template: model.Template{
					ID:     model.RepoDeclaredID(model.DeclaredTemplate, widgets, "triage"),
					Name:   "triage",
					Title:  "Triage {{item}}",
					Body:   "Triage {{item}}",
					Target: &widgets,
					Fields: []model.TemplateField{{Name: "item", Required: true}},
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/admin-op.yml",
				Template: model.Template{
					ID:     adminTmplID,
					Name:   "admin-op",
					Title:  "Admin task",
					Body:   "Needs admin",
					Target: &widgets,
					Grants: []model.Grant{{Capability: adminGrant}},
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/bypass-op.yml",
				Template: model.Template{
					ID:        model.RepoDeclaredID(model.DeclaredTemplate, widgets, "bypass-op"),
					Name:      "bypass-op",
					Title:     "Bypass task",
					Body:      "Needs bypass",
					Target:    &widgets,
					Admission: model.AdmissionBypass,
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/standing.yml",
				Template: model.Template{
					ID:          model.RepoDeclaredID(model.DeclaredTemplate, widgets, "standing"),
					Name:        "standing",
					Title:       "Keep running task",
					Body:        "Stays running",
					Target:      &widgets,
					KeepRunning: true,
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/tagger.yml",
				Template: model.Template{
					ID:     model.RepoDeclaredID(model.DeclaredTemplate, widgets, "tagger"),
					Name:   "tagger",
					Title:  "Push tags",
					Body:   "Tags release",
					Target: &widgets,
				},
				Push: model.RefPushSet{{Pattern: "refs/tags/v*"}},
			},
			{
				Path: model.TemplateDeclarationDir + "/cross-write.yml",
				Template: model.Template{
					ID:     model.RepoDeclaredID(model.DeclaredTemplate, widgets, "cross-write"),
					Name:   "cross-write",
					Title:  "Touch grain",
					Body:   "Cross-repo write",
					Target: &widgets,
					Writes: []model.WriteTarget{{Repo: grainRepo}},
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/cross-read.yml",
				Template: model.Template{
					ID:     model.RepoDeclaredID(model.DeclaredTemplate, widgets, "cross-read"),
					Name:   "cross-read",
					Title:  "Read grain",
					Body:   "Cross-repo read",
					Target: &widgets,
					Reads:  []model.RepoRef{grainRepo},
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/cross-grant.yml",
				Template: model.Template{
					ID:     model.RepoDeclaredID(model.DeclaredTemplate, widgets, "cross-grant"),
					Name:   "cross-grant",
					Title:  "Grant grain read",
					Body:   "Cross-repo api grant",
					Target: &widgets,
					Grants: []model.Grant{{Capability: "api:tasks:read@" + grainRepo.String()}},
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/unscoped-read.yml",
				Template: model.Template{
					ID:     model.RepoDeclaredID(model.DeclaredTemplate, widgets, "unscoped-read"),
					Name:   "unscoped-read",
					Title:  "Deployment-wide tasks:read",
					Body:   "Unscoped api grant",
					Target: &widgets,
					Grants: []model.Grant{{Capability: "api:tasks:read"}},
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/repo-write-api.yml",
				Template: model.Template{
					ID:     model.RepoDeclaredID(model.DeclaredTemplate, widgets, "repo-write-api"),
					Name:   "repo-write-api",
					Title:  "Repo settings write",
					Body:   "Non-CI api write scope",
					Target: &widgets,
					Grants: []model.Grant{{Capability: "api:repos:write@" + widgets.String()}},
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/grain-source.yml",
				Template: model.Template{
					ID:     model.RepoDeclaredID(model.DeclaredTemplate, widgets, "grain-source"),
					Name:   "grain-source",
					Title:  "Grain source",
					Body:   "Deployment daemon capability",
					Target: &widgets,
					Grants: []model.Grant{{Capability: model.GrainSourceGrant}},
				},
			},
			{
				Path: model.TemplateDeclarationDir + "/own-ci-read.yml",
				Template: model.Template{
					ID:     model.RepoDeclaredID(model.DeclaredTemplate, widgets, "own-ci-read"),
					Name:   "own-ci-read",
					Title:  "Own CI read",
					Body:   "Repo-narrowed CI read",
					Target: &widgets,
					Grants: []model.Grant{{Capability: "api:ci:read@" + widgets.String()}},
				},
			},
		},
	}, now, time.UTC); err != nil {
		t.Fatalf("syncing widgets declarations: %v", err)
	}

	gate := ciapi.NewEndpoint()
	gate.Log = func(string, ...any) {}
	gate.SetURL("http://192.0.2.1:9000")
	gate.SetUpstream(uiSrv)
	_, token, err := gate.Tokens.Mint(ciapi.Grant{
		Repo:        widgets,
		RunID:       "ci-widgets-1",
		Expires:     now.Add(time.Hour),
		Scopes:      []model.APIScope{"tasks:read", "tasks:from-template", "ci:read", "releases:read", "repos:read", "dispatch:read"},
		AnyTemplate: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		var r *strings.Reader
		if body != "" {
			r = strings.NewReader(body)
		} else {
			r = strings.NewReader("")
		}
		req := httptest.NewRequest(method, target, r)
		req.RemoteAddr = "10.0.0.5:4242"
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, req)
		return w
	}

	// 1. GET /api/tasks returns only acme/widgets' own task, never grain's or no-repo's.
	listW := do(http.MethodGet, "/api/tasks", "")
	if listW.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks = %d %s", listW.Code, listW.Body)
	}
	var listedTasks []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listedTasks); err != nil {
		t.Fatalf("decoding /api/tasks: %v (%s)", err, listW.Body)
	}
	if len(listedTasks) != 1 || listedTasks[0].ID != widgetsTask.ID {
		t.Errorf("GET /api/tasks = %+v, want only widgets task %s", listedTasks, widgetsTask.ID)
	}

	// 2. Cross-repo and no-repo task sub-routes, namespace files, attachments,
	// and un-narrowed deployment routes are all 403 Forbidden.
	for _, blocked := range []string{
		"/api/tasks/" + grainTask.ID,
		"/api/tasks/" + grainTask.ID + "/prompt",
		"/api/tasks/" + grainTask.ID + "/messages",
		"/api/tasks/" + grainTask.ID + "/diff-preview",
		"/api/tasks/" + grainTask.ID + "/tree",
		"/api/tasks/" + grainTask.ID + "/attempts/1/transcript",
		"/api/tasks/" + grainTask.ID + "/attempts/1/capability-audit",
		fmt.Sprintf("/api/tasks/%s/attachments/%d", grainTask.ID, grainDetail.Attachments[0].ID),
		"/api/tasks/" + grainTask.ID + "/context",
		"/api/fs/task/" + grainTask.ID + "/record.md",
		"/api/tasks/" + noRepoTask.ID,
		"/api/tasks/" + noRepoTask.ID + "/prompt",
		"/api/fs/task/" + noRepoTask.ID + "/record.md",
		"/api/fs/ci/ai-on-gke/grain",
		"/api/ci/runs/gha:ai-on-gke:grain:123/log",
		"/api/tasks/states",
		"/api/states",
		"/api/tasks/wait?ids=" + grainTask.ID + "&timeout=1",
		"/api/release-approval-key",
		"/api/git-ambient-sources",
		"/api/pause",
		"/api/pipeline-pause",
	} {
		if w := do(http.MethodGet, blocked, ""); w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d %s, want 403 Forbidden", blocked, w.Code, w.Body)
		}
	}

	// Meanwhile acme/widgets' own task, prompt, attachment, namespace record,
	// and event feed remain readable and narrowed to acme/widgets.
	for _, allowed := range []string{
		"/api/tasks/" + widgetsTask.ID,
		"/api/tasks/" + widgetsTask.ID + "/prompt",
		fmt.Sprintf("/api/tasks/%s/attachments/%d", widgetsTask.ID, widgetsDetail.Attachments[0].ID),
		"/api/fs/task/" + widgetsTask.ID + "/record.md",
		"/api/events",
	} {
		w := do(http.MethodGet, allowed, "")
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200 OK", allowed, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "Grain secret task") || strings.Contains(w.Body.String(), "Standalone operator task") {
			t.Errorf("GET %s leaked foreign task content: %s", allowed, w.Body)
		}
	}

	// Batch task summaries narrow per task: widgetsTask is returned, while
	// grainTask (foreign repo), noRepoTask (repo-less), and missing ids are
	// silently omitted and indistinguishable.
	sumW := do(http.MethodGet, fmt.Sprintf("/api/tasks/summaries?ids=%s,%s,%s,999999&comments_limit=1&attempts_limit=1",
		widgetsTask.ID, grainTask.ID, noRepoTask.ID), "")
	if sumW.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks/summaries = %d %s", sumW.Code, sumW.Body)
	}
	var sumResp struct {
		Tasks map[string]ui.TaskDetail `json:"tasks"`
	}
	if err := json.Unmarshal(sumW.Body.Bytes(), &sumResp); err != nil {
		t.Fatalf("decoding /api/tasks/summaries: %v (%s)", err, sumW.Body)
	}
	if len(sumResp.Tasks) != 1 {
		t.Fatalf("GET /api/tasks/summaries = %+v, want only widgetsTask %s", sumResp.Tasks, widgetsTask.ID)
	}
	if _, ok := sumResp.Tasks[widgetsTask.ID]; !ok {
		t.Fatalf("GET /api/tasks/summaries missing widgetsTask %s", widgetsTask.ID)
	}
	if fSum, mSum := do(http.MethodGet, "/api/tasks/summaries?ids="+grainTask.ID, ""), do(http.MethodGet, "/api/tasks/summaries?ids=999999", ""); fSum.Code != http.StatusOK || mSum.Code != http.StatusOK || fSum.Body.String() != mSum.Body.String() {
		t.Errorf("CI summaries foreign vs missing diverged: foreign=(%d %q) missing=(%d %q)",
			fSum.Code, fSum.Body.String(), mSum.Code, mSum.Body.String())
	}
	if badRepoW := do(http.MethodGet, "/api/tasks/summaries?ids="+grainTask.ID+"&repo=ai-on-gke/grain", ""); badRepoW.Code != http.StatusForbidden {
		t.Errorf("CI summaries with foreign ?repo= = %d %s, want 403", badRepoW.Code, badRepoW.Body)
	}

	// 3. Plain template files approved and records automation attribution
	// rather than claiming a human operator filed it.
	plainW := do(http.MethodPost, "/api/tasks/from-template", `{"template":"triage","fields":{"item":"bug-1"}}`)
	if plainW.Code != http.StatusCreated {
		t.Fatalf("filing plain triage template = %d %s, want 201", plainW.Code, plainW.Body)
	}
	var plainCreated ui.Task
	if err := json.Unmarshal(plainW.Body.Bytes(), &plainCreated); err != nil {
		t.Fatal(err)
	}
	if !plainCreated.Approved || plainCreated.State != model.StateQueued {
		t.Errorf("plain template filed as approved=%v state=%s, want approved queued", plainCreated.Approved, plainCreated.State)
	}
	storedPlain, err := store.GetTask(ctx, plainCreated.ID)
	if err != nil || storedPlain == nil {
		t.Fatalf("reading stored plain task: %v", err)
	}
	if storedPlain.Origin.Attribution.Actor.Kind != model.PrincipalAutomation || storedPlain.Origin.Attribution.Actor.ID != "ci-api" {
		t.Errorf("plain task Origin.Attribution = %+v, want automation:ci-api", storedPlain.Origin.Attribution)
	}

	// 4. Templates reaching another repository via reads, writes, or API grants are refused (403).
	for _, stem := range []string{"cross-write", "cross-read", "cross-grant"} {
		if w := do(http.MethodPost, "/api/tasks/from-template", `{"template":"`+stem+`"}`); w.Code != http.StatusForbidden {
			t.Errorf("filing %s = %d %s, want 403 Forbidden", stem, w.Code, w.Body)
		}
	}

	// Meanwhile a template carrying a repo-narrowed CI-admitted API grant on
	// acme/widgets itself still lands approved.
	ownCIW := do(http.MethodPost, "/api/tasks/from-template", `{"template":"own-ci-read"}`)
	if ownCIW.Code != http.StatusCreated {
		t.Fatalf("filing own-ci-read = %d %s, want 201 Created", ownCIW.Code, ownCIW.Body)
	}
	var ownCITask ui.Task
	if err := json.Unmarshal(ownCIW.Body.Bytes(), &ownCITask); err != nil {
		t.Fatal(err)
	}
	if !ownCITask.Approved || ownCITask.State != model.StateQueued {
		t.Errorf("own-ci-read landed approved=%v state=%s, want approved queued", ownCITask.Approved, ownCITask.State)
	}

	// 5. Templates asking for unaccepted person-held grants, deployment-wide or
	// non-CI API/daemon grants, admission bypass, keep_running, or a declared
	// push set land unapproved (StateProposed) waiting for a person rather than
	// self-approving.
	for _, stem := range []string{"admin-op", "unscoped-read", "repo-write-api", "grain-source", "bypass-op", "standing", "tagger"} {
		w := do(http.MethodPost, "/api/tasks/from-template", `{"template":"`+stem+`"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("filing %s = %d %s, want 201 Created (held unapproved)", stem, w.Code, w.Body)
		}
		var created ui.Task
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if created.Approved || created.State != model.StateProposed {
			t.Errorf("unaccepted %s landed approved=%v state=%s, want unapproved proposed", stem, created.Approved, created.State)
		}
	}

	// 6. POST /api/notifications/release-pending requires the full release-gate-read
	// scopes (including sandboxes:read), and even with them cannot overwrite an open
	// pending-release notification belonging to another repository.
	if w := do(http.MethodPost, "/api/notifications/release-pending", `{"version":"v2.4.0","body":"spoof"}`); w.Code != http.StatusForbidden {
		t.Errorf("release-pending without sandboxes:read = %d %s, want 403 Forbidden", w.Code, w.Body)
	}
	grainSubj := model.NotificationSubject(model.FamilyReleasePending, "v2.4.0")
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  grainSubj,
		Severity: model.NotificationWarning,
		Title:    "v2.4.0 is ready to promote",
		Body:     "official grain soak summary",
		Repo:     grainRepo.String(),
		RaisedBy: "automation:grain",
		Actions:  model.ReleasePendingActions,
	}); err != nil {
		t.Fatalf("seeding grain release-pending notification: %v", err)
	}
	_, widgetsGateToken, err := gate.Tokens.Mint(ciapi.Grant{
		Repo:      widgets,
		RunID:     "ci-widgets-gate",
		Expires:   now.Add(time.Hour),
		Scopes:    []model.APIScope{"ci:read", "tasks:read", "sandboxes:read", "tasks:from-template"},
		Templates: []string{"release-notes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	spoofReq := httptest.NewRequest(http.MethodPost, "/api/notifications/release-pending",
		strings.NewReader(`{"version":"v2.4.0","body":"spoofed widgets body"}`))
	spoofReq.RemoteAddr = "10.0.0.5:4242"
	spoofReq.Header.Set("Authorization", "Bearer "+widgetsGateToken)
	spoofReq.Header.Set("Content-Type", "application/json")
	spoofW := httptest.NewRecorder()
	gate.ServeHTTP(spoofW, spoofReq)
	if spoofW.Code != http.StatusForbidden {
		t.Errorf("cross-repo release-pending overwrite = %d %s, want 403 Forbidden", spoofW.Code, spoofW.Body)
	}
	openGrainNotif, err := store.OpenNotification(ctx, grainSubj)
	if err != nil || openGrainNotif == nil {
		t.Fatalf("reading open grain notification: %+v (%v)", openGrainNotif, err)
	}
	if openGrainNotif.Repo != grainRepo.String() || openGrainNotif.Body != "official grain soak summary" {
		t.Errorf("grain release-pending notification after spoof attempt = %+v, want untouched grain body", openGrainNotif)
	}

	// Once a person accepts admin-op's declaration set, subsequent CI filings
	// within that accepted set land approved.
	acc, err := store.GetDeclarationAcceptance(ctx, adminTmplID)
	if err != nil || acc == nil || !acc.Awaiting() {
		t.Fatalf("admin-op declaration acceptance record = %+v (%v), want awaiting acceptance", acc, err)
	}
	if _, err := store.AcceptDeclaration(ctx, adminTmplID, "alice@example.com", []string{"capabilities:admin"}, acc.Requested, now); err != nil {
		t.Fatalf("accepting admin-op declaration: %v", err)
	}
	acceptedW := do(http.MethodPost, "/api/tasks/from-template", `{"template":"admin-op"}`)
	if acceptedW.Code != http.StatusCreated {
		t.Fatalf("filing accepted admin-op = %d %s, want 201", acceptedW.Code, acceptedW.Body)
	}
	var acceptedTask ui.Task
	if err := json.Unmarshal(acceptedW.Body.Bytes(), &acceptedTask); err != nil {
		t.Fatal(err)
	}
	if !acceptedTask.Approved || acceptedTask.State != model.StateQueued {
		t.Errorf("accepted admin-op landed approved=%v state=%s, want approved queued", acceptedTask.Approved, acceptedTask.State)
	}
}
