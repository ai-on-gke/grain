package forge

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// WithGrainCI applies grain CI's verdict to a pull request's structural
// health. HealthOf removes GitHub's check verdict before calling this for a
// task that requires grain CI: enabling grain CI selects it as the CI
// provider rather than adding a second gate. GitHub still owns whether the
// pull request is open, merged, closed, conflicted, or not yet known to be
// mergeable.
//
// The whole of the policy is that the capability requires one successful
// full-suite verdict for the exact head commit. Selective runs are useful
// feedback, but are never copied into this gate.
// What follows is the questions that had to be answered before it could
// hold anything -- each of them one HealthFrom answers for GitHub with
// some care -- and what is answered here.
//
// # A repository that declares no workflows
//
// Merges, and nothing in this file has to know it is that repository.
// ci.PlanRun makes an empty plan a success on purpose, so such a task
// records "success" with a detail saying no workflow answered the push,
// and grainCIVerdictOf reads that as the required pass. Blocking on it would
// make adding `.grain/workflows` to one repository a change to every
// other one. A repository whose workflows cannot even be planned -- a
// file that does not parse -- cannot produce that pass and remains pending.
//
// A repository that declares workflows and matched *none* of them is not
// that repository, and does not merge on it. It produces the same empty
// run, but somebody has asked for CI there and this commit received none
// of it, so ci.PlanRun marks the plan unmatched and the engine concludes
// it errored -- which lands below, as "A run grain could not do", and
// holds the pull request pending rather than passing it. That is the
// distinction the two halves of this section turn on: no CI asked for,
// versus CI asked for and not delivered.
//
// # A run that has not finished
//
// Holds, as PrPending, which is the answer GitHub's own unfinished check
// gets and for the same reason: merging now lands a tree nothing has
// tested, and waiting for CI is the whole point of asking for it.
// It is bounded by the same clock, and that is what makes waiting safe: a
// pull request that reads PENDING for longer than
// defaultCheckStallDeadline is given up on and handed to a person
// (ChecksStalled, escalateStalledChecks). That is the answer to a CI
// service that stops answering mid-run -- a daemon restarted into
// -ci=false, an engine wedged, the capability taken off while a run was
// in flight. The queue moves on to the next task in the repository in
// every one of those cases, so none of them wedges a queue; one pull
// request waits for somebody, which is what the same failure of GitHub's
// CI already costs.
//
// # A run grain could not do -- no sandbox, no backend
//
// Holds, as PrPending, and deliberately not as PrFailing. A job that
// never got a machine (ci.ConclusionErrored) leaves the tree exactly as
// untested as a run that has not finished does, so merging on it would
// land code nothing has looked at -- but it is not a broken head either,
// and calling it one has a cost the pending answer does not: PrFailing
// files a fix task, and an agent dispatched to repair a pull request
// whose only problem is that this deployment could not build a VM will
// spend a run finding nothing and may well push a change to prove it.
// The tree is fine. The deployment is not.
//
// Pending is also what gets a person told, which is the outcome this
// wants: the same defaultCheckStallDeadline that bounds a wait on
// GitHub's checks bounds this one, and escalateStalledChecks then blocks
// the entry and says grain CI could not run and why
// (stalledChecksPhrase). A deployment-wide breakage -- the kontur
// sandbox names of grain/task-361 -- therefore reaches one human per
// pull request as a question about the deployment, rather than reaching
// each of them as a false accusation against their branch.
//
// Nothing re-runs an errored verdict on its own: reconcileTaskCI records
// one verdict per commit and the next push is what replaces it, so a
// deployment mended inside the stall window still leaves its pull
// requests waiting on a run nobody will start. That is the same wait a
// stalled GitHub check earns and it ends the same way, in somebody's
// hands; an automatic retry is a decision of its own, with a comment per
// attempt to keep quiet and a broken deployment to avoid spinning
// against, and it is not this function's.
//
// # A CI service that is down, or a daemon that has restarted
//
// Holds as pending. Absence is not a failure and never dispatches a repair,
// but it cannot stand in for the explicit successful full-suite run the
// capability requires. If the daemon forgets an in-flight run,
// collectCIRun forgets the dangling status and the agent must request the
// full suite again. The ordinary stall deadline bounds how long the merge
// queue waits before handing the infrastructure problem to a person.
//
// # PrUnknown, PrConflicted, PrMerged, PrClosed
//
// Left alone, all four. None is a statement about tests: a conflict has a
// repair to be sent for that has nothing to do with CI, PrUnknown is
// checks this deployment's credential could not read (and syncEntry acts
// on neither), and a pull request that is already merged or closed is
// past the point any verdict changes anything.
func WithGrainCI(health Health, grain GrainCI) Health {
	// A non-required task normally supplies the zero verdict. Preserve the
	// older helper contract for explicit verdict values as well: tests and
	// boundary adapters may compose a known run directly, while a stale
	// stored verdict on a task whose grant was removed is normalized to zero
	// by grainCIVerdictOf before it reaches here.
	if !grain.Required {
		if health != Clean || (!grain.Running && !grain.Errored && !grain.Failed && !grain.Passed) {
			return health
		}
	}
	// These are facts about the pull request rather than CI verdicts.
	if health == Conflicted || health == Unknown || health == Merged || health == Closed {
		return health
	}
	switch {
	case grain.Running, grain.Errored:
		return Pending
	case grain.Failed:
		return Failing
	case grain.Required && !grain.Passed:
		return Pending
	}
	return health
}

// FailingChecks returns the names of the completed runs in checks that
// did not pass, in the order GitHub reported them -- what decides
// PrFailing above, and what healthReason names in the fix task's own
// body so the agent repairing the PR is told which jobs to look at
// rather than being left to rediscover them.
func FailingChecks(checks []github.CheckRun) []string {
	var names []string
	for _, c := range checks {
		if c.Status != "completed" || c.Conclusion == nil {
			continue
		}
		switch *c.Conclusion {
		case "failure", "timed_out", "startup_failure":
			name := c.Name
			if name == "" {
				name = "(unnamed check)"
			}
			names = append(names, name)
		}
	}
	return names
}

// IsActionsBudgetFailure reports whether c is an external GitHub Actions check
// run that failed specifically due to account spending/budget limits
// (0 steps executed with a budget error message).
func IsActionsBudgetFailure(c github.CheckRun) bool {
	if c.Status != "completed" || c.Conclusion == nil {
		return false
	}
	if *c.Conclusion != "failure" && *c.Conclusion != "startup_failure" {
		return false
	}
	if c.StepsExecuted > 0 {
		return false
	}
	lower := strings.ToLower(c.FailureReason)
	return strings.Contains(lower, "actions budget") ||
		strings.Contains(lower, "spending limit") ||
		strings.Contains(lower, "budget is preventing further use")
}

// FilterActionsBudgetFailures returns a copy of checks with all external GitHub Actions
// budget exhaustion failures removed.
func FilterActionsBudgetFailures(checks []github.CheckRun) []github.CheckRun {
	var filtered []github.CheckRun
	for _, c := range checks {
		if IsActionsBudgetFailure(c) {
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered
}

// Sniffing an Actions budget block out of a check's text used to live
// here, and deliberately does not any more. It read the *name* of a check
// run for the words GitHub puts in a workflow run's error message, so it
// could not fire on the state it was written for -- and had it fired, a
// substring in a job's name would have silently taken a repository's
// whole CI gate away. Whether GitHub is a source of verdicts here is a
// declaration somebody makes about the deployment
// (model.RepoConfig.IgnoreGitHubCI), true or false at a glance and
// visible on the repository pane, rather than something guessed at from
// the text of an answer. docs/merge-queue-behind.md has the distinction.

// CancelledChecks returns the names of the completed runs in checks that
// finished without delivering a verdict -- the other half of what makes
// HealthFrom answer PrPending, and named separately from the runs below
// because the two are not the same thing to whoever is eventually told
// about them. A check that has not finished may still be about to; one
// of these has finished, saying nothing, and only a re-run will change
// that (stalledChecksPhrase, stalledChecksCause).
//
// Three shapes land here. "cancelled" is the common one: somebody
// pressed cancel, or a fail-fast matrix took a job's siblings down with
// it. "stale" is GitHub giving up on a check that stayed incomplete for
// fourteen days, which is the same news a fortnight later. And a run
// GitHub reports completed with no conclusion at all is a verdict the
// API never delivered; reading it as passing is a gate that fails open
// on a malformed answer, so it waits with the rest of them.
func CancelledChecks(checks []github.CheckRun) []string {
	var names []string
	for _, c := range checks {
		if c.Status != "completed" {
			continue
		}
		if c.Conclusion != nil && *c.Conclusion != "cancelled" && *c.Conclusion != "stale" {
			continue
		}
		name := c.Name
		if name == "" {
			name = "(unnamed check)"
		}
		names = append(names, name)
	}
	return names
}

// AwaitingApprovalChecks returns the names of the completed runs in
// checks GitHub concluded "action_required": an Actions workflow run
// held for a maintainer to approve it -- a first-time contributor's run,
// or a deployment environment with required reviewers. The third way
// HealthFrom answers PrPending. It is not a pass, because nothing has
// judged the tree (GitHub's own branch protection does not count it as
// one), and not a failure, because there is nothing for an agent to
// repair: only a person approving the run delivers a verdict. It is
// named apart from CancelledChecks because the person told about it has
// a different thing to do -- approve the run, not re-run it.
func AwaitingApprovalChecks(checks []github.CheckRun) []string {
	var names []string
	for _, c := range checks {
		if c.Status != "completed" || c.Conclusion == nil || *c.Conclusion != "action_required" {
			continue
		}
		name := c.Name
		if name == "" {
			name = "(unnamed check)"
		}
		names = append(names, name)
	}
	return names
}

// UnfinishedChecks returns the names of the runs in checks GitHub has not
// finished -- the other way HealthFrom answers PrPending, and what
// escalateStalledChecks names once they have gone on doing so for longer
// than the merge queue is willing to wait. A person asked to go and look
// at a stuck pull request starts from which job is stuck.
func UnfinishedChecks(checks []github.CheckRun) []string {
	var names []string
	for _, c := range checks {
		if c.Status == "completed" {
			continue
		}
		name := c.Name
		if name == "" {
			name = "(unnamed check)"
		}
		names = append(names, name)
	}
	return names
}

// defaultCheckRegistrationWindow is how long a pull request's head commit
// has to have had *no* checks reported against it before HealthFrom
// believes that means the repo has no CI, rather than that CI has not got
// round to registering yet.
//
// It exists because the two are the same API response. GitHub Actions
// creates a workflow run's check runs asynchronously, after it has
// processed the push, and finishWithPullRequest opens the pull request
// the moment the branch lands -- so a SyncPullRequests tick landing in
// that gap reads an empty list on a repo whose CI is about to start, and
// reading that as clean is how a merge beats the tests. A repo with no CI
// configured never fills the list in, and there is nothing in the Checks
// API that tells the two apart; only time does.
//
// GitHub's own mergeable_state is not the exception it looks like: it
// reads "clean" for an empty check list, the same answer it gives a
// genuinely green one, because it is computed from that same empty list.
// A live pull request on this repository was caught reading "clean" with
// zero check runs four seconds after a push, two seconds before its
// first check registered -- this gap, measured, in the field that was
// supposed to close it. See github.PullRequestDetail.MergeableState for
// the whole run. Nothing there removes this wait.
//
// Two minutes is chosen against what it costs on each side. On a repo
// that does have CI it costs nothing at all: the checks appear within
// seconds and the very next tick sees them. On a repo with genuinely no
// CI it is the whole cost -- every merge waits this out, once, per head
// commit -- so it wants to be small enough not to feel like a stall and
// large enough to cover GitHub being slow to dispatch a workflow, which
// is measured in seconds but not reliably in single digits.
const defaultCheckRegistrationWindow = 2 * time.Minute

// defaultCheckStallDeadline is the other end of the same wait: how long a
// pull request's checks may sit unfinished before the merge queue stops
// waiting on them, comments, and moves on (ChecksStalled,
// escalateStalledChecks).
//
// PrPending is acted on by neither arm of syncEntry's switch, which is
// right for CI that finishes and unbounded for CI that does not. A
// workflow waiting on an approval nobody gives, a self-hosted runner that
// never picks the job up, a third-party check whose provider posted
// "queued" and then went away: each leaves its task at the head of its
// queue forever, with nothing merged, nothing filed and nothing said to
// anyone -- and, since only the head is ever acted on (orchestrator's
// mergeWalk), everything queued behind it for the same branch waits too.
//
// It is the bound on a cancelled check too, and that is the whole of
// what reading one as PENDING costs (HealthFrom's "cancelled is
// neither"): a verdict that is never coming back is waited on for this
// long, once per head commit, and then a person is told. Every other
// wait here resolves by itself if CI is merely slow; that one only
// resolves if somebody re-runs the check, so this deadline is not a
// margin for it but the whole of its patience.
//
// Two hours is chosen against what each direction costs. Too long is the
// stall above, paid by every task behind it. Too short costs less than it
// looks: giving up is a comment and MergeQueueBlockedAt, which stops this
// task being anyone's queue head but leaves it merging the moment it does
// read clean (syncEntry's `blocked` arm), so a genuinely slow suite that
// finishes green still lands -- just out of queue order, and having said
// something alarming on the way. So this wants to sit past any honest CI
// run rather than exactly on the longest one, and two hours is well past
// the CI a deployment running an agent set on a poll interval measured in
// seconds is plausibly waiting on.
//
// It is a constant rather than a grain_config field on purpose. Every
// setting in that row is one an operator has a reason to reach for during
// ordinary use (which repos, how many at once, which model); this is one
// they would only ever reach for if their CI is slower than any CI here
// is expected to be, and adding it costs a schema field, a settings
// control, and a line in three docs. SetCheckStallDeadline is the escape
// hatch until a deployment actually needs the setting -- at which point
// this const becomes that field's default and nothing else here changes.
const defaultCheckStallDeadline = 2 * time.Hour

// sightings is the bookkeeping behind both clocks this file runs on a
// pull request's CI: the window an empty check list is held PENDING for
// (emptyChecks, defaultCheckRegistrationWindow) and the deadline an
// unfinished check is waited on before the queue gives up (pendingChecks,
// defaultCheckStallDeadline). Each remembers, per pull request, the head
// commit this process first saw the state it times and when it first saw
// it, alongside the duration itself, so a setter can change that without
// racing a cycle that is reading it.
//
// Deliberately in memory rather than in a task's Observation. Persisting
// either would mean a schema column, and neither fact is worth one:
// losing a sighting (a restart, a new leader) makes the next cycle start
// that clock afresh, which costs one more window of waiting and can never
// merge something early or give up on something sooner. Erring toward
// waiting is the whole point of both mechanisms.
//
// Both are keyed by pull request and hold the head sha alongside, so a
// push to a branch that already has a pull request open -- a fix task
// merging into it, a human pushing by hand, a redispatched task -- starts
// the clock again rather than inheriting the verdict of the commit before
// it. That push empties the check list, and sets CI running again, just
// as surely as the first one did.
type sightings struct {
	mu     sync.Mutex
	window time.Duration
	seen   map[string]sighting
}

type sighting struct {
	headSHA string
	first   time.Time
}

func newSightings(window time.Duration) *sightings {
	return &sightings{window: window, seen: map[string]sighting{}}
}

// emptyChecks times how long ref's current head commit has had *no*
// checks reported against it (EmptyChecksSettled).
var emptyChecks = newSightings(defaultCheckRegistrationWindow)

// pendingChecks times how long ref's current head commit has read
// PrPending without interruption (ChecksStalled). Interruption matters:
// the sighting is dropped the moment a cycle reads anything else, so a
// check re-run hours after the first round settled is timed from the
// re-run rather than from the commit's first ever pending read.
var pendingChecks = newSightings(defaultCheckStallDeadline)

// observe records now as the first moment this process saw ref at headSHA
// in the state the caller is timing -- unless it has already recorded one
// for that exact pair -- and returns that moment together with the window
// in effect. Both come back from under one lock, so a window changed
// mid-cycle is never compared against a sighting taken under the old one.
//
// now comes from the caller (SyncPullRequests') clock, the same one that
// recorded the sighting, so nothing here ever compares grain's clock
// against GitHub's -- which is the trap in measuring either of these from
// a timestamp GitHub stamped, like the pull request's own CreatedAt.
//
// A window of zero is off, and off records nothing: a caller with no
// clock to consult has no sighting to consult it against.
func (s *sightings) observe(ref Ref, headSHA string, now time.Time) (time.Time, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.window <= 0 {
		return time.Time{}, s.window
	}
	key := ref.String()
	seen, ok := s.seen[key]
	if !ok || seen.headSHA != headSHA {
		seen = sighting{headSHA: headSHA, first: now}
		s.seen[key] = seen
	}
	return seen.first, s.window
}

// forget drops ref's sighting, so the next observe starts its clock
// again. Called when a pull request closes -- the one moment this package
// knows it will never be synced again, and so where the map stops growing
// -- and, for pendingChecks, on any cycle that reads something other than
// the state it is timing. Anything that stops being tracked without ever
// reading closed leaks one small entry until the process restarts, which
// is a bound worth having over pruning the map against the live set every
// cycle: two daemons sharing a process (as tests do) would then each
// delete the other's clocks and leave every pull request starting afresh
// every cycle.
func (s *sightings) forget(ref Ref) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seen, ref.String())
}

// currentWindow is the duration these sightings are being measured
// against, read under the same lock everything else here is.
func (s *sightings) currentWindow() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.window
}

// setWindow replaces the duration and returns the function that puts the
// old one back.
//
// Changing it clears the sightings taken under the old one. Measuring an
// existing sighting against a window it was not started for is
// meaningless in the shortening direction and wrong in the lengthening
// one, and starting every clock afresh is the conservative reset -- it
// can only make the next merge later, or the next escalation later, never
// either one sooner.
func (s *sightings) setWindow(d time.Duration) func() {
	s.mu.Lock()
	prev := s.window
	s.window = d
	s.seen = map[string]sighting{}
	s.mu.Unlock()
	return func() { s.setWindow(prev) }
}

// SetCheckRegistrationWindow replaces the window described on
// defaultCheckRegistrationWindow and returns the function that puts the
// old one back -- the same shape DisableBranchExistsSleep has, and used
// the same way.
//
// Zero disables the wait outright: an empty check list is believed the
// first time it is read, which is what this package did before the window
// existed. That is the right setting for a caller driving a simulated
// GitHub whose repos have no CI at all and never will -- every test in
// tests/e2e and cmd/grain is one -- where the only thing the window can
// do is make each of them sit out two minutes of wall clock for a signal
// that is never coming. It is the wrong setting for a deployment: see
// defaultCheckRegistrationWindow for what the wait is buying.
func SetCheckRegistrationWindow(d time.Duration) func() { return emptyChecks.setWindow(d) }

// SetCheckStallDeadline replaces the deadline described on
// defaultCheckStallDeadline and returns the function that puts the old
// one back, the same shape as SetCheckRegistrationWindow.
//
// Zero switches giving up off: PrPending is then waited on forever, which
// is what this package did before the deadline existed. Note that zero
// means the opposite direction to zero above -- one disables a wait, the
// other disables giving up on one -- and both are the setting that leaves
// the merge queue doing less on its own.
func SetCheckStallDeadline(d time.Duration) func() { return pendingChecks.setWindow(d) }

// CheckStallDeadline is that deadline as it stands now -- zero where
// giving up is switched off.
//
// It exists because the merge queue has a second thing to say about the
// same wait: orchestrator's liveness table flags an entry that has been in
// one state for longer than the component driving it should take, and for
// waiting_ci that patience is this deadline plus a cycle's grace. Reading
// it rather than restating two hours is what keeps the two from
// disagreeing -- a suite that shortens the deadline shortens the patience
// with it, and one that switches giving up off is not then told its
// pull requests are stuck.
func CheckStallDeadline() time.Duration { return pendingChecks.currentWindow() }

// EmptyChecksSettled reports whether ref's current head commit has had an
// empty check list for longer than the registration window --
// HealthFrom's checksSettled. Call it only when the check list actually
// is empty: the first such call for a given commit is what starts that
// commit's window.
//
// An empty headSHA is never settled, short of the window being off
// altogether. GitHub fills the head sha in asynchronously the same way it
// computes Mergeable (PullRequestDetail's own doc comments), so a read
// without one is a read of a pull request GitHub has not finished
// thinking about -- not a commit whose CI can be concluded absent.
func EmptyChecksSettled(ref Ref, headSHA string, now time.Time) bool {
	first, window := emptyChecks.observe(ref, headSHA, now)
	// A window of zero is off, and off means every path here answers the
	// way it did before the window existed -- including the one below
	// that has nothing to do with elapsed time.
	if window <= 0 {
		return true
	}
	if headSHA == "" {
		return false
	}
	// Written as a comparison against the deadline rather than as
	// `now.Sub(first) >= window`, so a clock that jumped backwards reads
	// as "not settled yet": the same direction everything else here errs
	// in.
	return !now.Before(first.Add(window))
}

// ForgetEmptyChecks drops ref's registration-window sighting; see
// sightings.forget.
func ForgetEmptyChecks(ref Ref) { emptyChecks.forget(ref) }

// ChecksStalled reports whether ref's current head commit has read
// PrPending for longer than defaultCheckStallDeadline, and the deadline
// it was measured against (which escalateStalledChecks puts in what it
// tells the task's own thread). Call it only on a cycle that actually
// read PrPending, and call ForgetPendingChecks on every cycle that did
// not: the first such call for a given commit is what starts its clock,
// and forgetting is what keeps that clock measuring one unbroken run of
// pending reads rather than the span between two of them.
//
// Unlike EmptyChecksSettled an empty headSHA is timed rather than
// excused. There the sha is the thing being reasoned about -- no commit,
// nothing whose CI can be concluded absent -- while here the pull request
// is plainly stuck whatever GitHub is or is not naming as its head, and a
// head sha that never arrives is one more way for PENDING to last
// forever rather than a reason to allow it.
func ChecksStalled(ref Ref, headSHA string, now time.Time) (bool, time.Duration) {
	return ChecksStalledWithin(ref, headSHA, now, 0)
}

// ChecksStalledWithin is ChecksStalled measured against timeout instead,
// where timeout is set and shorter than the deployment's deadline -- a
// path's own `stall_timeout` (grain/task-1659). It can only shorten the
// wait: a deadline switched off stays off, and a longer timeout is the
// deployment's.
func ChecksStalledWithin(ref Ref, headSHA string, now time.Time, timeout time.Duration) (bool, time.Duration) {
	first, deadline := pendingChecks.observe(ref, headSHA, now)
	if deadline <= 0 {
		return false, 0
	}
	if timeout > 0 && timeout < deadline {
		deadline = timeout
	}
	return !now.Before(first.Add(deadline)), deadline
}

// StalledChecks counts the pull requests, across this whole process,
// whose head has read PrPending for at least the stall deadline, and
// says when the oldest of those waits began. The merge queue reads it as
// one of its signs that grain CI is short of machines deployment-wide
// rather than stuck on one pull request (orchestrator.ciStarvation).
// When giving up is switched off, or nothing has stalled, it returns zero.
func StalledChecks(now time.Time) (int, time.Time) {
	pendingChecks.mu.Lock()
	defer pendingChecks.mu.Unlock()
	window := pendingChecks.window
	if window <= 0 {
		return 0, time.Time{}
	}
	n, oldest := 0, time.Time{}
	for _, seen := range pendingChecks.seen {
		if now.Before(seen.first.Add(window)) {
			continue
		}
		n++
		if oldest.IsZero() || seen.first.Before(oldest) {
			oldest = seen.first
		}
	}
	return n, oldest
}

// ForgetPendingChecks drops ref's stall-deadline sighting; see
// sightings.forget.
func ForgetPendingChecks(ref Ref) { pendingChecks.forget(ref) }

// workflowFallbackOnce keeps the "falling back to Actions" notice to one
// line per process: like checksUnavailable below it is a standing
// property of the deployment's credential, not an event.
// verdictSourceFailedOnce keeps a reporting service that is down to one
// line rather than one per pull request per tick, for the reason
// workflowFallbackOnce below does the same: the condition persists, the
// fallback is silent and correct, and a line a cycle would bury
// everything else in the journal.
var verdictSourceFailedOnce sync.Once

var workflowFallbackOnce sync.Once

// checksUnavailableOnce keeps the "no Checks access" notice below to one
// line per process rather than one per pull request per cycle: it is a
// standing property of the deployment's credential, not an event, and it
// never resolves on its own while that credential is in use.
var checksUnavailableOnce sync.Once

// checksUnavailable backs ChecksUnavailable -- set alongside the log line
// checksUnavailableOnce prints, so a caller outside this package can ask
// the same question without grepping server logs for it.
var checksUnavailable atomic.Bool

// ChecksUnavailable reports whether this process has ever seen GitHub
// refuse a check-runs read with 403 (CheckRuns's own doc comment).
// Once true it stays true for the rest of the process's life -- the same
// permanent-until-restart fact the "no Checks access" log line already
// reports -- which is what lets pkg/ui surface a "submit is queued but
// will never actually merge" notice on this deployment's own /api/config
// (bwsalmon/agents#483) instead of an operator having to notice a task's
// AutoMerge never resolving and go searching logs to learn why.
func ChecksUnavailable() bool {
	return checksUnavailable.Load()
}

// CheckRuns reads ref's CI state, reporting whether the answer is
// known at all. It tries two endpoints, because no single one is
// readable by every credential type a deployment might be configured
// with:
//
//  1. The Checks API, which sees every check on the commit whoever
//     reported it. Needs the `repo` scope on a classic PAT; a GitHub App
//     installation token has it too.
//  2. Failing that with a permission error, the Actions API, which sees
//     GitHub Actions workflow runs and nothing else. This is the only one
//     of the two a fine-grained PAT can reach -- "Checks" cannot be
//     granted to one at all, so a deployment on a fine-grained token
//     needs "Actions" read or it has no CI signal whatsoever.
//
// The two do not see the same failures, and one gap between them is
// worth knowing before anybody moves a deployment from the second
// endpoint to the first. A workflow run GitHub concludes before any job
// of it starts leaves no check run behind at all -- not a red one, none:
// its check suite exists and is empty, because no job was ever created
// (measured on three public repositories, 2026-09-07; see
// docs/state-repo-check-posted-by-grain.md). So a repository whose
// Actions cannot start reads as failing here only through the fallback,
// and reads as *clean* to any credential that can reach the Checks API.
// A deployment that adopts a GitHub App -- to read a non-Actions
// provider's checks, say -- stops seeing that failure and starts
// auto-merging what it used to block, which for this deployment's own
// state repository is its settings merging unvalidated.
//
// A permission error from both is a fact about the credential rather
// than a failure to handle: it will repeat on every call, forever, so
// failing the sync on it would leave every tracked pull request erroring
// every cycle. The health of those PRs goes unknown instead -- which
// costs auto-merge, and nothing else: dispatch, the push, and opening
// the PR are a separate reconciler that never reads CI.
//
// Every other error is still an error, and that deliberately includes
// the other conditions GitHub answers 403 for -- rate limits, SAML
// enforcement, an organization IP allow list. Those clear, on their own
// or with a change an operator can make, so swallowing one as "this
// credential cannot read CI" would switch auto-merge off until the next
// restart over something that had already fixed itself (checksUnavailable
// below never clears within a process). github.IsPermissionDenied draws
// that line by message, not by status.
//
// Both endpoints are scoped to headSHA -- the commit the caller read off
// the pull request a moment ago, and the commit whose health it is about
// to decide. The Checks API takes any ref, including the head branch's
// name, and a branch-scoped read is a read of whatever that branch points
// at *now*: a push landing between the caller's GetPullRequest and this
// call answers for a commit the caller never saw, so the check list, the
// verdict computed from it and the registration-window sighting keyed on
// the caller's headSHA would each describe a different commit. Naming the
// commit costs nothing and keeps all three talking about one (a push
// during the cycle then simply makes the *next* cycle start that new
// commit's window afresh, which is what sighting.headSHA is for).
//
// head, the branch name, is the fallback for the one case there is no sha
// to name: headSHA may be empty on a PR read before GitHub filled it in.
// A branch-scoped read is then better than no CI signal at all, since the
// caller's own headSHA is empty too and EmptyChecksSettled already
// refuses to conclude anything from an empty list without one. The
// Actions fallback below has no such fallback of its own -- it takes a
// commit and nothing else -- so it is skipped rather than widened.
func CheckRuns(client github.Client, from Verdicts, ref Ref, head, headSHA string) (
	[]github.CheckRun, bool, error) {

	// Somewhere that already knows, asked first and never trusted with a
	// partial answer (verdicts.go). Only ever a way to save a GitHub
	// read: anything short of the whole answer -- a build still running,
	// a service that is down, a repository that reports nothing --
	// falls through to exactly what this function did before the seam
	// existed.
	//
	// headSHA and not `commit` below: a verdict is about a tree, and the
	// branch-name fallback resolves to whatever that branch points at
	// now. A source has nothing to say about a commit nobody has named.
	if from != nil && headSHA != "" {
		reported, complete, err := from.For(ref, headSHA)
		switch {
		case err != nil:
			verdictSourceFailedOnce.Do(func() {
				log.Printf("forge: could not read reported verdicts for %s (%v) -- falling back "+
					"to asking GitHub, which is what this deployment did before it had a "+
					"reporting service. This is logged once.", ref, err)
			})
		case complete:
			return reported, true, nil
		}
	}

	commit := headSHA
	if commit == "" {
		commit = head
	}
	checks, err := client.ListCheckRuns(ref.Owner, ref.Repo, commit)
	if err == nil {
		return checks, true, nil
	}
	if !github.IsPermissionDenied(err) {
		return nil, false, fmt.Errorf("forge: reading check runs for %s: %w", ref, err)
	}
	checksErr := err

	if headSHA != "" {
		runs, err := client.ListWorkflowRuns(ref.Owner, ref.Repo, headSHA)
		if err == nil {
			workflowFallbackOnce.Do(func() {
				log.Printf("forge: this deployment's GitHub credential cannot read check " +
					"runs, so pull request health comes from GitHub Actions workflow runs " +
					"instead. CI reported by anything other than Actions is invisible -- grant " +
					"the `repo` scope (classic PAT) or install a GitHub App if this deployment " +
					"has checks from another provider.")
			})
			return runs, true, nil
		}
		if !github.IsPermissionDenied(err) {
			return nil, false, fmt.Errorf("forge: reading workflow runs for %s: %w", ref, err)
		}
	}

	checksUnavailableOnce.Do(func() {
		checksUnavailable.Store(true)
		// GitHub's own refusal goes in the line: it is the only place an
		// operator can see which permission is missing, and this is the
		// one time the process ever reports it.
		log.Printf("forge: this deployment's GitHub credential can read neither check "+
			"runs nor Actions workflow runs -- pull request health stays unknown and nothing "+
			"is auto-merged. Everything else is unaffected. Grant the `repo` scope (classic "+
			"PAT) or \"Actions\" read (fine-grained PAT), then restart. GitHub said: %v", checksErr)
	})
	return nil, false, nil
}
