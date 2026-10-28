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

package demo

// demo_scheduled_workflows.go is `grain demo -github-sim -fake-ci
// -scheduled-workflows`: the two steps of the daemon's reconcile loop a
// workflow schedule walks through -- "declarations", which reads the
// simulated repo's own `.grain` into schedules, and "scheduled-workflows",
// which starts a fired schedule's CI run and settles or reviews it once
// the run has ended -- run on a timer against the demo's own store.
//
// Without it a `workflow:` schedule cannot be walked in a demo at all
// (docs/test-runs/2026-10-14-workflow-schedules-candidate.md): nothing
// reads the repo's declarations, Run now writes a firing nothing ever
// starts, and -fake-ci's runs never end except by being cancelled. So this
// adds three things and nothing else:
//
//   - a `candidate` branch on the simulated repo, beside main and
//     release/1.0, for a schedule's `base: candidate` to name;
//   - those two reconcilers, the orchestrator's own, every interval;
//   - POST /api/e2e/fake-ci/runs/{id}/finish, which ends a -fake-ci run
//     with the conclusion a walkthrough asks for -- the one answer a stub
//     that executes nothing cannot reach by itself -- and GET
//     /api/e2e/scheduled-workflows, which names the bare repo.
//
// Declarations are read from the simulated repo's default branch, the way
// the daemon's RepoDeclarationSource reads a forge's: a shallow clone into
// a temporary directory, then repodecl.Load. A walkthrough pushes its
// `.grain` files straight into the bare repo the demo logs at startup, and
// turns declarations on for the repo on its Repository page, as an
// operator would.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// demoScheduledWorkflowBranch is the branch -scheduled-workflows adds to
// the simulated repo: the base a `workflow:` schedule on this deployment
// names (.grain/schedules/*-candidate.yml).
const demoScheduledWorkflowBranch = "candidate"

// demoScheduledWorkflowReconcilers are the reconcilers runDemoScheduledWorkflows
// runs, by their names in orchestrator.Reconcilers, in that order.
var demoScheduledWorkflowReconcilers = []string{"declarations", "scheduled-workflows"}

// demoBareDeclarations is an orchestrator.DeclarationSource over the
// simulated repo's bare git directory.
type demoBareDeclarations struct {
	repo model.RepoRef
	bare string
}

var _ orchestrator.DeclarationSource = demoBareDeclarations{}

func (d demoBareDeclarations) Head(_ context.Context, repo model.RepoRef) (string, error) {
	if !repo.Same(d.repo) {
		return "", fmt.Errorf("%s: the simulated GitHub only has %s", repo, d.repo)
	}
	return demoGit(d.bare, "rev-parse", "HEAD")
}

func (d demoBareDeclarations) Read(_ context.Context, repo model.RepoRef) (model.RepoDeclarations, string, error) {
	if !repo.Same(d.repo) {
		return model.RepoDeclarations{}, "", fmt.Errorf("%s: the simulated GitHub only has %s", repo, d.repo)
	}
	dir, err := os.MkdirTemp("", "grain-demo-declarations-")
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	defer os.RemoveAll(dir)
	checkout := filepath.Join(dir, "repo")
	if _, err := demoGit("", "clone", "--quiet", "--depth", "1", "--", "file://"+d.bare, checkout); err != nil {
		return model.RepoDeclarations{}, "", err
	}
	commit, err := demoGit(filepath.Join(checkout, ".git"), "rev-parse", "HEAD")
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	declared, err := repodecl.Load(os.DirFS(checkout), repo)
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	return declared, commit, nil
}

// configureScheduledWorkflowsDemo adds demoScheduledWorkflowBranch to the
// simulated repo at bare and returns the reconcilers' Deps.
func configureScheduledWorkflowsDemo(store *model.Store, repo model.RepoRef, bare string,
	runner ci.Runner, client github.Client) (orchestrator.Deps, error) {

	if err := pushDemoGitHubSimBranch(bare, demoScheduledWorkflowBranch); err != nil {
		return orchestrator.Deps{}, err
	}
	return orchestrator.Deps{
		Store:        store,
		Client:       client,
		CI:           runner,
		Declarations: demoBareDeclarations{repo: repo, bare: bare},
	}, nil
}

// runDemoScheduledWorkflows runs demoScheduledWorkflowCycle once straight
// away and then every interval until ctx is done, logging a failed cycle
// as runDemoReconcile does.
func runDemoScheduledWorkflows(ctx context.Context, deps orchestrator.Deps, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := demoScheduledWorkflowCycle(ctx, deps, time.Now().UTC()); err != nil {
			log.Printf("grain demo: scheduled workflows cycle: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// demoScheduledWorkflowCycle is one cycle of runDemoScheduledWorkflows,
// each error named after its reconciler as orchestrator.RunCycle names it.
func demoScheduledWorkflowCycle(ctx context.Context, deps orchestrator.Deps, now time.Time) error {
	return runDemoReconcilers(ctx, deps, now, demoScheduledWorkflowReconcilers...)
}

// Finish ends a run that is still going with conclusion. A run started
// with no jobs of its own -- a workflow schedule's, which names a
// workflow rather than jobs, and which this stub cannot read -- is given
// one job named jobName, so that "success" is a run with a job that
// passed rather than a jobless one (orchestrator.scheduledWorkflowRunPassed
// reads those differently, on purpose).
func (f *demoFakeCI) Finish(id string, conclusion ci.Conclusion, jobName string) (ci.Run, error) {
	return f.FinishWithOutput(id, conclusion, jobName, "", "", "")
}

// FinishWithOutput is Finish with optional step name, step output, and
// job-level error so walkthroughs and e2e specs can exercise shared-fault
// grouping and compact CI views over -fake-ci runs.
func (f *demoFakeCI) FinishWithOutput(id string, conclusion ci.Conclusion, jobName, stepName, output, jobErr string) (ci.Run, error) {
	switch conclusion {
	case ci.ConclusionSuccess, ci.ConclusionFailure, ci.ConclusionErrored:
	default:
		return ci.Run{}, fmt.Errorf("a run can be finished as %q, %q or %q, not %q",
			ci.ConclusionSuccess, ci.ConclusionFailure, ci.ConclusionErrored, conclusion)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.runs {
		run := &f.runs[i]
		if run.ID != id {
			continue
		}
		if run.Conclusion.Done() {
			return ci.Run{}, fmt.Errorf("run %s has already ended (%s)", id, run.Conclusion)
		}
		now := time.Now().UTC()
		if len(run.Jobs) == 0 && strings.TrimSpace(jobName) != "" {
			run.Jobs = []ci.JobRun{{JobID: jobName, Name: jobName}}
		}
		for j := range run.Jobs {
			run.Jobs[j].Conclusion = conclusion
			run.Jobs[j].StartedAt = run.StartedAt
			run.Jobs[j].FinishedAt = &now
			if jobErr != "" {
				run.Jobs[j].Error = jobErr
			}
			if stepName != "" || output != "" {
				name := stepName
				if name == "" {
					name = "run"
				}
				run.Jobs[j].Steps = []ci.StepRun{{
					Name:       name,
					Conclusion: conclusion,
					Output:     output,
					StartedAt:  run.StartedAt,
					FinishedAt: &now,
				}}
			}
		}
		run.FinishedAt = &now
		run.Conclusion = conclusion
		return *run, nil
	}
	return ci.Run{}, fmt.Errorf("no run %s", id)
}

// scheduledWorkflowsDemoHandler serves POST
// /api/e2e/fake-ci/runs/{id}/finish with a body of {"conclusion":
// "success"|"failure"|"errored", "job": "name", "step": "optional",
// "output": "optional", "error": "optional"}, and GET
// /api/e2e/scheduled-workflows, which says where the simulated repo's
// bare git directory is ({"bare": path}) for a walkthrough with no other
// way to learn it to push its declarations to. Everything else goes to
// app.
func scheduledWorkflowsDemoHandler(app http.Handler, f *demoFakeCI, bare string) http.Handler {
	const prefix, suffix = "/api/e2e/fake-ci/runs/", "/finish"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/e2e/scheduled-workflows" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"bare": bare})
			return
		}
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) {
			app.ServeHTTP(w, r)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
		var body struct {
			Conclusion string `json:"conclusion"`
			Job        string `json:"job"`
			Step       string `json:"step"`
			Output     string `json:"output"`
			Error      string `json:"error"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "the body is {\"conclusion\": ..., \"job\": ...}: "+err.Error(), http.StatusBadRequest)
			return
		}
		run, err := f.FinishWithOutput(id, ci.Conclusion(body.Conclusion), body.Job, body.Step, body.Output, body.Error)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(run)
	})
}
