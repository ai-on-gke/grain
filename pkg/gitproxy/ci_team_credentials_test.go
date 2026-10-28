package gitproxy

import (
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// A CI job holding repository access it declared goes through the proxy
// as its team (grain/task-1654): the team's credential is what GitHub
// sees, the deployment's own ladder is never a fallback, and what the job
// may push is still only what it declared.

func newCITeamProxy(t *testing.T, teamCredential string) (*GitProxy, *FakeForwarder, *ReadScopes, *WriteScopes, *SandboxTeams) {
	t.Helper()
	store, ctx := openStoreForTest(t)
	// The deployment's connection: a CI job runs with it whichever team
	// it runs as (docs/single-tenant-reset.md, stage 1).
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = teamCredential
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	credentials, err := LoadCredentialSet(writeCredentialSet(t, map[string]string{"*": "deployment"}, map[string]string{
		"deployment": "token-deployment",
		"infra-cred": "token-infra",
	}))
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"ci-job": "tok-ci"}))
	if err != nil {
		t.Fatal(err)
	}
	reads, writes, teams := NewReadScopes(), NewWriteScopes(), NewSandboxTeams()
	forwarder := &FakeForwarder{Response: UpstreamResponse{Status: 200, Headers: map[string]string{"Content-Type": "x"}, Body: []byte("ok")}}
	proxy := &GitProxy{
		Authorizer: &ReadOnlyScopeAuthorizer{
			Scopes: reads, WriteScopes: writes, Next: &ModelAuthorizer{Store: store},
		},
		Credentials:         credentials,
		Tokens:              tokens,
		Forwarder:           forwarder,
		Audit:               &RecordingAuditLog{},
		CredentialOverrides: store,
		SandboxTeams:        teams,
		TeamCredentials:     store,
	}
	return proxy, forwarder, reads, writes, teams
}

var ciJobHeaders = merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok-ci")})

func TestACIJobRunningAsATeamForwardsWithTheTeamsCredential(t *testing.T) {
	t.Parallel()
	proxy, forwarder, _, writes, teams := newCITeamProxy(t, "infra-cred")
	writes.Grant("ci-job", model.RepoRef{Owner: "acme", Name: "widgets"})
	teams.Grant("ci-job", "infra")

	for _, req := range []struct{ method, path, query string }{
		{"GET", "/acme/widgets.git/info/refs", "service=git-upload-pack"},
		{"GET", "/acme/widgets.git/info/refs", "service=git-receive-pack"},
	} {
		resp := proxy.Handle(t.Context(), req.method, req.path, req.query, ciJobHeaders, nil)
		if resp.Status != 200 {
			t.Fatalf("%s %s?%s = %d: %s", req.method, req.path, req.query, resp.Status, resp.Body)
		}
	}
	for i, call := range forwarder.Calls {
		if call.Token == nil || *call.Token != "token-infra" {
			t.Errorf("call %d went out with %v, want the team's token-infra", i, call.Token)
		}
	}

	// Once the job is released the sandbox is nobody's again.
	teams.Drop("ci-job")
	if _, ok := teams.Team("ci-job"); ok {
		t.Fatal("team registration survived Drop")
	}
}

func TestACIJobWhoseTeamHasNoCredentialIsToldWhereToSetOne(t *testing.T) {
	t.Parallel()
	proxy, forwarder, _, writes, teams := newCITeamProxy(t, "")
	writes.Grant("ci-job", model.RepoRef{Owner: "acme", Name: "widgets"})
	teams.Grant("ci-job", "infra")

	resp := proxy.Handle(t.Context(), "GET", "/acme/widgets.git/info/refs", "service=git-receive-pack", ciJobHeaders, nil)
	if resp.Status == 200 {
		t.Fatal("a team with no credential reached GitHub")
	}
	if len(forwarder.Calls) != 0 {
		t.Fatalf("forwarded %d requests with no team credential -- the deployment's ladder is not a fallback", len(forwarder.Calls))
	}
	body := string(resp.Body)
	for _, want := range []string{"acme/widgets", "Settings → Git"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal %q does not name %q", body, want)
		}
	}
	if strings.Contains(strings.ToLower(body), "admin") {
		t.Errorf("refusal %q sends the team to an admin", body)
	}
}

func TestACIJobThatDeclaredOnlyReadCannotPushWithACapableTeamToken(t *testing.T) {
	t.Parallel()
	proxy, forwarder, reads, _, teams := newCITeamProxy(t, "infra-cred")
	reads.Grant("ci-job", model.RepoRef{Owner: "acme", Name: "widgets"})
	teams.Grant("ci-job", "infra")

	if resp := proxy.Handle(t.Context(), "GET", "/acme/widgets.git/info/refs", "service=git-upload-pack", ciJobHeaders, nil); resp.Status != 200 {
		t.Fatalf("declared read refused: %d %s", resp.Status, resp.Body)
	}
	resp := proxy.Handle(t.Context(), "POST", "/acme/widgets.git/git-receive-pack", "", postHeaders("git-receive-pack", "tok-ci"), nil)
	if resp.Status != 403 {
		t.Fatalf("push from a read-only job = %d (%s), want 403", resp.Status, resp.Body)
	}
	if len(forwarder.Calls) != 1 {
		t.Fatalf("forwarded %d requests, want only the fetch", len(forwarder.Calls))
	}
}

func TestGitHubsRefusalOfATeamsCredentialNamesTheTeam(t *testing.T) {
	t.Parallel()
	proxy, forwarder, _, writes, teams := newCITeamProxy(t, "infra-cred")
	writes.Grant("ci-job", model.RepoRef{Owner: "acme", Name: "widgets"})
	teams.Grant("ci-job", "infra")
	forwarder.Response = UpstreamResponse{Status: 403, Body: []byte("Permission to acme/widgets.git denied to infra-bot.")}

	resp := proxy.Handle(t.Context(), "GET", "/acme/widgets.git/info/refs", "service=git-receive-pack", ciJobHeaders, nil)
	if resp.Status != 403 {
		t.Fatalf("status = %d, want 403", resp.Status)
	}
	body := string(resp.Body)
	for _, want := range []string{"acme/widgets", "Settings → Git"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal %q does not name %q", body, want)
		}
	}
}

func TestCISandboxSingleTenantDefaultTeamPATNoCredentialsJSON(t *testing.T) {
	t.Parallel()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}

	upstreamDir := t.TempDir()
	bare := filepath.Join(upstreamDir, "acme", "widgets.git")
	runCmd(t, "", "git", "init", "--bare", "--initial-branch=main", bare)
	runCmd(t, "", "git", "-C", bare, "config", "http.receivepack", "true")

	// Create and push the task's pushed branch to the upstream repo.
	seed := filepath.Join(upstreamDir, "branch-seed")
	runCmd(t, "", "git", "clone", "-q", bare, seed)
	runCmd(t, seed, "git", "config", "user.email", "seed@example.com")
	runCmd(t, seed, "git", "config", "user.name", "seed")
	runCmd(t, seed, "git", "checkout", "-b", "grain/eu/task-4622")
	if err := os.WriteFile(filepath.Join(seed, "pushed.txt"), []byte("pushed-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, seed, "git", "add", "pushed.txt")
	runCmd(t, seed, "git", "commit", "-q", "-m", "task branch commit")
	runCmd(t, seed, "git", "push", "-q", "origin", "grain/eu/task-4622")

	const validPAT = "secret-default-pat"
	cgiHandler := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + upstreamDir, "GIT_HTTP_EXPORT_ALL=1"},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || (user != validPAT && pass != validPAT) {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized: invalid git token", http.StatusUnauthorized)
			return
		}
		cgiHandler.ServeHTTP(w, r)
	}))
	defer upstream.Close()

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	// Credential set directory has ONLY default-pat.token -- NO credentials.json!
	credDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(credDir, "default-pat.token"), []byte(validPAT), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(credDir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("expected credentials.json to not exist")
	}
	credentials, err := LoadCredentialSet(credDir)
	if err != nil {
		t.Fatal(err)
	}
	credentials.ForwardHost = upstreamHost

	store, ctx := openStoreForTest(t)
	store.SingleTenant = true
	store.GitDefaultHost = upstreamHost
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitConnections = []model.TeamGitConnection{{Credential: "default-pat", Host: upstreamHost}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reads := NewReadScopes()
	reads.Grant("ci-job-1", model.RepoRef{Owner: "acme", Name: "widgets"})
	teams := NewSandboxTeams()
	teams.Grant("ci-job-1", model.DefaultTeamID)

	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"ci-job-1": "tok-ci-1"}))
	if err != nil {
		t.Fatal(err)
	}

	proxy := &GitProxy{
		Authorizer:      &ReadOnlyScopeAuthorizer{Scopes: reads},
		Credentials:     credentials,
		Tokens:          tokens,
		SandboxTeams:    teams,
		TeamCredentials: store,
		SingleTenant:    true,
		Forwarder:       &RealForwarder{Host: upstreamHost, UseTLS: false},
	}
	proxySrv := httptest.NewServer(NewHandler(proxy))
	defer proxySrv.Close()

	// Clone the task's pushed branch through the proxy
	cloneDir := filepath.Join(t.TempDir(), "ci-clone")
	proxyURL := fmt.Sprintf("http://x-access-token:tok-ci-1@%s/acme/widgets.git", strings.TrimPrefix(proxySrv.URL, "http://"))
	runCmd(t, "", "git", "clone", "-q", "--branch", "grain/eu/task-4622", proxyURL, cloneDir)

	data, err := os.ReadFile(filepath.Join(cloneDir, "pushed.txt"))
	if err != nil {
		t.Fatalf("failed reading pushed.txt from checkout: %v", err)
	}
	if string(data) != "pushed-content\n" {
		t.Fatalf("checkout content = %q, want %q", data, "pushed-content\n")
	}
}

// TestACISandboxOfAnyTeamUsesTheDeploymentsConnection is the security
// expectation stage 1 of the single-tenant reset changes
// (docs/single-tenant-reset.md): a team's own git connections no longer
// choose what its CI job reaches with. Every sandbox, whichever team it
// runs as, goes out with the deployment's connection (the default team's
// row), and a team holding a connection of its own is not given it.
func TestACISandboxOfAnyTeamUsesTheDeploymentsConnection(t *testing.T) {
	t.Parallel()
	store, ctx := openStoreForTest(t)
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitConnections = []model.TeamGitConnection{{Credential: "deployment-pat"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	credentials, err := LoadCredentialSet(writeCredentialSet(t, nil, map[string]string{
		"alpha-pat": "token-alpha", "deployment-pat": "token-deployment",
	}))
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"ci-alpha": "tok-alpha"}))
	if err != nil {
		t.Fatal(err)
	}
	reads := NewReadScopes()
	reads.Grant("ci-alpha", model.RepoRef{Owner: "acme", Name: "widgets"})
	teams := NewSandboxTeams()
	teams.Grant("ci-alpha", "team-alpha")
	forwarder := &FakeForwarder{Response: UpstreamResponse{Status: 200, Body: []byte("ok")}}
	proxy := &GitProxy{
		Authorizer:      &ReadOnlyScopeAuthorizer{Scopes: reads},
		Credentials:     credentials,
		Tokens:          tokens,
		Forwarder:       forwarder,
		SandboxTeams:    teams,
		TeamCredentials: store,
	}
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok-alpha")})
	resp := proxy.Handle(t.Context(), "GET", "/acme/widgets.git/info/refs", "service=git-upload-pack", headers, nil)
	if resp.Status != 200 {
		t.Fatalf("team alpha's CI job = %d %s", resp.Status, resp.Body)
	}
	if len(forwarder.Calls) != 1 || forwarder.Calls[0].Token == nil || *forwarder.Calls[0].Token != "token-deployment" {
		t.Fatalf("forwarded %+v, want one call with the deployment's token and never the team's", forwarder.Calls)
	}
}

func TestRefusalNamesTeamAndPointsToGitConnections(t *testing.T) {
	t.Parallel()
	store, _ := openStoreForTest(t)
	store.SingleTenant = true
	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"ci-job": "tok-ci"}))
	if err != nil {
		t.Fatal(err)
	}
	reads := NewReadScopes()
	reads.Grant("ci-job", model.RepoRef{Owner: "acme", Name: "widgets"})
	teams := NewSandboxTeams()
	teams.Grant("ci-job", model.DefaultTeamID)

	// Empty credentials directory -- no credentials.json
	credDir := t.TempDir()
	credentials, err := LoadCredentialSet(credDir)
	if err != nil {
		t.Fatal(err)
	}

	proxy := &GitProxy{
		Authorizer:      &ReadOnlyScopeAuthorizer{Scopes: reads},
		Credentials:     credentials,
		Tokens:          tokens,
		SandboxTeams:    teams,
		TeamCredentials: store,
		SingleTenant:    true,
	}

	resp := proxy.Handle(t.Context(), "GET", "/acme/widgets.git/info/refs", "service=git-upload-pack", ciJobHeaders, nil)
	if resp.Status == 200 {
		t.Fatalf("expected refusal, got 200")
	}
	body := string(resp.Body)
	// Must point to Settings → Git, and not to the credentials.json
	// ladder, which nothing reads any more.
	if !strings.Contains(body, "Settings → Git") {
		t.Errorf("refusal %q does not point to Settings → Git", body)
	}
	if strings.Contains(body, "credentials.json") {
		t.Errorf("refusal %q points at credentials.json, which no longer resolves anything", body)
	}
}
