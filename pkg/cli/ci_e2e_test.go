package cli

// One real grain CI run of this repository's own workflow file, against
// a real sandbox backend.
//
// Everything else that tests pkg/ci runs against a fake backend whose
// Exec returns whatever the test wrote, which is the right way to test a
// scheduler and no way at all to find out whether a job can be given a
// machine, a checkout and a shell. ci_test.go's own live test is the
// next step up -- a real sandbox, a real proxy, real git -- but it drives
// the adapter directly, with a repository it wrote itself and steps it
// made up. This is the whole of it: ci.Service planning from this
// repository's own `.grain/workflows/tests.yml` at the commit under
// test, ci.Engine scheduling its jobs, cmd/grain's adapter giving each
// one a machine and a checkout, and a verdict at the end that can be
// held up against what `.github/workflows/tests.yml` said about the same
// commit.
//
// Gated, and skipped rather than failed when the gate is closed, the way
// tests/container and the live suites gate themselves -- so `go test
// ./...` on a laptop stays a unit run:
//
//	GRAIN_TEST_CI_E2E=host     run it against orchestrator.HostSandboxes:
//	                           a directory on this machine. Needs
//	                           everything the workflow's steps need (go,
//	                           node, npm, make) on this machine's own
//	                           PATH, since a host sandbox is this
//	                           machine. A host sandbox pins HOME to
//	                           itself, so unless GOMODCACHE and
//	                           npm_config_cache are set in this test's
//	                           own environment the job downloads the
//	                           whole module and package tree into a
//	                           directory it then deletes -- pass this
//	                           machine's own caches and the run is a
//	                           build rather than a download. Those two
//	                           are on the short allowlist of the
//	                           daemon's variables a host sandbox's shell
//	                           passes to a step
//	                           (orchestrator.hostShellPassthrough);
//	                           nothing else in this test's environment
//	                           reaches one, so a variable a step needs
//	                           goes on that list or in the workflow.
//	GRAIN_TEST_CI_E2E=granule   run it against a granule VM per job, which
//	                           is what a real deployment does. Needs
//	                           /dev/kvm, docker and a built guest image;
//	                           the KONTUR_* environment
//	                           daemon_granule_wiring_test.go documents.
//
//	GRAIN_TEST_CI_E2E_COMMIT   the commit to test. Defaults to this
//	                           checkout's HEAD.
//	GRAIN_TEST_GITHUB_TOKEN    optional: a token that may read this
//	                           repository's check runs. With it, the
//	                           test does not merely assert its own
//	                           verdict -- it compares it with what
//	                           .github/workflows/tests.yml concluded
//	                           about the same commit, which is the only
//	                           way "grain's CI agrees with GitHub's" is
//	                           a measurement rather than a hope.
//
// One deliberate difference from the file as it is written belongs to
// the host backend and not the granule one:
//
//   - the jobs' `sandbox:` sizes are dropped, because a host directory
//     has no size of its own and orchestrator.HostSandboxes refuses a
//     shape rather than silently ignoring it.
//
// Network access is intentionally available in both arms: it is inherent
// on the host, while the granule CI adapter explicitly requests open egress.
//
// Everything else -- the steps, their order, their environment, the
// checkout they run in -- is the file.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestLiveCIRunsThisRepositorysOwnWorkflowInARealSandbox(t *testing.T) {
	t.Parallel()
	backend := os.Getenv("GRAIN_TEST_CI_E2E")
	if backend == "" {
		t.Skip("set GRAIN_TEST_CI_E2E=host (or =granule) to run one real grain CI job; see this file's own doc comment")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()

	source := repoRoot(t)
	commit := os.Getenv("GRAIN_TEST_CI_E2E_COMMIT")
	if commit == "" {
		commit = gitOutput(t, source, "rev-parse", "HEAD")
	}
	branch := gitOutput(t, source, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "HEAD" {
		branch = "main"
	}
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	t.Logf("grain CI e2e: %s/%s@%s (%s), backend %q", repo.Owner, repo.Name, branch, commit, backend)

	// A bare clone of this checkout, served the way GitHub serves one, so
	// the sandbox's clone is a real clone over real smart HTTP through
	// the real proxy -- and so this test needs no network and no
	// credential of anybody's.
	gitRoot := t.TempDir()
	bare := filepath.Join(gitRoot, repo.Owner, repo.Name+".git")
	gitOutput(t, source, "clone", "--quiet", "--bare", source, bare)
	upstream := httptest.NewServer(gitHTTPBackendLive(gitRoot))
	defer upstream.Close()

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()

	// The proxy the job clones through, holding the same read-scope
	// registry the adapter grants into -- startGitProxy's own wiring, so
	// what authorizes this clone is what authorizes a deployment's.
	proxyURL, _, stopProxy, err := daemon.StartGitProxy(dataDir, store,
		strings.TrimPrefix(upstream.URL, "http://"), true, daemon.NewDirect(""), nil)
	if err != nil {
		t.Fatalf("startGitProxy: %v", err)
	}
	defer stopProxy(context.Background())

	sandboxes, sized := e2eSandboxBackend(t, backend)
	// On granule each job is a VM of the size it asked for, so the engine
	// may run as many at once as a deployment's -max-workers would. On
	// host they are two directories on one machine, and this
	// repository's two jobs between them ask for twelve vCPUs: running
	// them at once is how a twenty-minute frontend job is cancelled at
	// its own bound having spent the whole of it waiting for a core.
	concurrency := ci.DefaultConcurrency
	if !sized {
		concurrency = 1
	}
	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))
	service := &ci.Service{
		Source: ciSourceFromCommit(t, source, commit, sized),
		Engine: &ci.Engine{
			Sandboxes: daemon.CiSandboxes{
				Sandboxes:     sandboxes,
				GitRemoteBase: proxyURL,
				MintToken:     tokens.EnsureToken,
				RevokeToken:   tokens.Revoke,
				Scopes:        daemon.CiReadScopes,
				Log:           t.Logf,
			},
			Concurrency: concurrency,
			Log:         t.Logf,
		},
		Log: t.Logf,
	}

	run, err := service.Start(ctx, ci.Request{
		Repo: repo.Owner + "/" + repo.Name, Ref: branch, Commit: commit,
		Event: ci.EventManual, RequestedBy: "ci_e2e_test",
	})
	if err != nil {
		t.Fatalf("starting a CI run: %v", err)
	}
	if len(run.Jobs) == 0 {
		t.Fatal("this repository's own .grain/workflows planned no jobs for a manual run")
	}

	finished := awaitCIRun(t, service, run.ID)
	for _, job := range finished.Jobs {
		t.Logf("job %s/%s: %s in %s (sandbox %s)", job.WorkflowPath, job.JobID, job.Conclusion,
			ciJobDuration(job, time.Now()), job.Sandbox)
		for _, step := range job.Steps {
			t.Logf("  step %s: %s (exit %d)", step.Name, step.Conclusion, step.ExitCode)
			if step.Conclusion == ci.ConclusionFailure {
				t.Logf("  --- %s said:\n%s", step.Name, step.Output)
			}
		}
		if job.Error != "" {
			t.Logf("  job error: %s", job.Error)
		}
	}

	// The verdict, and then -- where a token was given -- the same
	// verdict GitHub reached about the same commit. Two CIs disagreeing
	// about one commit is the finding this whole exercise exists to look
	// for; asserting only that grain's own is green would find a grain
	// that is wrong in the same direction twice.
	if finished.Conclusion != ci.ConclusionSuccess {
		t.Errorf("grain CI concluded %s for %s; %s", finished.Conclusion, commit, finished.Error)
	}
	if verdict, ok := gitHubVerdict(t, repo, commit); ok {
		t.Logf("GitHub concluded %q for the same commit", verdict)
		if verdict == "success" && finished.Conclusion != ci.ConclusionSuccess {
			t.Errorf("GitHub says %s passes and grain CI says %s", commit, finished.Conclusion)
		}
		if verdict == "failure" && finished.Conclusion == ci.ConclusionSuccess {
			t.Errorf("GitHub says %s fails and grain CI says it passes", commit)
		}
	}
}

// e2eSandboxBackend builds the backend the gate named, and reports
// whether it can size a sandbox -- which decides whether the workflow's
// own `sandbox:` blocks survive into the plan (see this file's own doc
// comment).
func e2eSandboxBackend(t *testing.T, backend string) (orchestrator.Sandboxes, bool) {
	t.Helper()
	switch backend {
	case "host":
		return orchestrator.NewHostSandboxes(t.TempDir()), false
	case "granule":
		return orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
			StateDir:   os.Getenv("GRANULE_STATE_DIR"),
			CreateArgs: strings.Fields(os.Getenv("GRANULE_CREATE_ARGS")),
			SSHUser:    os.Getenv("GRANULE_SSH_USER"),
			Workspace:  os.Getenv("GRANULE_WORKSPACE"),
		}), true
	default:
		t.Fatalf("GRAIN_TEST_CI_E2E=%q: expected \"host\" or \"granule\"", backend)
		return nil, false
	}
}

// ciSourceFromCommit is a ci.Source that reads `.grain/workflows` out of
// one commit of this checkout, with git rather than with the filesystem
// -- so what runs is what that commit declares rather than whatever is in
// the working tree at the time the test runs.
//
// The daemon's own source (ciWorkflowSource) clones from GitHub, which
// this test deliberately does not reach; what is under test here is the
// engine, the adapter and the workflow file, not where the bytes of the
// file came from.
//
// sized false drops each job's requested sandbox shape, which is the one
// concession the host backend needs.
func ciSourceFromCommit(t *testing.T, dir, commit string, sized bool) ci.Source {
	t.Helper()
	return ci.SourceFunc(func(_ context.Context, _, _ string) ([]ci.Workflow, string, error) {
		listing := gitOutput(t, dir, "ls-tree", "--name-only", commit, ci.WorkflowDir+"/")
		var workflows []ci.Workflow
		for _, file := range strings.Fields(listing) {
			switch strings.ToLower(path.Ext(file)) {
			case ".yml", ".yaml":
			default:
				continue
			}
			w, err := ci.Parse(file, []byte(gitOutput(t, dir, "show", commit+":"+file)))
			if err != nil {
				return nil, "", err
			}
			if !sized {
				for i := range w.Jobs {
					w.Jobs[i].Sandbox = ci.SandboxSpec{}
				}
			}
			workflows = append(workflows, w)
		}
		if len(workflows) == 0 {
			return nil, "", fmt.Errorf("%s declares no workflows at %s", ci.WorkflowDir, commit)
		}
		return workflows, commit, nil
	})
}

// awaitCIRun waits for the run to reach a conclusion, which is what a
// caller of the API does: Start is asynchronous on purpose.
//
// How long it waits is `go test -timeout`'s business rather than this
// file's -- the same choice daemon_live_test.go makes -- so this polls
// until the test's own deadline rather than inventing a window of its
// own for a suite whose whole point is that it takes as long as a build
// takes.
func awaitCIRun(t *testing.T, service *ci.Service, id string) ci.Run {
	t.Helper()
	for {
		run, ok := service.Get(id)
		if !ok {
			t.Fatalf("CI run %s vanished from the run store", id)
		}
		if run.Conclusion != "" {
			return run
		}
		time.Sleep(2 * time.Second)
	}
}

// gitHubVerdict is what GitHub's own checks concluded about commit, when
// this test was given a token that may ask. "" and false when it was not,
// which is the ordinary case and not a failure: the comparison is the
// bonus, the run is the test.
func gitHubVerdict(t *testing.T, repo model.RepoRef, commit string) (string, bool) {
	t.Helper()
	token := os.Getenv("GRAIN_TEST_GITHUB_TOKEN")
	if token == "" {
		t.Log("no GRAIN_TEST_GITHUB_TOKEN, so grain's verdict is not compared with GitHub's own")
		return "", false
	}
	client := github.NewClient(github.NewRealTransport("github.com"), github.StaticToken{Token: &token})
	checks, err := client.ListCheckRuns(repo.Owner, repo.Name, commit)
	if err != nil {
		t.Logf("asking GitHub what it made of %s: %v", commit, err)
		return "", false
	}
	// The same fold pkg/ci's own conclusion does, over GitHub's answer: a
	// failure anywhere is a failure, an unfinished check means GitHub has
	// not said yet, and everything else finished is a pass.
	verdict := "success"
	for _, check := range checks {
		t.Logf("  GitHub check %q: %s", check.Name, check.Status)
		if check.Status != "completed" {
			return "", false
		}
		if check.Conclusion == nil {
			continue
		}
		if *check.Conclusion == "failure" || *check.Conclusion == "timed_out" {
			verdict = "failure"
		}
	}
	if len(checks) == 0 {
		return "", false
	}
	return verdict, true
}
