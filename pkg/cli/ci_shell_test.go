package cli

// Which transport a step runs over, and why it matters that it is not
// the one an agent uses.
//
// ci_test.go's own step tests drive a fake sandbox that exposes only a
// run_command tool, which is the fallback; the backends a deployment
// actually runs also implement orchestrator.SandboxShell, and that is
// the path a real job takes. These pin the choice between them, because
// getting it wrong is not visible in a passing job -- it is visible in a
// forty-minute one that is killed at ten with an unreadable log.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// shellSandbox is a fakeSandbox that also has a shell, the way
// hostSandbox and granuleSandbox both do.
type shellSandbox struct {
	fakeSandbox
	requests []orchestrator.ShellRequest
	result   orchestrator.ShellResult
	err      error
}

func (s *shellSandbox) RunShell(_ context.Context, req orchestrator.ShellRequest) (orchestrator.ShellResult, error) {
	s.requests = append(s.requests, req)
	return s.result, s.err
}

// shellBackend hands out one shellSandbox. newCIAdapter's own backend
// cannot: it is typed to *fakeSandbox, and what these tests are about is
// precisely the interface the concrete type does or does not have.
type shellBackend struct{ sandbox *shellSandbox }

func (b *shellBackend) Acquire(_ context.Context, name string, _ orchestrator.Shape) (orchestrator.Sandbox, error) {
	b.sandbox.name = name
	return b.sandbox, nil
}

// newShellCIAdapter is newCIAdapter over a backend with a shell in it.
func newShellCIAdapter(sandbox *shellSandbox) daemon.CiSandboxes {
	return daemon.CiSandboxes{
		Sandboxes:     &shellBackend{sandbox: sandbox},
		GitRemoteBase: "http://127.0.0.1:9999",
		MintToken:     func(string) (string, error) { return "a-minted-token", nil },
		RevokeToken:   func(string) error { return nil },
		Scopes:        gitproxy.NewReadScopes(),
	}
}

// TestACIStepPrefersTheSandboxShellOverTheAgentsToolCall: a backend with
// a shell gets the step over the shell, with the step's own bound and
// its own working directory, and run_command is left for the agents it
// was written for.
func TestACIStepPrefersTheSandboxShellOverTheAgentsToolCall(t *testing.T) {
	t.Parallel()
	sandbox := &shellSandbox{result: orchestrator.ShellResult{Output: "ok\n"}}
	adapter := newShellCIAdapter(sandbox)
	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}

	out, err := job.Exec(context.Background(), ci.Command{
		Script:  "go test -race -timeout 30m ./...",
		Dir:     "sub/dir",
		Env:     map[string]string{"CI": "true"},
		Timeout: 45 * time.Minute,
	})
	if err != nil {
		t.Fatalf("running a step: %v", err)
	}
	if out.ExitCode != 0 || out.Output != "ok\n" {
		t.Errorf("step result = %+v, want what the shell reported", out)
	}
	if len(sandbox.requests) != 1 {
		t.Fatalf("the step should have gone over the shell exactly once, not %d times", len(sandbox.requests))
	}
	req := sandbox.requests[0]
	if req.Script != "go test -race -timeout 30m ./..." {
		t.Errorf("script = %q, want the step's own, unwrapped", req.Script)
	}
	// The checkout, then the step's `working-directory` inside it.
	if req.Dir != orchestrator.CheckoutDir+"/sub/dir" {
		t.Errorf("dir = %q, want the step's directory inside the checkout", req.Dir)
	}
	// The bound the workflow file asked for, not run_command's ten
	// minutes: this is the whole reason for the second transport.
	if req.Timeout != 45*time.Minute {
		t.Errorf("timeout = %s, want the job's own %s", req.Timeout, 45*time.Minute)
	}
	if req.Env["CI"] != "true" {
		t.Errorf("env = %v, want the step's own", req.Env)
	}
	// The checkout goes over run_command either way (it is a clone, well
	// inside every bound), so what this asserts is that no *step* did.
	for _, command := range sandbox.commands {
		if strings.Contains(command, "go test") {
			t.Errorf("a step went over run_command as well as the shell:\n%s", command)
		}
	}
}

// TestACIStepWithNoShellFallsBackToRunCommand: a Sandbox implementation
// without one -- a test's, or a backend grain grows later -- still runs
// its steps, over the tool every sandbox has.
func TestACIStepWithNoShellFallsBackToRunCommand(t *testing.T) {
	t.Parallel()
	sandbox := &fakeSandbox{}
	adapter, _, _ := newCIAdapter(sandbox)
	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := job.Exec(context.Background(), ci.Command{Script: "make test"}); err != nil {
		t.Fatalf("running a step: %v", err)
	}
	if last := sandbox.commands[len(sandbox.commands)-1]; !strings.Contains(last, "make test") {
		t.Errorf("the step should have gone over run_command:\n%s", last)
	}
}

// TestAShellTransportFailureFailsTheJobRatherThanTheStep is
// ci.Sandbox.Exec's contract on the new path: a machine that has stopped
// answering is not a step that failed, because a step that failed is a
// verdict about the commit and this is not.
func TestAShellTransportFailureFailsTheJobRatherThanTheStep(t *testing.T) {
	t.Parallel()
	sandbox := &shellSandbox{err: errors.New("the VM stopped answering")}
	adapter := newShellCIAdapter(sandbox)
	job, err := adapter.Acquire(context.Background(), ci.SandboxRequest{Name: "ci-1", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := job.Exec(context.Background(), ci.Command{Script: "make test"}); err == nil {
		t.Fatal("a transport failure should be an error, not an exit code")
	}
}

// A non-zero exit over the shell is still an answer rather than an
// error, the same way it is over run_command.
func TestAFailedStepOverTheShellIsAnAnswerAndNotAnError(t *testing.T) {
	t.Parallel()
	sandbox := &shellSandbox{result: orchestrator.ShellResult{Output: "FAIL\n", ExitCode: 2}}
	adapter := newShellCIAdapter(sandbox)
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

// Checked here rather than at each use, so a change to either end fails
// to compile in this file.
var _ orchestrator.SandboxShell = (*shellSandbox)(nil)
