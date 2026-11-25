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

// The per-repository reflection rows: one for every repository the
// deployment works in, seeded with the reflection's own defaults, kept
// as the deployment left them, and gone when the repository is.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSystemSchedulesSeedsOneReflectionPerRepository(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	for _, repo := range []model.RepoRef{widgets, stateRepo} {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, PromptExtension: "Mind the generated client."}); err != nil {
			t.Fatalf("PutRepoConfig(%s): %v", repo, err)
		}
	}
	// A repository a task targets with no settings row of its own.
	human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	if err := store.PutTask(ctx, model.Task{
		ID: "t1", Title: "Fix the gadget", Target: &gadgets,
		Filing: model.Filing{Attribution: human, Reason: model.ReasonDirect},
	}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}
	for _, repo := range []model.RepoRef{widgets, gadgets} {
		id := model.RepoReflectionTemplateID(repo)
		sched, err := store.GetSchedule(ctx, id)
		if err != nil {
			t.Fatalf("GetSchedule(%s): %v", id, err)
		}
		if sched == nil {
			t.Fatalf("no reflection schedule for %s, a repository this deployment works in", repo)
		}
		if sched.Target != repo {
			t.Errorf("%s targets %s, want %s", id, sched.Target, repo)
		}
		want := model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: model.RepoReflectionEveryNMinutes}
		if sched.Recurrence != want {
			t.Errorf("%s cadence = %+v, want %+v", id, sched.Recurrence, want)
		}
		if !sched.Enabled || !sched.AutoApproveProposals {
			t.Errorf("%s enabled=%v autoApproveProposals=%v, want both on", id, sched.Enabled, sched.AutoApproveProposals)
		}
		if !sched.NextRunAt.After(now) {
			t.Errorf("%s first fires at %s, on or before the cycle that seeded it", id, sched.NextRunAt)
		}
	}
	if sched, err := store.GetSchedule(ctx, model.RepoReflectionTemplateID(stateRepo)); err != nil || sched != nil {
		t.Errorf("the state repository got a reflection schedule (%v, %v); it holds settings, not code", sched, err)
	}

	// The deployment's answers for one repository survive later cycles.
	daily := model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 24 * 60}
	widgetsID := model.RepoReflectionTemplateID(widgets)
	if err := store.UpdateSchedule(ctx, widgetsID, func(s *model.Schedule) error {
		s.Recurrence, s.AutoApproveProposals = daily, false
		return nil
	}); err != nil {
		t.Fatalf("editing %s: %v", widgetsID, err)
	}
	if err := reconcileSystemSchedules(ctx, deps, now.Add(time.Hour)); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}
	sched, err := store.GetSchedule(ctx, widgetsID)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule(%s) = %v, %v", widgetsID, sched, err)
	}
	if sched.Recurrence != daily || sched.AutoApproveProposals {
		t.Errorf("a cycle wrote over the deployment's answers for %s: cadence %+v, autoApproveProposals %v",
			widgetsID, sched.Recurrence, sched.AutoApproveProposals)
	}
}

func TestSystemSchedulesRetiresTheReflectionOfARepositoryThatIsGone(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	now := time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC)

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, PromptExtension: "x"}); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}
	if err := reconcileSystemSchedules(ctx, deps, now); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	id := model.RepoReflectionTemplateID(widgets)
	if sched, err := store.GetSchedule(ctx, id); err != nil || sched == nil {
		t.Fatalf("GetSchedule(%s) = %v, %v; want the seeded row", id, sched, err)
	}

	// An empty settings row is deleted, and no task targets the repository.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets}); err != nil {
		t.Fatalf("clearing the settings row: %v", err)
	}
	if err := reconcileSystemSchedules(ctx, deps, now.Add(time.Hour)); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}
	if sched, err := store.GetSchedule(ctx, id); err != nil || sched != nil {
		t.Errorf("GetSchedule(%s) = %v, %v; want the row gone with its repository", id, sched, err)
	}
}
