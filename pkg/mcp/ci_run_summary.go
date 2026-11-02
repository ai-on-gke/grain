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

// ci_run_summary.go is list_ci_runs' summary view, its default: one line
// per run -- id, ref, verdict, failing jobs -- and "after" paging, so the
// list a judgement agent reads is the shape the notes used to advise
// narrowing it to (design/design/durable-tasks.design.md#DT-13). view "full" is the
// report renderGrainCIRunList has always written.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

const (
	ciRunViewSummary = "summary"
	ciRunViewFull    = "full"
)

// maxSummaryFailingJobs is how many failing jobs a summary line names
// before it counts the rest.
const maxSummaryFailingJobs = 4

// ciRunsAfter narrows f to the runs that come after the run named in the
// list's own order -- a page boundary a caller read off the last page.
// Only the started-time order has an "after" that a time bound can say.
func ciRunsAfter(ctx context.Context, runner GrainCIRunner, scope CIRepoScope, f ci.RunFilter, after string) (ci.RunFilter, error) {
	if f.Sort != "" && f.Sort != ci.SortRunsByStarted {
		return f, fmt.Errorf("after pages only the default order, by start time; drop sort %q or after", f.Sort)
	}
	run, err := runner.GrainCIRun(ctx, after)
	if err != nil {
		return f, fmt.Errorf("reading run %s to page after it: %v", after, err)
	}
	if !scope.permits(run.Repo) {
		return f, scope.refuseRun(run)
	}
	if run.StartedAt == nil {
		return f, fmt.Errorf("run %s has not started, so nothing can be paged after it", after)
	}
	if f.Descending {
		if f.Until.IsZero() || run.StartedAt.Before(f.Until) {
			f.Until = *run.StartedAt
		}
	} else if next := run.StartedAt.Add(time.Nanosecond); next.After(f.Since) {
		f.Since = next
	}
	return f, nil
}

// renderGrainCIRunSummary is the summary view of one page.
func renderGrainCIRunSummary(page ci.RunPage, f ci.RunFilter, scope CIRepoScope) string {
	if len(page.Runs) == 0 {
		return fmt.Sprintf("No grain CI run matches %s (%d remembered in all).",
			scope.describeFilter(f), page.Stored)
	}
	var b strings.Builder
	if f.Narrowed() {
		fmt.Fprintf(&b, "Filter: %s.\n", scope.describeFilter(f))
	}
	fmt.Fprintf(&b, "%d of %d matching runs (%s):\n", len(page.Runs), page.Matched, ciRunOrderPhrase(f))
	for _, run := range page.Runs {
		b.WriteString(ciRunSummaryLine(ciListedRun(run, f)))
		b.WriteByte('\n')
	}
	if dropped := page.Matched - len(page.Runs); dropped > 0 {
		fmt.Fprintf(&b, "%d more: read on with after %q, or view \"full\" for one run's jobs.",
			dropped, page.Runs[len(page.Runs)-1].ID)
	} else {
		b.WriteString("That is every matching run; view \"full\" gives each one's jobs and errors.")
	}
	return b.String()
}

// ciRunSummaryLine is one run in a line: id, repo and ref, verdict, when
// it started, and the jobs that failed.
func ciRunSummaryLine(run ci.Run) string {
	verdict := string(run.Conclusion)
	if verdict == "" {
		verdict = "running"
	}
	line := fmt.Sprintf("%s %s %s %s", run.ID, run.Repo, run.Ref, verdict)
	if run.StartedAt != nil {
		line += " " + run.StartedAt.UTC().Format(time.RFC3339)
	}
	var failing []string
	for _, j := range run.Jobs {
		if j.Conclusion == ci.ConclusionFailure {
			name := j.Name
			if name == "" {
				name = j.JobID
			}
			failing = append(failing, name)
		}
	}
	if len(failing) > 0 {
		shown := failing
		if len(shown) > maxSummaryFailingJobs {
			shown = shown[:maxSummaryFailingJobs]
		}
		line += " failing: " + strings.Join(shown, ", ")
		if more := len(failing) - len(shown); more > 0 {
			line += fmt.Sprintf(" (+%d more)", more)
		}
	}
	return line
}
