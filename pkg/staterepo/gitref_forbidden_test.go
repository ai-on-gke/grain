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

package staterepo_test

// A merged grain/config change is the third way a git_ref schedule reaches
// the database, after POST/PATCH /api/schedules and `grain state check`,
// and it used to be the one that let a forbidden watch through (#744;
// docs/test-runs/2026-11-07-releases-upgrades-ci.md, Bug F6).

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func deploymentWatching(t *testing.T) (context.Context, *model.Store, *staterepo.Repo, string, func() error) {
	t.Helper()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dataDir := t.TempDir()
	if err := secretsrepo.SaveSettings(dataDir, secretsrepo.Settings{
		Remote: "git@github.com:acme/host-secrets.git", Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, s := range []struct{ id, title, watched string }{
		{"sched-watch", "Watch upstream", "acme/upstream-watched"},
		// Enabled and watching what is now the secrets backup repository:
		// a schedule from before that repository was configured.
		{"sched-grandfathered", "Grandfathered watch", "acme/host-secrets"},
	} {
		if err := store.PutSchedule(ctx, model.Schedule{
			ID: s.id, Title: s.title, Target: target,
			Recurrence: model.Recurrence{Kind: model.RecurrenceGitRef,
				GitRef: &model.GitRefTrigger{Repo: s.watched, Ref: "refs/heads/main"}},
			Overlap: model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
		}); err != nil {
			t.Fatalf("putting %s: %v", s.id, err)
		}
	}
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "state"), Remote: remote, DataDir: dataDir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	apply := func() error {
		_, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion)
		return err
	}
	return ctx, store, repo, remote, apply
}

func watchedRepo(t *testing.T, ctx context.Context, store *model.Store, id string) string {
	t.Helper()
	s, err := store.GetSchedule(ctx, id)
	if err != nil || s == nil || s.Recurrence.GitRef == nil {
		t.Fatalf("reading %s: %+v %v", id, s, err)
	}
	return s.Recurrence.GitRef.Repo
}

func TestAMergedSettingsChangeMayNotWatchAForbiddenRepository(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, to, wantSub string
	}{
		{"retired grain/config alias", "repo: grain/config-alpha", "grain/config-alpha not found: only grain/config exists"},
		{"encrypted secrets backup repository", "repo: acme/host-secrets", "encrypted secrets backup repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, store, _, remote, apply := deploymentWatching(t)
			mergeIntoRemote(t, remote, "settings/schedules.yml", "repo: acme/upstream-watched", tc.to)
			err := apply()
			if err == nil || !errors.Is(err, staterepo.ErrNotApplied) || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Apply = %v, want ErrNotApplied naming %q", err, tc.wantSub)
			}
			if got := watchedRepo(t, ctx, store, "sched-watch"); got != "acme/upstream-watched" {
				t.Fatalf("sched-watch now watches %q: the refused import changed the database", got)
			}
		})
	}
}

func TestAMergedSettingsChangeKeepsAGrandfatheredWatchWorking(t *testing.T) {
	t.Parallel()
	ctx, store, _, remote, apply := deploymentWatching(t)
	// A change that is not about the grandfathered schedule's watch at
	// all must still apply: refusing it would wedge the repository on a
	// row nobody changed.
	mergeIntoRemote(t, remote, "settings/schedules.yml", "title: Watch upstream", "title: Watch upstream (renamed)")
	if err := apply(); err != nil {
		t.Fatalf("Apply = %v, want the unrelated change applied", err)
	}
	s, err := store.GetSchedule(ctx, "sched-watch")
	if err != nil || s == nil || s.Title != "Watch upstream (renamed)" {
		t.Fatalf("sched-watch = %+v (%v), want the merged title", s, err)
	}
	if got := watchedRepo(t, ctx, store, "sched-grandfathered"); got != "acme/host-secrets" {
		t.Fatalf("sched-grandfathered watches %q", got)
	}
}
