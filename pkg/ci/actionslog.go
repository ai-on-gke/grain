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

package ci

import (
	"fmt"
	"strings"
	"time"
)

// ActionsStepLog is the part of a GitHub Actions job log that step i of
// steps wrote.
//
// The forge keeps one log per job, and nothing in it names a step: a
// step's "##[group]" header is its command line ("Run make images"),
// not its name, and the job's set-up and clean-up write groups of their
// own. What does tie a line to a step is time -- every line starts with
// the runner's RFC 3339 timestamp, and the jobs API gives each step its
// started_at and completed_at (to the second). So the step is the lines
// stamped from its start up to the next step's start (or its own end,
// for the last one or one whose successor never ran). Lines in the
// second two steps share can land in both; a line with no timestamp of
// its own (a continuation) goes with the line before it.
//
// A step that never ran, a log with no timestamps, or a window holding
// no lines is an error saying so, never an empty answer.
func ActionsStepLog(log string, steps []StepRun, i int) (string, error) {
	if i < 0 || i >= len(steps) {
		return "", fmt.Errorf("no step %d in this job", i+1)
	}
	step := steps[i]
	if step.StartedAt == nil {
		return "", fmt.Errorf("step %q never started (%s): it has no lines in the job log", step.Name, conclusionOrPending(step.Conclusion))
	}
	from := step.StartedAt.Truncate(time.Second)
	var until time.Time
	for _, next := range steps[i+1:] {
		if next.StartedAt != nil {
			until = next.StartedAt.Truncate(time.Second).Add(time.Second)
			break
		}
	}
	if step.FinishedAt != nil {
		if end := step.FinishedAt.Truncate(time.Second).Add(time.Second); until.IsZero() || end.Before(until) {
			until = end
		}
	}
	var b strings.Builder
	stamped, in := false, false
	for _, line := range strings.SplitAfter(log, "\n") {
		if line == "" {
			continue
		}
		if at, ok := actionsLineTime(line); ok {
			stamped = true
			in = !at.Before(from) && (until.IsZero() || at.Before(until))
		}
		if in {
			b.WriteString(line)
		}
	}
	if !stamped {
		return "", fmt.Errorf("the job log carries no timestamps to find step %q by: read the whole job.log", step.Name)
	}
	if b.Len() == 0 {
		window := from.Format(time.RFC3339) + " onwards"
		if !until.IsZero() {
			window = from.Format(time.RFC3339) + " to " + until.Format(time.RFC3339)
		}
		return "", fmt.Errorf("the job log has no lines from step %q's run (%s): read the whole job.log", step.Name, window)
	}
	return b.String(), nil
}

// actionsLineTime is the timestamp a runner log line starts with.
func actionsLineTime(line string) (time.Time, bool) {
	line = strings.TrimPrefix(line, "\ufeff")
	head, _, _ := strings.Cut(line, " ")
	if len(head) < len("2006-01-02T15:04:05Z") || head[4] != '-' || head[10] != 'T' {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimRight(head, "\r\n"))
	return at, err == nil
}

func conclusionOrPending(c Conclusion) string {
	if c == "" {
		return "pending"
	}
	return string(c)
}
