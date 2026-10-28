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

// grain CI, in the three tools that answer "is this pull request in good
// shape?".
//
// pull_request_status, wait_for_checks and base_branch_status all build
// their answers out of GitHub's checks and nothing else, which was the
// whole truth until grain/task-302: a grain CI verdict recorded on a task
// now holds the merge queue exactly as a red GitHub check does -- a
// failure against the commit a pull request would land makes it
// PrFailing and earns it the queue's one automatic repair, and a run
// still going holds it PrPending (orchestrator.withGrainCI). A grain CI
// run is not a GitHub check run, reports no commit status, and reaches
// those tools' check lists by no route at all, so a run whose branch is
// held by one is shown twelve green checks and no failure -- and the run
// dispatched *because* grain CI is red calls the tool it was told to call
// and is told its build is fine.
//
// That gap is what this file closes: one paragraph, appended to those
// reports, saying what grain CI makes of the same commit and what that
// costs the pull request.
//
// # The rule is the merge queue's, restated
//
// Deliberately the same three tests orchestrator.grainCIVerdictOf
// applies, because the one thing worse than a report that says nothing
// about grain CI is a report that disagrees with the gate:
//
//   - only a verdict about the commit being judged can satisfy the gate.
//     A verdict about an older head is about a different tree;
//   - only "failure" is failure. "cancelled" is a supersede or a daemon
//     shutting down and "skipped" is not a successful full run. Neither
//     dispatches a repair, but neither satisfies the gate;
//   - absence is not failure, but it is still pending. No verdict, an
//     unreadable one, or a CI service that is down cannot substitute for
//     the required full-suite success.
//
// # Why it is read here rather than passed in
//
// Because the answer changes while the run works. wait_for_checks starts
// a grain CI run explicitly and it concludes minutes later, so a snapshot
// taken when this process started would be empty for every run that has
// not pushed yet -- which is all of them. GrainCIReader is therefore a
// read at report time, one per answer: cmd/grain implements it as a hop
// to the daemon that holds the task (daemonGrainCI), the same hop
// update_status, chat and the wait credit already make.
//
// # Silence is the default
//
// A nil reader -- a deployment with no daemon to ask, a task that does
// not hold the grain-ci capability, every caller in pkg/agent that builds
// this roster only to read the tool names off it -- leaves all three
// reports exactly as they read before this existed. There is no paragraph
// about a CI system that is not judging this branch.

import (
	"context"
	"fmt"
	"strings"
)

// GrainCIVerdict is what grain's own CI last recorded about the branch
// this run pushed: model.CIStatus's readable half, restated here because
// pkg/mcp does not import pkg/model (its own limits are restated in this
// package for the same reason -- see cmd/grain's daemonMemory, which is
// the other place that owns both vocabularies).
//
// Conclusion carries pkg/ci's own words -- "success", "failure",
// "skipped", "cancelled" -- and is empty for a run still going, which is
// the one distinction the reports below turn on besides Commit.
type GrainCIVerdict struct {
	// RunID is the ci.Run this verdict came from, which GET
	// /api/ci/runs/{id} serves the job-by-job detail from while the run
	// is still remembered.
	RunID string
	// Commit is the full SHA the run was started against -- which tree
	// this verdict is about, and what keeps it from being read as an
	// answer about any other.
	Commit string
	// Conclusion is empty while the run is still going.
	Conclusion string
	// Detail is grain's own one-line account of the verdict: which jobs
	// failed, or why nothing ran.
	Detail string
}

// The two conclusions the reports below have to tell apart by name. The
// rest of pkg/ci's vocabulary is handled as "something that holds
// nothing", which is what it is (see this file's doc comment).
const (
	grainCISuccess   = "success"
	grainCIFailure   = "failure"
	grainCICancelled = "cancelled"
)

// GrainCIReader reads the grain CI verdict recorded on this run's own
// task. Which task that is is fixed where the reader is built, exactly as
// PullRequestScope fixes the branch: a run reads grain CI's answer about
// its own work or about nothing.
//
// A nil *GrainCIVerdict is the ordinary case, not an error -- a task
// whose branch has not been through CI yet, or whose verdict a restart
// forgot -- and is silence here as it is in the merge queue.
type GrainCIReader interface {
	GrainCI(ctx context.Context) (*GrainCIVerdict, error)
}

// grainCIReport is the paragraph pull_request_status and wait_for_checks
// hang off the end of their check lists, already separated from whatever
// came before it, or "" where there is nothing to say.
//
// headSHA is the commit the report is about -- the same commit the checks
// above it were read for -- and is what the verdict is matched against.
func grainCIReport(ctx context.Context, reader GrainCIReader, headSHA, branch string) string {
	body := grainCIParagraph(ctx, reader, headSHA, branch)
	if body == "" {
		return ""
	}
	return "\n\n" + body
}

// grainCIParagraph is the sentence itself, one case per state the verdict
// can be in.
func grainCIParagraph(ctx context.Context, reader GrainCIReader, headSHA, branch string) string {
	if reader == nil {
		return ""
	}
	verdict, err := reader.GrainCI(ctx)
	if err != nil {
		// Said rather than swallowed. Absence is never failure, so this
		// holds nothing and the run is not told to act on it -- but a
		// run that has been given grain CI and hears nothing about it
		// would reasonably read the green checks above as the whole
		// verdict, and this is the one state where they are not.
		return fmt.Sprintf(
			"grain's own CI (.grain/workflows) judges this branch too, and its verdict could "+
				"not be read: %v -- grain's plumbing, not your change. Call this again "+
				"before you finish: a red grain CI run keeps this pull request out of the "+
				"merge queue however green the checks above are.", err)
	}
	if verdict == nil {
		return "grain's own CI (.grain/workflows) judges this branch too and has no " +
			"full-suite verdict about it yet. Call wait_for_checks without a jobs " +
			"selection: this task stays pending until a complete run succeeds against " +
			"the exact commit."
	}
	if verdict.Commit != headSHA {
		return fmt.Sprintf(
			"grain's own CI (.grain/workflows) has no verdict about %s yet -- its last is "+
				"about %s (%s), which is not a pass for this commit. The task stays pending "+
				"until a complete run succeeds against the new head.",
			shortSHA(headSHA), shortSHA(verdict.Commit), grainCIConclusionWord(verdict.Conclusion))
	}
	switch verdict.Conclusion {
	case "":
		return fmt.Sprintf(
			"grain's own CI (.grain/workflows) is still running against %s. It is not one of "+
				"the checks above, and the merge queue holds this pull request pending until "+
				"it reports, so a green build above is not yet a mergeable branch.%s",
			shortSHA(headSHA), grainCIRunPointer(verdict))
	case grainCIFailure:
		return fmt.Sprintf(
			"grain's own CI (.grain/workflows) FAILED against %s.%s It is not one of the "+
				"checks above, and the queue counts this pull request as failing exactly as "+
				"for a red GitHub check. Fix what it found, commit, and `git push origin %s` "+
				"-- grain runs it again against the new head. base_branch_status cannot "+
				"settle it: grain CI never runs against the base.%s",
			shortSHA(headSHA), grainCIDetailClause(verdict), branch, grainCIRunPointer(verdict))
	case grainCISuccess:
		return fmt.Sprintf(
			"grain's own CI (.grain/workflows) passed against %s, so it holds nothing.%s",
			shortSHA(headSHA), grainCIDetailClause(verdict))
	case grainCICancelled:
		// The one conclusion something else is going to act on. grain
		// replaces a cancellation -- a job past its timeout, a daemon that
		// stopped, a supersede, or a person stopping a hung or duplicate
		// run -- with another run for the same commit a few minutes later,
		// up to a small number of attempts (orchestrator's
		// ciReplacementFor), so the useful advice here is to wait rather
		// than to push.
		return fmt.Sprintf(
			"grain's own CI (.grain/workflows) was cancelled against %s.%s Not a failure to "+
				"repair, but not the required full-suite success either: the task stays "+
				"pending until a complete run passes this commit. grain replaces a cancelled "+
				"run -- whoever cancelled it -- with another full-suite run for the same "+
				"commit a few minutes later, so wait_for_checks again is usually the whole "+
				"answer. If the reason they gave asks you to do something else (wait out a "+
				"hold, a broken base, an objection to the change), act on it and say so.",
			shortSHA(headSHA), grainCIDetailClause(verdict))
	default:
		// skipped, and anything pkg/ci grows later. Neither a failure to
		// repair nor the required successful verdict.
		return fmt.Sprintf(
			"grain's own CI (.grain/workflows) ended %s against %s.%s Not a failure to "+
				"repair, but not the required full-suite success either: the task stays "+
				"pending until a complete run passes this commit.",
			verdict.Conclusion, shortSHA(headSHA), grainCIDetailClause(verdict))
	}
}

// grainCIConclusionWord renders a conclusion for the one report that
// quotes a verdict about some *other* commit, where "" is a run still
// going rather than an empty field.
func grainCIConclusionWord(conclusion string) string {
	if conclusion == "" {
		return "still running"
	}
	return conclusion
}

// grainCIDetailClause is grain's own account of the verdict, as a
// sentence, or nothing at all where the record carries none -- which is
// every verdict written by a daemon that could not reach its CI engine
// for long enough to be told why.
func grainCIDetailClause(verdict *GrainCIVerdict) string {
	detail := strings.TrimSpace(verdict.Detail)
	if detail == "" {
		return ""
	}
	return " " + strings.TrimRight(detail, ".") + "."
}

// grainCIRunPointer names the run id, for the two states where a run
// would want the job-by-job detail: one still going, and one that failed.
// It is a fact about where the detail is rather than a URL, because what
// serves it is this deployment's own API and nothing in a sandbox can
// reach it -- what the id is actually good for is naming the run to a
// person, in the note a run leaves behind.
func grainCIRunPointer(verdict *GrainCIVerdict) string {
	if verdict.RunID == "" {
		return ""
	}
	return fmt.Sprintf(" grain CI run %s; its job-by-job detail is on this task in grain's UI.",
		verdict.RunID)
}

// grainCIBaseCaveat is what base_branch_status says instead, and it says
// it whatever the verdict is -- because what it has to say is about the
// comparison rather than about any one run.
//
// The other two tools report the run's own branch, where a verdict is an
// answer. This one exists to tell an inherited failure from one the
// change caused, and grain CI cannot be told apart that way at all: it is
// run against the branch a task pushed, never against the base
// (orchestrator.reconcileCI walks tasks with an open pull request), so
// there is no counterpart here to set beside it. A run that had only this
// tool's answer and a red grain CI verdict could reasonably conclude the
// base is what is broken; it is not, and this is the sentence that says
// so.
func grainCIBaseCaveat(base, branch string) string {
	return fmt.Sprintf(
		"\n\nThis comparison covers GitHub's checks only: grain's own CI (.grain/workflows) "+
			"runs against task branches, never against %s, so there is nothing here to set "+
			"beside the verdict on %s. A red grain CI run is yours to fix whatever the "+
			"checks above say; pull_request_status reports it.",
		base, branch)
}
