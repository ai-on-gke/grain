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

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestSettingsRepoNamesInternalOrExternalRepository(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatalf("opening the config repository: %v", err)
	}
	manager := NewStateManager(dataDir, nil, repo, OpenSecrets(dataDir), nil, nil)
	wantInternal := model.RepoRef{Owner: "grain", Name: "config"}
	if got := manager.settingsRepo(); got != wantInternal {
		t.Fatalf("settingsRepo() in internal mode = %v, want %v", got, wantInternal)
	}
	if !manager.isInternalConfigRepo() {
		t.Fatal("expected isInternalConfigRepo() = true in internal mode")
	}

	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{
		Remote: "https://github.com/acme/grain-config.git",
		Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	wantExternal := model.RepoRef{Owner: "acme", Name: "grain-config"}
	if got := manager.settingsRepo(); got != wantExternal {
		t.Fatalf("settingsRepo() in external mode = %v, want %v", got, wantExternal)
	}
	if manager.isInternalConfigRepo() {
		t.Fatal("expected isInternalConfigRepo() = false in external mode")
	}
}

func TestConfigRepoInternalAndExternalModes(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()

	// Prepare an external bare repository with an unrelated git history and its own template.
	remote := filepath.Join(t.TempDir(), "external-config.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	extSourceDir := t.TempDir()
	extStore, extDB, err := OpenStore("sqlite", "", extSourceDir)
	if err != nil {
		t.Fatalf("opening ext source store: %v", err)
	}
	defer extDB.Close()
	if err := extStore.PutTemplate(ctx, model.Template{
		ID:    "tmpl-ext-1",
		Name:  "external-iac",
		Title: "External IaC template v1",
		Body:  "Managed in external git repo",
	}); err != nil {
		t.Fatalf("creating external template: %v", err)
	}
	extWork := filepath.Join(t.TempDir(), "ext-work")
	if out, err := exec.Command("git", "clone", remote, extWork).CombinedOutput(); err != nil {
		t.Fatalf("git clone remote: %v: %s", err, out)
	}
	for _, kv := range [][2]string{{"user.name", "ops"}, {"user.email", "ops@localhost"}} {
		if out, err := exec.Command("git", "-C", extWork, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("git config: %v: %s", err, out)
		}
	}
	if err := staterepo.Export(ctx, extDB, extWork); err != nil {
		t.Fatalf("exporting external state: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(extWork, model.SchemaVersion); err != nil {
		t.Fatalf("writing schema version: %v", err)
	}
	if out, err := exec.Command("git", "-C", extWork, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", extWork, "commit", "-m", "Initial external IaC config").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", extWork, "push", "-u", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("git push: %v: %s", err, out)
	}

	// 1. Start in Internal mode (`Remote == ""`) with a populated local `grain/config`.
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer db.Close()
	if err := store.PutTemplate(ctx, model.Template{
		ID:    "tmpl-local-1",
		Name:  "internal-only",
		Title: "Internal local template",
		Body:  "Created in internal mode",
	}); err != nil {
		t.Fatalf("creating internal template: %v", err)
	}

	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatalf("opening state repo: %v", err)
	}
	if err := loadStateRepo(ctx, dataDir, repo, db, storeMigration{}); err != nil {
		t.Fatalf("loading state repo: %v", err)
	}
	manager := NewStateManager(dataDir, db, repo, OpenSecrets(dataDir), nil, nil)
	prClient := &StateRepoPullRequestClient{Manager: manager, Store: store}
	if !prClient.isLocal("grain", "config") {
		t.Fatal("expected StateRepoPullRequestClient to intercept grain/config in internal mode")
	}
	head, err := prClient.GetBranchHead("grain", "config", "main")
	if err != nil || head == nil || head.SHA == "" {
		t.Fatalf("GetBranchHead in internal mode: head=%+v err=%v", head, err)
	}
	if err := prClient.CreateBranch("grain", "config", "grain/merge-batch-1", head.SHA); err != nil {
		t.Fatalf("CreateBranch in internal mode: %v", err)
	}
	if err := prClient.DeleteBranch("grain", "config", "grain/merge-batch-1"); err != nil {
		t.Fatalf("DeleteBranch in internal mode: %v", err)
	}
	if exists, err := prClient.BranchExists("grain", "config", "grain/merge-batch-1"); err != nil || exists {
		t.Fatalf("BranchExists after DeleteBranch = %v, %v; want false, nil", exists, err)
	}
	if err := prClient.DeleteBranch("grain", "config", "main"); err == nil {
		t.Fatal("expected DeleteBranch to refuse deleting default branch in internal mode")
	}
	if err := prClient.AddLabel("grain", "config", 1, "in-progress"); err != nil {
		t.Fatalf("AddLabel in internal mode: %v", err)
	}
	if reqs, err := prClient.MergeRequirements("grain", "config", 1, "main"); err != nil || len(reqs.RequiredChecks) != 0 {
		t.Fatalf("MergeRequirements in internal mode = %+v, %v", reqs, err)
	}

	// Reject invalid branch names before saving state-repo.json or secrets-repo.json.
	for _, badBranch := range []string{"-b", "invalid..branch", "@{-1}"} {
		if _, err := manager.SetRemote(ctx, ui.StateRepoRemoteRequest{Remote: remote, Branch: badBranch}); err == nil {
			t.Fatalf("SetRemote accepted invalid branch %q", badBranch)
		}
		if _, err := manager.SetSecretsRepo(ctx, ui.SecretsRepoRequest{Remote: "https://github.com/acme/sec.git", Branch: badBranch}); err == nil {
			t.Fatalf("SetSecretsRepo accepted invalid branch %q", badBranch)
		}
	}

	// 2. Switch from populated Internal mode to External mode with unrelated git history.
	status, err := manager.SetRemote(ctx, ui.StateRepoRemoteRequest{
		Remote: remote,
		Branch: "main",
		Token:  "ghp_external_token",
	})
	if err != nil {
		t.Fatalf("SetRemote: %v", err)
	}
	if status.Mode != "external" || status.Remote != remote || status.Branch != "main" {
		t.Fatalf("status = %+v, want Mode=external Remote=%q Branch=main", status, remote)
	}
	if prClient.isLocal("grain", "config") {
		t.Fatal("expected StateRepoPullRequestClient NOT to intercept grain/config in external mode")
	}
	if _, err := prClient.CreatePullRequest("grain", "config", "grain/task-1", "main", "title", "body"); err == nil {
		t.Fatal("expected CreatePullRequest for grain/config to be refused in external mode")
	}
	if _, err := prClient.CompareRefs("grain", "config", "main", "grain/task-1"); err == nil {
		t.Fatal("expected CompareRefs for grain/config to be refused in external mode")
	}
	if _, err := prClient.EnqueuePullRequest("grain", "config", 1, strings.Repeat("a", 40)); err == nil {
		t.Fatal("expected EnqueuePullRequest for grain/config to be refused in external mode")
	}
	if _, err := prClient.MergeQueueEntryOf("grain", "config", 1); err == nil {
		t.Fatal("expected MergeQueueEntryOf for grain/config to be refused in external mode")
	}
	if err := prClient.DeleteBranch("grain", "config", "grain/task-1"); err == nil {
		t.Fatal("expected DeleteBranch for grain/config to be refused in external mode")
	}
	if err := prClient.SetDefaultBranch("grain", "config", "main"); err == nil {
		t.Fatal("expected SetDefaultBranch for grain/config to be refused in external mode")
	}
	if _, err := prClient.ListPullRequestCommits("grain", "config", 1); err == nil {
		t.Fatal("expected ListPullRequestCommits for grain/config to be refused in external mode")
	}
	if err := prClient.AddLabel("grain", "config", 1, "in-progress"); err == nil {
		t.Fatal("expected AddLabel for grain/config to be refused in external mode")
	}
	if _, err := prClient.MergeRequirements("grain", "config", 1, "main"); err == nil {
		t.Fatal("expected MergeRequirements for grain/config to be refused in external mode")
	}
	ciSource := CiWorkflowSource{ConfigRepoDir: StateRepoDir(dataDir), TempDir: t.TempDir()}
	if _, _, err := ciSource.Workflows(ctx, "grain/config", "main"); err == nil {
		t.Fatal("expected CiWorkflowSource.Workflows for grain/config to be refused in external mode")
	}
	checks := StateCheckSandboxes{ConfigRepoDir: StateRepoDir(dataDir), TempDir: t.TempDir()}
	if _, err := checks.Acquire(ctx, ci.SandboxRequest{Builtin: StateCheckBuiltin, Repo: "grain/config"}); err == nil {
		t.Fatal("expected StateCheckSandboxes.Acquire for grain/config to be refused in external mode")
	}

	// Setting secrets repo to the same URL is refused.
	if _, err := manager.SetSecretsRepo(ctx, ui.SecretsRepoRequest{Remote: remote}); err == nil {
		t.Fatal("SetSecretsRepo allowed the external config repo URL as the secrets repo")
	}

	// Verify the database adopted the external repository's state and replaced the internal template.
	templates, err := store.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates after switching to external repo: %v", err)
	}
	if len(templates) != 1 || templates[0].Name != "external-iac" || templates[0].Title != "External IaC template v1" {
		t.Fatalf("expected external template in DB after SetRemote, got %+v", templates)
	}

	// Verify the external remote never received the old internal template commit.
	tmplPath := staterepo.DumpPath("template")
	out, err := exec.Command("git", "--git-dir", remote, "show", "main:"+tmplPath).CombinedOutput()
	if err != nil {
		t.Fatalf("reading %s from remote: %v: %s", tmplPath, err, out)
	}
	if strings.Contains(string(out), "Internal local template") {
		t.Fatalf("external remote received local internal template:\n%s", out)
	}

	// 3. Push an update to the external repository and verify `manager.Sync` pulls and applies it without pushing local DB edits.
	if err := store.PutTemplate(ctx, model.Template{
		ID:    "tmpl-unexported",
		Name:  "unexported-local-edit",
		Title: "Should never be pushed to external remote",
	}); err != nil {
		t.Fatal(err)
	}
	extTmplFile := filepath.Join(extWork, filepath.FromSlash(tmplPath))
	raw, err := os.ReadFile(extTmplFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extTmplFile, []byte(strings.ReplaceAll(string(raw), "External IaC template v1", "External IaC template v2")), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", extWork, "commit", "-am", "Update template to v2").CombinedOutput(); err != nil {
		t.Fatalf("git commit v2: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", extWork, "push", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("git push v2: %v: %s", err, out)
	}

	if _, err := manager.Sync(ctx); err != nil {
		t.Fatalf("Sync in external mode: %v", err)
	}
	templates, err = store.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 1 || templates[0].Title != "External IaC template v2" {
		t.Fatalf("expected updated external template v2 in DB, got %+v", templates)
	}
	out, err = exec.Command("git", "--git-dir", remote, "show", "main:"+tmplPath).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "unexported-local-edit") {
		t.Fatalf("external remote received daemon-exported commit:\n%s", out)
	}

	// 4. Switch back to Internal mode (`Remote == ""`) and verify origin is removed and local exports resume.
	status, err = manager.SetRemote(ctx, ui.StateRepoRemoteRequest{})
	if err != nil {
		t.Fatalf("switching back to internal mode: %v", err)
	}
	if status.Mode != "internal" || status.Remote != "" || repo.Remote() != "" {
		t.Fatalf("expected internal mode with empty remote, got status=%+v repo.Remote()=%q", status, repo.Remote())
	}
	if !prClient.isLocal("grain", "config") {
		t.Fatal("expected StateRepoPullRequestClient to intercept grain/config after switching back to internal mode")
	}
	remotesOut, err := exec.Command("git", "-C", repo.Dir(), "remote").Output()
	if err != nil {
		t.Fatalf("git remote: %v", err)
	}
	if strings.TrimSpace(string(remotesOut)) != "" {
		t.Fatalf("git remote = %q, want empty", remotesOut)
	}

	// Setting external config repo to an existing secrets repo URL is also refused.
	const secURL = "https://github.com/acme/grain-secrets.git"
	if err := secretsrepo.SaveSettings(dataDir, secretsrepo.Settings{Remote: secURL}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetRemote(ctx, ui.StateRepoRemoteRequest{Remote: secURL}); err == nil {
		t.Fatal("SetRemote allowed the secrets repo URL as the external config repo")
	}

	// 5. Fresh install pointing at `remote` in external mode pulls and applies the external repository.
	freshDir := t.TempDir()
	if err := staterepo.SaveSettings(freshDir, staterepo.Settings{Remote: remote, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	freshStore, freshDB, err := OpenStore("sqlite", "", freshDir)
	if err != nil {
		t.Fatalf("opening fresh store: %v", err)
	}
	defer freshDB.Close()
	freshRepo, err := OpenStateRepo(ctx, freshDir)
	if err != nil {
		t.Fatalf("opening fresh state repo: %v", err)
	}
	if err := loadStateRepo(ctx, freshDir, freshRepo, freshDB, storeMigration{}); err != nil {
		t.Fatalf("loading fresh state repo from external remote: %v", err)
	}
	freshTemplates, err := freshStore.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("listing restored templates: %v", err)
	}
	if len(freshTemplates) != 1 || freshTemplates[0].Title != "External IaC template v2" {
		t.Fatalf("fresh install did not apply template from external repo: got %+v", freshTemplates)
	}
	if _, err := os.Stat(filepath.Join(freshRepo.Dir(), filepath.FromSlash(staterepo.WorkflowFile))); !os.IsNotExist(err) {
		t.Fatalf("expected no workflow file in external state-repo, got err=%v", err)
	}

	// 6. Malformed state-repo.json during Sync records failure via noteOutcome without re-locking m.mu.
	if err := os.WriteFile(filepath.Join(dataDir, staterepo.SettingsFileName), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Sync(ctx); err == nil {
		t.Fatal("expected Sync to return an error when state-repo.json is corrupt")
	}
}
