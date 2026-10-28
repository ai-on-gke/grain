package forge

import (
	"github.com/ai-on-gke/grain/pkg/github"
)

// HealthFrom computes a Health from a fresh GitHub read, the pure
// half of SyncPullRequests split out so it needs no store or client to
// test. A closed PR reads PrMerged or PrClosed off detail.Merged --
// GitHub's own field for the one distinction detail.State alone collapses
// (github.PullRequestDetail.State's own doc comment) -- which is what
// lets recordPullRequestEvents tell a task's timeline "PR merged" from
// "PR closed unmerged" (bwsalmon/agents#493).
//
// A check GitHub has not finished running reads PrPending unless another
// check has already failed. A failure is enough to know the pull request
// needs repair, so it wins immediately and lets the repair run alongside
// the checks that are still finishing. Pending still prevents a clean
// verdict, and therefore prevents merging, when no failure is known.
//
// An *empty* check list is the case no read of GitHub can resolve on its
// own: a repo with no CI configured and a repo whose CI has not
// registered its first check run yet look exactly alike from the Checks
// API. checksSettled is the caller's answer to that -- true once the
// current head commit has been sitting there with nothing reported for
// long enough that CI would have shown up if there were any (see
// EmptyChecksSettled). Until then an empty list reads PrPending, so a
// pull request opened seconds ago waits a cycle or two rather than
// merging in the gap between the push and GitHub creating the workflow
// run's check runs. checksSettled is only consulted for an empty list;
// once even one check exists, the rules above already decide.
//
// Failing deliberately outranks pending. The queue asks for exactly one
// automatic repair per PR (requeueForRepair, escalateToUser), and starting
// it as soon as the first failure is known overlaps the repair with the
// remaining CI work instead of waiting for a verdict that cannot become
// clean.
//
// Of the conclusions a completed run can carry, "failure", "timed_out"
// and "startup_failure" (the last only ever from the Actions fallback,
// where a workflow file too broken to run at all lands) read as
// PrFailing: each is a check that ran, or tried to, and did not pass --
// which is what a repair exists to fix. "neutral" and "skipped" read as
// passing: CheckRun's own doc comment leaves which conclusions count as
// broken to the caller, and treating every non-"success" run as failing
// would file a fix task against a job the workflow deliberately did not
// run.
//
// "action_required" (a workflow run waiting on a maintainer's approval)
// and "stale" (GitHub giving up on a check left incomplete for fourteen
// days) read PrPending, as does a completed run with no conclusion at
// all. Decided 2026-10-02, where all three used to fall through to
// PrClean -- which let a pull request whose only CI was waiting for
// approval merge without ever having been tested, and GitHub's own
// branch protection does not count "action_required" as a pass either.
// None of them is a failure: no agent can approve a workflow run, and a
// stale check has nothing broken in it. They take the "cancelled" path
// below -- bounded by the stall deadline, after which a person is told
// -- with action_required named apart (AwaitingApprovalChecks) so that
// person is told to approve the run rather than to re-run it.
//
// # "cancelled" is neither, and reads PrPending
//
// Decided 2026-09-08, where this used to answer PrClean; the case for
// each of the three answers, since this is the one conclusion where all
// three are arguable.
//
// A cancelled check delivered *no verdict*: the tree it was asked about
// was never judged. Reading it as clean merges on the strength of a
// suite that never reported -- on this repository the cancelled check
// caught in the field was `go`, the whole Go suite (see
// docs/hand-runs/2026-09-07-merge-queue.md section 3, which is where
// this decision was filed from). Reading it as failing is the mistake
// the paragraph above avoids: it dispatches an agent at a job nobody
// has any reason to think is broken, and it would have a merge queue's
// own "cancelled, will retry" check block a PR this package has no
// business blocking. PENDING is the third answer and the honest one --
// this commit has no verdict on that check *yet* -- and it is the one
// the rest of this function already reaches for whenever CI has not
// spoken.
//
// It fails toward waiting, and the wait is bounded by the clock every
// other PENDING read is bounded by: past defaultCheckStallDeadline the
// queue stops waiting, says what it was waiting on and hands the pull
// request to a person (ChecksStalled, escalateStalledChecks). So a
// deployment whose CI legitimately leaves a check cancelled on a head
// commit pays one stall per head commit and is told once, instead of
// merging silently; and the "cancelled, will retry" case resolves
// inside that window with nobody doing anything, because the retry's
// own run registers against the same head sha long before it runs out.
// Nothing is filed against anyone in either case: a stall escalation is
// a comment and a queue position, never a fix task.
//
// Failing beats cancelled too. A cancelled check will never say anything
// more on its own, and a known failure already makes repair necessary.
// That keeps a fail-fast matrix -- one job red, its siblings cancelled
// out from under it, which is an ordinary shape on GitHub Actions -- on
// the repair path instead of sitting out two hours to reach a person.
//
// grain CI's own cancelled runs are a separate required-success gate
// (grainCIVerdictOf, WithGrainCI): they are not failures, but they leave
// that gate pending until the agent explicitly requests and passes the
// full suite. A cancellation on GitHub's side is one grain neither caused
// nor can re-run, and a re-run is exactly what resolves it.
//
// GitHub's own mergeable_state is not a way out of the judgement, for
// the record: a cancelled check leaves it "unstable" where the check is
// not required and "blocked" where it is, and "blocked" also covers a
// missing review, so it cannot separate this from a block that has
// nothing to do with CI. It is out of this gate for reasons of its own
// (github.PullRequestDetail.MergeableState) and this does not change
// that.
//
// grain's own CI (pkg/ci) is still not among the checks judged here, and
// is still invisible to this function: a grain CI run is not a GitHub
// check run, reports no commit status, and reaches `checks` by no route
// at all. HealthOf selects between this GitHub CI verdict and grain CI's
// verdict; it does not require both.
//
// The two are separate because their questions are. Every hedge above --
// checksKnown, checksSettled, the registration window -- exists because
// nothing GitHub serves tells "this repository has no CI" apart from "its
// CI has not spoken yet". grain CI never has to ask: it started the run
// itself, and what it knows is written on the task. See WithGrainCI for
// what is done with that.
//
// requiredCheck is the one exception to all of that, and it exists for
// one repository: the deployment's own state repository, on a deployment
// that checks its settings changes itself and posts the answer as a
// check run (pkg/staterepo/postcheck.go). Named, that check missing is
// PrPending rather than nothing to report, whatever else is on the
// commit -- because for a self-posted check "absent" is the shape a
// deployment that was down for the whole life of the pull request
// leaves, and reading that as PrClean is a gate that fails open. Empty,
// which is every other pull request grain watches, nothing here changes.
func HealthFrom(detail github.PullRequestDetail, checks []github.CheckRun, requiredCheck string, checksKnown, checksSettled bool) Health {
	if detail.State == "closed" {
		if detail.Merged {
			return Merged
		}
		return Closed
	}
	if detail.Mergeable != nil && !*detail.Mergeable {
		return Conflicted
	}
	// Checks we could not read are not checks that passed. Both of the
	// facts above are read straight off the PR and stay authoritative
	// without them, but everything below this line is a judgement about
	// CI, and an empty `checks` is how a genuinely clean PR looks too --
	// so a deployment whose credential cannot reach the Checks API at
	// all (CheckRuns, on a scoped PAT) would otherwise read every PR
	// as PrClean and merge it with CI red. PrUnknown is the honest
	// answer, and the one syncEntry declines to act on.
	if !checksKnown {
		return Unknown
	}
	// A completed failure is already enough to require repair. Check it
	// before unfinished and missing checks so the repair can start while
	// the rest of CI continues in parallel.
	if len(FailingChecks(checks)) > 0 {
		return Failing
	}
	for _, c := range checks {
		if c.Status != "completed" {
			return Pending
		}
	}
	// The check this deployment posts itself, not (yet) here. PrPending
	// rather than PrUnknown for the same reason the empty list below is:
	// this is a thing that resolves on its own, within a sync tick of
	// grain seeing the pull request, and one that does not resolve is a
	// pull request stalling on a check that never finishes --
	// escalateStalledChecks' business, and a person's, which is exactly
	// where a deployment that cannot check its own settings belongs.
	//
	// Above the empty-list clause, so that a settled empty list cannot
	// answer for a commit this deployment has not vouched for.
	if requiredCheck != "" && !hasCheck(checks, requiredCheck) {
		return Pending
	}
	// Nothing reported at all, and not yet for long enough to believe
	// that means there is nothing to report. PrPending rather than
	// PrUnknown: this resolves by itself, on a clock, which is what
	// PENDING says and what UNKNOWN does not.
	if len(checks) == 0 && !checksSettled {
		return Pending
	}
	// Below the failing arm on purpose: see "cancelled is neither" above.
	if len(CancelledChecks(checks)) > 0 || len(AwaitingApprovalChecks(checks)) > 0 {
		return Pending
	}
	if detail.Mergeable == nil {
		return Unknown
	}
	return Clean
}

// hasCheck reports whether a check run of that name is on the commit at
// all -- running, passed or failed. Only its presence is asked about
// here: a red one is FailingChecks' business below, and a still-running
// one is already PrPending by the time this is reached.
func hasCheck(checks []github.CheckRun, name string) bool {
	for _, c := range checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

// HealthOf is a pull request's health as this package actually decides it.
// A task that requires grain CI uses that verdict as its only CI gate;
// otherwise it uses GitHub's checks. Pull-request state and mergeability
// still come from GitHub in both cases. Both callers -- queueEntry.observe
// and syncEntry's re-read after a merge -- go through here, so there is one
// policy rather than two.
func HealthOf(detail github.PullRequestDetail, checks []github.CheckRun, requiredCheck string,
	checksKnown, checksSettled bool, grain GrainCI) Health {
	effectiveChecks := checks
	if grain.Passed {
		// When internal Grain CI is configured and has passed (all native
		// Grain CI checks are green), external GitHub Actions workflow conclusion
		// failures caused specifically by account spending/budget limits (0 steps executed
		// with budget error message) are non-blocking / ignored.
		effectiveChecks = FilterActionsBudgetFailures(checks)
	}
	health := HealthFrom(detail, effectiveChecks, requiredCheck, checksKnown, checksSettled)
	if grain.Required {
		// A repository using grain CI chose it as its CI provider. GitHub's
		// check runs may still exist -- while migrating, or because Actions
		// is disabled only temporarily -- but they are not a second gate.
		// Keep facts about the pull request itself (closed, merged,
		// conflicted, or GitHub still computing mergeability), while deriving
		// no health from GitHub's CI answer.
		health = HealthFrom(detail, nil, "", true, true)
	}
	return WithGrainCI(health, grain)
}

// GrainCICheckName is what grain's own CI is called in the sentences the
// merge queue writes about a pull request -- the repair it asks for
// (healthReason), and the stall it gives up on (stalledChecksPhrase).
// GitHub's checks are named there by the names GitHub gave them, and
// this is grain CI's: one name, not a job list, because the jobs are in
// the verdict's own detail and in the comment reconcileCI has already
// left on the task.
const GrainCICheckName = "grain CI"

// GrainCI is what grain's own CI has to say about the one commit a merge
// would land. Required is the capability-backed gate; Passed is true only
// for a complete successful run against that exact commit. Selected runs
// never reach this value because they are not recorded as task verdicts.
//
// It is a value computed per cycle from what is already in the store
// rather than anything read from the CI service. SyncPullRequests has no
// CIRunner and wants none: the answer it needs is the one written where
// the task is read (model.CIStatus, which is a copy for exactly this
// reason), and a merge queue that had to ask a running engine what it
// thought would be a merge queue that stops working when the engine does.
type GrainCI struct {
	// Required says this task holds the grain-ci capability and therefore
	// needs an exact-commit full-suite success before it may merge.
	Required bool
	// GitHubToo says GitHub's checks gate this commit as well as grain
	// CI -- its merge rules say `ci.mode: both` (or `github`) on a
	// repository that gates on grain CI. Nothing decides differently on
	// it; it lets a wait name GitHub's unfinished checks, which a
	// Required verdict otherwise leaves out as not a source of verdicts.
	GitHubToo bool
	// Passed is a complete successful run against the commit being judged.
	Passed bool
	// Running is a run in flight against the commit being judged.
	Running bool
	// Failed is a run that concluded against that commit and failed.
	Failed bool
	// Errored is a run that concluded against that commit having never
	// managed to test it: no sandbox to run a job in, no CI backend
	// wired at all (ci.ConclusionErrored). It is not a statement about
	// the commit, so it is not Failed -- but it is not an answer either,
	// which is why it is not silence.
	Errored bool
	// Cancelled is a run that concluded against that commit by being
	// stopped: somebody pressed cancel, the daemon shut down under it, a
	// job timed out (ci.ConclusionCancelled). Like Errored it is not a
	// verdict on the tree, so it is not Failed and it cannot satisfy the
	// required-success gate -- the commit stays Pending until a run is
	// asked for again.
	//
	// It is a word of its own rather than the absence of the other four
	// because absence is what it used to be, and absence had nothing to
	// say. A task whose grain CI was cancelled reads exactly like a task
	// whose grain CI has not started: Required and nothing else. The
	// merge queue then waited on a run that was over and would never
	// report, with no sentence it could write about the wait
	// (orchestrator's stalledChecksPhrase) -- which is the silence
	// grain/task-149 sat in for a day. Nothing decides differently on
	// it; it only lets the row say which of the two waits this is, and
	// that only a re-run ends it.
	Cancelled bool
	// Detail is the verdict in the words the task's own record carries
	// (model.CIStatus.Detail) -- which jobs failed, why grain could not
	// run, why nothing ran, or the note whoever cancelled the run left
	// about why they did (ci.Run.CancelReason). Set alongside Failed,
	// Errored and Cancelled, and only ever rendered into a comment or a
	// merge queue row.
	Detail string
	// Replacement is what grain is going to do about a verdict that is
	// not an answer -- a cancelled run, a run that errored -- and it is
	// here because the row has to say so. "Cancelled without a verdict,
	// which nothing but another run will replace" was a true sentence
	// while nothing replaced one; it is a lie about a commit a
	// replacement run is already owed for, and a different lie about one
	// grain has finished trying. See GrainCIReplacement.
	Replacement GrainCIReplacement
	// Attempts is how many runs have been started for this commit,
	// including the one this verdict came from (model.CIStatus.Attempts).
	// Only worth rendering beside Replacement, which is the only thing
	// that makes a count of attempts mean anything.
	Attempts int
}

// GrainCIReplacement is whether another grain CI run is coming for this
// commit, for a verdict that can never turn into a pass on its own.
//
// It is the answer to the question a cancelled or errored verdict leaves
// hanging, and the merge queue's rows and its give-up comment are its
// only readers: nothing decides anything differently on it. The decision
// itself belongs to orchestrator.reconcileTaskCI, which computes this
// value from the same status by the same rule, so a row saying a
// replacement is due is a row the next cycle makes good on.
type GrainCIReplacement string

const (
	// GrainCIReplacementNone is a verdict nothing is owed for: a pass, a
	// failure the branch itself has to fix, or a run still going.
	GrainCIReplacementNone GrainCIReplacement = ""
	// GrainCIReplacementDue is a verdict grain will replace with another
	// run of its own accord, on the next cycle after the retry delay.
	GrainCIReplacementDue GrainCIReplacement = "due"
	// GrainCIReplacementSpent is a verdict grain has retried as often as
	// it will (orchestrator's ciMaximumAttempts). Nothing further is
	// coming, which makes it a state a person owns.
	GrainCIReplacementSpent GrainCIReplacement = "spent"
)
