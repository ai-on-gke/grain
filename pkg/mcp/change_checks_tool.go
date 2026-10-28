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
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
)

// The checks tools of a run whose task's change is a CL rather than a pull
// request (changelists.md §3, G3-3). pull_request_status and
// wait_for_checks answer from the change's own CI (presubmit, for a CL)
// through the registered change.Checks; grain CI's own tools --
// retrigger_ci, run_ci_job, get_ci_log -- have nothing to act on and
// refuse in the provider's words. Registered in place of the pull-request
// and grain CI tools of the same names, never beside them.

// ChangeChecksReader is how these tools reach the task's change.Checks:
// the daemon, over its REST API (pkg/daemon's DaemonChangeChecks), which
// reads the task and its primary change itself. Which task is fixed at
// process start, never by a tool call.
type ChangeChecksReader interface {
	ChangeChecksStatus(ctx context.Context) (change.ChecksReport, error)
	WaitChangeChecks(ctx context.Context, req change.ChecksWait) (change.ChecksReport, error)
	ChangeCIRefusal(ctx context.Context, tool string) (string, error)
}

// maxChangeWaitSlice bounds one hop of a changelist wait, so a long wait
// is several requests each well inside the daemon client's response
// timeout, and the blocked time is credited between them.
var maxChangeWaitSlice = 5 * time.Minute

// The words a changelist wait's report is read back by
// (telemetry.go's ReadCheckWait): the provider, then the verdict.
const changeWaitVerdictPrefix = "Verdict from "

// NewChangeChecksTools is the five checks tools of a changelist task.
func NewChangeChecksTools(reader ChangeChecksReader) []Tool {
	tools := []Tool{changeStatusTool(reader), changeWaitTool(reader)}
	for _, name := range []string{"retrigger_ci", "run_ci_job", "get_ci_log"} {
		tools = append(tools, changeRefusalTool(reader, name))
	}
	return tools
}

func changeStatusTool(reader ChangeChecksReader) Tool {
	return Tool{
		Name: "pull_request_status",
		Description: "Show where this task's change (its CL) stands: its state and " +
			"snapshot, its presubmit and every check on the current snapshot with the " +
			"failing ones named and their log tails quoted, its review, its health, and " +
			"whether it is submittable and why not. This only reads; it uploads " +
			"nothing and changes nothing.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		},
		ElisionAdvice: elisionAdviceCIReport,
		Handler: func(ctx context.Context, _ map[string]any) Result {
			report, err := reader.ChangeChecksStatus(ctx)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			text := strings.TrimRight(report.Text, "\n")
			if report.Health != "" && !strings.Contains(text, "Health: ") {
				text += fmt.Sprintf("\nHealth: %s.", report.Health)
			}
			return Result{Text: text}
		},
	}
}

func changeWaitTool(reader ChangeChecksReader) Tool {
	return Tool{
		Name: "wait_for_checks",
		Description: "Wait for presubmit on this task's CL and report its verdict. " +
			"It starts presubmit if none has run on the current snapshot (it never " +
			"re-runs an unchanged one), then waits until every required check has a " +
			"verdict for that snapshot. Upload with open_pull_request first: checks " +
			"are per snapshot. jobs narrows the wait to those TAP targets or projects " +
			"for fast feedback; a narrowed wait never satisfies the task's gate, so " +
			"finish on one without jobs. Time spent blocked here is credited back to " +
			"your run's deadline, up to 45 minutes over the run. timeout_seconds " +
			"defaults to 900 and is capped at 3600.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"timeout_seconds": map[string]any{"type": "number"},
				"jobs": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"minItems":    1,
					"description": "TAP targets or projects to wait for instead of every required check.",
				},
			},
		},
		ElisionAdvice: elisionAdviceCIReport,
		Handler: func(ctx context.Context, args map[string]any) Result {
			jobs := argStringSlice(args, "jobs")
			budget := waitForChecksBudget(ctx, args)
			if budget.tooLate {
				return Result{Text: fmt.Sprintf("There is no time to wait on presubmit: %s is left "+
					"before grain cancels this run. Leave what is unfinished in a comment_on_issue note.",
					humanRemaining(budget.remaining))}
			}
			report, waited, err := waitChangeChecks(ctx, reader, jobs, budget.timeout)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: budget.note + renderChangeWait(report, jobs, waited)}
		},
	}
}

// waitChangeChecks waits up to timeout in slices of maxChangeWaitSlice,
// crediting the time blocked to the run's deadline after each, and stops
// once a slice ends with a verdict, the timeout is spent, or the run's
// deadline (with the credit it can still earn) has no room for more.
func waitChangeChecks(ctx context.Context, reader ChangeChecksReader, jobs []string,
	timeout time.Duration) (change.ChecksReport, time.Duration, error) {

	started := time.Now()
	credit := startWaitCredit(ctx)
	var credited time.Duration
	if deadline, ok := runDeadlineFromContext(ctx); ok {
		credited = deadline.blockedSoFar()
	}
	for {
		left := timeout - time.Since(started)
		slice := min(left, maxChangeWaitSlice)
		asked := time.Now()
		report, err := reader.WaitChangeChecks(ctx, change.ChecksWait{
			Jobs: jobs, Timeout: slice, Credited: credited + time.Since(started),
		})
		waited := time.Since(started)
		remaining, bounded := credit(ctx, waited)
		if err != nil {
			return change.ChecksReport{}, waited, err
		}
		if len(jobs) > 0 {
			report.Selective, report.SatisfiesGate = true, false
		}
		if !report.TimedOut || waited >= timeout || (bounded && remaining <= waitForChecksDeadlineSlack) {
			return report, waited, nil
		}
		// A provider that answered "still running" without blocking is
		// not waited on in a tight loop.
		if time.Since(asked) < slice/2 {
			if err := sleepUntilCancelled(ctx, min(checkPollInterval, timeout-waited)); err != nil {
				return change.ChecksReport{}, waited, err
			}
		}
	}
}

// changeWaitVerdict is the verdict a changelist wait ended in, in the
// words task_run_check_wait stores (WaitVerdict*).
func changeWaitVerdict(r change.ChecksReport) string {
	switch {
	case len(r.Failing) > 0 || r.Presubmit == change.PresubmitFailed:
		return WaitVerdictFailed
	case r.TimedOut || len(r.Waiting) > 0:
		return WaitVerdictTimedOut
	case r.Presubmit == change.PresubmitNotRun:
		return WaitVerdictNoChecks
	}
	return WaitVerdictPassed
}

func renderChangeWait(r change.ChecksReport, jobs []string, waited time.Duration) string {
	provider := r.Provider
	if provider == "" {
		provider = "tap"
	}
	var b strings.Builder
	target := "this task's change"
	if r.Snapshot > 0 {
		target = fmt.Sprintf("snapshot %d of this task's change", r.Snapshot)
	}
	fmt.Fprintf(&b, "%s%s%s %s.\n", waitHeaderPrefix, roundDuration(waited), waitHeaderSuffix, target)
	fmt.Fprintf(&b, "%s%s: %s.\n\n", changeWaitVerdictPrefix, provider, changeWaitVerdict(r))
	if r.Started {
		b.WriteString("No presubmit had run on this snapshot, so this wait started one.\n\n")
	}
	b.WriteString(strings.TrimRight(r.Text, "\n"))
	if r.FaultNote != "" {
		fmt.Fprintf(&b, "\n\n%s", r.FaultNote)
	}
	switch {
	case len(jobs) > 0:
		fmt.Fprintf(&b, "\n\nThis wait was narrowed to %s, so it does not satisfy this task's gate: "+
			"before finishing, call wait_for_checks without jobs.", strings.Join(jobs, ", "))
	case r.TimedOut:
		b.WriteString("\n\nThe wait timed out with checks still running: call wait_for_checks again to keep waiting.")
	}
	return b.String()
}

func changeRefusalTool(reader ChangeChecksReader, name string) Tool {
	return Tool{
		Name: name,
		Description: "Not available on this task: its change is a CL, which grain CI " +
			"does not run. Use wait_for_checks and pull_request_status, which answer " +
			"from the CL's own presubmit.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"properties":           map[string]any{},
		},
		Handler: func(ctx context.Context, _ map[string]any) Result {
			text, err := reader.ChangeCIRefusal(ctx, name)
			if err != nil || strings.TrimSpace(text) == "" {
				text = fmt.Sprintf("%s is grain CI's, and this task's change is a CL that grain CI "+
					"does not run. Use wait_for_checks and pull_request_status instead.", name)
			}
			return Result{Text: text, IsError: true}
		},
	}
}
