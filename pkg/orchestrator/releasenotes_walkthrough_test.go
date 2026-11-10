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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// docs/test-plans/2026-11-10.md §8 item 4, on ai-on-gke/grain's own state
// on 2026-11-10: v3.0.0..v3.4.0 tagged, docs/releases/ documenting v3.1.0,
// v3.1.1 and v3.2.0 only, and the repository's own
// .grain/templates/release-notes.yml. Exactly one task each for v3.3.0 and
// v3.4.0, ready to run, none for v3.0.x, and none again after a restart
// (the reconciler's in-memory watch forgotten).
func TestReleaseNotesOnThisRepositorysTagsFilesV330AndV340Once(t *testing.T) {
	resetReleaseNotesWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "example", Name: "widgets"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub, Declarations: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	if _, err := store.SyncRepoDeclarations(ctx, repo, "abc1234", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{Path: ".grain/templates/release-notes.yml", Template: loadRepoReleaseNotesTemplate(t, repo)}},
	}, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}
	forge := &fixtureForge{repo: repo.String(), tags: map[string]fixtureTag{}, files: map[string]string{
		"scripts/release.conf":    "RELEASE_MAJOR=3\n",
		"docs/releases/v3.1.0.md": "# v3.1.0\n",
		"docs/releases/v3.1.1.md": "# v3.1.1\n",
		"docs/releases/v3.2.0.md": "# v3.2.0\n",
	}}
	for i, tag := range []string{"v3.0.0", "v3.0.1", "v3.0.2", "v3.1.0-rc.1", "v3.1.0", "v3.1.1", "v3.2.0-rc.1",
		"v3.2.0-rc.1-discarded", "v3.2.0-rc.2", "v3.2.0-rc.2-discarded", "v3.2.0-rc.3", "v3.2.0", "v3.3.0-rc.1", "v3.3.0",
		"v3.4.0-rc.1", "v3.4.0"} {
		forge.tags[tag] = fixtureTag{commit: strings.Repeat(string("0123456789abcdef"[i]), 40), message: "Release " + tag}
	}
	deps := Deps{Store: store, Client: github.NewClient(forge, nil)}
	filed := func() []string {
		t.Helper()
		tasks, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var titles []string
		for _, task := range tasks {
			titles = append(titles, task.Title)
			if task.Approval == nil {
				t.Errorf("%q is held for a person, want the repository's template to file it ready to run", task.Title)
			}
		}
		slices.Sort(titles)
		return titles
	}
	want := []string{"docs(releases): write the release notes for v3.3.0", "docs(releases): write the release notes for v3.4.0"}
	for i := range 3 {
		if i == 2 {
			releaseNotesPolled.Clear() // a daemon restart
			releaseNotesMerged.Clear()
		}
		if err := reconcileReleaseNotes(ctx, deps, now.Add(time.Duration(i)*releaseNotesPollInterval)); err != nil {
			t.Fatalf("reconcileReleaseNotes (pass %d): %v", i, err)
		}
		if got := filed(); !slices.Equal(got, want) {
			t.Fatalf("pass %d filed %q, want %q", i, got, want)
		}
	}
}
