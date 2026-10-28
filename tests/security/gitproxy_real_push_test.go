package security

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// The ref check in gitproxy_test.go is driven with hand-built pkt-lines.
// These drive it with the git binary a sandbox actually runs: a real
// clone through the proxy, then `git push` of each kind of ref update a
// sandbox might try -- the default branch, a force-push of another
// branch, a branch deletion, a tag, and its own task branch -- through
// the production authorizer chain (BuildProxy with read and write
// scopes, as cmd/grain's startGitProxy wires it), in front of githubsim's
// GitHub auth layer and a real `git http-backend` over a bare repository.
//
// A refusal counts only if the upstream repository's refs did not move
// and no receive-pack body reached it, and the sentence git prints is
// asserted, because that sentence is all an agent has to go on
// (docs/test-runs/2026-10-04-security-boundaries.md).

// recordingGitBackend serves projectRoot with `git http-backend` and
// remembers every receive-pack POST that reached it.
type recordingGitBackend struct {
	root     string
	mu       sync.Mutex
	receives int
}

func (b *recordingGitBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
		b.mu.Lock()
		b.receives++
		b.mu.Unlock()
	}
	cmd := exec.Command("git", "http-backend")
	cmd.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+b.root,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO="+r.URL.Path,
		"QUERY_STRING="+r.URL.RawQuery,
		"REQUEST_METHOD="+r.Method,
		"CONTENT_TYPE="+r.Header.Get("Content-Type"),
		"HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
		"GATEWAY_INTERFACE=CGI/1.1",
		"SERVER_PROTOCOL=HTTP/1.1",
		// Basic auth reached this far only past githubsim's door; the
		// backend needs a user to accept a push at all.
		"REMOTE_USER=bot",
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
	status := http.StatusOK
	for _, line := range strings.Split(string(out[:headEnd]), "\n") {
		key, value, _ := strings.Cut(strings.TrimRight(line, "\r"), ":")
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if strings.EqualFold(key, "Status") {
			if code, err := strconv.Atoi(strings.Fields(value)[0]); err == nil {
				status = code
			}
			continue
		}
		w.Header().Add(key, value)
	}
	w.WriteHeader(status)
	_, _ = w.Write(out[headEnd+len(sep):])
}

func (b *recordingGitBackend) receivePacks() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.receives
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// realPushRig is one upstream bare repository, acme/widgets, seeded with
// main, release and tag-free history, and a live run of task tw on it
// whose sandbox holds a clone made through the proxy.
type realPushRig struct {
	backend  *recordingGitBackend
	bare     string
	clone    string
	upstream map[string]string // ref -> sha, as seeded
}

func newRealPushRig(t *testing.T, grants []model.Grant) *realPushRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(ctx, model.Task{
		ID: "tw", Intent: model.IntentImplement, Title: "real push boundary",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Target: &target, Grants: grants, Binding: model.BindingDirective,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "tw-1", TaskID: "tw", Sandbox: "sandbox-w", Attempt: 1, StartedAt: time.Now(),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "bot"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The upstream: a bare repository with main and release, served
	// behind githubsim's door, which lets the deployment's bot token
	// read and write it.
	root := t.TempDir()
	bare := filepath.Join(root, "acme", "widgets.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "--bare", "-b", "main", bare)
	for _, kv := range [][2]string{{"http.receivepack", "true"}, {"maintenance.autoDetach", "false"}, {"gc.autoDetach", "false"}} {
		gitIn(t, bare, "config", kv[0], kv[1])
	}
	seed := filepath.Join(root, "seed")
	gitIn(t, root, "clone", bare, seed)
	gitIn(t, seed, "config", "user.email", "seed@example.com")
	gitIn(t, seed, "config", "user.name", "seed")
	gitIn(t, seed, "commit", "--allow-empty", "-m", "initial commit")
	gitIn(t, seed, "push", "origin", "main")
	gitIn(t, seed, "commit", "--allow-empty", "-m", "release work")
	gitIn(t, seed, "push", "origin", "HEAD:refs/heads/release")

	backend := &recordingGitBackend{root: root}
	upstream := &githubsim.GitUpstream{
		Tokens:  map[string]githubsim.GitAccess{"bot-token": {Read: []string{"acme/widgets"}, Write: []string{"acme/widgets"}}},
		Backend: backend,
	}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)
	upstreamURL, err := url.Parse(upstreamServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	writeSecret(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "bot"}`)
	writeSecret(t, filepath.Join(dataDir, "secrets", "github", "bot.token"), "bot-token")
	writeSecret(t, filepath.Join(dataDir, "secrets", "sandbox-tokens.json"), `{"sandbox-w": "token-for-w"}`)
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store, ForwardHost: upstreamURL.Host, ForwardTLS: false,
		ReadScopes: gitproxy.NewReadScopes(), WriteScopes: gitproxy.NewWriteScopes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(proxyServer.Close)
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("x", "token-for-w")

	// The sandbox: a clone through the proxy with the run's own token,
	// one new commit on top of main, and a local tag.
	clone := filepath.Join(t.TempDir(), "work")
	gitIn(t, filepath.Dir(clone), "clone", proxyURL.String()+"/acme/widgets.git", clone)
	gitIn(t, clone, "config", "user.email", "agent@example.com")
	gitIn(t, clone, "config", "user.name", "agent")
	gitIn(t, clone, "commit", "--allow-empty", "-m", "agent work")
	gitIn(t, clone, "tag", "v9.9.9")

	rig := &realPushRig{backend: backend, bare: bare, clone: clone, upstream: map[string]string{}}
	rig.upstream = rig.refs(t)
	return rig
}

// refs is every ref the upstream repository has, and where it points.
func (r *realPushRig) refs(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(gitIn(t, r.bare, "for-each-ref", "--format=%(refname) %(objectname)")), "\n") {
		if ref, sha, ok := strings.Cut(line, " "); ok {
			got[ref] = sha
		}
	}
	return got
}

// realPushCases are the pushes §8 of docs/test-plans/2026-10-04.md
// names, each as the refspec git is given and the ref it updates.
var realPushCases = []struct {
	name, refspec, ref string
}{
	{"push to main", "HEAD:refs/heads/main", "refs/heads/main"},
	{"force-push another branch", "+HEAD:refs/heads/release", "refs/heads/release"},
	{"delete a branch", ":refs/heads/release", "refs/heads/release"},
	{"push a tag", "refs/tags/v9.9.9:refs/tags/v9.9.9", "refs/tags/v9.9.9"},
	{"push the task's own branch", "HEAD:refs/heads/grain/task-tw", "refs/heads/grain/task-tw"},
}

func TestRealGitPushThroughTheProxyReachesOnlyTheTaskBranch(t *testing.T) {
	rig := newRealPushRig(t, nil)
	for _, c := range realPushCases[:4] {
		before := rig.backend.receivePacks()
		out, err := gitTry(rig.clone, "push", "origin", c.refspec)
		t.Logf("%s: git push origin %s\n%s", c.name, c.refspec, out)
		if err == nil {
			t.Errorf("%s: git push succeeded; want it refused\n%s", c.name, out)
		}
		// receive-pack's report-status says "ng <ref> <reason>", which
		// git prints as "! [remote rejected] ... (<reason>)".
		if !strings.Contains(out, "[remote rejected]") || !strings.Contains(out, "grain/task-tw") {
			t.Errorf("%s: git printed %q; want a [remote rejected] line naming the branch it may push", c.name, out)
		}
		if got := rig.backend.receivePacks(); got != before {
			t.Errorf("%s: the refused push reached the upstream's receive-pack", c.name)
		}
	}
	if got := rig.refs(t); !equalRefs(got, rig.upstream) {
		t.Fatalf("refused pushes moved the upstream's refs: %v, was %v", got, rig.upstream)
	}

	// One push naming the task branch and main together must not carry
	// main along with the permitted update.
	before := rig.backend.receivePacks()
	out, err := gitTry(rig.clone, "push", "origin", "HEAD:refs/heads/grain/task-tw", "HEAD:refs/heads/main")
	t.Logf("mixed push: git push origin HEAD:refs/heads/grain/task-tw HEAD:refs/heads/main\n%s", out)
	if err == nil || rig.backend.receivePacks() != before {
		t.Errorf("a push of the task branch and main together was not refused whole: %v\n%s", err, out)
	}
	if got := rig.refs(t); !equalRefs(got, rig.upstream) {
		t.Fatalf("a mixed push moved the upstream's refs: %v, was %v", got, rig.upstream)
	}

	own := realPushCases[4]
	if out, err := gitTry(rig.clone, "push", "origin", own.refspec); err != nil {
		t.Fatalf("%s: %v\n%s", own.name, err, out)
	}
	head := strings.TrimSpace(gitIn(t, rig.clone, "rev-parse", "HEAD"))
	if got := rig.refs(t)[own.ref]; got != head {
		t.Fatalf("%s: upstream %s is %q, want %s", own.name, own.ref, got, head)
	}
}

func TestRealGitPushWithPushAnyRefReachesEveryRef(t *testing.T) {
	rig := newRealPushRig(t, []model.Grant{{Capability: model.GitPushAnyRefCapability, Via: model.GrantByLabel}})
	head := strings.TrimSpace(gitIn(t, rig.clone, "rev-parse", "HEAD"))
	for _, c := range []struct {
		name, refspec, ref, want string
	}{
		{"push to main", "HEAD:refs/heads/main", "refs/heads/main", head},
		{"push a tag", "refs/tags/v9.9.9:refs/tags/v9.9.9", "refs/tags/v9.9.9", head},
		{"push the task's own branch", "HEAD:refs/heads/grain/task-tw", "refs/heads/grain/task-tw", head},
		// release is not an ancestor of HEAD, so this one is a real force.
		{"force-push another branch", "+HEAD:refs/heads/release", "refs/heads/release", head},
		{"delete a branch", ":refs/heads/release", "refs/heads/release", ""},
	} {
		if out, err := gitTry(rig.clone, "push", "origin", c.refspec); err != nil {
			t.Fatalf("%s with git-push-any-ref: %v\n%s", c.name, err, out)
		}
		if got := rig.refs(t)[c.ref]; got != c.want {
			t.Errorf("%s with git-push-any-ref: upstream %s is %q, want %q", c.name, c.ref, got, c.want)
		}
	}
}

func equalRefs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
