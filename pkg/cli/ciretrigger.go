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

package cli

import (
	"context"
	"flag"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	cicli "github.com/ai-on-gke/grain/pkg/ci/cli"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// cmdCIRetrigger is `grain ci retrigger <run-id>`: run the jobs of a run
// that failed again, once, with the job cache bypassed -- the in-sandbox
// `grain ci retrigger` for a run named by id rather than a task's own
// branch. -jobs names the jobs instead of taking the failed ones. A
// GitHub Actions run (gha:...) is re-run on GitHub as its next attempt,
// through POST /api/ci/runs/{id}/rerun.
func cmdCIRetrigger(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain ci retrigger", flag.ContinueOnError)
	jobsFlag := fs.String("jobs", "",
		"rerun these jobs, by id, comma-separated, rather than the ones that failed")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := atMostArgs(fs, pos, 1); err != nil {
		return err
	}
	id := strings.TrimSpace(first(pos))
	if id == "" {
		return usageErrorf("a run id is required: %q (\"grain ci list\" prints them)", "grain ci retrigger <run-id>")
	}
	if ui.IsActionsRunID(id) {
		res, err := c.RerunCIRun(ctx, id, ui.ActionsRerunRequest{Jobs: cicli.SplitJobs(*jobsFlag)})
		if err != nil {
			return err
		}
		out.actionsRerun(res)
		return nil
	}
	run, err := c.CIRun(ctx, id)
	if err != nil {
		return err
	}
	if ci.ProviderOf(run) != ci.ProviderGrain || run.Actions != nil {
		return usageErrorf("run %s is a %s run: grain ci retrigger reruns grain CI's own jobs only", id, ci.ProviderOf(run))
	}
	jobs := cicli.SplitJobs(*jobsFlag)
	if len(jobs) == 0 {
		jobs = cicli.FailedJobIDs(run)
	}
	if len(jobs) == 0 {
		return usageErrorf("run %s has no failed jobs to rerun: name the ones to run with -jobs", id)
	}
	req := ui.CIRunRequest{
		Repo: run.Repo, Ref: run.Ref, Commit: run.Commit,
		Jobs: jobs, Recheck: true,
	}
	// The run's own event, so that the jobs planned are the ones that
	// ran -- but never a manual dispatch's, which would hand the rerun
	// release secrets the run it repeats may not have had.
	if run.Event == ci.EventPush || run.Event == ci.EventPullRequest {
		req.Event = string(run.Event)
	}
	next, err := c.StartCIRun(ctx, req)
	if err != nil {
		return err
	}
	out.ciRetriggered(run, next, jobs)
	return nil
}
