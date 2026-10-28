package gitproxy

// The push-side hardening of 2026-10-11 (docs/test-plans/2026-10-11.md
// §11) with a real git client and a real HTTP server in front of the
// proxy, rather than through Handle with a byte slice: a push larger than
// the spool threshold lands, through both ways a push leaves the proxy,
// with the spool file it made gone afterwards; and a crafted pack whose
// commit inflates past maxPackObjectBytes, or whose delta claims a
// destination past it, is refused by the fast-forward proof without the
// proxy falling over.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// hardeningFixture is a sandbox token, a repository acme/widgets whose
// run may push grain/task-2112 and (as a fast-forward only) main, and a
// proxy serving it over HTTP.
type hardeningFixture struct {
	state  string // the repository the push lands in
	client string // the sandbox's clone
	url    string // the proxy's URL for acme/widgets, token included
	proxy  *GitProxy
}

func newHardeningFixture(t *testing.T, forwarded bool) hardeningFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	state := filepath.Join(root, "acme", "widgets.git")
	if forwarded {
		runLocalGit(t, "", "init", "-q", "--bare", "-b", "main", state)
		runLocalGit(t, state, "config", "http.receivepack", "true")
		seed := filepath.Join(root, "seed")
		runLocalGit(t, "", "init", "-q", "-b", "main", seed)
		runLocalGit(t, seed, "-c", "user.name=grain", "-c", "user.email=grain@example.invalid",
			"commit", "-q", "--allow-empty", "-m", "Initial")
		runLocalGit(t, seed, "push", "-q", state, "main")
	} else {
		runLocalGit(t, "", "init", "-q", "-b", "main", state)
		runLocalGit(t, state, "-c", "user.name=grain", "-c", "user.email=grain@example.invalid",
			"commit", "-q", "--allow-empty", "-m", "Initial")
		runLocalGit(t, state, "checkout", "-q", "--detach", "main")
	}

	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"}))
	if err != nil {
		t.Fatal(err)
	}
	proxy := &GitProxy{
		Authorizer: &ModelAuthorizer{
			Store: stubScopeLookup{target: &model.RepoRef{Owner: "acme", Name: "widgets"}},
			Branches: stubDeclaredPush{
				stubBranchLookup: stubBranchLookup{branch: "grain/task-2112", live: true},
				declared:         releaseDeclaration(model.RefPush{Pattern: "refs/heads/main"}),
			},
		},
		Tokens: tokens,
		Audit:  &RecordingAuditLog{},
	}
	if forwarded {
		// The way a push to GitHub leaves: a RealForwarder to a smart-HTTP
		// server, here git http-backend over CGI, which streams the body
		// to receive-pack rather than holding it.
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		upstream := httptest.NewServer(&cgi.Handler{
			Path: git, Args: []string{"http-backend"},
			Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
		})
		t.Cleanup(upstream.Close)
		credentials, err := LoadCredentialSet(writeCredentialSet(t, map[string]string{"*": "bot"}, map[string]string{"bot": "bot-token"}))
		if err != nil {
			t.Fatal(err)
		}
		proxy.Credentials = credentials
		proxy.TeamCredentials = ladderConnections{proxy: proxy}
		proxy.Forwarder = &RealForwarder{Host: strings.TrimPrefix(upstream.URL, "http://")}
	} else {
		proxy.Forwarder = &FakeForwarder{}
		proxy.Local = &LocalRepository{Owner: "acme", Name: "widgets", Dir: state}
	}
	srv := httptest.NewServer(NewHandler(proxy))
	t.Cleanup(srv.Close)
	url := strings.Replace(srv.URL, "http://", "http://x:tok0@", 1) + "/acme/widgets.git"

	client := filepath.Join(root, "client")
	runLocalGit(t, "", "clone", "-q", url, client)
	runLocalGit(t, client, "config", "user.name", "sandbox")
	runLocalGit(t, client, "config", "user.email", "sandbox@example.invalid")
	return hardeningFixture{state: state, client: client, url: url, proxy: proxy}
}

func (f hardeningFixture) tip(t *testing.T, ref string) string {
	t.Helper()
	out, _ := localGitOutput(t, f.state, "", "rev-parse", "--verify", "-q", ref)
	return strings.TrimSpace(out)
}

// spoolFiles is the push spool files in dir.
func spoolFiles(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "grain-gitproxy-push-") {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestLivePushOverTheSpoolThresholdLandsAndLeavesNoSpoolFile(t *testing.T) {
	for name, forwarded := range map[string]bool{"local repository": false, "forwarded upstream": true} {
		t.Run(name, func(t *testing.T) {
			spoolDir := t.TempDir()
			t.Setenv("TMPDIR", spoolDir)
			f := newHardeningFixture(t, forwarded)

			// 40 MiB that does not compress, so the pack is as big as the
			// file: well over the 16 MiB at which a push spools.
			blob := make([]byte, 40<<20)
			rand.New(rand.NewSource(3147)).Read(blob)
			if err := os.WriteFile(filepath.Join(f.client, "large.bin"), blob, 0o644); err != nil {
				t.Fatal(err)
			}
			runLocalGit(t, f.client, "add", "large.bin")
			runLocalGit(t, f.client, "commit", "-q", "-m", "A large commit")
			head, _ := localGitOutput(t, f.client, "", "rev-parse", "HEAD")

			// Watch the spool directory, and the heap, while the push runs.
			var sawSpool atomic.Bool
			var peakHeap atomic.Uint64
			done := make(chan struct{})
			var watched sync.WaitGroup
			watched.Add(1)
			go func() {
				defer watched.Done()
				var stats runtime.MemStats
				for {
					if len(spoolFiles(spoolDir)) > 0 {
						sawSpool.Store(true)
					}
					runtime.ReadMemStats(&stats)
					if stats.HeapInuse > peakHeap.Load() {
						peakHeap.Store(stats.HeapInuse)
					}
					select {
					case <-done:
						return
					case <-time.After(5 * time.Millisecond):
					}
				}
			}()
			out, err := localGitOutput(t, f.client, "", "push", "origin", "HEAD:refs/heads/grain/task-2112")
			close(done)
			watched.Wait()
			if err != nil {
				t.Fatalf("a 40 MiB push of the run's own branch failed: %v\n%s", err, out)
			}
			if got := f.tip(t, "refs/heads/grain/task-2112"); got != head {
				t.Errorf("grain/task-2112 = %q, want the pushed commit %q", got, head)
			}
			if !sawSpool.Load() {
				t.Errorf("no grain-gitproxy-push-* file appeared in $TMPDIR during a 40 MiB push")
			}
			if left := spoolFiles(spoolDir); len(left) != 0 {
				t.Errorf("spool files left in $TMPDIR after the push: %v", left)
			}
			t.Logf("peak heap in use during the push: %d MiB", peakHeap.Load()>>20)
		})
	}
}

// receivePackBody is a receive-pack request moving ref from old to new,
// carrying pack.
func receivePackBody(old, new, ref string, pack []byte) []byte {
	line := fmt.Sprintf("%s %s %s\x00report-status side-band-64k\n", old, new, ref)
	var body bytes.Buffer
	fmt.Fprintf(&body, "%04x%s0000", len(line)+4, line)
	body.Write(pack)
	return body.Bytes()
}

// packOf frames entries (each an object header and its zlib stream) as a
// version 2 packfile.
func packOf(entries ...[]byte) []byte {
	var pack bytes.Buffer
	pack.WriteString("PACK")
	_ = binary.Write(&pack, binary.BigEndian, uint32(2))
	_ = binary.Write(&pack, binary.BigEndian, uint32(len(entries)))
	for _, e := range entries {
		pack.Write(e)
	}
	pack.Write(make([]byte, 20))
	return pack.Bytes()
}

// packEntry is one object: its type and size header, any prefix (a
// delta's base offset), and data deflated -- or, with zeros > 0, that
// many zero bytes deflated after it.
func packEntry(typ int, prefix, data []byte, zeros int) []byte {
	var e bytes.Buffer
	e.WriteByte(byte(typ << 4)) // the size is advisory; the walk reads the stream
	e.Write(prefix)
	zw := zlib.NewWriter(&e)
	_, _ = zw.Write(data)
	chunk := make([]byte, 1<<20)
	for zeros > 0 {
		n := min(zeros, len(chunk))
		_, _ = zw.Write(chunk[:n])
		zeros -= n
	}
	_ = zw.Close()
	return e.Bytes()
}

func TestLiveCraftedPackBombsAreRefusedAndTheProxyStaysUp(t *testing.T) {
	f := newHardeningFixture(t, false)
	main := f.tip(t, "refs/heads/main")
	commit := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nparent " + main +
		"\nauthor a <a@b> 0 +0000\ncommitter a <a@b> 0 +0000\n\nbomb\n")
	base := packEntry(objCommit, nil, commit, 0)

	delta := encodeDeltaHeader(len(commit), 1<<40)
	for name, tc := range map[string]struct {
		pack  []byte
		bound func() error // the bound that has to be what refuses it
	}{
		// A commit whose stream inflates to 33 MiB of zeros.
		"zlib bomb": {packOf(packEntry(objCommit, nil, commit, maxPackObjectBytes+(1<<20))), func() error {
			_, err := readPackCommits(packOf(packEntry(objCommit, nil, commit, maxPackObjectBytes+(1<<20))), 20)
			return err
		}},
		// A delta against a commit in the pack, claiming a 1 TiB result.
		"delta bomb": {packOf(base, packEntry(objOfsDelta, []byte{byte(len(base))}, delta, 0)), func() error {
			_, err := applyDelta(commit, delta)
			return err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.bound(); err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("the size bound did not trip on the crafted pack: %v", err)
			}
			pack := tc.pack
			body := receivePackBody(main, strings.Repeat("b", 40), "refs/heads/main", pack)
			req, err := http.NewRequest(http.MethodPost, f.url+"/git-receive-pack", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
			req.Header.Set("Accept", "application/x-git-receive-pack-result")
			req.Header.Set("User-Agent", "git/2.39.2")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("posting the crafted pack: %v", err)
			}
			reply, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !bytes.Contains(reply, []byte("could not show")) {
				t.Errorf("the crafted pack was not refused by the fast-forward proof: %d\n%q", resp.StatusCode, reply)
			}
			if got := f.tip(t, "refs/heads/main"); got != main {
				t.Errorf("main moved to %s", got)
			}

			// And the proxy answers the next client as ever.
			req, err = http.NewRequest(http.MethodGet, f.url+"/info/refs?service=git-upload-pack", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("User-Agent", "git/2.39.2")
			resp, err = http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("the proxy after a crafted pack: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("the proxy after a crafted pack: status %d", resp.StatusCode)
			}
		})
	}

	// The same push with an honest pack is the fast-forward it claims.
	runLocalGit(t, f.client, "commit", "-q", "--allow-empty", "-m", "Honest")
	if out, err := localGitOutput(t, f.client, "", "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("an honest fast-forward of main after the bombs: %v\n%s", err, out)
	}
}

// The bound on a pack's objects is a bound on what the fast-forward proof
// keeps, not on what a push may carry: a 40 MiB file pushed to a declared
// ref as a fast-forward lands. Before the walk inflated blobs into
// io.Discard, this was refused as a fast-forward it "could not show".
func TestLiveFastForwardCarryingAFileOverThePackObjectBoundLands(t *testing.T) {
	f := newHardeningFixture(t, false)
	blob := make([]byte, 40<<20)
	rand.New(rand.NewSource(2955)).Read(blob)
	if err := os.WriteFile(filepath.Join(f.client, "large.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	runLocalGit(t, f.client, "add", "large.bin")
	runLocalGit(t, f.client, "commit", "-q", "-m", "A large commit")
	head, _ := localGitOutput(t, f.client, "", "rev-parse", "HEAD")
	if out, err := localGitOutput(t, f.client, "", "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("a fast-forward of declared main carrying a 40 MiB file was refused: %v\n%s", err, out)
	}
	if got := f.tip(t, "refs/heads/main"); got != head {
		t.Errorf("main = %q, want the pushed commit %q", got, head)
	}
}

// A real git push of a delta chain against one large commit -- each delta
// a couple of kilobytes on the wire, each reconstruction a whole commit --
// is refused once the commits the fast-forward proof holds pass
// maxPackCommitBytes, with the proxy's heap bounded by that sum rather than
// by the chain's length; and an ordinary fast-forward right after it lands
// (docs/test-plans/2026-10-12.md §3).
func TestLiveDeltaChainPastThePackCommitTotalIsRefused(t *testing.T) {
	f := newHardeningFixture(t, false)
	main := f.tip(t, "refs/heads/main")

	// A ~30 MiB commit message, then commits that differ from it only in
	// their last line: git packs every one after the first as a small
	// delta against it. Enough of them to reconstruct past the total.
	message := filepath.Join(t.TempDir(), "message")
	base := strings.Repeat("grain walkthrough: a delta base far larger than any real commit\n", 30<<20/64)
	n := int(maxPackCommitBytes/int64(len(base))) + 3
	for i := range n {
		if err := os.WriteFile(message, fmt.Appendf([]byte(base), "variant %d\n", i), 0o644); err != nil {
			t.Fatal(err)
		}
		runLocalGit(t, f.client, "commit", "-q", "--allow-empty", "-F", message)
	}

	var peakHeap atomic.Uint64
	done := make(chan struct{})
	var watched sync.WaitGroup
	watched.Add(1)
	go func() {
		defer watched.Done()
		var stats runtime.MemStats
		for {
			runtime.ReadMemStats(&stats)
			if stats.HeapInuse > peakHeap.Load() {
				peakHeap.Store(stats.HeapInuse)
			}
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	out, err := localGitOutput(t, f.client, "", "push", "origin", "HEAD:refs/heads/main")
	close(done)
	watched.Wait()
	if err == nil {
		t.Fatalf("a delta chain reconstructing past %d bytes of commits was accepted:\n%s", maxPackCommitBytes, out)
	}
	if !strings.Contains(out, "could not show") {
		t.Errorf("the push was not refused by the fast-forward proof:\n%s", out)
	}
	if got := f.tip(t, "refs/heads/main"); got != main {
		t.Errorf("main moved to %s", got)
	}
	// What the walk may hold is the total plus the one commit that passed
	// it; the rest is the push itself and the proxy's own state.
	if limit := uint64(maxPackCommitBytes) + 2*uint64(len(base)) + 96<<20; peakHeap.Load() > limit {
		t.Errorf("peak heap in use during the push: %d MiB, over %d MiB", peakHeap.Load()>>20, limit>>20)
	}
	t.Logf("%d commits of %d MiB pushed as deltas; peak heap in use %d MiB", n, len(base)>>20, peakHeap.Load()>>20)

	runLocalGit(t, f.client, "reset", "-q", "--hard", main)
	runLocalGit(t, f.client, "commit", "-q", "--allow-empty", "-m", "Ordinary")
	if out, err := localGitOutput(t, f.client, "", "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("an ordinary fast-forward of main after the delta chain: %v\n%s", err, out)
	}
}

// A declared ref fast-forwarded to a commit the remote already has -- the
// release cut's back-merge, pushed to its task branch and verified there
// first -- carries an empty pack, and lands on the strength of the
// remote's own commits; one the remote has that is not a fast-forward is
// still refused. Both ways a push leaves: to the local repository, and
// forwarded with a credential as to GitHub (grain/task-3546).
func TestLiveFastForwardToACommitTheRemoteHasLands(t *testing.T) {
	for name, forwarded := range map[string]bool{"local": false, "forwarded": true} {
		t.Run(name, func(t *testing.T) {
			f := newHardeningFixture(t, forwarded)
			push := func(args ...string) (string, error) {
				return localGitOutput(t, f.client, "", append([]string{"push", "origin"}, args...)...)
			}
			runLocalGit(t, f.client, "commit", "-q", "--allow-empty", "-m", "Back-merge")
			head, _ := localGitOutput(t, f.client, "", "rev-parse", "HEAD")
			if out, err := push("HEAD:refs/heads/grain/task-2112"); err != nil {
				t.Fatalf("pushing the run's own branch: %v\n%s", err, out)
			}
			if out, err := push("HEAD:refs/heads/main"); err != nil {
				t.Fatalf("a fast-forward of declared main to a commit the remote has was refused: %v\n%s", err, out)
			}
			if got := f.tip(t, "refs/heads/main"); got != strings.TrimSpace(head) {
				t.Errorf("main = %q, want the back-merge %q", got, head)
			}

			main := f.tip(t, "refs/heads/main")
			runLocalGit(t, f.client, "reset", "-q", "--hard", "HEAD~1")
			runLocalGit(t, f.client, "commit", "-q", "--allow-empty", "-m", "Not a fast-forward")
			if out, err := push("--force", "HEAD:refs/heads/grain/task-2112"); err != nil {
				t.Fatalf("pushing the run's own branch: %v\n%s", err, out)
			}
			if out, err := push("--force", "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "could not show") {
				t.Errorf("a move of main the remote has but does not descend from main: err %v\n%s", err, out)
			}
			if got := f.tip(t, "refs/heads/main"); got != main {
				t.Errorf("main moved to %s", got)
			}
		})
	}
}
