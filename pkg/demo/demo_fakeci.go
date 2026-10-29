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

// demo_fakeci.go is `grain demo -fake-ci`: a grain CI that takes runs and
// reports them and never executes anything.
//
// Ordinary `grain demo` wires no CI at all, so every route that starts a
// run answers 404 "this deployment has no grain CI wired" before any of
// its own checks are asked -- and the checks in front of a release run
// (pkg/ui's checkReleaseRef: a reviewed branch only, and only a system
// admin may pin a commit) were unreachable from a walkthrough or an e2e
// spec (docs/test-plans/2026-10-02.md §1). The observability demo wires a
// real ci.Service, but that one executes its workflow on the host, which
// is the opposite of what a demo of who-may-start-what wants. This stub
// sits exactly where ci.Service would, at ui.Config.CI, so everything in
// front of it -- the routes, the RBAC, the ref checks -- is the real
// thing, and the only answer that is not is the run itself: it is
// recorded and stays pending until somebody cancels it.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// demoFakeCI is the stub. It is safe for concurrent use: the UI server
// serves each request on a goroutine of its own.
type demoFakeCI struct {
	ci.NopRunner
	mu   sync.Mutex
	next int
	runs []ci.Run // in the order they were started
}

var (
	_ ci.Runner   = (*demoFakeCI)(nil)
	_ ci.RunStore = (*demoFakeCI)(nil)
)

// Start records req as a run and returns it, pending. Nothing about the
// repository is read -- there is no repository -- so a ref is accepted
// whatever it names; refusing a ref is what the callers in front of this
// are for, and what -fake-ci exists to show.
func (f *demoFakeCI) Start(_ context.Context, req ci.Request) (ci.Run, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return ci.Run{}, fmt.Errorf("a run needs a repository")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	now := time.Now().UTC()
	event := req.Event
	if event == "" {
		event = ci.EventPush
	}
	run := ci.Run{
		ID:          fmt.Sprintf("fake-ci-%d", f.next),
		Repo:        req.Repo,
		Ref:         req.Ref,
		Commit:      req.Commit,
		Event:       event,
		RequestedBy: req.RequestedBy,
		StartedAt:   &now,
		Jobs:        []ci.JobRun{},
	}
	for _, job := range req.Jobs {
		run.Jobs = append(run.Jobs, ci.JobRun{JobID: job, Name: job})
	}
	f.runs = append(f.runs, run)
	return run, nil
}

// WorkflowOptions has no workflow files to read, so it offers nothing.
func (f *demoFakeCI) WorkflowOptions(context.Context, ci.Request) ([]ci.WorkflowOption, error) {
	return nil, nil
}

func (f *demoFakeCI) Put(run ci.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.runs {
		if f.runs[i].ID == run.ID {
			f.runs[i] = run
			return
		}
	}
	f.runs = append(f.runs, run)
}

func (f *demoFakeCI) Update(id string, fn func(*ci.Run)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.runs {
		if f.runs[i].ID == id {
			fn(&f.runs[i])
			return
		}
	}
}

func (f *demoFakeCI) Live(key ci.Key) (ci.Run, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.runs) - 1; i >= 0; i-- {
		run := f.runs[i]
		if run.Key == key && !run.Conclusion.Done() {
			return run, true
		}
	}
	return ci.Run{}, false
}

func (f *demoFakeCI) Get(id string) (ci.Run, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, run := range f.runs {
		if run.ID == id {
			return run, true
		}
	}
	return ci.Run{}, false
}

// List is newest first, as ci.Service's is.
func (f *demoFakeCI) List() []ci.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.runs)
	slices.Reverse(out)
	return out
}

func (f *demoFakeCI) Find(filter ci.RunFilter, limit int) ci.RunPage {
	all := f.List()
	matched := ci.SortRuns(ci.FilterRuns(all, filter), filter, time.Now())
	page := ci.RunPage{Runs: matched, Matched: len(matched), Stored: len(all)}
	if limit > 0 && len(page.Runs) > limit {
		page.Runs = page.Runs[:limit]
	}
	return page
}

// Cancel is the one way a stub run ever finishes.
func (f *demoFakeCI) Cancel(id, reason string, by ci.CancelSource) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.runs {
		run := &f.runs[i]
		if run.ID != id || run.Conclusion.Done() {
			continue
		}
		now := time.Now().UTC()
		run.FinishedAt = &now
		run.Conclusion = ci.ConclusionCancelled
		run.CancelReason = reason
		run.CancelledBy = by
		return true
	}
	return false
}

// CacheStats and ClearCache: a stub that runs nothing keeps no cache.
func (f *demoFakeCI) CacheStats(context.Context) (ci.CacheStats, bool, error) {
	return ci.CacheStats{}, false, nil
}

func (f *demoFakeCI) ClearCache(context.Context) (int, bool, error) {
	return 0, false, nil
}

// configureFakeCIDemo wires the stub into cfg and records repo's default
// branch, main, as a base branch grain watches -- the row a real
// deployment's reconciler writes once grain has merged into a branch,
// and the thing a release run's ref check reads as "already reviewed".
// A demo has no reconciler to write it, and nothing over HTTP may.
// Turning grain CI on for repo is left to whoever walks it (PUT
// /api/repos/{owner}/{name}/ci-mode), so that refusal stays reachable too.
func configureFakeCIDemo(ctx context.Context, store *model.Store, cfg ui.Config, repo model.RepoRef) (ui.Config, error) {
	cfg.CI = &demoFakeCI{}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo:      repo,
		Branch:    "main",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		return cfg, fmt.Errorf("watching %s main: %w", repo, err)
	}
	return cfg, nil
}
