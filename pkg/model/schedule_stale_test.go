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

package model_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The live real-model suites' shape: a .grain/workflows cron in a
// ci_mode: github repository comes due every night and is dropped, so its
// schedule's LastRunAt stops moving. StaleSchedules names it, with why,
// once it is more than twice its cadence old -- and leaves a schedule that
// is firing, a disabled one, and one still inside the window alone.
func TestStaleSchedulesNamesAScheduleThatStoppedRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	daily := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 7*60 + 37}
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	tmpl := model.WorkflowReviewTemplateID("live-agent")
	for _, s := range []model.Schedule{
		{ID: "workflow-dark", Title: "live-agent", Target: repo, TemplateID: &tmpl, Recurrence: daily, Enabled: true,
			LastRunAt: at(14 * 24 * time.Hour), CreatedAt: now.Add(-60 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
		{ID: "never-ran", Title: "never ran", Target: repo, Recurrence: daily, Enabled: true,
			CreatedAt: now.Add(-5 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
		{ID: "firing", Title: "firing", Target: repo, Recurrence: daily, Enabled: true,
			LastRunAt: at(22 * time.Hour), CreatedAt: now.Add(-60 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
		{ID: "missed-one", Title: "missed one night", Target: repo, Recurrence: daily, Enabled: true,
			LastRunAt: at(40 * time.Hour), CreatedAt: now.Add(-60 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
		{ID: "disabled", Title: "disabled", Target: repo, Recurrence: daily, Enabled: false,
			LastRunAt: at(30 * 24 * time.Hour), CreatedAt: now.Add(-60 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
	} {
		if err := store.PutSchedule(ctx, s); err != nil {
			t.Fatalf("PutSchedule(%s): %v", s.ID, err)
		}
	}

	stale, err := store.StaleSchedules(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range stale {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "never-ran,workflow-dark" {
		t.Fatalf("StaleSchedules = %v, want never-ran (no run since it was created) then workflow-dark", ids)
	}
	dark := stale[1]
	if dark.Workflow != "live-agent" || dark.Cadence != 24*time.Hour {
		t.Errorf("workflow-dark = %+v, want workflow live-agent with a 24h cadence", dark)
	}
	if !strings.Contains(dark.Reason, "github") || !strings.Contains(dark.Reason, ".grain/workflows/live-agent.yml") {
		t.Errorf("workflow-dark's reason = %q, want it to say the repository's CI mode drops .grain/workflows/live-agent.yml", dark.Reason)
	}
}

// A workflow kept on grain CI in a ci_mode: github repository has to keep its
// own `on: schedule` cron so a `.grain/schedules/` entry with `workflow:` and
// `grain-ci: true` parses (repodecl.scheduledWorkflowStem). That cron's own
// schedule row is still dropped at every firing, so its LastRunAt never moves;
// StaleSchedules skips it once an enabled grain-ci schedule on the same
// repository runs that workflow on the default branch.
func TestStaleSchedulesSkipsADroppedWorkflowCronCoveredByAGrainCISchedule(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	daily := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 5*60 + 53}
	ran := now.Add(-7 * time.Hour)
	tmpl := model.WorkflowReviewTemplateID("live-secrets-repo")

	for _, base := range []string{"main", ""} {
		t.Run("base="+base, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := openTestStore(t)
			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
				t.Fatal(err)
			}
			cronID := model.RepoDeclaredID(model.DeclaredSchedule, repo, "workflow.live-secrets-repo")
			secondCronID := model.RepoDeclaredID(model.DeclaredSchedule, repo, "workflow.live-secrets-repo.2")
			peerID := model.RepoDeclaredID(model.DeclaredSchedule, repo, "live-secrets-repo")
			for _, s := range []model.Schedule{
				{ID: cronID, Title: "live-secrets-repo cron", Target: repo, TemplateID: &tmpl,
					Recurrence: daily, Enabled: true, CreatedAt: now.Add(-30 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
				{ID: secondCronID, Title: "live-secrets-repo second cron", Target: repo, TemplateID: &tmpl,
					Recurrence: daily, Enabled: true, CreatedAt: now.Add(-30 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
				{ID: peerID, Title: "Live secrets repository", Target: repo, Base: base, TemplateID: &tmpl,
					GrainCI: true, Recurrence: daily, Enabled: true, LastRunAt: &ran,
					CreatedAt: now.Add(-2 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
			} {
				if err := store.PutSchedule(ctx, s); err != nil {
					t.Fatalf("PutSchedule(%s): %v", s.ID, err)
				}
			}

			stale, err := store.StaleSchedules(ctx, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(stale) != 0 {
				t.Fatalf("StaleSchedules = %+v, want empty when %s runs the workflow on the default branch", stale, peerID)
			}
		})
	}
}

// When no enabled default-branch schedule actually runs the workflow under the
// repository's CI mode, the dropped workflow cron stays flagged.
func TestStaleSchedulesKeepsADroppedWorkflowCronFlaggedWithoutACoveringSchedule(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	otherRepo := model.RepoRef{Owner: "acme", Name: "gadgets"}
	daily := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 5*60 + 53}
	ran := now.Add(-7 * time.Hour)
	tmpl := model.WorkflowReviewTemplateID("live-secrets-repo")
	otherTmpl := model.WorkflowReviewTemplateID("other-workflow")

	cases := []struct {
		name   string
		ciMode model.CIMode
		peers  []model.Schedule
	}{
		{
			name:   "no peer schedule",
			ciMode: model.CIModeGitHub,
		},
		{
			name:   "peer lacks grain-ci",
			ciMode: model.CIModeGitHub,
			peers: []model.Schedule{{
				ID: "peer", Title: "peer", Target: repo, Base: "main", TemplateID: &tmpl,
				GrainCI: false, Recurrence: daily, Enabled: true, LastRunAt: &ran,
				CreatedAt: now.Add(-2 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
			}},
		},
		{
			name:   "peer is disabled",
			ciMode: model.CIModeGitHub,
			peers: []model.Schedule{{
				ID: "peer", Title: "peer", Target: repo, Base: "main", TemplateID: &tmpl,
				GrainCI: true, Recurrence: daily, Enabled: false, LastRunAt: &ran,
				CreatedAt: now.Add(-2 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
			}},
		},
		{
			name:   "peer runs on a non-default branch",
			ciMode: model.CIModeGitHub,
			peers: []model.Schedule{{
				ID: "peer", Title: "peer", Target: repo, Base: "candidate", TemplateID: &tmpl,
				GrainCI: true, Recurrence: daily, Enabled: true, LastRunAt: &ran,
				CreatedAt: now.Add(-2 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
			}},
		},
		{
			name:   "peer targets another repository",
			ciMode: model.CIModeGitHub,
			peers: []model.Schedule{{
				ID: "peer", Title: "peer", Target: otherRepo, Base: "main", TemplateID: &tmpl,
				GrainCI: true, Recurrence: daily, Enabled: true, LastRunAt: &ran,
				CreatedAt: now.Add(-2 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
			}},
		},
		{
			name:   "peer runs a different workflow stem",
			ciMode: model.CIModeGitHub,
			peers: []model.Schedule{{
				ID: "peer", Title: "peer", Target: repo, Base: "main", TemplateID: &otherTmpl,
				GrainCI: true, Recurrence: daily, Enabled: true, LastRunAt: &ran,
				CreatedAt: now.Add(-2 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
			}},
		},
		{
			name:   "ci_mode none drops grain-ci schedules too",
			ciMode: model.CIModeNone,
			peers: []model.Schedule{{
				ID: "peer", Title: "peer", Target: repo, Base: "main", TemplateID: &tmpl,
				GrainCI: true, Recurrence: daily, Enabled: true, LastRunAt: &ran,
				CreatedAt: now.Add(-2 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := openTestStore(t)
			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: tc.ciMode}); err != nil {
				t.Fatal(err)
			}
			cronID := model.RepoDeclaredID(model.DeclaredSchedule, repo, "workflow.live-secrets-repo")
			if err := store.PutSchedule(ctx, model.Schedule{
				ID: cronID, Title: "live-secrets-repo cron", Target: repo, TemplateID: &tmpl,
				Recurrence: daily, Enabled: true, CreatedAt: now.Add(-30 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
			}); err != nil {
				t.Fatalf("PutSchedule(%s): %v", cronID, err)
			}
			for _, p := range tc.peers {
				if err := store.PutSchedule(ctx, p); err != nil {
					t.Fatalf("PutSchedule(%s): %v", p.ID, err)
				}
			}

			stale, err := store.StaleSchedules(ctx, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(stale) != 1 || stale[0].ID != cronID {
				t.Fatalf("StaleSchedules = %+v, want [%s]", stale, cronID)
			}
			if stale[0].Reason == "" {
				t.Errorf("StaleSchedules[0].Reason is empty, want CI-mode drop explanation")
			}
		})
	}
}

// Only the `.grain/workflows` cron itself is inert on purpose: a
// `.grain/schedules/` file or settings-owned schedule that runs the same
// workflow on the default branch (`base: ""`) and forgot `grain-ci: true` is
// genuinely broken and must still be flagged even when another `grain-ci: true`
// schedule exists.
func TestStaleSchedulesFlagsNonCronWorkflowSchedulesMissingGrainCI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub, Declarations: true}); err != nil {
		t.Fatal(err)
	}
	daily := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 5*60 + 53}
	ran := now.Add(-7 * time.Hour)
	tmpl := model.WorkflowReviewTemplateID("live-secrets-repo")

	cronID := model.RepoDeclaredID(model.DeclaredSchedule, repo, "workflow.live-secrets-repo")
	peerID := model.RepoDeclaredID(model.DeclaredSchedule, repo, "live-secrets-repo")
	extraDeclID := model.RepoDeclaredID(model.DeclaredSchedule, repo, "live-secrets-extra")
	createdLongAgo := now.Add(-30 * 24 * time.Hour)

	if _, err := store.SyncRepoDeclarations(ctx, repo, "0123456789abcdef", model.RepoDeclarations{
		Schedules: []model.DeclaredScheduleFile{
			{
				Path: ".grain/workflows/live-secrets-repo.yml",
				Schedule: model.Schedule{
					ID: cronID, Title: "live-secrets-repo cron", Target: repo, TemplateID: &tmpl,
					Recurrence: daily, Enabled: true,
				},
			},
			{
				Path: ".grain/schedules/live-secrets-repo.yml",
				Schedule: model.Schedule{
					ID: peerID, Title: "Live secrets repository", Target: repo, Base: "main", TemplateID: &tmpl,
					GrainCI: true, Recurrence: daily, Enabled: true,
				},
			},
			{
				Path: ".grain/schedules/live-secrets-extra.yml",
				Schedule: model.Schedule{
					ID: extraDeclID, Title: "Extra secrets check without grain-ci", Target: repo, TemplateID: &tmpl,
					GrainCI: false, Recurrence: daily, Enabled: true,
				},
			},
		},
	}, createdLongAgo, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}
	if err := store.UpdateSchedule(ctx, peerID, func(sc *model.Schedule) error {
		sc.LastRunAt = &ran
		return nil
	}); err != nil {
		t.Fatalf("UpdateSchedule(%s): %v", peerID, err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-42", Title: "Settings schedule without grain-ci", Target: repo, TemplateID: &tmpl,
		GrainCI: false, Recurrence: daily, Enabled: true,
		CreatedAt: now.Add(-20 * 24 * time.Hour), NextRunAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("PutSchedule(sched-42): %v", err)
	}

	stale, err := store.StaleSchedules(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range stale {
		ids = append(ids, s.ID)
	}
	want := extraDeclID + ",sched-42"
	if got := strings.Join(ids, ","); got != want {
		t.Fatalf("StaleSchedules IDs = %q, want %q (workflow cron skipped, non-cron schedules flagged)", got, want)
	}
}
