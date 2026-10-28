package ci

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// AttributionKind is the classification of a CI job failure as either a regression
// introduced by the branch's changes or an upstream/shared base branch fault.
type AttributionKind string

const (
	// AttributionRegression indicates the job passed on the base branch but failed on this commit.
	AttributionRegression AttributionKind = "REGRESSION"
	// AttributionSharedBaseFault indicates the job failed with the same signature on main or across sibling branches.
	AttributionSharedBaseFault AttributionKind = "SHARED / BASE FAULT"
	// AttributionSharedFaultAssigned indicates the job is a shared fault and this task is the first in queue order assigned to fix it.
	AttributionSharedFaultAssigned AttributionKind = "SHARED FAULT — ASSIGNED TO YOU"
)

const (
	// GuidanceRegression is the explicit guidance provided to agents when a regression is detected.
	GuidanceRegression = "This failure was introduced by your changes."
	// GuidanceSharedBaseFault is the explicit guidance provided to agents when an upstream/shared fault is detected.
	GuidanceSharedBaseFault = "This is a shared upstream issue on the base branch, not caused by your changes. Do not attempt to fix this locally."
	// GuidanceSharedFaultAssigned is the explicit guidance provided to the first task in queue order hitting a shared fault.
	GuidanceSharedFaultAssigned = "You are the first task in the queue hitting this failure (assigned to you to fix). Fix it on your branch so the merge queue can advance."
)

// GuidanceSharedFaultOwnedBy formats agent guidance when an earlier task in the queue (or an active FixesBaseBranch task) owns the fault.
func GuidanceSharedFaultOwnedBy(ownerTaskID string) string {
	return fmt.Sprintf("This is a shared upstream issue on the base branch, not caused by your changes (assigned to Task #%s ([Task #%s](/tasks/%s)) ahead of you in the queue). Do not attempt to fix this locally.", ownerTaskID, ownerTaskID, ownerTaskID)
}

// JobAttribution describes whether a job failure is an introduced regression
// on the current branch or a shared/base fault inherited from upstream or
// observed across multiple sibling branches.
type JobAttribution struct {
	Kind           AttributionKind `json:"kind"`
	JobID          string          `json:"jobId"`
	JobName        string          `json:"jobName,omitempty"`
	BaseRef        string          `json:"baseRef,omitempty"`
	BaseCommit     string          `json:"baseCommit,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Guidance       string          `json:"guidance,omitempty"`
	SiblingRefs    []string        `json:"siblingRefs,omitempty"`
	OwnerTaskID    string          `json:"ownerTaskId,omitempty"`
	AssignedToSelf bool            `json:"assignedToSelf,omitempty"`
}

// WithOwner updates a shared-fault attribution with the resolved owner task ID.
func (a JobAttribution) WithOwner(currentRefOrTaskID, ownerTaskID string) JobAttribution {
	ownerTaskID = strings.TrimSpace(ownerTaskID)
	if ownerTaskID == "" {
		return a
	}
	if a.Kind != AttributionSharedBaseFault && a.Kind != AttributionSharedFaultAssigned {
		return a
	}
	a.OwnerTaskID = ownerTaskID
	selfID := TaskIDFromRef(currentRefOrTaskID)
	if selfID == "" {
		selfID = strings.TrimSpace(currentRefOrTaskID)
	}
	if selfID != "" && selfID == ownerTaskID {
		a.AssignedToSelf = true
		a.Guidance = GuidanceSharedFaultAssigned
	} else {
		a.AssignedToSelf = false
		a.Guidance = GuidanceSharedFaultOwnedBy(ownerTaskID)
	}
	return a
}

// Summary formats the attribution as a single annotated string, e.g.:
// "[REGRESSION] The job passed on the base branch at commit abc1234 but failed on this commit. This failure was introduced by your changes."
// "[SHARED / BASE FAULT] The job failed with the same signature on main at commit def5678. This is a shared upstream issue on the base branch, not caused by your changes. Do not attempt to fix this locally."
func (a JobAttribution) Summary() string {
	if a.Kind == "" {
		return ""
	}
	kind := a.Kind
	if a.AssignedToSelf && kind == AttributionSharedBaseFault {
		kind = AttributionSharedFaultAssigned
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]", kind)
	if a.Reason != "" {
		fmt.Fprintf(&b, " %s", strings.TrimRight(a.Reason, "."))
		if !strings.HasSuffix(b.String(), ".") {
			b.WriteString(".")
		}
	}
	if a.Guidance != "" {
		fmt.Fprintf(&b, " %s", strings.TrimRight(a.Guidance, "."))
		if !strings.HasSuffix(b.String(), ".") {
			b.WriteString(".")
		}
	}
	return b.String()
}

// AttributeJobFailure classifies one failing job in run against baseRun and siblingRuns.
// Returns ok=false if the job did not fail or if attribution could not be determined.
func AttributeJobFailure(run Run, job JobRun, baseRun *Run, siblingRuns []Run) (JobAttribution, bool) {
	if job.Conclusion != ConclusionFailure && job.Conclusion != ConclusionErrored {
		return JobAttribution{}, false
	}

	sig, sigOk := FailureSignature(run, job)
	matchingSiblings := make(map[string]Run)
	if sigOk && len(siblingRuns) > 0 {
		for _, sib := range siblingRuns {
			if sib.Ref == "" || sib.Ref == run.Ref || (baseRun != nil && sib.Ref == baseRun.Ref) {
				continue
			}
			for _, sj := range sib.Jobs {
				if (sj.JobID == job.JobID || (job.Name != "" && sj.Name == job.Name)) &&
					(sj.Conclusion == ConclusionFailure || sj.Conclusion == ConclusionErrored) {
					sibSig, sibSigOk := FailureSignature(sib, sj)
					if sibSigOk && sibSig.Digest == sig.Digest {
						matchingSiblings[sib.Ref] = sib
					}
				}
			}
		}
	}

	resolveSiblingOwner := func() string {
		ownerID := TaskIDFromRun(run)
		for _, sib := range matchingSiblings {
			tid := TaskIDFromRun(sib)
			if tid == "" {
				continue
			}
			if ownerID == "" || CompareTaskIDs(tid, ownerID) < 0 {
				ownerID = tid
			}
		}
		return ownerID
	}

	// 1. Check if the target base branch failed with the exact same signature.
	if baseRun != nil {
		for _, bj := range baseRun.Jobs {
			if bj.JobID == job.JobID || (job.Name != "" && bj.Name == job.Name) {
				if bj.Conclusion == ConclusionFailure || bj.Conclusion == ConclusionErrored {
					baseSig, baseSigOk := FailureSignature(*baseRun, bj)
					if sigOk && baseSigOk && sig.Digest == baseSig.Digest {
						baseRef := baseRun.Ref
						if baseRef == "" {
							baseRef = "main"
						}
						commit := baseRun.Commit
						if len(commit) > 7 {
							commit = commit[:7]
						}
						reason := fmt.Sprintf("The job failed with the same signature on %s at commit %s.", baseRef, commit)
						if commit == "" {
							reason = fmt.Sprintf("The job failed with the same signature on %s.", baseRef)
						}
						attr := JobAttribution{
							Kind:       AttributionSharedBaseFault,
							JobID:      job.JobID,
							JobName:    job.Name,
							BaseRef:    baseRun.Ref,
							BaseCommit: baseRun.Commit,
							Reason:     reason,
							Guidance:   GuidanceSharedBaseFault,
						}
						if len(matchingSiblings) > 0 {
							attr = attr.WithOwner(run.Ref, resolveSiblingOwner())
						}
						return attr, true
					}
				}
				break
			}
		}
	}

	// 2. Check if sibling task branches failed with the exact same signature.
	if sigOk && len(matchingSiblings) >= 1 {
		var sibRefs []string
		for ref := range matchingSiblings {
			sibRefs = append(sibRefs, ref)
		}
		sort.Strings(sibRefs)
		branchWord := "sibling branches"
		if len(sibRefs) == 1 {
			branchWord = "sibling branch"
		}
		attr := JobAttribution{
			Kind:        AttributionSharedBaseFault,
			JobID:       job.JobID,
			JobName:     job.Name,
			Reason:      fmt.Sprintf("The job failed with the same signature across %d %s.", len(sibRefs), branchWord),
			Guidance:    GuidanceSharedBaseFault,
			SiblingRefs: sibRefs,
		}
		attr = attr.WithOwner(run.Ref, resolveSiblingOwner())
		return attr, true
	}

	// 3. Check if the job passed on the base branch.
	if baseRun != nil {
		for _, bj := range baseRun.Jobs {
			if bj.JobID == job.JobID || (job.Name != "" && bj.Name == job.Name) {
				if bj.Conclusion == ConclusionSuccess {
					commit := baseRun.Commit
					if len(commit) > 7 {
						commit = commit[:7]
					}
					reason := fmt.Sprintf("The job passed on the base branch at commit %s but failed on this commit.", commit)
					if commit == "" {
						reason = "The job passed on the base branch but failed on this commit."
					}
					return JobAttribution{
						Kind:       AttributionRegression,
						JobID:      job.JobID,
						JobName:    job.Name,
						BaseRef:    baseRun.Ref,
						BaseCommit: baseRun.Commit,
						Reason:     reason,
						Guidance:   GuidanceRegression,
					}, true
				}
				break
			}
		}
	}

	return JobAttribution{}, false
}

// FindLatestBaseRun locates the newest completed CI run on the base branch.
func FindLatestBaseRun(repo, baseRef string, runs []Run) *Run {
	if baseRef == "" {
		baseRef = "main"
	}
	var latest *Run
	var latestTime time.Time
	for i := range runs {
		r := &runs[i]
		if r.Repo != "" && repo != "" && r.Repo != repo {
			continue
		}
		if !r.Conclusion.Done() {
			continue
		}
		if r.Ref != baseRef && r.Ref != "refs/heads/"+baseRef && !(baseRef == "main" && (r.Ref == "main" || r.Ref == "master")) {
			continue
		}
		t := timeValue(r.FinishedAt)
		if t.IsZero() {
			t = timeValue(r.StartedAt)
		}
		if latest == nil || t.After(latestTime) {
			latest = r
			latestTime = t
		}
	}
	return latest
}

// FindSiblingRuns returns runs on other task branches (excluding run.Ref and baseRef).
func FindSiblingRuns(run Run, baseRef string, runs []Run) []Run {
	if baseRef == "" {
		baseRef = "main"
	}
	var siblings []Run
	for _, r := range runs {
		if r.ID == run.ID || r.Ref == run.Ref {
			continue
		}
		if r.Repo != "" && run.Repo != "" && r.Repo != run.Repo {
			continue
		}
		if r.Ref == baseRef || r.Ref == "refs/heads/"+baseRef || (baseRef == "main" && (r.Ref == "main" || r.Ref == "master")) {
			continue
		}
		siblings = append(siblings, r)
	}
	return siblings
}

// AttributeRunFailures classifies all failing jobs in run against a baseRun and siblingRuns.
func AttributeRunFailures(run Run, baseRun *Run, siblingRuns []Run) map[string]JobAttribution {
	out := make(map[string]JobAttribution)
	for _, job := range run.Jobs {
		if job.Conclusion != ConclusionFailure && job.Conclusion != ConclusionErrored {
			continue
		}
		if attr, ok := AttributeJobFailure(run, job, baseRun, siblingRuns); ok {
			out[job.JobID] = attr
			if job.Name != "" && job.Name != job.JobID {
				out[job.Name] = attr
			}
		}
	}
	return out
}

// AttributeRun is a convenience helper that finds the latest base run and sibling runs from allRuns and attributes failures in run.
func AttributeRun(run Run, baseRef string, allRuns []Run) map[string]JobAttribution {
	baseRun := FindLatestBaseRun(run.Repo, baseRef, allRuns)
	siblingRuns := FindSiblingRuns(run, baseRef, allRuns)
	return AttributeRunFailures(run, baseRun, siblingRuns)
}
