// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

// ci_test.go covers the CI wiring in ci.go: the adapter that turns
// whichever sandbox backend this deployment runs into the one pkg/ci
// asks for, and the read-only git scope a job's sandbox holds while it
// runs.
//
// The live test at the bottom is the one that proves the pair actually
// works together -- a real host sandbox cloning a real repository
// through a real git proxy, and being refused when it tries to push it
// back.

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// TestTheCIGateSaysSoBeforeTheReconcileLoopHasStarted: the UI/API server
// is up before runDaemon has built a CI service, and a request that
// arrives in that window is told what is going on rather than
// dereferencing nil.
func TestTheCIGateSaysSoBeforeTheReconcileLoopHasStarted(t *testing.T) {
	t.Parallel()
	if daemon.LiveCI.Load() != nil {
		t.Skip("something in this package has already published a CI service")
	}
	// Collected rather than reported as they are found, because the
	// window this test is about can close underneath it: liveCI is
	// process-global and any of this binary's daemon tests may publish
	// into it (and never takes it back) while these five calls are
	// running, at which point a gate that refused nothing is a gate doing
	// its job. The check above cannot be the whole guard -- it is one
	// instant, and eight parallel lanes make the gap after it a real one
	// -- and a t.Skip after a t.Error would still be a red, so the
	// re-check has to come before anything is reported (grain/task-510).
	var problems []string
	if _, err := (daemon.CiGate{}).Start(context.Background(), ci.Request{Repo: "acme/widgets", Ref: "main"}); err == nil {
		problems = append(problems, "starting a run before the CI service exists should be refused, and say why")
	}
	if _, err := (daemon.CiGate{}).WorkflowOptions(context.Background(), ci.Request{Repo: "acme/widgets", Ref: "main"}); err == nil {
		problems = append(problems, "listing workflow options before the CI service exists should be refused, and say why")
	}
	if _, ok := (daemon.CiGate{}).Get("whatever"); ok {
		problems = append(problems, "there are no runs to get yet")
	}
	if runs := (daemon.CiGate{}).List(); len(runs) != 0 {
		problems = append(problems, fmt.Sprintf("List = %v, want nothing yet", runs))
	}
	if (daemon.CiGate{}).Cancel("whatever", "", ci.CancelBySystem) {
		problems = append(problems, "there is nothing to cancel yet")
	}
	if daemon.LiveCI.Load() != nil {
		t.Skip("a CI service was published while this test was running: the window it is about had closed")
	}
	for _, problem := range problems {
		t.Error(problem)
	}
}

// --- a sandbox backend that records what it was asked to do -------------

type fakeSandboxBackend struct {
	sandbox      orchestrator.Sandbox
	acquired     string
	shape        orchestrator.Shape
	defaultShape orchestrator.Shape
	acquerErr    error
}

func (b *fakeSandboxBackend) SetDefaultShape(shape orchestrator.Shape) {
	b.defaultShape = shape
}

func (b *fakeSandboxBackend) DefaultShape() orchestrator.Shape {
	return b.defaultShape
}

func (b *fakeSandboxBackend) Acquire(_ context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	b.acquired, b.shape = name, shape
	if b.acquerErr != nil {
		return nil, b.acquerErr
	}
	if fs, ok := b.sandbox.(*fakeSandbox); ok {
		fs.name = name
	}
	return b.sandbox, nil
}

type fakeSandbox struct {
	name        string
	commands    []string
	runCommands [][]string
	runStdins   []string
	remoteURL   string
	token       string
	released    bool
	noTools     bool
	answer      func(command string) mcp.Result
	toolsError  error
}

func (s *fakeSandbox) Name() string { return s.name }

func (s *fakeSandbox) Tools(context.Context) ([]mcp.Tool, error) {
	if s.toolsError != nil {
		return nil, s.toolsError
	}
	if s.noTools {
		return nil, nil
	}
	return []mcp.Tool{{
		Name: "run_command",
		Handler: func(_ context.Context, args map[string]any) mcp.Result {
			command, _ := args["command"].(string)
			s.commands = append(s.commands, command)
			if s.answer != nil {
				return s.answer(command)
			}
			return mcp.Result{Text: "exit=0\nstdout:\n\nstderr:\n"}
		},
	}}, nil
}

func (s *fakeSandbox) ConfigureGitCredentials(_ context.Context, remoteURL, token string, _ mcp.GitIdentity) error {
	s.remoteURL, s.token = remoteURL, token
	return nil
}

func (s *fakeSandbox) Release(context.Context) error {
	s.released = true
	return nil
}

func (s *fakeSandbox) RunInSandbox(_ context.Context, argv []string, stdin string) (string, string, int) {
	s.runCommands = append(s.runCommands, argv)
	s.runStdins = append(s.runStdins, stdin)
	return "", "", 0
}

// newCIAdapter is ciSandboxes over a fake backend and a registry of its
// own, so one test's grants cannot be another's.
func newCIAdapter(sandbox *fakeSandbox) (daemon.CiSandboxes, *fakeSandboxBackend, *gitproxy.ReadScopes) {
	backend := &fakeSandboxBackend{sandbox: sandbox}
	scopes := gitproxy.NewReadScopes()
	writeScopes := gitproxy.NewWriteScopes()
	revoked := map[string]bool{}
	return daemon.CiSandboxes{
		Sandboxes:     backend,
		GitRemoteBase: "http://127.0.0.1:9999",
		MintToken:     func(string) (string, error) { return "a-minted-token", nil },
		RevokeToken:   func(name string) error { revoked[name] = true; return nil },
		Scopes:        scopes,
		WriteScopes:   writeScopes,
	}, backend, scopes
}

func TestACIJobsSandboxIsCheckedOutAtTheCommitUnderTest(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{}
	adapter, backend, scopes := newCIAdapter(sandbox)

	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name: "ci-run1-tests-build", Repo: "acme/widgets", Ref: "main", Commit: "deadbeef",
		Shape: ci.SandboxSpec{CPUs: 2, MemoryMB: 4096},
	})
	if err != nil {
		t.Fatalf("acquiring a CI sandbox: %v", err)
	}
	if backend.acquired != "ci-run1-tests-build" {
		t.Errorf("backend was asked for %q, want the name the engine derived", backend.acquired)
	}
	if backend.shape != (orchestrator.Shape{CPUs: 2, MemoryMB: 4096, NetworkAccess: model.NetworkOpen, Class: orchestrator.SandboxClassCI}) {
		t.Errorf("backend was asked for shape %+v, want the job's size and GitHub-compatible open network", backend.shape)
	}
	if job.Name() != "ci-run1-tests-build" {
		t.Errorf("job sandbox name = %q", job.Name())
	}
	if len(sandbox.commands) != 1 {
		t.Fatalf("expected exactly one setup command, got %d: %q", len(sandbox.commands), sandbox.commands)
	}
	checkout := sandbox.commands[0]
	for _, want := range []string{
		"git clone --quiet 'http://127.0.0.1:9999/acme/widgets.git' 'work'",
		"git checkout --quiet --detach 'deadbeef'",
	} {
		if !strings.Contains(checkout, want) {
			t.Errorf("checkout script does not contain %q:\n%s", want, checkout)
		}
	}
	if sandbox.token != "a-minted-token" || !strings.HasPrefix(sandbox.remoteURL, "http://127.0.0.1:9999") {
		t.Errorf("git credentials configured with %q/%q, want the proxy and this sandbox's own token",
			sandbox.remoteURL, sandbox.token)
	}

	// The scope, while the job runs: this sandbox, this repository.
	repo, ok := scopes.Repo("ci-run1-tests-build")
	if !ok || repo != (model.RepoRef{Owner: "acme", Name: "widgets"}) {
		t.Fatalf("read scope for the job = %+v (present=%v), want the repo under test", repo, ok)
	}
}

func TestACIJobTreatsSandboxDimensionsAsAtLeastFloorAgainstDeploymentDefault(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{}
	adapter, backend, _ := newCIAdapter(sandbox)
	backend.SetDefaultShape(orchestrator.Shape{CPUs: 12, MemoryMB: 16384, DiskGB: 40})

	ledger := &model.Ledger{}
	gate := &ci.SharedGate{
		Budget: func() model.Budget {
			return model.Budget{
				Host:             model.Commitment{CPUs: 64, MemoryMB: 128 * 1024},
				CPUOvercommit:    1,
				MemoryOvercommit: 1,
			}
		},
		Ledger: func() *model.Ledger { return ledger },
		Default: func() model.Commitment {
			ds := backend.DefaultShape()
			return model.Commitment{CPUs: ds.CPUs, MemoryMB: ds.MemoryMB, DiskGB: ds.DiskGB}
		},
	}

	for _, tc := range []struct {
		name           string
		defaultShape   orchestrator.Shape
		reqShape       ci.SandboxSpec
		wantCommitment model.Commitment
		wantAcquired   orchestrator.Shape
	}{
		{
			name:           "smaller job shape scales up to deployment default",
			defaultShape:   orchestrator.Shape{CPUs: 12, MemoryMB: 16384, DiskGB: 40},
			reqShape:       ci.SandboxSpec{CPUs: 2, MemoryMB: 8192},
			wantCommitment: model.Commitment{CPUs: 12, MemoryMB: 16384, DiskGB: 40},
			wantAcquired:   orchestrator.Shape{CPUs: 12, MemoryMB: 16384, DiskGB: 40, NetworkAccess: model.NetworkOpen, Class: orchestrator.SandboxClassCI},
		},
		{
			name:           "larger job shape keeps its larger dimensions",
			defaultShape:   orchestrator.Shape{CPUs: 12, MemoryMB: 16384, DiskGB: 40},
			reqShape:       ci.SandboxSpec{CPUs: 16, MemoryMB: 32768, DiskGB: 60},
			wantCommitment: model.Commitment{CPUs: 16, MemoryMB: 32768, DiskGB: 60},
			wantAcquired:   orchestrator.Shape{CPUs: 16, MemoryMB: 32768, DiskGB: 60, NetworkAccess: model.NetworkOpen, Class: orchestrator.SandboxClassCI},
		},
		{
			name:           "cpus-only job with larger default gets max cpus and default memory/disk",
			defaultShape:   orchestrator.Shape{CPUs: 12, MemoryMB: 16384, DiskGB: 40},
			reqShape:       ci.SandboxSpec{CPUs: 8},
			wantCommitment: model.Commitment{CPUs: 12, MemoryMB: 16384, DiskGB: 40},
			wantAcquired:   orchestrator.Shape{CPUs: 12, MemoryMB: 16384, DiskGB: 40, NetworkAccess: model.NetworkOpen, Class: orchestrator.SandboxClassCI},
		},
		{
			name:           "cpus-only job with smaller default keeps job cpus and takes default memory/disk",
			defaultShape:   orchestrator.Shape{CPUs: 4, MemoryMB: 16384, DiskGB: 40},
			reqShape:       ci.SandboxSpec{CPUs: 8},
			wantCommitment: model.Commitment{CPUs: 8, MemoryMB: 16384, DiskGB: 40},
			wantAcquired:   orchestrator.Shape{CPUs: 8, MemoryMB: 16384, DiskGB: 40, NetworkAccess: model.NetworkOpen, Class: orchestrator.SandboxClassCI},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend.SetDefaultShape(tc.defaultShape)
			release, err := gate.Acquire(context.Background(), ci.GateRequest{Want: ci.SandboxCommitment(tc.reqShape)})
			if err != nil {
				t.Fatalf("gate.Acquire: %v", err)
			}
			if got := ledger.Live(); got != tc.wantCommitment {
				t.Errorf("gate charged %+v, want %+v", got, tc.wantCommitment)
			}
			job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
				Name: "ci-run1-tests-build", Repo: "acme/widgets", Ref: "main", Commit: "deadbeef",
				Shape: tc.reqShape,
			})
			if err != nil {
				release()
				t.Fatalf("adapter.Acquire: %v", err)
			}
			if backend.shape != tc.wantAcquired {
				t.Errorf("backend acquired shape %+v, want %+v", backend.shape, tc.wantAcquired)
			}
			_ = job.Release(context.Background())
			release()
		})
	}
}

func TestACIJobWithNoCommitChecksOutTheBranch(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{}
	adapter, _, _ := newCIAdapter(sandbox)
	if _, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name: "ci-1", Repo: "acme/widgets", Ref: "some-branch",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sandbox.commands[0], "'some-branch'") {
		t.Errorf("expected the branch to be what is checked out:\n%s", sandbox.commands[0])
	}
}

func TestReleasingACIJobDropsItsScopeAndDestroysTheSandbox(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{}
	adapter, _, scopes := newCIAdapter(sandbox)

	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Release(context.Background()); err != nil {
		t.Fatalf("releasing: %v", err)
	}
	if !sandbox.released {
		t.Error("the backing sandbox was not destroyed")
	}
	if scopes.Len() != 0 {
		t.Errorf("the job's read scope outlived it: %d grant(s) left", scopes.Len())
	}
}

func TestAFailedCheckoutReleasesTheSandboxItFailedIn(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{answer: func(string) mcp.Result {
		return mcp.Result{Text: "exit=128\nstdout:\n\nstderr:\nfatal: repository not found\n"}
	}}
	adapter, _, scopes := newCIAdapter(sandbox)

	_, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets", Ref: "main"})
	if err == nil {
		t.Fatal("a checkout that failed should fail the job rather than handing over a sandbox with no repository in it")
	}
	if !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("error = %v, want git's own words in it", err)
	}
	if !sandbox.released {
		t.Error("a sandbox whose checkout failed must still be destroyed")
	}
	if scopes.Len() != 0 {
		t.Errorf("a failed acquire left %d read scope(s) behind", scopes.Len())
	}
}

func TestACIJobNeedsSomewhereToCloneFrom(t *testing.T) {
	t.Parallel()
	adapter, _, _ := newCIAdapter(&fakeSandbox{})
	adapter.GitRemoteBase = ""
	if _, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets"}); err == nil {
		t.Fatal("a deployment with no git proxy has nowhere for a CI job to clone from and should say so")
	}
}

func TestACIJobNeedsARunCommandTool(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{noTools: true}
	adapter, _, scopes := newCIAdapter(sandbox)
	if _, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets"}); err == nil {
		t.Fatal("a sandbox with no run_command has nothing to run a step with")
	}
	if !sandbox.released || scopes.Len() != 0 {
		t.Error("the sandbox and its scope should both be gone")
	}
}

func TestACIStepRunsInTheCheckoutWithItsOwnEnvironment(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{}
	adapter, _, _ := newCIAdapter(sandbox)
	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}

	out, err := job.Exec(context.Background(), ci.Command{
		Script: "make test",
		Dir:    "sub/dir",
		Env:    map[string]string{"CI": "true", "GRAIN_CI_COMMIT": "dead'beef", "not a name": "dropped"},
	})
	if err != nil {
		t.Fatalf("running a step: %v", err)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit code = %d, want the 0 the tool reported", out.ExitCode)
	}
	script := sandbox.commands[len(sandbox.commands)-1]
	for _, want := range []string{
		"set -e\n",
		"cd 'work'\n",
		"cd 'sub/dir'\n",
		"export CI='true'\n",
		`export GRAIN_CI_COMMIT='dead'\''beef'` + "\n",
		"make test",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("step script does not contain %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "not a name") {
		t.Errorf("an env name that is not a shell identifier should be dropped, not written:\n%s", script)
	}
}

func TestAFailedStepIsAnAnswerAndNotAnError(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{answer: func(command string) mcp.Result {
		if strings.Contains(command, "make test") {
			return mcp.Result{Text: "exit=2\nstdout:\nFAIL\nstderr:\n", IsError: true}
		}
		return mcp.Result{Text: "exit=0\n"}
	}}
	adapter, _, _ := newCIAdapter(sandbox)
	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := job.Exec(context.Background(), ci.Command{Script: "make test"})
	if err != nil {
		t.Fatalf("a step that exited non-zero is CI's answer, not a transport failure: %v", err)
	}
	if out.ExitCode != 2 || !strings.Contains(out.Output, "FAIL") {
		t.Errorf("step result = %+v, want exit 2 and what it printed", out)
	}
}

func TestATransportFailureFailsTheJobRatherThanTheStep(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{answer: func(string) mcp.Result {
		return mcp.Result{Text: "the sandbox is gone", IsError: true}
	}}
	adapter, _, _ := newCIAdapter(sandbox)
	// The checkout goes through the same answer, so acquire fails first;
	// drive Exec directly against a prepared sandbox instead.
	job := &daemon.CiSandbox{Owner: adapter, Backing: sandbox, SandboxName: "ci-1"}
	tools, _ := sandbox.Tools(context.Background())
	job.Run, _ = daemon.CiRunCommand(tools)

	if _, err := job.Exec(context.Background(), ci.Command{Script: "make test"}); err == nil {
		t.Fatal("an answer with no exit status at all is the transport failing, which fails the job")
	}
}

func TestACIJobRefusesARepositoryItCannotName(t *testing.T) {
	t.Parallel()
	adapter, _, _ := newCIAdapter(&fakeSandbox{})
	for _, repo := range []string{"", "not a repo"} {
		if _, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: repo}); err == nil {
			t.Errorf("Acquire(%q) should have refused", repo)
		}
	}
}

func TestABackendFailureIsReportedWithNoScopeLeftBehind(t *testing.T) {
	t.Parallel()
	backend := &fakeSandboxBackend{sandbox: &fakeSandbox{}, acquerErr: errors.New("no room")}
	scopes := gitproxy.NewReadScopes()
	adapter := daemon.CiSandboxes{
		Sandboxes: backend, GitRemoteBase: "http://127.0.0.1:9999",
		MintToken: func(string) (string, error) { return "t", nil },
		Scopes:    scopes,
	}
	if _, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets"}); err == nil {
		t.Fatal("a backend that cannot build a sandbox should fail the job")
	}
	if scopes.Len() != 0 {
		t.Error("nothing should be granted for a sandbox that was never built")
	}
}

// TestCISandboxNamesFitAGranuleVMName holds the two ends of a budget
// neither package can see whole together. pkg/ci bounds a job's sandbox
// name at ci.MaxSandboxName because a granule VM is named after it, and
// the orchestrator refuses a VM name over its own cap
// (orchestrator.VMNameFor, 11 bytes for netshim's tap device) -- but
// pkg/ci imports neither the orchestrator nor granule, so nothing in
// either package can notice the day one of the two constants moves.
// This file is where both are in scope, which is the argument ci.go's
// own doc comment makes for the adapter living here.
//
// A CI deployment on granule was dead on this: names were bounded at
// sixty, so every job failed at Acquire before it ran a step.
func TestCISandboxNamesFitAGranuleVMName(t *testing.T) {
	t.Parallel()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{})
	if _, err := k.VMNameFor(strings.Repeat("c", ci.MaxSandboxName)); err != nil {
		t.Errorf("a %d-byte CI sandbox name (ci.MaxSandboxName) is more than a granule VM name affords: %v",
			ci.MaxSandboxName, err)
	}
	// And the budget is the whole of what is left, rather than a number
	// picked short of it: one byte more must not fit, or pkg/ci is
	// spending fewer bytes on a name than it could.
	if _, err := k.VMNameFor(strings.Repeat("c", ci.MaxSandboxName+1)); err == nil {
		t.Errorf("ci.MaxSandboxName is %d, but a granule VM name has room for more -- raise it",
			ci.MaxSandboxName)
	}
}

// --- where workflows are read from --------------------------------------

// TestTheWorkflowSourceReadsWhatABranchDeclares pins ciWorkflowSource
// against a real repository served the same way the live test below
// serves one: the workflows it declares on that branch, and the commit
// they were read at.
func TestTheWorkflowSourceReadsWhatABranchDeclares(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	gitRoot := t.TempDir()
	bare := initBareRepo(t, filepath.Join(gitRoot, "acme", "widgets.git"))
	seed := filepath.Join(t.TempDir(), "seed")
	runLive(t, gitRoot, "git", "clone", "-q", bare, seed)
	runLive(t, seed, "git", "config", "user.email", "seed@example.com")
	runLive(t, seed, "git", "config", "user.name", "seed")
	if err := os.MkdirAll(filepath.Join(seed, ci.WorkflowDir), 0o755); err != nil {
		t.Fatal(err)
	}
	workflow := "name: tests\non: [push]\njobs:\n  build:\n    steps:\n      - run: make test\n"
	if err := os.WriteFile(filepath.Join(seed, ci.WorkflowDir, "tests.yml"), []byte(workflow), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seed, "git", "add", ".")
	runLive(t, seed, "git", "commit", "-q", "-m", "declare a workflow")
	runLive(t, seed, "git", "push", "-q", "origin", "main")

	upstream := httptest.NewServer(gitHTTPBackendLive(gitRoot))
	defer upstream.Close()

	source := daemon.CiWorkflowSource{
		GitHubHost:   strings.TrimPrefix(upstream.URL, "http://"),
		InsecureHTTP: true,
		TempDir:      t.TempDir(),
	}
	workflows, commit, err := source.Workflows(context.Background(), "acme/widgets", "main")
	if err != nil {
		t.Fatalf("reading a repository's workflows: %v", err)
	}
	if len(workflows) != 1 || workflows[0].Name != "tests" {
		t.Fatalf("workflows = %+v, want the one the branch declares", workflows)
	}
	head := strings.TrimSpace(string(mustOutput(t, gitRoot, "git", "--git-dir", bare, "rev-parse", "main")))
	if commit != head {
		t.Errorf("commit = %q, want the branch's own head %q", commit, head)
	}
}

// TestTheWorkflowSourcePlacesACommitOnItsBranch pins ciWorkflowSource's
// OnBranch against a real repository holding, beside its branch, a pull
// request head the way GitHub holds a fork's (refs/pull/N/head): served
// by commit, but in no branch's history, and so not a tree the
// repository vouches for.
func TestTheWorkflowSourcePlacesACommitOnItsBranch(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	gitRoot := t.TempDir()
	bare := initBareRepo(t, filepath.Join(gitRoot, "acme", "widgets.git"))
	seed := filepath.Join(t.TempDir(), "seed")
	runLive(t, gitRoot, "git", "clone", "-q", bare, seed)
	runLive(t, seed, "git", "config", "user.email", "seed@example.com")
	runLive(t, seed, "git", "config", "user.name", "seed")
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		runLive(t, seed, "git", "add", ".")
		runLive(t, seed, "git", "commit", "-q", "-m", name)
		return strings.TrimSpace(string(mustOutput(t, seed, "git", "rev-parse", "HEAD")))
	}
	older := commit("first")
	tip := commit("second")
	runLive(t, seed, "git", "push", "-q", "origin", "main")
	runLive(t, seed, "git", "checkout", "-q", "-b", "fork")
	fork := commit("from-a-fork")
	runLive(t, seed, "git", "push", "-q", "origin", "fork:refs/pull/1/head")

	upstream := httptest.NewServer(gitHTTPBackendLive(gitRoot))
	defer upstream.Close()
	source := daemon.CiWorkflowSource{
		GitHubHost:   strings.TrimPrefix(upstream.URL, "http://"),
		InsecureHTTP: true,
		TempDir:      t.TempDir(),
	}
	for _, tc := range []struct {
		commit string
		want   bool
	}{
		{tip, true},
		{older, true},
		{older[:10], true},
		{fork, false},
		{"main", false},
	} {
		got, err := source.OnBranch(context.Background(), "acme/widgets", "main", tc.commit)
		if err != nil {
			t.Fatalf("OnBranch(%s): %v", tc.commit, err)
		}
		if got != tc.want {
			t.Errorf("OnBranch(%s) = %v, want %v", tc.commit, got, tc.want)
		}
	}
}

func TestTheWorkflowSourceReadsTheControllerLocalConfigRepository(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare := initBareRepo(t, filepath.Join(t.TempDir(), "config.git"))
	seed := filepath.Join(t.TempDir(), "seed")
	runLive(t, t.TempDir(), "git", "clone", "-q", bare, seed)
	runLive(t, seed, "git", "config", "user.email", "seed@example.com")
	runLive(t, seed, "git", "config", "user.name", "seed")
	runLive(t, seed, "git", "checkout", "-q", "-b", "grain/task-7")
	if err := os.WriteFile(filepath.Join(seed, "PROPOSED"), []byte("config change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seed, "git", "add", ".")
	runLive(t, seed, "git", "commit", "-q", "-m", "change config")
	runLive(t, seed, "git", "push", "-q", "origin", "grain/task-7")

	source := daemon.CiWorkflowSource{ConfigRepoDir: bare, Forbidden: gitproxy.NewForbiddenSet(), TempDir: t.TempDir()}
	_, commit, err := source.Workflows(context.Background(), "grain/config", "grain/task-7")
	if err != nil {
		t.Fatalf("reading the local config branch: %v", err)
	}
	head := strings.TrimSpace(string(mustOutput(t, seed, "git", "rev-parse", "HEAD")))
	if commit != head {
		t.Errorf("commit = %q, want local config branch head %q", commit, head)
	}
}

func TestConfigRepoHasABuiltInStateCheck(t *testing.T) {
	t.Parallel()
	source := daemon.ConfigRepoCISource{}
	workflows, commit, err := source.Workflows(context.Background(), "GRAIN/CONFIG", "grain/task-7")
	if err != nil {
		t.Fatalf("reading the built-in config workflow: %v", err)
	}
	if commit != "" {
		t.Errorf("built-in workflow resolved commit %q; the branch source owns that", commit)
	}
	if len(workflows) != 1 {
		t.Fatalf("workflows = %+v, want the built-in config check", workflows)
	}
	workflow := workflows[0]
	if workflow.Path != daemon.ConfigRepoCIWorkflowPath || !workflow.On.Matches(ci.EventPush, "grain/task-7") {
		t.Errorf("workflow = %+v, want a push check at %s", workflow, daemon.ConfigRepoCIWorkflowPath)
	}
	if len(workflow.Jobs) != 1 || len(workflow.Jobs[0].Steps) != 1 {
		t.Fatalf("jobs = %+v, want one validation task", workflow.Jobs)
	}
	// Run by the daemon itself, so that the check needs no image of the
	// build it is running -- an image a registry cleanup can delete.
	if workflow.Jobs[0].Builtin != daemon.StateCheckBuiltin {
		t.Errorf("Builtin = %q, want %q", workflow.Jobs[0].Builtin, daemon.StateCheckBuiltin)
	}
	step := workflow.Jobs[0].Steps[0]
	if step.Run != "grain state check /state" {
		t.Errorf("validation command = %q, want the state check it runs", step.Run)
	}
	if strings.Contains(step.Run, "docker") {
		t.Errorf("validation command still pulls an image: %s", step.Run)
	}
	if step.Env["GRAIN_STATE_CHECK_TEAM"] != "" || step.Env["GRAIN_STATE_CHECK_BUILD"] == "" {
		t.Errorf("step env = %v, want the build and no team", step.Env)
	}

	ordinary, _, err := source.Workflows(context.Background(), "acme/widgets", "main")
	if err != nil || len(ordinary) != 0 {
		t.Errorf("ordinary repo got config workflow %+v, %v", ordinary, err)
	}
}

// The configuration repository's check, end to end: the workflow
// ConfigRepoCISource declares, planned and run by a real engine whose
// sandboxes are StateCheckSandboxes over the daemon's own local copy of
// grain/config. A dump grain wrote passes and one it cannot load fails,
// and neither is given a machine or an image -- the ordinary sandboxes
// behind it are never asked.
func TestTheConfigRepoCheckRunsInTheDaemonFromItsLocalCopy(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare := initBareRepo(t, filepath.Join(t.TempDir(), "config.git"))
	seed := filepath.Join(t.TempDir(), "seed")
	runLive(t, t.TempDir(), "git", "clone", "-q", bare, seed)
	runLive(t, seed, "git", "config", "user.email", "seed@example.com")
	runLive(t, seed, "git", "config", "user.name", "seed")
	runLive(t, seed, "git", "checkout", "-q", "-b", "grain/task-7")
	if err := os.CopyFS(seed, os.DirFS(exportedState(t))); err != nil {
		t.Fatal(err)
	}
	runLive(t, seed, "git", "add", ".")
	runLive(t, seed, "git", "commit", "-q", "-m", "a dump grain wrote")
	good := strings.TrimSpace(string(mustOutput(t, seed, "git", "rev-parse", "HEAD")))
	// The hand edit that used to take a deployment down at its next
	// start: a task row missing a column the schema insists on.
	path := filepath.Join(seed, staterepo.TablesDir, "task.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `"title"`, `"no_such_column"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seed, "git", "commit", "-q", "-am", "break it")
	bad := strings.TrimSpace(string(mustOutput(t, seed, "git", "rev-parse", "HEAD")))
	runLive(t, seed, "git", "push", "-q", "origin", "grain/task-7")

	workflows, _, err := daemon.ConfigRepoCISource{}.Workflows(context.Background(), "grain/config", "grain/task-7")
	if err != nil {
		t.Fatal(err)
	}
	refused := &refusingCISandboxes{}
	engine := &ci.Engine{Sandboxes: daemon.StateCheckSandboxes{
		Sandboxes: refused, ConfigRepoDir: bare, TempDir: t.TempDir(),
	}}
	for _, tc := range []struct {
		commit string
		want   ci.Conclusion
		says   string
	}{
		{good, ci.ConclusionSuccess, "ok: grain can load this"},
		{bad, ci.ConclusionFailure, "task"},
	} {
		plan, err := ci.PlanRun(ci.Request{Repo: "grain/config", Ref: "grain/task-7", Commit: tc.commit, Event: ci.EventPush}, workflows)
		if err != nil {
			t.Fatal(err)
		}
		run := engine.Run(context.Background(), "r-"+tc.commit[:7], plan, nil)
		if len(run.Jobs) != 1 || len(run.Jobs[0].Steps) != 1 {
			t.Fatalf("%s: run = %+v, want the one check", tc.commit, run)
		}
		job := run.Jobs[0]
		if job.Conclusion != tc.want {
			t.Errorf("%s: conclusion = %q, want %q (%s)\n%s", tc.commit, job.Conclusion, tc.want, job.Error, job.Steps[0].Output)
		}
		if !strings.Contains(job.Steps[0].Output, tc.says) {
			t.Errorf("%s: output does not say %q:\n%s", tc.commit, tc.says, job.Steps[0].Output)
		}
	}
	if refused.asked > 0 {
		t.Errorf("the config check asked the ordinary sandboxes for a machine %d times", refused.asked)
	}

	// Anything but the configuration repositories is refused, rather than
	// checked out from wherever on the daemon's host it names.
	if _, err := (daemon.StateCheckSandboxes{ConfigRepoDir: bare}).Acquire(context.Background(),
		ci.SandboxRequest{Repo: "acme/widgets", Commit: good, Builtin: daemon.StateCheckBuiltin}); err == nil {
		t.Error("an ordinary repository was checked out for the in-daemon state check")
	}
	// And so is a builtin this build does not have.
	if _, err := (daemon.StateCheckSandboxes{ConfigRepoDir: bare}).Acquire(context.Background(),
		ci.SandboxRequest{Repo: "grain/config", Commit: good, Builtin: "something-else"}); err == nil {
		t.Error("an unknown builtin job was given a sandbox")
	}
}

// refusingCISandboxes is the ordinary sandboxes, for a test in which no
// job should reach them.
type refusingCISandboxes struct{ asked int }

func (s *refusingCISandboxes) Acquire(context.Context, ci.SandboxRequest) (ci.Sandbox, error) {
	s.asked++
	return nil, errors.New("no sandboxes here")
}

func TestTheWorkflowSourceRefusesAForbiddenRepository(t *testing.T) {
	t.Parallel()
	source := daemon.CiWorkflowSource{
		Forbidden: gitproxy.NewForbiddenSet(model.RepoRef{Owner: "acme", Name: "grain-state"}),
	}
	if _, _, err := source.Workflows(context.Background(), "Acme/Grain-State", "main"); err == nil {
		t.Fatal("the repository no sandbox may fetch is not one CI may run against either")
	}
}

func mustOutput(t *testing.T, dir, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

// --- the whole thing, live ----------------------------------------------

// TestLiveACIJobClonesThroughTheProxyAndCannotPushBack drives the two
// halves of this file's wiring against real components: a real host
// sandbox, a real git proxy in front of a real bare repository, and a
// real git inside the sandbox.
//
// The store behind the proxy has no task and no run in it at all, which
// is the situation a CI job is genuinely in -- so the clone below can
// only be authorized by the read scope the adapter registered, and the
// push below can only be refused by that scope being read-only.
func TestLiveACIJobClonesThroughTheProxyAndCannotPushBack(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	// The stand-in GitHub: one repository with one commit in it.
	gitRoot := t.TempDir()
	bare := initBareRepo(t, filepath.Join(gitRoot, "acme", "widgets.git"))
	runLive(t, gitRoot, "git", "-C", bare, "config", "http.receivepack", "true")
	seed := filepath.Join(t.TempDir(), "seed")
	runLive(t, gitRoot, "git", "clone", "-q", bare, seed)
	runLive(t, seed, "git", "config", "user.email", "seed@example.com")
	runLive(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "hello.txt"), []byte("hello from the repo under test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seed, "git", "add", "hello.txt")
	runLive(t, seed, "git", "commit", "-q", "-m", "initial commit")
	runLive(t, seed, "git", "push", "-q", "origin", "main")

	upstream := httptest.NewServer(gitHTTPBackendLive(gitRoot))
	defer upstream.Close()

	// The daemon's own halves: a store with nothing in it, the proxy,
	// and the token store a sandbox's identity is minted from.
	dataDir := t.TempDir()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")
	// What the daemon does at startup: the ladder becomes the
	// deployment's git connections (docs/single-tenant-reset.md, stage 1).
	if err := store.UpdateConfig(context.Background(), func(t *model.Config) error {
		t.GitConnections = append(t.GitConnections, model.TeamGitConnection{Credential: "anonymous"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proxyURL, _, stopProxy, err := daemon.StartGitProxy(dataDir, store,
		strings.TrimPrefix(upstream.URL, "http://"), true, daemon.NewDirect(""), nil)
	if err != nil {
		t.Fatalf("startGitProxy: %v", err)
	}
	defer stopProxy(context.Background())

	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))
	adapter := daemon.CiSandboxes{
		Sandboxes:     orchestrator.NewHostSandboxes(t.TempDir()),
		GitRemoteBase: proxyURL,
		MintToken:     tokens.EnsureToken,
		RevokeToken:   tokens.Revoke,
		// The very registry startGitProxy just handed the proxy.
		Scopes: daemon.CiReadScopes,
		Log:    t.Logf,
	}

	ctx := context.Background()
	job, err := adapter.Acquire(ctx, ci.SandboxRequest{
		Name: "ci-live-1", Repo: "acme/widgets", Ref: "main",
	})
	if err != nil {
		t.Fatalf("acquiring a CI sandbox with a real checkout in it: %v", err)
	}
	defer job.Release(ctx)

	// The checkout is there, and a step sees it.
	out, err := job.Exec(ctx, ci.Command{Script: "cat hello.txt", Env: map[string]string{"GRAIN_CI": "true"}})
	if err != nil {
		t.Fatalf("running a step: %v", err)
	}
	if out.ExitCode != 0 || !strings.Contains(out.Output, "hello from the repo under test") {
		t.Fatalf("the job's first step did not see the checkout: %+v", out)
	}

	// And the thing the whole scope exists to prevent: a step that tries
	// to write the repository back is refused by the proxy, not by luck.
	out, err = job.Exec(ctx, ci.Command{Script: fmt.Sprintf(
		"git config user.email ci@example.com\n"+
			"git config user.name ci\n"+
			"echo 'a workflow was here' >> hello.txt\n"+
			"git add hello.txt\n"+
			"git commit -q -m 'a workflow rewrote the repository'\n"+
			"git push %s HEAD:main", "origin")})
	if err != nil {
		t.Fatalf("running the push step: %v", err)
	}
	if out.ExitCode == 0 {
		t.Fatalf("a CI job pushed the repository it was testing:\n%s", out.Output)
	}

	tip := exec.Command("git", "--git-dir", bare, "log", "-1", "--format=%s", "main")
	tipOut, err := tip.CombinedOutput()
	if err != nil {
		t.Fatalf("reading the upstream tip: %v: %s", err, tipOut)
	}
	if strings.TrimSpace(string(tipOut)) != "initial commit" {
		t.Errorf("upstream tip = %q, want the seed commit -- the push landed", strings.TrimSpace(string(tipOut)))
	}

	// Released, the sandbox's token authorizes nothing at all: the scope
	// it was granted is gone with the job.
	if err := job.Release(ctx); err != nil {
		t.Fatalf("releasing the job's sandbox: %v", err)
	}
	if _, ok := daemon.CiReadScopes.Repo("ci-live-1"); ok {
		t.Error("the released job still holds a read scope")
	}
}

// A CI job's sandbox is counted against the deployment's lifetime
// figures the same way a dispatched run's is (grain/task-315): two
// callers build sandboxes here, and "how many sandboxes, agent and CI"
// is only answerable if both of them count.
func TestACIJobsSandboxIsCountedAgainstTheDeploymentsLifetime(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	adapter, _, _ := newCIAdapter(&fakeSandbox{})
	adapter.Meter = orchestrator.MeterFor(store)

	job, err := adapter.Acquire(ctx, ci.SandboxRequest{
		Name: "c1a2b3-0", Repo: "acme/widgets", Ref: "main", Commit: "deadbeef",
	})
	if err != nil {
		t.Fatalf("acquiring a CI sandbox: %v", err)
	}
	if err := job.Release(ctx); err != nil {
		t.Fatalf("releasing a CI sandbox: %v", err)
	}

	usages, err := store.SandboxUsages(ctx)
	if err != nil {
		t.Fatalf("SandboxUsages: %v", err)
	}
	if len(usages) != 1 || usages[0].Kind != model.SandboxKindCI {
		t.Fatalf("counted %+v, want one CI row", usages)
	}
	got := usages[0]
	if got.Attempted != 1 || got.Created != 1 || got.Finished != 1 {
		t.Errorf("attempted=%d created=%d finished=%d, want 1/1/1 for one job", got.Attempted, got.Created, got.Finished)
	}
	// Released twice -- the engine's own teardown and a failed
	// checkout's both go through this method -- and still one life.
	if err := job.Release(ctx); err != nil {
		t.Fatalf("releasing a CI sandbox twice: %v", err)
	}
	if again := mustSandboxUsage(t, store, ctx); again.Finished != 1 {
		t.Errorf("finished = %d after a second release, want 1", again.Finished)
	}
}

func mustSandboxUsage(t *testing.T, store *model.Store, ctx context.Context) model.SandboxUsage {
	t.Helper()
	usages, err := store.SandboxUsages(ctx)
	if err != nil {
		t.Fatalf("SandboxUsages: %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("counted %+v, want one row", usages)
	}
	return usages[0]
}

type commandingFakeSandbox struct {
	fakeSandbox
	mu      sync.Mutex
	written map[string]string
}

func (s *commandingFakeSandbox) RunInSandbox(_ context.Context, argv []string, stdin string) (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written == nil {
		s.written = map[string]string{}
	}
	if len(argv) > 4 {
		s.written[argv[4]] = stdin
	}
	return "", "", 0
}

func TestACIJobsSandboxIsConfiguredWithTransparentPackageCache(t *testing.T) {
	t.Parallel()
	sandbox := &commandingFakeSandbox{}
	backend := &fakeSandboxBackend{sandbox: sandbox}
	scopes := gitproxy.NewReadScopes()
	writeScopes := gitproxy.NewWriteScopes()
	adapter := daemon.CiSandboxes{
		Sandboxes:       backend,
		GitRemoteBase:   "http://127.0.0.1:9999",
		MintToken:       func(string) (string, error) { return "a-minted-token", nil },
		RevokeToken:     func(string) error { return nil },
		Scopes:          scopes,
		WriteScopes:     writeScopes,
		PackageCache:    func(context.Context) string { return "http://172.17.0.1:43325" },
		PackageCacheCA:  func(context.Context) []byte { return []byte("---GRAIN-CA-CERT---") },
		PackageCacheDNS: func(context.Context) string { return "172.17.0.1" },
	}

	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{
		Name: "ci-run1-packagecache-test", Repo: "acme/widgets", Ref: "main", Commit: "deadbeef",
	})
	if err != nil {
		t.Fatalf("acquiring a CI sandbox: %v", err)
	}
	defer job.Release(context.Background())

	sandbox.mu.Lock()
	defer sandbox.mu.Unlock()

	if got := sandbox.written["/usr/local/share/ca-certificates/grain-ca.crt"]; got != "---GRAIN-CA-CERT---" {
		t.Errorf("CA certificate was not written to /usr/local/share/ca-certificates/grain-ca.crt: got %q", got)
	}
	if got := sandbox.written["/etc/resolv.conf"]; !strings.Contains(got, "nameserver 172.17.0.1") {
		t.Errorf("/etc/resolv.conf was not written with DNS server: got %q", got)
	}
	if got := sandbox.written["/etc/docker/certs.d/ghcr.io/hosts.toml"]; !strings.Contains(got, "172.17.0.1:43325") {
		t.Errorf("docker hosts.toml was not written for ghcr.io: got %q", got)
	}
}
