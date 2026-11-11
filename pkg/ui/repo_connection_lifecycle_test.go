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

// The add/remove lifecycle of repositories and git connections
// (grain/task-4249), walked end to end through the HTTP routes the
// Settings -> Repositories and Sources panes call, against a real store
// and a real credential set rather than mocks: add a connection, add a
// repository through it, pin the repository to it, give the repository
// per-repo state, then remove them in each order and re-add them, and
// assert at every step what is left behind.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// lifecycleRig is one deployment: a UI server over a real store, a real
// credential set and a real secrets store.
type lifecycleRig struct {
	t      *testing.T
	srv    *ui.Server
	client *ui.Client
	store  *model.Store
	creds  *gitproxy.CredentialSet
	ctx    context.Context
}

func newLifecycleRig(t *testing.T) *lifecycleRig {
	t.Helper()
	client, store, ctx := testClient(t)
	creds, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = creds
	client.Config.Secrets = secrets.New(t.TempDir())
	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	return &lifecycleRig{t: t, srv: ui.NewServerWithClient(client), client: client, store: store, creds: creds, ctx: ctx}
}

func (r *lifecycleRig) call(method, path, body string) (int, string) {
	r.t.Helper()
	rec := doWithHeaders(r.t, r.srv, method, path, body,
		map[string]string{"Content-Type": "application/json", "X-Grain-User": "alice"})
	return rec.Code, rec.Body.String()
}

func (r *lifecycleRig) must(method, path, body string) string {
	r.t.Helper()
	code, out := r.call(method, path, body)
	if code != http.StatusOK {
		r.t.Fatalf("%s %s: %d %s", method, path, code, out)
	}
	return out
}

func (r *lifecycleRig) targets() []string {
	r.t.Helper()
	got, err := r.store.TargetRepos(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

func (r *lifecycleRig) connections() []ui.TeamGitConnectionView {
	r.t.Helper()
	var views []ui.TeamGitConnectionView
	if err := json.Unmarshal([]byte(r.must(http.MethodGet, "/api/deployment/git-connections", "")), &views); err != nil {
		r.t.Fatalf("decoding git connections: %v", err)
	}
	return views
}

// addConnection adds a PAT connection and returns the credential name
// grain minted for it.
func (r *lifecycleRig) addConnection(host, scope, value string) string {
	r.t.Helper()
	before := map[string]bool{}
	for _, v := range r.connections() {
		before[v.Credential] = true
	}
	body := `{"host":"` + host + `","kind":"github","type":"pat","value":"` + value + `"`
	if scope != "" {
		body += `,"scope":"` + scope + `"`
	}
	r.must(http.MethodPost, "/api/deployment/git-connections", body+"}")
	for _, v := range r.connections() {
		if !before[v.Credential] {
			return v.Credential
		}
	}
	r.t.Fatalf("adding a connection on %s listed no new connection: %+v", host, r.connections())
	return ""
}

func (r *lifecycleRig) credentialExists(name string) bool {
	_, ok := r.creds.Get(name)
	return ok
}

// staticRepoNames is a RepoNames that answers every lookup with one
// fixed spelling, as the default host's REST client does: owner/name,
// never a host.
type staticRepoNames struct{ full string }

func (s staticRepoNames) RepoFullName(owner, name string) (string, error) { return s.full, nil }

// Scenario 1 and 3 in the repo-first order: connection, repository,
// pinned repository, task; then the repository goes first and the
// connection after it, and neither leaves anything behind.
func TestRepoAndConnectionLifecycleRemovingTheRepoFirst(t *testing.T) {
	r := newLifecycleRig(t)

	cred := r.addConnection("github.com", "acme", "ghp_acme_one")
	if !r.credentialExists(cred) {
		t.Fatalf("connection %s added but its credential is not stored", cred)
	}
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	r.must(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"`+cred+`"}`)
	r.must(http.MethodPut, "/api/repos/acme/widgets/setup-command", `{"setupCommand":"make deps"}`)
	r.must(http.MethodPut, "/api/repos/acme/widgets/ci-mode", `{"ciMode":"github"}`)

	if got := r.targets(); !slices.Equal(got, []string{"acme/widgets"}) {
		t.Fatalf("targets after add = %v", got)
	}
	task, err := r.client.CreateTask(r.ctx, ui.CreateTaskRequest{Title: "Fix the widget", Repo: "acme/widgets"})
	if err != nil {
		t.Fatalf("filing a task against the added repo: %v", err)
	}

	r.must(http.MethodDelete, "/api/repos/acme/widgets", "")
	if got := r.targets(); len(got) != 0 {
		t.Fatalf("targets after remove = %v, want none", got)
	}
	if rc, err := r.store.GetRepoConfig(r.ctx, model.RepoRef{Owner: "acme", Name: "widgets"}); err != nil || rc != nil {
		t.Fatalf("repo_config after remove = %+v, %v; want the row gone", rc, err)
	}
	// The task keeps its target and its page; it is parked on dispatch,
	// not deleted.
	if _, err := r.client.GetTask(r.ctx, task.ID); err != nil {
		t.Fatalf("the removed repo's task is gone: %v", err)
	}
	// Removing an absent repo again is a no-op, not an error.
	r.must(http.MethodDelete, "/api/repos/acme/widgets", "")

	// The repository-scoped pin is a connection naming the same
	// credential: removing the connection takes both.
	r.must(http.MethodDelete, "/api/deployment/git-connections/"+cred, "")
	if views := r.connections(); len(views) != 0 {
		t.Fatalf("connections after remove = %+v, want none", views)
	}
	if r.credentialExists(cred) {
		t.Fatalf("connection %s removed but its minted credential is still stored", cred)
	}
	if code, out := r.call(http.MethodDelete, "/api/deployment/git-connections/"+cred, ""); code != http.StatusBadRequest {
		t.Fatalf("removing a removed connection = %d %s, want 400", code, out)
	}
}

// Scenario 1 and 3 in the connection-first order: the connection the
// repository is pinned to goes while the repository stays, so the
// repository falls back to no connection rather than naming a deleted
// credential.
func TestRepoAndConnectionLifecycleRemovingTheConnectionFirst(t *testing.T) {
	r := newLifecycleRig(t)

	cred := r.addConnection("github.com", "", "ghp_default_one")
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	r.must(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"`+cred+`"}`)

	r.must(http.MethodDelete, "/api/deployment/git-connections/"+cred, "")
	for _, v := range r.connections() {
		if v.Credential == cred {
			t.Fatalf("connection %s still listed after removal: %+v", cred, v)
		}
	}
	if r.credentialExists(cred) {
		t.Fatalf("removed connection's credential %s is still stored", cred)
	}
	if got := r.targets(); !slices.Equal(got, []string{"acme/widgets"}) {
		t.Fatalf("removing a connection changed the repository list: %v", got)
	}
	// Pinning the repository to the removed connection is refused by name.
	code, out := r.call(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"`+cred+`"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, cred) {
		t.Fatalf("pinning to a removed connection = %d %s, want a 400 naming it", code, out)
	}
	r.must(http.MethodDelete, "/api/repos/acme/widgets", "")
}

// Scenario 3: a connection whose minted credential a repository's own
// override still names is refused, naming the repository, and nothing
// is half-removed -- the connection and its credential both stay.
func TestRemovingAConnectionARepositoryStillNamesIsRefusedWhole(t *testing.T) {
	r := newLifecycleRig(t)

	cred := r.addConnection("github.com", "", "ghp_default_two")
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	ref := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := r.store.PutRepoConfig(r.ctx, model.RepoConfig{Repo: ref, GitCredential: cred}); err != nil {
		t.Fatal(err)
	}

	code, out := r.call(http.MethodDelete, "/api/deployment/git-connections/"+cred, "")
	if code != http.StatusBadRequest || !strings.Contains(out, "acme/widgets") {
		t.Fatalf("removing a connection a repository names = %d %s, want a 400 naming acme/widgets", code, out)
	}
	if !r.credentialExists(cred) {
		t.Fatal("a refused removal deleted the credential anyway")
	}
	if !slices.ContainsFunc(r.connections(), func(v ui.TeamGitConnectionView) bool { return v.Credential == cred }) {
		t.Fatal("a refused removal dropped the connection anyway")
	}

	// Once the repository lets go of it, the removal goes through.
	if err := r.store.PutRepoConfig(r.ctx, model.RepoConfig{Repo: ref}); err != nil {
		t.Fatal(err)
	}
	r.must(http.MethodDelete, "/api/deployment/git-connections/"+cred, "")
	if r.credentialExists(cred) {
		t.Fatal("credential survived its connection's removal")
	}
}

// Scenario 3, for named tokens: a token a repository's override names is
// not deleted out from under it.
func TestDeletingANamedTokenARepositoryStillNamesIsRefused(t *testing.T) {
	r := newLifecycleRig(t)
	if err := r.creds.SetPAT("widgets-bot", "ghp_named"); err != nil {
		t.Fatal(err)
	}
	ref := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := r.store.PutRepoConfig(r.ctx, model.RepoConfig{Repo: ref, GitCredential: "widgets-bot"}); err != nil {
		t.Fatal(err)
	}
	code, out := r.call(http.MethodDelete, "/api/github-tokens/widgets-bot", "")
	if code != http.StatusBadRequest || !strings.Contains(out, "acme/widgets") {
		t.Fatalf("deleting a token a repository names = %d %s, want a 400 naming acme/widgets", code, out)
	}
	if !r.credentialExists("widgets-bot") {
		t.Fatal("refused delete removed the token anyway")
	}
}

// Scenario 4: re-adding a connection on the same host and scope after
// removing it mints a fresh credential, lists exactly one connection,
// and does not resurrect the old credential.
func TestReAddingARemovedConnectionStartsClean(t *testing.T) {
	r := newLifecycleRig(t)

	first := r.addConnection("github.com", "acme", "ghp_first")
	code, out := r.call(http.MethodPost, "/api/deployment/git-connections",
		`{"host":"github.com","kind":"github","type":"pat","scope":"acme","value":"ghp_dupe"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, first) {
		t.Fatalf("a second connection on the same host and scope = %d %s, want a 400 naming %s", code, out, first)
	}
	r.must(http.MethodDelete, "/api/deployment/git-connections/"+first, "")

	second := r.addConnection("github.com", "acme", "ghp_second")
	if second == first {
		t.Fatalf("re-added connection reused the removed credential name %s", first)
	}
	if r.credentialExists(first) {
		t.Fatal("the removed connection's credential came back")
	}
	if views := r.connections(); len(views) != 1 || views[0].Credential != second {
		t.Fatalf("connections after re-add = %+v, want only %s", views, second)
	}
	got, ok := r.creds.Get(second)
	if !ok || got.Token == nil || *got.Token != "ghp_second" {
		t.Fatalf("re-added connection's credential = %+v, %v; want the new token", got, ok)
	}
}

// Scenario 1 across two hosts: a connection on a second host covers that
// host's repositories only, and removing it leaves the default host's
// connection in place.
func TestConnectionsOnTwoHostsAreAddedAndRemovedIndependently(t *testing.T) {
	r := newLifecycleRig(t)

	dflt := r.addConnection("github.com", "", "ghp_dotcom")
	ghe := r.addConnection("ghe.example.com", "", "ghp_ghe")
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	r.must(http.MethodPost, "/api/repos", `{"repo":"ghe.example.com/acme/widgets"}`)
	if got := r.targets(); !slices.Equal(got, []string{"acme/widgets", "ghe.example.com/acme/widgets"}) {
		t.Fatalf("the same owner/name on two hosts = %v, want two entries", got)
	}

	r.must(http.MethodDelete, "/api/deployment/git-connections/"+ghe, "")
	if views := r.connections(); len(views) != 1 || views[0].Credential != dflt {
		t.Fatalf("connections after removing the second host's = %+v, want only %s", views, dflt)
	}

	// Removing one host's repository leaves the other host's of the same
	// owner/name.
	r.must(http.MethodDelete, "/api/repos/ghe.example.com%2Facme/widgets", "")
	if got := r.targets(); !slices.Equal(got, []string{"acme/widgets"}) {
		t.Fatalf("targets after removing the second host's repo = %v", got)
	}
}

// Scenario 5, and the operator's report (grain/task-4257): adding a
// repository on another host with no clone URL kept the default host's
// GitHub spelling of owner/name and dropped the host, so the allowlist
// named a different repository.
func TestAddingARepositoryOnAnotherHostKeepsItsHost(t *testing.T) {
	r := newLifecycleRig(t)
	r.client.Config.RepoNames = staticRepoNames{full: "Experimental/Grain"}

	r.must(http.MethodPost, "/api/repos", `{"repo":"git.example.org/experimental/grain"}`)
	if got := r.targets(); !slices.Equal(got, []string{"git.example.org/experimental/grain"}) {
		t.Fatalf("targets = %v, want the host kept", got)
	}

	// A default-host repository still takes GitHub's spelling.
	r.must(http.MethodPost, "/api/repos", `{"repo":"experimental/grain"}`)
	if got := r.targets(); !slices.Equal(got, []string{"git.example.org/experimental/grain", "Experimental/Grain"}) {
		t.Fatalf("targets = %v, want the default host's spelling adopted beside the other host's", got)
	}
}

// Scenario 5: the spellings one repository is written in -- case,
// whitespace, a trailing .git, URL forms -- are one entry, removable in
// any of them.
func TestRepoSpellingsAreOneEntry(t *testing.T) {
	r := newLifecycleRig(t)

	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	for _, spelling := range []string{
		"Acme/Widgets",
		"  acme/widgets  ",
		"https://github.com/acme/widgets.git",
		"https://github.com/ACME/widgets",
		"git@github.com:acme/widgets.git",
		"github.com/acme/widgets",
	} {
		code, out := r.call(http.MethodPost, "/api/repos", `{"repo":"`+spelling+`"}`)
		if code != http.StatusOK {
			t.Errorf("adding %q: %d %s", spelling, code, out)
			continue
		}
		if got := r.targets(); len(got) != 1 {
			t.Fatalf("adding %q beside acme/widgets made %v, want one entry", spelling, got)
		}
	}
	r.must(http.MethodDelete, "/api/repos/ACME/Widgets", "")
	if got := r.targets(); len(got) != 0 {
		t.Fatalf("removing in another case left %v", got)
	}
	for _, bad := range []string{"", "acme", "acme/", "/widgets", "acme/widgets/extra/more", "acme/wid gets"} {
		if code, out := r.call(http.MethodPost, "/api/repos", `{"repo":"`+bad+`"}`); code != http.StatusBadRequest {
			t.Errorf("adding %q = %d %s, want 400", bad, code, out)
		}
	}
	if got := r.targets(); len(got) != 0 {
		t.Fatalf("refused adds wrote %v", got)
	}
}

// Scenario 2 and 4: a repository with per-repo state -- settings,
// capability defaults, release config and secrets -- is forgotten
// whole on removal, and re-adding it starts clean rather than
// inheriting any of it.
func TestRemovingARepositoryForgetsItsStateAndReAddingStartsClean(t *testing.T) {
	r := newLifecycleRig(t)
	ref := model.RepoRef{Owner: "acme", Name: "widgets"}

	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	r.must(http.MethodPut, "/api/repos/acme/widgets/setup-command", `{"setupCommand":"make deps"}`)
	r.must(http.MethodPut, "/api/repos/acme/widgets/prompt-extension", `{"promptExtension":"be careful"}`)
	r.must(http.MethodPut, "/api/repos/acme/widgets/ci-mode", `{"ciMode":"github"}`)
	r.must(http.MethodPut, "/api/repos/acme/widgets/watch-default-branch", `{"enabled":true}`)
	r.must(http.MethodPut, "/api/repos/acme/widgets/release", `{"enabled":false}`)
	r.must(http.MethodPut, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", `{"value":"npm-secret"}`)
	secret, key := ci.ReleaseSecretAddress("acme/widgets", "NPM_TOKEN")
	if v, err := r.client.Config.Secrets.Resolve(r.ctx, secret+"/"+key); err != nil || v != "npm-secret" {
		t.Fatalf("release secret before removal = %q, %v", v, err)
	}
	// A value stored under the repository's release namespace that its
	// row never came to list -- a set whose repo_config write failed --
	// is the repository's too, and goes with it.
	orphan, orphanKey := ci.ReleaseSecretAddress("acme/widgets", "UNLISTED")
	if err := r.client.Config.Secrets.Set(orphan, orphanKey, []byte("orphaned")); err != nil {
		t.Fatal(err)
	}
	if rc, err := r.store.GetRepoConfig(r.ctx, ref); err != nil || rc == nil || rc.SetupCommand != "make deps" {
		t.Fatalf("repo_config before removal = %+v, %v", rc, err)
	}

	r.must(http.MethodDelete, "/api/repos/acme/widgets", "")
	if rc, err := r.store.GetRepoConfig(r.ctx, ref); err != nil || rc != nil {
		t.Fatalf("repo_config after removal = %+v, %v; want none", rc, err)
	}
	if v, err := r.client.Config.Secrets.Resolve(r.ctx, secret+"/"+key); err == nil {
		t.Fatalf("release secret survived its repository's removal: %q", v)
	}
	if v, err := r.client.Config.Secrets.Resolve(r.ctx, orphan+"/"+orphanKey); err == nil {
		t.Fatalf("an unlisted release secret survived its repository's removal: %q", v)
	}

	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	var defaults ui.RepoDefaults
	if err := json.Unmarshal([]byte(r.must(http.MethodGet, "/api/repos/acme/widgets/settings", "")), &defaults); err != nil {
		t.Fatal(err)
	}
	if defaults.SetupCommand != "" || defaults.PromptExtension != "" {
		t.Fatalf("re-added repository inherited its old settings: %+v", defaults)
	}
	var release ui.RepoReleaseSettings
	if err := json.Unmarshal([]byte(r.must(http.MethodGet, "/api/repos/acme/widgets/release", "")), &release); err != nil {
		t.Fatal(err)
	}
	if !release.Enabled || len(release.Secrets) != 0 {
		t.Fatalf("re-added repository's release settings = %+v, want the defaults", release)
	}
	if got := r.targets(); !slices.Equal(got, []string{"acme/widgets"}) {
		t.Fatalf("targets after re-add = %v, want one entry", got)
	}
}

// Scenario 2: removing a repository whose tasks are still in play is
// allowed and the tasks stay -- they park on their next dispatch
// (orchestrator.reconcileTargetRepos) rather than vanishing with it.
func TestRemovingARepositoryWithQueuedTasksKeepsTheTasks(t *testing.T) {
	r := newLifecycleRig(t)
	// A second repository keeps the allowlist non-empty once acme/widgets
	// goes: an empty one is an unrestricted deployment
	// (TestRemovingTheLastRepositoryLeavesAnUnrestrictedDeployment).
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/gadgets"}`)
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	var ids []string
	for _, title := range []string{"one", "two"} {
		task, err := r.client.CreateTask(r.ctx, ui.CreateTaskRequest{Title: title, Repo: "acme/widgets"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	r.must(http.MethodDelete, "/api/repos/acme/widgets", "")
	for _, id := range ids {
		task, err := r.client.GetTask(r.ctx, id)
		if err != nil {
			t.Fatalf("task %s gone after its repository's removal: %v", id, err)
		}
		if task.Repo != "acme/widgets" {
			t.Fatalf("task %s retargeted to %q", id, task.Repo)
		}
	}
	// A new task against the removed repository is filed parked on
	// grain's off-allowlist comment, never dispatched.
	late, err := r.client.CreateTask(r.ctx, ui.CreateTaskRequest{Title: "three", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if late.State != model.StateAwaitingReply {
		t.Fatalf("a task filed against a removed repository is %s, want parked (%s)", late.State, model.StateAwaitingReply)
	}
}

// Scenario 2: the config repository cannot be removed at all.
func TestTheConfigRepositoryCannotBeRemoved(t *testing.T) {
	r := newLifecycleRig(t)
	r.client.Config.StateRepoRef = func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-config"} }
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/grain-config"}`)
	code, out := r.call(http.MethodDelete, "/api/repos/acme/grain-config", "")
	if code != http.StatusBadRequest || !strings.Contains(out, "config repository") {
		t.Fatalf("removing the config repository = %d %s, want a 400 saying why", code, out)
	}
	if got := r.targets(); !slices.Equal(got, []string{"acme/grain-config"}) {
		t.Fatalf("targets = %v, want the config repository kept", got)
	}
}

// Removing the last repository leaves TargetRepos empty, and an empty
// allowlist is an unrestricted deployment: new tasks against the
// repository just removed are accepted again. This pins today's
// behaviour so a change to it is deliberate; whether the last removal
// should instead be refused or leave the deployment closed is a
// follow-up (filed from grain/task-4249).
func TestRemovingTheLastRepositoryLeavesAnUnrestrictedDeployment(t *testing.T) {
	r := newLifecycleRig(t)
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	if off, err := r.client.CreateTask(r.ctx, ui.CreateTaskRequest{Title: "off list", Repo: "acme/other"}); err != nil || off.State != model.StateAwaitingReply {
		t.Fatalf("a restricted deployment filed a task off its list as %+v, %v; want it parked", off.State, err)
	}
	r.must(http.MethodDelete, "/api/repos/acme/widgets", "")
	if got := r.targets(); len(got) != 0 {
		t.Fatalf("targets = %v", got)
	}
	after, err := r.client.CreateTask(r.ctx, ui.CreateTaskRequest{Title: "after", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if after.State == model.StateAwaitingReply {
		t.Fatalf("today an empty allowlist is unrestricted, but the task was parked: %+v", after.State)
	}
}

// Scenario 7: the agent API gate on the add/remove routes. A task without
// repos:write or connections:write may not add or remove a repository or
// a connection, and a task granted repos:write on its own repository
// alone may not widen its reach by adding another one.
func TestAgentsCannotAddOrRemoveRepositoriesOrConnectionsBeyondTheirGrant(t *testing.T) {
	r := newLifecycleRig(t)
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	cred := r.addConnection("github.com", "", "ghp_gate")

	seedAPITask(t, r.client, "901", "acme/widgets", "api:tasks:write@acme/widgets")
	seedAPITask(t, r.client, "902", "acme/widgets", "api:repos:write@acme/widgets")

	for _, tc := range []struct {
		task, method, path, body string
	}{
		{"901", http.MethodPost, "/api/repos", `{"repo":"other/thing"}`},
		{"901", http.MethodDelete, "/api/repos/acme/widgets", ""},
		{"901", http.MethodPost, "/api/deployment/git-connections", `{"host":"github.com","kind":"github","type":"pat","scope":"other","value":"ghp_x"}`},
		{"901", http.MethodDelete, "/api/deployment/git-connections/" + cred, ""},
		{"901", http.MethodPut, "/api/github-tokens/agent-made", `{"token":"ghp_y"}`},
		{"902", http.MethodPost, "/api/repos", `{"repo":"other/thing"}`},
		{"902", http.MethodDelete, "/api/deployment/git-connections/" + cred, ""},
	} {
		code, out := asAgent(t, r.srv, tc.task, tc.method, tc.path, tc.body, "testing the gate")
		if code != http.StatusForbidden {
			t.Errorf("task %s %s %s = %d %s, want 403", tc.task, tc.method, tc.path, code, out)
		}
	}
	if got := r.targets(); !slices.Equal(got, []string{"acme/widgets"}) {
		t.Fatalf("targets after refused agent calls = %v", got)
	}
	if views := r.connections(); len(views) != 1 || views[0].Credential != cred {
		t.Fatalf("connections after refused agent calls = %+v", views)
	}
	if r.credentialExists("agent-made") {
		t.Fatal("a refused agent call stored a token")
	}
}
