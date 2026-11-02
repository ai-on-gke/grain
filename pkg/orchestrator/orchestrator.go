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

// Package orchestrator is v2's equivalent of v1's core.py Orchestrator:
// the component that decides *when* to call GitHub, wired to dispatch.Cycle's
// dispatch decisions and model.Store's state. README.md's "What this
// does not have yet" section named this gap after bwsalmon/agents#243
// ported github.Client and github/githubsim but wired neither into
// anything that runs; this package is that wiring.
//
// It is a library, not a binary — RunCycle is what a deployment's own
// cron/timer loop calls once per tick, the same shape v1's `automation
// run-once` command wraps around core.py's Orchestrator.run_once.
// cmd/graind is that timer loop, calling RunCycle against a real embedded
// SQLite store, a real github.RESTClient, and a real agent.Framework
// (bwsalmon/agents#263, reconciling this package with the parallel
// pkg/orchestrate/cmd/graind bwsalmon/agents#254 built independently of
// it — see README.md's "What this does not have yet" for what that
// merge kept and dropped).
//
// **Sandboxing defaults to "execute on the host," deliberately, for now,
// with a real host adapter available as an opt in.** Deps.Sandboxes is the
// seam: HostSandboxes reuses exactly the stand-in e2e already
// validated, a plain directory on the machine this process runs on, which
// pkg/mcp's sandbox tools confine a run to (NewSandboxTools' own doc
// comment: "root stands in for the sandbox"). It carries no isolation at
// all — an agent given a directory here can do anything this process's own
// user can do. GranuleSandboxes is the real alternative: one
// bwsalmon/granule-managed VM per run, reached over SSH the way
// v1 runs the agent process itself against a real, separate sandbox VM.
// Neither this package nor pkg/granule builds that VM's guest image —
// GranuleSandboxes assumes one already exists that carries the operator's
// SSH key and runs sshd, the same assumption v1's own sandbox provisioning
// stands in for; provisioning it is still open (README.md).
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/console"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// Sandboxes builds the sandbox one run's agent is confined to, and
// destroys it when that run is done. HostSandboxes and GranuleSandboxes
// both implement it; RunCycle only ever calls Acquire and then the
// returned Sandbox's own methods, never anything backend-specific, so
// swapping Deps.Sandboxes between them is the whole change a deployment
// needs to make to move from the local stand-in to a real VM.
//
// It used to hand out tools for a named slot, and a slot's sandbox
// outlived every run dispatched onto it -- which is why isolating one
// task from the next had to be bolted on afterwards, as a recreate
// between runs plus a reset pass at startup to cover the runs a crash
// interrupted. A sandbox that is created for a run and destroyed with it
// gets that for free: there is no previous task's filesystem to inherit
// because there is no previous task.
type Sandboxes interface {
	// Acquire builds a sandbox called name, sized to shape, ready for use
	// by the time it returns. A caller that gets a nil error owns the
	// returned Sandbox and must Release it.
	Acquire(ctx context.Context, name string, shape Shape) (Sandbox, error)
}

// NetworkIsolator is a Sandboxes backend that can actually withhold the
// internet from a run -- model.NetworkNone, honoured rather than refused
// (GranuleSandboxes.enforceGuestNetwork). Nothing here asks it about a
// task somebody filed: a person who asked for an isolation this
// deployment cannot provide is told so, loudly, by
// HostSandboxes.Acquire's refusal.
//
// It exists for the tasks grain files for *itself*, where there is
// nobody to tell. A standing console session ships asking for no network
// (model.DefaultConsoleSessions), which on a host-directory deployment
// is a session whose every exchange fails in setup with nothing said in
// the chat -- grain's own default making grain's own feature unusable.
// reconcileConsoleSessions asks this instead, and files the session with
// the access the deployment can actually give it.
//
// A backend that does not implement this is read as not isolating, which
// is the answer for every backend in this repository but GranuleSandboxes
// -- including the wrappers tests put around HostSandboxes, which is
// exactly the case a type assertion would otherwise get wrong. See
// sandboxesIsolateNetwork.
type NetworkIsolator interface {
	IsolatesNetwork() bool
}

// sandboxesIsolateNetwork reports whether this deployment's sandbox
// backend can withhold the internet from a run.
//
// A nil backend -- a Deps built without one, which dispatches nothing at
// all -- is left as "yes", so a caller with no backend to ask keeps
// whatever was configured rather than having grain quietly widen it.
func sandboxesIsolateNetwork(sandboxes Sandboxes) bool {
	if sandboxes == nil {
		return true
	}
	iso, ok := sandboxes.(NetworkIsolator)
	return ok && iso.IsolatesNetwork()
}

// Shape is how big a sandbox a run asked for -- model.Task's own
// SandboxCPUs/SandboxMemoryMB/SandboxDiskGB (bwsalmon/agents#534,
// grain/task-41), or the zero value for a run content with the
// deployment default (and, where that names nothing either, with
// grain's own DefaultShape: a dimension no one asked for still reaches
// granule as a number).
//
// It is passed to Acquire rather than applied afterwards because a
// sandbox is now built per run: the one moment its size is decided is
// when it is created, so there is nothing to resize. GranuleSandboxes
// used to expose a Reshape for exactly that gap -- a `granule vm
// update` against a long-lived slot VM already sized from the deployment
// default at create time, undone by the next recreate -- and it is gone
// with the gap. Disk is the one dimension that could never have been
// applied afterwards even then: a VM's root disk is a qcow2 overlay
// created with the VM, and growing one under a running guest is not
// something `granule vm update` offers.
type Shape struct {
	CPUs, MemoryMB int
	// DiskGB is the VM's root disk size, in GiB -- the third dimension,
	// added by grain/task-41 alongside the other two rather than as a
	// type of its own so every path that already carries a requested
	// size carries this one too.
	DiskGB int
	// NetworkAccess is how much of the internet the sandbox may reach
	// (model.Task.NetworkAccess, grain/task-269) -- the fourth
	// dimension, and here for the same reason DiskGB is: it is decided
	// when the sandbox is created and it travels the path a requested
	// size already travels, so nothing on that path needs a second
	// argument to carry it.
	//
	// Not a size, and resolved differently at the far end: the three
	// numbers become `granule vm create` flags, while this is applied
	// to the guest once it is up (GranuleSandboxes.enforceGuestNetwork).
	// "" means the same as a zero size -- nobody asked, take the
	// deployment default -- and an unset deployment default is
	// model.NetworkOpen, the network every sandbox already had.
	NetworkAccess model.NetworkAccess
	// OperatorEnv is whether the run may have its operator's ambient
	// credentials: HostSandboxes.WithEnv's entries (-host-sandbox-env --
	// an SSH agent, a gcloud login, a proxy). RunDispatch sets it only
	// for a task whose whole tree of work is this machine's own
	// (localTask), so a run for anybody else never acts with them.
	// Every other backend ignores it, and IsZero does too: it asks for
	// nothing a backend sizes.
	OperatorEnv bool
	// GrainSource is whether the run holds self-debug, and so reads the
	// deployment's baked-in copy of grain's source at grain-src/. A
	// backend that can mount that copy when it creates the sandbox
	// (ContainerSandboxes, per-revision volume) does so, and only then:
	// no other sandbox is given it. Backends that cannot ignore it, and
	// placeGrainSource streams a copy in; IsZero ignores it too.
	GrainSource bool
	// Class is what the sandbox is for -- a person's interactive
	// session, an ordinary agent run, a CI job -- set by whichever of
	// those acquires it. A backend sharing a fixed pool of machine
	// between all three weighs them by it: K8sSandboxes and
	// K8sGranuleSandboxes give each class its own PriorityClass
	// (K8sPriorityClasses), so on a full cluster an interactive session
	// preempts CI rather than waiting behind it. Every other backend
	// ignores it, and IsZero does too: it asks for nothing a backend
	// sizes. "" is SandboxClassAgent.
	Class SandboxClass
	// Life is the sandbox's expected lifetime (a CI job's timeout, a
	// persistent sandbox's refresh TTL). Backends that mint per-sandbox
	// credentials scope their validity to Life, capped at the credential
	// maximum; zero uses the backend's default.
	Life time.Duration
}

// SandboxClass is Shape.Class: which kind of work a sandbox is built for.
type SandboxClass string

const (
	// SandboxClassInteractive is a run of a task a person is talking to
	// as it goes (model.Task.Interactive) -- a Director session, a chat.
	SandboxClassInteractive SandboxClass = "interactive"
	// SandboxClassAgent is an ordinary agent run, a base-branch repair,
	// or a persistent sandbox.
	SandboxClassAgent SandboxClass = "agent"
	// SandboxClassCI is a CI job.
	SandboxClassCI SandboxClass = "ci"
)

// IsZero reports whether a shape asks for nothing in particular, which is
// what a task with no override at all produces.
func (s Shape) IsZero() bool { return s.SizeIsZero() && s.NetworkAccess == "" }

// SizeIsZero reports whether a shape asks for no particular *size*,
// saying nothing about the network -- IsZero's older reading, kept
// separate because the two have different answers on the backend that
// cannot honour either: a host directory has no size to give and no
// network to take away, and the two refusals want to say different
// things (HostSandboxes.Acquire).
func (s Shape) SizeIsZero() bool { return s.CPUs == 0 && s.MemoryMB == 0 && s.DiskGB == 0 }

// orDefault fills each dimension this shape leaves at zero from def --
// how a run that asked for no size of its own (or asked in only one
// dimension) lands on the deployment-wide default its sandbox backend is
// currently carrying (GranuleSandboxes.SetDefaultShape). Per dimension,
// not all-or-nothing: a task that names only CPUs still gets the
// deployment's memory default.
func (s Shape) orDefault(def Shape) Shape {
	if s.CPUs == 0 {
		s.CPUs = def.CPUs
	}
	if s.MemoryMB == 0 {
		s.MemoryMB = def.MemoryMB
	}
	if s.DiskGB == 0 {
		s.DiskGB = def.DiskGB
	}
	if s.NetworkAccess == "" {
		s.NetworkAccess = def.NetworkAccess
	}
	if s.Life == 0 {
		s.Life = def.Life
	}
	return s
}

// AtLeast raises each size dimension of s to at least def's, and fills
// NetworkAccess from def when s leaves it unset.
//
// What a CI workflow writes in `sandbox:` is a floor ("at least this
// much machine to run without starving or OOMing on a small host"), not
// a ceiling that pins the job down when a larger deployment has raised
// its default sandbox shape in Settings.
func (s Shape) AtLeast(def Shape) Shape {
	s.CPUs = max(s.CPUs, def.CPUs)
	s.MemoryMB = max(s.MemoryMB, def.MemoryMB)
	s.DiskGB = max(s.DiskGB, def.DiskGB)
	if s.NetworkAccess == "" {
		s.NetworkAccess = def.NetworkAccess
	}
	s.Life = max(s.Life, def.Life)
	return s
}

// Sandbox is one run's own sandbox: a local directory, container, pod,
// or bwsalmon/granule-managed VM. It lives exactly as long as the run does.
type Sandbox interface {
	// Name is what this sandbox is called -- the string recorded as the
	// run's model.Run.Sandbox, and the identity the git proxy resolves
	// back to that run's task.
	Name() string
	// Tools are the MCP tools the run's agent has its tool calls confined
	// to.
	Tools(ctx context.Context) ([]mcp.Tool, error)
	// ConfigureGitCredentials points this sandbox's git at the proxy,
	// using the bearer token minted for it, and gives it the identity the
	// run's commits are authored under (Config.GitIdentity).
	ConfigureGitCredentials(ctx context.Context, remoteURL, token string, identity mcp.GitIdentity) error
	// Release destroys the sandbox. It is called once the run is done,
	// success or failure alike, and is what makes the isolation between
	// one task and the next a property of the lifecycle rather than
	// something a caller has to remember to do.
	Release(ctx context.Context) error
}

// rootedSandbox is implemented by a Sandbox with a local directory on the
// host this process runs on -- HostSandboxes' own. It is what a task with
// capabilities to place needs (orchestrator.placeAttachments writes into
// it directly), and what a granule VM deliberately does not have: its
// filesystem is inside a guest this process can only reach by exec'ing
// into it.
type rootedSandbox interface {
	Root() (string, error)
}

// envSandbox is implemented by a Sandbox whose agent mcpserver should
// set extra environment on run_command: only a HostSandboxes directory,
// and only what its operator passed by name (HostSandboxes.WithEnv).
// SandboxEnv is the entries themselves, which the run's stored
// transcript is redacted of (recordRunEnd); SandboxEnvFile is the 0600
// file holding them that the mcpserver is pointed at
// (agent.RunConfig.SandboxEnvFile), "" when there are none.
type envSandbox interface {
	SandboxEnv() []string
	SandboxEnvFile() (string, error)
}

// hidingSandbox is implemented by a Sandbox whose run_command must not
// reach some paths on the host it runs on: only a HostSandboxes
// directory, and only on a daemon that asked (HostSandboxes.WithHidden).
// HiddenPaths is the deny-list, handed to the agent's mcpserver as
// agent.RunConfig.SandboxHide.
type hidingSandbox interface {
	HiddenPaths() []string
}

// SandboxPlacer is implemented by a Sandbox that can write a file into
// its own filesystem over whatever transport it already has --
// granuleSandbox's own, over the same runner every tool call and
// ConfigureGitCredentials use. It is rootedSandbox's counterpart for
// capability placements (orchestrator.applyPlacements): a rooted sandbox
// is written into with plain os.WriteFile under its directory, and a
// sandbox that has no directory on this host needs a route of its own or
// its placements have nowhere to land at all (bwsalmon/agents#643 left
// exactly that gap: every deployment running -granule-sandboxes failed
// any task granting gcp-key/gemini-key/github-sandbox during
// preparation, before the agent's first turn).
//
// Exported, unlike rootedSandbox and vmNamedSandbox, because RunDispatch
// takes one: a Sandboxes implementation outside this package can supply
// the route, and a Sandbox that wraps another (e2e's own) must forward
// this method explicitly for it to be seen -- an embedded interface value
// carries no methods outside its own method set.
type SandboxPlacer interface {
	// PlaceFile writes content to path -- always the absolute path the
	// placement means inside the sandbox -- with mode, an octal string
	// (model.Placement.EffectiveMode's own shape). It creates path's
	// parent directory, and must not widen mode at any point, since
	// everything placed this way is credential material.
	PlaceFile(ctx context.Context, path, content, mode string) error
}

// vmNamedSandbox is implemented by a Sandbox reachable only as a named
// bwsalmon/granule-managed VM rather than a local directory --
// granuleSandbox's own, and rootedSandbox's counterpart. It is what
// agent.RunConfig.GranuleVM needs: a Framework with no in-process route to
// a sandbox (agent/claude, which forks a real MCP client as a subprocess
// rather than looping tool calls itself) points its own forked
// "mcpserver -granule-vm" at this name instead of a local -sandbox-root.
type vmNamedSandbox interface {
	VMName() string
}

type containerNamedSandbox interface {
	ContainerName() string
}

// podNamedSandbox is a sandbox that is a Kubernetes pod (K8sSandboxes'
// own): a Framework that forks its own "mcpserver" reaches it with
// `kubectl exec` (agent.RunConfig.K8sPod).
type podNamedSandbox interface {
	KubePod() agent.KubePod
}

// Config is what one deployment's orchestrator needs to know: which repo
// is the task queue, which label marks an issue as ready to dispatch, and
// what a dispatched run is allowed to do on its own.
type Config struct {
	// MaxMergesPerRepo limits successful queue merges per cycle into one
	// base branch of one repository (a merge queue lane, mergelane.go).
	MaxMergesPerRepo int
	// Porter makes a port mechanically, before an agent is asked to
	// (port.go, pkg/portpick). Nil files every port as an agent task,
	// as before.
	Porter Porter
	// TaskRepo, TriggerLabel and DefaultTarget are gone with the poll that
	// needed them: nothing here reads GitHub issues to find tasks any
	// more, so there is no task repo to list, no label to look for, and no
	// default target to apply to an issue body that named none. A task
	// arrives with its Target already set, because whatever wrote it set
	// one (ui.Config.DefaultTarget is where that fallback lives now).

	// Capabilities is the registry RunDispatch resolves and materializes
	// each dispatched task's Grants against before running its agent, and
	// revokes once the run finishes -- ported from pkg/orchestrate's own
	// Config.Capabilities (bwsalmon/agents#254) when that package merged
	// into this one. Nil, or a task with no Grants, skips capability
	// handling entirely rather than erroring, so a deployment or test
	// that grants no capabilities needs to configure none of this.
	Capabilities *model.CapabilityRegistry
	// CapabilityPorts is the relay a capability grant's ports are
	// forwarded through (pkg/capability/portforward), served on the
	// sandbox fabric at CapabilityPortsURL. Nil refuses every run whose
	// grants return ports.
	CapabilityPorts    *portforward.Relay
	CapabilityPortsURL string
	// CapabilitySandboxServers is where a run's sandbox-callable
	// capability tools are registered for the sandbox proxy's /cap/
	// (grain cap, design/spec/capabilities/capabilities.spec.md §5.2.6). Nil reaches none of
	// them from the sandbox; model-only tools are unaffected.
	CapabilitySandboxServers *gitproxy.CapRegistry
	// CapabilitySetupTimeout bounds each capability setup command
	// (design/spec/capabilities/capabilities.spec.md §5.5.3); zero is
	// DefaultCapabilitySetupTimeout.
	CapabilitySetupTimeout time.Duration
	// PreparedCapabilities are finite resources reserved before dispatch
	// recorded this run. runOne sets this on its private Config copy; callers
	// that invoke RunDispatch directly leave it empty and retain the ordinary
	// materialization path.
	PreparedCapabilities []model.Materialized
	// sandboxPod is the Kubernetes pod this run's sandbox is, when it is
	// one (podNamedSandbox): set per run, like PreparedCapabilities, on
	// the copy of Config one dispatch is handed.
	sandboxPod agent.KubePod
	// sandboxEnv is the environment this run's sandbox asks its agent
	// mcpserver to add (envSandbox), set per run like sandboxPod: its
	// values are what the run's stored transcript is redacted of, and
	// sandboxEnvFile is the file that hands them to the mcpserver.
	sandboxEnv     []string
	sandboxEnvFile string
	// sandboxHide is the paths this run's sandbox asks its agent
	// mcpserver's run_command to hide (hidingSandbox), set per run like
	// sandboxEnv.
	sandboxHide []string
	// sandboxOOM is this run's sandbox when it can say whether the
	// kernel OOM-killed it (SandboxOOMReporter), nil otherwise, and
	// sandboxDefaultMemoryMB the memory its backend gives a run that asks
	// for none (0 when unknown) -- both set per run like sandboxPod, and
	// read by applySandboxOOM once the agent has stopped.
	sandboxOOM             SandboxOOMReporter
	sandboxDefaultMemoryMB int
	// NoteOOM is told of every sandbox grain finds OOM-killed -- the
	// daemon's model.OvercommitController.NoteOOM, so auto overcommit
	// backs off. Nil where there is no controller.
	NoteOOM func(time.Time)
	// capabilityMCPServers and capabilityPromptSections are what this
	// run's capability pass gives its agent (wireEndpoints): the MCP
	// servers its CLI is configured with beside grain-sandbox, and the
	// attributed prompt blocks appended to its prompt. Set per run like
	// sandboxPod.
	capabilityMCPServers     []agent.CapabilityMCPServer
	capabilityPromptSections string
	// Credentials resolves the named credentials a capability provider
	// asks for by name, e.g. gcpkey's minter service account.
	Credentials model.CredentialResolver
	// MaxAgentTurns caps model/tool round trips per run; 0 leaves the
	// agent framework's own default in place, which for both frameworks
	// is no cap at all (agent/claude's defaultMaxTurns has why). What
	// actually bounds a runaway run is MaxRunRuntime below.
	//
	// Whatever a caller sets here is only the starting value on a
	// deployment with a store: RunCycle re-reads model.Config.
	// MaxAgentTurns out of grain_config every cycle, alongside
	// Deps.MaxWorkers/MaxMergers, so a change made in Settings reaches the next
	// run dispatched rather than the next restart.
	MaxAgentTurns int
	// PromptExtension is this deployment's own standing instructions for
	// every run it dispatches -- model.Config.PromptExtension, and
	// model/prompt_extension.go for what the three layers of it are.
	// Empty adds nothing to any prompt, which is what a caller that has
	// never set one gets.
	//
	// Only the deployment-wide layer lives here. The repo's own is read
	// per dispatch, from the task's target (RunDispatch), because it is
	// keyed by something only a task names; the task's own is on the task
	// already. Whatever a caller sets here is only the starting value on
	// a deployment with a store, exactly like MaxAgentTurns above:
	// RunCycle re-reads it out of grain_config every cycle, so an edit in
	// Settings reaches the next run dispatched rather than the next
	// restart.
	PromptExtension string
	// TimeZone is this deployment's own clock, model.Config.TimeZone --
	// "" for one that never set it. It is the zone a run's prompt tells
	// the time in for a task that names none of its own (RunDispatch);
	// a task filed or messaged from the browser carries that browser's
	// zone instead. Re-read every cycle like PromptExtension above.
	TimeZone string
	// GitRemoteBase is the base URL of this deployment's git proxy
	// (pkg/cli/daemon.go's startGitProxy), which RunDispatch turns into
	// a task's own clone URL to prepare its sandbox's checkout with --
	// see prepareCheckout. Empty skips that preparation entirely and
	// leaves the sandbox as it always was, an empty directory the agent
	// has to populate itself: the default for a test, and for a
	// deployment running no proxy.
	GitRemoteBase string
	// CancelPollInterval is how often RunDispatch re-reads its task's
	// state from store while a run is live, to notice the task being
	// closed out from under it -- bwsalmon/agents#346's own store-polled
	// cancellation signal, needed because grain daemon (running the run)
	// and grain ui (where a close actually lands) are separate processes
	// sharing only the store. 2 seconds if zero; a test that wants to
	// prove cancellation happens promptly, without waiting seconds for
	// real, sets this to something much smaller.
	CancelPollInterval time.Duration
	// MaxRunRuntime bounds how long RunDispatch lets one run's
	// framework.Run call stay live before cancelling it outright --
	// v2's own equivalent of v1's AutomationConfig.max_runtime_minutes
	// plus its sweeper (bwsalmon/agents#575), both aimed at the same
	// failure: a run that is alive but stuck making no progress, tying
	// up its share of the concurrency limit indefinitely with nothing to notice or
	// recover it. A tool call with no bound of its own is exactly how
	// that happens in practice (a run_command whose own caller omitted
	// "timeout" -- see mcp.defaultRunCommandTimeout, the fix for the
	// same issue's other half), but this is the backstop for any way a
	// framework can end up wedged, not only that one.
	//
	// v1 needed a whole separate sweeper process for this because its
	// controller launches each run as an external unit, polled between
	// cron invocations, with no long-lived supervisor of its own to hand
	// a deadline to. RunDispatch already is that supervisor -- one
	// goroutine, alive for exactly as long as the run is -- so a
	// deadline on the very ctx framework.Run receives is the whole
	// mechanism; see RunDispatch's own use of context.WithTimeoutCause.
	// It reaches an already-live tool call the same way task-closed
	// cancellation does (bwsalmon/agents#346's "actually terminate a
	// running task's sandbox process on cancel"), through
	// exec.CommandContext/procgroup.
	//
	// Zero uses DefaultMaxRunRuntime. There is no "uncapped" value the
	// way MaxAgentTurns' own zero means "no turn cap": an uncapped run
	// is exactly the gap this field exists to close -- all the more so
	// now that it is the only ceiling a default deployment has -- so a
	// deployment that really wants a longer one sets it explicitly
	// instead of switching this off.
	MaxRunRuntime time.Duration
	// MaxDurableRuns is how many runs of durable tasks (model.Task.IsDurable,
	// design/design/durable-tasks.design.md#DT-1) may be live at once. They are
	// admitted outside every worker and merger count, as every task with
	// priority dispatch is, so this is the one count that holds them:
	// a safety ceiling, not a queue (model.Limits.Durable). Zero uses
	// DefaultMaxDurableRuns; negative turns the ceiling off.
	MaxDurableRuns int
	// DurableIdleTimeout is how long a durable run may go with no
	// transcript output, no tool call starting or finishing, and no
	// declared wait in progress before the idle watchdog suspends it
	// (model.SuspendIdle) to be resumed on the next cycle. A durable run
	// has no deadline (maxRunRuntime), so this is what catches a wedged
	// one. Zero uses DefaultDurableIdleTimeout.
	DurableIdleTimeout time.Duration
	// durable is set by durableDispatch for the run of a durable task:
	// maxRunRuntime is then 0, no deadline at all.
	durable bool
	// renewLeases, on a durable run, renews its capability pass to until
	// (CapabilityPasses.Extend); its idle watchdog calls it.
	renewLeases func(ctx context.Context, until time.Time) error

	// Consoles is where a console task's session is registered while its
	// run lasts, so that pkg/ui can find the terminal a person is asking
	// to type into (pkg/console's own doc comment on why the two ends are
	// so far apart). nil -- a deployment serving no UI, and every test
	// that wired none -- is what makes a task holding the console
	// capability fail its dispatch with a sentence saying so, rather than
	// silently running unattended (consoleDispatch).
	Consoles *console.Manager

	// MaxConsoleRuntime is MaxRunRuntime for a console run: how long a
	// session left open is allowed to last before grain ends it anyway.
	// Zero means DefaultMaxConsoleRuntime, and that constant's own doc
	// comment says why the two numbers are so far apart.
	MaxConsoleRuntime time.Duration

	// MaxInteractiveRuntime is MaxRunRuntime for a run holding a
	// conversation -- a task filed as Interactive, whose agent has the
	// chat tool and spends much of its life blocked in it waiting for a
	// person to answer. Zero means DefaultMaxInteractiveRuntime, and that
	// constant's own doc comment says why waiting is not charged against
	// the budget for working.
	MaxInteractiveRuntime time.Duration

	// Now reads the current time, and exists for the timestamps
	// RunDispatch cannot take from its caller: when a run's agent got its
	// first turn, and when the run finished. Every other moment this
	// package records is the `at` RunCycle hands down -- the moment the
	// cycle began -- and those two are genuinely later than that, by
	// however long setup and then the agent took. Stamping finished_at
	// with `at` too made every run ever recorded read back as zero
	// seconds long; the agent's own start is the line pkg/metrics splits
	// that duration at (Store.SetRunAgentStarted).
	//
	// nil is the wall clock, which is what a real deployment wants. A
	// test driving this package off a fake clock sets it so a run's
	// finish lands on the same timeline as the `at` it dispatched with;
	// dispatch's own retry backoff compares the two.
	Now func() time.Time
	// TranscriptDir, if set, is a directory RunDispatch asks each run's
	// agent.Framework to mirror its own transcript-in-progress into, one
	// file per run named after d.RunID -- agent.RunConfig.TranscriptPath's
	// own doc comment says what a Framework does with it. A deployment
	// wiring this up also wires the very same directory into
	// ui.Config.LiveTranscripts (e.g. claude.LiveTranscriptDir) so the two
	// sides agree on where one run's file is; RunDispatch itself never
	// reads it back, only writes to and then removes it. "" means no
	// caller wants this, and RunDispatch leaves agent.RunConfig's own
	// TranscriptPath empty (bwsalmon/agents#467).
	TranscriptDir string
	// GrantTools maps a capability name to a function building the extra
	// MCP tools a task holding that grant gets added to its own
	// dispatched run -- runOne's own hook for a capability whose effect
	// is which tools a run has, rather than only what Materialize's own
	// Placements put in the sandbox or what PromptSection adds to the
	// prompt (model.CapabilityProvider's four-moment contract has no
	// room for either). selfdebug's read-only source tools and
	// selfrepair's confirmation-gated host command tool
	// (bwsalmon/agents#540) are the two capabilities that need this
	// today.
	//
	// Kept as a caller-supplied map rather than a fifth method on
	// CapabilityProvider deliberately: model/capability.go's own doc
	// comment already explains why a provider is handed no Runner, and a
	// method returning live mcp.Tools would be exactly the kind of
	// convention that comment says a structural contract should avoid.
	// A deployment that wants a grant to add tools wires it here
	// instead, entirely outside that package.
	//
	// runOne only ever consults this for an Interactive task
	// (model.Task.Interactive) -- every capability it is meant for today
	// assumes a human is actually watching the task's own chat closely
	// enough to answer a tool's own confirmation prompt (selfrepair.
	// Confirm) within its timeout, which is not a safe assumption for an
	// unattended dispatch. nil, or a capability with no entry, adds no
	// tools.
	//
	// These tools reach no agent at present. They travel as
	// agent.RunConfig.Tools, whose only consumer was the in-process
	// Gemini runtime agent/antigravity replaced; every Framework left
	// forks a CLI that manages its own MCP connection and cannot be
	// handed an in-process registry. See selfrepair.Confirm's own doc
	// comment for what closing that gap would take.
	//
	// self-debug and bootstrap-playbooks are the capabilities that no
	// longer depend on this, and it is worth reading why: their tools are
	// read-only, so they need no route back to a live run's own
	// conversation, and a forked "grain mcpserver" can therefore build
	// them for itself out of an argument (agent.GrantArgs) plus, for
	// self-debug's task half, a REST client of the daemon. What travels
	// is the name of the grant, not the tools --
	// agent.RunConfig.Grants, set by RunDispatch. selfrepair cannot
	// follow them: its one tool blocks on a human reply in the task's own
	// chat, which is a store handle that process deliberately lacks.
	GrantTools map[string]func(store *model.Store, taskID string) []mcp.Tool
	// PackageCache answers where a sandbox reaches this deployment's
	// apt, PyPI and Docker Hub cache (pkg/pkgcache) -- "http://host:port" -- or ""
	// when it is switched off, when this process runs none, or when the
	// listener did not bind. Every dispatch asks, so the switch in
	// Settings reaches the next run rather than the next restart
	// (model.Config.PackageCacheEnabled).
	//
	// A function rather than a string for exactly that reason: it is a
	// live setting, and a URL captured when this Config was built would
	// be the answer this deployment gave at startup forever.
	//
	// nil is a deployment that never points a sandbox at a cache, which
	// is every caller of this package outside cmd/grain today.
	PackageCache func(ctx context.Context) string
	// PackageCacheCA returns the PEM encoded root CA certificate for the transparent package cache.
	PackageCacheCA func(ctx context.Context) []byte
	// PackageCacheDNS returns the controller DNS IP for transparent sandbox DNS interception.
	PackageCacheDNS func(ctx context.Context) string
	// SandboxGrainBinary is this deployment's own build of the sandbox's
	// grain (cmd/grain-sandbox) on the controller's disk, which setup
	// places at SandboxGrainPath in every sandbox that does not already
	// carry it (placeSandboxGrain says why). cmd/grain finds it beside its
	// own executable (SandboxGrainBinaryBeside); "" places nothing, and
	// runs use whatever grain their image has.
	SandboxGrainBinary string
	// GrainSourceDir is the checkout of grain's own source a self-debug
	// run may read -- cmd/grain's own sourceDir (the copy baked into the
	// deployment image, or -upgrade-src-dir's checkout), passed on as
	// agent.RunConfig.GrainSourceDir for the tasks that hold the grant
	// and to nobody else.
	//
	// "" is a deployment with no such checkout: the run still gets the
	// tools, and they answer that there is no source here to read rather
	// than vanishing from its roster (selfdebug.SourceTools).
	GrainSourceDir string
	// GrainSecretsDir is the directory holding this deployment's own
	// credentials -- cmd/grain's secretsDir -- passed on as
	// agent.RunConfig.GrainSecretsDir for the tasks that hold the
	// grain-log grant and to nobody else.
	//
	// It is not something a run is given. It travels so that the
	// mcpserver a run's Framework forks, which is a process on this
	// controller and not in any sandbox, can read those values and
	// remove them from the journal lines read_grain_log returns
	// (pkg/capability/grainlog). "" leaves that tool refusing every
	// call, which is the safe direction: a journal nobody could redact
	// is one nobody should be reading.
	GrainSecretsDir string
	// GrainStateDir is the controller-local synchronized state repository used by deployment-health.
	GrainStateDir string
	// LocalScripts is where this host finds local-scripts declarations
	// (pkg/capability/localscripts): a run whose task holds
	// local-scripts:<name> is told the declaration's path and handed its
	// credentials in a 0600 file (agent.RunConfig.LocalScripts).
	LocalScripts localscripts.Config
	// SandboxGuestImage is the reference this deployment's sandboxes are
	// really created from -- cmd/grain's chooseSandboxImage, the same
	// value ui.SandboxImage.Image reports.
	//
	// One reconciler reads it, and only to answer one question: which
	// repository publishes the guest this deployment is running on
	// (model.GuestImageRepo). That repository's release workflow holds a
	// build input every CI job here depends on, so grain keeps filling
	// the content-addressed guest cache after its releases are switched
	// off rather than making an operator name the job
	// (model.GuestCacheFollowsBaseBranch, reconcileBaseBranchDispatch).
	//
	// "" is a deployment with no image to attribute -- the
	// host-directory sandbox backend, a test, `grain demo` -- and
	// attributes the guest to no repository, so nothing is dispatched
	// that an operator did not name.
	SandboxGuestImage string
	// SandboxRecreations, when non-nil, is the registry each dispatched
	// run parks itself in so that it can later ask for its own sandbox to
	// be destroyed and rebuilt -- the daemon-side half of pkg/mcp's
	// recreate_sandbox tool. See SandboxRecreations' own doc comment for
	// why the request has to come back to the daemon at all rather than
	// being something the run does inside its sandbox.
	//
	// nil registers nothing and answers every request with "no live run",
	// which is the honest answer for a caller (a test, a one-shot cycle,
	// `grain demo`) that has no UI/API route for such a request to arrive
	// over in the first place.
	SandboxRecreations *SandboxRecreations
	// RunWaitCredits, when non-nil, is the registry each dispatched run
	// parks its own wall-clock timer in, so that time it later spends
	// blocked on CI can be credited back to its deadline while it is
	// still blocked -- the daemon-side half of pkg/mcp's wait_for_checks
	// credit. See RunWaitCredits and RunWaitCreditCap.
	//
	// nil registers nothing and answers every report with "no live run",
	// so every run is bounded by MaxRunRuntime alone. That is the honest
	// arrangement for a caller with no UI/API route for a report to
	// arrive over (a test, a one-shot cycle, `grain demo`): a deadline
	// moved for a report nobody can send would be a promise made to
	// nobody -- and the prompt's own budget paragraph would then overstate
	// what such a run may spend (runtimeSection).
	RunWaitCredits *RunWaitCredits
	// TaskCancellers, when non-nil, is the registry of live runs that
	// can be cancelled on request (for example, when a user clicks Stop
	// in the UI).
	TaskCancellers *TaskCancellers
	// Preemptions and CIPreempter are what a base-branch repair the
	// budget's reserve cannot hold may stop to make room (preempter,
	// grain/task-488): Preemptions this process's record of what it has
	// already stopped, CIPreempter the deployment's grain CI. A nil
	// Preemptions pre-empts nothing; a nil CIPreempter leaves agent runs
	// as the only thing a repair may stop.
	Preemptions *Preemptions
	CIPreempter CIPreempter
	// CapabilityRestarts is this process's record of the live runs told
	// their task's grants changed (reconcileCapabilityChanges). A nil one
	// restarts nothing, and a grant change reaches only the next run.
	CapabilityRestarts *CapabilityRestarts
	// Pause, when non-nil, is the gate a run that meets its agent's own
	// usage limit closes: every run in flight on the same agent framework
	// is cancelled and no further task of that framework is dispatched
	// until the provider's window resets. See Pause for why that is the
	// right response to a limit rather than letting each task discover it
	// in turn, and why it covers one framework rather than the whole
	// deployment.
	//
	// Both halves of it are read from here -- RunDispatch registers each
	// run's own cancellation with it and reports a limit to it,
	// reconcileDispatch asks it which frameworks it may dispatch --
	// because Config is what both already have in hand, the same way
	// SandboxRecreations above is reached from two places.
	//
	// nil means a usage limit is reported on the run that met it (its
	// outcome is model.PausedOutcome either way) and pauses nothing,
	// which is what a caller with no loop to pause wants: a test, a
	// one-shot cycle. A deployment always sets one -- without it the
	// next tick dispatches the next task straight into the same refusal.
	Pause *Pause
	// AgentFramework is this deployment's default agent framework
	// (model.Config.AgentFramework): the one a task that named none of
	// its own is dispatched onto. cmd/grain seeds it from
	// -agent-framework and RunCycle overwrites its own copy from the
	// store's grain_config row each tick, exactly as it does MaxWorkers,
	// so choosing another framework in Settings takes effect on the next
	// tick rather than at a restart.
	//
	// This package builds no framework from it -- that is Deps.Framework's
	// job, and its factory resolves the same default itself. What it
	// needs the name for is the pause above: which framework's credential
	// a run is spending, and so which runs a limit covers (frameworkFor).
	//
	// "" reads as model.NormalizeAgentFramework's own default, which is
	// what a caller that has not configured a framework at all is
	// running.
	AgentFramework string
	// Verdicts, when set, is somewhere other than the forge that already
	// knows how a commit's checks ended -- this deployment's own
	// reporting service (pkg/checkpost), which CI posts to at the end of
	// a build instead of grain asking GitHub every thirty seconds.
	//
	// nil is every deployment without one, and leaves the merge queue
	// reading checks exactly as it always did. It can only ever save a
	// GitHub read: an answer that is not the whole answer for a commit --
	// a build still running, a service that is down, a repository that
	// reports nothing -- falls through to GitHub, which is what makes
	// this safe to have on. See forge.Verdicts.
	Verdicts forge.Verdicts
	// StateRepo answers which repository this deployment keeps its own
	// settings in -- the state repository grain loads its configuration
	// out of and exports its database back into (pkg/staterepo) -- so
	// that every run dispatched can be told, in as many words, whether
	// the checkout it has been handed is that repository or not. See
	// settingsRepoSection in run.go for what is said and why nothing in
	// a sandbox could work it out instead.
	//
	// A function rather than a value because the answer changes under a
	// running daemon: adopting a different repository, from the Settings
	// pane's State tab or from `grain state adopt`, swaps it out
	// mid-process (cmd/grain's stateManager), and a value snapshotted
	// when the daemon started would have every run dispatched afterwards
	// naming the repository this deployment used to run on. That is a
	// worse failure than naming none, since a run has no way to check it.
	//
	// nil, or a function returning the zero RepoRef, says nothing to any
	// run. Both are honest answers rather than a gap: a deployment whose
	// state is local-only has no repository to name -- its settings are
	// not something a pull request can reach at all -- and a caller with
	// no state repository of its own (`grain demo`, a test, anything
	// embedding this package) has nothing to say either.
	StateRepo func() model.RepoRef
	// StateRepoCheck names the check run this deployment posts itself on
	// pull requests against StateRepo -- staterepo.PostedCheckName, on a
	// deployment configured to post one ("postCheck" in state-repo.json,
	// pkg/staterepo/postcheck.go) -- and is empty on every other, which
	// is almost all of them.
	//
	// What naming it does is make the merge queue refuse to call a pull
	// request against that repository clean without it. That rule is the
	// half of the posted check that keeps the gate closed, and without it
	// the other half is a regression rather than a feature: a missing
	// check is not a blocked pull request, it is a clean one (healthFrom,
	// on an empty check list whose registration window has settled), so a
	// deployment that posts its own check and is then down for the whole
	// life of a settings pull request would merge that pull request
	// unvalidated. Named rather than inferred, because "grain was down"
	// and "grain has nothing to say" have to look different from here and
	// only the deployment itself knows which it is.
	//
	// A function for the same reason StateRepo is one, and read with it:
	// the two are one answer -- this repository, this check -- and an
	// adopt changes both under a running daemon.
	StateRepoCheck func() string
	// StateRepoMerged, when set, is called after the merge queue merges a
	// pull request into StateRepo: this deployment's own settings have
	// just changed on the remote, and the sooner it takes that commit up
	// the smaller the window in which it exports the settings it had over
	// the settings it was given (cmd/grain's stateManager nudges its sync
	// loop). It must not block -- it is called on the reconciler's own
	// goroutine, between one queue entry and the next.
	StateRepoMerged func()
	// CheckImage is the container image a run dispatched into a state
	// repository is told to run `grain state check` out of -- the same
	// image grain writes into that repository's own CI step
	// (staterepo.Config.CheckImage, resolved by cmd/grain from the
	// operator's state-repo.json or from the build this deployment is
	// itself running).
	//
	// It is here because there is no `grain` binary in a sandbox, and
	// nothing in one could work out which image to run instead: grain
	// publishes one image per build and no bare binaries, and `grain
	// state check` refuses a dump stamped with a schema it does not know
	// -- so a run told to check its own change has to be told the exact
	// image this deployment runs, or it is told to do something that
	// either does not work or answers about the wrong build. See
	// stateRepoSection in run.go.
	//
	// A function rather than a value for the same reason StateRepo above
	// is one: an adopt swaps the settings that name it out from under a
	// running daemon, and a value snapshotted at startup would go on
	// naming the image the deployment used to check with.
	//
	// nil, or a function returning "", reads as
	// staterepo.DefaultCheckImage -- the tag CI keeps pointed at main,
	// which is exactly the fallback an unstamped build already gives
	// that repository's CI step (pkg/cli/grainimage.go). A moving tag
	// that resolves to something real beats naming nothing.
	CheckImage func() string
	// UpgradeStatus, when set, reports this deployment's last (or
	// current) in-place upgrade -- (*upgrade.Upgrader).Status, in a
	// deployment, reading the status file that upgrade persists across
	// the very restart it performs.
	//
	// It is what makes the upgrade check possible at all
	// (reconcileUpgradeCheck): the process that performed the upgrade is
	// not this one, so there is no event to have been handed, and this is
	// the only thing that can tell a daemon that has just started that it
	// started because it was upgraded.
	//
	// nil means no check is ever filed, which is what a caller with no
	// upgrader has: `grain demo`, a test, a deployment configured with
	// neither -upgrade-image nor -upgrade-src-dir. A function rather than
	// a value for the same reason StateRepo above is one -- it is read
	// per cycle, and the answer changes as upgrades happen.
	UpgradeStatus func() (upgrade.Status, error)
	// ProcessStartedAt is when this daemon process started, so that the
	// upgrade check can tell the process an upgrade is about to restart
	// away from the one that restart brought up
	// (upgrade.Status.RestartPending). Zero cannot tell the two apart
	// once the restart has begun.
	ProcessStartedAt time.Time
	// RestartGap, when set, reports how long the restart that started
	// this process left each of its listeners unreachable
	// (upgrade.GapRecorder), for the upgrade check to report beside the
	// upgrade it checks. nil, or a gap with no services, says nothing.
	RestartGap func() upgrade.RestartGap
	// Adoption, when set, reports what this process's startup adoption
	// pass did with the runs a previous daemon left in run hosts, for
	// the upgrade check to report how many came through the restart and
	// why any did not. nil, or a nil report, says nothing.
	Adoption func() *AdoptionReport
	// UpgradeInterrupts, when set, is by framework why the run location
	// this install has for that CLI loses its runs on a restart
	// (daemon.CLIRunResolution.UpgradeInterrupts, what the upgrade pane
	// shows), for the upgrade check to tell a lost run the install was
	// set up to lose from a regression (AdoptionReport.Expected). nil
	// says every lost run is a regression.
	UpgradeInterrupts func(context.Context) map[string]string
	// StartUpgrade, when set, kicks an upgrade of this deployment off --
	// (*upgrade.Upgrader).Start, in a deployment, the same call POST
	// /api/upgrade makes and against the same Upgrader, so that "one
	// upgrade at a time" still means one.
	//
	// It is what lets an upgrade be scheduled rather than performed on
	// the spot (reconcileScheduledUpgrade, grain/task-156): the operator
	// says which branch, grain holds the queue until the deployment is
	// idle, and this is the call it makes when it is. Nothing else in
	// this package starts an upgrade -- an upgrade is otherwise always
	// somebody pressing a button, and this reconciler is the one place
	// where the press and the upgrade are separated in time.
	//
	// nil means a scheduled upgrade is never applied, which is what a
	// caller with no upgrader has -- the same set UpgradeStatus above is
	// nil for. The API refuses to schedule one on such a deployment for
	// exactly that reason, so a nil here is not a schedule left waiting
	// forever.
	StartUpgrade func(branch string) error
	// UpgradeUnchanged, when set, says cheaply whether an upgrade onto
	// branch would find the build already running --
	// (*upgrade.Upgrader).AlreadyRunning, in a deployment, which asks the
	// registry for the branch's digest or origin for its commit and
	// pulls nothing.
	//
	// The daily automatic upgrade asks it before holding the queue
	// (reconcileAutoUpgrade): a channel with nothing new on it is a day
	// done, not a drain that staging would only then find was for
	// nothing. Only a true with no error skips the schedule; nil, an
	// error, or an answer slower than autoUpgradeProbeTimeout schedules
	// the upgrade as if it had not been asked.
	UpgradeUnchanged func(ctx context.Context, branch string) (bool, error)
	// UpgradeDrainWindow, when non-zero, bounds how long a scheduled upgrade
	// will wait for in-flight tasks and CI jobs to complete before proceeding
	// with the upgrade. If the draining window elapses and runs are still active,
	// the upgrade proceeds, interrupting active runs non-destructively so they can
	// resume on the upgraded daemon.
	UpgradeDrainWindow time.Duration
	// SessionSandboxIdleTTL bounds how long an interactive session task's
	// sandbox (and in-process agent session) stays warm awaiting a reply
	// after a turn ends before being released. Zero reads from
	// grain_config.session_sandbox_idle_ttl_ms (defaulting to
	// model.DefaultSessionSandboxIdleTTL, 30m); a negative value turns warm
	// retention off.
	SessionSandboxIdleTTL time.Duration
	// WaitForSessionReply keeps an interactive session's run alive in-process
	// waiting for the next human message (up to SessionSandboxIdleTTL) when
	// the agent ends a turn with an answer and a resumable session, even when
	// Deps.Runs is nil. When Deps.Runs is non-nil (an asynchronous daemon loop),
	// in-process session continuation is enabled automatically.
	WaitForSessionReply bool

	// sessionIdleTTLStored says SessionSandboxIdleTTL was read from the
	// stored setting at dispatch rather than pinned by the caller, so an
	// in-process idle wait follows that setting while it waits
	// (Config.liveSessionIdleTTL).
	sessionIdleTTLStored bool

	sandboxReused        bool
	sandboxReclaimed     bool
	inProcessSessionWait bool
	sessionSandboxName   string
	sessionShape         model.Commitment
	sessionLedger        *model.Ledger
	// sessionReach and sessionClient are Deps.GitReach and Deps.Client,
	// for the proposals continueSessionTurns relays mid-run: they get the
	// same reach and fork-head checks processResult gives the rest.
	sessionReach  *gitproxy.Reach
	sessionClient github.Client
}

func (c Config) cancelPollInterval() time.Duration {
	if c.CancelPollInterval > 0 {
		return c.CancelPollInterval
	}
	return 2 * time.Second
}

// DefaultMaxRunRuntime is Config.maxRunRuntime's fallback -- the same
// 120-minute value v1's own AutomationConfig.max_runtime_minutes
// defaulted to, so a deployment moving from v1 gets back the ceiling it
// already had rather than a new, differently-tuned one.
//
// Exported because a run is now told this number (BuildPrompt's
// wall-clock paragraph), which makes it a fact about grain rather than a
// private tuning knob: anything that has to state the budget without a
// live Config in hand -- `grain demo`'s seeded prompt, a test -- names
// this rather than writing "two hours" out again somewhere it can go
// stale.
const DefaultMaxRunRuntime = 120 * time.Minute

// DefaultMaxDurableRuns is Config.maxDurableRuns' fallback.
const DefaultMaxDurableRuns = 4

// DefaultDurableIdleTimeout is Config.durableIdleTimeout's fallback.
const DefaultDurableIdleTimeout = 60 * time.Minute

// maxDurableRuns is model.Limits.Durable: 0 for no ceiling.
func (c Config) maxDurableRuns() int {
	switch {
	case c.MaxDurableRuns > 0:
		return c.MaxDurableRuns
	case c.MaxDurableRuns < 0:
		return 0
	}
	return DefaultMaxDurableRuns
}

func (c Config) durableIdleTimeout() time.Duration {
	if c.DurableIdleTimeout > 0 {
		return c.DurableIdleTimeout
	}
	return DefaultDurableIdleTimeout
}

// maxRunRuntime is how long a run may work before its clock cancels it,
// and 0 for a durable run (durableDispatch), which has no deadline: every
// caller reads 0 as none (newRunClock, capabilityDeadline, the prompt).
func (c Config) maxRunRuntime() time.Duration {
	if c.durable {
		return 0
	}
	if c.MaxRunRuntime > 0 {
		return c.MaxRunRuntime
	}
	return DefaultMaxRunRuntime
}

// settingsRepo reads Config.StateRepo, or the zero RepoRef when a caller
// wired none -- and also when the function it wired answers with one, so
// that "no state repository" and "nobody asked" reach the prompt as the
// same silence (settingsRepoSection).
func (c Config) settingsRepo() model.RepoRef {
	if c.StateRepo == nil {
		return model.RepoRef{}
	}
	return c.StateRepo()
}

// stateRepoGate gathers what the merge queue has to know about this
// deployment's own state repository: which one it is, which check it
// posts there itself, and who to tell when one of its pull requests
// merges. A caller that wired none of the three gets the zero gate,
// which changes nothing about how any pull request is judged.
func (c Config) stateRepoGate() StateRepoGate {
	gate := StateRepoGate{Repo: c.settingsRepo(), Merged: c.StateRepoMerged, Porter: c.Porter}
	if c.StateRepoCheck != nil {
		gate.Check = c.StateRepoCheck()
	}
	return gate
}

// checkImage reads Config.CheckImage, or staterepo.DefaultCheckImage
// when a caller wired none -- and also when the function it wired
// answers with "", so that "no image configured" and "nobody asked"
// reach the prompt as the same published tag rather than as a command
// with a hole in it (stateRepoSection).
func (c Config) checkImage() string {
	if c.CheckImage == nil {
		return staterepo.DefaultCheckImage
	}
	if image := c.CheckImage(); image != "" {
		return image
	}
	return staterepo.DefaultCheckImage
}

// now reads Config.Now, or the wall clock when a caller set none.
func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// HostSandboxes hands out one directory per run, on the host this process
// itself runs on. A directory is created by Acquire and removed by
// Release, so nothing of one task's run survives into the next.
//
// It isolates nothing. A daemon builds it only when asked by name
// (-host-sandboxes), and refuses that on a daemon others can reach
// (pkg/daemon's CheckHostSandboxes): it is for a single-user workstation
// whose agents are meant to use the host's own tools. It is also the
// in-process stand-in tests, `grain demo` and the UI e2e servers dispatch
// into.
//
// That is a change of substance for this backend, not just of shape. A
// HostSandboxes directory used to be per *slot* and deliberately
// long-lived, resetting one between tasks being "the caller's job" -- and
// since no caller did, sequential tasks on one slot genuinely shared a
// filesystem. They no longer can.
type HostSandboxes struct {
	baseDir string
	// env is WithEnv's: NAME=VALUE entries the run_command of a run
	// acquired with Shape.OperatorEnv sets on top of mcp's allowlist.
	env []string
	// hide is WithHidden's: paths no run's run_command may reach.
	hide []string

	mu       sync.Mutex
	live     map[string]*hostSandbox
	building map[string]bool
}

// NewHostSandboxes returns a HostSandboxes rooted at baseDir, which must
// already exist.
//
// A relative baseDir is made absolute here, against this process's
// working directory, because a sandbox's root is also the HOME every
// command in it runs with (mcp's sandboxEnv): a relative HOME is resolved
// against wherever the command has cd'd to, so git looked for
// ~/.git-credentials under the checkout and every clone failed asking
// for a username. A daemon started with `-sandbox-dir d/sb` did exactly
// that (docs/test-runs/2026-10-15-corp-research-capability.md).
func NewHostSandboxes(baseDir string) *HostSandboxes {
	if abs, err := filepath.Abs(baseDir); err == nil {
		baseDir = abs
	}
	return &HostSandboxes{baseDir: baseDir, live: map[string]*hostSandbox{}, building: map[string]bool{}}
}

// WithEnv sets the NAME=VALUE entries (mcp.ValidateSandboxEnv's form)
// a run's run_command sets on top of mcp's allowlist -- the daemon's
// -host-sandbox-env -- and returns h. Only a run acquired with
// Shape.OperatorEnv gets them: they carry the operator's own identity,
// so a task that is not this machine's own runs without them (GSA1-05).
// They reach a run two ways: through
// Tools, for the commands this process runs in the sandbox itself
// (checkout, the setup script), and through SandboxEnv, which RunDispatch
// hands the Framework as agent.RunConfig.SandboxEnv for the agent's own
// mcpserver. Only this backend has the method: a VM or a container runs
// its commands in a guest whose environment is its own, and the
// operator's sockets and paths would name nothing there.
func (h *HostSandboxes) WithEnv(env []string) *HostSandboxes {
	h.env = append([]string(nil), env...)
	return h
}

// WithHidden sets the paths every run's run_command runs with hidden
// (mcp.NewHostSandboxTools) and returns h: pkg/daemon passes its own
// data directory on a single-tenant daemon, whose access code lives
// there (docs/access-code.md). Unlike WithEnv's entries these apply to
// every run, the in-process commands (checkout, the setup script) as
// well as the agent's own mcpserver (HiddenPaths).
func (h *HostSandboxes) WithHidden(paths []string) *HostSandboxes {
	h.hide = append([]string(nil), paths...)
	return h
}

// IsolatesNetwork implements NetworkIsolator: no. A run here executes on
// the host, through the host's own network stack, which is exactly why
// Acquire refuses a task that asked for none rather than pretending.
func (h *HostSandboxes) IsolatesNetwork() bool { return false }

// Unisolated implements unisolatedSandboxes: every run here executes as
// this daemon's own user, on this host, so only this machine's own
// tasks are dispatched to it (checkUnisolated).
func (h *HostSandboxes) Unisolated() bool { return true }

// Acquire implements Sandboxes: a fresh directory under baseDir, named
// for the run.
//
// A non-zero shape is refused rather than ignored. A local directory has
// no CPU, memory or disk of its own to size -- it is a path on the host's
// own filesystem -- so a task that asked for a specific one would
// silently get the host's instead: the same refusal this backend gave
// before, when a shape override went looking for a Reshape it does not
// implement.
//
// A task asking for model.NetworkNone is refused for the same reason and
// more sharply. There is nothing here to enforce it against: a run in
// this backend executes on the host, through the host's own network
// stack, so the only way to take the internet away from it would be to
// take it away from the daemon. Handing such a task a networked sandbox
// would be answering a promise with silence -- the whole point of the
// setting is that a job said to reach nothing reaches nothing -- so it
// fails here instead, naming the backend as the reason.
func (h *HostSandboxes) Acquire(ctx context.Context, name string, shape Shape) (Sandbox, error) {
	if shape.NetworkAccess == model.NetworkNone {
		return nil, fmt.Errorf("orchestrator: sandbox %q asks for no network access, and a host-directory "+
			"sandbox has no network of its own to withhold -- it runs on this host, with this host's own "+
			"network. Only a VM backend (-granule-sandboxes) can honour %q; file this task without a "+
			"network-access override, or dispatch it on a deployment that has one",
			name, model.NetworkNone)
	}
	if !shape.SizeIsZero() {
		return nil, fmt.Errorf("orchestrator: sandbox %q asks for %d vCPU/%d MiB/%d GiB but a host-directory sandbox has no shape of its own", name, shape.CPUs, shape.MemoryMB, shape.DiskGB)
	}
	h.mu.Lock()
	if h.building == nil {
		h.building = map[string]bool{}
	}
	h.building[name] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.building, name)
		h.mu.Unlock()
	}()
	root := filepath.Join(h.baseDir, name)
	// A directory left behind by a previous process using this same
	// -sandbox-dir would otherwise be inherited wholesale, which is the
	// one thing a sandbox per run exists to rule out.
	if err := os.RemoveAll(root); err != nil {
		return nil, fmt.Errorf("orchestrator: clearing sandbox directory %q: %w", root, err)
	}
	// 0700: the checkout may be a private repository, and nobody else on
	// this host should read it (GSA1-18).
	if err := os.MkdirAll(root, 0o700); err != nil {
		// A full filesystem is the one failure here that says nothing
		// about this run and everything about the deployment: every
		// task fails identically at setup until somebody reclaims
		// space, so the error a task's own timeline shows is the only
		// place an operator is likely to read about it. Say where the
		// space went and what recovers it, rather than leaving them
		// "no space left on device" against a path.
		if errors.Is(err, syscall.ENOSPC) {
			return nil, fmt.Errorf("orchestrator: creating sandbox directory %q: the filesystem holding %q is full "+
				"(it holds one checkout per run); restarting the daemon reaps the directories runs killed mid-flight "+
				"left behind (HostSandboxes.ReapOrphans), and a disk that is full of live runs needs to grow: %w",
				root, h.baseDir, err)
		}
		return nil, fmt.Errorf("orchestrator: creating sandbox directory %q: %w", root, err)
	}
	sb := &hostSandbox{owner: h, name: name, root: root, hide: h.hide, startedAt: time.Now()}
	if shape.OperatorEnv {
		sb.env = h.env
	}
	h.mu.Lock()
	h.live[name] = sb
	h.mu.Unlock()
	return sb, nil
}

// ReapOrphans removes every directory under baseDir that this process is
// not itself holding, and reports how many it removed.
//
// The host-directory half of GranuleSandboxes.ReapOrphans, and here for
// the same reason: a directory is created for a run and removed with it
// (Acquire and Release above), so one still on disk while nothing holds
// it belongs to a process that died before its Release could run. Being
// killed mid-run is not exotic for this daemon --
// a run whose unwinding outlasts pkg/daemon's shutdownDrain is given up
// on, and a daemon killed outright (SIGKILL, OOM, a host reboot) gives
// up on all of them, so an upgrade or a restart that lands on a run in
// flight can leave that run behind.
//
// What it leaves behind is a whole checkout of the task's repository,
// plus whatever the agent downloaded into it, and until now nothing ever
// removed one: a deployment that restarts often enough accumulates one
// per killed run until the filesystem holding baseDir is full -- at
// which point every later Acquire fails at mkdir with ENOSPC and no task
// can run at all. Reaping at startup is what makes that a restart rather
// than a permanently wedged deployment.
//
// Everything under baseDir is fair game because baseDir is this type's
// alone -- -sandbox-dir's own flag doc calls it "the root directory
// HostSandboxes creates one working directory per run under" -- and
// because the live check above is what keeps a running deployment's own
// sandboxes out of it, should a caller ever run this off startup.
//
// A removal that fails is joined onto the others rather than ending the
// sweep: the directory that cannot be removed is exactly the one worth
// naming, and it is no reason to leave the rest of the disk spent.
func (h *HostSandboxes) ReapOrphans(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(h.baseDir)
	if err != nil {
		// Nothing to reap from a directory that does not exist yet --
		// the ordinary state of a deployment's first start, and not
		// something to report as a failure of the sweep.
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("orchestrator: listing sandbox directories to reap under %q: %w", h.baseDir, err)
	}
	h.mu.Lock()
	live := make(map[string]bool, len(h.live)+len(h.building))
	for name := range h.live {
		live[name] = true
	}
	for name := range h.building {
		live[name] = true
	}
	h.mu.Unlock()

	var reaped int
	var errs []error
	for _, entry := range entries {
		if live[entry.Name()] {
			continue
		}
		path := filepath.Join(h.baseDir, entry.Name())
		killDetachedStepRunners(path)
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("removing orphaned sandbox directory %q: %w", path, err))
			continue
		}
		reaped++
	}
	return reaped, errors.Join(errs...)
}

// hostSandbox is one run's directory.
type hostSandbox struct {
	owner *HostSandboxes
	name  string
	root  string
	env   []string
	hide  []string
	// startedAt is when this process acquired (or readopted) the
	// sandbox -- SandboxHealth.StartedAt.
	startedAt time.Time

	// envFile is SandboxEnvFile's, written the first time it is asked
	// for and removed by Release.
	envMu   sync.Mutex
	envFile string
}

func (s *hostSandbox) Name() string { return s.name }

// Root implements rootedSandbox: the directory itself, which is what a
// task with capabilities to place needs.
func (s *hostSandbox) Root() (string, error) { return s.root, nil }

// Tools implements Sandbox: mcp.NewHostSandboxTools confined to this
// run's own directory, with the owner's WithEnv entries and its
// WithHidden paths hidden.
func (s *hostSandbox) Tools(ctx context.Context) ([]mcp.Tool, error) {
	return mcp.NewHostSandboxTools(s.root, s.hide, s.env...), nil
}

// HiddenPaths implements hidingSandbox: the owner's WithHidden paths.
func (s *hostSandbox) HiddenPaths() []string { return s.hide }

// SandboxEnv implements envSandbox: the owner's WithEnv entries, for the
// agent's own mcpserver (agent.RunConfig.SandboxEnv).
func (s *hostSandbox) SandboxEnv() []string { return s.env }

// SandboxEnvFile implements envSandbox: the owner's WithEnv entries
// written to a 0600 file (mcp.WriteSandboxEnvFile) for the agent's own
// mcpserver to read, so that they never sit on its argv (GSA1-12). It
// lives in the system temp directory rather than under baseDir, where
// ReapOrphans would take it for a dead run's directory, or under root,
// where the agent's own read_file would reach it. "" when this sandbox
// carries no entries.
func (s *hostSandbox) SandboxEnvFile() (string, error) {
	if len(s.env) == 0 {
		return "", nil
	}
	s.envMu.Lock()
	defer s.envMu.Unlock()
	if s.envFile == "" {
		path, err := mcp.WriteSandboxEnvFile("", s.env)
		if err != nil {
			return "", fmt.Errorf("orchestrator: sandbox %q: %w", s.name, err)
		}
		s.envFile = path
	}
	return s.envFile, nil
}

// ConfigureGitCredentials points this run's git at the proxy -- an
// ordinary file write under its directory, where GranuleSandboxes' own
// method of the same name has to reach into a VM's guest to do it.
func (s *hostSandbox) ConfigureGitCredentials(ctx context.Context, remoteURL, token string, identity mcp.GitIdentity) error {
	return mcp.ConfigureGitCredentials(s.root, remoteURL, token, identity)
}

// Rebuild implements SandboxRebuilder: remove this run's directory and
// make an empty one again at the same path -- Acquire's own
// RemoveAll/MkdirAll pair, which is already exactly "destroy whatever is
// under this name and start clean".
//
// It is worth having here even though a host directory cannot break the
// way a VM can (there is no guest to stop answering, no disk of its own
// to fill). What it can be is *wrong* -- a build left half-finished, a
// dependency tree in a state the agent cannot reason about -- and
// starting over is the same answer to that whichever backend a
// deployment runs. A run that reaches for this on one backend and is
// refused on the other would be the more surprising outcome.
func (s *hostSandbox) Rebuild(ctx context.Context) error {
	if err := os.RemoveAll(s.root); err != nil {
		return fmt.Errorf("orchestrator: removing sandbox directory %q to rebuild it: %w", s.root, err)
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("orchestrator: recreating sandbox directory %q: %w", s.root, err)
	}
	return nil
}

// Release removes the directory. Unlike a granule VM's own Release there
// is no isolation boundary to enforce here -- a host directory never had
// one from the host daemon it sits beside -- but leaving one behind would
// accumulate a checkout per run for the life of the deployment.
func (s *hostSandbox) Release(ctx context.Context) error {
	s.owner.mu.Lock()
	delete(s.owner.live, s.name)
	s.owner.mu.Unlock()
	killDetachedStepRunners(s.root)
	s.envMu.Lock()
	envFile := s.envFile
	s.envFile = ""
	s.envMu.Unlock()
	if envFile != "" {
		if err := os.Remove(envFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("orchestrator: removing sandbox environment file %q: %w", envFile, err)
		}
	}
	if err := os.RemoveAll(s.root); err != nil {
		return fmt.Errorf("orchestrator: removing sandbox directory %q: %w", s.root, err)
	}
	return nil
}

// SandboxReadopter is a Sandboxes backend that can take back a sandbox a
// previous process created and never released: the sandbox of a run this
// daemon adopted from the one that started it (ResumeAdoptedRun,
// design/design/non-disruptive-upgrade.design.md, Stage 2). Readopt claims it -- it is
// live from then on, so ReapOrphans and SweepOrphans spare it exactly as
// they spare one Acquire built -- and returns a Sandbox whose Release
// destroys it. It builds nothing: a sandbox that is not there is an
// error, and the run it belonged to is finished as interrupted instead.
//
// Called before the startup reap, which is what makes that reap one by
// claim rather than by prefix.
type SandboxReadopter interface {
	Readopt(ctx context.Context, name string) (Sandbox, error)
}

// Readopt implements SandboxReadopter: the directory name a previous
// process created under baseDir.
func (h *HostSandboxes) Readopt(_ context.Context, name string) (Sandbox, error) {
	root := filepath.Join(h.baseDir, name)
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: readopting sandbox directory %q: %w", root, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("orchestrator: readopting sandbox %q: %s is not a directory", name, root)
	}
	// No WithEnv entries: nothing here says whose run this was, and
	// the operator's credentials go only to a run known to be theirs.
	sb := &hostSandbox{owner: h, name: name, root: root, startedAt: time.Now()}
	h.mu.Lock()
	h.live[name] = sb
	h.mu.Unlock()
	return sb, nil
}
