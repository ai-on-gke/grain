package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Trunk release gates: the runs a release from trunk is selected on, and
// what grain does when one of them ends without a verdict about the tree
// (docs/design/release-from-trunk.md, RFT-2).
//
// # Priority
//
// A release candidate is the newest `main` commit whose post-merge suite
// (basebranchci.go), release run (basebranchrelease.go) and nightly
// (scheduledworkflow.go) are green. On 2026-10-22 the only nightly on the
// branch being released was starved behind ordinary task sandboxes and
// then cancelled when the host ran out of memory, because it was ranked
// like any other job. So those runs ask for ci.PriorityReleaseGate: above
// a merge-queue head, allowed into the repair reserve, and with a hole
// held for a large job from its first refusal (pkg/ci/gate.go).
//
// # `infra` verdicts
//
// A run that ends cancelled, errored or interrupted said nothing about the
// tree, and neither did a failure whose every failing job failed on the
// deployment rather than on the code: a capability or credential grain
// could not hand over, a quota, a registry rate limit. Each of those is
// re-run -- the jobs that did not pass and only them, uncached
// (ci.Request.Recheck), so the passes the run did reach are kept -- up to
// maxInfraRetries times and no sooner than infraRetryDelay after the last,
// before anybody is filed to repair or review anything.

// maxInfraRetries is how many automatic re-runs an `infra` verdict on a
// trunk release gate gets before it is treated as the verdict it is.
const maxInfraRetries = 2

// infraRetryDelay spaces those re-runs: a quota or a starved host is
// given a few minutes to recover rather than asked again in the same
// second. The same delay a task branch's errored run waits (ciRetryDelay).
const infraRetryDelay = ciRetryDelay

// releaseGateWorkflowStems are the scheduled workflows whose runs on the
// default branch are release gates: the nightly suite LKG selection
// requires, and the release pipeline itself.
var releaseGateWorkflowStems = map[string]bool{
	"nightly":       true,
	"release-train": true,
}

// releaseGatePriority is the rank a run on repo's branch asks for at the
// job gate (ci.Request.Priority): ci.PriorityReleaseGate on the
// repository's default branch -- trunk, the one branch releases are cut
// from -- and nothing extra on any other. A default branch that cannot be
// read is not trunk: the run is ranked as it was before this existed,
// which costs it capacity rather than giving the reserve to a branch that
// may not deserve it.
func releaseGatePriority(deps Deps, repo model.RepoRef, branch string) ci.Priority {
	if isTrunk(deps, repo, branch) {
		return ci.PriorityReleaseGate
	}
	return ci.PriorityNormal
}

// isTrunk reports whether branch is repo's default branch.
func isTrunk(deps Deps, repo model.RepoRef, branch string) bool {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if deps.Client == nil || branch == "" {
		return false
	}
	def, err := deps.Client.DefaultBranch(repo.Owner, repo.Name)
	if err != nil {
		sayOnce("trunk-unknown:"+repo.String(), "orchestrator: reading %s's default branch: %v", repo, err)
		return false
	}
	return def != "" && def == branch
}

// infraVerdict reports whether a finished run's red is the deployment's
// rather than the tree's, and says why in a clause.
//
//   - interrupted, cancelled or errored: nothing in the run reached a
//     verdict about the tree (ci.concludeRun only concludes errored when
//     no job failed);
//   - a failure every one of whose failing jobs failed on a known
//     infrastructure signature (infraSignature) -- one failing job that
//     did not makes it a test failure, whatever the others did.
func infraVerdict(run ci.Run) (string, bool) {
	if run.Interrupted {
		return "the run was interrupted by the daemon that was running it stopping", true
	}
	switch run.Conclusion {
	case ci.ConclusionCancelled:
		return "the run was cancelled before it reached a verdict", true
	case ci.ConclusionErrored:
		return "no job of the run reached a verdict about the tree", true
	case ci.ConclusionFailure:
	default:
		return "", false
	}
	var why []string
	for _, job := range run.Jobs {
		if job.Conclusion != ci.ConclusionFailure {
			continue
		}
		sig := infraSignature(job)
		if sig == "" {
			return "", false
		}
		why = append(why, job.JobID+" ("+sig+")")
	}
	if len(why) == 0 {
		return "", false
	}
	return "every failing job failed on the deployment rather than the tree: " + strings.Join(why, ", "), true
}

// infraJobErrors are what grain's own account of a job (JobRun.Error,
// StepRun.Error -- never a test's output) says when the job could not be
// given what it needed: a capability or credential, or a quota behind
// one. Grain wrote those words, so they can be read broadly.
var infraJobErrors = []string{
	"capabilit", "credential", "quota", "rate limit", "rate-limit", "429", "too many requests",
	"resource_exhausted", "not granted",
}

// infraStepOutput are external rate-limit and registry signatures read
// out of a failing step's own output. A test's output is the tree's, so
// only signatures specific enough that no assertion prints them by
// accident are here: GitHub/GCP/registry rate limiting and quota, and
// the docker client's own wording for a registry 5xx.
var infraStepOutput = []string{
	"429 too many requests", "toomanyrequests", "resource_exhausted", "rate limit exceeded",
	"quota exceeded", "received unexpected http status: 5",
}

// infraStepTail is how much of a failing step's output is searched: the
// end, which is where a tool that gave up says why.
const infraStepTail = 4096

// infraSignature is the infrastructure signature a failed job failed on,
// or "" for a failure that is the tree's.
func infraSignature(job ci.JobRun) string {
	if sig := firstContained(job.Error, infraJobErrors); sig != "" {
		return sig
	}
	for _, step := range job.Steps {
		if step.Conclusion != ci.ConclusionFailure {
			continue
		}
		if sig := firstContained(step.Error, infraJobErrors); sig != "" {
			return sig
		}
		out := step.Output
		if len(out) > infraStepTail {
			out = out[len(out)-infraStepTail:]
		}
		if sig := firstContained(out, infraStepOutput); sig != "" {
			return sig
		}
	}
	return ""
}

func firstContained(text string, needles []string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return n
		}
	}
	return ""
}

// infraRetrySuffix marks a run's RequestedBy as the nth automatic re-run
// of an `infra` verdict, which is where the count lives for the rows that
// have no column for it (model.BaseBranchRelease counts releases of a
// commit, not re-runs of one; model.ScheduledWorkflowRun counts starts):
// the run being collected says which re-run it was.
const infraRetrySuffix = "#infra-retry-"

// infraRetryRequester is base's RequestedBy for its nth re-run. The base
// stays a prefix, so anything reading RequestedBy by prefix (a scheduled
// run's "workflow-schedule:") reads a re-run as the run it re-runs.
func infraRetryRequester(base string, n int) string {
	if n <= 0 {
		return base
	}
	return base + infraRetrySuffix + strconv.Itoa(n)
}

// infraRetriesOf is how many automatic re-runs the run that asked as
// requestedBy was preceded by: zero for an original run.
func infraRetriesOf(requestedBy string) int {
	i := strings.LastIndex(requestedBy, infraRetrySuffix)
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(requestedBy[i+len(infraRetrySuffix):])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// startInfraRetry asks grain CI to re-run the jobs of run that did not
// pass, uncached, as the nth re-run of base: the request req is the one
// the original run was asked with, so the re-run is the same question
// narrowed to what is still unanswered. A run whose non-passing jobs
// cannot be named (it never got as far as having jobs) is asked about in
// full, still uncached.
func startInfraRetry(ctx context.Context, deps Deps, req ci.Request, run ci.Run, base string, n int,
	why string) (ci.Run, error) {

	if jobs := jobsToRecheck(run); len(jobs) > 0 {
		req.Jobs = jobs
	}
	req.Recheck = true
	req.RequestedBy = infraRetryRequester(base, n)
	retry, err := deps.CI.Start(ctx, req)
	if err != nil {
		return ci.Run{}, fmt.Errorf("re-running %s's %s at %s: %w", req.Repo, req.Ref, shortSHA(req.Commit), err)
	}
	what := "the whole run"
	if len(req.Jobs) > 0 {
		what = strings.Join(req.Jobs, ", ")
	}
	log.Printf("orchestrator: run %s on %s %s at %s ended without a verdict about the tree (%s): "+
		"re-running %s uncached as run %s (automatic re-run %d of %d)",
		run.ID, req.Repo, req.Ref, shortSHA(req.Commit), why, what, retry.ID, n, maxInfraRetries)
	return retry, nil
}
