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
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

func resetReleaseNotesWatch(t *testing.T) {
	t.Helper()
	logToTest(t)
	clear := func() {
		releaseNotesPolled.Range(func(k, _ any) bool { releaseNotesPolled.Delete(k); return true })
		releaseNotesMerged.Range(func(k, _ any) bool { releaseNotesMerged.Delete(k); return true })
		releaseNotesSeriesSettled.Range(func(k, _ any) bool { releaseNotesSeriesSettled.Delete(k); return true })
	}
	clear()
	t.Cleanup(clear)
}

func loadRepoReleaseNotesTemplate(t *testing.T, repo model.RepoRef) model.Template {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	decl, err := repodecl.Load(os.DirFS(root), repo)
	if err != nil {
		t.Fatalf("loading repository declarations: %v", err)
	}
	for _, f := range decl.Templates {
		if f.Path == ".grain/templates/release-notes.yml" {
			return f.Template
		}
	}
	t.Fatal(".grain/templates/release-notes.yml not found in declarations")
	return model.Template{}
}

func TestReleaseNotesReconcilerIsRegisteredInCycle(t *testing.T) {
	t.Parallel()
	var declIdx, notesIdx, targetIdx, dispatchIdx = -1, -1, -1, -1
	for i, r := range Reconcilers() {
		switch r.Name {
		case "declarations":
			declIdx = i
		case "release-notes":
			notesIdx = i
		case "target-repos":
			targetIdx = i
		case dispatchReconciler:
			dispatchIdx = i
		}
	}
	if notesIdx < 0 {
		t.Fatal("release-notes reconciler is not registered in Reconcilers()")
	}
	if declIdx < 0 || notesIdx <= declIdx {
		t.Errorf("release-notes (%d) must run after declarations (%d)", notesIdx, declIdx)
	}
	if targetIdx < 0 || notesIdx >= targetIdx {
		t.Errorf("release-notes (%d) must run before target-repos (%d)", notesIdx, targetIdx)
	}
	if dispatchIdx < 0 || notesIdx >= dispatchIdx {
		t.Errorf("release-notes (%d) must run before dispatch (%d)", notesIdx, dispatchIdx)
	}
}

func TestPromotedReleaseTagFilesOneReleaseNotesTaskAndSkipsExistingOrOpen(t *testing.T) {
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
	tmpl := loadRepoReleaseNotesTemplate(t, repo)
	now := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	if _, err := store.SyncRepoDeclarations(ctx, repo, "abc1234", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{Path: ".grain/templates/release-notes.yml", Template: tmpl}},
	}, now, time.UTC); err != nil {
		t.Fatalf("SyncRepoDeclarations: %v", err)
	}

	forge := &fixtureForge{
		repo: repo.String(),
		tags: map[string]fixtureTag{
			"v2.5.0":           {commit: strings.Repeat("2", 40)},
			"v3.1.0":           {commit: strings.Repeat("a", 40), message: "Release v3.1.0"},
			"v3.1.1":           {commit: strings.Repeat("b", 40), message: "Release v3.1.1"},
			"v3.1.2":           {commit: strings.Repeat("c", 40), message: "Release v3.1.2"},
			"v3.1.2-retracted": {commit: strings.Repeat("c", 40), message: "Retracted v3.1.2"},
			"v3.2.0-rc.1":      {commit: strings.Repeat("d", 40), message: "RC 1"},
			"v3.2.0":           {commit: strings.Repeat("e", 40), message: "Release v3.2.0\n\nPromoted-From: v3.2.0-rc.1"},
		},
		files: map[string]string{
			"scripts/release.conf":    "RELEASE_MAJOR=3\nRELEASE_NEVER_PROMOTE=\"v2.5.0\"\n",
			"docs/releases/v3.1.0.md": "# v3.1.0\n",
			"docs/releases/v3.1.1.md": "# v3.1.1\n",
		},
	}
	deps := Deps{Store: store, Client: github.NewClient(forge, nil)}

	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleaseNotes: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("filed %d tasks (%+v), want exactly 1 for v3.2.0", len(tasks), tasks)
	}
	filed := tasks[0]
	if filed.Title != "docs(releases): write the release notes for v3.2.0" {
		t.Errorf("Title = %q, want v3.2.0 release-notes title", filed.Title)
	}
	if filed.Target == nil || !filed.Target.Same(repo) || filed.Base != "main" || !filed.AutoMerge {
		t.Errorf("task placement = (target=%v, base=%q, autoMerge=%v), want (%s, main, true)",
			filed.Target, filed.Base, filed.AutoMerge, repo)
	}
	if filed.Approval == nil {
		t.Error("filed.Approval = nil, want approved task ready for dispatch")
	}
	if !strings.Contains(filed.Body, "v3.1.1") || !strings.Contains(filed.Body, "docs/releases/v3.2.0.md") {
		t.Errorf("Body missing expected previous_version v3.1.1 or target path:\n%s", filed.Body)
	}
	if strings.Contains(strings.ToLower(filed.Body), "staging soak") {
		t.Errorf("Body still mentions retired staging soak:\n%s", filed.Body)
	}

	// Within the poll interval, no additional tag read happens.
	readsBefore := forge.reads
	if err := reconcileReleaseNotes(ctx, deps, now.Add(time.Minute)); err != nil {
		t.Fatalf("reconcileReleaseNotes (+1m): %v", err)
	}
	if forge.reads != readsBefore {
		t.Errorf("reads after 1m = %d, want %d (poll interval guard)", forge.reads, readsBefore)
	}

	// Past the poll interval while the task is still open: idempotent, no second task.
	now = now.Add(releaseNotesPollInterval)
	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleaseNotes (+10m, task open): %v", err)
	}
	if tasks, _ = store.ListTasks(ctx); len(tasks) != 1 {
		t.Fatalf("tasks while v3.2.0 task is open = %d, want 1", len(tasks))
	}

	// Simulate merging docs/releases/v3.2.0.md and closing the task; next poll still files nothing.
	forge.mu.Lock()
	forge.files["docs/releases/v3.2.0.md"] = "# v3.2.0\n"
	forge.mu.Unlock()
	if err := closeTask(ctx, store, filed.ID, now, "merged", func(o *model.Observation) {
		o.ClosedAt = &now
	}); err != nil {
		t.Fatalf("closing task: %v", err)
	}
	now = now.Add(releaseNotesPollInterval)
	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleaseNotes (+20m, notes merged): %v", err)
	}
	if tasks, _ = store.ListTasks(ctx); len(tasks) != 1 {
		t.Fatalf("tasks after v3.2.0.md merged = %d, want 1", len(tasks))
	}
}

func TestPendingReleaseTagFilesOnceAndApprovalOrReStageBehavesIdempotently(t *testing.T) {
	resetReleaseNotesWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "example", Name: "widgets"}

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	tmpl := loadRepoReleaseNotesTemplate(t, repo)
	now := time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC)
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatal(err)
	}

	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)
	forge := &fixtureForge{
		repo: repo.String(),
		tags: map[string]fixtureTag{
			"v3.2.0":         {commit: strings.Repeat("1", 40), message: "Release v3.2.0"},
			"v3.3.0-pending": {commit: shaA, message: "Release v3.3.0 pending approval"},
		},
		files: map[string]string{
			"docs/releases/v3.2.0.md": "# v3.2.0\n",
		},
	}
	deps := Deps{Store: store, Client: github.NewClient(forge, nil)}

	// 1. Staged -pending tag files one release-notes task.
	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleaseNotes (pending): %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListTasks = %d (%v), want 1", len(tasks), err)
	}
	firstID := tasks[0].ID

	// 2. Approval promotes v3.3.0 at the same commit shaA and removes -pending while the PR hasn't merged yet:
	// even if the first task closed without merging (e.g. cancelled), the same (v3.3.0, shaA) does not loop.
	forge.setTag("v3.3.0", shaA, "Release v3.3.0")
	forge.deleteTag("v3.3.0-pending")
	if err := closeTask(ctx, store, firstID, now, "closed without merge", func(o *model.Observation) {
		o.ClosedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(releaseNotesPollInterval)
	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleaseNotes (promoted same commit): %v", err)
	}
	if tasks, _ = store.ListTasks(ctx); len(tasks) != 1 {
		t.Fatalf("tasks after promotion at same commit = %d, want 1 (no infinite re-file loop)", len(tasks))
	}

	// 3. A subsequent version v3.4.0 staged, rejected, then re-staged at a new commit shaB files for shaB.
	forge.mu.Lock()
	forge.files["docs/releases/v3.3.0.md"] = "# v3.3.0\n"
	forge.mu.Unlock()
	forge.setTag("v3.4.0-pending", shaA, "Pending rc.1")
	now = now.Add(releaseNotesPollInterval)
	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	tasks, _ = store.ListTasks(ctx)
	if len(tasks) != 2 {
		t.Fatalf("tasks after v3.4.0-pending@shaA = %d, want 2", len(tasks))
	}
	for _, tk := range tasks {
		if tk.ID != firstID {
			_ = closeTask(ctx, store, tk.ID, now, "rejected rc.1", func(o *model.Observation) { o.ClosedAt = &now })
		}
	}
	forge.setTag("v3.4.0", shaB, "Release v3.4.0 from rc.2")
	forge.deleteTag("v3.4.0-pending")
	now = now.Add(releaseNotesPollInterval)
	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	if tasks, _ = store.ListTasks(ctx); len(tasks) != 3 {
		t.Fatalf("tasks after v3.4.0@shaB = %d, want 3 (new commit gets a fresh task)", len(tasks))
	}
}

func TestManualOpenReleaseNotesTaskSuppressesDuplicateWhileRCTaskDoesNot(t *testing.T) {
	resetReleaseNotesWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "example", Name: "widgets"}

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	tmpl := loadRepoReleaseNotesTemplate(t, repo)
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 9, 17, 0, 0, 0, time.UTC)
	// Seed a manually filed open task for v1.1.0 (without ReleaseNotesVersionTag)
	// and an unrelated open [rc-blocker v1.2.0-rc.1] task mentioning release notes.
	target := repo
	for _, tk := range []model.Task{
		{
			ID:         "manual-110",
			Intent:     model.IntentImplement,
			Title:      "docs(releases): write the release notes for v1.1.0",
			Body:       "Write docs/releases/v1.1.0.md by hand.",
			Target:     &target,
			Base:       "main",
			TemplateID: tmpl.ID,
			CreatedAt:  &now,
		},
		{
			ID:        "rc-blocker-120",
			Intent:    model.IntentImplement,
			Title:     "[rc-blocker v1.2.0-rc.1] fix release notes test flake",
			Body:      "Fix flaky test on v1.2.0-rc.1 referencing docs/releases/v1.2.0.md.",
			Target:    &target,
			Base:      "main",
			CreatedAt: &now,
		},
	} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	forge := &fixtureForge{
		repo: repo.String(),
		tags: map[string]fixtureTag{
			"v1.0.0": {commit: strings.Repeat("1", 40)},
			"v1.1.0": {commit: strings.Repeat("2", 40)},
			"v1.2.0": {commit: strings.Repeat("3", 40)},
		},
		files: map[string]string{
			"docs/releases/v1.0.0.md": "# v1.0.0\n",
		},
	}
	deps := Deps{Store: store, Client: github.NewClient(forge, nil)}
	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleaseNotes: %v", err)
	}

	open110, err := store.OpenTasksWithTag(ctx, ReleaseNotesVersionTag(repo, "v1.1.0"))
	if err != nil || len(open110) != 0 {
		t.Errorf("filed duplicate tagged task for v1.1.0 despite manual open task: %v (%v)", open110, err)
	}
	open120, err := store.OpenTasksWithTag(ctx, ReleaseNotesVersionTag(repo, "v1.2.0"))
	if err != nil || len(open120) != 1 {
		t.Errorf("OpenTasksWithTag(v1.2.0) = %v (%v), want 1 task filed despite unrelated v1.2.0-rc.1 task", open120, err)
	}
}

func TestReleaseNotesRespectsRepoEligibilityAndPersonOnlyGrantGuard(t *testing.T) {
	resetReleaseNotesWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)

	noTemplate := model.RepoRef{Owner: "example", Name: "notemplate"}
	releaseOff := model.RepoRef{Owner: "example", Name: "releaseoff"}
	offTarget := model.RepoRef{Owner: "example", Name: "offtarget"}
	personGrantRepo := model.RepoRef{Owner: "example", Name: "persongrant"}

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{noTemplate.String(), releaseOff.String(), personGrantRepo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: releaseOff, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []model.RepoRef{releaseOff, offTarget} {
		if err := store.PutTemplate(ctx, loadRepoReleaseNotesTemplate(t, r)); err != nil {
			t.Fatal(err)
		}
	}
	guardedTmpl := loadRepoReleaseNotesTemplate(t, personGrantRepo)
	guardedTmpl.Grants = []model.Grant{{Capability: "api:tasks:admin", Via: "test"}}
	if err := store.PutTemplate(ctx, guardedTmpl); err != nil {
		t.Fatal(err)
	}

	forges := multiFixtureForge{}
	for _, r := range []model.RepoRef{noTemplate, releaseOff, offTarget, personGrantRepo} {
		forges[r.String()] = &fixtureForge{
			repo:  r.String(),
			tags:  map[string]fixtureTag{"v1.0.0": {commit: strings.Repeat("f", 40)}},
			files: map[string]string{},
		}
	}

	now := time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)
	if err := reconcileReleaseNotes(ctx, Deps{Store: store, Client: github.NewClient(forges, nil)}, now); err != nil {
		t.Fatalf("reconcileReleaseNotes: %v", err)
	}

	for _, skipped := range []model.RepoRef{noTemplate, releaseOff, offTarget} {
		if forges[skipped.String()].reads != 0 {
			t.Errorf("polled %s %d times, want 0", skipped, forges[skipped.String()].reads)
		}
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListTasks = %d (%v), want 1 for %s", len(tasks), err, personGrantRepo)
	}
	if tasks[0].Approval != nil {
		t.Errorf("task with person-only grant landed approved (%+v), want unapproved", tasks[0].Approval)
	}
}

func TestHistoricalPreNotesMinorSeriesIsNotBackfilledWhileHotfixPatchIsFiled(t *testing.T) {
	resetReleaseNotesWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)
	repo := model.RepoRef{Owner: "example", Name: "widgets"}

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{repo.String()}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, loadRepoReleaseNotesTemplate(t, repo)); err != nil {
		t.Fatal(err)
	}

	forge := &fixtureForge{
		repo: repo.String(),
		tags: map[string]fixtureTag{
			"v3.0.0": {commit: strings.Repeat("1", 40), message: "v3.0.0"},
			"v3.0.1": {commit: strings.Repeat("2", 40), message: "v3.0.1"},
			"v3.0.2": {commit: strings.Repeat("3", 40), message: "v3.0.2"},
			"v3.1.0": {commit: strings.Repeat("4", 40), message: "Release v3.1.0"},
			"v3.1.1": {commit: strings.Repeat("5", 40), message: "Release v3.1.1"},
			"v3.2.0": {commit: strings.Repeat("6", 40), message: "Release v3.2.0"},
		},
		files: map[string]string{
			"docs/releases/v3.1.0.md": "# v3.1.0\n",
			"docs/releases/v3.1.1.md": "# v3.1.1\n",
			"docs/releases/v3.2.0.md": "# v3.2.0\n",
		},
	}
	deps := Deps{Store: store, Client: github.NewClient(forge, nil)}
	now := time.Date(2026, 11, 9, 19, 0, 0, 0, time.UTC)

	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleaseNotes: %v", err)
	}
	if tasks, err := store.ListTasks(ctx); err != nil || len(tasks) != 0 {
		t.Fatalf("ListTasks = %d (%v), want 0 (pre-train v3.0.* tags must not backfill once v3.1/v3.2 are documented)", len(tasks), err)
	}

	// Steady-state poll with no new tags performs only the single ListMatchingTags read.
	readsBefore := forge.reads
	now = now.Add(releaseNotesPollInterval)
	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleaseNotes (+10m steady): %v", err)
	}
	if got := forge.reads - readsBefore; got != 1 {
		t.Errorf("steady-state forge reads = %d, want 1 (ListMatchingTags only)", got)
	}

	// A hotfix patch v3.1.2 on the already-documented 3.1 minor series still files a task from v3.1.1.
	forge.setTag("v3.1.2", strings.Repeat("7", 40), "Release v3.1.2")
	now = now.Add(releaseNotesPollInterval)
	if err := reconcileReleaseNotes(ctx, deps, now); err != nil {
		t.Fatalf("reconcileReleaseNotes (hotfix v3.1.2): %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListTasks after v3.1.2 hotfix = %d (%v), want 1", len(tasks), err)
	}
	if tasks[0].Title != "docs(releases): write the release notes for v3.1.2" {
		t.Errorf("Title = %q, want v3.1.2 release-notes title", tasks[0].Title)
	}
	if !strings.Contains(tasks[0].Body, "v3.1.1") {
		t.Errorf("hotfix Body missing previous_version v3.1.1:\n%s", tasks[0].Body)
	}
}

func TestReleaseNotesHoldsCatalogueRequiresPersonAndEscalatedTemplateSettings(t *testing.T) {
	resetReleaseNotesWatch(t)
	ctx := context.Background()
	store := openTestStoreForOrchestrator(t)

	catPersonRepo := model.RepoRef{Owner: "example", Name: "catperson"}
	catRefusedRepo := model.RepoRef{Owner: "example", Name: "catrefused"}
	keepRunRepo := model.RepoRef{Owner: "example", Name: "keeprun"}
	configReadRepo := model.RepoRef{Owner: "example", Name: "configread"}
	highPrioRepo := model.RepoRef{Owner: "example", Name: "highprio"}

	disabled := false
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{
		catPersonRepo.String(), catRefusedRepo.String(),
		keepRunRepo.String(), configReadRepo.String(), highPrioRepo.String(),
	}
	cfg.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{
		InProcess: map[string]model.CatalogueInProcess{
			"github-sandbox": {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
			"disabled-cap":   {Enabled: &disabled},
			"default-off":    {Enabled: &disabled},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	tCatPerson := loadRepoReleaseNotesTemplate(t, catPersonRepo)
	tCatPerson.Grants = []model.Grant{
		{Capability: "github-sandbox", Via: "test"},
		{Capability: "default-off", Via: model.GrantByDefault},
	}

	tCatRefused := loadRepoReleaseNotesTemplate(t, catRefusedRepo)
	tCatRefused.Grants = []model.Grant{{Capability: "disabled-cap", Via: "test"}}

	tKeepRun := loadRepoReleaseNotesTemplate(t, keepRunRepo)
	tKeepRun.KeepRunning = true

	tConfigRead := loadRepoReleaseNotesTemplate(t, configReadRepo)
	tConfigRead.Reads = []model.RepoRef{model.ConfigRepo()}

	highPrio := model.DefaultTaskPriority + 1
	tHighPrio := loadRepoReleaseNotesTemplate(t, highPrioRepo)
	tHighPrio.Priority = &highPrio

	for _, tmpl := range []model.Template{tCatPerson, tCatRefused, tKeepRun, tConfigRead, tHighPrio} {
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}

	forges := multiFixtureForge{}
	for _, r := range []model.RepoRef{catPersonRepo, catRefusedRepo, keepRunRepo, configReadRepo, highPrioRepo} {
		forges[r.String()] = &fixtureForge{
			repo:  r.String(),
			tags:  map[string]fixtureTag{"v1.0.0": {commit: strings.Repeat("9", 40)}},
			files: map[string]string{},
		}
	}

	now := time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC)
	if err := reconcileReleaseNotes(ctx, Deps{Store: store, Client: github.NewClient(forges, nil)}, now); err != nil {
		t.Fatalf("reconcileReleaseNotes: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil || len(tasks) != 5 {
		t.Fatalf("ListTasks = %d (%v), want 5", len(tasks), err)
	}
	for _, tk := range tasks {
		if tk.Approval != nil {
			t.Errorf("task for %v landed approved (%+v), want unapproved for human review", tk.Target, tk.Approval)
		}
		if tk.Target != nil && tk.Target.Same(catPersonRepo) {
			if !strings.Contains(tk.Body, "Filed for a person to approve") {
				t.Errorf("catalogue requires_person task missing approval note:\n%s", tk.Body)
			}
			for _, g := range tk.Grants {
				if g.Capability == "default-off" {
					t.Errorf("refused GrantByDefault capability default-off was kept on task grants: %+v", tk.Grants)
				}
			}
		}
		if tk.Target != nil && tk.Target.Same(catRefusedRepo) && !strings.Contains(tk.Body, "settings/capabilities.yml refuses") {
			t.Errorf("catalogue-refused task missing refusal note:\n%s", tk.Body)
		}
	}
}
