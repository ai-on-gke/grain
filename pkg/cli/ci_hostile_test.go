package cli

// A deliberately hostile workflow, run through the real adapter, coming
// back empty-handed.
//
// pkg/ci's package doc and .grain/README.md both rest the whole design on
// one property: a workflow file is a file every agent grain dispatches
// can rewrite, so it must never be able to widen what anything is allowed
// to do -- "a CI job's sandbox is given no capability material, no minted
// key and no GitHub credential beyond whatever read-only checkout it was
// built with". Until ci.go existed that was true by absence: there was no
// code that could pass a credential to a job. It is now a property a test
// has to hold, and the failure mode is silent -- a job that can read a
// capability file, or push with the credential its checkout used, does
// not fail. It passes faster.
//
// So this test is written from the sandbox's side and asserts on what the
// steps *found*. Reading the wiring proves the wiring; a workflow that
// ransacks the machine it was given and comes back with nothing proves
// the sandbox. Every assertion below is on a line one of the steps
// printed, and every step prints a `checked <name>` marker at its end, so
// a script that silently did nothing fails the test rather than passing
// it.
//
// The whole path is the real one. The hostile file is committed to the
// repository under test as `.grain/workflows/hostile.yml` -- which is
// exactly how it would arrive, since that directory is a file an agent
// can rewrite -- read back by the daemon's own ciWorkflowSource, planned
// by ci.Service, scheduled by ci.Engine and run through ciSandboxes into
// a real sandbox over a real git proxy.
//
// # What is asserted, and what is only reported
//
// Asserted, on every backend:
//
//   - none of the paths grain's own capabilities place material at exists
//     in the job's home, nor either of the two places a GitHub credential
//     conventionally lives (.netrc, gh's hosts.yml);
//   - no file in that home carries anything credential-shaped except the
//     git credential store the checkout needs;
//   - that store names this deployment's git proxy and nothing else --
//     no github.com, no second host;
//   - the token in it still fetches the repository under test (the
//     control: without this, every refusal below could be a dead token
//     rather than a narrow one);
//   - and it cannot push that repository back, cannot fetch any other
//     repository through the proxy, and buys nothing at the host behind
//     the proxy, which serves nobody without the deployment's own
//     credential.
//
// Reported but not asserted: what the machine happens to hold beyond what
// grain put there, and what the network happens to reach. A host sandbox
// *is* the machine the daemon runs on -- it inherits its environment and
// its filesystem -- and whether a granule VM reaches the internet is the
// deployment's firewall rather than anything in this repository. Those
// are backend properties; what this test is about is what grain hands a
// job, so the steps print an inventory and a reachability probe for a
// human reading the log and the assertions stay on the credential.
//
// For the same reason the deny-list is checked relative to $HOME rather
// than at the absolute /home/debian the capability packages name: a host
// sandbox pins HOME to itself, so asking about /home/debian there is
// asking about the developer's own machine, which will happily have a
// .git-credentials of its own. On a granule VM the two are the same path.
//
// # Where it runs
//
// Unlike ci_e2e_test.go this one is not gated behind a real deployment:
// it needs nothing but git and a temporary directory, so it runs on the
// host backend in the ordinary `go test ./...` -- this is precisely the
// test that is worthless if it quietly does not run. GRAIN_TEST_CI_E2E,
// when it is set, chooses the backend instead (see that file's doc
// comment for what each needs) and turns the one remaining skip into a
// failure, so in the gated suite -- where a backend is guaranteed -- this
// test either runs or goes red.
//
//	GRAIN_TEST_CI_E2E                 "host" (the default) or "granule".
//	GRAIN_TEST_CI_E2E_ADVERTISE_HOST  the address a sandbox should reach
//	                                  this test's git proxy at. Only the
//	                                  granule arm needs it: a VM has a
//	                                  network namespace of its own, so
//	                                  the 127.0.0.1 the host arm clones
//	                                  from is the VM itself there.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/geminikey"
	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	modelproxycap "github.com/ai-on-gke/grain/pkg/capability/modelproxy"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
)

// hostileCapabilityPaths is every path a grain capability places material
// at inside a sandbox -- the files an agent run's sandbox has and a CI
// job's must not. Named through the packages that write them rather than
// copied as strings, so a capability that moves its file moves this list
// with it.
var hostileCapabilityPaths = append(modelProxySandboxPaths(), []string{
	gcpkey.SandboxKeyPath,
	gcpkey.KeyPath("hostile"),
	geminikey.KeyPath,
	githubsandbox.SandboxTokenPath,
	githubsandbox.SandboxRepoPath,
}...)

// modelProxySandboxPaths is the two files each vendor's proxy capability
// places -- three vendors, six paths, none of which a CI job's sandbox
// may hold.
func modelProxySandboxPaths() []string {
	var paths []string
	for _, vendor := range modelproxy.Vendors() {
		paths = append(paths, modelproxycap.URLPath(vendor), modelproxycap.TokenPath(vendor))
	}
	return paths
}

// hostileOtherRepoContents is what the second repository upstream holds:
// a string that appears nowhere else, so "the job read a repository it
// was not testing" is a grep over what the steps printed rather than an
// inference from an exit code.
const hostileOtherRepoContents = "THE-OTHER-REPOSITORY-A-CI-JOB-MUST-NOT-REACH"

func TestLiveAHostileWorkflowFindsNothingInACIJobsSandbox(t *testing.T) {
	backend, gated := os.Getenv("GRAIN_TEST_CI_E2E"), true
	if backend == "" {
		backend, gated = "host", false
	}
	if _, err := exec.LookPath("git"); err != nil {
		if gated {
			t.Fatalf("GRAIN_TEST_CI_E2E=%s asked for this test on a machine with no git: %v", backend, err)
		}
		t.Skip("git not installed")
	}
	ctx := context.Background()

	// Every capability path this test knows about has to be under the
	// home a sandbox's HOME points at, or the $HOME-relative deny-list
	// below stops asking about it -- silently, which is the one thing
	// this test may not do.
	for _, p := range hostileCapabilityPaths {
		if path.Dir(p) != "/home/debian" {
			t.Fatalf("a capability now places %s outside a sandbox's home directory: "+
				"this test looks for capability material under $HOME and would no longer see it", p)
		}
	}

	// The deployment's own GitHub credential. The stand-in for GitHub
	// below serves nobody without it, so "a step reached the host behind
	// the proxy" and "a step got hold of this" are the same question --
	// and one this test can ask without ever putting the value in the
	// sandbox for a step to find.
	deploymentToken := fmt.Sprintf("ghs_hostiletest%016x", time.Now().UnixNano())

	// A secret in the daemon's own environment, which is what an
	// operator's unit file or login shell would put there. The daemon
	// here is this process, so this is that environment exactly. On the
	// host backend a step is a subprocess of it and used to inherit the
	// whole thing (orchestrator.hostShellPassthrough is what stopped
	// that); on granule the daemon and the step are on opposite sides of
	// a transport and it never could. Asserted on both, because which
	// backend is wired should not decide the answer.
	daemonOnly := fmt.Sprintf("ghs_daemonenvironment%016x", time.Now().UnixNano())
	t.Setenv(hostileDaemonEnvName, daemonOnly)

	// The stand-in GitHub: the repository under test, and one other the
	// job has no business seeing.
	gitRoot := t.TempDir()
	widgets := initBareRepo(t, filepath.Join(gitRoot, "acme", "widgets.git"))
	// So that a refused push is the proxy refusing it rather than the
	// upstream declining to receive one at all.
	runLive(t, gitRoot, "git", "-C", widgets, "config", "http.receivepack", "true")
	other := initBareRepo(t, filepath.Join(gitRoot, "acme", "private.git"))
	seedRepoLive(t, other, map[string]string{"secret.txt": hostileOtherRepoContents + "\n"},
		"the repository this job is not testing")

	upstream := httptest.NewServer(hostileUpstream(deploymentToken, gitHTTPBackendLive(gitRoot)))
	defer upstream.Close()

	seedRepoLive(t, widgets, map[string]string{
		"hello.txt": "hello from the repository under test\n",
		ci.WorkflowDir + "/hostile.yml": strings.ReplaceAll(
			hostileWorkflowYAML, "@UPSTREAM@", upstream.URL),
	}, "declare a hostile workflow")

	// The daemon's own halves: its secrets directory with the credential
	// above in it, a store with nothing in it (a CI job has no task
	// behind it, so the only thing that can authorize its clone is the
	// read scope the adapter grants), and the proxy.
	dataDir := t.TempDir()
	secrets := filepath.Join(dataDir, "secrets", "github")
	if err := os.MkdirAll(secrets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "deployment.token"), []byte(deploymentToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "credentials.json"), []byte(`{"*": "deployment"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	// What the daemon does at startup: its forward host is the default
	// git host, and the ladder becomes the deployment's git connections
	// (docs/single-tenant-reset.md, stage 1).
	store.GitDefaultHost = upstreamHost
	if err := store.UpdateConfig(context.Background(), func(t *model.Config) error {
		t.GitConnections = append(t.GitConnections, model.TeamGitConnection{Credential: "deployment"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proxyURL, _, stopProxy, err := daemon.StartGitProxy(dataDir, store, upstreamHost, true,
		daemon.NewDirect(os.Getenv("GRAIN_TEST_CI_E2E_ADVERTISE_HOST")), nil)
	if err != nil {
		t.Fatalf("startGitProxy: %v", err)
	}
	defer stopProxy(context.Background())

	credentials, err := gitproxy.LoadCredentialSet(secrets)
	if err != nil {
		t.Fatalf("loading this deployment's credentials: %v", err)
	}
	sandboxes, _ := e2eSandboxBackend(t, backend)
	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))
	service := &ci.Service{
		// The daemon's own source: the hostile file is read out of the
		// repository, by the code that reads a repository's workflows.
		Source: daemon.CiWorkflowSource{
			Credentials:         credentials,
			GitHubHost:          upstreamHost,
			InsecureHTTP:        true,
			TempDir:             t.TempDir(),
			TeamGitCredential:   store.TeamGitCredential,
			TeamGitCredentialOn: store.TeamGitCredentialOn,
		},
		Engine: &ci.Engine{
			Sandboxes: daemon.CiSandboxes{
				Sandboxes:     sandboxes,
				GitRemoteBase: proxyURL,
				MintToken:     tokens.EnsureToken,
				RevokeToken:   tokens.Revoke,
				// The very registry startGitProxy just handed the proxy.
				Scopes: daemon.CiReadScopes,
				Log:    t.Logf,
			},
			Concurrency: 1,
			Log:         t.Logf,
		},
		Log: t.Logf,
	}

	run, err := service.Start(ctx, ci.Request{
		Repo: "acme/widgets", Ref: "main", Event: ci.EventPush, RequestedBy: "ci_hostile_test",
	})
	if err != nil {
		t.Fatalf("starting a CI run over the hostile workflow: %v", err)
	}
	finished := awaitCIRun(t, service, run.ID)
	if len(finished.Jobs) != 1 {
		t.Fatalf("the hostile workflow planned %d jobs, want the one it declares: %+v", len(finished.Jobs), finished)
	}
	job := finished.Jobs[0]

	// Everything the steps printed, in order, kept as one string: every
	// assertion below is a question about what a step found, and this is
	// what they found.
	var report strings.Builder
	for _, step := range job.Steps {
		fmt.Fprintf(&report, "--- step %q (%s, exit %d):\n%s\n", step.Name, step.Conclusion, step.ExitCode, step.Output)
	}
	found := report.String()
	t.Logf("the hostile workflow, in sandbox %q:\n%s", job.Sandbox, found)

	// The job has to have run to the end, because a step that never ran
	// found nothing for the most boring reason there is. Every step here
	// exits zero by construction -- each one captures what it tried
	// rather than failing on it -- so a failure is this test's own
	// scaffolding breaking, and is worth reporting as that.
	if job.Error != "" {
		t.Fatalf("the hostile job never ran (%s); nothing below was proved: %s", job.Conclusion, job.Error)
	}
	if job.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("the hostile job concluded %s, so some step did not run to the end; nothing below was proved", job.Conclusion)
	}
	for _, step := range hostileStepMarkers {
		if !strings.Contains(found, "hostile: checked "+step) {
			t.Fatalf("the %q step never printed that it finished, so what it did not find proves nothing", step)
		}
	}

	// The control, before any of the refusals mean anything: the
	// credential the sandbox does hold is live, and fetches the
	// repository the job is testing.
	if got := hostileValue(t, found, "fetch-exit"); got != "0" {
		t.Fatalf("the job could not fetch the repository it was testing (exit %s), so every refusal below "+
			"could be a dead token rather than a narrow one", got)
	}

	// What the ransacking found: nothing.
	for _, line := range hostileLines(found, "FOUND ") {
		t.Errorf("a hostile workflow found something a CI job's sandbox should not have: %s", line)
	}
	for _, line := range hostileLines(found, "credential-file ") {
		if line != ".git-credentials" && line != ".git-credentials.netrc" {
			t.Errorf("a hostile workflow found credential-shaped content in %s, which is not the git "+
				"credential store the checkout needs", line)
		}
	}

	// And what a step's environment holds: what grain put there, and
	// nothing the daemon happens to be holding.
	if strings.Contains(found, hostileDaemonEnvName) || strings.Contains(found, daemonOnly) {
		t.Error("a variable from the daemon's own environment reached a CI job's sandbox")
	}
	if backend == "host" {
		for _, name := range hostileLines(found, "env-name ") {
			if !hostileStepEnvNames[name] {
				t.Errorf("a step saw the environment variable %s, which grain did not mean to give it -- "+
					"a name arriving here is either the daemon's environment reaching a step again, or a "+
					"pass-through somebody added to orchestrator.hostShellPassthrough without saying so here",
					name)
			}
		}
	}

	// The one credential it has, and the one host it is for.
	wantCredential := "http://REDACTED@" + strings.TrimPrefix(proxyURL, "http://")
	if got := hostileCredentialHosts(found); len(got) != 1 || got[0] != wantCredential {
		t.Errorf("the job's git is configured to authenticate to %q, want exactly %q -- this deployment's "+
			"git proxy and nothing else", got, wantCredential)
	}

	// And what that credential is worth: a fetch of one repository, and
	// nothing else at all.
	if got := hostileValue(t, found, "push-exit"); got == "0" {
		t.Error("a workflow pushed the repository it was testing back through the proxy")
	}
	tip := strings.TrimSpace(string(mustOutput(t, gitRoot, "git", "--git-dir", widgets, "log", "-1", "--format=%s", "main")))
	if tip != "declare a hostile workflow" {
		t.Errorf("the repository under test now says %q at its tip -- a workflow's push landed", tip)
	}
	if got := hostileValue(t, found, "other-repo-exit"); got == "0" {
		t.Error("a workflow fetched a repository the job was not testing, through the proxy")
	}
	if got := hostileValue(t, found, "beyond-exit"); got == "0" {
		t.Error("a workflow reached past the proxy to the host behind it and was served")
	}
	if strings.Contains(found, hostileOtherRepoContents) {
		t.Error("a workflow read the contents of a repository the job was not testing")
	}
	if strings.Contains(found, deploymentToken) {
		t.Error("this deployment's own GitHub credential reached a CI job's sandbox")
	}

	// Nothing outlives the run: the scope the job's sandbox held is gone,
	// so its token -- wherever a copy of it ended up -- now authorizes
	// nothing.
	if _, ok := daemon.CiReadScopes.Repo(job.Sandbox); ok {
		t.Errorf("the finished job's sandbox %q still holds a read scope", job.Sandbox)
	}
}

// hostileStepMarkers are the names each step of the workflow below signs
// off with. Listed here as well as in the file so that a step that was
// renamed, reordered or lost fails this test rather than quietly
// reducing what it covers.
var hostileStepMarkers = []string{
	"capability-material",
	"credential-shaped-files",
	"git-credentials",
	"the-credential-still-works",
	"push-the-repository-back",
	"another-repository",
	"beyond-the-proxy",
}

// hostileDaemonEnvName is the variable this test puts a secret in, in the
// daemon's own environment, so that "a step can read what the daemon was
// started with" is something a step is asked rather than something the
// wiring is trusted about.
const hostileDaemonEnvName = "GRAIN_HOSTILE_DAEMON_ONLY_TOKEN"

// hostileStepEnvNames is every environment variable a step of this
// workflow may see on the host backend, and the whole of it: what grain
// composes, what the workflow itself declares, and what bash sets for
// itself.
//
// Written out here rather than read from orchestrator, on purpose. A
// variable added to orchestrator.hostShellPassthrough widens what every
// CI job on this backend can read out of the daemon, which is exactly
// the kind of change that should not be able to happen quietly: it
// turns this test red until somebody says here that they meant it.
//
// Only the host arm is asserted this exactly. A granule VM's shell starts
// with the guest's own login environment, which is the guest image's
// business and not grain's -- and the daemon's environment is on another
// machine there, which is what the assertion above checks on both.
var hostileStepEnvNames = map[string]bool{
	// orchestrator.hostShellEnv: HOME pinned to the sandbox,
	// GIT_CONFIG_NOSYSTEM=1 so the host's system gitconfig (and any
	// credential helper it names) never reaches the sandbox's git,
	// GIT_DEFAULT_REF_FORMAT=files so a repository it creates is one the
	// daemon's git can read (pkg/gitrefs), and
	// orchestrator.hostShellPassthrough.
	"HOME": true, "GIT_CONFIG_NOSYSTEM": true, "GIT_DEFAULT_REF_FORMAT": true, "PATH": true, "LANG": true, "LC_ALL": true,
	"GOMODCACHE": true, "GOCACHE": true, "npm_config_cache": true,
	// ci.runEnv: what grain tells a step about the run it is part of.
	"CI": true, "GRAIN_CI": true, "GRAIN_CI_REPO": true, "GRAIN_CI_REF": true,
	"GRAIN_CI_COMMIT": true, "GRAIN_CI_EVENT": true, "GRAIN_CI_WORKFLOW": true,
	"GRAIN_CI_JOB": true,
	// The workflow file's own `env:` block, below.
	"HOSTILE_UPSTREAM": true, "GIT_TERMINAL_PROMPT": true,
	// What bash sets for itself, which nobody handed it.
	"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true,
}

// hostileUpstream is the stand-in for GitHub: the same smart-HTTP backend
// the other live tests here serve, behind this deployment's own
// credential.
//
// It is what makes "a step reached past the proxy" an assertion rather
// than a reachability test. A host sandbox is the machine this test runs
// on and can open a socket to anything it likes; what it must not be able
// to do is present a credential at the other end. So the upstream refuses
// everyone who does not hold the deployment's token -- which the proxy
// adds on the job's behalf and the sandbox never sees.
//
// Both spellings of that token are accepted because both are used
// against it: the proxy forwards it as the Basic auth *username*
// (gitproxy's forward.go, GitHub's own PAT convention) and
// ciWorkflowSource's askpass hands it over as the password half.
func hostileUpstream(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || (user != token && password != token) {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "this host serves nobody without the deployment's own credential", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// seedRepoLive puts one commit into a bare repository over the
// filesystem, so a test's fixtures are set up without going anywhere near
// the proxy or the server under test.
func seedRepoLive(t *testing.T, bare string, files map[string]string, message string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "seed")
	runLive(t, filepath.Dir(bare), "git", "clone", "-q", bare, work)
	runLive(t, work, "git", "config", "user.email", "seed@example.com")
	runLive(t, work, "git", "config", "user.name", "seed")
	for name, content := range files {
		full := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runLive(t, work, "git", "add", ".")
	runLive(t, work, "git", "commit", "-q", "-m", message)
	runLive(t, work, "git", "push", "-q", "origin", "main")
}

// hostileLines is every line a step printed under one of the workflow's
// own markers, with the marker stripped -- "what did the steps find",
// which is the only thing this test asserts on.
func hostileLines(output, marker string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "hostile: "+marker); ok {
			out = append(out, strings.TrimSpace(rest))
		}
	}
	return out
}

// hostileCredentialHosts is every host the job's git holds a credential
// for, as the workflow's own step reported them with the credentials
// themselves cut out.
//
// The colon before a port is decoded on the way past: grain writes that
// file itself (mcp.ConfigureGitCredentials) in the plain form, and
// git-credential-store rewrites it in its own the first time git uses it
// -- which the checkout already has by the time any step runs. Both
// spellings name the same host, and which one the test happens to see is
// not the question it is asking.
func hostileCredentialHosts(output string) []string {
	hosts := hostileLines(output, "git-credential-for ")
	for i, host := range hosts {
		hosts[i] = strings.ReplaceAll(strings.ReplaceAll(host, "%3a", ":"), "%3A", ":")
	}
	return hosts
}

// hostileValue reads one `hostile: <name>=<value>` line. Exactly one is
// expected: two would mean a step ran twice and none that it never got
// there, and either way what follows is not an answer to the question
// this test asked.
func hostileValue(t *testing.T, output, name string) string {
	t.Helper()
	values := hostileLines(output, name+"=")
	if len(values) != 1 {
		t.Fatalf("the steps printed %d %q lines, want exactly one", len(values), name)
	}
	return values[0]
}

// hostileWorkflowYAML is the workflow itself, as it is committed to the
// repository under test.
//
// It is written the way an actually hostile one would be -- it hunts for
// credentials, tries to push the repository back, tries a repository it
// was not given and tries the host behind the proxy -- with one
// concession to being a test: every step captures what it tried instead
// of failing on it, and prints what it found under a `hostile:` marker.
// That keeps the job green when the properties hold, so a red job means
// the job could not run at all, and it means every step runs rather than
// the first refusal skipping the rest.
const hostileWorkflowYAML = `# A hostile workflow, for cmd/grain's ci_hostile_test.go.
#
# This file is the thing pkg/ci's threat model is about: every agent grain
# dispatches into a repository can write one of these, so it must be able
# to reach nothing the agent could not already reach. It goes looking.
name: hostile

on: [push]

env:
  # The host behind the proxy -- github.com's stand-in. A real hostile
  # workflow would simply write github.com here; the test needs somewhere
  # it can watch.
  HOSTILE_UPSTREAM: "@UPSTREAM@"
  # So a git that cannot authenticate fails with an error instead of
  # hanging on a credential prompt nobody is there to answer.
  GIT_TERMINAL_PROMPT: "0"

jobs:
  ransack:
    name: ransack
    # Nothing here should take seconds, let alone minutes: a step that
    # hangs should fail the job while somebody is still watching.
    timeout-minutes: 5
    steps:
      - name: What this sandbox was given
        run: |
          echo "hostile: HOME=$HOME"
          echo "--- what is in this home, for a reader rather than for an assertion:"
          ls -la "$HOME" || true
          echo "--- environment variable names (the values are deliberately not printed):"
          env | cut -d= -f1 | sort | while read -r n; do echo "hostile: env-name $n"; done
          echo "--- the paths grain places capability material and credentials at:"
          for p in .claude-proxy-url .claude-proxy-token .openai-proxy-url \
                   .openai-proxy-token .gemini-proxy-url .gemini-proxy-token \
                   .gcp-service-account.json \
                   .gemini-api-key .github-sandbox-token .github-sandbox-repo \
                   .package-publish-registry .package-publish-username .package-publish-token \
                   .registry-push-registry .registry-push-username .registry-push-token \
                   .grain .netrc .config/gh/hosts.yml; do
            if [ -e "$HOME/$p" ]; then
              echo "hostile: FOUND $p"
            fi
          done
          # A named gcp-key:<name> lands at its own file (gcpkey.KeyPath).
          for f in "$HOME"/.gcp-service-account-*.json; do
            if [ -e "$f" ]; then
              echo "hostile: FOUND ${f#$HOME/}"
            fi
          done
          echo "hostile: checked capability-material"

      - name: Anything credential-shaped
        run: |
          # The checkout is skipped: the repository's own contents are
          # not a secret, and this step is standing in it.
          checkout=$(pwd)
          patterns='gh[pousr]_[A-Za-z0-9]{16,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|AIza[0-9A-Za-z_-]{30,}|sk-[A-Za-z0-9]{20,}'
          # Bounded rather than exhaustive: a credential lives in a small
          # dotfile near the top of a home directory, and a guest image's
          # module and package caches are gigabytes nobody should grep.
          find "$HOME" -maxdepth 2 -type f -size -64k ! -path "$checkout/*" 2>/dev/null | while read -r f; do
            if grep -qIE "$patterns" "$f" 2>/dev/null; then
              echo "hostile: credential-file ${f#$HOME/}"
            fi
          done
          echo "hostile: checked credential-shaped-files"

      - name: What git will authenticate to
        run: |
          # The hosts, never the credentials: a value that leaked into a
          # sandbox should not then leak into a CI log.
          if [ -e "$HOME/.git-credentials" ]; then
            sed -E 's#^([a-z]+)://[^@]*@#\1://REDACTED@#' "$HOME/.git-credentials" | while read -r l; do
              echo "hostile: git-credential-for $l"
            done
          fi
          echo "hostile: checked git-credentials"

      - name: The credential still works for what it is for
        run: |
          if git fetch --quiet origin; then
            echo "hostile: fetch-exit=0"
          else
            echo "hostile: fetch-exit=$?"
          fi
          echo "hostile: checked the-credential-still-works"

      - name: Push the repository back
        run: |
          git config user.email hostile@example.com
          git config user.name hostile
          echo "a workflow rewrote this" >> hello.txt
          git add hello.txt
          git commit -q -m "a workflow rewrote the repository"
          if out=$(git push origin HEAD:main 2>&1); then
            echo "hostile: push-exit=0"
          else
            echo "hostile: push-exit=$?"
          fi
          echo "$out" | head -20
          echo "hostile: checked push-the-repository-back"

      - name: Another repository, through the proxy
        run: |
          if command -v timeout >/dev/null 2>&1; then bound="timeout 60"; else bound=""; fi
          remote=$(git remote get-url origin)
          base=${remote%/acme/widgets.git}
          if out=$($bound git clone --quiet "$base/acme/private.git" "$HOME/other" 2>&1); then
            echo "hostile: other-repo-exit=0"
          else
            echo "hostile: other-repo-exit=$?"
          fi
          if [ -e "$HOME/other/secret.txt" ]; then
            echo "hostile: FOUND another repository's contents:"
            cat "$HOME/other/secret.txt"
          fi
          echo "$out" | head -20
          echo "hostile: checked another-repository"

      - name: Past the proxy, straight at the host behind it
        run: |
          if command -v timeout >/dev/null 2>&1; then bound="timeout 60"; else bound=""; fi
          # Reachability is the deployment's business (a sandbox that
          # reaches no network at all is a firewall, not this repository)
          # -- so this is reported and not asserted. What is asserted is
          # the clone below: reaching the host buys nothing, because the
          # job holds no credential for it.
          if command -v curl >/dev/null 2>&1; then
            echo "hostile: upstream-http=$($bound curl -s -o /dev/null -w '%{http_code}' "$HOSTILE_UPSTREAM/" || echo unreachable)"
          fi
          if out=$($bound git clone --quiet "$HOSTILE_UPSTREAM/acme/widgets.git" "$HOME/beyond" 2>&1); then
            echo "hostile: beyond-exit=0"
          else
            echo "hostile: beyond-exit=$?"
          fi
          if [ -e "$HOME/beyond/hello.txt" ]; then
            echo "hostile: FOUND the repository under test, straight from the host behind the proxy"
          fi
          echo "$out" | head -20
          echo "hostile: checked beyond-the-proxy"
`
