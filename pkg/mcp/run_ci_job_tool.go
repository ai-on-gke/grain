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

	"github.com/ai-on-gke/grain/pkg/ci"
)

// NewRunCIJobTools returns the run_ci_job tool for targeted single-job verification.
func NewRunCIJobTools(runner GrainCIRunner, scope CIRepoScope) []Tool {
	return []Tool{
		{
			Name: "run_ci_job",
			Description: fmt.Sprintf(
				"Run a fast, targeted Grain CI check for a single named job (e.g. \"go\", \"lint\") "+
					"against your current branch commit, without invoking unrelated workflow jobs. "+
					"Blocks until the targeted job finishes and returns the immediate pass/fail verdict "+
					"with job log output. Optional timeout_seconds defaults to %d and is capped at %d.",
				int(DefaultWaitForChecksTimeout.Seconds()), int(MaxWaitForChecksTimeout.Seconds())),
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"job"},
				"properties": map[string]any{
					"job": map[string]any{
						"type":        "string",
						"description": "The name or ID of the single CI job to run (e.g. \"go\", \"lint\").",
					},
					"timeout_seconds": map[string]any{
						"type":        "number",
						"description": fmt.Sprintf("Optional timeout in seconds (default %d, max %d).", int(DefaultWaitForChecksTimeout.Seconds()), int(MaxWaitForChecksTimeout.Seconds())),
					},
				},
			},
			ElisionAdvice: elisionAdviceCIRun,
			Handler: func(ctx context.Context, args map[string]any) Result {
				if runner == nil {
					return Result{Text: "Grain CI is unavailable for this run.", IsError: true}
				}
				jobName, ok := argString(args, "job")
				jobName = strings.TrimSpace(jobName)
				if !ok || jobName == "" {
					return Result{Text: "job is required", IsError: true}
				}
				budget := waitForChecksBudget(ctx, args)
				if budget.tooLate {
					return Result{Text: noTimeToWait(budget.remaining, "grain CI")}
				}
				text, err := runSingleCIJob(ctx, runner, jobName, budget.timeout)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				return Result{Text: budget.note + text}
			},
		},
	}
}

func runSingleCIJob(ctx context.Context, runner GrainCIRunner, jobName string, timeout time.Duration) (string, error) {
	run, err := runner.StartGrainCI(ctx, []string{jobName})
	if err != nil {
		return "", fmt.Errorf("starting grain CI: %w", err)
	}
	started := time.Now()
	credit := startWaitCredit(ctx)
	defer func() { _, _ = credit(ctx, time.Since(started)) }()
	deadline := started.Add(timeout)

	report := func(run ci.Run, timedOut bool) string {
		faults := sharedFaults(ctx, runner, run)
		text := renderGrainCIRun(run, time.Since(started), timedOut, faults)
		if faultsSaid(faults, run) {
			text += faultAdvice
		}
		targetJob, err := grainCIJob(run, jobName)
		if err != nil {
			targetJob, _ = grainCIJob(run, "")
		}
		if targetJob != nil {
			logText, _, _ := grainCILog(run, *targetJob, "")
			if strings.TrimSpace(logText) != "" && logText != "(no output recorded)" && logText != "(nothing printed yet)" {
				text += fmt.Sprintf("\n\nJob %q log output:\n%s", targetJob.Name, logText)
			} else if failedJob, err := grainCIJob(run, ""); err == nil && failedJob != nil && failedJob.Name != targetJob.Name {
				failedLog, _, _ := grainCILog(run, *failedJob, "")
				if strings.TrimSpace(failedLog) != "" {
					text += fmt.Sprintf("\n\nJob %q log output:\n%s", failedJob.Name, failedLog)
				}
			} else if strings.TrimSpace(logText) != "" {
				text += fmt.Sprintf("\n\nJob %q log output:\n%s", targetJob.Name, logText)
			}
		}
		return text + selectiveRunReminder([]string{jobName})
	}

	for {
		latest, err := runner.GrainCIRun(ctx, run.ID)
		if err != nil {
			return "", fmt.Errorf("reading grain CI run %s: %w", run.ID, err)
		}
		run = latest
		if run.Conclusion.Done() || hasFailedJob(run) {
			return report(run, false), nil
		}
		if time.Now().After(deadline) {
			return report(run, true), nil
		}
		if left, ok := credit(ctx, time.Since(started)); ok && left <= waitForChecksDeadlineSlack {
			return report(run, true), nil
		}
		nap := checkPollInterval
		if remaining := time.Until(deadline); remaining < nap {
			nap = remaining
		}
		if err := sleepUntilCancelled(ctx, nap); err != nil {
			return "", fmt.Errorf("waiting for grain CI run %s: %w", run.ID, err)
		}
	}
}
