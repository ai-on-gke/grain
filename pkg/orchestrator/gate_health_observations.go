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
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Same disabled-by-default selection as the ordinary task projection. These
// helpers never call legacy writers or return failures into legacy collectors.
func healthObservationEnabled() bool {
	ids := strings.Split(strings.TrimSpace(os.Getenv("GRAIN_GATE_OBSERVATION_TASKS")), ",")
	if len(ids) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || len(id) > 128 || seen[id] {
			return false
		}
		seen[id] = true
		for i, c := range []byte(id) {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || c == '_') {
				continue
			}
			return false
		}
	}
	return true
}

// Collector interests are scoped to the existing health goals in the explicit
// cohort. Selecting a report task must not start unrelated health source reads.
func healthSourceWanted(ctx context.Context, deps Deps, repo model.RepoRef, ref, workflow string) bool {
	if !healthObservationEnabled() || deps.Store == nil {
		return false
	}
	readCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	for _, raw := range strings.Split(os.Getenv("GRAIN_GATE_OBSERVATION_TASKS"), ",") {
		g, err := deps.Store.TaskGoal(readCtx, strings.TrimSpace(raw))
		if err != nil {
			return false
		}
		if g == nil {
			continue
		}
		switch g.Kind {
		case model.GoalBaseGreen:
			if workflow == "" && model.SameRepoText(g.Spec.Repo, repo.String()) && g.Spec.Branch == ref {
				return true
			}
		case model.GoalNightlyGreen:
			if workflow != "" && model.SameRepoText(g.Spec.Repo, repo.String()) && strings.EqualFold(g.Spec.Workflow, workflow) {
				return true
			}
		case model.GoalPipelineHealthy:
			if workflow != "" {
				continue
			}
			repos := g.Spec.Repos
			if len(repos) == 0 {
				repos, err = deps.Store.TargetRepos(readCtx)
				if err != nil {
					return false
				}
			}
			for _, r := range repos {
				if model.SameRepoText(r, repo.String()) {
					return true
				}
			}
		}
	}
	return false
}

func observeBaseHealthSource(ctx context.Context, deps Deps, row model.BaseBranchCI, tip string, readable bool, now time.Time) {
	if !healthSourceWanted(ctx, deps, row.Repo, row.Branch, "") {
		return
	}
	readCtx, stopRead := context.WithTimeout(ctx, 50*time.Millisecond)
	cfg, err := deps.Store.GetRepoConfig(readCtx, row.Repo)
	stopRead()
	if err != nil {
		return
	}
	p := model.GateBaseSource{RowRevision: model.GateBaseRowRevision(row), PolicyRevision: model.GateHealthPolicyRevision(cfg), Tip: tip, At: now}
	if !readable {
		p.Tip = ""
	}
	if cfg != nil && cfg.CIMode == model.CIModeGrain && deps.CI != nil && readable {
		if run, ok := deps.CI.Get(row.RunID); ok {
			p.Complete = run.ID == row.RunID && string(run.Conclusion) == row.Conclusion && baseHealthRunIdentity(run, row) && (row.Conclusion == "" || run.FinishedAt != nil) && len(run.Jobs) > 0
			planVersions := []string{gates.Digest(run.Key)}
			have := map[string]bool{}
			for _, j := range run.Jobs {
				have[j.WorkflowPath+":"+j.JobID] = true
				planVersions = append(planVersions, gates.Digest(struct {
					Path, ID, Key, Conclusion string
					Cached                    bool
				}{j.WorkflowPath, j.JobID, gates.Digest(j.Key), string(j.Conclusion), j.Cached}))
				if j.JobID == "" || j.WorkflowPath == "" || (j.Conclusion != ci.ConclusionSuccess && j.Conclusion != ci.ConclusionFailure && !(row.Conclusion == "" && j.Conclusion == "")) {
					p.Complete = false
				}
			}
			if len(run.Jobs) > 512 {
				p.Complete = false
			} else {
				p.PlanRevision = gates.Digest(planVersions)
			}
			for _, id := range cfg.BaseBranchCIJobs {
				if !have[id] {
					p.Complete = false
				}
			}
			if row.Confirmation == model.BaseCICleared && row.ConfirmRunID != "" && len(row.ConfirmJobs) > 0 {
				if recheck, ok := deps.CI.Get(row.ConfirmRunID); ok && recheck.ID == row.ConfirmRunID && baseHealthRunMatches(recheck, row) && recheck.Conclusion == ci.ConclusionSuccess && recheck.ID != run.ID {
					recheckVersions := []string{gates.Digest(recheck.Key)}
					covered := map[string]bool{}
					for _, j := range recheck.Jobs {
						recheckVersions = append(recheckVersions, gates.Digest(struct {
							Path, ID, Key, Conclusion string
							Cached                    bool
						}{j.WorkflowPath, j.JobID, gates.Digest(j.Key), string(j.Conclusion), j.Cached}))
						if !j.Cached && j.Conclusion == ci.ConclusionSuccess {
							covered[j.WorkflowPath+":"+j.JobID] = true
						}
					}
					p.ConfirmationRevision = gates.Digest(recheckVersions)
					p.Nonreproduction = true
					for _, j := range run.Jobs {
						if j.Conclusion == ci.ConclusionFailure && !slices.Contains(row.ConfirmJobs, j.WorkflowPath+":"+j.JobID) {
							p.Nonreproduction = false
						}
					}
					for _, id := range row.ConfirmJobs {
						if !covered[id] {
							p.Nonreproduction = false
						}
					}
				}
			}
		}
		// A recorded branch is not the configured expected base set. Pin the
		// default-branch read and explicit port refs to this exact settings revision.
		if cfg.WatchDefaultBranch && deps.Client != nil {
			if def, e := deps.Client.DefaultBranch(row.Repo.StoredOwner(), row.Repo.Name); e == nil && def != "" {
				p.ManagedRefs = append([]string{def}, cfg.PortBranches...)
				slices.Sort(p.ManagedRefs)
				p.ManagedRefs = slices.Compact(p.ManagedRefs)
			}
		}
	}
	// The ordinary forge read above must not spend the database write budget.
	diag, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_ = deps.Store.RecordGateBaseSource(diag, row.Repo, row.Branch, p)
}
func baseHealthRunIdentity(run ci.Run, row model.BaseBranchCI) bool {
	return (run.Source == "" || run.Source == "grain") && run.Event == ci.EventPush && model.SameRepoText(run.Repo, row.Repo.String()) && strings.TrimPrefix(run.Ref, "refs/heads/") == row.Branch && run.Commit == row.Commit && !run.Interrupted
}
func baseHealthRunMatches(run ci.Run, row model.BaseBranchCI) bool {
	return baseHealthRunIdentity(run, row) && run.FinishedAt != nil && run.Conclusion.Done()
}

func observeWorkflowHealthSource(ctx context.Context, deps Deps, run ConcludedGitHubWorkflowRun, now time.Time) {
	if !healthSourceWanted(ctx, deps, run.Repo, run.Ref, run.WorkflowName) {
		return
	}
	// The shadow read is separate from the legacy latest-attempt listing;
	// exact-attempt evidence must never change what legacy callbacks receive.
	var jobs []github.WorkflowRunJob
	exact := false
	if reader, ok := deps.Client.(github.WorkflowRunAttemptJobsReader); ok {
		var err error
		jobs, err = reader.ListWorkflowRunAttemptJobs(run.Repo.StoredOwner(), run.Repo.Name, run.RunID, run.Attempt)
		exact = err == nil
	}
	recordWorkflowHealthSource(ctx, deps, run, jobs, exact, now)
}
func recordWorkflowHealthSource(ctx context.Context, deps Deps, run ConcludedGitHubWorkflowRun, jobs []github.WorkflowRunJob, exact bool, now time.Time) {
	if !healthSourceWanted(ctx, deps, run.Repo, run.Ref, run.WorkflowName) {
		return
	}
	r := gates.WorkflowHealthRun{ID: run.RunID, Attempt: run.Attempt, Repository: run.Repo.String(), HeadRepository: run.HeadRepo, Ref: strings.TrimPrefix(run.Ref, "refs/heads/"), Commit: run.HeadSHA, Workflow: run.WorkflowName, Path: run.WorkflowPath, Event: run.Event, Source: "github", Complete: exact && len(jobs) > 0}
	if run.StartedAt != nil {
		r.StartedAt = *run.StartedAt
	}
	if run.CompletedAt != nil {
		r.CompletedAt = *run.CompletedAt
	}
	if r.StartedAt.IsZero() || r.StartedAt.After(r.CompletedAt) || r.CompletedAt.IsZero() || r.HeadRepository == "" || !strings.EqualFold(r.Repository, r.HeadRepository) || !slices.Contains([]string{"webhook", "poll"}, run.Via) {
		r.Complete = false
	}
	// Canonical identity spelling avoids treating a case-only forge spelling as
	// a different authenticated issuer; foreign repos remain foreign.
	if strings.EqualFold(r.Repository, r.HeadRepository) {
		r.HeadRepository = r.Repository
	}
	for _, j := range jobs {
		if j.RunID != run.RunID || j.ID == 0 || j.Status != "completed" {
			r.Complete = false
		}
		r.Jobs = append(r.Jobs, gates.HealthJob{ID: strconv.FormatInt(j.ID, 10), Name: j.Name, Workflow: r.Workflow, Ref: r.Ref, Commit: r.Commit, Conclusion: j.Conclusion, Source: "github", App: "github-actions", RunID: j.RunID, Attempt: r.Attempt})
	}
	if len(r.Jobs) > 512 {
		r.Complete = false
		r.Jobs = nil
	}
	diag, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_ = deps.Store.RecordGateWorkflowSource(diag, r, now)
}
