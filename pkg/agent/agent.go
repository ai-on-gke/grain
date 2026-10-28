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

// Package agent defines the seam between the controller and whichever
// process actually drives a coding agent. v1 has exactly one implementation
// of this shape, Claude, hardcoded throughout grain/automation/dispatch.py
// -- there is no equivalent interface there to reuse. This is that
// interface's first appearance. The built-in implementations drive real
// CLIs as subprocesses; pkg/agent/custom also adapts an executable from a
// deployment's state repository through a versioned JSON protocol.
// pkg/cli/daemon.go's agentFrameworks chooses between them per run.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/deploymenthealth"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunConfig is what one agent run needs to start: a prompt and where its
// sandbox tools should reach. A Framework that forks a real MCP client
// as a subprocess and lets it manage its own connection -- which both
// agent/antigravity and agent/claude do -- cannot consume Tools at all,
// there being no in-process registry to hand a forked process, so it
// reads SandboxRoot or GranuleVM instead, whichever is set, and builds the
// arguments its own forked "mcpserver" subcommand needs to reach that
// sandbox: -sandbox-root for a local directory, or -granule-vm for a named
// orchestrator.GranuleSandboxes VM.
//
// Tools therefore has no consumer at present. It was read by the
// in-process Gemini runtime agent/antigravity replaced, and RunDispatch
// still populates it from orchestrator.Config.GrantTools (whose own doc
// comment, and selfrepair.Confirm's, say what that costs); it is kept
// here rather than deleted because it belongs to the interface, not to
// this moment's two implementations.
type RunConfig struct {
	Prompt      string
	SandboxRoot string
	// SandboxEnvFile is a 0600 file (mcp.WriteSandboxEnvFile) holding the
	// NAME=VALUE entries the run_command of a SandboxRoot sandbox sets on
	// top of its allowlist: the daemon's -host-sandbox-env, which only a
	// host-directory sandbox carries (orchestrator.HostSandboxes).
	// SandboxEnvArgs turns it into the forked mcpserver's
	// -sandbox-env-file, and only alongside SandboxRoot. A path rather
	// than the entries themselves, because those are the operator's own
	// credentials and an argv is readable by every user on the host
	// (GSA1-12).
	SandboxEnvFile string
	// SandboxHide is the paths the run_command of a SandboxRoot sandbox
	// must not reach (mcp.NewHostSandboxTools): a single-tenant daemon's
	// data directory, which holds its access code. SandboxEnvArgs turns
	// it into the forked mcpserver's -sandbox-hide, and only alongside
	// SandboxRoot.
	SandboxHide []string
	// GranuleVM, when SandboxRoot is empty, names a bwsalmon/granule-managed
	// VM (orchestrator.GranuleSandboxes.VMNameFor's own result, not the
	// sandbox/run's own name) a Framework with no in-process route to a
	// sandbox can point its own forked "mcpserver -granule-vm" subprocess
	// at instead of a local directory -- see agent/antigravity's and
	// agent/claude's Framework.Run.
	GranuleVM string
	// Container names a local Docker container whose filesystem and processes
	// make up this run's sandbox. Frameworks pass it to mcpserver directly.
	Container string
	// K8sPod names a Kubernetes sandbox pod (orchestrator.K8sSandboxes')
	// that makes up this run's sandbox when none of the three above is
	// set: the forked "mcpserver" reaches it with `kubectl exec`, from
	// the daemon pod or from a run pod, which carries the same service
	// account.
	K8sPod KubePod
	// TaskID is the task this run belongs to -- the one fact a forked
	// "mcpserver" subprocess needs before it can ask the daemon to act on
	// this run's behalf rather than only on its sandbox. It is passed as
	// that subprocess's own -task, alongside the daemon URL the Framework
	// itself was constructed with (agent/claude's WithGrainServer), and
	// what it buys is the open_pull_request tool: a run that can open its
	// own pull request while it still has turns left to react to CI.
	//
	// Empty -- a caller that has no task, or a Framework never told where
	// its daemon is -- simply leaves that tool unregistered, and the run
	// works exactly as it did before: its branch still becomes a pull
	// request when orchestrator.ProcessResult finishes the run.
	//
	// It is deliberately separate from Repo/Branch below: those say which
	// branch a run may *read* CI for, and this says which task grain may
	// be asked to act on. Neither is derived from the other, and neither
	// is derived from anything the agent can influence.
	TaskID string
	// RunID is the run this is (model.Dispatch.RunID), for whatever a
	// Framework records about the run outside its own transcript -- the
	// host tools a host-tool grant admits (agent/antigravity) are
	// recorded against it. Empty for a caller with no run to name.
	RunID string
	// Repo ("owner/name") and Branch are the repository this run pushes
	// to and the branch it pushes -- model.BranchName's answer for this
	// task, the same pair BuildPrompt already names in the prompt. A
	// Framework passes them to its forked "mcpserver" subprocess so
	// pkg/mcp's pull_request_status can read CI for exactly that branch
	// and no other (pkg/cli/mcpserver.go's -pr-repo/-pr-branch).
	//
	// Both empty is a task with no repo attached, which is a real case
	// (Target is a pointer, and BuildPrompt has a sentence for it): the
	// tool then reports that there is nothing to look at rather than
	// disappearing from the roster.
	//
	// They are deliberately not derived from SandboxRoot or from
	// anything the agent can influence. Where a run's tools may look is
	// grain's to decide, the same "deterministic, not self-reported"
	// rule model.BranchName's own doc comment sets out for the branch
	// itself.
	Repo   string
	Branch string
	// Base is the branch Branch merges into -- model.Task's own Base --
	// passed to the same subprocess as its -pr-base, so pkg/mcp's
	// base_branch_status can read CI for the branch this work is aimed
	// at and tell a run whether a failing check was already failing
	// before its change.
	//
	// Empty is the ordinary case rather than a missing value: a task with
	// no base of its own merges into the repo's default branch, and that
	// tool resolves it there rather than here. It is under the same rule
	// as Repo/Branch above -- grain's to decide, never the agent's.
	Base string
	// PullRequestNumber, when positive, pins an external pull request
	// within Repo (derived from model.LinkExternalPR) as "-pr-number" on
	// the forked mcpserver, so pull_request_status,
	// reply_to_pull_request_comment and resolve_review_thread can inspect
	// and reply on that pull request even when Branch has no open PR of
	// its own.
	PullRequestNumber int
	// Repair marks this run as the merge queue's repair of the branch
	// its pull request is already open from
	// (orchestrator.mergeRepairRun), passed to the same subprocess as
	// its -pr-repair so that base_branch_status answers a run holding
	// up a merge queue the way that run needs answering: breakage it
	// inherited from the base is still breakage it has to fix here,
	// where every other run is told to leave it alone
	// (mcp.PullRequestScope.Repair).
	//
	// False is the ordinary run, and leaves that tool's answers exactly
	// as they have always read. It is grain's own fact, under the same
	// rule as Repo/Branch/Base above -- nothing the agent says can set
	// it.
	Repair bool
	// GitHubCIIgnored says Repo has declared that GitHub's check runs
	// are not a source of verdicts for it -- grain CI is its gate
	// (model.RepoConfig.IgnoreGitHubCI) -- passed to the same subprocess
	// as its -pr-ignore-github-ci so that pull_request_status and
	// base_branch_status say that rather than listing checks the merge
	// queue is not reading (mcp.PullRequestScope.GitHubCIIgnored).
	//
	// False is every repository that has not declared it, and leaves
	// both tools exactly as they read before. Grain's own fact, under
	// the same rule as the four above.
	GitHubCIIgnored bool
	// BaseIncident carries notice of an active base branch incident, if any.
	BaseIncident string
	// GitCredential names the credential this run's git work goes
	// through grain's proxy with -- model.Store.TaskGitCredential's
	// answer for the task and Repo: the task's own github-credential
	// grant, else its team's connection for that repository -- passed to
	// the same subprocess as its -pr-credential, so that
	// pull_request_status, base_branch_status and the issue tools read
	// GitHub as the account the branch is pushed as. It is the name of a
	// credential and never the credential itself: the process told it is
	// on the controller, where the credential store already is, and
	// reads the token there (gitproxy.CredentialSet.GetForRepoOn).
	//
	// Empty leaves those reads on the deployment's own owner/repo ladder,
	// as they were before this field existed -- which, on a deployment
	// whose credentials are all team connections (grain/task-1592), is
	// no credential at all: every read went out unauthenticated, and a
	// private repository answered 404 to the very run that had just
	// pushed to it. Under the same rule as Repo/Branch above: grain's to
	// decide, never the agent's.
	GitCredential string
	// Grants names the capability grants this run's task holds whose
	// whole effect is which tools the run gets --
	// "self-debug" (pkg/capability/selfdebug), for reading grain's own
	// source, and "bootstrap-playbooks" (pkg/capability/bootstrap), for
	// reading the setup runbooks embedded in this binary. A Framework
	// passes each one on as a "-grant <name>" argument of its forked
	// "mcpserver" subprocess, which is where those tools are actually
	// built (GrantArgs, and pkg/cli/mcpserver.go's flag of that name).
	//
	// They are names here, rather than the tools themselves in Tools,
	// because Tools has no consumer left (see above): a Framework that
	// forks a CLI cannot look at an in-process tool set, so what it needs
	// is the *fact* of the grant, in a form that survives the fork.
	//
	// One list rather than a field per capability because a Framework's
	// whole part in this is to pass the names through untouched: a fourth
	// capability that wants the same treatment is a name
	// orchestrator.RunDispatch adds at one end and mcpserver recognizes
	// at the other, and nothing at all in between. Only the grants that
	// decide a tool roster belong here -- RunDispatch picks them out of
	// the task's own Grants and leaves the rest, so nothing learns about
	// a credential-minting capability it has no business knowing about.
	//
	// Empty -- every ordinary task -- passes nothing, and the run's tool
	// roster is exactly what it was.
	Grants []string
	// ToolBudget puts this run under a durable judgement task's per-wake
	// tool budget (mcp's tool_budget.go): its forked mcpserver is passed
	// -tool-budget (GrantArgs) and every result past the budget says so.
	ToolBudget bool
	// ProposeRepos are the repositories this deployment works in, which a
	// propose_task call may ask its proposal to be filed against
	// (model.Store.WorkRepos, resolved by orchestrator.RunDispatch), most
	// recently worked in first. A Framework passes each one on as a
	// "-propose-repo <owner/name>" argument of its forked "mcpserver"
	// subprocess (ProposeRepoArgs), which folds the list into
	// propose_task's own `repo` argument description.
	//
	// It buys a run the one fact that argument cannot state for itself.
	// The rule -- any repository this deployment works in may be named --
	// is in the description already; which repositories those are is a
	// question about the store, two processes away, and a run that cannot
	// answer it files work in the wrong repository silently
	// (orchestrator.proposedTarget files it against the run's own
	// repository with a note, and a human retargets it by hand).
	//
	// Capped by the caller rather than here: this is a list bound for a
	// tool description, and a deployment with forty repositories should
	// spend a sentence on the ones its work is actually going on in
	// rather than a page on all of them. Empty -- a caller with no store
	// to ask, which is every test fake -- leaves that description exactly
	// as it reads without this.
	//
	// Under the same rule as Repo/Branch above: what a run may name is
	// grain's to decide, never the agent's to report.
	ProposeRepos []string
	// GrainSourceDir is the checkout of grain's own source read_grain_source
	// may read, passed on as the forked "mcpserver"'s own -grain-src-dir.
	// It is a deployment-wide fact (cmd/grain's sourceDir: the copy baked
	// into the image, or -upgrade-src-dir's checkout) that reaches a
	// Framework through the run rather than through construction, so that
	// only a run which is actually allowed to read it is ever told where
	// it is: orchestrator.RunDispatch sets it only alongside the
	// self-debug grant in Grants.
	GrainSourceDir string
	// GrainSecretsDir is the directory holding this deployment's own
	// secrets -- cmd/grain's secretsDir, -data-dir/secrets -- passed on
	// as the forked "mcpserver"'s own -grain-secrets-dir, and only ever
	// alongside the grain-log grant.
	//
	// It is not material and it is not for the run: nothing in that
	// directory is served to a sandbox, and the process told about it is
	// on the controller, where those files are already readable. It is
	// there so that read_grain_log can redact the values it finds in the
	// journal *before* answering, which is the whole basis on which that
	// capability is granted at all (pkg/capability/grainlog's Redactor).
	// Without it that tool refuses every call rather than serving a
	// journal it could not clean.
	GrainSecretsDir string
	// GrainStateDir is the controller's synchronized state checkout, passed only with deployment-health.
	GrainStateDir string
	// LocalScripts is the local-scripts declarations this run is served
	// (pkg/capability/localscripts, docs/local-scripts.md), each one
	// only after the daemon has checked the run's task holds its
	// local-scripts:<name> capability. The forked mcpserver snapshots
	// each and serves its scripts as local_<name> tools.
	LocalScripts []LocalScripts
	// LocalScriptsEnvFile is a 0600 file (localscripts.WriteCredentialsFile)
	// of the secrets those declarations' scripts are delivered, named to
	// the forked mcpserver as -local-scripts-env-file, never the values in
	// argv, for SandboxEnvFile's reason. "" when they name none.
	LocalScriptsEnvFile string
	Tools               []mcp.Tool
	// MaxTurns caps the number of model-response/tool-call round trips
	// before Run gives up and returns an error, guarding against a run
	// that never stops asking for tools. Zero means the framework's own
	// default.
	MaxTurns int
	// TranscriptPath, if set, is a file a Framework may write its own
	// transcript-in-progress to as the run proceeds, rather than only
	// handing one back in Result.Transcript once Run returns -- what
	// lets a caller with filesystem access to that path show a
	// still-running run's output, not just a finished one's
	// (bwsalmon/agents#467). "" means no caller wants this. The exact
	// file format is a Framework's own business -- pkg/agent/claude and
	// pkg/agent/antigravity's own doc comments on Framework.Run say what
	// each writes there and how a reader gets a still-in-progress run's
	// transcript back out of it. Both mirror their subprocess's raw
	// stream-json, but the two event vocabularies differ, so each
	// package owns its own reader rather than sharing one.
	TranscriptPath string
	// Addenda, if set, is polled by a Framework whose own loop has a
	// "between turns" to poll at, for anything a
	// human has added to the task's conversation since the last poll --
	// a comment posted while this very run is still in flight, not just
	// the ones already folded into Prompt at dispatch. It returns new
	// addenda oldest first, or nil if there are none; a Framework that
	// finds one folds it into the conversation as a fresh user turn, the
	// same way a redispatch already folds the whole thread in up front
	// (orchestrator.commentThreadSection).
	//
	// No Framework calls this today: each runs one blocking subprocess
	// with the whole prompt written to stdin before the process starts,
	// so there is no turn boundary here to poll at. It was polled by the
	// in-process Gemini runtime agent/antigravity replaced, which owned
	// its own turn loop and so had a "between turns" to poll at.
	//
	// A comment posted mid-run does still reach the run, by the other
	// route: the forked "grain mcpserver" polls the daemon for it and
	// appends it to the next tool result the run asks for
	// (AddendaAfter below, and pkg/mcp's addenda.go). That needs nothing
	// of a Framework but the fork it already makes, which is why it is
	// the one that got built -- see mcp's own file comment for what it
	// costs against a real user turn.
	Addenda func(ctx context.Context) ([]string, error)

	// AddendaAfter is the last comment on this task already folded into
	// Prompt (orchestrator.commentThreadSection's own reading of the
	// thread), passed on as the forked "mcpserver"'s -addenda-after so
	// that the poll behind those tool-result notices starts where the
	// prompt stops (AddendaArgs, mcp.Registry.AnnounceAddenda).
	//
	// It is the cursor rather than a switch: what turns the notices on is
	// having a daemon and a task to ask about at all (TaskID and the
	// Framework's own server URL), and this only says how far back
	// "since the run started" reaches. A dispatch that got this wrong in
	// the low direction reads the prompt's own conversation back to the
	// run as breaking news; in the high direction it swallows a comment
	// that arrived before the fork.
	//
	// Zero is a task whose prompt quotes no conversation -- every first
	// dispatch -- and is exactly right there: nothing is in front of the
	// run yet, so everything on the task is new to it.
	//
	// Under the same rule as Repo/Branch above: how far back a run's own
	// tools may read is grain's to decide, never the agent's to report.
	AddendaAfter int64

	// Deadline is when grain cancels this run if it earns no wait credit
	// -- the wall-clock budget orchestrator.Config.MaxRunRuntime states,
	// measured from the moment the run's agent started. It is what a
	// Framework passes on to its forked mcpserver so that the tools it
	// serves can count it down on every result, and what those tools
	// credit blocked time back against (RunDeadlineArgs,
	// mcp.Registry.AnnounceDeadline, mcp's wait_credit.go).
	//
	// Uncredited, on purpose: the credited deadline moves while the run
	// is in flight, and a fixed number in a forked process's arguments
	// could only ever be a stale copy of a moving one. The fork is told
	// where the count starts and asks the daemon for what has been added
	// to it.
	//
	// Zero is a caller with no deadline to state, which leaves the fork
	// with no -run-deadline at all -- the case every in-process caller
	// and most tests are in.
	Deadline time.Time

	// Interactive marks this run's task as one a person is sitting with
	// (model.Task.Interactive), and is what turns the chat tool on in the
	// forked mcpserver (InteractiveArgs, and pkg/cli/mcpserver.go's
	// flag of the same name): the run says something in the task's own
	// conversation and blocks until they answer, inside one tool call.
	//
	// It is a fact about the task rather than a capability, which is why
	// it is a field here rather than a name in Grants: nothing is
	// provisioned for it and nothing has to be granted: the whole of what
	// it decides is whether a tool that waits for a person is worth
	// offering, and on a task nobody is watching it is not.
	//
	// False -- every ordinary task -- passes nothing, and the run's tool
	// roster is exactly what it was.
	Interactive bool

	// ResumeSession is the conversation a previous run of this same task
	// left suspended (model.SuspendedSession, recorded off
	// Result.ResumableSession), which this run's first exec joins
	// instead of starting the task's conversation from nothing.
	//
	// It is the cross-dispatch half of what retry.go does inside one
	// run. The saving is the same and larger: everything the suspended
	// run had read, worked out and decided is still in the conversation,
	// so a task that ran out of wall clock, met a usage limit or stopped
	// to ask a question carries on from there rather than spending its
	// first half hour rediscovering the same repository.
	//
	// What it does *not* carry is the sandbox. A dispatch builds a new
	// one and clones the repository again, so the conversation is the
	// only thing in common: anything the suspended run wrote and did not
	// push is gone. SuspendedPreamble is the paragraph that says so, and
	// a Framework honouring this field must send it -- in front of
	// Prompt, not instead of it, because the prompt has moved on since
	// (a human's reply, an attempt history, a merge queue repair) and
	// the conversation cannot know any of it.
	//
	// "" -- every first dispatch, and every task whose framework has no
	// session that outlives a run -- is the ordinary case and leaves the
	// run exactly as it was. A Framework that cannot resume ignores the
	// field; one that can must still degrade to starting the task from
	// its prompt when the CLI turns out not to have the session. That is
	// not a courtesy: the session store is on the controller's disk and
	// is pruned out from under a token either way -- claude's by the CLI
	// itself, which grain does not own, and codex's and antigravity's by
	// grain's own bounds on the directory it keeps for them
	// (pkg/agent/sessionstore).
	ResumeSession string

	// SuspendedPrompt, when non-empty, is the trimmed/delta prompt a
	// Framework sends after SuspendedPreamble when joining ResumeSession,
	// carrying only what has changed since the suspended run ended (new
	// comments, attempt history, checkout orientation) rather than
	// re-injecting the static task instructions, standing prompt
	// extensions, and full 64 KiB conversation history that are already in
	// the resumed CLI session. Prompt remains the full assembled prompt and
	// is used whenever ResumeSession is empty, refused, or lost.
	SuspendedPrompt string

	// ContinuePrompt, when set alongside ResumeSession, is the whole of
	// what a Framework sends on joining that session: no
	// SuspendedPreamble and no prompt. It is how
	// orchestrator.RunDispatch applies the stop rule to a framework whose
	// CLI cannot apply it itself (StopRuleFramework, stoprule.go): the
	// session it rejoins ended seconds ago, in the same sandbox, so the
	// preamble's "the sandbox is not the same one" would be false and the
	// prompt would read as "start again". The one thing to say is the
	// nudge. A Framework that finds the session gone degrades as it does
	// for any ResumeSession, by starting the task from Prompt.
	ContinuePrompt string

	// OnResumableSession, when set, is called with the conversation this
	// run is in as soon as its CLI names it -- the same token
	// Result.ResumableSession will carry, under the same rule that it is
	// only ever one a later dispatch can actually resume -- and again if
	// a retry moves the run to another one.
	//
	// It is what makes a run that never returns worth resuming: a daemon
	// stopped mid-turn for an upgrade cancels every live run, and a
	// session read off the Result is written only if the drain gets that
	// far (orchestrator.RunDispatch). A caller records it as it arrives
	// (model.SuspendedSession) and lets Run's own answer overwrite it at
	// the end, as it always did. Called from the goroutine copying the
	// CLI's output, so it must not block for long; never with "".
	// SessionWatcher is how a Framework implements it.
	OnResumableSession func(session string)

	// CapabilityMCPServers are the MCP servers this run's capability
	// grants expose (docs/capabilities-spec.md §5.2.4), each a loopback
	// proxy on grain's own host, in grant order. A Framework passes them
	// through CarryCapabilityServers for the host its CLI runs on, then
	// registers every one beside grain-sandbox, under its Name, and
	// admits each of its Tools. Empty -- every run holding no capability reference --
	// leaves the run's configuration exactly as it was.
	CapabilityMCPServers []CapabilityMCPServer
}

// CapabilityMCPServer is one grant's mcp_endpoint as a Framework wires it:
// the final server name (§5.2.3), the URL of grain's proxy for it, and the
// tools its tools/list reported.
type CapabilityMCPServer struct {
	Name  string
	URL   string
	Tools []string
	// Handler is the proxy URL serves (protocol.Proxy): what a host
	// whose CLI cannot reach URL serves the same requests with on
	// grain's side of its own connection (CapabilityServerCarrier). Nil
	// for a server grain does not proxy, which such a host refuses.
	Handler http.Handler
}

// CapabilityServerCarrier is a Host whose CLI cannot reach grain's
// loopback, but which can host a listener of its own and carry what
// arrives there back to grain (§5.2.4, last paragraph): a remote
// executor, over its authenticated stream.
//
// CarryCapabilityServers returns servers with each URL replaced by one
// the CLI reaches on its own machine, whose requests grain serves with
// that server's Handler, and release, which stops carrying them. The
// rules of §5.2.2 are the Handler's, applied on grain's side, so nothing
// the executor does to a request can widen them.
type CapabilityServerCarrier interface {
	CarryCapabilityServers(servers []CapabilityMCPServer) (carried []CapabilityMCPServer, release func(), err error)
}

// CarryCapabilityServers returns servers as h's CLI is to be given
// them, and release for when the run is done with them. Each is a
// loopback proxy on grain's own host, so they are handed on as they are
// to a CLI that shares that loopback, carried by a host that can carry
// them (CapabilityServerCarrier), and refused for any other: a CLI there
// would find nothing listening.
func CarryCapabilityServers(h Host, servers []CapabilityMCPServer) (carried []CapabilityMCPServer, release func(), err error) {
	noop := func() {}
	if len(servers) == 0 {
		return servers, noop, nil
	}
	if l, ok := h.(interface{ SharesHostLoopback() bool }); ok {
		// Asked first: a host that says whether its CLI shares grain's
		// loopback knows better than IsLocal, which a host that only
		// stages locally (runhost.Host) also reports.
		if l.SharesHostLoopback() {
			return servers, noop, nil
		}
	} else if l, ok := h.(interface{ IsLocal() bool }); ok && l.IsLocal() {
		return servers, noop, nil
	}
	if c, ok := h.(CapabilityServerCarrier); ok {
		carried, release, err := c.CarryCapabilityServers(servers)
		if err != nil {
			return nil, nil, err
		}
		if release == nil {
			release = noop
		}
		return carried, release, nil
	}
	return nil, nil, fmt.Errorf("this run's capability grants expose MCP servers, which grain proxies on its own host, and its agent CLI runs where it cannot reach them (%T)", h)
}

// InteractiveArgs is the "-interactive" flag a Framework passes its
// forked "grain mcpserver" subprocess for a task somebody is sitting
// with, and nothing at all for any other (pkg/cli/mcpserver.go's flag
// of the same name).
//
// It lives here beside GrantArgs and RunDeadlineArgs for their reason:
// every Framework forks the same subcommand, so the translation from
// "what this run is" to "what that process is told" is one function
// rather than one per framework, each free to drift.
//
// A bare flag rather than a value because there is nothing to say beyond
// the fact itself. How long a chat turn waits is the tool's own business
// (mcp.ChatWait), and how long the run lives is the orchestrator's
// (Config.MaxInteractiveRuntime) -- neither is anything a forked process
// should be told twice.
func InteractiveArgs(cfg RunConfig) []string {
	if !cfg.Interactive {
		return nil
	}
	return []string{"-interactive"}
}

// EffectiveSuspendedPrompt returns SuspendedPrompt when set, falling back to
// the full Prompt for callers that did not supply a separate delta prompt.
func (c RunConfig) EffectiveSuspendedPrompt() string {
	if c.SuspendedPrompt != "" {
		return c.SuspendedPrompt
	}
	return c.Prompt
}

// ResumeMessage is the user turn a Framework sends on joining
// ResumeSession: ContinuePrompt alone when the stop rule is resuming a
// session that has only just ended, and otherwise SuspendedPreamble in
// front of EffectiveSuspendedPrompt.
func (c RunConfig) ResumeMessage() string {
	if c.ContinuePrompt != "" {
		return c.ContinuePrompt
	}
	return SuspendedPreamble + c.EffectiveSuspendedPrompt()
}

// AddendaArgs is the "-addenda-after <id>" pair a Framework passes its
// forked "grain mcpserver" subprocess, so that the comments it polls the
// daemon for are the ones posted after the run's prompt was built and not
// the ones quoted in it (pkg/cli/mcpserver.go's flag of the same name,
// and mcp.Registry.AnnounceAddenda).
//
// It lives here beside InteractiveArgs and RunDeadlineArgs for their
// reason: every Framework forks the same subcommand, so the translation
// from "what this run is" to "what that process is told" is one function
// rather than one per framework, each free to drift.
//
// Nothing at all for a cursor of zero, which is a task whose prompt
// quotes no conversation: the flag's own default is zero, and an
// argument list that says so anyway is a line of noise on every dispatch
// of every first-time task. A negative cursor is not a cursor and is
// dropped the same way rather than passed on to be parsed.
func AddendaArgs(cfg RunConfig) []string {
	if cfg.AddendaAfter <= 0 {
		return nil
	}
	return []string{"-addenda-after", strconv.FormatInt(cfg.AddendaAfter, 10)}
}

// RunDeadlineArgs is the "-run-deadline <RFC3339>" pair a Framework
// passes its forked "mcpserver" subprocess so that the tools it serves
// can tell the run how much wall-clock time it has left
// (mcp.Registry.AnnounceDeadline, and pkg/cli/mcpserver.go's flag of
// the same name).
//
// RunConfig.Deadline first, and the ctx's own only as the fallback. The
// ctx used to be the whole answer, on the argument that the ctx a
// Framework's Run is given *is* the thing that ends the run, so its
// deadline is the deadline and no second copy could drift from it. That
// stopped being true when the deadline started moving:
// orchestrator.RunDispatch now derives that ctx from a timer it can
// reset as the run earns wait credit (its runClock), and a ctx with a
// resettable timer behind it carries no deadline of its own to read. So
// the field carries the run's *uncredited* deadline -- the fixed number
// this whole arrangement counts from -- and the fork adds the credit it
// earns on top of it (mcp's wait_credit.go).
//
// A caller with neither -- every in-process caller, every test that sets
// no deadline -- yields no arguments at all, and the run's tool results
// read exactly as they always have.
//
// UTC and RFC3339 because the receiving process is a fork with its own
// environment: a zone-free local timestamp is the one way this could be
// read back as a different moment than it was written as.
func RunDeadlineArgs(ctx context.Context, cfg RunConfig) []string {
	deadline := cfg.Deadline
	if deadline.IsZero() {
		at, ok := ctx.Deadline()
		if !ok {
			return nil
		}
		deadline = at
	}
	return []string{"-run-deadline", deadline.UTC().Format(time.RFC3339)}
}

// GrantArgs is the "-grant <name>" set a Framework passes its forked
// "grain mcpserver" subprocess, one pair per grant in RunConfig.Grants,
// and nothing at all for a run whose task holds none of them
// (pkg/cli/mcpserver.go's flag of the same name).
//
// It lives here, next to RunDeadlineArgs, for the same reason that one
// does: every Framework forks the same subcommand, so the translation
// from "what this run is allowed to do" to "what that process is told"
// is one function rather than one per framework, each free to drift.
//
// A repeated flag naming grants, rather than a boolean flag per
// capability, because there is nothing per-capability to say here: which
// tools a name turns on is mcpserver's own business, and a grant added
// later needs no new flag, no new field and no edit to this function.
// The exceptions are the controller-side directories a tool needs:
// self-debug's -grain-src-dir, grain-log's -grain-secrets-dir, and both
// deployment-health's -grain-secrets-dir and -grain-state-dir. They are
// written beside their grants, so a process's arguments say which grant a
// directory belongs to, and omitted rather than passed empty when a
// deployment has nothing to name. An empty flag value in a process's own
// arguments reads like a bug; what an omission means is the receiving
// tool's to state. In particular, a tool that cannot load the credentials
// it must redact refuses the read rather than returning unredacted output.
//
// An api grant naming one of the deployment's own scope sets is written as
// the scope grants it resolves to (model.APIScopeSetGrantNames): the
// mcpserver is a process of its own and does not read the deployment's
// sets, while grain's built-in ones it knows by name.
func GrantArgs(cfg RunConfig) []string {
	var args []string
	var grants []string
	for _, grant := range cfg.Grants {
		grants = append(grants, model.APIScopeSetGrantNames(grant)...)
	}
	for _, grant := range grants {
		if grant == "" {
			continue
		}
		args = append(args, "-grant", grant)
		if grant == selfdebug.CapabilityName && cfg.GrainSourceDir != "" {
			args = append(args, "-grain-src-dir", cfg.GrainSourceDir)
		}
		if (grant == grainlog.CapabilityName || grant == deploymenthealth.CapabilityName) && cfg.GrainSecretsDir != "" {
			args = append(args, "-grain-secrets-dir", cfg.GrainSecretsDir)
		}
		if grant == deploymenthealth.CapabilityName && cfg.GrainStateDir != "" {
			args = append(args, "-grain-state-dir", cfg.GrainStateDir)
		}
	}
	// Local scripts travel here too, rather than in a function of their
	// own, so that every Framework that forks the mcpserver -- the custom
	// backends' included -- passes them the moment it passes its grants.
	for _, l := range cfg.LocalScripts {
		if l.Name == "" || l.Path == "" {
			continue
		}
		args = append(args, "-local-scripts", l.Name+"="+l.Path)
	}
	if len(cfg.LocalScripts) > 0 && cfg.LocalScriptsEnvFile != "" {
		args = append(args, "-local-scripts-env-file", cfg.LocalScriptsEnvFile)
	}
	// And the tool budget, which is what a judgement task's grants mean
	// for the process serving its tools, so it travels with them.
	if cfg.ToolBudget {
		args = append(args, "-tool-budget")
	}
	return args
}

// LocalScripts is one local-scripts declaration a run is served: the
// name its local-scripts:<name> capability grants, and its file on the
// node host.
type LocalScripts struct {
	Name, Path string
}

// ProposeRepoArgs is the "-propose-repo <owner/name>" set a Framework
// passes its forked "grain mcpserver" subprocess, one pair per entry in
// RunConfig.ProposeRepos and nothing at all for a run that was given
// none (pkg/cli/mcpserver.go's flag of the same name).
//
// It lives here beside GrantArgs for that function's own reason: every
// Framework forks the same subcommand, so the translation from "what
// this run may name" to "what that process is told" is one function
// rather than one per framework. The order is kept as the caller wrote
// it -- most recently worked in first -- because that is the order the
// tool description repeats them in, and a run reading a list reads the
// front of it.
func ProposeRepoArgs(cfg RunConfig) []string {
	var args []string
	for _, repo := range cfg.ProposeRepos {
		if repo == "" {
			continue
		}
		args = append(args, "-propose-repo", repo)
	}
	return args
}

// SandboxEnvArgs is the "-sandbox-env-file PATH" pair a Framework passes
// its forked "grain mcpserver" subprocess, naming RunConfig.SandboxEnvFile,
// for GrantArgs' reason: every Framework forks the same subcommand.
// Nothing at all unless the run's sandbox is a local directory
// (SandboxRoot), the only transport that flag is for.
//
// The entries travel in a file that subprocess reads rather than in its
// environment because not every agent CLI passes its own environment on
// to an MCP server it starts (codex hands one only an allowlist of its
// own), and an argv -- what each of them does pass on unchanged -- is
// what carries the path. Never the values themselves: they used to be
// "-sandbox-env NAME=VALUE" pairs, readable in /proc/<pid>/cmdline by
// every local user for the whole run (GSA1-12).
//
// It carries RunConfig.SandboxHide too, as one "-sandbox-hide PATH" pair
// per path, for the same reason: argv is what every agent CLI passes on,
// and a path is not a secret.
func SandboxEnvArgs(cfg RunConfig) []string {
	if cfg.SandboxRoot == "" {
		return nil
	}
	var args []string
	if cfg.SandboxEnvFile != "" {
		args = append(args, "-sandbox-env-file", cfg.SandboxEnvFile)
	}
	for _, p := range cfg.SandboxHide {
		args = append(args, "-sandbox-hide", p)
	}
	return args
}

// ToolCall records one function call an agent made and what it got back,
// for a caller that wants the play-by-play rather than just the outcome --
// the closest v2 equivalent of v1's transcript.py.
type ToolCall struct {
	// Name is the tool's own name, as pkg/mcp registers it
	// ("ask_question", "run_command"), never a CLI's namespaced
	// spelling of it. Both frameworks drive a CLI that reports a tool
	// loaded from an MCP config as "mcp__grain-sandbox__<tool>", so both
	// put a reported name through mcp.BareToolName before recording it
	// here, and any Framework added later must do the same: a consumer
	// downstream of Result matches this field against the tool
	// vocabulary pkg/mcp defines -- orchestrator.ProcessResult decides
	// whether a run asked a question or left a closing comment by
	// comparing against "ask_question" and "comment_on_issue" exactly --
	// and a prefixed name silently matches none of them.
	Name      string
	Arguments map[string]any
	Text      string
	IsError   bool
}

// Result is what a run produced: the agent's final text answer plus every
// tool call it made along the way.
type Result struct {
	FinalText string
	ToolCalls []ToolCall
	// Transcript is a human-readable, chronological narrative of the run
	// -- its thinking and text output interleaved with each tool call and
	// what it got back -- for a caller that wants to read the whole story
	// rather than just its outcome (bwsalmon/agents#446's "show attempt
	// agent logs": a single scrolling pane to debug or check up on a run
	// with). It is best-effort: "" means the framework that produced this
	// Result does not build one, not that the run said nothing.
	Transcript string
	// ResumableSession is the conversation this run leaves behind for a
	// later run of the same task to be picked up in -- named in whatever
	// form this framework's own CLI takes one back in (a claude
	// session_id, a codex thread id, an agy conversation id). A caller
	// stores it against the task and hands it back as
	// RunConfig.ResumeSession on the next dispatch; see
	// model.SuspendedSession, which is where it lands.
	//
	// "" is the ordinary answer and means "there is nothing here a later
	// run could join", which covers several different reasons for saying
	// it: a CLI that cannot resume a conversation at all, a run whose
	// CLI never named a session before it died, and -- for the two
	// frameworks that keep their conversations in a home grain builds --
	// a run whose home does not outlive it, which is every run with no
	// task id and every deployment that has configured no session store
	// (pkg/agent/sessionstore). A framework must only fill this in when
	// the session genuinely outlives the run: a token a later dispatch
	// cannot resume costs that dispatch a refused exec and shows a
	// person a resume token that resumes nothing.
	//
	// It is set on a *partial* Result too, and that is the case it
	// mostly exists for: a run cut off by the wall-clock cap or paused
	// by a usage limit is exactly the one worth resuming, and those runs
	// come back as a Result beside an error (see Framework).
	ResumableSession string
	// StopBlocks is every time the stop rule sent this run back rather
	// than let it end (stoprule.go): by the CLI's own Stop hook, or by
	// grain resuming the session once afterwards. Each one is a line in
	// Transcript as well, and a count in the run's tool census under
	// StopCensusTool. Empty is every run that ended where it meant to.
	StopBlocks []StopBlock
}

// Framework drives one agent run to completion, using only the tools its
// own MCP server exposes -- see mcp. Run should not return until the
// agent has produced a final answer or RunConfig.MaxTurns is exhausted.
//
// Run may return a non-nil Result together with a non-nil error, and a
// caller must read both: an error means the run did not finish, not that
// it did nothing. A run that edits files, commits, pushes and only then
// exhausts MaxTurns has already changed the world, and a caller that
// treats the error as "no result" strands that work.
// A nil Result with an error means the run never started -- a line
// orchestrator.RunDispatch reads literally, so a Framework must not
// manufacture an empty Result for a run that produced nothing (see
// antigravity.partialResult).
type Framework interface {
	Run(ctx context.Context, cfg RunConfig) (*Result, error)
}

// ErrDetached is what a Host's Run returns, wrapped or not, for a run it
// left running somewhere that outlives this process -- a run host the
// daemon detached from as it stopped (runhost.Host.Detach), for the next
// daemon to adopt. A Framework that sees it returns at once with it and
// cleans up nothing the run is still using: the files it staged for the
// CLI belong to the run, not to this call, from then on.
var ErrDetached = errors.New("agent: the daemon stopped; the run continues in its run host for the next daemon to adopt")

// ResultParser is the optional half of Framework an adopting daemon needs
// (docs/non-disruptive-upgrade.md, Stage 2): it turns the whole stdout of
// a run another process started -- read back from a run host's spool --
// and the way it ended into the Result and error Run would have returned
// for it, so the run's outcome is read by the same rules whichever daemon
// was watching when it finished. A Framework that does not implement it
// cannot have its runs adopted.
type ResultParser interface {
	ParseResult(stdout string, runErr error) (*Result, error)
}

// AttemptsParser is the optional half of ResultParser for a framework
// that retries a run in a fresh exec, each in its own run host: the
// spool an adopting daemon reads holds only the attempt in flight, and
// earlier is what the attempts before it wrote -- read back from the
// run's live transcript -- so the Result's Transcript is the whole run's,
// as Run's would have been, while the run is still classified by its
// last attempt.
type AttemptsParser interface {
	ParseAttempts(earlier, stdout string, runErr error) (*Result, error)
}

// PullRequestFramework is the optional half of Framework that answers one
// question about the runs it drives: do they actually get the
// open_pull_request tool?
//
// It exists because that tool is registered on a fact only the Framework
// holds. A run gets it when its forked mcpserver was passed both -server
// and -task (pkg/cli/mcpserver.go), which happens when the Framework
// was built WithGrainServer -- a deployment whose daemon serves a UI/API
// at all -- and RunConfig.TaskID is set. Nothing downstream of Run can
// see the first half, and orchestrator.BuildPrompt needs it: a prompt
// that told every run to call open_pull_request would, on a deployment
// serving no UI/API, name a tool that is not on the roster.
//
// A Framework that does not implement this drives runs with no such tool
// -- the reading orchestrator.RunDispatch takes -- which is what every
// test fake and every in-process caller is.
type PullRequestFramework interface {
	Framework
	// CanOpenPullRequest reports whether this Framework gives a run it
	// drives with a task id the open_pull_request tool. It is a property
	// of the Framework, not of one run: RunDispatch always passes a task
	// id, so the per-run half is never the one in doubt.
	CanOpenPullRequest() bool
}

// TerminalCommand is one interactive invocation of a framework's own CLI:
// the binary, its arguments, whatever it needs in its environment, and
// how to clean up after it. It is what a Framework says when it is asked
// for a session somebody will sit in front of rather than a run nobody
// watches (TerminalFramework), and pkg/console is what actually puts it
// on a pseudo-terminal.
//
// It is a description rather than an *exec.Cmd because the two ends want
// different things from it: this package would have to import os/exec and
// decide about process groups, terminals and cancellation, none of which
// is a framework's business, while pkg/console has to make all three
// decisions the same way for every framework.
type TerminalCommand struct {
	// Path is the CLI binary, resolved against $PATH by whoever execs it
	// -- the same claudePath/agyPath/codexPath a Framework was
	// constructed with.
	Path string
	// Args are the arguments that turn that CLI into *this* run's
	// session: the MCP config naming this run's sandbox, the tool roster
	// grain allows, the model the deployment asked for. Deliberately not
	// the prompt: an interactive session's first turn is typed by the
	// person attached to it, and a task's own brief has never travelled
	// on argv (Framework.Run passes it over stdin, which a session's
	// stdin -- a keyboard -- has no room for).
	Args []string
	// Env is added to the daemon's own environment, not substituted for
	// it: the same handful of variables Run passes its subprocess, a
	// credential and a timeout among them.
	Env []string
	// Dir is the working directory to start the CLI in, or "" for the
	// daemon's own. It matters for the CLIs that decide something by
	// where they were started -- codex and agy both refuse to run
	// outside a git repository unless told otherwise, and agy resolves
	// its project from the cwd -- and it is the same directory
	// Framework.Run gives its own subprocess, so a session and a
	// headless run of the same task begin in the same place. A granule
	// run has no local directory at all (cfg.SandboxRoot is empty) and
	// leaves this empty with it, exactly as Run does.
	Dir string
	// Cleanup, if non-nil, is called once the session has ended -- the
	// temporary MCP config file this command names, most of all, which
	// Run removes with a defer it has no equivalent of here.
	Cleanup func()
}

// TerminalFramework is the optional half of Framework for a CLI that can
// also be driven by hand: `claude` rather than `claude -p`.
//
// A framework that implements it can back an interactive task -- one
// dispatched, sandboxed, checked out and finished exactly like any other,
// but whose agent is driven from a terminal in the UI instead of from a
// prompt (pkg/console, and orchestrator's own console.go). One that does
// not simply cannot be asked for one: orchestrator fails such a dispatch
// with a sentence naming the framework, rather than quietly running the
// task unattended, since "nobody was there" is not a detail to discover
// afterwards.
//
// The tool roster must be the same one Run gives a run of the same task:
// what makes an interactive session safe is that the agent on the other
// end of it reaches the same sandbox through the same MCP server with the
// same allowlist, and the person typing is the only thing that changed.
type TerminalFramework interface {
	Framework
	TerminalCommand(ctx context.Context, cfg RunConfig) (*TerminalCommand, error)
}

// KubePod is a Kubernetes sandbox pod as a forked "mcpserver" is told
// about it (RunConfig.K8sPod). The zero value names no pod.
type KubePod struct {
	Namespace         string
	Pod               string
	Kubeconfig        string
	Granule           bool
	GranuleAccessFile string
	// Workspace is the directory the sandbox tools operate in inside it.
	Workspace string
}

// MCPServerArgs is the "mcpserver" invocation that runs its tools in p.
func (p KubePod) MCPServerArgs() []string {
	args := []string{"mcpserver", "-k8s-pod", p.Pod, "-k8s-namespace", p.Namespace, "-workspace", p.Workspace}
	if p.Kubeconfig != "" {
		args = append(args, "-k8s-kubeconfig", p.Kubeconfig)
	}
	if p.Granule {
		args = append(args, "-k8s-granule")
	}
	if p.GranuleAccessFile != "" {
		args = append(args, "-k8s-granule-access-file", p.GranuleAccessFile)
	}
	return args
}
