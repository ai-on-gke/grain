package steps

import (
	"context"
	"time"

	"github.com/ai-on-gke/grain/pkg/workflows"
)

// Sandboxes builds the sandbox one CI job runs in and hands it over
// ready to work in: the repository checked out at the commit under
// test, in the workspace the job's steps will run from.
//
// It is this package's own seam rather than orchestrator.Sandboxes
// itself, for the same reason ui.SandboxHealth is its own type: a
// package this low has no business importing the orchestrator, and the
// two interfaces want different things anyway. An agent run's sandbox
// hands out MCP tools and a git credential; a CI job's hands out a
// shell, a checkout, and any explicitly configured CI capability material
// (see the package doc on what a workflow file is trusted with). cmd/grain is
// where an adapter between them lives, which is the one place both
// types are ever in scope.
//
// Acquire is expected to do the whole of the setup a job needs -- build
// the machine, check the commit out, and answer only when a step could
// run -- because that is the boundary that keeps a workflow file free of
// `uses: actions/checkout` and free of anything that could reach a
// credential to do it with.
type Sandboxes interface {
	Acquire(ctx context.Context, req SandboxRequest) (Sandbox, error)
}

// Admission reserves a job's finite capability resources before either of
// the engine's sandbox gates is taken. A blocked call may report its current
// reason through wait; it must not build a sandbox itself.
//
// wait takes the same Wait a Gate reports, so that time spent here is
// attributed alongside time spent queueing for a machine -- a job held
// twenty minutes for a key and a job held twenty minutes for memory are
// the same length of wait and two entirely different problems.
type Admission interface {
	Acquire(ctx context.Context, req SandboxRequest, wait func(workflows.Wait)) (release func(), err error)
}

// SandboxRequest is what one job needs of a machine.
type SandboxRequest struct {
	// Capabilities is the operator-owned set snapshotted by Service,
	// narrowed to the job's declaration, plus the capability references
	// (model.IsCapabilityRef) the job declares, which Admission mints
	// subject to the deployment's capability policy (jobCapabilities).
	Capabilities []string
	// RunID and JobAttempt name this attempt at the job for the
	// capability grants minted for it: the attempt_ids of its pass are
	// <deployment>-<RunID>-<JobAttempt>-<index>. JobAttempt is the job's
	// place in its run's plan, so no two jobs of one run share one; a
	// re-run is a new run.
	RunID      string
	JobAttempt int
	// Untrusted is a run whose commit nothing vouches for -- the head of
	// a fork's pull request, or one pushed by somebody without write
	// access (Request.untrustedCommit) -- which the capability policy
	// denies every capability reference unless a rule opts it in.
	Untrusted bool
	// ReleaseSecrets is the trusted repository configuration snapshotted by
	// Service. Values are resolved by the sandbox adapter and never carried here.
	ReleaseSecrets []string
	// Event is the workflow event being executed. Capability providers may
	// use it only to narrow their own availability (for example, credentials
	// that are safe exclusively after a merge).
	Event workflows.Event
	// TrustedSchedule is Request.TrustedSchedule: Event is a schedule,
	// and it is one grain itself fired rather than one a caller asked
	// for, which a model.TrustedScheduleCapability may be released to.
	TrustedSchedule bool
	// Name is what the sandbox should be called -- unique per job per
	// run, derived by the engine (sandboxName) so a backend does not
	// have to invent one and an operator reading a VM list can tell
	// which run it belongs to. Never longer than MaxSandboxName, which
	// is what a backend that names a network interface after it has
	// room for.
	Name string
	// Repo, Ref and Commit are what to check out. Commit is the
	// authority where it is set; Ref is what to resolve when it is not.
	Repo   string
	Ref    string
	Commit string
	// Shape is the minimum size ("at least") the job asked for, zero
	// where it asked for nothing in particular -- each dimension is
	// resolved as the larger of the job's ask and the deployment's
	// default sandbox shape.
	Shape workflows.SandboxSpec
	// Builtin is Job.Builtin: empty for every job a workflow file
	// declares, and otherwise the name of work the deployment does
	// itself, which a Sandboxes that does not know that name must refuse
	// rather than run the job's scripts in an ordinary sandbox.
	Builtin string
}

// Sandbox is one job's machine, for as long as that job lasts.
type Sandbox interface {
	// Name is what this sandbox ended up being called -- recorded on the
	// JobRun, so it is worth being the name an operator would find it
	// under rather than the one that was asked for, if a backend has to
	// differ.
	Name() string
	// Exec runs one command and reports what it printed and what it
	// exited with. A non-zero exit is *not* an error: it is the answer
	// to what CI asked. An error is the transport failing -- the machine
	// gone, the connection dropped -- which fails the job rather than
	// the step.
	Exec(ctx context.Context, cmd Command) (CommandResult, error)
	// Release destroys the sandbox. Called exactly once per acquired
	// sandbox, success or failure alike.
	Release(ctx context.Context) error
}

// StreamingSandbox is a Sandbox whose Exec can say what a command is
// printing while it is still printing it, so a step that takes forty
// minutes can be watched rather than only read afterwards (see live.go).
//
// An optional interface, the way orchestrator.SandboxShell is: the engine
// asks for it and falls back to plain Exec where a backend does not have
// one, so a Sandbox in a test -- or a transport that genuinely cannot
// stream, like an MCP tool call that answers once -- needs no new method
// and loses nothing but the live view.
type StreamingSandbox interface {
	Sandbox
	// ExecStream runs cmd exactly as Exec does and reports exactly what
	// Exec reports -- the whole output, the script's status, an error only
	// for the transport -- and additionally calls out with each piece of
	// output as the sandbox produces it.
	//
	// out is called with the new bytes rather than with everything so far,
	// never concurrently with itself, and never after ExecStream has
	// returned. What a caller does with a chunk is its own business; an
	// implementation must not depend on it being fast, but it may assume
	// it returns.
	ExecStream(ctx context.Context, cmd Command, out func(chunk string)) (CommandResult, error)
}

// Command is one step's script, as the sandbox is asked to run it.
type Command struct {
	// Script is the step's `run:`, run with bash. Multi-line, and a
	// backend is expected to fail the whole script on the first failing
	// line (`set -e`, or however its transport spells it) -- GitHub's
	// own default shell behaviour, and the one that makes a two-line
	// step mean what it looks like it means.
	Script string
	// Dir is the working directory, relative to the workspace the
	// repository was checked out into. Empty means the workspace.
	Dir string
	// Env is every variable the script should see: the step's own,
	// layered over the job's, over the workflow's, over the handful
	// grain supplies itself (see runEnv).
	Env map[string]string
	// Timeout bounds this one command inside the sandbox. Zero means the
	// engine has already bounded it by cancelling the context instead --
	// a backend that can bound the command itself should still do so,
	// since a context deadline only ends the *call*.
	Timeout time.Duration
	// StepID, when non-empty, names this step inside the sandbox so its
	// shell script runs in a detached in-sandbox step runner that spools
	// output to disk and can be reattached across a daemon restart.
	StepID string
}

// StepIDFor returns the deterministic in-sandbox step runner identifier
// for the 1-based attempt and 0-based step index of a CI job.
func StepIDFor(attempt, stepIndex int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return "a" + itoa(attempt) + "-s" + itoa(stepIndex)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// CommandResult is what a command did.
type CommandResult struct {
	// Output is stdout and stderr interleaved, as the sandbox saw them.
	Output string
	// ExitCode is the script's status.
	ExitCode int
}
