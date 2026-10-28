package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

func gitHTTPBackendWithAuth(projectRoot, validToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || (user != validToken && pass != validToken) {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized: invalid git token", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd := exec.Command("git", "http-backend")
		cmd.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+projectRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+r.URL.Path,
			"QUERY_STRING="+r.URL.RawQuery,
			"REQUEST_METHOD="+r.Method,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"),
			"HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
			"GATEWAY_INTERFACE=CGI/1.1",
			"SERVER_PROTOCOL=HTTP/1.1",
		)
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.Output()
		if err != nil {
			http.Error(w, fmt.Sprintf("git http-backend: %v", err), http.StatusInternalServerError)
			return
		}
		sep := []byte("\r\n\r\n")
		headEnd := bytes.Index(out, sep)
		if headEnd == -1 {
			sep = []byte("\n\n")
			headEnd = bytes.Index(out, sep)
		}
		headerBlock := string(out[:headEnd])
		respBody := out[headEnd+len(sep):]

		status := http.StatusOK
		for _, line := range strings.Split(headerBlock, "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			key, value, _ := strings.Cut(line, ":")
			value = strings.TrimSpace(value)
			if strings.EqualFold(key, "Status") {
				if code, err := strconv.Atoi(strings.Fields(value)[0]); err == nil {
					status = code
				}
				continue
			}
			w.Header().Add(key, value)
		}
		w.WriteHeader(status)
		w.Write(respBody)
	}
}

func TestCiWorkflowSourceClonesWithDefaultTeamGitConnection(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	gitRoot := t.TempDir()
	repoDir := filepath.Join(gitRoot, "bwsalmon", "grain.git")
	gitIn(t, gitRoot, "init", "-q", "--bare", repoDir)

	seed := filepath.Join(t.TempDir(), "seed")
	gitIn(t, gitRoot, "clone", "-q", repoDir, seed)
	gitIn(t, seed, "config", "user.email", "test@example.com")
	gitIn(t, seed, "config", "user.name", "test")
	gitIn(t, seed, "checkout", "-b", "main")
	if err := os.MkdirAll(filepath.Join(seed, ".grain", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	workflow := "name: default team checks\non: push\njobs:\n  check:\n    steps:\n      - run: true\n"
	if err := os.WriteFile(filepath.Join(seed, ".grain", "workflows", "check.yml"), []byte(workflow), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-q", "-m", "add workflow")
	gitIn(t, seed, "push", "-q", "origin", "main")

	const defaultToken = "secret-default-token"
	upstream := httptest.NewServer(gitHTTPBackendWithAuth(gitRoot, defaultToken))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	credDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(credDir, "grain-team-default.token"), []byte(defaultToken), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := gitproxy.LoadCredentialSet(credDir)
	if err != nil {
		t.Fatal(err)
	}

	store := teamConfigTestStore(t)
	store.GitDefaultHost = upstreamHost
	credentials.ForwardHost = upstreamHost
	ctx := context.Background()
	// Default team has GitConnections, and no custom team claims bwsalmon/grain.
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitConnections = []model.TeamGitConnection{{Credential: "grain-team-default", Host: upstreamHost}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	authSource := CiWorkflowSource{
		Credentials:         credentials,
		GitHubHost:          upstreamHost,
		InsecureHTTP:        true,
		TempDir:             t.TempDir(),
		TeamGitCredential:   store.TeamGitCredential,
		TeamGitCredentialOn: store.TeamGitCredentialOn,
	}
	workflows, commit, err := authSource.Workflows(ctx, "bwsalmon/grain", "main")
	if err != nil {
		t.Fatalf("authSource.Workflows failed: %v", err)
	}
	if len(workflows) != 1 || workflows[0].Name != "default team checks" {
		t.Fatalf("unexpected workflows: %+v", workflows)
	}
	if commit == "" {
		t.Fatal("expected non-empty commit")
	}
}

func TestCiWorkflowSourceClonesWithTeamCredentialCallback(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	gitRoot := t.TempDir()
	repoDir := filepath.Join(gitRoot, "acme", "widgets.git")
	gitIn(t, gitRoot, "init", "-q", "--bare", repoDir)

	seed := filepath.Join(t.TempDir(), "seed")
	gitIn(t, gitRoot, "clone", "-q", repoDir, seed)
	gitIn(t, seed, "config", "user.email", "test@example.com")
	gitIn(t, seed, "config", "user.name", "test")
	gitIn(t, seed, "checkout", "-b", "main")
	if err := os.MkdirAll(filepath.Join(seed, ".grain", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	workflow := "name: cb checks\non: push\njobs:\n  check:\n    steps:\n      - run: true\n"
	if err := os.WriteFile(filepath.Join(seed, ".grain", "workflows", "check.yml"), []byte(workflow), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-q", "-m", "add workflow")
	gitIn(t, seed, "push", "-q", "origin", "main")

	const cbToken = "secret-cb-token"
	upstream := httptest.NewServer(gitHTTPBackendWithAuth(gitRoot, cbToken))
	defer upstream.Close()

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	src := CiWorkflowSource{
		GitHubHost:   upstreamHost,
		InsecureHTTP: true,
		TempDir:      t.TempDir(),
		TeamCredential: func(ctx context.Context, repo model.RepoRef) (string, error) {
			if repo.Owner == "acme" && repo.Name == "widgets" {
				return cbToken, nil
			}
			return "", nil
		},
	}
	workflows, commit, err := src.Workflows(context.Background(), "acme/widgets", "main")
	if err != nil {
		t.Fatalf("src.Workflows with TeamCredential callback failed: %v", err)
	}
	if len(workflows) != 1 || workflows[0].Name != "cb checks" {
		t.Fatalf("unexpected workflows: %+v", workflows)
	}
	if commit == "" {
		t.Fatal("expected non-empty commit")
	}
}

func TestBuildCIServiceWiresTeamGitCredentialsToCiWorkflowSource(t *testing.T) {
	t.Parallel()
	store := teamConfigTestStore(t)
	cfg := Config{DataDir: t.TempDir()}
	service := BuildCIService(context.Background(), cfg, nil, nil, nil, store, nil, nil, "", nil, nil, nil, nil, nil, nil, nil, nil, nil)
	layered, ok := service.Source.(ci.Layered)
	if !ok || len(layered) == 0 {
		t.Fatalf("expected service.Source to be non-empty ci.Layered, got %T", service.Source)
	}
	src, ok := layered[0].(CiWorkflowSource)
	if !ok {
		t.Fatalf("expected layered[0] to be CiWorkflowSource, got %T", layered[0])
	}
	if src.TeamGitCredential == nil {
		t.Error("expected src.TeamGitCredential to be non-nil when store is provided")
	}
	if src.TeamGitCredentialOn == nil {
		t.Error("expected src.TeamGitCredentialOn to be non-nil when store is provided")
	}
}

func TestCiSandboxRegistersTeamInSandboxTeamsEvenWithoutDeclaredAccess(t *testing.T) {
	t.Parallel()
	teams := gitproxy.NewSandboxTeams()
	sandbox := &CiSandbox{
		SandboxName: "ci-sandbox-ordinary",
		Owner: CiSandboxes{
			Teams: teams,
		},
	}
	ctx := context.Background()
	// Job with no declared repo access (ids is nil / empty)
	if err := sandbox.registerWithProxy(ctx, model.RepoRef{Owner: "acme", Name: "widgets"}, "push", false, nil); err != nil {
		t.Fatalf("registerWithProxy failed: %v", err)
	}

	team, ok := teams.Team("ci-sandbox-ordinary")
	if !ok || team != model.DefaultTeamID {
		t.Fatalf("teams.Team = (%q, %v), want (%q, true)", team, ok, model.DefaultTeamID)
	}

	teams.Drop("ci-sandbox-ordinary")
	if _, ok := teams.Team("ci-sandbox-ordinary"); ok {
		t.Fatal("expected team registration to be dropped")
	}
}

func TestCiWorkflowAndDeclarationsUsePerRepoGitCredentialOverrideAndDefaultHostConnection(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	gitRoot := t.TempDir()
	for _, repo := range []string{"bwsalmon/grain.git", "acme/override.git"} {
		repoDir := filepath.Join(gitRoot, filepath.FromSlash(repo))
		gitIn(t, gitRoot, "init", "-q", "--bare", repoDir)
		gitIn(t, repoDir, "symbolic-ref", "HEAD", "refs/heads/main")

		seed := filepath.Join(t.TempDir(), "seed")
		gitIn(t, gitRoot, "clone", "-q", repoDir, seed)
		gitIn(t, seed, "config", "user.email", "test@example.com")
		gitIn(t, seed, "config", "user.name", "test")
		gitIn(t, seed, "checkout", "-b", "main")
		if err := os.MkdirAll(filepath.Join(seed, ".grain", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		workflow := "name: checks\non: push\njobs:\n  check:\n    steps:\n      - run: true\n"
		if err := os.WriteFile(filepath.Join(seed, ".grain", "workflows", "check.yml"), []byte(workflow), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, seed, "add", ".")
		gitIn(t, seed, "commit", "-q", "-m", "add workflow")
		gitIn(t, seed, "push", "-q", "origin", "main")
	}

	const defaultToken = "secret-default-token"
	const overrideToken = "secret-override-token"
	defaultBackend := gitHTTPBackendWithAuth(gitRoot, defaultToken)
	overrideBackend := gitHTTPBackendWithAuth(gitRoot, overrideToken)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/acme/override.git/") {
			overrideBackend(w, r)
			return
		}
		defaultBackend(w, r)
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	credDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(credDir, "grain-team-default.token"), []byte(defaultToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "repo-override.token"), []byte(overrideToken), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := gitproxy.LoadCredentialSet(credDir)
	if err != nil {
		t.Fatal(err)
	}

	// Leave store.GitDefaultHost empty (as sqlite.Open does when no custom host is set on the store)
	// and configure GitConnections on the default host (""), plus a per-repo GitCredential override.
	store := teamConfigTestStore(t)
	ctx := context.Background()
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitConnections = []model.TeamGitConnection{{Credential: "grain-team-default", Host: ""}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:          model.RepoRef{Owner: "acme", Name: "override"},
		GitCredential: "repo-override",
		Declarations:  true,
	}); err != nil {
		t.Fatal(err)
	}

	authSource := CiWorkflowSource{
		Credentials:         credentials,
		GitHubHost:          upstreamHost,
		InsecureHTTP:        true,
		TempDir:             t.TempDir(),
		TeamGitCredential:   store.TeamGitCredential,
		TeamGitCredentialOn: store.TeamGitCredentialOn,
	}

	if _, commit, err := authSource.Workflows(ctx, "bwsalmon/grain", "main"); err != nil || commit == "" {
		t.Fatalf("Workflows(bwsalmon/grain) with default-host GitConnections: commit=%q err=%v", commit, err)
	}
	if _, commit, err := authSource.Workflows(ctx, "acme/override", "main"); err != nil || commit == "" {
		t.Fatalf("Workflows(acme/override) with per-repo GitCredential override: commit=%q err=%v", commit, err)
	}

	declSource := RepoDeclarationSource{Clone: authSource}
	head, err := declSource.Head(ctx, model.RepoRef{Owner: "acme", Name: "override"})
	if err != nil || head == "" {
		t.Fatalf("RepoDeclarationSource.Head(acme/override): head=%q err=%v", head, err)
	}
	if _, commit, err := declSource.Read(ctx, model.RepoRef{Owner: "acme", Name: "override"}); err != nil || commit != head {
		t.Fatalf("RepoDeclarationSource.Read(acme/override): commit=%q (want %q) err=%v", commit, head, err)
	}
}

func TestCiWorkflowSourceDoesNotLeakDefaultHostCredentialToOffDefaultHost(t *testing.T) {
	t.Parallel()
	credDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(credDir, "grain-team-default.token"), []byte("default-host-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := gitproxy.LoadCredentialSet(credDir)
	if err != nil {
		t.Fatal(err)
	}

	src := CiWorkflowSource{
		Credentials: credentials,
		TeamGitCredentialOn: func(ctx context.Context, team, host, owner, repo string) (string, error) {
			return "grain-team-default", nil
		},
	}
	if cred, ok := src.resolveTeamGitCredential(context.Background(), model.RepoRef{
		Host:  "ghe.example.com",
		Owner: "acme",
		Name:  "widgets",
	}); ok {
		t.Fatalf("resolveTeamGitCredential leaked default-host credential %+v to ghe.example.com", cred)
	}
}

func TestRepoDeclarationSourceHeadIgnoresSlashHEADRefOnUnbornRepository(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	gitRoot := t.TempDir()
	repoDir := filepath.Join(gitRoot, "acme", "unborn.git")
	gitIn(t, gitRoot, "init", "-q", "--bare", repoDir)
	gitIn(t, repoDir, "symbolic-ref", "HEAD", "refs/heads/main")

	seed := filepath.Join(t.TempDir(), "seed")
	gitIn(t, gitRoot, "clone", "-q", repoDir, seed)
	gitIn(t, seed, "config", "user.email", "test@example.com")
	gitIn(t, seed, "config", "user.name", "test")
	gitIn(t, seed, "checkout", "-b", "fix/HEAD")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("branch ending in /HEAD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-q", "-m", "push fix/HEAD while main is still unborn")
	gitIn(t, seed, "push", "-q", "origin", "fix/HEAD")

	const token = "secret-unborn-token"
	upstream := httptest.NewServer(gitHTTPBackendWithAuth(gitRoot, token))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	declSource := RepoDeclarationSource{
		Clone: CiWorkflowSource{
			GitHubHost:   upstreamHost,
			InsecureHTTP: true,
			TempDir:      t.TempDir(),
			TeamCredential: func(ctx context.Context, repo model.RepoRef) (string, error) {
				return token, nil
			},
		},
	}

	ctx := context.Background()
	head, err := declSource.Head(ctx, model.RepoRef{Owner: "acme", Name: "unborn"})
	if err != nil {
		t.Fatalf("Head on unborn main with refs/heads/fix/HEAD present: %v", err)
	}
	if head != "" {
		t.Fatalf("Head = %q, want empty string when default branch is unborn and only refs/heads/fix/HEAD exists", head)
	}

	// Now push main and verify Head returns main's commit SHA.
	gitIn(t, seed, "checkout", "-b", "main")
	gitIn(t, seed, "push", "-q", "origin", "main")
	head, err = declSource.Head(ctx, model.RepoRef{Owner: "acme", Name: "unborn"})
	if err != nil || head == "" {
		t.Fatalf("Head after pushing main = %q, err = %v; want non-empty commit SHA", head, err)
	}
}
