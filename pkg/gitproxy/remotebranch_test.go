package gitproxy

import (
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestReadRefAdvertisementReadsEachRef(t *testing.T) {
	id := strings.Repeat("a", 40)
	body := pkt("# service=git-upload-pack\n") + "0000" +
		pkt(id+" HEAD\x00multi_ack side-band-64k\n") +
		pkt(id+" refs/heads/main\n") +
		pkt(id+" refs/heads/grain/1e3bf7ca/task-3\n") + "0000"
	refs, err := readRefAdvertisement(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HEAD", "refs/heads/main", "refs/heads/grain/1e3bf7ca/task-3"} {
		if refs[want] != id {
			t.Errorf("refs[%q] = %q; refs = %v", want, refs[want], refs)
		}
	}
	if _, err := readRefAdvertisement(strings.NewReader("<html>Not found</html>")); err == nil {
		t.Error("an HTML page read as an advertisement")
	}
}

// The single-tenant walkthrough's B8, third half: a branch on a plain git
// host is read from that host over git, through the proxy's own
// forwarder -- here a real `git http-backend` serving a bare repository on
// loopback.
func TestRemoteBranchExistsAsksThePlainGitHost(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	root := t.TempDir()
	work := filepath.Join(t.TempDir(), "work")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "-q", "--bare", filepath.Join(root, "walk", "demo.git"))
	run(root, "init", "-q", "-b", "main", work)
	run(work, "commit", "-q", "--allow-empty", "-m", "first")
	run(work, "push", "-q", filepath.Join(root, "walk", "demo.git"), "main:main", "main:grain/1e3bf7ca/task-3")

	server := httptest.NewServer(&cgi.Handler{
		Path: gitBin, Args: []string{"http-backend"},
		Env:    []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
		Stderr: io.Discard,
	})
	defer server.Close()
	hostPort := strings.TrimPrefix(server.URL, "http://")
	h := CredentialHost{Host: hostPort, Kind: model.GitHostGit, BaseURL: server.URL}
	anon := Credential{Name: "anonymous"}

	if ok, err := RemoteBranchExists(h, anon, "walk", "demo", "grain/1e3bf7ca/task-3"); err != nil || !ok {
		t.Errorf("pushed branch = %v, %v; want true", ok, err)
	}
	if ok, err := RemoteBranchExists(h, anon, "walk", "demo", "grain/1e3bf7ca/task-4"); err != nil || ok {
		t.Errorf("unpushed branch = %v, %v; want false", ok, err)
	}
	if _, err := RemoteBranchExists(h, anon, "walk", "missing", "main"); err == nil {
		t.Error("a repository the host does not have read as having no such branch")
	}
	names, err := RemoteBranches(h, anon, "walk", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names, ","), "grain/1e3bf7ca/task-3,main"; got != want {
		t.Errorf("RemoteBranches = %v; want %s (HEAD and anything outside refs/heads/ left out)", names, want)
	}
	if _, err := RemoteBranches(h, anon, "walk", "missing"); err == nil {
		t.Error("a repository the host does not have listed as having no branches")
	}
}

// A host that refuses the read is an error, not "no such branch".
func TestRemoteBranchExistsReportsARefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer server.Close()
	h := CredentialHost{Host: strings.TrimPrefix(server.URL, "http://"), Kind: model.GitHostGit, BaseURL: server.URL}
	if _, err := RemoteBranchExists(h, Credential{Name: "anonymous"}, "o", "r", "b"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v; want the 401 named", err)
	}
}
