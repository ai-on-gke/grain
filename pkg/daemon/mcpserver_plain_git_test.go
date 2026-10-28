package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
)

func setupPlainGitHostSecrets(t *testing.T, host string) string {
	t.Helper()
	dataDir := t.TempDir()
	secretsDir := filepath.Join(dataDir, "secrets", "github")
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hostJSON, err := json.Marshal(gitproxy.CredentialHost{
		Host: host,
		Kind: model.GitHostGit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, host+".host.json"), hostJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func TestPullRequestReaderPlainGitHostRefusal(t *testing.T) {
	t.Parallel()
	dataDir := setupPlainGitHostSecrets(t, "git.example.com")
	repo := "git.example.com/acme/widgets"

	_, _, err := PullRequestReader(dataDir, "github.com", false, repo, "main", "", "", false, false, "")
	if err == nil {
		t.Fatal("PullRequestReader expected error for plain git host, got nil")
	}
	want := `repository "git.example.com/acme/widgets" is on git.example.com, a plain git host with no pull request or CI checks API`
	if err.Error() != want {
		t.Errorf("PullRequestReader err = %q, want %q", err.Error(), want)
	}
}

func TestIssueReaderPlainGitHostRefusal(t *testing.T) {
	t.Parallel()
	dataDir := setupPlainGitHostSecrets(t, "git.example.com")
	repo := "git.example.com/acme/widgets"

	_, _, err := issueReader(dataDir, "github.com", false, repo, "")
	if err == nil {
		t.Fatal("issueReader expected error for plain git host, got nil")
	}
	if !strings.Contains(err.Error(), "plain git host with no issues API") {
		t.Errorf("issueReader err = %q, want plain git host refusal", err.Error())
	}
}

func TestGithubDefaultBranchReaderPlainGitHostRefusal(t *testing.T) {
	t.Parallel()
	dataDir := setupPlainGitHostSecrets(t, "git.example.com")

	fn := githubDefaultBranchReader(dataDir, "github.com", false, "")
	if fn == nil {
		t.Fatal("githubDefaultBranchReader returned nil function")
	}

	branch, err := fn("git.example.com/acme", "widgets")
	if err == nil {
		t.Fatalf("githubDefaultBranchReader expected error, got branch %q", branch)
	}
	want := `repository "git.example.com/acme/widgets" is on git.example.com, a plain git host with no forge API`
	if err.Error() != want {
		t.Errorf("githubDefaultBranchReader err = %q, want %q", err.Error(), want)
	}
}

func TestCredentialTokenSourceGitOnlyPlainGitHost(t *testing.T) {
	t.Parallel()
	dataDir := setupPlainGitHostSecrets(t, "git.example.com")
	creds, err := gitproxy.LoadCredentialSet(filepath.Join(dataDir, "secrets", "github"))
	if err != nil {
		t.Fatal(err)
	}

	src := credentialTokenSource{credentials: creds}
	reason := src.GitOnly("git.example.com/acme", "widgets")
	want := "repository git.example.com/acme/widgets is on git.example.com, a plain git host with no forge API or pull requests"
	if reason != want {
		t.Errorf("GitOnly = %q, want %q", reason, want)
	}
}

func TestPullRequestToolsPlainGitHost(t *testing.T) {
	t.Parallel()
	dataDir := setupPlainGitHostSecrets(t, "git.example.com")
	tools := PullRequestTools(dataDir, "github.com", false, "git.example.com/acme/widgets", "branch", "", "", false, false, "", nil)
	if len(tools) != 5 {
		t.Fatalf("len(tools) = %d, want 5", len(tools))
	}
	for _, tool := range tools {
		res := tool.Handler(t.Context(), map[string]any{})
		if !res.IsError {
			t.Errorf("tool %s expected error response, got text: %s", tool.Name, res.Text)
		}
	}
}

func TestIssueToolsPlainGitHost(t *testing.T) {
	t.Parallel()
	dataDir := setupPlainGitHostSecrets(t, "git.example.com")
	tools := issueTools(dataDir, "github.com", false, "git.example.com/acme/widgets", "")
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	for _, tool := range tools {
		res := tool.Handler(t.Context(), map[string]any{})
		if !res.IsError {
			t.Errorf("tool %s expected error response, got text: %s", tool.Name, res.Text)
		}
	}
}

// The single-tenant walkthrough's B8: a task's branch on a plain git host
// was checked by asking the GitHub API about owner/name, host dropped.
// The token source reads it from the host over git instead, and leaves a
// repository on GitHub to the API.
func TestCredentialTokenSourceReadsAPlainGitHostsBranchOverGit(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("b", 40)
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		for _, line := range []string{"# service=git-upload-pack\n", "", id + " refs/heads/grain/1e3bf7ca/task-3\x00side-band-64k\n", ""} {
			if line == "" {
				io.WriteString(w, "0000")
				continue
			}
			fmt.Fprintf(w, "%04x%s", len(line)+4, line)
		}
	}))
	defer server.Close()
	hostPort := strings.TrimPrefix(server.URL, "http://")
	creds, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.SetToken("plain", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := creds.SetHost("plain", gitproxy.CredentialHost{Host: hostPort, Kind: model.GitHostGit, BaseURL: server.URL}); err != nil {
		t.Fatal(err)
	}
	src := credentialTokenSource{credentials: creds,
		teamCredential: func(host, owner, repo string) (string, bool) { return "plain", host == hostPort }}

	exists, ok, err := src.GitBranchExists(hostPort+"/walk", "demo", "grain/1e3bf7ca/task-3")
	if !ok || err != nil || !exists {
		t.Fatalf("GitBranchExists = %v, %v, %v; want the pushed branch found over git", exists, ok, err)
	}
	if asked != "/walk/demo.git/info/refs?service=git-upload-pack" {
		t.Errorf("the host was asked %q", asked)
	}
	if exists, ok, err := src.GitBranchExists(hostPort+"/walk", "demo", "grain/1e3bf7ca/task-4"); !ok || err != nil || exists {
		t.Errorf("unpushed branch = %v, %v, %v; want false", exists, ok, err)
	}
	if _, ok, _ := src.GitBranchExists("walk", "demo", "b"); ok {
		t.Error("a repository on github.com was read over git rather than left to the API")
	}
}
