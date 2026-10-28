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

package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// A shared fault is one failure showing up on several branches at once
// (pkg/ci/sharedfault.go, which is where the judgement about what counts
// as one lives). This file is the half an agent reads.
//
// It exists because the human half shipped without it. The CI pane and a
// task's pane say "this job is failing the same way on N other branches
// right now" and name the open pull request that touches the file the
// failure points at; wait_for_checks and list_ci_runs said nothing, and
// those are what a repair run dispatched onto a red pull request
// actually reads. The afternoon both were written for spent itself
// exactly there: two agents, hours apart, each rediscovering that a
// deployment-wide proxy failure was not their branch's doing, each
// spending a run on it, neither able to see the other had already
// finished the work (grain/task-296 for the pane, grain/task-299 for
// this).
//
// What is deliberately not here: any decision about whether two
// failures are the same fault, or whether a pull request fixes one. Both
// are made once, in Go, where the output that proves them lives, and are
// read back over the daemon's API (GET /api/ci/faults). A sandbox holds
// neither the store nor a GitHub credential, so a tool that tried to
// judge this for itself could only guess -- and a fault annotation that
// blames the wrong change is worse than one that says nothing.

// CIFault is one shared fault as a tool reports it: ui.CIFault over the
// wire, restated here so pkg/mcp keeps its own vocabulary and does not
// take a dependency on the daemon's HTTP types (cmd/grain's
// daemonGrainCI maps one to the other, as it does for every other answer
// it fetches).
type CIFault struct {
	ci.SharedFault
	// Fixes are the open pull requests grain is willing to name as
	// fixing this fault -- already filtered, at the daemon, to the ones
	// touching a file every branch in the fault printed. Empty is the
	// ordinary answer and the honest one.
	Fixes []CIFaultFix
}

// CIFaultFix is one such pull request, and the evidence for naming it.
type CIFaultFix struct {
	PullRequest string
	Task        string
	Title       string
	// Files are the paths it touches that this failure named -- said out
	// loud, because a reader who thinks the claim is wrong should be
	// able to see why it was made without opening anything.
	Files []string
}

// maxReportedFaultPaths bounds how many of a fault's agreed-on paths one
// report lists. The intersection is already small (ci.SharedFault.Paths)
// and the first few are the ones a failure was reported at.
const maxReportedFaultPaths = 4

// faultAdvice is what a report says once, under a run that carries any
// shared fault at all. It is the whole point of telling an agent: the
// diagnosis is already made, and the next move is to check it rather
// than to start rediscovering it.
const faultAdvice = "\n\nAt least one failure above is not this branch's own. Check it before " +
	"changing anything to chase it -- list_ci_runs (ref, job, conclusion) and get_ci_log read the " +
	"other branches' runs -- and fix a shared fault where it comes from, not here. A branch cannot " +
	"go green against a fault it did not cause."

// ciFaultReport is what one failing job's report says about the fault it
// is part of, indented under that job's own lines -- empty where grain
// found none, which is the ordinary red build.
//
// ref is the branch being reported on, and every count and list here is
// of the *others*: an agent is looking at its own branch's failure
// already, and the news is that somebody else has it too. Where the
// reader's branch is the only one grain has a recent run for, there is
// no fault to find in the first place (a fault needs two distinct refs)
// and this says nothing -- the rule the agent path must not weaken, and
// does not, because it never makes the judgement itself.
func ciFaultReport(fault CIFault, ref string) string {
	summary := fault.Summary(ref)
	if summary == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nShared fault: %s", summary)
	if refs := fault.OtherRefsPhrase(ref); refs != "" {
		fmt.Fprintf(&b, "\n  Also failing on: %s", refs)
	}
	if run, ok := otherFaultRun(fault, ref); ok {
		// One of the other runs by id, so that the claim can be read
		// rather than believed: get_ci_log on this id is the same failure
		// on somebody else's branch.
		fmt.Fprintf(&b, "\n  For example grain CI run %s on %s -- read it with get_ci_log", run.RunID, run.Ref)
	}
	if paths := fault.Paths; len(paths) > 0 {
		shown := paths
		if len(shown) > maxReportedFaultPaths {
			shown = shown[:maxReportedFaultPaths]
		}
		fmt.Fprintf(&b, "\n  Every one of them names: %s", strings.Join(shown, ", "))
	}
	for _, fix := range fault.Fixes {
		fmt.Fprintf(&b, "\n  Open already, touching %s: %s", faultFixFiles(fix), fix.PullRequest)
		if fix.Title != "" {
			fmt.Fprintf(&b, " (%s)", fix.Title)
		}
		if fix.Task != "" {
			fmt.Fprintf(&b, ", from task %s", fix.Task)
		}
	}
	if fault.OwnerTaskID != "" {
		if fault.OwnedBy(ref) {
			fmt.Fprintf(&b, "\n  Assigned to you (Task #%s): [SHARED FAULT — ASSIGNED TO YOU] You are the first task in the queue hitting this failure (assigned to you to fix). Fix it on your branch so the merge queue can advance.", fault.OwnerTaskID)
		} else {
			fmt.Fprintf(&b, "\n  Assigned to Task #%s ([Task #%s](/tasks/%s)) ahead of you in the queue: do not attempt to fix this on your branch.", fault.OwnerTaskID, fault.OwnerTaskID, fault.OwnerTaskID)
		}
	}
	return b.String()
}

// faultAdviceForRun returns the paragraph appended under a single run's
// status report when at least one failing job matches a shared fault.
// When ownership is known:
//   - If the calling task owns the fault, it instructs the agent to fix it on
//     this branch rather than saying "not this branch's own".
//   - If an earlier task owns the fault, it names that task explicitly and
//     tells the agent not to fix it on this branch.
func faultAdviceForRun(faults []CIFault, run ci.Run) string {
	var assignedToSelf bool
	var ownerIDs []string
	seenOwner := map[string]bool{}
	hasUnowned := false
	for _, job := range run.Jobs {
		fault, ok := faultForJob(faults, run, job)
		if !ok || fault.Summary(run.Ref) == "" {
			continue
		}
		if fault.OwnerTaskID == "" {
			hasUnowned = true
			continue
		}
		if fault.OwnedBy(run.Ref) {
			assignedToSelf = true
		} else if !seenOwner[fault.OwnerTaskID] {
			seenOwner[fault.OwnerTaskID] = true
			ownerIDs = append(ownerIDs, fault.OwnerTaskID)
		}
	}
	if assignedToSelf {
		return "\n\n[SHARED FAULT — ASSIGNED TO YOU]: You are the first task in the queue hitting this failure (assigned to you to fix). Fix it on your branch so the merge queue can advance."
	}
	if len(ownerIDs) > 0 {
		id := ownerIDs[0]
		return fmt.Sprintf("\n\nAt least one failure above is a shared fault already assigned to Task #%s ([Task #%s](/tasks/%s)) ahead of you in the queue — it is not this branch's own. Do not attempt to fix this on your branch; the merge queue will re-test your branch once Task #%s lands.", id, id, id, id)
	}
	if hasUnowned {
		return faultAdvice
	}
	return faultAdvice
}

// onlyExternallyOwnedFaultsInProgress reports whether run is still in progress
// and every job that has failed on it so far is a shared fault owned by an
// earlier task in the queue. When true, waitForGrainCI keeps waiting for the
// remaining jobs on run to finish so the agent gets a complete verdict on its
// own changes.
func onlyExternallyOwnedFaultsInProgress(ctx context.Context, runner GrainCIRunner, run ci.Run) bool {
	if run.Conclusion.Done() {
		return false
	}
	failed, unfinished := ciJobOutcomes(run)
	if len(failed) == 0 || unfinished == 0 {
		return false
	}
	faults := sharedFaults(ctx, runner, run)
	var attributions map[string]ci.JobAttribution
	if len(faults) == 0 && runner != nil {
		if page, err := runner.FindGrainCIRuns(ctx, ci.RunFilter{Repo: run.Repo, Descending: true}, 50); err == nil {
			attributions = ci.AttributeRun(run, "main", append([]ci.Run{run}, page.Runs...))
		}
	}
	for _, job := range failed {
		if fault, ok := faultForJob(faults, run, job); ok && fault.OwnedByOther(run.Ref) {
			continue
		}
		if attributions != nil {
			if attr, ok := attributions[job.JobID]; ok && attr.OwnerTaskID != "" && !attr.AssignedToSelf {
				continue
			}
			if job.Name != "" {
				if attr, ok := attributions[job.Name]; ok && attr.OwnerTaskID != "" && !attr.AssignedToSelf {
					continue
				}
			}
		}
		return false
	}
	return true
}

// faultFixFiles is why a pull request is being named, in the half of a
// sentence a line has room for.
func faultFixFiles(fix CIFaultFix) string {
	switch len(fix.Files) {
	case 0:
		return "a file this failure names"
	case 1:
		return fix.Files[0]
	default:
		return fmt.Sprintf("%d of the files this failure names", len(fix.Files))
	}
}

// otherFaultRun is one run of this fault that is not on ref -- the
// newest, since ci.SharedFault orders its runs that way. It is the
// evidence a reader can go and read for themselves.
func otherFaultRun(fault CIFault, ref string) (ci.FaultRun, bool) {
	for _, run := range fault.Runs {
		if run.Ref != ref {
			return run, true
		}
	}
	return ci.FaultRun{}, false
}

// sharedFaults fetches what the deployment knows, for a report that has
// something to hang it on. Errors are logged nowhere and swallowed here:
// this annotates a verdict that is worth giving without it, and a
// deployment whose daemon cannot answer must still be able to tell an
// agent that its build went red.
//
// It is asked for only when something actually failed (hasFailingJob),
// so a green run costs no extra call.
func sharedFaults(ctx context.Context, runner GrainCIRunner, runs ...ci.Run) []CIFault {
	if runner == nil {
		return nil
	}
	failing := false
	for _, run := range runs {
		if hasFailingJob(run) {
			failing = true
			break
		}
	}
	if !failing {
		return nil
	}
	faults, err := runner.GrainCISharedFaults(ctx)
	if err != nil {
		return nil
	}
	return faults
}

// hasFailingJob is whether a run has anything a fault could be about:
// the two conclusions ci.FailureSignature will look at, and nothing
// else.
func hasFailingJob(run ci.Run) bool {
	for _, job := range run.Jobs {
		if job.Conclusion == ci.ConclusionFailure || job.Conclusion == ci.ConclusionErrored {
			return true
		}
	}
	return false
}

// ciLogFaultNote is the one line a log read puts under its heading when
// the failure being read is one other branches are failing too.
//
// One line rather than the block a status report gets, because this
// answer is bytes of a log and the reader asked for the log: the point
// here is only to stop somebody reading four hundred lines of a proxy
// error as their own doing before they find out, from another tool, that
// it is not. The branches are named, since they are what turns the claim
// into something that can be checked.
//
// Nothing is asked of the deployment for a job that has not failed --
// which includes every read of a job still running, the shape a tail is
// usually called in.
func ciLogFaultNote(ctx context.Context, runner GrainCIRunner, run ci.Run, job ci.JobRun) string {
	if job.Conclusion != ci.ConclusionFailure && job.Conclusion != ci.ConclusionErrored {
		return ""
	}
	fault, ok := faultForJob(sharedFaults(ctx, runner, run), run, job)
	if !ok {
		return ""
	}
	summary := fault.Summary(run.Ref)
	if summary == "" {
		return ""
	}
	note := "Shared fault: " + summary
	if refs := fault.OtherRefsPhrase(run.Ref); refs != "" {
		note += " Also failing on: " + refs + "."
	}
	if fault.OwnerTaskID != "" {
		if fault.OwnedBy(run.Ref) {
			note += fmt.Sprintf(" Assigned to you (Task #%s) [SHARED FAULT — ASSIGNED TO YOU: Task #%s is first in queue order; fix it on your branch.]", fault.OwnerTaskID, fault.OwnerTaskID)
		} else {
			note += fmt.Sprintf(" Assigned to Task #%s ([Task #%s](/tasks/%s)) ahead of you in the queue; do not attempt to fix this on your branch.", fault.OwnerTaskID, fault.OwnerTaskID, fault.OwnerTaskID)
		}
	}
	return note + "\n"
}

// faultsSaid is whether any job of these runs is in a fault this report
// will have said something about -- what decides whether faultAdvice is
// worth a paragraph. Asked of the rendered runs rather than of the fault
// list, because a deployment may well be carrying a fault none of the
// runs in front of this reader is part of.
func faultsSaid(faults []CIFault, runs ...ci.Run) bool {
	for _, run := range runs {
		for _, job := range run.Jobs {
			if fault, ok := faultForJob(faults, run, job); ok && fault.Summary(run.Ref) != "" {
				return true
			}
		}
	}
	return false
}

// faultForJob is ci.FaultForJob over this package's own fault type: the
// fault a job belongs to, by run id where the daemon's answer already
// knows this run and by the failure's own digest where it does not.
//
// The second case is the ordinary one for wait_for_checks, whose run
// concluded seconds ago and behind whose faults sits a cache.
func faultForJob(faults []CIFault, run ci.Run, job ci.JobRun) (CIFault, bool) {
	shared := make([]ci.SharedFault, 0, len(faults))
	for _, fault := range faults {
		shared = append(shared, fault.SharedFault)
	}
	found, ok := ci.FaultForJob(shared, run, job)
	if !ok {
		return CIFault{}, false
	}
	for _, fault := range faults {
		if fault.Digest == found.Digest {
			return fault, true
		}
	}
	return CIFault{}, false
}
