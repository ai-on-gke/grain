package cli

// TestSandboxTokenMintedAfterGitProxyStartsAuthenticates is the inverse
// of the regression this file used to guard, and it exists because the
// old guarantee was turned inside out.
//
// gitproxy's SandboxTokens used to load sandbox-tokens.json once and
// never reread it, so a token had to be minted before the proxy started
// or the proxy could never recognise it. run() minted one per slot up
// front for exactly that reason, and bwsalmon/agents#265 was the bug
// where it did not: every push through the proxy failed closed with 401
// for the life of the process.
//
// There is no set of sandboxes to mint for ahead of time now. A sandbox
// is built for one run, so its token is minted while the proxy is
// already serving -- which is safe only because SandboxTokens rereads
// the file when shown a token it does not know. That reread is the whole
// mechanism this test covers: mint *after* the proxy is up, then perform
// the exact clone/branch/commit/push sequence a dispatched agent would,
// by hand, against the running proxy.
//
// It stays keyless and fast, the same way the ordering test it replaces
// was: daemon_live_test.go's TestRunLiveDispatchesAndOpensAPullRequest is
// what would catch this with a real agent, but only with a Gemini key.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSandboxTokenMintedAfterGitProxyStartsAuthenticates(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const owner, repoName = "acme", "widgets"

	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"))
	runLive(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedDir := filepath.Join(t.TempDir(), "seed")
	runLive(t, t.TempDir(), "git", "clone", bare, seedDir)
	runLive(t, seedDir, "git", "config", "user.email", "seed@example.com")
	runLive(t, seedDir, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seedDir, "git", "add", "README.md")
	runLive(t, seedDir, "git", "commit", "-q", "-m", "initial commit")
	runLive(t, seedDir, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	giveDefaultTeamCredential(t, store, "anonymous")

	task := model.Task{
		ID:     "token-ordering-check",
		Intent: model.IntentImplement,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Binding: model.BindingDirective,
		Target:  &model.RepoRef{Owner: owner, Name: repoName},
	}
	task.Approval = &model.Attribution{Actor: task.Origin.Attribution.Actor}
	if err := store.PutTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	// gitproxy's Authorizer reads the sandbox's live dispatched run, so
	// this needs a real Dispatch on record, not just a queued task -- and
	// the run has to name its sandbox, which is what orchestrator.runOne
	// records via SetRunSandbox once it has acquired one.
	dispatches, err := dispatch.Cycle(context.Background(), store, model.Limits{Workers: 1}, time.Now().UTC())
	if err != nil || len(dispatches) != 1 {
		t.Fatalf("expected exactly one dispatch, got %v (err=%v)", dispatches, err)
	}
	sandbox := dispatches[0].RunID
	if err := store.SetRunSandbox(context.Background(), dispatches[0].RunID, sandbox, task.Target); err != nil {
		t.Fatal(err)
	}

	// The order under test: start the proxy against an empty token file,
	// *then* mint -- the order a sandbox per run forces, and the one the
	// old ordering would have failed closed on.
	proxyURL, _, stop, err := daemon.StartGitProxy(dataDir, store, githubHost, true, daemon.NewDirect(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())

	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))
	token, err := tokens.EnsureToken(sandbox)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := mcp.ConfigureGitCredentials(root, proxyURL+"/placeholder/placeholder.git", token, mcp.GitIdentity{}); err != nil {
		t.Fatal(err)
	}

	branch := model.BranchName(task.ID)
	script := "HOST=$(sed -n 's#.*@\\([^/]*\\).*#\\1#p' ~/.git-credentials) && " +
		"git clone http://$HOST/" + owner + "/" + repoName + ".git work && " +
		"cd work && git checkout -b " + branch + " && " +
		"echo 'token ordering check' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'token ordering check' && " +
		"git push origin " + branch

	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("push through the proxy failed for a token minted after the proxy started -- SandboxTokens must reread on an unknown token: %v\n%s", err, out)
	}
}

// giveDefaultTeamCredential gives the default team the GitHub credential
// name, as the daemon's one-time migration (ui.Client.MigrateTeamGitCredentials)
// would for a team that relied on the ladder: a team's work has no
// deployment credential to fall back to (grain/task-1592), and these
// tests start the proxy without the rest of run().
func giveDefaultTeamCredential(t *testing.T, store *model.Store, name string) {
	t.Helper()
	if err := store.UpdateConfig(context.Background(), func(tm *model.Config) error {
		tm.GitCredential = name
		return nil
	}); err != nil {
		t.Fatalf("giving the default team its credential: %v", err)
	}
}
