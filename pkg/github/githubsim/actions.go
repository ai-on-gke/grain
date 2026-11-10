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

package githubsim

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
)

// WorkflowDispatch is one POST .../actions/workflows/{workflow}/dispatches
// the sim accepted: the workflow file, the ref it runs on and its inputs.
type WorkflowDispatch struct {
	Workflow string            `json:"workflow"`
	Ref      string            `json:"ref"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	// RunID is the queued run the dispatch started.
	RunID int64 `json:"runId,omitempty"`
}

// freezeRunIDs gives every seeded run without an ID the one it has been
// served under (runIDBase+500+index), so a run put in front of them --
// GitHub lists newest first -- does not renumber them.
func (s *Sim) freezeRunIDs() {
	for i := range s.WorkflowRuns {
		if s.WorkflowRuns[i].ID <= 0 {
			s.WorkflowRuns[i].ID = int64(runIDBase + 500 + i)
		}
	}
}

// dispatchWorkflow answers a workflow_dispatch: 404 for a workflow the
// repository has never run, 422 for a ref it does not have, else 204 and
// a queued run of the workflow on ref, newest first.
func (s *Sim) dispatchWorkflow(owner, repo, workflow string, body []byte) (github.ApiResponse, error) {
	var payload struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return jsonResponse(400, map[string]string{"message": "Problems parsing JSON"}), nil
	}
	if !s.hasWorkflowFile(workflow) {
		return jsonResponse(404, map[string]string{"message": "Not Found"}), nil
	}
	ref := strings.TrimPrefix(strings.TrimPrefix(payload.Ref, "refs/heads/"), "refs/tags/")
	sha := ""
	for _, candidate := range []string{"refs/heads/" + ref, "refs/tags/" + ref} {
		if got := s.revSHA(candidate); got != "" {
			sha = got
			break
		}
	}
	if sha == "" {
		return jsonResponse(422, map[string]string{"message": "No ref found for: " + payload.Ref}), nil
	}
	// GitHub serves every run with its workflow's path, which is what a
	// per-workflow listing (.../workflows/{workflow}/runs) matches on; a
	// workflow that has never run before is still the file it was
	// dispatched as.
	name, wfPath := strings.TrimSuffix(workflow, path.Ext(workflow)), ".github/workflows/"+workflow
	for _, wr := range s.WorkflowRuns {
		if workflowFileMatches(wr.Path, wr.ID, workflow) {
			if wr.Name != "" {
				name = wr.Name
			}
			wfPath = wr.Path
			break
		}
	}
	s.freezeRunIDs()
	id := int64(runIDBase + 700)
	for _, wr := range s.WorkflowRuns {
		id = max(id, wr.ID+1)
	}
	run := WorkflowRun{
		ID: id, Name: name, Path: wfPath, HeadBranch: ref, HeadSHA: sha,
		Event: "workflow_dispatch", Status: "queued", RunAttempt: 1, CreatedAt: s.now().UTC(),
	}
	s.WorkflowRuns = append([]WorkflowRun{run}, s.WorkflowRuns...)
	s.Dispatches = append(s.Dispatches, WorkflowDispatch{Workflow: workflow, Ref: payload.Ref, Inputs: payload.Inputs, RunID: id})
	return github.ApiResponse{Status: 204}, nil
}

// cancelRun answers POST .../actions/runs/{id}/cancel: 202 and the run
// concluded cancelled, 409 for a run already completed, 404 for none.
func (s *Sim) cancelRun(id int64) (github.ApiResponse, error) {
	s.freezeRunIDs()
	for i := range s.WorkflowRuns {
		wr := &s.WorkflowRuns[i]
		if wr.ID != id {
			continue
		}
		if wr.Status == "" || wr.Status == "completed" {
			return jsonResponse(409, map[string]string{"message": "Cannot cancel a workflow run that is completed."}), nil
		}
		wr.Status, wr.Conclusion = "completed", "cancelled"
		s.CancelledRuns = append(s.CancelledRuns, id)
		return jsonResponse(202, map[string]any{}), nil
	}
	return jsonResponse(404, map[string]string{"message": "Not Found"}), nil
}

// ErrWorkflowRunNotFound and ErrWorkflowRunCompleted are what
// CompleteWorkflowRun answers for a run the sim does not have and for one
// that has already finished.
var (
	ErrWorkflowRunNotFound  = errors.New("no such workflow run")
	ErrWorkflowRunCompleted = errors.New("the workflow run has already completed")
)

// CompleteWorkflowRun finishes run id -- one a workflow_dispatch queued, or
// any other still queued or in progress -- with conclusion, as GitHub
// does when its last job ends: status completed, updated_at now, and jobs
// as its jobs (each job's empty Conclusion read as conclusion's). It keeps
// everything else the run was queued with, so it stays the dispatch's run:
// its ID, workflow, ref, commit and event. With no jobs, the run gets one
// job named after the workflow concluding conclusion, with no log.
func (s *Sim) CompleteWorkflowRun(id int64, conclusion string, jobs []WorkflowJob) (WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freezeRunIDs()
	for i := range s.WorkflowRuns {
		wr := &s.WorkflowRuns[i]
		if wr.ID != id {
			continue
		}
		if wr.Status == "" || wr.Status == "completed" {
			return *wr, ErrWorkflowRunCompleted
		}
		if conclusion == "" {
			conclusion = "success"
		}
		if len(jobs) == 0 {
			jobs = []WorkflowJob{{Name: wr.Name}}
		}
		done := make([]WorkflowJob, len(jobs))
		for j, job := range jobs {
			if job.Conclusion == "" {
				job.Conclusion = conclusion
			}
			done[j] = job
		}
		wr.Status, wr.Conclusion, wr.Jobs = "completed", conclusion, done
		wr.UpdatedAt = s.now().UTC()
		return *wr, nil
	}
	return WorkflowRun{}, ErrWorkflowRunNotFound
}

// latestAttemptJobs is wr's jobs from its latest attempt.
func latestAttemptJobs(wr WorkflowRun) []WorkflowJob {
	latest := max(wr.RunAttempt, 1)
	var out []WorkflowJob
	for _, job := range wr.Jobs {
		if job.RunAttempt == 0 || job.RunAttempt == latest {
			out = append(out, job)
		}
	}
	return out
}

// runJobsJSON is GET .../actions/runs/{id}/jobs for a seeded run, in
// GitHub's shape: each job with its run_id, run_attempt, head_sha,
// workflow_name and status, the latest attempt's jobs or, with
// ?filter=all, every attempt's. A job's id stays its index in wr.Jobs, so
// an id served here still finds its log.
func (s *Sim) runJobsJSON(owner, repo string, wr WorkflowRun, all bool) map[string]any {
	latest := max(wr.RunAttempt, 1)
	headSHA := wr.HeadSHA
	if headSHA == "" && wr.HeadBranch != "" {
		headSHA = s.branchSHA(wr.HeadBranch)
	}
	name := wr.Name
	if name == "" {
		name = "tests"
	}
	full := jobsJSON(owner, repo, int(wr.ID), wr.Jobs)["jobs"].([]map[string]any)
	items := []map[string]any{}
	for i, job := range wr.Jobs {
		attempt := job.RunAttempt
		if attempt == 0 {
			attempt = latest
		}
		if !all && attempt != latest {
			continue
		}
		item := full[i]
		item["run_id"] = wr.ID
		item["run_attempt"] = attempt
		item["head_sha"] = headSHA
		item["workflow_name"] = name
		item["run_url"] = fmt.Sprintf("https://api.github.example/repos/%s/%s/actions/runs/%d", owner, repo, wr.ID)
		if job.Status != "" && job.Status != "completed" {
			item["status"] = job.Status
			item["conclusion"] = nil
		}
		items = append(items, item)
	}
	return map[string]any{"total_count": len(items), "jobs": items}
}

// tagsJSON answers GET .../tags from the bare repository: each tag with
// the commit it points at (peeled through an annotated tag), newest
// version first, paged by ?per_page= (default 30, at most 100) and
// ?page= with a Link header naming the next page, as GitHub pages it.
func (s *Sim) tagsJSON(reqPath string, qs url.Values) (github.ApiResponse, error) {
	all := []map[string]any{}
	if s.BareRepo != "" {
		out, err := s.gitOutput(nil, nil, "for-each-ref", "--sort=-version:refname",
			"--format=%(refname:short) %(objectname) %(*objectname)", "refs/tags")
		if err != nil {
			return github.ApiResponse{}, err
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			sha := f[1]
			if len(f) > 2 {
				sha = f[2]
			}
			all = append(all, map[string]any{
				"name":   f[0],
				"commit": map[string]any{"sha": sha, "url": fmt.Sprintf("https://api.github.example/repos/%s/%s/commits/%s", s.Owner, s.Repo, sha)},
			})
		}
	}
	perPage, page := 30, 1
	if n, err := strconv.Atoi(qs.Get("per_page")); err == nil && n > 0 {
		perPage = min(n, 100)
	}
	if n, err := strconv.Atoi(qs.Get("page")); err == nil && n > 0 {
		page = n
	}
	start, end := min((page-1)*perPage, len(all)), min(page*perPage, len(all))
	resp := jsonResponse(200, all[start:end])
	if end < len(all) {
		next := url.Values{}
		for k, v := range qs {
			next[k] = v
		}
		next.Set("page", strconv.Itoa(page+1))
		next.Set("per_page", strconv.Itoa(perPage))
		resp.Headers = map[string]string{"Link": fmt.Sprintf(`<https://api.github.example%s?%s>; rel="next"`, reqPath, next.Encode())}
	}
	return resp, nil
}
