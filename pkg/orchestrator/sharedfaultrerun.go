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

package orchestrator

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// # A red grain called somebody else's is re-run once the base is green
//
// annotateGitHubSharedFaults tells a branch "one fault, not this
// branch's" when the job failing on it is failing the same way on other
// branches. Until this file nothing acted on that: once the pull
// request's repairs were spent the queue blocked it, and when the fault
// was fixed on the base nobody re-ran the branch's checks. Task 4525 sat
// red for seventeen hours on a `ui e2e` fault that had been green on main
// for six of them, until a person pressed Retry merge.
//
// So when every check still failing on a pull request's head is one grain
// attributed to a shared fault at that very commit (the marker the
// annotation leaves, sharedFaultMarker), and every one of those checks has
// since concluded green on the base branch, the head's failed jobs are
// re-run. Once per green base commit (sharedFaultRerunMarker): if the
// re-run is red again, the base moving to a new green commit is what buys
// another, and nothing else does.
//
// Nothing about the verdict changes: the re-run is just CI, and a blocked
// change stays blocked until it reads clean (and then merges, as any
// blocked change does).

// sharedFaultMarkerPrefix opens the hidden line annotateGitHubSharedFaults
// leaves on a task for commit.
func sharedFaultMarkerPrefix(commit string) string {
	return "<!-- grain:shared-fault " + strings.TrimSpace(commit) + " "
}

// sharedFaultMarker is that line: the commit and the jobs (JobKey
// spelling, "|"-separated) grain attributed to a shared fault there.
func sharedFaultMarker(commit string, jobs []string) string {
	return sharedFaultMarkerPrefix(commit) + strings.Join(jobs, "|") + " -->"
}

// sharedFaultJobs is every job of run that is part of one of faults.
func sharedFaultJobs(run ci.Run, faults []ci.SharedFault) []string {
	var out []string
	for _, job := range run.Jobs {
		if _, ok := ci.FaultForJob(faults, run, job); ok && !slices.Contains(out, job.JobID) {
			out = append(out, job.JobID)
		}
	}
	return out
}

// sharedFaultRerunMarker is the line a re-run leaves, which is what makes
// it once per head and green base commit.
func sharedFaultRerunMarker(head, baseCommit string) string {
	return fmt.Sprintf("<!-- grain:shared-fault-rerun %s %s -->", head, baseCommit)
}

// sharedFaultAnnotator is who annotateGitHubSharedFaults comments as.
// Only its comments carry a marker attributedSharedFault believes: the
// marker buys a re-run of a red head, so one an agent relayed into its
// own task's conversation, or a person typed, must not.
var sharedFaultAnnotator = model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}

// attributedSharedFault is what the task's comments say grain attributed
// to a shared fault at head: the jobs, and when it said so. ok is false
// when it never did.
func attributedSharedFault(comments []model.Comment, head string) (jobs []string, at time.Time, ok bool) {
	for _, c := range comments {
		if a := c.Author.Actor; a.Kind != sharedFaultAnnotator.Kind || a.ID != sharedFaultAnnotator.ID || c.Author.OnBehalfOf != nil {
			continue
		}
		body := c.Body
		for {
			i := strings.Index(body, "<!-- grain:shared-fault ")
			if i < 0 {
				break
			}
			body = body[i+len("<!-- grain:shared-fault "):]
			end := strings.Index(body, " -->")
			if end < 0 {
				break
			}
			line := body[:end]
			body = body[end:]
			commit, list, _ := strings.Cut(line, " ")
			if commit == "" || !sameCommit(commit, head) {
				continue
			}
			for _, j := range strings.Split(list, "|") {
				if j = strings.TrimSpace(j); j != "" && !slices.Contains(jobs, j) {
					jobs = append(jobs, j)
				}
			}
			if c.CreatedAt.After(at) {
				at = c.CreatedAt
			}
			ok = true
		}
	}
	return jobs, at, ok && len(jobs) > 0
}

// sameCommit compares two shas, either of which may be abbreviated.
func sameCommit(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// jobNamed reports whether a check called name is the job key names:
// "test" is "CI / test", since a polled check run carries no workflow.
func jobNamed(key, name string) bool {
	return key == name || strings.HasSuffix(key, " / "+name)
}

// rerunSharedFaultRed re-runs the failed jobs of e's head when the whole
// of its red is a shared fault grain attributed to it and the base has
// since gone green on those checks (see the top of this file). It reports
// whether it re-ran anything, in which case the caller neither repairs
// nor escalates this cycle.
func rerunSharedFaultRed(ctx context.Context, store *model.Store, client github.Client, e queueEntry,
	now time.Time) (bool, error) {

	if e.health != model.PrFailing || e.grain.Failed || e.detail.HeadSHA == "" || e.detail.BaseRef == "" {
		return false, nil
	}
	failing := failingCheckNames(e)
	if len(failing) == 0 {
		return false, nil
	}
	rerunner, ok := client.(github.WorkflowRunRerunner)
	if !ok {
		return false, nil
	}
	comments, err := store.Comments(ctx, e.task.ID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's comments: %w", e.task.ID, err)
	}
	jobs, attributedAt, ok := attributedSharedFault(comments, e.detail.HeadSHA)
	if !ok {
		return false, nil
	}
	keyOf := map[string]string{}
	for _, name := range failing {
		i := slices.IndexFunc(jobs, func(k string) bool { return jobNamed(k, name) })
		if i < 0 {
			// Something red that grain did not call shared: the branch's
			// own, and not this file's to re-run.
			return false, nil
		}
		keyOf[name] = jobs[i]
	}
	baseCommit, green, err := sharedFaultGreenOnBase(ctx, store, e.ref.Repo, e.detail.BaseRef, failing, attributedAt)
	if err != nil || !green {
		return false, err
	}
	marker := sharedFaultRerunMarker(e.detail.HeadSHA, baseCommit)
	if slices.ContainsFunc(comments, func(c model.Comment) bool { return strings.Contains(c.Body, marker) }) {
		return false, nil
	}
	ev := readBuildEvidence(client, e.ref, e.detail.HeadSHA)
	runs := infraRerunRuns(ev.logs)
	if len(runs) == 0 {
		return false, nil
	}
	for _, r := range runs {
		if err := rerunner.RerunFailedJobs(e.ref.Repo.StoredOwner(), e.ref.Repo.Name, r.id); err != nil {
			log.Printf("orchestrator: re-running run %d of %s after its shared fault cleared on %s: %v",
				r.id, e.ref, e.detail.BaseRef, err)
			return false, nil
		}
	}
	names := make([]string, 0, len(failing))
	for _, name := range failing {
		names = append(names, "`"+name+"`")
	}
	body := fmt.Sprintf("Re-ran the failed checks of %s at `%s`: everything still red on it (%s) was the "+
		"shared fault grain flagged on this commit (%s), and those checks have since passed on `%s` at "+
		"`%s`. This happens once per green `%s` commit; no repair is counted.\n\n%s",
		e.ref, shortSHA(e.detail.HeadSHA), strings.Join(names, ", "), strings.Join(sharedKeys(keyOf, failing), ", "),
		e.detail.BaseRef, shortSHA(baseCommit), e.detail.BaseRef, marker)
	log.Printf("orchestrator: %s at %s: shared fault cleared on %s at %s, re-ran its failed jobs",
		e.ref, shortSHA(e.detail.HeadSHA), e.detail.BaseRef, shortSHA(baseCommit))
	if err := queueComment(ctx, store, e.task.ID, body, now); err != nil {
		return true, err
	}
	return true, nil
}

func sharedKeys(keyOf map[string]string, order []string) []string {
	var out []string
	for _, name := range order {
		if k := keyOf[name]; k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// sharedFaultGreenOnBase reports whether every check in names has, as its
// latest recorded conclusion on base, a success that finished after since
// -- and the newest base commit those greens are on.
func sharedFaultGreenOnBase(ctx context.Context, store *model.Store, repo model.RepoRef, base string,
	names []string, since time.Time) (string, bool, error) {

	rows, err := store.ListGitHubCIJobs(ctx, model.GitHubCIFilter{
		Repo: repo.String(), Refs: []string{base}, Since: since,
	})
	if err != nil {
		return "", false, err
	}
	commit, newest := "", time.Time{}
	for _, name := range names {
		var latest *model.GitHubCIJob
		for i := range rows {
			r := rows[i]
			if !jobNamed(r.JobKey(), name) && r.JobName != name {
				continue
			}
			if latest == nil || githubRowTime(r).After(githubRowTime(*latest)) {
				latest = &rows[i]
			}
		}
		if latest == nil || !strings.EqualFold(latest.Conclusion, "success") || !githubRowTime(*latest).After(since) {
			return "", false, nil
		}
		if t := githubRowTime(*latest); t.After(newest) {
			commit, newest = latest.Commit, t
		}
	}
	return commit, commit != "", nil
}

func githubRowTime(j model.GitHubCIJob) time.Time {
	if j.CompletedAt != nil {
		return *j.CompletedAt
	}
	return j.UpdatedAt
}
