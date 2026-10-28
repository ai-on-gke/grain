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

package ciapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

var grain = model.GrainSourceRepo()

// upstream records what the listener handed grain's API, and stands in
// for its route table with a few of the agent API's own routes.
type upstream struct {
	mux  *http.ServeMux
	got  []*http.Request
	body []string
}

var upstreamScopes = map[string]model.APIScope{
	"GET /api/ci/runs":                     "ci:read",
	"GET /api/ci/runs/{id}":                "ci:read",
	"POST /api/ci/runs":                    "ci:write",
	"DELETE /api/ci/runs/{id}":             "ci:write",
	"GET /api/persistent-sandboxes":        "sandboxes:read",
	"GET /api/tasks":                       "tasks:read",
	"POST /api/tasks":                      "tasks:write",
	"POST /api/tasks/from-template":        "tasks:from-template",
	"GET /api/settings":                    "settings:read",
	"GET /api/repos/{owner}/{name}/config": "repos:read",
	"GET /api/auth/status":                 "internal",
}

func newUpstream() *upstream {
	u := &upstream{mux: http.NewServeMux()}
	for pattern := range upstreamScopes {
		u.mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return u
}

func (u *upstream) APIRoute(r *http.Request) (string, model.APIScope, bool) {
	_, pattern := u.mux.Handler(r)
	scope, ok := upstreamScopes[pattern]
	return pattern, scope, ok && scope != "internal"
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	u.got = append(u.got, r)
	u.body = append(u.body, string(data))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/tasks":
		// grain's own task list, which ignores ?repo= and answers every
		// repository's tasks.
		_, _ = w.Write([]byte(`[{"id":"1","repo":"bwsalmon/grain"},{"id":"2","repo":"acme/widgets"},{"id":"3"}]`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/ci/runs":
		_, _ = w.Write([]byte(`{"enabled":true,"runs":[{"id":"a","repo":"bwsalmon/grain","n":12345678901234567},{"id":"b","repo":"acme/widgets"}]}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/ci/runs/theirs":
		_, _ = w.Write([]byte(`{"id":"theirs","repo":"acme/widgets"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/ci/runs/ours":
		_, _ = w.Write([]byte(`{"id":"ours","repo":"bwsalmon/grain"}`))
	default:
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

// releaseGate is the token release-gate-read is worth.
func releaseGate() Grant {
	g, _, _ := model.ParseAPIGrant("api:release-gate-read")
	return Grant{Repo: grain, RunID: "ci-job", Expires: time.Now().Add(time.Hour), Scopes: g.Scopes, Templates: g.Templates}
}

func servedWith(t *testing.T, grant Grant) (*Endpoint, *upstream, string) {
	t.Helper()
	e := NewEndpoint()
	e.Log = func(string, ...any) {}
	e.SetURL("http://192.0.2.1:9000")
	up := newUpstream()
	e.SetUpstream(up)
	_, token, err := e.Tokens.Mint(grant)
	if err != nil {
		t.Fatal(err)
	}
	return e, up, token
}

func call(e *Endpoint, method, target, token, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	req.RemoteAddr = "10.0.0.5:4242"
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func TestReleaseGateReadAnswersItsRequestsNarrowedToTheTokensRepo(t *testing.T) {
	e, up, token := servedWith(t, releaseGate())
	for _, tc := range []struct {
		method, target, body string
		wantPath, wantRepo   string
	}{
		{method: "GET", target: "/api/ci/runs?repo=bwsalmon/grain&ref=main&limit=100", wantPath: "/api/ci/runs", wantRepo: "bwsalmon/grain"},
		{method: "GET", target: "/api/ci/runs?ref=main", wantPath: "/api/ci/runs", wantRepo: "bwsalmon/grain"},
		{method: "GET", target: "/api/persistent-sandboxes", wantPath: "/api/persistent-sandboxes", wantRepo: "bwsalmon/grain"},
		{method: "GET", target: "/api/system/persistent-sandboxes", wantPath: "/api/persistent-sandboxes", wantRepo: "bwsalmon/grain"},
		{method: "GET", target: "/api/tasks?repo=BWSalmon/Grain&limit=100", wantPath: "/api/tasks", wantRepo: "bwsalmon/grain"},
	} {
		w := call(e, tc.method, tc.target, token, tc.body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s, want it answered", tc.method, tc.target, w.Code, w.Body)
		}
		got := up.got[len(up.got)-1]
		if got.URL.Path != tc.wantPath || got.URL.Query().Get("repo") != tc.wantRepo {
			t.Errorf("%s %s reached the API as %s, want %s repo=%q", tc.method, tc.target, got.URL, tc.wantPath, tc.wantRepo)
		}
		// Nothing of the caller's reaches the API: no forwarding headers,
		// no remote address, no token.
		if got.RemoteAddr != "" || got.Header.Get("X-Forwarded-For") != "" || got.Header.Get("Authorization") != "" {
			t.Errorf("%s %s carried the caller's %q / %v through", tc.method, tc.target, got.RemoteAddr, got.Header)
		}
	}
}

// TestCrossRepoResultsAreFilteredOut holds every answer to the token's
// repository: the API's task list takes no repo filter, so passing
// ?repo= on alone would hand the job every repository's tasks, and an
// object of another repository's is not the job's to read at all.
func TestCrossRepoResultsAreFilteredOut(t *testing.T) {
	e, _, token := servedWith(t, releaseGate())
	w := call(e, "GET", "/api/tasks", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks = %d %s, want it answered", w.Code, w.Body)
	}
	var tasks []struct {
		ID   string `json:"id"`
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decoding %s: %v", w.Body, err)
	}
	if len(tasks) != 2 || tasks[0].ID != "1" || tasks[1].ID != "3" {
		t.Errorf("GET /api/tasks answered %+v, want bwsalmon/grain's task 1 and the repository-less 3", tasks)
	}

	w = call(e, "GET", "/api/ci/runs", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/ci/runs = %d %s", w.Code, w.Body)
	}
	if body := w.Body.String(); strings.Contains(body, "acme/widgets") || !strings.Contains(body, `"n":12345678901234567`) {
		t.Errorf("GET /api/ci/runs answered %s, want acme/widgets' run dropped and numbers kept exact", body)
	}

	if w := call(e, "GET", "/api/ci/runs/theirs", token, ""); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "theirs") {
		t.Errorf("another repository's run = %d %s, want 404 with nothing of it", w.Code, w.Body)
	}
	if w := call(e, "GET", "/api/ci/runs/ours", token, ""); w.Code != http.StatusOK {
		t.Errorf("the job's own repository's run = %d %s, want it answered", w.Code, w.Body)
	}
}

func TestARouteNamingAnotherRepositoryInItsPathIsRefused(t *testing.T) {
	grant := releaseGate()
	grant.Scopes = append(grant.Scopes, "repos:read")
	e, up, token := servedWith(t, grant)
	if w := call(e, "GET", "/api/repos/acme/widgets/config", token, ""); w.Code != http.StatusForbidden {
		t.Errorf("another repository's config = %d, want 403", w.Code)
	}
	if len(up.got) != 0 {
		t.Fatalf("the refused request reached the API")
	}
	if w := call(e, "GET", "/api/repos/BWSalmon/grain/config", token, ""); w.Code != http.StatusOK {
		t.Errorf("the job's own repository's config = %d %s", w.Code, w.Body)
	}
}

func TestReleaseGateReadFilesOnlyTheReleaseNotesTemplate(t *testing.T) {
	e, up, token := servedWith(t, releaseGate())
	w := call(e, "POST", "/api/tasks/from-template", token, `{"fields":{"version":"v2.4.0"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("filing release notes = %d %s", w.Code, w.Body)
	}
	got := up.got[len(up.got)-1]
	if got.Method != http.MethodPost || got.URL.Path != "/api/tasks/from-template" {
		t.Fatalf("reached the API as %s %s, want POST /api/tasks/from-template", got.Method, got.URL)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(up.body[len(up.body)-1]), &body); err != nil {
		t.Fatal(err)
	}
	if body["template"] != "release-notes" || body["repo"] != "bwsalmon/grain" || body["approved"] != true {
		t.Errorf("body = %v, want the approved release-notes template of bwsalmon/grain", body)
	}
	if fields, _ := body["fields"].(map[string]any); fields["version"] != "v2.4.0" {
		t.Errorf("fields = %v, want version passed through", body["fields"])
	}

	for _, refused := range []string{
		`{"template":"design-planner"}`,
		`{"template":"release-notes","title":"anything else"}`,
		`{"templateId":"x","capabilities":["git-push-any-ref"]}`,
		`{"template":"release-notes"}{"template":"design-planner"}`,
		`{"template":"release-notes","repo":"acme/widgets"}`,
		`{"template":"../release-notes"}`,
	} {
		if w := call(e, "POST", "/api/tasks/from-template", token, refused); w.Code != http.StatusForbidden {
			t.Errorf("POST %s = %d, want it refused", refused, w.Code)
		}
	}
	for _, named := range []string{`{"template":"release-notes"}`,
		`{"template":"` + model.RepoDeclaredID(model.DeclaredTemplate, grain, "release-notes") + `"}`} {
		if w := call(e, "POST", "/api/tasks/from-template", token, named); w.Code != http.StatusOK {
			t.Errorf("naming release-notes as %s = %d %s, want it filed", named, w.Code, w.Body)
		}
	}
}

func TestAnUnlimitedFromTemplateGrantFilesAnyOfTheReposTemplates(t *testing.T) {
	e, up, token := servedWith(t, Grant{Repo: grain, RunID: "ci-job", Scopes: []model.APIScope{"tasks:from-template"}, AnyTemplate: true})
	if w := call(e, "POST", "/api/tasks/from-template", token, `{"template":"design-planner"}`); w.Code != http.StatusOK {
		t.Fatalf("= %d %s", w.Code, w.Body)
	}
	if !strings.Contains(up.body[len(up.body)-1], `"template":"design-planner"`) {
		t.Errorf("body = %s", up.body[len(up.body)-1])
	}
	if w := call(e, "POST", "/api/tasks/from-template", token, `{}`); w.Code != http.StatusForbidden {
		t.Errorf("naming no template = %d, want it refused", w.Code)
	}
}

func TestReleaseGateReadRaisesOnlyTheReleasePendingNotification(t *testing.T) {
	e, up, token := servedWith(t, releaseGate())
	w := call(e, "POST", "/api/notifications/release-pending", token,
		`{"version":"v2.4.0","rc":"v2.4.0-rc.2","body":"soak PASS"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("raising release-pending = %d %s", w.Code, w.Body)
	}
	got := up.got[len(up.got)-1]
	if got.Method != http.MethodPost || got.URL.Path != "/api/notifications" {
		t.Fatalf("reached the API as %s %s, want POST /api/notifications", got.Method, got.URL)
	}
	var body struct {
		Subject, Title, Body, Severity, Repo string
		Actions                              []string
	}
	if err := json.Unmarshal([]byte(up.body[len(up.body)-1]), &body); err != nil {
		t.Fatal(err)
	}
	if body.Subject != "release.pending:v2.4.0" || body.Repo != "bwsalmon/grain" || body.Severity != "warning" ||
		body.Body != "soak PASS" || !strings.Contains(body.Title, "v2.4.0 is ready to promote") {
		t.Errorf("body = %+v, want release.pending:v2.4.0 about bwsalmon/grain", body)
	}
	if strings.Join(body.Actions, ",") != "approve-release,reject-release" {
		t.Errorf("actions = %v, want Approve and Reject", body.Actions)
	}

	for _, refused := range []string{
		`{"version":"v2.4.1"}`,
		`{"version":"latest"}`,
		`{"version":"v2.4.0","rc":"v2.5.0-rc.1"}`,
		`{"version":"v2.4.0","subject":"pipeline.base-red:x"}`,
		`{"version":"v2.4.0","actions":["investigate"]}`,
		`{"version":"v2.4.0"}{"version":"v2.5.0"}`,
	} {
		if w := call(e, "POST", "/api/notifications/release-pending", token, refused); w.Code != http.StatusForbidden {
			t.Errorf("POST %s = %d, want it refused", refused, w.Code)
		}
	}
	if w := call(e, "POST", "/api/notifications", token, `{"subject":"x","title":"y"}`); w.Code != http.StatusForbidden {
		t.Errorf("POST /api/notifications = %d, want it refused: only the release-pending one is raised", w.Code)
	}
}

func TestTheListenerRefusesEverythingElse(t *testing.T) {
	e, up, token := servedWith(t, releaseGate())
	for _, tc := range []struct{ method, target, token string }{
		{"GET", "/api/ci/runs", ""},
		{"GET", "/api/ci/runs", "ga_nope_nope"},
		{"GET", "/api/ci/runs?repo=acme/widgets", token},
		{"GET", "/api/ci/runs?repo=bwsalmon/grain&repo=acme/widgets", token},
		{"GET", "/api/tasks?repo=acme/widgets", token},
		{"GET", "/api/tasks?repo=bwsalmon/grain&repo=acme/widgets", token},
		{"POST", "/api/tasks", token},
		{"POST", "/api/ci/runs", token},
		{"DELETE", "/api/ci/runs/run1", token},
		{"GET", "/api/settings", token},
		{"GET", "/api/auth/status", token},
		{"GET", "/api/nowhere", token},
		{"PUT", "/api/persistent-sandboxes", token},
	} {
		w := call(e, tc.method, tc.target, tc.token, "")
		if w.Code == http.StatusOK {
			t.Errorf("%s %s (token %q) was answered, want it refused", tc.method, tc.target, tc.token)
		}
	}
	if len(up.got) != 0 {
		t.Errorf("refused requests reached the API: %d", len(up.got))
	}
}

func TestARevokedOrExpiredTokenIsWorthNothing(t *testing.T) {
	e, _, token := servedWith(t, releaseGate())
	id, _, _ := strings.Cut(strings.TrimPrefix(token, "ga_"), "_")
	e.Tokens.Revoke(id)
	if w := call(e, "GET", "/api/persistent-sandboxes", token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("a revoked token = %d, want 401", w.Code)
	}

	stale := releaseGate()
	stale.Expires = time.Now().Add(-time.Second)
	_, staleToken, err := e.Tokens.Mint(stale)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(e, "GET", "/api/persistent-sandboxes", staleToken, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("an expired token = %d, want 401", w.Code)
	}
	if n := e.Tokens.Len(); n != 0 {
		t.Errorf("%d tokens live, want none", n)
	}
}

func TestTheProviderIsCIOnlyAndAdmitsTrustedEventsOnly(t *testing.T) {
	for _, name := range []string{model.ReleaseGateReadCapability, "api:ci:read", "api:release-gate-read"} {
		p, ok := DynamicProvider(NewEndpoint(), Config{})(name)
		if !ok {
			t.Fatalf("%s has no CI provider", name)
		}
		spec := p.Spec()
		if spec.Name != name || !spec.CIOnly || spec.Provision != model.ProvisionMint {
			t.Fatalf("spec = %+v, want a CI-only minted capability named %s", spec, name)
		}
		events := p.(model.CIEventCapability)
		for event, want := range map[string]bool{
			"release": true, "workflow_dispatch": true, "push": false, "pull_request": false, "schedule": false,
		} {
			if got := events.SupportsCIEvent(event); got != want {
				t.Errorf("%s: SupportsCIEvent(%q) = %v, want %v", name, event, got, want)
			}
		}
		if !p.(model.TrustedScheduleCapability).SupportsTrustedSchedule() {
			t.Errorf("%s does not admit grain's own cron, which release-train.yml runs on", name)
		}
	}
	if _, ok := DynamicProvider(NewEndpoint(), Config{})("gemini-key"); ok {
		t.Error("a name that is no api grant resolved to a CI api provider")
	}
}

func TestDangerousAndUnnarrowableScopesAreRefusedInCI(t *testing.T) {
	ctx := context.Background()
	e := NewEndpoint()
	e.SetURL("http://192.0.2.1:9000")
	cc := model.CapabilityContext{Task: model.Task{Target: &grain}, Run: model.Run{ID: "ci-job"}, Now: time.Now(), CI: true}
	for _, name := range []string{
		"api:settings:write", "api:secrets:write", "api:capabilities:admin", "api:tasks:admin",
		"api:task-admin", "api:deployment-ops", "api:release-manager",
		"api:settings:read", "api:secrets:read", "api:sessions:read", "api:deployment:read", "api:tasks:write", "api:ci:write",
		"api:ci:read@acme/widgets", "api:nonsense",
	} {
		p, ok := DynamicProvider(e, Config{})(name)
		if !ok {
			t.Fatalf("%s has no CI provider", name)
		}
		res, err := p.Resolve(ctx, cc)
		if err != nil || !res.Refused {
			t.Errorf("%s resolved for a CI job (%+v, %v), want it refused", name, res, err)
		}
		if _, err := p.Materialize(ctx, cc); err == nil {
			t.Errorf("%s materialized for a CI job", name)
		}
	}
	if e.Tokens.Len() != 0 {
		t.Errorf("%d tokens minted for refused grants", e.Tokens.Len())
	}
	for _, name := range []string{"api:ci:read", "api:ci-read", "api:tasks:read@bwsalmon/grain", "api:sandboxes:read"} {
		p, _ := DynamicProvider(e, Config{})(name)
		if res, err := p.Resolve(ctx, cc); err != nil || res.Refused {
			t.Errorf("%s refused for a CI job: %+v, %v", name, res, err)
		}
	}
}

func TestTheProviderMintsOneTokenForAllOfAJobsGrantsForAllowedReposOnly(t *testing.T) {
	ctx := context.Background()
	e := NewEndpoint()
	resolve := DynamicProvider(e, Config{})
	p, _ := resolve(model.ReleaseGateReadCapability)
	extra, _ := resolve("api:repos:read")
	refused, _ := resolve("api:settings:write")
	cc := model.CapabilityContext{
		Task: model.Task{Target: &grain, Grants: []model.Grant{
			{Capability: model.ReleaseGateReadCapability}, {Capability: "api:repos:read"},
			{Capability: "api:settings:write"}, {Capability: "gemini-key"},
		}},
		Run: model.Run{ID: "ci-job"}, Now: time.Now(), CI: true,
	}

	if res, _ := p.Resolve(ctx, cc); !res.Refused {
		t.Error("resolved while the listener is not being served")
	}
	e.SetURL("http://192.0.2.1:9000/")
	if res, err := p.Resolve(ctx, cc); err != nil || res.Refused {
		t.Fatalf("Resolve for bwsalmon/grain = %+v, %v", res, err)
	}
	other := model.RepoRef{Owner: "acme", Name: "widgets"}
	if res, _ := p.Resolve(ctx, model.CapabilityContext{Task: model.Task{Target: &other}, CI: true}); !res.Refused {
		t.Error("resolved for acme/widgets, which it is not configured for")
	}
	if res, _ := refused.Resolve(ctx, cc); !res.Refused {
		t.Error("api:settings:write resolved for a CI job")
	}

	m, err := p.Materialize(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	if m.Lease == nil || m.Lease.Capability != model.ReleaseGateReadCapability || e.Tokens.Len() != 1 {
		t.Fatalf("lease = %+v with %d tokens, want one lease and one token", m.Lease, e.Tokens.Len())
	}
	placed := map[string]string{}
	for _, pl := range m.Placements {
		if pl.Side != model.SideSandbox {
			t.Errorf("placement %s is not in the sandbox", pl.Path)
		}
		placed[pl.Path] = pl.Content
	}
	token := strings.TrimSpace(placed[TokenPath])
	if strings.TrimSpace(placed[URLPath]) != "http://192.0.2.1:9000" || token == "" {
		t.Fatalf("placements = %v", placed)
	}
	if env := placed[EnvPath]; !strings.Contains(env, URLEnv+"=") || !strings.Contains(env, TokenEnv+"=\""+token+"\"") {
		t.Errorf("env file = %q, want both exports", env)
	}
	g, ok := e.Tokens.Lookup(token)
	if !ok || !g.Repo.Same(grain) || g.RunID != "ci-job" {
		t.Fatalf("token grants %+v, %v", g, ok)
	}
	if !g.Holds("ci:read") || !g.Holds("repos:read") || g.Holds("settings:write") || !g.MayFile("release-notes") || g.MayFile("design-planner") {
		t.Errorf("token holds %+v, want release-gate-read's scopes and repos:read, not settings:write", g)
	}

	// The job's second api grant places the same token, and mints nothing.
	m2, err := extra.Materialize(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Lease != nil || e.Tokens.Len() != 1 {
		t.Errorf("second grant leased %+v with %d tokens, want the one token placed again", m2.Lease, e.Tokens.Len())
	}

	if err := p.Revoke(ctx, cc, *m.Lease); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Tokens.Lookup(token); ok {
		t.Error("the token outlived its lease's revocation")
	}

	widgets, _ := DynamicProvider(e, Config{Repos: []model.RepoRef{other}})(model.ReleaseGateReadCapability)
	if res, _ := widgets.Resolve(ctx, model.CapabilityContext{Task: model.Task{Target: &other}, CI: true}); res.Refused {
		t.Errorf("a provider configured for acme/widgets refused it: %s", res.Reason)
	}
}
