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
	"sort"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// DisableBranchExistsSleep makes branchExistsSettled's own backoff
// (branchExistsRetries/branchExistsBackoff) stop actually sleeping, and
// returns the function that puts it back.
//
// sync_internal_test.go's withNoSleep already does exactly this, but it
// lives in this package and so can only reach the tests written here.
// Most of this package's tests are in orchestrator_test, and a dispatch
// driven from there runs the real thing: ProcessResult -> salvagePushedBranch
// -> branchExistsSettled, which re-reads a negative branchExists answer
// branchExistsRetries times with branchExistsBackoff between attempts.
// Against a simulated GitHub that answers instantly and honestly, every
// one of those waits is dead time -- seconds per dispatch, paid by every
// external test that finishes a run, with nothing about the retry
// behaviour being asserted (the internal test above owns that).
//
// This exists so TestMain can spend that time nowhere. Any test that does
// want the real backoff can restore it with the returned function.
func DisableBranchExistsSleep() func() {
	prev := branchExistsSleep
	branchExistsSleep = func(time.Duration) {}
	return func() { branchExistsSleep = prev }
}

// SalvagePushedBranch is salvagePushedBranch exported for testing.
func SalvagePushedBranch(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, now time.Time) (bool, error) {
	return salvagePushedBranch(ctx, store, client, task, now)
}

// RepairDeadline is defaultRepairDeadline, for the merge-queue tests in
// orchestrator_test.
//
// The other two merge-queue clocks have setters (SetCheckStallDeadline,
// SetCheckRegistrationWindow) because their sightings live in memory and
// a test has no other way to reach them. This one is measured off the
// task's own Observation.MergeQueueRepairAt, so a test drives it by
// moving the clock it hands SyncPullRequests -- and only needs to know
// how far to move it.
const RepairDeadline = defaultRepairDeadline

// ShapeOrDefaultForTest is Shape.orDefault, for the network-access tests
// in orchestrator_test: how a run's own request and the deployment-wide
// default combine is worth asserting per dimension, and every other way
// to reach it goes through a whole Acquire.
func ShapeOrDefaultForTest(asked, deployment Shape) Shape { return asked.orDefault(deployment) }

// GrainCIVerdictFor is grainCIVerdictOf for the grain CI tests in
// orchestrator_test: what the merge queue reads about a task's branch off
// its own record, including whether grain owes this commit another run
// (forge.GrainCI.Replacement). The gate here is the task's own grant,
// which is what every one of those tests sets up.
func GrainCIVerdictFor(task model.Task, obs *model.Observation, headSHA string) forge.GrainCI {
	return grainCIVerdictOf(task, obs, headSHA, false)
}

// The merge lane's liveness classification (liveness.go), for the
// driver-fires tests in orchestrator_test. Those have to be written there
// -- proving a driver fires means driving a whole cycle against a
// simulated GitHub, which is what that package's helpers are for -- and
// the classification they are written against is this package's.
//
// Three accessors rather than the table itself: what a test outside here
// needs is the list of states it has to cover, what each one claims about
// itself, and the runtime answer. Handing out the table would let a test
// assert against fields nothing outside this package reads.

// MergeLaneDrivenStates is every merge-queue state the classification
// calls driven -- the states a test in orchestrator_test has to show a
// driver firing for.
func MergeLaneDrivenStates() []string {
	var states []string
	for state, liveness := range mergeLane.states {
		if liveness.class == livenessDriven {
			states = append(states, state)
		}
	}
	sort.Strings(states)
	return states
}

// MergeLaneClaimOf is what the classification says about one state: its
// class, the component that leaves it, the control that restarts the cycle
// where a person owns it, and how long it may plausibly stand.
func MergeLaneClaimOf(state string) (class, driver, control string, patience time.Duration) {
	liveness, ok := mergeLane.of(state)
	if !ok {
		return "", "", "", 0
	}
	if liveness.patience != nil {
		patience = liveness.patience()
	}
	return string(liveness.class), liveness.driver, liveness.control, patience
}

// MergeLaneOverdue is the runtime half: whether an entry that has been in
// state since `since` has outlived that state's patience.
func MergeLaneOverdue(state string, since *time.Time, now time.Time) (time.Duration, bool) {
	return mergeLane.overdue(state, since, now)
}

// The task lane's own two accessors (liveness.go), for the same tests and
// for the same reason. A task's states are model.State's, so a test
// outside here names those constants itself and only needs to ask this
// package what the classification claims about them.

// TaskLaneDrivenStates is every task state the classification calls
// driven -- the states a test in orchestrator_test has to show a driver
// firing for.
func TaskLaneDrivenStates() []string {
	var states []string
	for state, liveness := range taskLane.states {
		if liveness.class == livenessDriven {
			states = append(states, state)
		}
	}
	sort.Strings(states)
	return states
}

// TaskLaneClaimOf is what the classification says about one task state:
// its class, the component that leaves it, the control that restarts the
// cycle where a person owns it, and how long it may plausibly stand.
func TaskLaneClaimOf(state string) (class, driver, control string, patience time.Duration) {
	liveness, ok := taskLane.of(state)
	if !ok {
		return "", "", "", 0
	}
	if liveness.patience != nil {
		patience = liveness.patience()
	}
	return string(liveness.class), liveness.driver, liveness.control, patience
}

// MergeStateFailed is the terminal state's stored value, so that a test in
// orchestrator_test asserting the awaiting-a-person clause names the same
// string the daemon writes.
const MergeStateFailed = mergeStateFailed

// CIMaximumAttempts is how many runs grain will start for one commit
// before a verdict it would otherwise retry becomes a state a person
// owns, for the same tests. Exported rather than restated there so that
// changing the cap changes what they assert.
const CIMaximumAttempts = ciMaximumAttempts

// BaseCIMaximumAttempts is the same bound for one commit of a base
// branch, whose verdicts about nothing get maxInfraRetries re-runs
// (docs/design/release-from-trunk.md, RFT-2).
const BaseCIMaximumAttempts = baseCIMaximumAttempts

// MaxInfraRetries is how many automatic re-runs an `infra` verdict on a
// trunk release gate gets.
const MaxInfraRetries = maxInfraRetries

// How a firing of a workflow-review schedule gives up: how many times
// grain asks CI for its run, how long it waits between those attempts,
// and how long it waits for a run to end before it stops waiting and
// files the review anyway (scheduledworkflow.go). Exported for the same
// reason as the cap above -- the tests assert grain's own policy rather
// than a number restated beside it.
const (
	ScheduledWorkflowStartAttempts = scheduledWorkflowStartAttempts
	ScheduledWorkflowStartDelay    = scheduledWorkflowStartDelay
	ScheduledWorkflowDeadline      = scheduledWorkflowDeadline
)

// SetHealthClockForTest replaces the clock GranuleSandboxes.Health
// measures its per-sandbox breaker against (health.go), so a test in
// orchestrator_test can move a cooldown on without sitting out fifteen
// real seconds of it.
//
// Safe to call between sweeps and not during one: Health's own
// goroutines read this while they run, and collectSandboxHealth joins
// them all before it returns.
func (k *GranuleSandboxes) SetHealthClockForTest(now func() time.Time) { k.clock = now }

// The health breaker's own intervals, for those tests: what a sweep owes
// a guest that has been failing depends on both of them, and a test
// naming its own numbers instead would keep passing after the policy
// changed underneath it.
const (
	HealthBreakerCooldown     = healthBreakerCooldown
	HealthBreakerFullInterval = healthBreakerFullInterval
)

// EnsureGuestDockerNetworkForTest runs ensureGuestDockerNetwork for a named VM,
// for testing guest docker network repair in orchestrator_test.
func (k *GranuleSandboxes) EnsureGuestDockerNetworkForTest(ctx context.Context, name string) {
	k.ensureGuestDockerNetwork(ctx, name, k.execRunner(name))
}

// EnsureGuestDockerNetworkWithRunnerForTest runs ensureGuestDockerNetwork with a custom runner.
func (k *GranuleSandboxes) EnsureGuestDockerNetworkWithRunnerForTest(ctx context.Context, name string, runner sandboxRunner) {
	k.ensureGuestDockerNetwork(ctx, name, runner)
}

// StubHostFirewall replaces the host nftables isolation every granule
// NetworkNone sandbox now has to get (enforceGranuleHostIsolation) with
// one that succeeds without touching anything, and returns the function
// that puts the real one back.
//
// The tests in orchestrator_test drive Acquire through a fake granule and
// a fake docker, on a host whose firewall they have no business editing;
// without this every NetworkNone acquire among them would be refused for
// want of nft, which is exactly what production must do and exactly what
// those tests are not about. TestMain installs it; a real-VM test that
// does want the host's own rules restores them with the returned function.
func StubHostFirewall() func() {
	prevNFT, prevNets, prevPorts := hostNFT, hostGuestNetworks, hostGuestPorts
	hostNFT = func(ctx context.Context, stdin string, args ...string) ([]byte, error) { return nil, nil }
	hostGuestNetworks = func(ctx context.Context, container string) ([]guestNetwork, error) {
		return parseGuestNetworks("172.18.0.2|16|172.18.0.1|||\n")
	}
	hostGuestPorts = func(ctx context.Context, container string) ([]string, error) {
		return []string{"veth0000000"}, nil
	}
	return func() { hostNFT, hostGuestNetworks, hostGuestPorts = prevNFT, prevNets, prevPorts }
}

// UseRealHostFirewall undoes StubHostFirewall for one test, and returns
// the function that puts back whatever was there before it.
func UseRealHostFirewall() func() {
	prevNFT, prevNets, prevPorts := hostNFT, hostGuestNetworks, hostGuestPorts
	hostNFT, hostGuestNetworks, hostGuestPorts = runNFT, inspectGuestNetworks, inspectGuestPorts
	return func() { hostNFT, hostGuestNetworks, hostGuestPorts = prevNFT, prevNets, prevPorts }
}

// GuestDockerNetworkRepairScript is guestDockerNetworkRepair itself, and
// GuestDockerConfigPath the one absolute path it edits, for the repair
// test in orchestrator_test.
//
// That test used to run a hand-copied paraphrase of the script instead,
// which meant it asserted that a copy of the logic did what the copy
// said. The copy had dropped the docker0 branch entirely, spelled the
// conf path as $CONF, dropped every sudo -n, and ended in an
// "echo REPAIRED=$repaired" that production does not have -- so a
// divergence in the real script's subnet arithmetic, or in which of the
// two families it swaps to, could not have failed it. It now drives this
// text, with the conf path rewritten to a temporary file and sudo, ip,
// systemctl and sed stood in for on PATH.
//
// The path is exported beside the script because substituting it is the
// test's safety boundary, not a convenience: the stub it installs for
// sudo runs its arguments directly, so an occurrence the test failed to
// rewrite would have the real script sed the developer's own
// /etc/docker/daemon.json and delete their own docker0. The test asserts
// it left none behind.
const (
	GuestDockerNetworkRepairScript = guestDockerNetworkRepair
	GuestDockerConfigPath          = pkgcache.DockerConfigPath
)

// ReconcilePathTriggers is the "path-triggers" reconciler, for tests.
func ReconcilePathTriggers(ctx context.Context, deps Deps, now time.Time) error {
	return reconcilePathTriggers(ctx, deps, now)
}

// JudgePullRequestTriggers is what the merge queue does with a head the
// pull-request triggers have not judged yet (pathTriggersCurrentFor).
func JudgePullRequestTriggers(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, headSHA string, now time.Time) (bool, error) {
	return pathTriggersCurrentFor(ctx, store, client, task, ref, headSHA, now)
}

// CheckTriggerApprovers is the declaration sync's approver check (grain/task-1714).
var CheckTriggerApprovers = checkTriggerApprovers

// NarrowedJobsPassed is narrowedJobsPassed, for mergerules_test.go.
var NarrowedJobsPassed = narrowedJobsPassed

// ProcessResultWithReach is ProcessResult as the reconcile loop calls
// it, with Deps.GitReach, so a test can see a proposal its team cannot
// reach parked as it is filed.
func ProcessResultWithReach(ctx context.Context, store *model.Store, reach *gitproxy.Reach,
	client github.Client, task model.Task, result *agent.Result, runID string, now time.Time) error {
	return processResult(ctx, store, reach, client, nil, task, result, runID, now)
}

// ProcessResultWithAppBot exercises processResult with an explicit GitHub
// App bot identity resolver.
func ProcessResultWithAppBot(ctx context.Context, store *model.Store, client github.Client,
	appBot AppBotIdentityFunc, task model.Task, result *agent.Result, runID string, now time.Time) error {
	return processResult(ctx, store, nil, client, appBot, task, result, runID, now)
}

// ProcessResultFrom is processResult for a run dispatched with its task
// branch already at preRunBranchSHA.
func ProcessResultFrom(ctx context.Context, store *model.Store, reach *gitproxy.Reach, client github.Client,
	task model.Task, result *agent.Result, runID, preRunBranchSHA string, now time.Time) error {
	return processResultFrom(ctx, store, reach, client, nil, task, result, runID, preRunBranchSHA, now)
}

// VerdictRunOf is verdictRunOf, for mergerules_test.go.
var VerdictRunOf = verdictRunOf

// ParkConsoleSession is parkConsoleSession, for consolesessions_test.go.
var ParkConsoleSession = parkConsoleSession

// ConsoleSessionResting is consoleSessionResting, for consolesessions_test.go.
const ConsoleSessionResting = consoleSessionResting

// CommitType is commitType, for the table of summary lines it reads.
var CommitType = commitType

// FilePorts is filePorts exported for testing: a close-out that runs it a
// second time for the same merge.
func FilePorts(ctx context.Context, store *model.Store, client github.Client, task model.Task,
	ref model.PullRequestRef, detail github.PullRequestDetail, now time.Time) error {
	return filePorts(ctx, store, client, nil, task, ref, detail, now)
}

// MaterializePass is materializePass exported for the real-sandbox suites
// in orchestrator_test, which drive a capability grant into a real VM or
// container the way a dispatch does: ports forwarded over relay, which a
// guest reaches at relayURL, then placements, then setup. The returned
// function closes the forwards, which is what the end of a run does.
func MaterializePass(ctx context.Context, relay *portforward.Relay, relayURL string, sandbox Sandbox, pass *protocol.Pass,
	deadline time.Time, service func(capabilityID string) (portforward.Service, error)) (func(), error) {
	m, err := materializePass(ctx, Config{CapabilityPorts: relay, CapabilityPortsURL: relayURL}, sandbox, pass, deadline, service)
	if err != nil {
		return nil, err
	}
	return m.Close, nil
}

// SetAutoUpgradeProbeTimeout shortens how long the daily automatic
// upgrade waits for Config.UpgradeUnchanged, and returns the function
// that puts it back. Not for a parallel test: the reconciler reads it.
func SetAutoUpgradeProbeTimeout(d time.Duration) func() {
	old := autoUpgradeProbeTimeout
	autoUpgradeProbeTimeout = d
	return func() { autoUpgradeProbeTimeout = old }
}

// SetGitHubCIBudgetLow makes the GitHub CI failure-tail reader see the
// hour's budget as low (or not), and returns the function that puts it
// back. Not for a parallel test.
func SetGitHubCIBudgetLow(low bool) func() {
	old := gitHubCIBudgetLow
	gitHubCIBudgetLow = func(time.Time) bool { return low }
	return func() { gitHubCIBudgetLow = old }
}

// RecordPolledChecks is recordPolledChecks, for a test of the poll path.
func RecordPolledChecks(ctx context.Context, store *model.Store, repo model.RepoRef, ref, sha string,
	checks []github.CheckRun, now time.Time) {
	recordPolledChecks(ctx, store, repo, ref, sha, checks, now)
}

// GitHubCITailsPerHour is the hourly cap on failed-job log reads.
const GitHubCITailsPerHour = gitHubCITailsPerHour

// MergeQueueReviewPending reports whether the merge queue's review hold
// (reviewStateOf) refuses task now -- a takeover's review gate included.
func MergeQueueReviewPending(ctx context.Context, store *model.Store, task model.Task, now time.Time) (bool, error) {
	s, err := reviewStateOf(ctx, store, task, nil, now)
	return s.pending, err
}

// StartMergeRecheckForTest is startMergeRecheck, for the test that a head
// pushed after the queue cleared it is never re-checked or merged.
func StartMergeRecheckForTest(ctx context.Context, store *model.Store, client github.Client, runner ci.Runner,
	repo model.RepoRef, branch, taskID string, number int, clearedHead string, now time.Time) (bool, error) {
	return startMergeRecheck(ctx, Deps{Store: store, Client: client, CI: runner}, repo, branch,
		model.MergeBatchMember{TaskID: taskID, Number: number, HeadSHA: clearedHead}, now)
}

// ReconcileTaskCI is reconcileTaskCI, for a test that reconciles one task
// the "ci" reconciler's own walk over open pull requests would not reach.
func ReconcileTaskCI(ctx context.Context, deps Deps, task model.Task, now time.Time) error {
	return reconcileTaskCI(ctx, deps, task, now)
}
