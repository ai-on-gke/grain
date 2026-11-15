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

import (
	"context"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// Scenario 6 of grain/task-4249: repositories added and removed on one
// deployment reach another through the state repository export and its
// settings import -- adds arrive with their repo_config rows, and a
// removal on the source is a removal on the destination too, rather than
// the destination's stale rows surviving the import (the revert an
// upgrade once made of merged repo_config changes, grain/task-2731).
func TestRepositoryAddsAndRemovesRoundTripThroughTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src, srcDB := openDB(t)

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	gheWidgets := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	if err := src.PutConfig(ctx, model.Config{
		PollInterval: model.DefaultPollInterval,
		TargetRepos:  []string{widgets.String(), gheWidgets.String()},
	}); err != nil {
		t.Fatal(err)
	}
	for _, rc := range []model.RepoConfig{
		{Repo: widgets, SetupCommand: "make deps", CIMode: model.CIModeGitHub},
		{Repo: gheWidgets, SetupCommand: "make ghe"},
	} {
		if err := src.PutRepoConfig(ctx, rc); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	dst, dstDB := openDB(t)
	if err := staterepo.ImportTables(ctx, dstDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing: %v", err)
	}
	if got, err := dst.TargetRepos(ctx); err != nil || !slices.Equal(got, []string{"acme/widgets", "ghe.example.com/acme/widgets"}) {
		t.Fatalf("imported targets = %v, %v", got, err)
	}
	for ref, want := range map[model.RepoRef]string{widgets: "make deps", gheWidgets: "make ghe"} {
		rc, err := dst.GetRepoConfig(ctx, ref)
		if err != nil || rc == nil || rc.SetupCommand != want {
			t.Fatalf("imported repo_config for %s = %+v, %v; want setup %q", ref, rc, err, want)
		}
	}

	// Remove the second host's repository on the source, as
	// DELETE /api/repos does, and export again over the same directory.
	if err := src.ForgetRepo(ctx, gheWidgets); err != nil {
		t.Fatal(err)
	}
	if err := src.UpdateConfig(ctx, func(c *model.Config) error {
		c.TargetRepos = []string{widgets.String()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatalf("re-exporting: %v", err)
	}
	if err := staterepo.ImportTables(ctx, dstDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("re-importing: %v", err)
	}
	if got, err := dst.TargetRepos(ctx); err != nil || !slices.Equal(got, []string{"acme/widgets"}) {
		t.Fatalf("targets after the removal's import = %v, %v; want only acme/widgets", got, err)
	}
	if rc, err := dst.GetRepoConfig(ctx, gheWidgets); err != nil || rc != nil {
		t.Fatalf("the removed repository's repo_config survived the import: %+v, %v", rc, err)
	}
	if rc, err := dst.GetRepoConfig(ctx, widgets); err != nil || rc == nil || rc.SetupCommand != "make deps" {
		t.Fatalf("the kept repository's repo_config = %+v, %v", rc, err)
	}

	// Re-adding it on the source arrives clean: no row came back with it.
	if err := src.UpdateConfig(ctx, func(c *model.Config) error {
		c.TargetRepos = append(c.TargetRepos, gheWidgets.String())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.ImportTables(ctx, dstDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatal(err)
	}
	if got, err := dst.TargetRepos(ctx); err != nil || !slices.Equal(got, []string{"acme/widgets", "ghe.example.com/acme/widgets"}) {
		t.Fatalf("targets after the re-add's import = %v, %v", got, err)
	}
	if rc, err := dst.GetRepoConfig(ctx, gheWidgets); err != nil || (rc != nil && rc.SetupCommand != "") {
		t.Fatalf("the re-added repository brought old settings back: %+v, %v", rc, err)
	}
}

// grain/task-4288: a removed repository's qualification plan -- settings
// the state repository carries (SettingsTables) -- is gone on the
// importing side too, so neither a restart that re-imports nor an
// upgrade brings the plan back to a repository re-added later.
func TestARemovedRepositorysQualificationPlanDoesNotComeBackThroughTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src, srcDB := openDB(t)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	if err := src.PutConfig(ctx, model.Config{
		PollInterval: model.DefaultPollInterval,
		TargetRepos:  []string{widgets.String(), gadgets.String()},
	}); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []model.RepoRef{widgets, gadgets} {
		if err := src.PutQualificationPlan(ctx, model.QualificationPlan{Repo: repo, Configured: true,
			RequireApproval: true, Items: []model.QualificationItem{{TemplateID: "tpl-soak", Repeat: 2}}}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	dst, dstDB := openDB(t)
	if err := staterepo.ImportTables(ctx, dstDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatal(err)
	}
	if p, err := dst.GetQualificationPlan(ctx, widgets); err != nil || p == nil || len(p.Items) != 1 {
		t.Fatalf("imported qualification plan = %+v, %v", p, err)
	}

	if err := src.ForgetRepo(ctx, widgets); err != nil {
		t.Fatal(err)
	}
	if err := src.UpdateConfig(ctx, func(c *model.Config) error {
		c.TargetRepos = []string{gadgets.String()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Twice: the second import is the restart, which must not find
	// anything to put back either.
	for i := 0; i < 2; i++ {
		if err := staterepo.Export(ctx, srcDB, dir); err != nil {
			t.Fatal(err)
		}
		if err := staterepo.ImportTables(ctx, dstDB, dir, staterepo.SettingsTables); err != nil {
			t.Fatal(err)
		}
		if p, err := dst.GetQualificationPlan(ctx, widgets); err != nil || p != nil {
			t.Fatalf("import %d: the removed repository's qualification plan survived: %+v, %v", i+1, p, err)
		}
		if p, err := dst.GetQualificationPlan(ctx, gadgets); err != nil || p == nil || len(p.Items) != 1 {
			t.Fatalf("import %d: the kept repository's qualification plan = %+v, %v", i+1, p, err)
		}
	}
}
