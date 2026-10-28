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

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/cifault"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// Deps is everything one RunCycle call needs. Framework is a factory, not
// a shared instance: a real deployment gives every dispatch its own agent
// conversation, the way a run's own MCP server
// is scoped to one run's own tools at a time. Sandboxes is HostSandboxes
// for the local-directory stand-in, or GranuleSandboxes for a real
// bwsalmon/granule-managed VM — see the package doc comment.
//
// MaxWorkers/MaxMergers/MaxCIJobs are a count ceiling this caller may
// impose, and grain's own daemon imposes none: a deployment-wide count is
// what grain/task-28 removed, and no team has counts of its own any more,
// leaving the sandbox budget as the deployment's whole capacity control
// (Budget, Ledger). They are still here because a
// caller driving one cycle -- a test, a one-shot -- may want a hard cap of
// its own, and because zero means "no ceiling of mine" rather than "start
// nothing".
type Deps struct {
	// CapabilityServers are the deployment's attached capability
	// servers, which admission mints a task's capability references
	// through (capabilityAdmitter). Nil refuses every task holding one.
	CapabilityServers CapabilityServers
	// CapabilityPasses holds the passes minted for running attempts;
	// nil shares one holder across the process.
	CapabilityPasses *CapabilityPasses
	Store            *model.Store
	Client           github.Client
	Sandboxes        Sandboxes
	// GitReach is what each team's GitHub credential can reach, asked
	// as this package files a task itself -- a schedule firing, a suite
	// pass, a run's proposals -- so one its team cannot reach is parked
	// at filing rather than at its first clone (parkUnreached). Nil asks
	// nothing.
	GitReach *gitproxy.Reach
	// Framework builds the agent.Framework one dispatch is driven by.
	// The choice is the task's own -- an empty AgentChoice meaning "this
	// deployment's default" (model.Config.AgentFramework, and the
	// rotation through model.Config.AgentBackends behind it), which only
	// the caller knows, so resolving that is the factory's job and not
	// this package's (cmd/grain's own agentFrameworks).
	//
	// It returns an error rather than a Framework for the case a
	// deployment can now genuinely be in: the credential that framework
	// runs as (a Gemini API key, a Claude Code OAuth token) is set from
	// the UI, so it may simply not be there yet when a task naming that
	// framework comes up for dispatch. runOne reports that as a
	// setup-failed run naming what is missing, rather than the daemon
	// refusing to start at all the way a startup-time construction had
	// to.
	Framework func(ctx context.Context, choice AgentChoice) (agent.Framework, error)
	// ModelAvailability, when set, is asked at admission whether a task's
	// choice (ChoiceFor) can be served right now. A non-empty answer is
	// why not -- "no enabled connection serves model X", "all connections
	// serving X are busy" -- and holds the task in the queue with that
	// reason on it (model.GrantWait, capability model.ModelWaitCapability)
	// rather than dispatching it into a setup failure. Nil admits every
	// task, which is what a caller with one static framework wants.
	//
	// A task whose model is paused after a usage limit on every
	// connection that would serve it waits here, with "model X is paused
	// after a usage limit" as its reason (daemon.AgentPool.Availability,
	// Pause.ModelPaused), until that model's pause lifts. Nothing tries
	// another model in its place. This is the seam an opt-in fallback
	// for tasks on "Model default" or "auto" would use -- answering ""
	// and rewriting the choice it hands Framework -- and it deliberately
	// does not do so today (docs/connections-and-models.md).
	ModelAvailability func(ctx context.Context, choice AgentChoice) string
	Config            Config
	// RequireTeamGitCredential holds back every task whose team has no
	// GitHub credential for its target (model.Store.TaskGitCredential):
	// its sandbox could neither clone nor push, since the git proxy has
	// no deployment credential to fall back to for a team's work
	// (grain/task-1592). The task stays queued and is dispatched by the
	// first cycle after a team admin sets one in Team settings. The
	// daemon sets it; a test that runs no git proxy leaves it false.
	RequireTeamGitCredential bool
	// TeamHasAgentBackend reports whether team has a provider connection
	// of its own (model.AgentBackend.Team) that is not disabled. A team
	// runs only on its own connections, with no deployment-wide one
	// behind them (grain/task-1601), so a task whose team has none is
	// held back the same way RequireTeamGitCredential holds one -- queued
	// at no cost, and dispatched by the first cycle after a team admin
	// adds one in Team settings. nil, as in a test that wires no
	// connections, holds nothing for this reason.
	TeamHasAgentBackend func(ctx context.Context, team string) bool
	// SingleTenant is the single-tenant engine, which the daemon always
	// runs (false is left only for the team tests until team RBAC goes,
	// docs/single-tenant-reset.md 3e-iii-d): one backlog for the machine's owner, run with no team scoping.
	// Nothing is held for a team's setup (RequireTeamGitCredential and
	// TeamHasAgentBackend are not asked), and a run is dispatched onto the default team's provider
	// connections and pushes with the daemon's own git credential rather
	// than its team's (choiceFor, runGitCredential). model.Task.Creator
	// and Approval reach its trailers and the policy check
	// (heldByPolicy), and decide nothing here. False is the multi-tenant
	// deployment, unchanged.
	SingleTenant bool
	// Policy is this workstation's local policy (pkg/policy, the daemon's
	// -policy flag): the claims on a task -- its repositories, who asked
	// for it and who approved it, its grants and network -- are checked
	// against it before the task is dispatched (heldByPolicy) and again
	// before its sandbox is built (runOne), so a task it refuses never
	// gets a sandbox or a checkout. nil checks nothing.
	Policy *policy.Policy
	// PolicyAudit writes down each refusal of local policy -- at the
	// dispatch hold, the admitter or runOne's pre-sandbox check -- as it
	// changes the task's local-policy wait (recordPolicyRefusal):
	// policy.AuditLog.Write over the daemon's local audit log, in a
	// deployment, so that what the policy refused, and when, outlives a
	// wait that is later overwritten or cleared. nil records nothing.
	PolicyAudit func(policy.AuditEntry)
	// MintSandboxToken returns the git-proxy bearer token identifying a
	// sandbox -- gitproxy.SandboxTokenStore.EnsureToken, in a deployment;
	// nil in a test with no proxy, which skips configuring credentials
	// altogether.
	//
	// A token is minted here, per run, rather than once per slot at
	// daemon startup as it was while a sandbox outlived the runs
	// dispatched onto it. That is the security property docs/
	// data-model.md predicted would fall out of a sandbox per task: a
	// proxy token now dies with the run that used it, so one that leaks
	// cannot be replayed by, or on behalf of, anything dispatched after
	// it.
	MintSandboxToken func(sandbox string) (string, error)
	// MintSandboxGrant mints a per-attempt sandbox token bound to (sandbox, taskID, attempt)
	// -- gitproxy.SandboxTokenStore.EnsureGrant in a deployment. When non-nil,
	// runOne uses it in preference to MintSandboxToken so persistent sandboxes
	// rotate tokens across attempts and bind TaskID and Attempt.
	MintSandboxGrant func(sandbox, taskID string, attempt int) (string, error)
	// RevokeSandboxToken drops the token MintSandboxToken made, once the
	// sandbox it identified has been released -- gitproxy.
	// SandboxTokenStore.Revoke, in a deployment; nil wherever
	// MintSandboxToken is.
	//
	// It is the other half of minting per run. One token per slot was a
	// fixed set that a deployment carried for its whole life; one per run
	// is a new entry every dispatch, in a file every mint reads and
	// rewrites whole. See that method's own doc comment: this is upkeep,
	// not authorization, which Store.GitScope already handles by
	// resolving a sandbox through the live run on it.
	RevokeSandboxToken func(sandbox string) error
	// RefreshSandboxToken extends the token MintSandboxToken made, so a
	// run that outlives the token's TTL keeps its git access --
	// gitproxy.SandboxTokenStore.RefreshToken, in a deployment; nil
	// wherever MintSandboxToken is, which leaves the TTL to end a token
	// whatever its run is doing. runOne calls it every
	// SandboxTokenRefreshInterval for as long as the run's sandbox is
	// held, and stops before RevokeSandboxToken -- see
	// keepSandboxTokenFresh.
	RefreshSandboxToken func(sandbox string) (string, error)
	// SandboxTokenRefreshInterval is how often RefreshSandboxToken is
	// called for a live run. Zero means DefaultSandboxTokenRefreshInterval.
	SandboxTokenRefreshInterval time.Duration
	// CredentialGitIdentity is the commit identity a run commits under:
	// the account its git credential for its target acts as -- a PAT's
	// own GitHub login, an App's bot account, an ambient connection's
	// host login (git_identity.go,
	// gitproxy.CredentialSet.CredentialGitIdentity). Whatever half it
	// leaves empty comes from the team's legacy commit author, then
	// grain's own default. nil leaves every run on those.
	CredentialGitIdentity CredentialGitIdentityFunc
	// MaxWorkers, MaxMergers and MaxCIJobs are the caller's own ceiling,
	// 0 meaning it imposes none. Nothing reads them out of grain_config
	// any more: a stored max_workers of 3, or of 300, has no effect on
	// what a cycle starts (grain/task-28).
	MaxWorkers int
	MaxMergers int
	MaxCIJobs  int
	// Budget is how much of the machine every live sandbox on this host
	// may commit between them (model.Budget, grain/task-173): the host's
	// own cores and memory, less what is reserved for the host itself,
	// times whatever overcommitment this deployment has chosen. cmd/grain
	// reads the host once at startup and passes it here; the zero value
	// bounds nothing, which is what a deployment that could not read its
	// host -- or a test -- runs with.
	Budget model.Budget
	// Ledger is what those sandboxes have already committed of it: the one
	// record every admission in this process charges, grain CI's job gate
	// included (model.Ledger, grain/task-28). A run this cycle starts
	// charges its shape there before its row is written and gives it back
	// when the run ends, and a cycle reconciles it against the runs the
	// store says are live, so a restart or a lost release cannot leave
	// capacity held by nothing.
	//
	// nil is a caller with no deployment to charge, which is every test
	// that drives a cycle directly: admission then sees an empty host,
	// exactly as it did before a ledger existed.
	Ledger *model.Ledger
	// AgentShape is what one agent sandbox takes out of that budget --
	// the deployment-wide sandbox shape, refreshed from grain_config each
	// cycle alongside the counts, and zero on a backend that applies no
	// shape at all.
	AgentShape model.Commitment
	// DiskSpace, when set, is how the cycle takes a *live* reading of the
	// volume this deployment's sandboxes and store are written to: it is
	// called once per cycle and the answer is put into Budget.Disk, so
	// that an agent dispatch is deferred while free space is below
	// Budget.DiskFloor (grain/task-196).
	//
	// A func rather than a value because, unlike the host's cores and
	// memory, this is not a fact about the machine: what fills a volume
	// is what the runs write, so a reading taken at startup would say
	// nothing about the disk the next dispatch is about to write to.
	//
	// nil leaves Budget.Disk as the caller set it -- for a deployment,
	// unread, and so unbounded, which is what every cycle did before this
	// existed.
	DiskSpace func() model.DiskSpace
	// Runs, when non-nil, is where a cycle parks the runs it starts:
	// reconcileDispatch gives each dispatch a goroutine tracked there and
	// returns without waiting for it, so the cycle -- and the tick after
	// it -- is over in the time the dispatch *decisions* take rather than
	// in the time the agents take. That is what makes the configured
	// concurrency reachable by a deployment whose tasks arrive one at a
	// time: see InFlight's own doc comment for what waiting used to
	// cost, and cmd/grain's drainInFlight for the other half, draining it at
	// shutdown so a run still gets to release its sandbox.
	//
	// Nil means a cycle waits for every run it started before returning,
	// and joins their errors into its own. That is the shape this package
	// had before there was an InFlight at all, and it is the right one for
	// a caller that has no loop to tick again -- a test that dispatches
	// and then asserts on what the run did, or a one-shot cycle -- since
	// such a caller has nothing to hold the process open while a detached
	// goroutine works. A deployment always sets one.
	Runs *InFlight
	// RunHost is the deployment's run-host driver when agent CLIs run in
	// run hosts (-run-host-image); nil otherwise. Sandbox reconciliation
	// sweeps the run hosts no live run claims through it every tick
	// (SweepOrphanRunHosts), so one whose own Destroy failed as its run
	// finished does not linger until the next restart.
	RunHost *runhost.Host
	// CycleTimes, when non-nil, is where each RunCycle call records how
	// long it took and where inside itself that time went -- see
	// CycleTimes' own doc comment for why the record is a ring in this
	// process rather than a row per tick.
	//
	// nil means the cycle measures nothing, which is what every caller
	// with no report to serve wants (a test, a one-shot cycle). A
	// deployment sets one; cmd/grain hands the same ring to its UI/API
	// server, which serves it as GET /api/metrics' own "cycles" section.
	CycleTimes *CycleTimes
	// CI, when non-nil, is grain's own CI (pkg/ci): what runs a pushed
	// branch's `.grain/workflows` and what reconcileCI reads the verdict
	// back from. nil -- the default, and what a deployment started
	// without -ci has -- makes every CI path in this package a no-op,
	// which is how "grain CI is not wired here" is spelled.
	//
	// Whether it runs for any particular task is a further question this
	// field does not answer: the grain-ci capability decides that, per
	// task, per repo or deployment-wide (pkg/capability/grainci).
	CI ci.Runner
	// CIFaultFiles is how a red verdict finds the open pull request that
	// already fixes a shared fault: what each candidate change touches
	// (cifault.FileLister, which is GitHub's ListPullRequestFiles and
	// nothing else). A deployment hands in a cifault.FileCache over the
	// same client Client holds, so that a fault storm -- several
	// branches concluding red within minutes of each other, which is
	// exactly when this is asked -- costs one read per open change
	// rather than one per verdict.
	//
	// nil falls back to Client, which answers the same question without
	// remembering the answer. A deployment with neither names no fix,
	// and the verdict comment still says the red is shared and which
	// files every branch in it printed.
	CIFaultFiles cifault.FileLister
	// Declarations, when non-nil, is where the templates, suites and
	// schedules a repository declares in its own `.grain` directory are
	// read from (grain/task-60, model's repo_declaration.go). nil makes
	// reconcileDeclarations a no-op, which is what a test with no
	// repository to read and an embedder that wants none both get.
	//
	// Which repositories are read is not this field's answer either:
	// RepoConfig.Declarations decides that, per repo, and a deployment
	// where no repository has it on spends one store read per cycle here.
	Declarations DeclarationSource
	// StandingSandboxes, when non-nil, supervises long-standing sandboxes
	// defined in grain/config (grain/task-126): starting them on boot,
	// restarting them on failure, and keeping them running.
	StandingSandboxes *StandingSandboxSupervisor
	// PersistentSandboxes supervises this deployment's persistent
	// sandboxes (model.PersistentSandbox). Nil runs none.
	PersistentSandboxes *PersistentSandboxSupervisor
	// GitHubEvents, when non-nil, supplies verified inbound GitHub App
	// webhook events from this deployment's webhook gateway
	// (pkg/ghwebhook, cmd/github-webhook). Nil makes reconcileGitHubEvents
	// a no-op.
	GitHubEvents GitHubEventSource
	// AppBotIdentity reports whether a repository's configured GitHub
	// credential is a GitHub App, and if so the bot login ("<slug>[bot]")
	// it acts as. Used for self-loop filtering on inbound webhook events,
	// @bot mention recognition, and posting submitted PR reviews rather
	// than invisible PENDING drafts.
	AppBotIdentity AppBotIdentityFunc
	// TakeoverLabel is the pull request label that asks the GitHub bot to
	// take a pull request over, as "@<slug> take over" does (takeover.go).
	// Empty is defaultTakeoverLabel, "grain:take-over".
	TakeoverLabel string
}

// runCleanupTimeout bounds each of the two things runOne does after a run
// is over -- releasing its sandbox and finishing its row -- on a context
// detached from the caller's own cancellation.
//
// Detaching is what makes them happen at all when the daemon is shutting
// down or a task was closed mid-run (their own comments in runOne).
// Bounding is what keeps that from being worse than not detaching: both
// reach something that can hang -- `granule vm delete` against a wedged
// docker, a store write behind a busy database -- and an unbounded
// detached context would pin the dispatch goroutine, and the run row it
// has not finished yet, for the life of the process. Generous rather than
// tight, since a VM teardown is genuinely slow and the alternative to
// waiting is leaving one running.
const runCleanupTimeout = 2 * time.Minute

// Reconciler is one independent unit of a cycle: a named function that
// reads current state, does whatever that state implies, and reports
// what went wrong without deciding anything about the rest of the cycle.
//
// Each one is level-triggered and idempotent — it re-reads the store and
// GitHub every time rather than acting on a change it was handed — so
// running one is always safe, and skipping one costs latency rather than
// correctness. That is what makes them independent: a reconciler that
// fails this cycle has done nothing the next cycle cannot redo, so no
// other reconciler has any reason to be held back by it.
type Reconciler struct {
	Name      string
	Reconcile func(ctx context.Context, deps Deps, now time.Time) error
}

// dispatchReconciler names the one reconciler in the cycle that decides
// what runs now -- the one a queued task is actually waiting for, which
// is why CycleTiming.DispatchWait singles its start out from the rest.
const dispatchReconciler = "dispatch"

// Reconcilers returns the cycle's reconcilers, in the order RunCycle runs
// them.
//
// There used to be a "poll" that listed the task repo's labelled GitHub
// issues and turned each into a task, and it is gone: tasks are created by
// writing them (README, "Input is a model update, not a GitHub issue"),
// so there is no longer an outside source of tasks to reconcile against.
// A task filed by the CLI or the UI is in task_ready the moment it is
// written, rather than on whichever tick happened to poll next.
//
// "schedule" (bwsalmon/agents#376) and "releases" (bwsalmon/agents#398)
// are the additions since. "schedule" turns each schedule whose interval
// has come due into a real task, the same store write CreateTask itself
// makes, and runs first for the same latency reason syncing runs last --
// a task a schedule files this cycle should be dispatchable this same
// tick, not on the next one. "releases" carries out whatever a cut or
// promotion a human just requested through the UI still declares -- a
// fresh row in the store, not an outside event to poll for, the same
// "the store is the record" shape everything else here already holds to.
//
// "qualifications" (bwsalmon/agents#518) runs right after "releases" on
// purpose: a candidate "releases" has just advanced to CandidateActive
// this same cycle gets its qualification run instantiated the same tick
// rather than waiting a cycle to be noticed, and a run whose last task
// "sync" observed completing earlier this same cycle is promoted (when
// its plan's AutoPromote asks for it) without a tick's delay either.
//
// "branches" (bwsalmon/agents#638) is the newest: it creates whatever
// branch a human just requested from the repo page still declares but has
// not yet had created on GitHub -- the same "a fresh row in the store,
// not an outside event to poll for" shape "releases" already holds to,
// just without a candidate or qualification sequence of its own to chain
// off of, so its place in the order carries no latency preference the way
// "releases" before "qualifications" does.
//
// "pause" and "upgrade-check" (grain/task-38) are the pair that lets
// grain check an upgrade of itself, and they sit immediately before
// "dispatch" for the latency reason "schedule" sits first, in both
// directions: a check filed this cycle is dispatchable this same tick,
// and a pause whose task has finished is a queue that moves this same
// tick rather than one that idles for another poll interval. "pause"
// runs before "upgrade-check" so that the pause a finished check is
// still holding is let go before anything considers taking a new one --
// the two never contend in practice (a check is filed once per upgrade),
// but the order that cannot leave a stale pause behind is free.
//
// "target-repos" (grain/task-404) is the one entry whose place in the
// order is a dependency rather than a preference: it parks every ready
// task whose target has come off Config.TargetRepos, and it has to run
// before "dispatch" on the same cycle or the run it exists to prevent
// has already been started by the time it looks. Everything else here
// produces the same state one cycle later; this would produce it one
// clone and one push too late. It is also last of the reconcilers that
// file or wake work, so a task "schedule", "wake" or "reviews" put in
// the queue this very cycle is judged against the list before it is
// dispatched rather than on the tick after.
//
// "wake" (grain/task-386) picks back up every deferred task whose wake
// time has arrived (model.Task.DeferredUntil), and sits next to
// "schedule" because it is the same kind of pass: a stored instant that
// has come round, acted on once. Before "dispatch" for the latency
// reason again -- a task woken this cycle is dispatchable this same tick
// -- and, like the rest of them, a different order produces the same
// state one cycle later.
//
// "reviews" (grain/task-284) files the review a finished task declares,
// from the template it names, and runs before "dispatch" and "sync" for
// the latency reason "schedule" runs first: a review filed this cycle is
// dispatchable this same tick, and the merge queue sees it the same tick
// too. That placement is a preference and not a dependency, though it
// looks like one -- syncEntry holds a reviewed task's own merge on the
// declaration (Task.ReviewTemplateID) rather than on the review task
// having been filed yet, precisely so that whether a change merges
// before its review exists does not come down to this ordering.
//
// "system-schedules" (grain/task-268) is first of all, and is not a
// firing at all: it seeds the schedule row for each standing job grain
// itself ships (model.SystemTemplates -- the nightly memory agent, the
// weekly release notes and the nightly infrastructure review), on
// the first cycle a deployment with a state repository runs without one,
// and never writes over a row that already exists. It sits immediately
// before "schedule" so the two read in the order they happen rather than
// for any latency reason: a schedule seeded this cycle is due at its
// cadence's next occurrence, which is never this one.
//
// "declarations" (grain/task-60) is second of all, and sits where it
// does for the reason "system-schedules" sits first: it is what puts the
// templates, suites and schedules a repository declares in its own
// `.grain` directory into the store, and a schedule that arrives this
// cycle should be able to come due on the same tick rather than on the
// next one. It also has one ordering that is a dependency rather than a
// preference, in the other direction: a repository whose declarations an
// operator has just switched off has them removed here, before
// "schedule" can fire one of them one last time.
//
// "ci" (grain/task-246) ensures grain's own CI is running against the
// current branch head of each task with an open pull request, and writes the
// verdict onto the task. It ran last while nothing read what it wrote; it now runs
// immediately before "sync", because the merge queue reads that verdict
// (orchestrator.withGrainCI) and the order is worth one thing: a branch
// whose head has just moved has its new run started here, and so reads as
// CI-in-flight rather than as a commit grain CI has said nothing about,
// on the same cycle that decides whether to merge it. That is a
// belt-and-braces preference rather than a dependency -- the wait an
// unreported commit already gets (defaultCheckRegistrationWindow) covers
// the same gap on its own -- and, as everywhere else here, a different
// order produces the same state one cycle later. A deployment with no CI
// wired (Deps.CI nil) spends nothing on it either way.
//
// The order among the rest is a latency preference, not a dependency:
// syncing pull requests near-last lets a merge this very cycle just
// performed be picked up without a tick's delay. All of them read their
// own inputs from the store, so a different order produces the same state
// one cycle later — which is exactly why one failing does not invalidate
// another.
func Reconcilers() []Reconciler {
	return []Reconciler{
		{Name: "system-schedules", Reconcile: reconcileSystemSchedules},
		{Name: "declarations", Reconcile: reconcileDeclarations},
		// Ahead of everything that creates a sandbox pod, so the first
		// cycle after a start or an upgrade has the PriorityClasses
		// those pods name in place, or knows to create them without
		// (reconcileK8sPriorityClasses).
		{Name: "k8s-priority-classes", Reconcile: reconcileK8sPriorityClasses},
		{Name: "console-sessions", Reconcile: reconcileConsoleSessions},
		{Name: "standing-sandboxes", Reconcile: reconcileStandingSandboxes},
		{Name: "persistent-sandboxes", Reconcile: reconcilePersistentSandboxes},
		{Name: "schedule", Reconcile: reconcileSchedule},
		// Immediately after "schedule", so that a workflow schedule which
		// came due on this tick has its CI run started on this tick
		// rather than on the next one -- and so that the run finishing
		// overnight is reviewed within a cycle of its verdict arriving
		// (reconcileScheduledWorkflows).
		{Name: "scheduled-workflows", Reconcile: reconcileScheduledWorkflows},
		{Name: "wake", Reconcile: reconcileWake},
		{Name: "capability-changes", Reconcile: reconcileCapabilityChanges},
		{Name: "notifications", Reconcile: reconcileNotifications},
		{Name: "reviews", Reconcile: reconcileReviews},
		{Name: "security-holds", Reconcile: reconcileSecurityHolds},
		{Name: "path-trigger-holds", Reconcile: reconcilePathTriggerHolds},
		{Name: "github-events", Reconcile: reconcileGitHubEvents},
		{Name: "github-ci-tails", Reconcile: reconcileGitHubCITails},
		// After the events that file takeovers, and before dispatch and
		// the merge queue: a shepherd's reviews are filed, and its gate
		// read, before either can act on it.
		{Name: "takeovers", Reconcile: reconcileTakeovers},
		{Name: "review-questions", Reconcile: reconcileReviewQuestions},
		{Name: "review-threads", Reconcile: reconcileReviewThreads},
		// An operator's pause on one task (model.TaskPause): its live
		// run and grain CI run are ended here, before dispatch, so the
		// slots they free are free to this cycle's dispatch.
		{Name: "task-pauses", Reconcile: reconcileTaskPauses},
		{Name: "pause", Reconcile: reconcilePipelinePause},
		// Before "scheduled-upgrade", so that the upgrade it schedules is
		// held for (or, on an idle deployment, started) this same tick.
		{Name: "auto-upgrade", Reconcile: reconcileAutoUpgrade},
		{Name: "scheduled-upgrade", Reconcile: reconcileScheduledUpgrade},
		{Name: "upgrade-check", Reconcile: reconcileUpgradeCheck},
		{Name: "target-repos", Reconcile: reconcileTargetRepos},
		{Name: dispatchReconciler, Reconcile: reconcileDispatch},
		{Name: "ci", Reconcile: reconcileCI},
		// Before "sync", so that a base branch found red this tick holds
		// this tick's merges rather than the next one's
		// (reconcileBaseBranchCI).
		{Name: "base-branch-ci", Reconcile: reconcileBaseBranchCI},
		// Right after it, so that a branch found green this tick closes
		// the goal tasks waiting on it on this tick (reconcileTaskGoals).
		{Name: "task-goals", Reconcile: reconcileTaskGoals},
		// Beside it, and deliberately not inside it: the work that
		// follows a base branch without judging it -- a repository's
		// content-addressed build cache above all -- is dispatched here,
		// on a row the merge queue does not read, so that a cache warm
		// which fails is a slow job later rather than a held queue
		// (reconcileBaseBranchDispatch, grain/task-409).
		{Name: "base-branch-dispatch", Reconcile: reconcileBaseBranchDispatch},
		// And beside that, the publishing itself. It used to be the run
		// "base-branch-ci" above collected, so a flaky publisher held
		// every merge in the repository; since grain/task-461 it is a
		// second run at the same tip, on a row of its own that the queue
		// does not read (reconcileBaseBranchRelease).
		{Name: "base-branch-release", Reconcile: reconcileBaseBranchRelease},
		// And beside those, a repository's own `on: merge` path triggers:
		// one diff of whatever landed since the branch was last diffed
		// (pathtrigger.go).
		{Name: "path-triggers", Reconcile: reconcilePathTriggers},
		// The progress comment on each pull request or issue a tree of
		// tasks came from, and the lineage sections of that tree's pull
		// requests (worktree.go). Rate-limited per origin.
		{Name: "work-trees", Reconcile: reconcileWorkTrees},
		{Name: "sync", Reconcile: reconcileSync},
		{Name: "releases", Reconcile: reconcileReleases},
		{Name: "branches", Reconcile: reconcileBranches},
		{Name: "qualifications", Reconcile: reconcileQualifications},
		{Name: "suites", Reconcile: reconcileSuites},
		{Name: "sandbox-reconciliation", Reconcile: reconcileSandboxes},
	}
}

// AgentChoice is everything a task says about which agent its run is
// driven by, as one value for Deps.Framework to resolve: the framework
// (model.Task.AgentFramework) and the model within it
// (model.Task.AgentBackendID), each empty for a task that named none.
//
// The two are not alternatives. A task may name a framework and leave
// the model to the rotation, name a model and be dispatched onto
// whatever framework that model is, or name neither and take the
// deployment's own answer to both. What the factory does with each is
// its business -- this package carries the request, it does not resolve
// it.
type AgentChoice struct {
	// Framework is model.Task.AgentFramework: which agent CLI, empty for
	// the deployment's default.
	Framework string
	// BackendID is model.Task.AgentBackendID: which configured model
	// inside that framework, empty for whichever one the rotation picks.
	// A pinned model is used even when it has been taken out of the
	// rotation (model.AgentBackend.Disabled), which is what makes
	// "disabled" mean "nothing lands here by accident" rather than "this
	// model cannot run".
	BackendID string
	// Model is the requested LLM model name.
	Model string
	// Priority is model.Task.HasPriorityDispatch: whether this run is a
	// console session's or a task's holding priority-dispatch, which
	// bypasses per-connection concurrency limits and does not consume a
	// connection concurrency slot while open.
	Priority bool
	// Durable is model.Task.IsDurable: the pool places the run on a
	// framework this daemon can adopt across its own restart when it has
	// one, ahead of the rotation's order but behind a pinned model or
	// connection (docs/design/durable-tasks.md#DT-1, "Adoption across
	// restarts and upgrades").
	Durable bool
	// AvoidModels are models this task's latest runs were suspended off
	// (capacity, usage-limit, model: model.Store.FailedOverModels): a
	// rotation tries them after the rest of its default list. A model the
	// task chose itself is used regardless.
	AvoidModels []string
}

// ChoiceFor is the AgentChoice task's own fields make: the one runOne
// builds its framework from, and the one admission asks
// Deps.ModelAvailability about, so the two cannot disagree.
func ChoiceFor(task model.Task) AgentChoice {
	return AgentChoice{
		Framework: task.AgentFramework,
		BackendID: task.AgentBackendID,
		Model:     task.Model,
		Priority:  task.HasPriorityDispatch(),
		Durable:   task.IsDurable(),
	}
}

// choiceFor is ChoiceFor as this deployment dispatches it. A task names
// no team, so every task runs on the default team's provider
// connections, which are the machine owner's.
func (d Deps) choiceFor(task model.Task) AgentChoice {
	return ChoiceFor(task)
}

// StaticFramework adapts one already-built agent.Framework to what
// Deps.Framework wants -- for a caller with exactly one framework and no
// per-task choice to make (every test in this repo, and any embedder
// driving a single framework of its own). A real deployment does not use
// it: cmd/grain builds the framework a run needs when that run is
// dispatched, from the credential the UI has by then, which is the whole
// point of the factory taking a choice and returning an error.
func StaticFramework(f agent.Framework) func(context.Context, AgentChoice) (agent.Framework, error) {
	return func(context.Context, AgentChoice) (agent.Framework, error) { return f, nil }
}

// RunCycle is v2's whole Orchestrator.run_once equivalent: let
// dispatch.Cycle decide what runs, actually run it, turn each result into
// the effect it implies, and refresh every pull request grain is still
// watching. A deployment's own timer calls this once per tick; nothing
// here loops on its own.
//
// **Every reconciler runs, whatever the ones before it did.** A cycle is
// not a pipeline: a GitHub outage in one reconciler used to return early
// and take the others down with it, so a merge that was ready and a queue
// that needed advancing waited on a failure they had nothing to do with.
// Each Reconcilers() entry runs on its own and its error is collected
// rather than returned, so what a cycle achieves degrades to whichever
// parts of the world are reachable rather than to the first one that
// isn't. The returned error joins everything that failed (errors.Join, so
// errors.Is still answers for any of them); graind logs it and ticks
// again.
//
// Cancellation is the one thing that does stop a cycle early, since a
// cancelled context means the daemon is shutting down rather than that
// one reconciler has a problem the others might not.
//
// deps.MaxWorkers, deps.MaxMergers, deps.Config.MaxAgentTurns,
// deps.Config.PromptExtension,
// deps.Config.GitIdentity, deps.Config.TargetRepos and
// deps.Config.AgentFramework are
// refreshed from deps.Store's own grain_config row (if any) before any
// reconciler runs, so a change made through the store -- `grain
// settings`, or the UI's Settings page -- takes effect on this cycle
// rather than at the next
// restart. They are the settings this package itself is the consumer
// of; the rest of model.Config is re-applied by whoever owns the piece
// it configures (pkg/cli/daemon.go's liveConfig, once per tick, for
// everything a running daemon can adopt at all). deps is taken by value,
// so this only ever affects the cycle it is refreshing and the dispatch
// goroutines that cycle starts -- never a caller's own Deps.
//
// A store with no row yet -- deps.Store is nil in tests that build Deps
// by hand, or GetConfig itself returns nil -- leaves every one of them
// exactly as the caller set it.
//
// A cycle times itself when deps.CycleTimes is set: the whole tick, each
// reconciler's own share of it, and how far in the dispatch decision was
// reached. That last number is the one a deployment cannot otherwise
// get -- see CycleTiming.DispatchWait -- since a queue wait looks
// identical whether the deployment was full or the tick was simply slow
// to get to dispatch.
func RunCycle(ctx context.Context, deps Deps, now time.Time) error {
	// The tick times itself from here, before the stored-configuration
	// read below: that read is a store call like any other, on the
	// critical path of every dispatch decision, so a cycle that measured
	// only its reconcilers would under-report exactly the kind of
	// slowness this is here to catch.
	//
	// timing is filled in as the cycle goes and handed over in one write
	// at the end, so nothing on this path takes the ring's lock more than
	// once per tick. The deferred Observe declines a cycle cut short by
	// cancellation: that is the shutdown tick, whose truncated duration
	// describes the daemon stopping rather than the daemon being slow,
	// and there is at most one of them per process.
	started := time.Now()
	timing := CycleTiming{Start: now}
	commitsBefore := uint64(0)
	if deps.Store != nil {
		commitsBefore = deps.Store.CommitCount()
	}
	if deps.CycleTimes != nil {
		defer func() {
			if ctx.Err() != nil {
				return
			}
			timing.Duration = time.Since(started)
			timing.Commits = deps.Store.CommitCount() - commitsBefore
			deps.CycleTimes.Observe(timing)
		}()
	}

	// The one part of the budget that is read rather than configured, and
	// read here rather than at startup: free disk is what the runs have
	// written so far (Deps.DiskSpace). Taken once per cycle, before the
	// dispatch decisions this cycle makes are held against it.
	if deps.DiskSpace != nil {
		deps.Budget.Disk = deps.DiskSpace()
	}

	if deps.Store != nil {
		if mc, err := deps.Store.GetConfig(ctx); err != nil {
			log.Printf("orchestrator: reading stored configuration: %v", err)
		} else if mc != nil {
			// grain_config's max_workers, max_mergers and max_ci_jobs are
			// deliberately not read here. They used to be re-read every
			// cycle, which is what made them live settings; they are
			// legacy columns now, and a cycle that adopted one would be
			// putting back the deployment-wide ceiling grain/task-28
			// removed.
			deps.Config.MaxMergesPerRepo = mc.MaxMergesPerRepo
			// The shape follows the counts: a deployment that doubles
			// its sandbox memory in Settings has doubled what each of
			// those runs commits, and the budget has to hear about it on
			// the same tick. Only where the caller had a shape to begin
			// with -- a backend that applies none commits nothing here,
			// whatever the stored row says (cmd/grain's own
			// sandboxShapeIgnored).
			// A stored 0 is "no choice made, take grain's own default"
			// (model.Config.SandboxCPUs), so each dimension falls back
			// to what the caller resolved at startup rather than to
			// nothing -- a sandbox charged zero memory is the whole bug
			// this budget exists to close.
			if !deps.AgentShape.IsZero() {
				if mc.SandboxCPUs > 0 {
					deps.AgentShape.CPUs = mc.SandboxCPUs
				}
				if mc.SandboxMemoryMB > 0 {
					deps.AgentShape.MemoryMB = mc.SandboxMemoryMB
				}
				if mc.SandboxDiskGB > 0 {
					deps.AgentShape.DiskGB = mc.SandboxDiskGB
				}
			}
			if deps.Budget.Known() {
				if mc.SandboxMemoryReserveMB > 0 {
					deps.Budget.ReserveMemoryMB = mc.SandboxMemoryReserveMB
				}
				if mc.SandboxCPUOvercommit > 0 {
					deps.Budget.CPUOvercommit = mc.SandboxCPUOvercommit
				}
				if mc.SandboxMemoryOvercommit > 0 {
					deps.Budget.MemoryOvercommit = mc.SandboxMemoryOvercommit
				}
				if mc.SandboxDiskOvercommit > 0 {
					deps.Budget.DiskOvercommit = mc.SandboxDiskOvercommit
				}
				if !deps.Budget.RepairReserve.IsZero() && !deps.AgentShape.IsZero() {
					deps.Budget.RepairReserve = deps.AgentShape
				}
			}
			deps.Config.MaxAgentTurns = mc.MaxAgentTurns
			deps.Config.PromptExtension = mc.PromptExtension
			deps.Config.TimeZone = mc.TimeZone
		}
	}
	syncSandboxLedger(ctx, deps)
	var errs []error
	reconcilers := Reconcilers()
	if deps.CycleTimes != nil {
		timing.Reconcilers = make([]ReconcilerTiming, 0, len(reconcilers))
	}
	for _, r := range reconcilers {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		wait := time.Since(started)
		began := time.Now()
		// Each reconciler's GitHub calls are charged to its own name
		// (github.WithCaller), which is what the metrics' "github"
		// section breaks the hour's budget down by.
		rdeps := deps
		rdeps.Client = github.WithCaller(deps.Client, r.Name)
		err := recoverReconcile(ctx, r, rdeps, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Name, err))
		}
		if deps.CycleTimes == nil {
			continue
		}
		timing.Reconcilers = append(timing.Reconcilers, ReconcilerTiming{
			Name:     r.Name,
			Wait:     wait,
			Duration: time.Since(began),
			Failed:   err != nil,
		})
		if r.Name == dispatchReconciler {
			timing.DispatchWait = wait
		}
	}
	return errors.Join(errs...)
}

// recoverReconcile calls r.Reconcile and turns a panic into an error the
// same way this func's own caller already treats any other error one
// reconciler returns -- see RunCycle's own "one reconciler having a
// problem shouldn't take the others down": a panic is such a problem too,
// and cmd/grain's own reconcile loop, which calls RunCycle once per tick,
// has nothing of its own left to recover it with by the time it has
// already unwound past this call (bwsalmon/agents#550 -- an unrecovered
// panic here would otherwise crash the whole grain daemon process,
// including the UI/API server it now also serves alongside RunCycle,
// bwsalmon/agents#363).
func recoverReconcile(ctx context.Context, r Reconciler, deps Deps, now time.Time) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %v\n%s", rec, debug.Stack())
		}
	}()
	return r.Reconcile(ctx, deps, now)
}

// reconcileReviews files the review each finished task declares
// (grain/task-284), from the template it names, onto the branch that
// task's own run pushed.
func reconcileReviews(ctx context.Context, deps Deps, now time.Time) error {
	return SyncReviews(ctx, deps.Store, now)
}

// reconcileSecurityHolds gives auto-merge back to every change held for
// touching the trust boundary whose security review finished clean
// (securityhold.go).
func reconcileSecurityHolds(ctx context.Context, deps Deps, now time.Time) error {
	return SyncSecurityHolds(ctx, deps.Store, deps.Client, now)
}

// reconcilePathTriggerHolds gives auto-merge back to every change held by
// a gating path trigger whose tasks have finished (pathtriggerhold.go).
func reconcilePathTriggerHolds(ctx context.Context, deps Deps, now time.Time) error {
	return SyncPathTriggerHolds(ctx, deps.Store, deps.Client, now)
}

// reconcileReviewQuestions sends a change held on its review's open
// questions back to the queue once its owner has answered them
// (reviewquestions.go).
func reconcileReviewQuestions(ctx context.Context, deps Deps, now time.Time) error {
	return SyncReviewQuestions(ctx, deps.Store, now)
}

// reconcileReviewThreads sends a run to deal with review threads somebody
// opened on a grain-owned pull request (reviewthreads.go).
func reconcileReviewThreads(ctx context.Context, deps Deps, now time.Time) error {
	return SyncReviewThreads(ctx, deps.Store, deps.Client, now)
}

// reconcileSync refreshes every pull request grain is still watching --
// told, as the plain SyncPullRequests is not, which repository holds
// this deployment's own settings and what that repository's own rules
// are (StateRepoGate).
func reconcileSync(ctx context.Context, deps Deps, now time.Time) error {
	gate := deps.Config.stateRepoGate()
	gate.CI = deps.CI
	return SyncPullRequestsGuarding(ctx, deps.Store, deps.Client, deps.Config.Verdicts, gate, deps.Config.MaxMergesPerRepo, now)
}

// reconcileReleases carries out whatever GitHub-side effect a release
// candidate still declares (bwsalmon/agents#398) but has not yet had
// performed -- cutting its own branch, or promoting it.
func reconcileReleases(ctx context.Context, deps Deps, now time.Time) error {
	return SyncReleases(ctx, deps.Store, deps.Client, now)
}

// reconcileBranches carries out whatever GitHub-side create a requested
// branch (bwsalmon/agents#638) still declares but has not yet had
// performed.
func reconcileBranches(ctx context.Context, deps Deps, now time.Time) error {
	return SyncBranches(ctx, deps.Store, deps.Client, now)
}

// reconcileQualifications schedules and, once successful, auto-promotes
// each repo's qualification plan (bwsalmon/agents#518) against its own
// active release candidate.
func reconcileQualifications(ctx context.Context, deps Deps, now time.Time) error {
	return SyncQualifications(ctx, deps.Store, now)
}

// reconcileSuites advances every suite run still in flight
// (bwsalmon/agents#642), firing its next pass or stopping it once its
// own Mode says to. Runs last, after "qualifications", for the same
// latency reason that one runs right after "releases": nothing else
// here produces or consumes a suite run, so its own position among the
// rest is a preference, not a dependency.
func reconcileSuites(ctx context.Context, deps Deps, now time.Time) error {
	return syncSuites(ctx, deps.Store, deps.GitReach, now)
}

// reconcileDispatch lets dispatch.Cycle decide what runs, then runs every
// dispatch it decided on concurrently, one goroutine per dispatch --
// handed to deps.Runs, and so outliving this cycle, wherever a caller has
// given it somewhere to park them (Deps.Runs).
//
// This is what actually makes -max-workers/GRAIN_MAX_WORKERS a
// concurrency knob rather than just a scheduling one (bwsalmon/
// agents#435): each Dispatch names a distinct task and a distinct run,
// and each run acquires a sandbox of its own, so two dispatches from the
// same Cycle call never touch the same sandbox, the same model.Run row,
// or the same task -- there is nothing for concurrent runOne calls to
// race over. That used to rest on dispatch.Cycle handing out distinct
// slots from a pool; it now rests on the simpler fact that a sandbox
// belongs to one run and is built for it. model.Store is already built
// for concurrent callers (pkg/model/sqlite's own doc comment: "the
// single writer for the whole store" -- serialization happens inside
// Store, not by a caller taking turns), so nothing here needs to hold a
// lock of its own.
//
// One dispatch failing does not abandon the others. Every Dispatch
// dispatch.Cycle returns already has its own durable store row (its own
// doc comment: "the store write is already durable by the time a Dispatch
// is returned"), so a run that fails here is one whose task is recorded
// as attempted either way — dropping the remaining dispatches on the
// floor would leave that capacity idle for a tick without changing
// anything about the one that failed. errs is appended to under errsMu
// since the goroutines below share it; ctx itself (passed to every
// runOne unchanged) is what stops them early on cancellation -- framework.
// Run and the store calls underneath it all already respect it, so there
// is no separate ctx.Err() check to make before launching them the way
// the old sequential loop needed one before each iteration.
func reconcileDispatch(ctx context.Context, deps Deps, now time.Time) error {
	// Nothing is dispatched onto a credential that has no budget left: a
	// task started now would spend a sandbox and an attempt reaching the
	// same refusal the run that closed that gate already reached
	// (Config.Pause). Only dispatch stops, and only onto the credentials
	// covered -- syncing pull requests, advancing the merge queue and
	// cutting releases cost no agent tokens and are exactly what should
	// keep working while a window is shut, and so do every other team's
	// tasks, and this team's on its other connections.
	//
	// A paused model connection -- or one model paused on it -- is the
	// connection pool's to pass over (Deps.ModelAvailability,
	// Pause.ModelPaused), since only it knows which connection a task's
	// run would land on, which model it would pass the CLI there, and
	// whether the team has another to rotate onto; what is gated here is
	// the pauses on no connection, by team and framework (gatedByPause).
	//
	// Asked once per tick, and level-triggered like every other
	// reconciler: a pause lifts because a later tick found its instant
	// passed, not because anything here holds a timer.
	//
	// Silent per tick on purpose: Pause logs the one line worth having
	// when a window closes and the one worth having when it opens again,
	// and a line per tick for however many hours a provider's window
	// lasts would bury both.
	// Nothing is dispatched without somewhere to dispatch it: a run needs
	// a sandbox, and runOne has no backend to ask for one. This used to
	// be true by accident, through the counts -- a Deps with no Sandboxes
	// is a Deps nobody gave a worker limit either, and dispatch.Cycle
	// read "no counts" as "start nothing". Counts are a team's now and
	// the deployment sets none (grain/task-28), so the invariant is
	// stated here instead of being a side effect of an unset number.
	if deps.Sandboxes == nil {
		return nil
	}

	gated := deps.Config.Pause.Blocked(now)

	// And nothing is picked up while the deployment itself is paused --
	// somebody's decision rather than a provider's refusal
	// (model.PipelinePause), and a row rather than a field in this
	// process, so it survives the restart an upgrade performs. Read here,
	// once per tick, for the reason every other reconciler re-reads its
	// own inputs: a pause taken (or lifted) between two ticks takes
	// effect on the next one with nothing to notify.
	//
	// A pause that names a task is not an early return, because that task
	// is exactly what the pause is waiting for -- it goes to
	// dispatch.Cycle as an exemption, and everything else in the ready
	// list is passed over there. A read that fails stops this reconciler
	// rather than being treated as "not paused": dispatching the whole
	// queue into a deployment somebody has taken out of service is the
	// worse of the two ways to be wrong about it.
	pause, err := pipelinePause(ctx, deps)
	if err != nil {
		return err
	}

	// dispatch.Busy is what keeps a task this process is still finishing
	// with out of this cycle's reach: a run's row is finished before
	// runOne has turned its result into the effects it implies, and a
	// tick landing in that window would otherwise dispatch the same task
	// again. There is no such window without deps.Runs -- a cycle that
	// waits for its own runs has no next tick to fall into it -- which is
	// why the option goes with it.
	var opts []dispatch.Option
	prepared := map[string][]model.Materialized{}
	frameworks := map[string]reservedFramework{}
	if deps.Runs != nil {
		opts = append(opts, dispatch.Busy(deps.Runs.Busy))
	}
	// And the other half of the usage-limit gate above: with something
	// paused, the cycle runs as usual and passes over the tasks a pause
	// on no connection covers (dispatch.PausedFor). Only wired when something is actually paused,
	// so an unpaused deployment -- every ordinary tick -- reads no task
	// rows it would not otherwise have read.
	if gated.Any() {
		opts = append(opts, dispatch.PausedFor(gatedByPause(ctx, deps, gated)))
	}
	// Each hold says why it holds a task, which dispatch logs the skip
	// with.
	var holds []func(string) string
	if !deps.SingleTenant && (deps.RequireTeamGitCredential || deps.TeamHasAgentBackend != nil) {
		holds = append(holds, teamSetupHold(ctx, deps))
	}
	if held := pendingBranchHold(ctx, deps); held != nil {
		holds = append(holds, held)
	}
	if deps.Policy != nil || isUnisolated(deps.Sandboxes) {
		holds = append(holds, policyHold(ctx, deps))
	}
	if held := catalogueHold(ctx, deps); held != nil {
		holds = append(holds, held)
	}
	if len(holds) > 0 {
		opts = append(opts, dispatch.Hold(func(taskID string) string {
			for _, held := range holds {
				if why := held(taskID); why != "" {
					return why
				}
			}
			return ""
		}))
	}
	if pause != nil {
		var exempt []string
		if pause.UntilTaskID != "" {
			exempt = append(exempt, pause.UntilTaskID)
		}
		opts = append(opts, dispatch.Paused(exempt...))
		// Manual mode: what somebody asked to run now is started under
		// any pause but an upgrade's hold, whose restart would discard
		// it (model.PipelinePause.AdmitsRunNow). An upgrade running or
		// scheduled under somebody else's pause is that hold too, though
		// the row does not say so (upgradeUnderway); not knowing counts
		// as one, so a request waits rather than being thrown away.
		//
		// Tasks with priority dispatch -- console sessions and holders
		// of priority-dispatch -- pass the same pauses, by the same test:
		// an operator can always reach the deployment, and the pipeline
		// steward is most needed while it is paused -- except under an
		// upgrade's hold, whose restart would end the run anyway.
		if pause.AdmitsRunNow() {
			underway, err := upgradeUnderway(ctx, deps)
			if err != nil {
				log.Printf("orchestrator: holding run-now requests and priority dispatch behind the pause: %v", err)
			}
			if err == nil && !underway {
				opts = append(opts, dispatch.RunNowPassesPause(), dispatch.PriorityPassesPause())
			}
		}
	}
	// Admission also enforces repository-required Grain CI. Keep it wired
	// even when no scarce capability providers are configured: a missing
	// grain-ci task grant is itself the condition admission must expose.
	opts = append(opts, dispatch.Preempt(preempter(deps, now)))
	opts = append(opts, dispatch.Admit(capabilityAdmitter(deps, grantAdmitterWithFrameworks(ctx, deps, prepared, frameworks, now))))
	limits := model.Limits{
		Workers: deps.MaxWorkers, Mergers: deps.MaxMergers, CIJobs: deps.MaxCIJobs,
		Durable: deps.Config.maxDurableRuns(),
		Budget:  deps.Budget, AgentShape: deps.AgentShape, Ledger: deps.Ledger,
	}
	// And the ledger every one of those admissions is charged against,
	// reconciled first against the runs the store says are live: this
	// process may have come up with runs already in flight (an upgrade
	// restarts the daemon and leaves the sandboxes alone), and a release
	// can be lost by a process that goes away between a run's row
	// finishing and the release that follows it. Either way the budget
	// would be wrong until something re-derived it, and this is the
	// something (model.Ledger.Sync).
	syncSandboxLedger(ctx, deps)
	dispatches, cycleErr := dispatch.Cycle(ctx, deps.Store, limits, now, opts...)
	if cycleErr != nil && len(dispatches) == 0 {
		for _, f := range frameworks {
			releaseFramework(f.framework)
		}
		return fmt.Errorf("orchestrator: %w", cycleErr)
	}

	// With somewhere to park them, a run outlives the cycle that started
	// it: the goroutines below are handed to deps.Runs and this function
	// returns as soon as they are launched. See Deps.Runs and InFlight
	// for why -- in short, waiting here made a cycle as slow as its
	// slowest agent, and cmd/grain's reconcile loop does not tick again
	// until a cycle returns, so a single running task stopped every other
	// task from starting however much of the deployment's capacity was
	// free.
	//
	// An error has nowhere to be returned to once that happens -- the
	// cycle it belonged to is long over -- so it is logged, the same way
	// cmd/grain's own loop logs whatever a cycle returns. Nothing else is
	// lost by that: the run's own outcome and diagnosis are already in
	// the store, on all three of the paths there are to get them there --
	// RunDispatch finishes the row for anything the agent's own run did,
	// runOne's setup guard finishes one whose sandbox never came up, and
	// noteFinishFailure records what went wrong after the run, once
	// RunDispatch has already finished the row. That is where a UI or a
	// retry reads them from, rather than from a cycle's error.
	if deps.Runs != nil {
		for _, d := range dispatches {
			reserved := prepared[d.RunID]
			delete(prepared, d.RunID)
			fw := frameworks[d.RunID]
			delete(frameworks, d.RunID)
			deps.Runs.Go(d.RunID, d.TaskID, func() {
				// The sandbox this run holds stops being charged when the
				// run is over, whatever it is over by: a normal finish, a
				// setup failure, a panic recovered below. Held until then,
				// including while the result is being written up, because
				// the sandbox is still there until runOne releases it.
				defer deps.Ledger.Release(model.PoolAgent, d.RunID)
				defer releaseRunCapabilities(context.WithoutCancel(ctx), deps, d.TaskID, d.RunID)
				if err := recoverRunOneWithFramework(ctx, deps, d, reserved, fw, now); err != nil {
					log.Printf("orchestrator: run %s (task %s): %v", d.RunID, d.TaskID, err)
				}
			})
		}
		for _, f := range frameworks {
			releaseFramework(f.framework)
		}
		if cycleErr != nil {
			return fmt.Errorf("orchestrator: %w", cycleErr)
		}
		return nil
	}

	var errsMu sync.Mutex
	var errs []error
	if cycleErr != nil {
		errs = append(errs, fmt.Errorf("orchestrator: %w", cycleErr))
	}
	var wg sync.WaitGroup
	for _, d := range dispatches {
		reserved := prepared[d.RunID]
		delete(prepared, d.RunID)
		fw := frameworks[d.RunID]
		delete(frameworks, d.RunID)
		wg.Add(1)
		go func(d dispatch.Dispatch, reserved []model.Materialized, fw reservedFramework) {
			defer wg.Done()
			defer deps.Ledger.Release(model.PoolAgent, d.RunID)
			defer releaseRunCapabilities(context.WithoutCancel(ctx), deps, d.TaskID, d.RunID)
			if err := recoverRunOneWithFramework(ctx, deps, d, reserved, fw, now); err != nil {
				errsMu.Lock()
				errs = append(errs, err)
				errsMu.Unlock()
			}
		}(d, reserved, fw)
	}
	for _, f := range frameworks {
		releaseFramework(f.framework)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// SyncSandboxLedger makes the deployment's live commitment ledger agree with
// the runs the store says are live.
func SyncSandboxLedger(ctx context.Context, deps Deps) {
	syncSandboxLedger(ctx, deps)
}

// syncSandboxLedger makes the deployment's live commitment ledger agree
// with the runs the store says are live, once per cycle: a run whose
// charge nobody made is charged at its task shape, and a charge with
// no live run behind it is dropped once it is old enough to be a leak
// rather than this cycle's own work in progress
// (model.LedgerSyncGrace).
//
// Best effort, and silent about a read that fails beyond one log line: a
// ledger that is a tick out of date admits slightly too much or slightly
// too little, and a cycle that refused to dispatch because it could not
// list live runs would be the worse failure.
func syncSandboxLedger(ctx context.Context, deps Deps) {
	if deps.Ledger == nil || deps.Store == nil {
		return
	}
	runs, err := deps.Store.LiveRuns(ctx)
	if err != nil {
		log.Printf("orchestrator: reading live runs to reconcile the sandbox ledger: %v", err)
		return
	}
	limits := model.Limits{AgentShape: deps.AgentShape}
	live := make(map[string]model.Commitment, len(runs))
	for _, r := range runs {
		shape := deps.AgentShape
		if !shape.IsZero() && r.TaskID != "" {
			if t, err := deps.Store.GetTask(ctx, r.TaskID); err == nil && t != nil {
				shape = limits.TaskShape(*t)
			}
		}
		live[r.ID] = shape
	}
	deps.Ledger.Sync(model.PoolAgent, live)
}

type reservedFramework struct {
	framework agent.Framework
	err       error
	built     bool
}

func releaseFramework(f agent.Framework) {
	if r, ok := f.(interface{ Release() }); ok {
		r.Release()
	}
}

// grantAdmitter reserves finite capability resources before dispatch records a
// run. A full pool declines only this candidate, leaving Cycle free to fill the
// same worker capacity with later tasks that do not need it.
//
// It is also where local policy is checked on the task it admits
// (checkPolicy): the dispatch hold (heldByPolicy) read the task a moment
// earlier, before this added its default grants, and an edit landing in
// between -- or a policy refusing the grain-ci grant added here -- has to
// hold the task with its reason before a scarce grant is minted for it,
// rather than fail its run once one has been.
func grantAdmitter(ctx context.Context, deps Deps, prepared map[string][]model.Materialized, now time.Time) dispatch.Admitter {
	return grantAdmitterWithFrameworks(ctx, deps, prepared, nil, now)
}

func grantAdmitterWithFrameworks(ctx context.Context, deps Deps, prepared map[string][]model.Materialized, frameworks map[string]reservedFramework, now time.Time) dispatch.Admitter {
	checksPolicy := deps.Policy != nil || isUnisolated(deps.Sandboxes)
	var defaultNetwork model.NetworkAccess
	if checksPolicy {
		defaultNetwork = deploymentNetworkAccess(ctx, deps.Store)
	}
	admitWithFramework := func(callCtx context.Context, task model.Task, candidate dispatch.Candidate, at time.Time, cc model.CapabilityContext, reserved []model.Materialized) (dispatch.Admission, error) {
		var fw agent.Framework
		if frameworks != nil && deps.Framework != nil {
			var fwErr error
			fw, fwErr = deps.Framework(callCtx, deps.dispatchChoiceFor(callCtx, task))
			if fwErr != nil && deps.ModelAvailability != nil {
				if reason := deps.ModelAvailability(callCtx, deps.choiceFor(task)); reason != "" {
					if len(reserved) > 0 {
						revokeReserved(callCtx, cc, reserved)
					}
					if setErr := deps.Store.SetGrantWait(callCtx, model.GrantWait{
						TaskID: task.ID, Capability: model.ModelWaitCapability, Resource: reason, WaitingAt: at,
					}); setErr != nil {
						return dispatch.Admission{}, setErr
					}
					return dispatch.Admission{}, nil
				}
			}
			frameworks[candidate.RunID] = reservedFramework{
				framework: fw,
				err:       fwErr,
				built:     true,
			}
		}
		var leases []model.Lease
		if len(reserved) > 0 {
			prepared[candidate.RunID] = reserved
			leases = make([]model.Lease, 0, len(reserved))
			for _, materialized := range reserved {
				if lease := materialized.Materialization.Lease; lease != nil {
					leases = append(leases, *lease)
				}
			}
		}
		if len(reserved) == 0 && fw == nil && frameworks == nil {
			return dispatch.Admission{Admitted: true}, nil
		}
		return dispatch.Admission{
			Admitted: true,
			Leases:   leases,
			Release: func() {
				if prepared != nil {
					delete(prepared, candidate.RunID)
				}
				if frameworks != nil {
					delete(frameworks, candidate.RunID)
				}
				releaseFramework(fw)
				if len(reserved) > 0 {
					revokeReserved(context.WithoutCancel(ctx), cc, reserved)
				}
			},
		}, nil
	}
	return func(callCtx context.Context, candidate dispatch.Candidate) (dispatch.Admission, error) {
		task, err := deps.Store.GetTask(callCtx, candidate.TaskID)
		if err != nil {
			return dispatch.Admission{}, err
		}
		if task == nil {
			return dispatch.Admission{Admitted: true}, nil
		}
		at := deps.Config.now()
		if at.IsZero() {
			at = now
		}
		// A task whose model no connection can serve right now waits for
		// one, saying which of the two it is waiting on, rather than being
		// dispatched into a sandbox only to fail setup for want of an agent.
		if deps.ModelAvailability != nil {
			if reason := deps.ModelAvailability(callCtx, deps.choiceFor(*task)); reason != "" {
				if err := deps.Store.SetGrantWait(callCtx, model.GrantWait{
					TaskID: task.ID, Capability: model.ModelWaitCapability, Resource: reason, WaitingAt: at,
				}); err != nil {
					return dispatch.Admission{}, err
				}
				return dispatch.Admission{}, nil
			}
		}
		// Every path out of here that admits the candidate has decided
		// there is nothing left to wait for, so each one clears the
		// marker: StartRun clears it too, but only on the path that
		// records an attempt, and the admitter reaches "nothing is
		// holding this task up" in cases where no run follows.
		requiresGrainCI, err := taskRequiresGrainCI(callCtx, deps.Store, *task)
		if err != nil {
			return dispatch.Admission{}, err
		}
		if requiresGrainCI && !grainci.Granted(task.Grants) {
			if err := deps.Store.UpdateTask(callCtx, task.ID, func(t *model.Task) error {
				if !grainci.Granted(t.Grants) {
					t.Grants = append(t.Grants, model.Grant{
						Capability: grainci.CapabilityName,
						Via:        model.GrantByDefault,
					})
				}
				return nil
			}); err != nil {
				return dispatch.Admission{}, err
			}
			task.Grants = append(task.Grants, model.Grant{
				Capability: grainci.CapabilityName,
				Via:        model.GrantByDefault,
			})
			_ = clearAdmissionWait(callCtx, deps.Store, task.ID)
		}
		// Local policy judges the task this admission is for, default
		// grants and all, before anything below resolves or mints a grant
		// for it. runOne checks once more as a backstop.
		if checksPolicy {
			if err := checkPolicy(callCtx, deps, *task, defaultNetwork); err != nil {
				if setErr := recordPolicyRefusal(callCtx, deps, *task, policyStageAdmission, err, at); setErr != nil {
					return dispatch.Admission{}, setErr
				}
				return dispatch.Admission{}, nil
			}
		}
		if deps.Config.Capabilities == nil || !hasScarceGrant(deps.Config.Capabilities, *task) {
			if err := clearAdmissionWait(callCtx, deps.Store, task.ID); err != nil {
				return dispatch.Admission{}, err
			}
			return admitWithFramework(callCtx, *task, candidate, at, model.CapabilityContext{}, nil)
		}
		cc := model.CapabilityContext{
			Task: *task,
			Run: model.Run{ID: candidate.RunID, TaskID: candidate.TaskID, Sandbox: candidate.RunID,
				Attempt: candidate.Attempt, StartedAt: at},
			Now: at, Credentials: deps.Config.Credentials,
		}
		resolved, err := model.ResolveGrants(callCtx, deps.Config.Capabilities, cc)
		if err != nil {
			return dispatch.Admission{}, err
		}
		reserved, err := model.MaterializeScarceGrants(callCtx, deps.Config.Capabilities, cc, resolved)
		if exhausted, waiting := model.ResourceExhausted(err); waiting {
			revokeReserved(callCtx, cc, reserved)
			resource := exhausted.Resource
			if resource == "" {
				resource = "capability resources"
			}
			capability := exhausted.Capability
			if capability == "" {
				capability = "capability"
			}
			if setErr := deps.Store.SetGrantWait(callCtx, model.GrantWait{
				TaskID: task.ID, Capability: capability, Resource: resource, WaitingAt: at,
			}); setErr != nil {
				return dispatch.Admission{}, setErr
			}
			return dispatch.Admission{}, nil
		}
		if err != nil {
			// Preserve the established behavior for configuration and provider
			// failures: they become a setup-failed attempt with a diagnosis.
			revokeReserved(callCtx, cc, reserved)
			_ = clearAdmissionWait(callCtx, deps.Store, task.ID)
			return admitWithFramework(callCtx, *task, candidate, at, cc, nil)
		}
		if err := clearAdmissionWait(callCtx, deps.Store, task.ID); err != nil {
			revokeReserved(callCtx, cc, reserved)
			return dispatch.Admission{}, err
		}
		return admitWithFramework(callCtx, *task, candidate, at, cc, reserved)
	}
}

// clearAdmissionWait clears taskID's wait if it is one grantAdmitter
// owns -- a model, local-policy, grain-ci or capability wait -- and
// leaves the ones a dispatch hold records and keeps (heldForTeamSetup,
// heldForPendingBranch) to the hold that recorded them.
func clearAdmissionWait(ctx context.Context, store *model.Store, taskID string) error {
	return store.ClearGrantWaitUnless(ctx, taskID, model.TeamSetupWaitCapability, model.BranchWaitCapability)
}

// taskRequiresGrainCI reports whether the task's target repository is
// configured to require Grain CI through either layer of task defaults. A
// task's grants remain pinned at creation, so a missing grant is not silently
// added here; admission leaves a visible, recoverable wait for the operator.
func taskRequiresGrainCI(ctx context.Context, store *model.Store, task model.Task) (bool, error) {
	if task.Target == nil {
		return false, nil
	}
	grants, err := defaultTaskGrants(ctx, store, task.Target, model.DefaultTeamID)
	if err != nil {
		return false, err
	}
	return grainci.Granted(grants), nil
}

func hasScarceGrant(reg *model.CapabilityRegistry, task model.Task) bool {
	for _, grant := range task.Grants {
		if provider, ok := reg.Lookup(grant.Capability); ok && model.IsScarceResourceProvider(provider) {
			return true
		}
	}
	return false
}

func revokeReserved(ctx context.Context, cc model.CapabilityContext, reserved []model.Materialized) {
	for _, materialized := range reserved {
		if lease := materialized.Materialization.Lease; lease != nil {
			if err := materialized.Provider.Revoke(ctx, cc, *lease); err != nil {
				log.Printf("orchestrator: task %s: releasing pre-dispatch capability %q: %v", cc.Task.ID, materialized.Grant.Capability, err)
			}
		}
	}
}

// gatedByPause is the predicate dispatch.PausedFor wants: whether a
// usage-limit pause in force this tick (gated, read once at the top of
// reconcileDispatch) covers taskID's team and the framework it would be
// dispatched onto -- the scope of a run on no model connection
// (PauseScope). A pause on a connection is not asked about here: the
// connection pool passes that connection over, and holds the task only
// when its team has nothing else to run it on (Deps.ModelAvailability).
//
// The lookup is a task row per candidate, which is why the caller only
// wires it while something is paused: the team and framework a task runs
// on are its own (frameworkFor), and the ready list carries neither. It is one small read against the same
// store the cycle is already reading, and only for the tasks a cycle
// still has capacity to consider.
//
// A row that cannot be read is treated as covered -- passed over rather
// than dispatched blind. That is the same way round reconcileDispatch
// reads a failed pipeline-pause read: dispatching a task into a window
// that has already refused it costs an attempt and a sandbox, while
// passing one over costs a tick, and the next tick asks again.
func gatedByPause(ctx context.Context, deps Deps, gated Coverage) func(string) bool {
	var backends []model.AgentBackend
	var defaultBackendID string
	if deps.Store != nil {
		if cfg, err := deps.Store.GetConfig(ctx); err == nil && cfg != nil {
			backends = model.DeploymentAgentBackends(cfg.AgentBackends, true)
			defaultBackendID = strings.TrimSpace(cfg.AgentBackendID)
		}
	}
	return func(taskID string) bool {
		task, err := deps.Store.GetTask(ctx, taskID)
		if err != nil {
			log.Printf("orchestrator: reading %s to see whether a usage-limit pause covers it: %v", taskID, err)
			return true
		}
		if task == nil {
			return true
		}
		fw := frameworkFor(*task, deps.Config)
		connection := strings.TrimSpace(task.AgentBackendID)
		modelName := strings.TrimSpace(task.Model)
		team := model.DefaultTeamID
		if connection == "" && strings.TrimSpace(task.AgentFramework) == "" && modelName == "" && defaultBackendID != "" {
			if findAgentBackend(backends, defaultBackendID) != nil {
				connection = defaultBackendID
			}
		}
		if connection != "" {
			if b := findAgentBackend(backends, connection); b != nil {
				connection = b.CredentialOwnerID()
				team = model.ConnectionTeam(*b)
				if modelName == "" {
					modelName = b.EffectiveModel()
				}
			}
		}
		reason := gated.Reason(PauseScope{
			Connection: connection,
			Model:      modelName,
			Team:       team,
			Framework:  fw,
		})
		if reason == "" {
			return false
		}
		if existing, err := deps.Store.GrantWaitOf(ctx, task.ID); err == nil && existing != nil &&
			existing.Capability != model.QuotaWaitCapability {
			return true
		}
		if err := deps.Store.SetGrantWait(ctx, model.GrantWait{
			TaskID:     task.ID,
			Capability: model.QuotaWaitCapability,
			Resource:   reason,
			WaitingAt:  deps.Config.now(),
		}); err != nil {
			log.Printf("orchestrator: recording why %s waits for a usage-limit pause: %v", task.ID, err)
		}
		return true
	}
}

func findAgentBackend(backends []model.AgentBackend, id string) *model.AgentBackend {
	id = strings.TrimSpace(id)
	for i := range backends {
		if backends[i].ID == id {
			return &backends[i]
		}
	}
	return nil
}

// heldIf is a hold that says why (teamSetupHold and the rest) as the
// predicate its tests ask.
func heldIf(why func(string) string) func(string) bool {
	return func(taskID string) bool { return why(taskID) != "" }
}

// teamSetupHold is the hold when the team a
// task belongs to has to have set something up for itself before the
// task can run: a provider connection (deps.TeamHasAgentBackend), and a
// GitHub credential (deps.RequireTeamGitCredential). Both are a team
// admin's to set in Team settings, and neither has a deployment-wide
// fallback, so a task whose team lacks either waits queued for it. A
// row that cannot be read is held, the same way round gatedByPause
// reads one. It returns why it holds a task -- the
// sentence it records on the task -- or "" when it does not hold it.
func teamSetupHold(ctx context.Context, deps Deps) func(string) string {
	gitCredential := heldForGitCredential(ctx, deps)
	return func(taskID string) string {
		task, err := deps.Store.GetTask(ctx, taskID)
		if err != nil || task == nil {
			if err != nil {
				log.Printf("orchestrator: reading %s to see whether its team is set up: %v", taskID, err)
			}
			return "held for setup: the task could not be read to see whether its team is set up"
		}
		// Either hold is said on the task, the way a model wait is
		// (grantAdmitter), so the task pane names what its team has to set
		// up and where, rather than showing a queued task with no reason.
		team := model.DefaultTeamID
		if deps.TeamHasAgentBackend != nil && !deps.TeamHasAgentBackend(ctx, team) {
			why := noConnectionWait
			setTeamSetupWait(ctx, deps, task.ID, why)
			return "held for setup: " + why
		}
		if deps.RequireTeamGitCredential && gitCredential(task) {
			why := fmt.Sprintf(noGitCredentialWait, *task.Target)
			setTeamSetupWait(ctx, deps, task.ID, why)
			return "held for setup: " + why
		}
		// Nothing the team has to set up holds the task any longer. The
		// admitter replaces or clears the wait when it reaches the task,
		// but a cycle out of capacity never does, and the sentence would
		// go on naming a setting the team admin has already made.
		clearTeamSetupWait(ctx, deps.Store, task.ID)
		return ""
	}
}

// pendingBranchHold holds a task whose base -- its own, or a write
// target's -- is a branch grain has been asked to create and has not
// yet (model.BranchPending): the new-task form's "create a new branch"
// (grain/task-2317) files the task and the request together, and the
// branches reconciler runs after dispatch in a cycle, so without this
// the task's first clone would find no such branch and park it
// (stopOnMissingBase). The task is started by the first cycle after the
// branch is cut or adopted.
//
// nil when nothing is pending, which is every ordinary tick, so a
// deployment with no requested branches reads no task rows for it. A
// failed read is logged and holds nothing: dispatch still parks a task
// whose base truly is not there, with a sentence saying so.
func pendingBranchHold(ctx context.Context, deps Deps) func(string) string {
	pending, err := deps.Store.PendingBranches(ctx)
	if err != nil {
		log.Printf("orchestrator: reading pending branches to hold their tasks: %v", err)
		return nil
	}
	if len(pending) == 0 {
		return nil
	}
	return func(taskID string) string {
		task, err := deps.Store.GetTask(ctx, taskID)
		if err != nil || task == nil {
			return ""
		}
		b, ok := pendingBase(*task, pending)
		if !ok {
			return ""
		}
		reason := fmt.Sprintf("branch %q is not created on %s yet", b.Name, b.Repo)
		if b.LastError != "" {
			reason += " -- " + b.LastError
		}
		if err := deps.Store.SetGrantWait(ctx, model.GrantWait{
			TaskID: task.ID, Capability: model.BranchWaitCapability, Resource: reason, WaitingAt: deps.Config.now(),
		}); err != nil {
			log.Printf("orchestrator: recording why %s waits for its branch: %v", task.ID, err)
		}
		return "held for its branch: " + reason
	}
}

// catalogueHold re-judges a queued task's grants against the capability
// catalogue (settings/capabilities.yml) before anything is minted or a
// sandbox started for it (docs/design/config-repo-settings.md §3.4): a
// config change merged after the task was filed may have revoked a
// capability or narrowed who may hold it, and the task waits, saying
// which grant and why (model.CatalogueWaitCapability), rather than
// running with it. A wait it recorded is cleared once the task passes.
// nil when the deployment has no catalogue. Like policyHold it fails
// closed: a catalogue that cannot be read holds every task this cycle,
// and a task that cannot be read is held, rather than either being
// dispatched unjudged.
func catalogueHold(ctx context.Context, deps Deps) func(string) string {
	cfg, err := deps.Store.GetConfig(ctx)
	if err != nil {
		log.Printf("orchestrator: reading the capability catalogue: %v", err)
		return func(string) string {
			return "held by settings/capabilities.yml: the capability catalogue could not be read to check it"
		}
	}
	if cfg == nil || cfg.CapabilityPolicy.Catalogue == nil {
		return nil
	}
	cat := cfg.CapabilityPolicy.Catalogue
	return func(taskID string) string {
		task, err := deps.Store.GetTask(ctx, taskID)
		if err != nil || task == nil {
			if err != nil {
				log.Printf("orchestrator: reading %s to check it against the capability catalogue: %v", taskID, err)
			}
			return "held by settings/capabilities.yml: the task could not be read to check it"
		}
		gc := model.TaskGrantContext(*task)
		if gc.TemplateID != "" {
			if tmpl, err := deps.Store.GetTemplate(ctx, gc.TemplateID); err == nil && tmpl != nil {
				gc.TemplateName = tmpl.Name
			}
		}
		if _, err := cat.ValidateGrants(gc); err != nil {
			if serr := deps.Store.SetGrantWait(ctx, model.GrantWait{
				TaskID: task.ID, Capability: model.CatalogueWaitCapability, Resource: err.Error(), WaitingAt: deps.Config.now(),
			}); serr != nil {
				log.Printf("orchestrator: recording why the capability catalogue holds %s: %v", taskID, serr)
			}
			return "held by settings/capabilities.yml: " + err.Error()
		}
		if wait, err := deps.Store.GrantWaitOf(ctx, taskID); err == nil && wait != nil && wait.Capability == model.CatalogueWaitCapability {
			if err := deps.Store.ClearGrantWait(ctx, taskID); err != nil {
				log.Printf("orchestrator: clearing %s's capability catalogue wait: %v", taskID, err)
			}
		}
		return ""
	}
}

// heldByPolicy holds a task deps.Policy refuses, saying on the task
// which claim it refused (model.PolicyWaitCapability). It fails closed: a
// task that cannot be read is held too. A wait it recorded earlier is
// cleared once the policy allows the task, so a fixed policy file does
// not leave a stale reason behind.
func heldByPolicy(ctx context.Context, deps Deps) func(string) bool {
	return heldIf(policyHold(ctx, deps))
}

// policyHold is heldByPolicy saying why it holds a task: "held by local
// policy:" and the refusal, so the daemon log names the rule a held task broke
// rather than leaving its operator to find it on the task.
func policyHold(ctx context.Context, deps Deps) func(string) string {
	defaultNetwork := deploymentNetworkAccess(ctx, deps.Store)
	return func(taskID string) string {
		task, err := deps.Store.GetTask(ctx, taskID)
		if err != nil || task == nil {
			if err != nil {
				log.Printf("orchestrator: reading %s to check it against local policy: %v", taskID, err)
			}
			return "held by local policy: the task could not be read to check it"
		}
		if err := checkPolicy(ctx, deps, *task, defaultNetwork); err != nil {
			if serr := recordPolicyRefusal(ctx, deps, *task, policyStageHold, err, deps.Config.now()); serr != nil {
				log.Printf("orchestrator: recording why local policy holds %s: %v", taskID, serr)
			}
			return "held by local policy: " + err.Error()
		}
		if wait, err := deps.Store.GrantWaitOf(ctx, taskID); err == nil && wait != nil && wait.Capability == model.PolicyWaitCapability {
			if err := deps.Store.ClearGrantWait(ctx, taskID); err != nil {
				log.Printf("orchestrator: clearing %s's local policy wait: %v", taskID, err)
			}
		}
		return ""
	}
}

// Where local policy refused a task, as a policy audit entry names it
// (policy.AuditEntry.Stage).
const (
	policyStageHold      = "dispatch hold"
	policyStageAdmission = "admission"
	policyStageSandbox   = "before the sandbox"
)

// recordPolicyRefusal holds task with refusal as its local-policy wait
// and, when that changes the wait it had -- none, another kind, or
// another reason -- writes the refusal to deps.PolicyAudit. The hold is
// asked every cycle, so the wait is what keeps a task refused for the
// same reason from being written down once a cycle; a changed reason, or
// a refusal after the wait was cleared (a run started, a policy that
// allowed the task for a while), is a new entry. A wait that cannot be
// read counts as changed: an entry too many rather than one missing. The
// entry is written even when the wait cannot be.
func recordPolicyRefusal(ctx context.Context, deps Deps, task model.Task, stage string, refusal error, at time.Time) error {
	reason := refusal.Error()
	if deps.PolicyAudit != nil {
		prev, err := deps.Store.GrantWaitOf(ctx, task.ID)
		if err != nil || prev == nil || prev.Capability != model.PolicyWaitCapability || prev.Resource != reason {
			e := policy.AuditEntry{Time: at.UTC(), Message: policy.AuditLocalPolicy, Task: task.ID,
				Outcome: policy.AuditRefused, Reason: reason, Stage: stage}
			if task.Target != nil {
				e.Repo = task.Target.String()
			}
			if r, err := rootRequester(ctx, deps.Store, task); err == nil {
				e.Requester = r.Name
			} else {
				e.Reason += " (root requester unreadable: " + err.Error() + ")"
			}
			deps.PolicyAudit(e)
		}
	}
	return deps.Store.SetGrantWait(ctx, model.GrantWait{
		TaskID: task.ID, Capability: model.PolicyWaitCapability, Resource: reason, WaitingAt: at,
	})
}

// checkPolicy checks task against deps.Policy, as requested by its root
// requester (rootRequester). On a deployment whose
// sandboxes isolate nothing from this host it also refuses a task that is
// not this machine's own (checkUnisolated) -- with or without a policy.
func checkPolicy(ctx context.Context, deps Deps, task model.Task, defaultNetwork model.NetworkAccess) error {
	if err := checkUnisolated(ctx, deps, task); err != nil {
		return err
	}
	if deps.Policy == nil {
		return nil
	}
	requester, err := rootRequester(ctx, deps.Store, task)
	if err != nil {
		return fmt.Errorf("local policy cannot resolve who the task was requested by: %w", err)
	}
	return deps.Policy.CheckAs(task, requester, defaultNetwork)
}

// maxRequesterChain bounds rootRequester's walk. Chains are a handful of
// tasks deep; one longer than this is judged unresolved, not walked on.
const maxRequesterChain = 64

// rootRequester is who local policy judges task as requested by: the
// first Creator met walking from task up through the tasks it was
// derived from (model.Task.DerivedFrom). Tasks filed since derived work began inheriting
// its requester (model.InheritRequester) name it themselves; the walk is
// what judges one filed before that, or by a path that does not, as its
// root's rather than as grain's own. A chain naming a task that is not
// stored, or one that loops or runs too deep, is unresolved -- an
// external request, never grain's.
func rootRequester(ctx context.Context, store *model.Store, task model.Task) (policy.Requester, error) {
	var r policy.Requester
	seen := map[string]bool{}
	for cur := &task; ; {
		if r.Name == "" {
			r.Name = strings.TrimSpace(cur.Creator)
		}
		from := cur.DerivedFrom()
		if from == "" {
			return r, nil
		}
		seen[cur.ID] = true
		if seen[from] || len(seen) > maxRequesterChain {
			r.Unresolved = true
			return r, nil
		}
		next, err := store.GetTask(ctx, from)
		if err != nil {
			return policy.Requester{}, err
		}
		if next == nil {
			r.Unresolved = true
			return r, nil
		}
		cur = next
	}
}

// deploymentNetworkAccess is the network access a task asking for none
// of its own gets. A config that cannot be read answers "", which
// model.NormalizeNetworkAccess reads as open -- the widest, so a policy
// limiting network refuses rather than admits on a failed read.
func deploymentNetworkAccess(ctx context.Context, store *model.Store) model.NetworkAccess {
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.NetworkAccess
}

// taskBaseTarget returns the primary repository and base branch the task targets.
func taskBaseTarget(task *model.Task) (model.RepoRef, string) {
	return model.TaskBaseBranch(*task)
}

// pendingBase is the pending branch request task's base names, if any.
func pendingBase(task model.Task, pending []model.Branch) (model.Branch, bool) {
	for _, b := range pending {
		if task.Target != nil && task.Base == b.Name && task.Target.Same(b.Repo) {
			return b, true
		}
		for _, w := range task.Writes {
			if w.Base == b.Name && w.Repo.Same(b.Repo) {
				return b, true
			}
		}
	}
	return model.Branch{}, false
}

// noConnectionWait is the wait a task shows while the deployment has no
// enabled provider connection.
const noConnectionWait = "the deployment has no enabled provider connection -- add one in Settings -> Models (/settings/models)"

// noGitCredentialWait is the wait a task shows while the deployment has
// no GitHub credential for its target, given the target.
const noGitCredentialWait = "the deployment has no GitHub credential for %s -- set one in Settings -> Git (/settings/git)"

// setTeamSetupWait records reason as taskID's wait, under
// model.TeamSetupWaitCapability. A failure is logged and otherwise
// ignored: the task is held either way, and the next cycle tries again.
func setTeamSetupWait(ctx context.Context, deps Deps, taskID, reason string) {
	if err := deps.Store.SetGrantWait(ctx, model.GrantWait{
		TaskID: taskID, Capability: model.TeamSetupWaitCapability, Resource: reason, WaitingAt: deps.Config.now(),
	}); err != nil {
		log.Printf("orchestrator: recording why %s waits for its team's setup: %v", taskID, err)
	}
}

// clearTeamSetupWait removes taskID's wait if it is a team-setup one,
// leaving any other (a model or capability wait the admitter recorded)
// alone.
func clearTeamSetupWait(ctx context.Context, store *model.Store, taskID string) {
	wait, err := store.GrantWaitOf(ctx, taskID)
	if err != nil || wait == nil || wait.Capability != model.TeamSetupWaitCapability {
		return
	}
	if err := store.ClearGrantWait(ctx, taskID); err != nil {
		log.Printf("orchestrator: clearing %s's wait for its team's setup: %v", taskID, err)
	}
}

// heldForGitCredential is the half of heldForTeamSetup that
// deps.RequireTeamGitCredential turns on: whether task's team has no
// GitHub credential its target could be cloned with. A task with no
// target, or one targeting a repository the controller serves itself,
// never needs one and is never held.
func heldForGitCredential(ctx context.Context, deps Deps) func(*model.Task) bool {
	return func(task *model.Task) bool {
		taskID := task.ID
		if task.Target == nil || task.Target.Owner == "" {
			return false
		}
		// grain's own configuration repository is served by the
		// controller itself (gitproxy.LocalRepository) and need no
		// GitHub credential at all.
		if model.IsConfigRepo(*task.Target) {
			return false
		}
		// On the target's own git host: a team whose only connection is
		// on a plain git host is not held for lacking one on GitHub.
		_, err := deps.Store.TaskGitCredentialOn(ctx, taskID, task.Target.Host, task.Target.Owner, task.Target.Name)
		if err == nil {
			return false
		}
		if !errors.Is(err, model.ErrNoTeamGitCredential) {
			log.Printf("orchestrator: resolving the GitHub credential of %s: %v", taskID, err)
		}
		return true
	}
}

// recoverRunOne wraps runOne with the same panic-to-error conversion
// recoverReconcile (cycle.go, above) gives every Reconciler, for the same
// "one dispatch failing does not abandon the others" reason this func's
// own caller already documents -- and for a reason unique to running in
// its own goroutine, one per concurrent dispatch: recover only ever
// catches a panic on the same goroutine it was deferred on, so
// RunCycle's/recoverReconcile's own recover, one level up, could never
// have caught a panic here anyway. Left unrecovered, it would crash the
// whole grain daemon process -- UI/API server included
// (bwsalmon/agents#550) -- over what should only ever cost this one
// dispatch its run.
//
// Converting the panic is not the whole of it: the run's row has to be
// finished too. runOne's own setup guard reads its named error, which a
// panic never sets, and RunDispatch finishes the row only on the paths it
// returns from -- so a panic anywhere in between left a live row behind,
// with all the cost runOne's own comment lists for one (a task reading
// 'running' forever, a worker lost) until the daemon was restarted.
// finishPanickedRun closes that, and only that: a row something already
// finished keeps the outcome it was given.
func recoverRunOneWithFramework(ctx context.Context, deps Deps, d dispatch.Dispatch, prepared []model.Materialized, preBuilt reservedFramework, now time.Time) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic running dispatch %s (task %s): %v\n%s", d.RunID, d.TaskID, rec, debug.Stack())
			if ferr := finishPanickedRun(ctx, deps.Store, d.RunID, now, rec); ferr != nil {
				err = errors.Join(err, ferr)
			}
		}
	}()
	return runOneWithFramework(ctx, deps, d, prepared, preBuilt, now)
}

// finishPanickedRun finishes runID as "failed" if it is still live, on a
// context detached from ctx's cancellation for the reason runOne's setup
// guard gives. The detail carries the panic's value but not its stack,
// which is long, belongs in the daemon's journal, and is logged there by
// recoverRunOne's caller.
func finishPanickedRun(ctx context.Context, store *model.Store, runID string, now time.Time, rec any) error {
	if store == nil {
		return nil
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancel()
	live, err := store.LiveRuns(finishCtx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading live runs to finish run %s after a panic: %w", runID, err)
	}
	for _, r := range live {
		if r.ID != runID {
			continue
		}
		if err := store.FinishRun(finishCtx, runID, now, "failed",
			fmt.Sprintf("grain itself panicked while running this dispatch: %v", rec)); err != nil {
			return fmt.Errorf("orchestrator: finishing run %s after a panic: %w", runID, err)
		}
		return nil
	}
	return nil
}

func runOne(ctx context.Context, deps Deps, d dispatch.Dispatch, prepared []model.Materialized, now time.Time) error {
	return runOneWithFramework(ctx, deps, d, prepared, reservedFramework{}, now)
}

func runOneWithFramework(ctx context.Context, deps Deps, d dispatch.Dispatch, prepared []model.Materialized, preBuilt reservedFramework, now time.Time) (err error) {
	// Everything between here and RunDispatch is this run's setup, and any
	// of it can fail: a granule VM has to boot, a token has to be minted, a
	// guest has to accept a git config. dispatch.Cycle has already made
	// this run durable (its own doc comment), and RunDispatch is what
	// finishes it on every path once it takes over -- so a setup failure
	// returning straight out of here would leave the row live with nothing
	// left to finish it.
	//
	// That is not merely untidy. task_state reads a live run as 'running',
	// so the task never returns to 'queued' and task_ready never offers it
	// again; LiveRunCount still counts the row, so the deployment
	// permanently loses one unit of -max-workers; and retryEligible
	// reads *finished* runs, so the backoff that is supposed to retry a
	// transient failure never sees one to retry. Nothing sweeps it either:
	// Config.MaxRunRuntime is enforced inside RunDispatch, and
	// RecoverOrphanedRuns is a startup pass, so the wedge lasts until the
	// daemon is restarted by hand.
	//
	// It mattered less when a slot's VM was built once at startup and
	// ToolsFor was a cheap lookup after that. A sandbox per run puts a VM
	// boot -- with a ReadyTimeout measured in minutes -- on the setup path
	// of every single dispatch, so this is now the ordinary way a run
	// fails rather than a first-boot curiosity.
	//
	// Finishing it here is what makes bwsalmon/agents#576's successor true:
	// a transient failure costs one run, and dispatch's own backoff
	// (retryBackoff, and MaxConsecutiveFailures behind it) retries the
	// task rather than a human noticing a log line. The outcome is its own
	// word rather than "failed" because no agent ever ran: nothing was
	// attempted that could have gone wrong on its own terms.
	//
	// Detached from ctx's cancellation for the same reason the release
	// below is: a daemon shutting down mid-Acquire is exactly when this
	// row would otherwise be stranded.
	//
	// Whatever setup phrase was standing when it broke is left standing on
	// the finished row, deliberately: nothing renders a finished run's
	// synopsis as current (Store.TaskActivity reads live runs only, and
	// state.js's runActivity checks the task is still running), so it
	// contradicts nothing, and where the run got to before it failed is
	// exactly the context the detail beside it does not carry -- "this
	// run's sandbox could not be prepared: ..." plus "cloning
	// acme/widgets" is a better record than the detail alone.
	//
	// Installed before the task is even read, so a store that cannot
	// answer GetTask below -- or a task deleted between being dispatched
	// and being read -- finishes the row as well, rather than being the
	// one setup failure the guard did not cover. A panic is
	// recoverRunOne's to finish (finishPanickedRun): err is still nil
	// while one unwinds through here.
	ranAgent := false
	defer func() {
		if ranAgent || err == nil {
			return
		}
		// ctx is the daemon's: done here means the daemon is stopping
		// under a run still in setup, which hands the run back rather
		// than failing it (finishStoppedInSetup).
		if ctx.Err() != nil {
			if ferr := finishStoppedInSetup(ctx, deps.Store, d.TaskID, d.RunID, now); ferr != nil {
				err = errors.Join(err, fmt.Errorf("orchestrator: finishing run %s stopped in setup: %w", d.RunID, ferr))
			}
			err = fmt.Errorf("orchestrator: run %s: %w", d.RunID, errors.Join(errDaemonStopped, err))
			return
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
		defer cancel()
		// A durable task's setup failure is a suspension rather than a
		// strike (finishSetupFailed); a full cluster is capacity for
		// every task alike.
		outcome, detail := setupFailedEnding(err)
		finish := func() error { return deps.Store.FinishRun(finishCtx, d.RunID, now, outcome, detail) }
		if outcome == model.SetupFailedOutcome {
			finish = func() error { return finishSetupFailed(finishCtx, deps.Store, d.TaskID, d.RunID, now, detail) }
		}
		if ferr := finish(); ferr != nil {
			err = errors.Join(err, fmt.Errorf("orchestrator: finishing run %s after its setup failed: %w", d.RunID, ferr))
		}
	}()

	var leasedFramework agent.Framework
	if preBuilt.built {
		leasedFramework = preBuilt.framework
	}
	// detached is set when the run was left running in its run host as
	// the daemon stopped (agent.ErrDetached): its sandbox and the token
	// it reaches the git proxy with are the adopting daemon's to release
	// (ResumeAdoptedRun), not this one's.
	detached := false
	defer func() {
		if detached {
			return
		}
		releaseFramework(leasedFramework)
	}()

	// The scarce leases admission reserved for this run are given back on
	// every setup failure, and installed before the task is read for the
	// reason the guard above is: a GetTask that fails below would
	// otherwise leave them spent until their own expiry. A task that could
	// not be read revokes as its bare ID. No provider's Revoke needs more:
	// each works from the lease and the credentials; gcpkey's KeyProvider
	// looks its key up by name, not by anything on cc.Task.
	var task *model.Task
	preparedHandedOff := len(prepared) == 0
	defer func() {
		if preparedHandedOff {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
		defer cancel()
		read := model.Task{ID: d.TaskID}
		if task != nil {
			read = *task
		}
		cc := model.CapabilityContext{
			Task: read,
			Run:  model.Run{ID: d.RunID, TaskID: d.TaskID, Sandbox: d.RunID, Attempt: d.Attempt, StartedAt: now},
			Now:  now, Credentials: deps.Config.Credentials,
		}
		revokeAll(cleanupCtx, deps.Store, cc, prepared)
	}()

	task, err = deps.Store.GetTask(ctx, d.TaskID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading task %s: %w", d.TaskID, err)
	}
	if task == nil {
		return fmt.Errorf("orchestrator: dispatch.Cycle dispatched unknown task %s", d.TaskID)
	}

	// The local policy is checked once more here, against the task as
	// this run read it, before anything is built or cloned: the dispatch
	// hold (heldByPolicy) and the admitter (grantAdmitter) read it a
	// moment earlier, and an edit landing in between must not slip past
	// the workstation owner's policy. This is the backstop; the
	// admitter's check is the one that holds the task with its reason.
	if err := checkPolicy(ctx, deps, *task, deploymentNetworkAccess(ctx, deps.Store)); err != nil {
		if serr := recordPolicyRefusal(ctx, deps, *task, policyStageSandbox, err, deps.Config.now()); serr != nil {
			log.Printf("orchestrator: recording why local policy refuses %s: %v", d.TaskID, serr)
		}
		return fmt.Errorf("orchestrator: local policy refuses task %s: %w", d.TaskID, err)
	}
	if preBuilt.built && preBuilt.err != nil {
		return fmt.Errorf("orchestrator: building the agent framework for run %s: %w", d.RunID, preBuilt.err)
	}

	// What grain itself is doing, on the task's own row, for the stretch
	// before there is an agent to say it -- see setupNotes. Every call
	// below is best-effort and none of them can fail this run.
	notes := setupNotes{store: deps.Store, taskID: d.TaskID, now: deps.Config.now}

	// The sandbox this run gets is named after the run itself. Nothing
	// else is in a position to name it: it is built for this run and torn
	// down with it, so there is no longer-lived thing (a slot, a pool
	// entry) whose name it could inherit, and the run's own ID is already
	// unique, already durable, and already what a log line or a
	// `granule vm list` would most usefully show.
	//
	// Recording it in the store is what lets the git proxy resolve a
	// caller back to this run's task (Store.GitScope), so it has to
	// happen before anything inside the sandbox can reach the proxy --
	// which the ordering here gives: the checkout RunDispatch performs is
	// the run's own first use of it.
	//
	// The target goes down with it, out of the task this dispatch read
	// once at the top of this function and works from for the rest of the
	// run -- the repo prepareCheckout is about to clone, and so the one
	// this run may push. Pinning it is what stops a repo edit made while
	// the run is in flight from moving the proxy's answer out from under
	// it: see SetRunSandbox and GitScope.
	sandboxName := d.RunID
	if err := deps.Store.SetRunSandbox(ctx, d.RunID, sandboxName, task.Target); err != nil {
		return fmt.Errorf("orchestrator: recording run %s's sandbox: %w", d.RunID, err)
	}

	// A task's own SandboxCPUs/SandboxMemoryMB/SandboxDiskGB
	// (bwsalmon/agents#534, grain/task-41) is a create-time argument now
	// rather than something applied to an already-built sandbox: this one
	// is built for this run, so its size is decided once, here, and goes
	// away with it.
	// NetworkAccess rides along as the fourth dimension (grain/task-269):
	// it is decided when the sandbox is created for exactly the same
	// reason a size is, and the backend resolves it against the
	// deployment default per dimension like the rest.
	shape := Shape{
		CPUs:          task.SandboxCPUs,
		MemoryMB:      task.SandboxMemoryMB,
		DiskGB:        task.SandboxDiskGB,
		NetworkAccess: task.NetworkAccess,
		// Read here, before the sandbox exists, because a backend that
		// can mount grain-src/ has to know at create time
		// (placeGrainSource).
		GrainSource: hasGrant(*task, model.SelfDebugCapability),
		Class:       sandboxClassFor(*task),
	}
	// The operator's ambient credentials (-host-sandbox-env) go to a run
	// only on the backend that has them, and only for a task whose whole
	// tree of work is this machine's own: anybody else's task never acts
	// as the owner (GSA1-05).
	if isUnisolated(deps.Sandboxes) {
		if shape.OperatorEnv, err = localTask(ctx, deps.Store, deps.Policy, *task); err != nil {
			return fmt.Errorf("orchestrator: deciding whose task %s is: %w", d.TaskID, err)
		}
	}
	notes.say(ctx, buildingSandboxNote)
	// Acquired through the meter, which counts this sandbox against the
	// deployment's lifetime figures on the way past and hands back the
	// backend's own sandbox untouched (sandboxmeter.go). sandboxCounted
	// is the other half of that measurement -- how long this one lived --
	// and is called from the release below, which is the one path every
	// dispatch leaves by.
	//
	// A sandbox that never comes up is counted too, before this returns:
	// the run fails either way, and a deployment whose sandboxes fail to
	// start has spent real time and real machine on them.
	meter := MeterFor(deps.Store)
	sandbox, sandboxCounted, err := meter.Acquire(ctx, deps.Sandboxes, model.SandboxKindAgent, sandboxName, shape)
	if err != nil {
		return fmt.Errorf("orchestrator: acquiring a sandbox for run %s: %w", d.RunID, err)
	}

	// stopTokenRefresh ends the refresher started once this sandbox's
	// token is minted below; the release stops it before revoking, so a
	// refresh can never race the revoke that is meant to be the token's end.
	stopTokenRefresh := func() {}

	// The sandbox is released once this dispatch is done with it, whether
	// or not the run succeeded -- a failed or cancelled run is exactly the
	// kind that should leave nothing behind. Deferred rather than run at
	// the end of the happy path: every early return below is a run that
	// has already got a sandbox, and leaking one would hold real resources
	// (a granule VM, its containers) for the life of the process.
	//
	// A release failure is joined onto whatever else went wrong rather
	// than replacing it, and never skips the rest of this function: what a
	// successful run's result implies for GitHub does not depend on
	// whether cleaning up after it also succeeded. That is also why this
	// needs the named return -- an error produced in a deferred call has
	// nowhere else to go.
	//
	// The release runs on a context detached from ctx's cancellation
	// (though not its values): ctx is cancelled when the daemon is
	// shutting down or the task was closed mid-run, which are exactly the
	// cases where a VM would otherwise be left running with nothing left
	// to release it.
	//
	// Detached, but not unbounded. Release execs `granule vm delete`
	// against a host that may be wedged, and a context with no deadline
	// would let that hang hold this goroutine -- and this run's row, which
	// only gets finished below it -- open forever, which is the failure
	// the detachment is supposed to prevent rather than cause. A VM that
	// outlives the timeout is ReapOrphans' problem at the next startup,
	// which is what that pass is for.
	defer func() {
		stopTokenRefresh()
		if detached {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
		defer cancel()
		if rerr := sandbox.Release(releaseCtx); rerr != nil {
			err = errors.Join(err, fmt.Errorf("orchestrator: releasing run %s's sandbox: %w", d.RunID, rerr))
		}
		// Counted as finished whether or not the release itself worked:
		// the sandbox's life as far as this deployment is concerned ends
		// here either way, and a backend that failed to tear one down has
		// left something behind for ReapOrphans rather than kept a
		// sandbox this run was using.
		sandboxCounted(releaseCtx)
		// The token identified this sandbox, and the sandbox is gone, so
		// the entry is too -- see SandboxTokenStore.Revoke on why the file
		// is worth pruning now that it grows by one per run rather than
		// holding one entry per slot forever. Logged rather than joined:
		// a leftover entry authorizes nothing on its own (Store.GitScope
		// resolves through the live run, and this one has none), so it is
		// not worth turning a run's own outcome into an error over.
		if deps.RevokeSandboxToken != nil {
			if rerr := deps.RevokeSandboxToken(sandboxName); rerr != nil {
				log.Printf("orchestrator: revoking run %s's sandbox token: %v", d.RunID, rerr)
			}
		}
	}()

	// Mint this sandbox's own proxy token and point its git at the proxy,
	// before the checkout RunDispatch performs needs either. Both used to
	// happen once per slot in cmd/grain's own runDaemon, at startup,
	// which is where they belonged while a slot's sandbox was long-lived
	// and shared across every run that landed on it. Neither is a
	// deployment-wide setup step any more: there is nothing to configure
	// until a run has a sandbox, and what is configured is destroyed with
	// it.
	if deps.MintSandboxGrant != nil || deps.MintSandboxToken != nil {
		notes.say(ctx, sandboxCredentialsNote)
		var token string
		var err error
		if deps.MintSandboxGrant != nil {
			token, err = deps.MintSandboxGrant(sandboxName, d.TaskID, d.Attempt)
		} else {
			token, err = deps.MintSandboxToken(sandboxName)
		}
		if err != nil {
			return fmt.Errorf("orchestrator: minting run %s's sandbox token: %w", d.RunID, err)
		}
		stopTokenRefresh = keepSandboxTokenFresh(ctx, deps.RefreshSandboxToken, sandboxName, deps.SandboxTokenRefreshInterval)
		gitIdentity := runGitIdentity(ctx, deps.Store, deps.CredentialGitIdentity, task)
		if err := sandbox.ConfigureGitCredentials(ctx,
			deps.Config.GitRemoteBase+"/placeholder/placeholder.git", token, gitIdentity); err != nil {
			return fmt.Errorf("orchestrator: configuring git credentials for run %s: %w", d.RunID, err)
		}
	}

	// And point its package managers at this deployment's own cache, if
	// it is running one and this deployment has it switched on. Beside
	// the git credentials because it is the same kind of thing -- a
	// sandbox-wide fact written once, before the run's first command --
	// and unlike them it never fails a dispatch: a sandbox that could not
	// be pointed at the cache downloads from the internet, as every
	// sandbox did before the cache existed (applyPackageCache).
	applyPackageCache(ctx, deps.Config, sandbox, d.TaskID)

	tools, err := sandbox.Tools(ctx)
	if err != nil {
		return err
	}

	// Config.GrantTools' own doc comment explains why this is gated on
	// Interactive and keyed by raw Grants rather than a resolved
	// GrantResolution: every capability it is wired for today is a GRANT
	// capability (model.ProvisionGrant) whose Resolve always honours, so
	// there is no "granted but refused" case to miss by not waiting for
	// prepareCapabilities below.
	if deps.Config.GrantTools != nil && task.Interactive {
		for _, g := range task.Grants {
			if build, ok := deps.Config.GrantTools[g.Capability]; ok {
				tools = append(tools, build(deps.Store, task.ID)...)
			}
		}
	}

	// sandboxRoot and granuleVM are the two ways a Sandbox can describe
	// itself to a Framework with no in-process route to it (agent/claude,
	// via agent.RunConfig.SandboxRoot/.GranuleVM -- see that struct's own
	// doc comment): a local directory for a rootedSandbox (HostSandboxes'
	// own), or a named VM for a vmNamedSandbox (GranuleSandboxes' own).
	// Computed unconditionally, not just when the task has capabilities to
	// place, since Root()/VMName() are always available where they exist
	// at all and cost nothing to read -- a claude-selected run needs one
	// of them regardless of whether this task granted anything.
	//
	// Whether a missing sandboxRoot actually matters is decided later, by
	// applyPlacements: not every grant materializes a SideSandbox
	// placement (self-debug and self-repair materialize none at all --
	// they only add tools), so erroring here on len(task.Grants) > 0
	// alone used to fail every task holding only those, dispatched onto
	// a non-rooted sandbox (bwsalmon/agents#643), whether or not it had
	// anything to place.
	//
	// placer is the third way, and the one a granule VM answers with for
	// the grants that DO place something: a route into a sandbox with no
	// local directory, which applyPlacements prefers over sandboxRoot
	// whenever a sandbox offers one. Without it a deployment running
	// -granule-sandboxes could never receive a gcp-key/gemini-key/
	// github-sandbox placement at all -- every such task failed during
	// preparation instead.
	var sandboxRoot, granuleVM, container string
	var placer SandboxPlacer
	if rooted, ok := sandbox.(rootedSandbox); ok {
		sandboxRoot, err = rooted.Root()
		if err != nil {
			return err
		}
	}
	if named, ok := sandbox.(vmNamedSandbox); ok {
		granuleVM = named.VMName()
	}
	if named, ok := sandbox.(containerNamedSandbox); ok {
		container = named.ContainerName()
	}
	var sandboxPod agent.KubePod
	if named, ok := sandbox.(podNamedSandbox); ok {
		sandboxPod = named.KubePod()
	}
	if placing, ok := sandbox.(SandboxPlacer); ok {
		placer = placing
	}
	var sandboxEnv, sandboxHide []string
	var sandboxEnvFile string
	if hiding, ok := sandbox.(hidingSandbox); ok {
		sandboxHide = hiding.HiddenPaths()
	}
	if withEnv, ok := sandbox.(envSandbox); ok {
		sandboxEnv = withEnv.SandboxEnv()
		if sandboxEnvFile, err = withEnv.SandboxEnvFile(); err != nil {
			return err
		}
	}

	// From here this run can be asked to throw its sandbox away and start
	// again in a fresh one (Config.SandboxRecreations, and pkg/mcp's
	// recreate_sandbox behind it) -- an agent that has wedged its own
	// sandbox, or landed on a guest that stopped answering, otherwise has
	// no move left but to spend the rest of its turns failing.
	//
	// Registered after everything a rebuild has to put back is in hand
	// (the sandbox itself, its tools, and the two routes a placement can
	// take into it) and deferred *after* the release above, so that
	// deferred calls run in the order this needs: unregistering first --
	// which waits out a rebuild already in flight -- and only then
	// destroying the VM that rebuild was building.
	stopRecreating := deps.Config.SandboxRecreations.register(d.TaskID, &sandboxRecreation{
		store:              deps.Store,
		cfg:                deps.Config,
		task:               *task,
		sandbox:            sandbox,
		tools:              tools,
		sandboxRoot:        sandboxRoot,
		placer:             placer,
		mint:               deps.MintSandboxToken,
		credentialIdentity: deps.CredentialGitIdentity,
	})
	defer stopRecreating()

	// Built here, after the sandbox and before RunDispatch takes over
	// finishing this run, so a task naming a framework whose credential
	// is not configured yet ends as a setup-failed run saying exactly
	// that (the guard above) rather than as a failed agent run.
	// Both halves of the choice are empty for a task that made none of
	// its own; Deps.Framework is what turns that into the deployment's
	// default framework and its rotation. A task that pinned a model
	// (task.AgentBackendID) is dispatched onto that one even if it has
	// been taken out of the rotation -- see AgentChoice.
	var framework agent.Framework
	if preBuilt.built {
		framework, err = preBuilt.framework, preBuilt.err
	} else {
		framework, err = deps.Framework(ctx, deps.dispatchChoiceFor(ctx, *task))
		leasedFramework = framework
	}
	if err != nil {
		return fmt.Errorf("orchestrator: building the agent framework for run %s: %w", d.RunID, err)
	}
	if placer == nil && framework != nil {
		if hp, ok := framework.(interface{ Host() agent.Host }); ok {
			if p, ok := hp.Host().(SandboxPlacer); ok {
				placer = p
			}
		}
	}

	// How this run is driven: unattended, or as a terminal somebody sits
	// in front of for as long as they want it (consoleDispatch, and the
	// console capability behind it). Built here for the same reason the
	// framework above is -- a task asking for a console this deployment
	// cannot serve ends as a setup-failed run saying exactly that, rather
	// than as an agent run nobody was watching.
	cfg := deps.Config
	cfg.PreparedCapabilities = prepared
	cfg.sandboxPod = sandboxPod
	if reporter, ok := sandbox.(SandboxOOMReporter); ok {
		cfg.sandboxOOM = reporter
	}
	if shaper, ok := deps.Sandboxes.(interface{ DefaultShape() Shape }); ok {
		cfg.sandboxDefaultMemoryMB = shaper.DefaultShape().MemoryMB
	}
	cfg.sandboxEnv = sandboxEnv
	cfg.sandboxEnvFile = sandboxEnvFile
	cfg.sandboxHide = sandboxHide
	framework, cfg, err = consoleDispatch(framework, *task, d.RunID, cfg)
	if err != nil {
		return err
	}
	// And the other thing a person on the other end changes: how long the
	// run may last. A task filed as a live chat spends most of its time
	// waiting for somebody to type, which is not work and must not come
	// out of a budget for working (interactiveDispatch).
	cfg = interactiveDispatch(*task, cfg)
	// And a durable task has no clock at all (durableDispatch); its pass
	// is renewed to the longest lease below.
	cfg = durableDispatch(*task, cfg)
	if cfg.durable {
		runID := d.RunID
		cfg.renewLeases = func(ctx context.Context, until time.Time) error {
			return deps.capabilityPasses().Extend(ctx, runID, until)
		}
	}
	// A pass admission minted was minted for the default budget; a run
	// whose budget that just extended has it renewed to the new
	// deadline before anything relies on it (§4.3.5).
	if err := deps.capabilityPasses().Extend(ctx, d.RunID, capabilityDeadline(now, cfg.maxRunRuntime())); err != nil {
		log.Printf("orchestrator: run %s: renewing its capability grants for its extended budget: %v", d.RunID, err)
	}
	// The pass admission minted, carried into the sandbox before anything
	// runs in it (docs/capabilities-spec.md §5): ports, placements, then
	// setup commands. A failure aborts the attempt here, as a setup
	// failure -- the guard above finishes the row, the deferred release
	// tears the sandbox down, and the dispatch goroutine releases the
	// pass -- with the failing grant reported on the task (§5.5.4). The
	// forwards close when this run ends, before the pass is released.
	if pass, deadline := deps.capabilityPasses().held(d.RunID); pass != nil {
		notes.say(ctx, capabilityGrantsNote)
		mat, err := materializePass(ctx, cfg, sandbox, pass, deadline, deps.capabilityService)
		if err == nil {
			// Then every grant's mcp_endpoint proxied, handshaken and
			// named, and its prompt_section rendered (§5.2, §5.4), each
			// narrating the calls made through it onto the task's row.
			if deps.Store != nil {
				mat.narrator = mcp.NewCapabilityNarrator(taskActivityReporter{store: deps.Store, taskID: d.TaskID, now: cfg.now})
			}
			if err = mat.wireEndpoints(ctx, deps.capabilityDataPlane); err != nil {
				mat.Close()
			}
		}
		if err != nil {
			reportCapabilitySetupFailure(ctx, deps.Store, d.TaskID, err, cfg.now())
			return fmt.Errorf("orchestrator: materializing run %s's capability grants: %w", d.RunID, err)
		}
		defer mat.Close()
		cfg.capabilityMCPServers = mat.servers
		cfg.capabilityPromptSections = mat.promptSections
		deps.Config.SandboxRecreations.setCapabilityPass(d.TaskID, mat)
	}

	// From here on RunDispatch owns finishing this run, on every path it
	// can take -- so the setup guard above must not also finish it.
	ranAgent = true
	preparedHandedOff = true
	// A pull request still open from this task's branch is this run's to
	// continue (ReattachPullRequest): linked before the agent starts, so
	// a task reopened while one was open does not end up with a second.
	// Best-effort -- the finish path finds an open one again anyway --
	// and not asked on a first attempt, which has no branch yet.
	obs, _ := deps.Store.GetObservation(ctx, task.ID)
	var preRunBranchSHA string
	if deps.Client != nil && task.Target != nil && (d.Attempt > 1 || obs.RepairInFlight() || hasFixesLink(*task)) {
		if head, err := deps.Client.GetBranchHead(task.Target.Owner, task.Target.Name, task.BranchName()); err == nil && head != nil {
			preRunBranchSHA = head.SHA
		}
	}
	if deps.Client != nil && d.Attempt > 1 {
		if _, err := ReattachPullRequest(ctx, deps.Store, deps.Client, *task, false, now); err != nil {
			log.Printf("orchestrator: %v", err)
		}
	}
	result, runErr := RunDispatch(ctx, deps.Store, framework, cfg, *task, d, tools, sandboxRoot, granuleVM, placer, now, container)
	if errors.Is(runErr, agent.ErrDetached) {
		detached = true
		return runErr
	}

	return afterAgentRun(ctx, deps, *task, d.RunID, result, runErr, preRunBranchSHA, preRunBranchSHA, now)
}

// afterAgentRun is what follows a run whose row RunDispatch has finished:
// the salvage of a branch a failed run pushed, or ProcessResult for one
// that came back -- and noteFinishFailure for either, if that is what
// failed. It is runOne's tail, shared with ResumeAdoptedRun so that a run
// another daemon started reaches its pull request the same way.
//
// dispatchBranchSHA is preRunBranchSHA when it was read as the run was
// dispatched, and "" when it was not: an adopted run's head is read when
// it is adopted, after the run may already have pushed, so it cannot
// prove that a nothing_needed run pushed nothing (processResultFrom).
func afterAgentRun(ctx context.Context, deps Deps, task model.Task, runID string,
	result *agent.Result, runErr error, preRunBranchSHA, dispatchBranchSHA string, now time.Time) error {
	// Whatever the run attached by path is held only for its own relay,
	// which is below or never: gone either way once this returns -- but
	// for a run the daemon stopped, which the next daemon resumes.
	defer func() {
		if errors.Is(runErr, errDaemonStopped) {
			return
		}
		if err := deps.Store.ClearStagedAttachments(context.WithoutCancel(ctx), runID); err != nil {
			log.Printf("orchestrator: run %s: %v", runID, err)
		}
	}()
	if runErr != nil {
		// A run stopped because the daemon itself is shutting down is not
		// finished work: RunDispatch has already recorded it as interrupted
		// without a strike and queued the task to resume its conversation on
		// the next daemon. Attempting to salvage a half-pushed branch here
		// would either fail against the cancelled daemon ctx (and have
		// noteFinishFailure overwrite "interrupted" with "finish-failed" and
		// charge a strike) or prematurely complete a task that was still
		// mid-turn.
		if errors.Is(runErr, errDaemonStopped) {
			return runErr
		}
		// A failed run is not necessarily an empty one. The framework
		// hands back what the agent managed to do before it broke (see
		// agent.Framework), and the one part of that which outlives the
		// run is a branch it already pushed -- the sandbox above is about
		// to be released, but GitHub still has the commits. Skipping
		// ProcessResult here left such a branch permanently stranded:
		// pushed, with no pull request, no closing comment and no second
		// run able to find it, until a daemon restart happened to run
		// RecoverOrphanedRuns over a row this one had already finished
		// and so was no longer live. An agent that commits, pushes and
		// then runs out of turns did the work; only the ending failed.
		//
		// Only the pushed-branch half, not all of ProcessResult: a
		// question or a proposed task from a run that then failed is a
		// half-formed intention, and the run's outcome stays "failed"
		// with the framework's own diagnosis rather than being overwritten
		// with no_action.
		var salvageErr error
		if result != nil {
			// A retry or merge-queue repair starts with the previous
			// attempt's branch already on the remote. Salvage only when
			// this run actually advanced the remote branch; otherwise an
			// untouched pre-existing branch would mark a failed or
			// usage-limit-paused repair as completed.
			salvage := true
			if preRunBranchSHA != "" && deps.Client != nil && task.Target != nil {
				head, err := deps.Client.GetBranchHead(task.Target.Owner, task.Target.Name, task.BranchName())
				salvage = err == nil && head != nil && head.SHA != preRunBranchSHA
			} else if obs, err := deps.Store.GetObservation(ctx, task.ID); err == nil && obs.RepairInFlight() {
				salvage = false
			}
			// An empty branch is excepted: salvagePushedBranch has
			// already explained it on the task and parked it
			// (noteEmptyBranch), and this run's row keeps runErr's own
			// diagnosis, which is the more informative of the two -- a
			// run that broke before it had committed anything failed
			// first and pushed nothing second.
			if salvage {
				if _, err := salvagePushedBranch(ctx, deps.Store, deps.Client, task, now); err != nil {
					if _, empty := emptyBranch(err); !empty {
						salvageErr = err
					}
				}
			}
		}
		if salvageErr != nil {
			// Joined, so the overwrite noteFinishFailure is about to make
			// carries the framework's own diagnosis too: a run that ran
			// out of turns *and* could not have its branch salvaged has
			// two things wrong with it, and the row has one detail.
			return noteFinishFailure(ctx, deps.Store, runID, errors.Join(runErr, salvageErr))
		}
		return runErr
	}
	return noteFinishFailure(ctx, deps.Store, runID,
		processResultFrom(ctx, deps.Store, deps.GitReach, deps.Client, deps.AppBotIdentity, task, result, runID, dispatchBranchSHA, now))
}

// noteFinishFailure records, on the run's own row, that turning a
// finished agent run into the effects it implies is what failed -- then
// hands err back unchanged for its caller to return.
//
// It closes the one hole in reconcileDispatch's claim that "the run's own
// outcome and diagnosis are already in the store". They are for
// everything RunDispatch owns: it finishes the row on every path it can
// take. They are not for anything after it. ProcessResult runs once
// FinishRun has already written this run's outcome, and every way it can
// fail is a GitHub call -- reading whether the branch is there
// (branchExistsSettled), finding or opening its pull request
// (EnsurePullRequest) -- so an error here means the agent's work is on
// the remote and nothing points at it. The task is left un-completed and
// dispatch retries it, which is the right thing to do about a transient
// GitHub failure and the wrong thing to do about a persistent one; either
// way the row still says whatever outcomeOf guessed, the error reaches
// only the daemon's journal, and a human reading the task sees an attempt
// whose detail describes a run that went fine, repeated, with no reason
// given anywhere for why it is being repeated. That is precisely the
// question `grain get` exists to answer.
//
// "finish-failed" rather than a correction of the outcome already there:
// the run itself is not what failed, the same distinction runOne's own
// setup guard draws with "setup-failed" at the other end. The vocabulary
// is open (metrics.Runs.Outcomes' own doc comment), and task_streak
// counts it the way it counts any other non-"succeeded" ending, so the
// retry this failure earns still backs off rather than spinning.
//
// The salvage half of a run that had already failed on its own goes
// through here too, with both errors joined, so overwriting the row does
// not throw the framework's own diagnosis away. A salvage that worked
// never reaches this at all, which is what leaves a run that ran out of
// turns after pushing reading "failed" with the reason it failed --
// salvaging its branch was never a claim that the run went well.
//
// A failure to record the failure is joined on rather than replacing it:
// the original error is the one worth reporting, and a store that cannot
// take this write is its own separate problem.
func noteFinishFailure(ctx context.Context, store *model.Store, runID string, err error) error {
	if err == nil {
		return nil
	}
	// Detached from ctx's own cancellation, and bounded, for the same
	// reason the sandbox release above is: a daemon shutting down or a
	// task closed mid-run is exactly when this row would otherwise keep
	// an outcome that does not explain itself.
	noteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancel()
	if serr := store.SetRunOutcome(noteCtx, runID, "finish-failed",
		"this run's result could not be turned into a pull request or a comment: "+
			err.Error()); serr != nil {
		return errors.Join(err, fmt.Errorf("orchestrator: recording run %s's finishing failure: %w", runID, serr))
	}
	return err
}

// setupFailedEnding is the outcome and detail runOne's setup guard
// finishes a run with: "setup-failed", which counts against the task,
// unless what failed was the cluster having no room for the run's
// sandbox pod (ErrClusterFull). That one is model.CapacityOutcome: the
// deployment's weather, not the task's fault, so it neither adds to the
// failure streak nor to the consecutive failed attempts that retire a
// task -- but it does count for retry backoff, which is what spaces the
// requeued attempts out while the cluster stays full. A pod no node
// could ever fit is not ErrClusterFull (admitPod, unschedulableOutcome)
// and stays a setup failure, so it surfaces rather than looping.
func setupFailedEnding(err error) (outcome, detail string) {
	if errors.Is(err, ErrClusterFull) {
		return model.CapacityOutcome, "this run's sandbox could not be scheduled: " + err.Error() +
			"; the task is not counted as failed and will be retried once the cluster has room"
	}
	return "setup-failed", "this run's sandbox could not be prepared: " + err.Error()
}
