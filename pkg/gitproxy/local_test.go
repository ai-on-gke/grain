package gitproxy

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalRepositoryServesTheControllerCheckout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	runLocalGit(t, "", "init", "-b", "main", dir)
	runLocalGit(t, dir, "-c", "user.name=grain", "-c", "user.email=grain@example.invalid",
		"commit", "--allow-empty", "-m", "Initial state")

	local := &LocalRepository{Owner: "grain", Name: "grain-config", Dir: dir}
	if !local.Matches("GRAIN", "Grain-Config") {
		t.Fatal("the local alias is not case insensitive")
	}
	response, err := local.Forward("GET", "info/refs", "service=git-upload-pack",
		map[string]string{"Content-Type": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Stream)
	response.Stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != 200 {
		t.Fatalf("status = %d, body = %q", response.Status, body)
	}
	if !strings.Contains(string(body), "refs/heads/main") {
		t.Fatalf("advertisement does not contain main: %q", body)
	}
}

func runLocalGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// serveLocal puts r behind a plain smart-HTTP server, the shape GitProxy
// reaches it in once a request is authorized, so a real git client can
// push to it.
func serveLocal(t *testing.T, r *LocalRepository) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		parsed, ok := ParsePath(req.URL.Path)
		if !ok {
			http.NotFound(w, req)
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := r.ForwardRepo(parsed.Owner, parsed.Repo, req.Method, parsed.Action, req.URL.RawQuery,
			map[string]string{
				"Content-Type":     req.Header.Get("Content-Type"),
				"Content-Encoding": req.Header.Get("Content-Encoding"),
			}, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		WriteResponse(w, ProxyResponse(resp))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/grain/grain-config.git"
}

func localGitOutput(t *testing.T, dir string, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func newPushFixture(t *testing.T) (state, client, url string) {
	t.Helper()
	root := t.TempDir()
	state = filepath.Join(root, "state")
	runLocalGit(t, "", "init", "-q", "-b", "main", state)
	runLocalGit(t, state, "-c", "user.name=grain", "-c", "user.email=grain@example.invalid",
		"commit", "-q", "--allow-empty", "-m", "Initial state")
	// Whatever the state checkout's own config says, the proxy's
	// hardening is what decides.
	runLocalGit(t, state, "config", "receive.denyCurrentBranch", "ignore")
	runLocalGit(t, state, "config", "receive.fsckObjects", "false")

	url = serveLocal(t, &LocalRepository{Owner: "grain", Name: "grain-config", Dir: state})
	client = filepath.Join(root, "client")
	runLocalGit(t, "", "clone", "-q", url, client)
	runLocalGit(t, client, "config", "user.name", "sandbox")
	runLocalGit(t, client, "config", "user.email", "sandbox@example.invalid")
	return state, client, url
}

func TestLocalRepositoryAcceptsAnOrdinaryBranchPush(t *testing.T) {
	state, client, _ := newPushFixture(t)
	if err := os.WriteFile(filepath.Join(client, "settings.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLocalGit(t, client, "add", "settings.json")
	runLocalGit(t, client, "commit", "-q", "-m", "Propose settings")
	if out, err := localGitOutput(t, client, "", "push", "origin", "HEAD:refs/heads/proposal"); err != nil {
		t.Fatalf("an ordinary branch push was refused: %v\n%s", err, out)
	}
	if out, err := localGitOutput(t, state, "", "rev-parse", "--verify", "refs/heads/proposal"); err != nil {
		t.Fatalf("the pushed branch is not in the state repository: %v\n%s", err, out)
	}
}

func TestLocalRepositoryRefusesForgedTrees(t *testing.T) {
	for name, entry := range map[string]string{
		".git":   ".git",
		"dotdot": "..",
	} {
		t.Run(name, func(t *testing.T) {
			state, client, _ := newPushFixture(t)
			blob, err := localGitOutput(t, client, "[core]\n\thooksPath = /tmp\n", "hash-object", "-w", "--stdin")
			if err != nil {
				t.Fatalf("hash-object: %v\n%s", err, blob)
			}
			inner, err := localGitOutput(t, client, "100644 blob "+blob+"\tconfig\n", "mktree")
			if err != nil {
				t.Fatalf("mktree: %v\n%s", err, inner)
			}
			tree, err := localGitOutput(t, client, "040000 tree "+inner+"\t"+entry+"\n", "mktree")
			if err != nil {
				t.Fatalf("mktree: %v\n%s", err, tree)
			}
			commit, err := localGitOutput(t, client, "", "commit-tree", "-p", "HEAD", "-m", "forged", tree)
			if err != nil {
				t.Fatalf("commit-tree: %v\n%s", err, commit)
			}
			out, err := localGitOutput(t, client, "", "push", "origin", commit+":refs/heads/forged")
			if err == nil {
				t.Fatalf("a tree with a %q entry was accepted:\n%s", entry, out)
			}
			if _, err := localGitOutput(t, state, "", "cat-file", "-e", commit); err == nil {
				t.Fatalf("the forged commit %s was written into the state repository", commit)
			}
			if _, err := localGitOutput(t, state, "", "rev-parse", "--verify", "refs/heads/forged"); err == nil {
				t.Fatal("the forged branch was created")
			}
		})
	}
}

// A tree entry whose mode git does not know is refused too. git's own
// fsck ranks badFilemode as "info", which receive-pack's strict check
// lets through on its own -- found pushing one through the proxy
// (docs/test-runs/2026-10-01-credential-proxy-hardening.md) -- so the
// hardening raises it by name. mktree refuses to write such a tree, so
// the test writes the raw object itself.
func TestLocalRepositoryRefusesTreesWithBadModes(t *testing.T) {
	for _, mode := range []string{"100666", "123456"} {
		t.Run(mode, func(t *testing.T) {
			state, client, _ := newPushFixture(t)
			blob, err := localGitOutput(t, client, "x\n", "hash-object", "-w", "--stdin")
			if err != nil {
				t.Fatalf("hash-object: %v\n%s", err, blob)
			}
			raw, err := hex.DecodeString(blob)
			if err != nil {
				t.Fatal(err)
			}
			treeFile := filepath.Join(t.TempDir(), "tree")
			if err := os.WriteFile(treeFile, append([]byte(mode+" file\x00"), raw...), 0o644); err != nil {
				t.Fatal(err)
			}
			tree, err := localGitOutput(t, client, "", "hash-object", "-t", "tree", "-w", "--literally", treeFile)
			if err != nil {
				t.Fatalf("hash-object tree: %v\n%s", err, tree)
			}
			commit, err := localGitOutput(t, client, "", "commit-tree", "-p", "HEAD", "-m", "bad mode", tree)
			if err != nil {
				t.Fatalf("commit-tree: %v\n%s", err, commit)
			}
			out, err := localGitOutput(t, client, "", "push", "origin", commit+":refs/heads/forged")
			if err == nil {
				t.Fatalf("a tree with mode %s was accepted:\n%s", mode, out)
			}
			if _, err := localGitOutput(t, state, "", "cat-file", "-e", commit); err == nil {
				t.Fatalf("the forged commit %s was written into the state repository", commit)
			}
			if _, err := localGitOutput(t, state, "", "rev-parse", "--verify", "refs/heads/forged"); err == nil {
				t.Fatal("the forged branch was created")
			}
		})
	}
}

func TestLocalRepositoryRefusesUpdatesToTheCheckedOutBranch(t *testing.T) {
	state, client, _ := newPushFixture(t)
	before, _ := localGitOutput(t, state, "", "rev-parse", "HEAD")
	runLocalGit(t, client, "commit", "-q", "--allow-empty", "-m", "Straight onto the state")
	if out, err := localGitOutput(t, client, "", "push", "origin", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("a push to the checked-out branch was accepted:\n%s", out)
	}
	if out, err := localGitOutput(t, client, "", "push", "origin", ":refs/heads/main"); err == nil {
		t.Fatalf("deleting the checked-out branch was accepted:\n%s", out)
	}
	if after, _ := localGitOutput(t, state, "", "rev-parse", "HEAD"); after != before {
		t.Fatalf("the state branch moved from %s to %s", before, after)
	}
}

// git gzips an upload-pack request body once it passes 1 KiB, which a
// fetch reaches with a couple of dozen wants or haves. git http-backend
// only inflates it when told the encoding, so without that a clone of a
// repository with many branches fails partway through negotiation.
func TestLocalRepositoryServesAGzippedFetch(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	runLocalGit(t, "", "init", "-q", "-b", "main", state)
	for i := 0; i < 40; i++ {
		runLocalGit(t, state, "-c", "user.name=grain", "-c", "user.email=grain@example.invalid",
			"commit", "-q", "--allow-empty", "-m", fmt.Sprintf("state %d", i))
		runLocalGit(t, state, "branch", fmt.Sprintf("proposal-%d", i))
	}
	url := serveLocal(t, &LocalRepository{Owner: "grain", Name: "grain-config", Dir: state})
	client := filepath.Join(t.TempDir(), "client")
	if out, err := localGitOutput(t, filepath.Dir(client), "", "clone", "-q", "--no-single-branch", url, client); err != nil {
		t.Fatalf("clone of a repository with many branches failed: %v\n%s", err, out)
	}
	if out, err := localGitOutput(t, client, "", "rev-parse", "--verify", "refs/remotes/origin/proposal-39"); err != nil {
		t.Fatalf("the clone is missing a branch: %v\n%s", err, out)
	}
}

func TestLocalRepositoryServesStreamingPush(t *testing.T) {
	state, client, _ := newPushFixture(t)
	local := &LocalRepository{Owner: "grain", Name: "grain-config", Dir: state}

	if err := os.WriteFile(filepath.Join(client, "streaming.txt"), []byte("streaming test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLocalGit(t, client, "add", "streaming.txt")
	runLocalGit(t, client, "commit", "-q", "-m", "Commit streaming")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		parsed, ok := ParsePath(req.URL.Path)
		if !ok {
			http.NotFound(w, req)
			return
		}
		headers := map[string]string{
			"Content-Type":     req.Header.Get("Content-Type"),
			"Content-Encoding": req.Header.Get("Content-Encoding"),
		}
		var resp UpstreamResponse
		var err error
		if parsed.Action == "git-receive-pack" {
			spool, sErr := spoolBody(req.Body, 10<<20, 100) // 100 byte threshold to trigger disk spooling
			if sErr != nil {
				http.Error(w, sErr.Error(), http.StatusBadRequest)
				return
			}
			defer spool.Close()
			r, rErr := spool.NewReader()
			if rErr != nil {
				http.Error(w, rErr.Error(), http.StatusInternalServerError)
				return
			}
			resp, err = local.ForwardRepoStream(parsed.Owner, parsed.Repo, req.Method, parsed.Action, req.URL.RawQuery, headers, r, spool.Len())
		} else {
			body, _ := io.ReadAll(req.Body)
			resp, err = local.ForwardRepo(parsed.Owner, parsed.Repo, req.Method, parsed.Action, req.URL.RawQuery, headers, body)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		WriteResponse(w, ProxyResponse(resp))
	}))
	defer srv.Close()

	runLocalGit(t, client, "remote", "set-url", "origin", srv.URL+"/grain/grain-config.git")
	if out, err := localGitOutput(t, client, "", "push", "origin", "HEAD:refs/heads/streaming-branch"); err != nil {
		t.Fatalf("streaming push failed: %v\n%s", err, out)
	}
	if out, err := localGitOutput(t, state, "", "rev-parse", "--verify", "refs/heads/streaming-branch"); err != nil {
		t.Fatalf("streaming-branch not in state repo: %v\n%s", err, out)
	}
}

// Only grain/config is served; every grain/config-<anything> name is one
// the proxy answers not found rather than forwards.
func TestRetiredConfigRepo(t *testing.T) {
	for _, tc := range []struct {
		host, owner, name string
		want              bool
	}{
		{"", "grain", "config-default", true},
		{"", "GRAIN", "Config-Alpha", true},
		{"", "grain", "config-", true},
		{"", "grain", "config", false},
		{"", "grain", "configs", false},
		{"", "acme", "config-default", false},
		{"git.example.com", "grain", "config-default", false},
	} {
		if got := RetiredConfigRepo(tc.host, tc.owner, tc.name); got != tc.want {
			t.Errorf("RetiredConfigRepo(%q, %q, %q) = %v, want %v", tc.host, tc.owner, tc.name, got, tc.want)
		}
	}
	if got := RetiredConfigRepoReason("grain", "config-default"); got != "grain/config-default not found: only grain/config exists" {
		t.Errorf("RetiredConfigRepoReason = %q", got)
	}
}

func TestDisabledLocalRepositoryRefusesRequestsInsteadOfForwarding(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	runLocalGit(t, "", "init", "-b", "main", dir)
	runLocalGit(t, dir, "-c", "user.name=grain", "-c", "user.email=grain@example.invalid",
		"commit", "--allow-empty", "-m", "Initial state")

	enabled := false
	local := &LocalRepository{
		Owner:   "grain",
		Name:    "config",
		Dir:     dir,
		Enabled: func() bool { return enabled },
	}
	if local.Matches("grain", "config") {
		t.Fatal("disabled LocalRepository matched grain/config")
	}
	if !local.DisabledFor("GRAIN", "Config") {
		t.Fatal("disabled LocalRepository did not report DisabledFor(GRAIN, Config)")
	}

	proxy := &GitProxy{
		Tokens: mustAlwaysAuthTokens(t),
		Local:  local,
	}
	resp := proxy.HandleRequest(t.Context(), "GET", "/grain/config.git/info/refs", "service=git-upload-pack", map[string]string{
		"User-Agent":    "git/2.43.0",
		"Authorization": basicAuthHeader("tok0"),
	}, nil)
	if resp.Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "local config repository is disabled when an external config repository is configured") {
		t.Fatalf("unexpected body: %q", resp.Body)
	}

	enabled = true
	if !local.Matches("grain", "config") || local.DisabledFor("grain", "config") {
		t.Fatal("enabled LocalRepository should match and not be disabled")
	}
}
