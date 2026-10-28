package cli

// `grain repo-case` and the state repository, which is the half of the
// reconciliation that is not in the database.
//
// The failure the state repository guards against is the one a hand run
// found (docs/hand-runs/2026-09-07-repo-case.md, finding 2): the
// repository is authoritative for the settings tables, and most of what a
// reconciliation rewrites is settings tables. A rewrite the repository
// has not been told about is one the next start imports its own dump
// back over -- repo_config and the templates go back to the old spelling
// while task and branch stay rewritten, and a template that comes back
// bound to the old spelling files new tasks under it.
//
// Everything below runs against the controller-local repository a start
// keeps at stateRepoDir, with no remote, and the real staterepo.Load a
// start runs: what is being asserted is exactly what a start does with
// what this command left behind.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// divergentStoreWithStateRepo is divergentStore with a repo_config stored
// under ACME/Widgets as well, a settings row for a later change to edit,
// and a state repository seeded from all of it the way a first start
// seeds one.
//
// The repo_config is what makes these tests about the finding rather than
// about tasks: task is not a settings table and survives any import,
// while repo_config is one and does not.
func divergentStoreWithStateRepo(t *testing.T) string {
	t.Helper()
	dataDir := divergentStore(t)
	ctx := context.Background()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	if err := store.PutConfig(ctx, model.Config{TargetRepos: []string{"acme/widgets"}}); err != nil {
		t.Fatalf("writing settings: %v", err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                model.RepoRef{Owner: "ACME", Name: "Widgets"},
		DefaultCapabilities: []string{"gcp-key"},
	}); err != nil {
		t.Fatalf("writing a repo_config: %v", err)
	}
	db.Close()
	// The first start: it finds no dump, seeds the repository from this
	// database, and records the commit it wrote as the one this host has
	// loaded.
	startWithStateRepo(t, dataDir)
	return dataDir
}

// startWithStateRepo is the part of a start these tests are about:
// staterepo.Load against the deployment's own working tree, which is
// where a repository that has moved on replaces the settings tables.
func startWithStateRepo(t *testing.T, dataDir string) {
	t.Helper()
	ctx := context.Background()
	_, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer db.Close()
	repo, err := daemon.OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatalf("opening the state repository: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading the state repository: %v", err)
	}
}

// storedRepoConfigs is every repo_config row's repo, in the spelling it
// is keyed under.
func storedRepoConfigs(t *testing.T, dataDir string) []string {
	t.Helper()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := store.ListRepoConfigs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, row := range rows {
		out = append(out, row.Repo.String())
	}
	return out
}

func storedPromptExtension(t *testing.T, dataDir string) string {
	t.Helper()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg, err := store.GetConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		return ""
	}
	return cfg.PromptExtension
}

// stateRepoGit runs git in this deployment's state repository and returns
// what it printed.
func stateRepoGit(t *testing.T, dataDir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = daemon.StateRepoDir(dataDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// commitSettingsChangeInto is a settings change committed straight into
// this deployment's state repository while the daemon is stopped -- a
// merged grain/config change the daemon has not yet taken up.
func commitSettingsChangeInto(t *testing.T, dataDir string) {
	t.Helper()
	path := filepath.Join(daemon.StateRepoDir(dataDir), staterepo.TablesDir, "grain_config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the dump: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("parsing the dump: %v: %s", err, data)
	}
	if len(rows) != 1 {
		t.Fatalf("the dump does not hold this deployment's settings: %s", data)
	}
	// An ordinary settings change: nothing to do with repo names, which is
	// the point -- what undoes the reconciliation is any commit at all this
	// host has not loaded.
	rows[0]["prompt_extension"] = mergedPromptExtension
	merged, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(merged, '\n'), 0o644); err != nil {
		t.Fatalf("writing the changed dump: %v", err)
	}
	stateRepoGit(t, dataDir, "-c", "user.email=agent@grain", "-c", "user.name=agent",
		"commit", "--quiet", "-am", "Set the prompt extension")
}

// mergedPromptExtension is what that change set, and what has to still be
// live once the reconciliation has been through.
const mergedPromptExtension = "Read docs/design.md first."

// The ordinary case: -apply rewrites the rows and commits the rewrite to
// the state repository before it returns, so the start that follows has
// nothing to import and the reconciliation stands.
func TestRepoCaseApplyExportsTheRewriteToTheStateRepository(t *testing.T) {
	t.Parallel()
	dataDir := divergentStoreWithStateRepo(t)

	var out bytes.Buffer
	if err := runRepoCase([]string{"-data-dir", dataDir, "-apply"}, &out); err != nil {
		t.Fatalf("applying: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "exported to the state repository") {
		t.Fatalf("the command does not say it exported anything:\n%s", out.String())
	}

	dump := stateRepoGit(t, dataDir, "show", "HEAD:"+staterepo.TablesDir+"/repo_config.json")
	if strings.Contains(dump, "Widgets") {
		t.Fatalf("the state repository still holds the old spelling of repo_config:\n%s", dump)
	}
	if !strings.Contains(dump, "widgets") {
		t.Fatalf("the state repository does not hold the reconciled repo_config:\n%s", dump)
	}
	// The commit says what it is: a diff of every row keyed on a
	// repository is not something a reviewer should have to guess at.
	if subject := strings.TrimSpace(stateRepoGit(t, dataDir, "log", "-1", "--format=%s")); !strings.Contains(subject, "Reconcile") {
		t.Fatalf("the export commit's subject is %q, which does not say what the diff is", subject)
	}

	// And the start that follows: the repository is where this host left
	// it, so nothing is imported and the reconciliation stands.
	startWithStateRepo(t, dataDir)
	if got := storedRepoConfigs(t, dataDir); len(got) != 1 || got[0] != "acme/widgets" {
		t.Fatalf("repo_config after the start = %v, want [acme/widgets]: the start undid the rewrite", got)
	}
	if got := storedTarget(t, dataDir); got != "acme/widgets" {
		t.Fatalf("the task's target after the start = %q, want acme/widgets", got)
	}
}

// The case the export cannot help with, because the change arrived
// first: a settings change committed while the daemon was stopped.
// Applying now would be applying into rows a start is about to replace,
// so nothing is written at all -- and once the daemon has taken that
// change up, the same command goes through.
func TestRepoCaseRefusesWhileTheStateRepositoryHoldsAnUnloadedCommit(t *testing.T) {
	t.Parallel()
	dataDir := divergentStoreWithStateRepo(t)
	commitSettingsChangeInto(t, dataDir)
	unloaded := strings.TrimSpace(stateRepoGit(t, dataDir, "rev-parse", "HEAD"))

	// A report says so before -apply has to.
	var out bytes.Buffer
	if err := runRepoCase([]string{"-data-dir", dataDir}, &out); err != nil {
		t.Fatalf("reporting: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "-apply will refuse") {
		t.Fatalf("the report does not warn that -apply will refuse:\n%s", out.String())
	}

	out.Reset()
	err := runRepoCase([]string{"-data-dir", dataDir, "-apply"}, &out)
	if err == nil {
		t.Fatalf("applying over an unloaded settings change succeeded:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "has not loaded") || !strings.Contains(out.String(), unloaded) {
		t.Fatalf("the refusal does not say what is in the way (%s):\n%s", unloaded, out.String())
	}
	if got := storedRepoConfigs(t, dataDir); len(got) != 1 || got[0] != "ACME/Widgets" {
		t.Fatalf("repo_config = %v, want [ACME/Widgets] untouched: a refusal writes nothing", got)
	}
	if got := storedTarget(t, dataDir); got != "ACME/Widgets" {
		t.Fatalf("the task's target = %q, want it untouched: a refusal writes nothing", got)
	}
	if head := strings.TrimSpace(stateRepoGit(t, dataDir, "rev-parse", "HEAD")); head != unloaded {
		t.Fatalf("the state repository moved from %s to %s: a refusal writes nothing", unloaded, head)
	}

	// The way out, which is the one the refusal names: start the daemon
	// once so it takes the change up, then run this again.
	startWithStateRepo(t, dataDir)
	if extension := storedPromptExtension(t, dataDir); extension != mergedPromptExtension {
		t.Fatalf("the start did not take the change up: prompt extension = %q", extension)
	}
	out.Reset()
	if err := runRepoCase([]string{"-data-dir", dataDir, "-apply"}, &out); err != nil {
		t.Fatalf("applying after the daemon took the change up: %v\n%s", err, out.String())
	}
	if got := storedRepoConfigs(t, dataDir); len(got) != 1 || got[0] != "acme/widgets" {
		t.Fatalf("repo_config = %v, want [acme/widgets]", got)
	}
	// Both survive the next start now: the committed setting, which this
	// host has loaded, and the rewrite, which it has exported.
	startWithStateRepo(t, dataDir)
	if got := storedRepoConfigs(t, dataDir); len(got) != 1 || got[0] != "acme/widgets" {
		t.Fatalf("repo_config after the start = %v, want [acme/widgets]: the start undid the rewrite", got)
	}
	if extension := storedPromptExtension(t, dataDir); extension != mergedPromptExtension {
		t.Fatalf("the committed setting = %q, want it still live", extension)
	}
}

// A deployment with no state repository at all -- nothing configured,
// nothing on disk -- reconciles exactly as it did before: this command
// must not be the thing that creates one.
func TestRepoCaseWithoutAStateRepositoryTouchesNothingOfTheKind(t *testing.T) {
	t.Parallel()
	dataDir := divergentStore(t)

	var out bytes.Buffer
	if err := runRepoCase([]string{"-data-dir", dataDir, "-apply"}, &out); err != nil {
		t.Fatalf("applying: %v\n%s", err, out.String())
	}
	if got := storedTarget(t, dataDir); got != "acme/widgets" {
		t.Fatalf("target = %q, want acme/widgets", got)
	}
	if _, err := os.Stat(daemon.StateRepoDir(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("a state repository was created at %s: %v", daemon.StateRepoDir(dataDir), err)
	}
}
