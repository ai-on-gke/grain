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

package gitproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// The release train's tag grant (design/playbook/releases.playbook.md
// §0.4), read from this repository's own .grain/templates/release-train.yml:
// a run of a task filed from it may push the rc, -pending and -discarded
// tags, force-move candidate, make a release/vX.Y.Z branch, and -- since
// the train releases a qualified rc itself (playbook §1) -- create vM.Y.0
// and force-move latest, all on main's history. It may not move or delete
// a release, tag a patch or a retraction, or delete latest; and
// scripts/release-from-trunk.sh narrows vM.Y.0 and latest further, to the
// commit of the rc that qualified.
func TestTheReleaseTrainsGrantReachesOnlyItsOwnRefs(t *testing.T) {
	t.Parallel()
	text, err := os.ReadFile(filepath.Join("..", "..", ".grain", "templates", "release-train.yml"))
	if err != nil {
		t.Fatal(err)
	}
	repo := model.GrainSourceRepo()
	declared, err := repodecl.Load(fstest.MapFS{
		".grain/templates/release-train.yml": {Data: text},
		".grain/schedules/release-train.yml": {Data: mustRead(t, filepath.Join("..", "..", ".grain", "schedules", "release-train.yml"))},
	}, repo)
	if err != nil {
		t.Fatalf("loading .grain/templates/release-train.yml: %v", err)
	}

	store, ctx := openStoreForTest(t)
	now := time.Date(2026, 11, 3, 4, 15, 0, 0, time.UTC)
	if _, err := store.SyncRepoDeclarations(ctx, repo, strings.Repeat("d", 40), declared, now, time.UTC); err != nil {
		t.Fatalf("syncing the declarations: %v", err)
	}
	sched, err := store.GetSchedule(ctx, model.RepoDeclaredID(model.DeclaredSchedule, repo, "release-train"))
	if err != nil || sched == nil {
		t.Fatalf("reading the schedule: %v, %v", sched, err)
	}
	fired, err := store.FireScheduleNow(ctx, *sched, now, model.RepoRef{})
	if err != nil || fired.TaskID == "" {
		t.Fatalf("firing the schedule: %+v, %v", fired, err)
	}
	task, err := store.GetTask(ctx, fired.TaskID)
	if err != nil || task == nil {
		t.Fatalf("reading the filed task: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "r1", TaskID: task.ID, Sandbox: "sandbox-train", Attempt: 1, StartedAt: now},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}
	// Regression for task 3060: on a ci_mode: github repository (the default
	// here, and ai-on-gke/grain in production), firing the release-train
	// schedule must leave the sandbox able to read its own repository's CI
	// (`grain ci list`, `grain ci checks`) without holding grain#ci or
	// opening other repositories' CI.
	var grantNames []string
	for _, g := range task.Grants {
		grantNames = append(grantNames, g.Capability)
	}
	if model.HasGrainCI(grantNames) {
		t.Errorf("scheduled release-train task on a ci_mode: github repo holds grain#ci: %+v", task.Grants)
	}
	scope, ok, err := store.SandboxNamespaceScope(ctx, "sandbox-train")
	if err != nil || !ok {
		t.Fatalf("SandboxNamespaceScope: ok=%v err=%v", ok, err)
	}
	if !scope.MayReadCI || scope.MayReadAllCI || !scope.AllowedRepos[repo.String()] || scope.AllowedRepos["other/repo"] {
		t.Errorf("SandboxNamespaceScope = %+v, want MayReadCI=true MayReadAllCI=false AllowedRepos[%s]=true", scope, repo)
	}
	authorize := func(u RefUpdate) string {
		t.Helper()
		reason, err := (&ModelAuthorizer{Store: store}).AuthorizeRefUpdates(withPushCheck(ctx, historyOnMain(oidB)),
			"sandbox-train", repo.Owner, repo.Name, []RefUpdate{u})
		if err != nil {
			t.Fatal(err)
		}
		return reason
	}

	for name, u := range map[string]RefUpdate{
		"an rc tag":                 {Old: zero, New: oidB, Ref: "refs/tags/v3.2.0-rc.1"},
		"a -pending tag":            {Old: zero, New: oidB, Ref: "refs/tags/v3.2.0-pending"},
		"a -discarded tag":          {Old: zero, New: oidB, Ref: "refs/tags/v3.2.0-rc.1-discarded"},
		"candidate, force-moved":    {Old: oidA, New: oidB, Ref: "refs/heads/candidate"},
		"the convergence branch":    {Old: zero, New: oidB, Ref: "refs/heads/release/v3.2.0"},
		"the run's own task branch": {Old: oidA, New: oidB, Ref: "refs/heads/" + task.BranchName()},
		"a release tag":             {Old: zero, New: oidB, Ref: "refs/tags/v3.2.0"},
		"a two-digit minor release": {Old: zero, New: oidB, Ref: "refs/tags/v3.12.0"},
		"latest, moved":             {Old: oidA, New: oidB, Ref: "refs/tags/latest"},
		"latest, created":           {Old: zero, New: oidB, Ref: "refs/tags/latest"},
	} {
		if reason := authorize(u); reason != "" {
			t.Errorf("%s (%s): refused: %s", name, u.Ref, reason)
		}
	}

	for name, u := range map[string]RefUpdate{
		"latest, deleted":                    {Old: oidA, New: zero, Ref: "refs/tags/latest"},
		"a release tag, moved":               {Old: oidA, New: oidB, Ref: "refs/tags/v3.1.0"},
		"a release tag, deleted":             {Old: oidA, New: zero, Ref: "refs/tags/v3.1.0"},
		"a patch release tag":                {Old: zero, New: oidB, Ref: "refs/tags/v3.1.1"},
		"main":                               {Old: oidA, New: oidB, Ref: "refs/heads/main"},
		"a -pending tag, deleted":            {Old: oidA, New: zero, Ref: "refs/tags/v3.2.0-pending"},
		"an rc tag, moved":                   {Old: oidA, New: oidB, Ref: "refs/tags/v3.2.0-rc.1"},
		"a retraction":                       {Old: zero, New: oidB, Ref: "refs/tags/v3.1.0-retracted"},
		"the convergence branch, rewritten":  {Old: oidA, New: oidB, Ref: "refs/heads/release/v3.2.0"},
		"the convergence branch, deleted":    {Old: oidA, New: zero, Ref: "refs/heads/release/v3.2.0"},
		"a branch nested under the release/": {Old: zero, New: oidB, Ref: "refs/heads/release/v3/x"},
		// The right refs, on a commit main does not have: the workflows
		// they start would run that commit's code.
		"an rc tag off main":              {Old: zero, New: oidA, Ref: "refs/tags/v3.2.0-rc.1"},
		"a -pending tag off main":         {Old: zero, New: oidA, Ref: "refs/tags/v3.2.0-pending"},
		"candidate, force-moved off main": {Old: oidB, New: oidA, Ref: "refs/heads/candidate"},
		"a convergence branch off main":   {Old: zero, New: oidA, Ref: "refs/heads/release/v3.2.0"},
		"a release tag off main":          {Old: zero, New: oidA, Ref: "refs/tags/v3.2.0"},
		"latest, moved off main":          {Old: oidB, New: oidA, Ref: "refs/tags/latest"},
	} {
		reason := authorize(u)
		if reason == "" {
			t.Errorf("%s (%s): the release train's grant let it through", name, u.Ref)
			continue
		}
		if !strings.Contains(reason, ".grain/templates/release-train.yml") {
			t.Errorf("%s (%s): refused without naming the declaration: %q", name, u.Ref, reason)
		}
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The release train task reads its verdicts with `grain ci list` and
// `grain ci checks` (scripts/release-train-task.sh). ai-on-gke/grain's CI
// is GitHub Actions, and a task filed for a repository grain CI does not
// judge has its grain-ci grant stripped (model.applyGrainCIGrant), so the
// template has to grant the read itself: without api:ci-read every tick
// was refused at its first `grain ci list` and ended
// RELEASE_TRAIN_RESULT=failed (docs/test-runs/2026-11-04-release-train.md,
// finding 2).
func TestTheReleaseTrainTaskMayReadItsRepositorysCI(t *testing.T) {
	t.Parallel()
	repo := model.GrainSourceRepo()
	declared, err := repodecl.Load(fstest.MapFS{
		".grain/templates/release-train.yml": {Data: mustRead(t, filepath.Join("..", "..", ".grain", "templates", "release-train.yml"))},
		".grain/schedules/release-train.yml": {Data: mustRead(t, filepath.Join("..", "..", ".grain", "schedules", "release-train.yml"))},
	}, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []model.CIMode{model.CIModeGitHub, model.CIModeGrain} {
		t.Run(string(mode), func(t *testing.T) {
			store, ctx := openStoreForTest(t)
			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: mode}); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 4, 4, 15, 0, 0, time.UTC)
			if _, err := store.SyncRepoDeclarations(ctx, repo, strings.Repeat("d", 40), declared, now, time.UTC); err != nil {
				t.Fatal(err)
			}
			sched, err := store.GetSchedule(ctx, model.RepoDeclaredID(model.DeclaredSchedule, repo, "release-train"))
			if err != nil || sched == nil {
				t.Fatalf("reading the schedule: %v, %v", sched, err)
			}
			fired, err := store.FireScheduleNow(ctx, *sched, now, model.RepoRef{})
			if err != nil || fired.TaskID == "" {
				t.Fatalf("firing the schedule: %+v, %v", fired, err)
			}
			if err := store.StartRun(ctx, model.Run{ID: "r1", TaskID: fired.TaskID, Sandbox: "sandbox-train", Attempt: 1, StartedAt: now},
				model.Limits{}); err != nil {
				t.Fatal(err)
			}
			scope, live, err := store.SandboxNamespaceScope(ctx, "sandbox-train")
			if err != nil || !live {
				t.Fatalf("scope: live %v, %v", live, err)
			}
			if !scope.MayReadCI {
				t.Fatalf("ci_mode %s: the release train's run may not read CI (grants %v): grain ci list would be refused", mode, scope.Grants)
			}
			if !scope.MayReadAllCI && !strings.EqualFold(scope.TaskRepo, repo.String()) && !scope.AllowedRepos[repo.String()] {
				t.Fatalf("ci_mode %s: the run may read CI, but not %s's (grants %v)", mode, repo, scope.Grants)
			}
		})
	}
}
