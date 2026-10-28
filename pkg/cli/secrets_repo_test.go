package cli

// The daemon's side of pkg/secretsrepo: which repository the ciphertext
// is backed up to, who may clone it (nobody), and what a rebuilt host
// gets back before it opens its secret store.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/secretsrepo"
	"github.com/bwsalmon/grain/pkg/staterepo"
	"github.com/bwsalmon/grain/pkg/ui"
)

func bareSecretsRemote(t *testing.T) string {
	t.Helper()
	return initBareRepo(t, filepath.Join(t.TempDir(), "secrets.git"))
}

// configured is a data directory pointed at a state repository and a
// secrets repository, the way a deploy or `grain secrets repo` leaves
// one.
func configured(t *testing.T, stateRemote, secretsRemote string) string {
	t.Helper()
	dataDir := t.TempDir()
	if stateRemote != "" {
		if err := staterepo.SaveSettings(dataDir, staterepo.Settings{Remote: stateRemote}); err != nil {
			t.Fatal(err)
		}
	}
	if secretsRemote != "" {
		if err := secretsrepo.SaveSettings(dataDir, secretsrepo.Settings{Remote: secretsRemote}); err != nil {
			t.Fatal(err)
		}
	}
	return dataDir
}

func writeCiphertext(t *testing.T, dataDir, ciphertext string) {
	t.Helper()
	path := daemon.SecretsConfig(dataDir).File
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(ciphertext), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The round trip a rebuilt host depends on: one deployment pushes its
// ciphertext, a second data directory with nothing in it gets it back
// under the same name -- and gets it before anything opens the store,
// which is what startupSecretsRepoSync's placement is for.
func TestARebuiltHostGetsItsCiphertextBackBeforeOpeningTheStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := bareSecretsRemote(t)
	first := configured(t, "https://github.com/owner/grain-state", remote)
	writeCiphertext(t, first, "sealed to this deployment")
	if _, err := daemon.SyncSecretsRepo(ctx, first); err != nil {
		t.Fatalf("pushing: %v", err)
	}

	rebuilt := configured(t, "https://github.com/owner/grain-state", remote)
	if repo := daemon.StartupSecretsRepoSync(ctx, rebuilt); repo == nil {
		t.Fatal("a configured deployment opened no secrets repository")
	}
	got, err := os.ReadFile(daemon.SecretsConfig(rebuilt).File)
	if err != nil {
		t.Fatalf("the ciphertext was not restored: %v", err)
	}
	if string(got) != "sealed to this deployment" {
		t.Errorf("restored %q", got)
	}
}

// The name in the repository has to say whose file it is: a secrets
// repository is assumed to be shared, and this is what stops two
// deployments -- or two tools -- landing on one file.
func TestTheFileIsNamedForGrainAndTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := bareSecretsRemote(t)
	dataDir := configured(t, "https://github.com/bwsalmon/grain-home", remote)
	writeCiphertext(t, dataDir, "ciphertext")
	repo, err := daemon.SyncSecretsRepo(ctx, dataDir)
	if err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if want := "grain-bwsalmon-grain-home.secrets.enc"; repo.File() != want {
		t.Errorf("the file is called %q, want %q", repo.File(), want)
	}
	out, err := exec.Command("git", "-C", remote, "ls-tree", "--name-only", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("listing the remote: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "grain-bwsalmon-grain-home.secrets.enc" {
		t.Errorf("the remote holds %q", out)
	}
}

// A deployment with nothing configured is the ordinary one, and it must
// not be an error anybody logs or a repository anybody opens.
func TestNoSecretsRepositoryIsNotAFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	repo, err := daemon.SyncSecretsRepo(ctx, dataDir)
	if err != nil || repo != nil {
		t.Fatalf("an unconfigured deployment gave (%v, %v)", repo, err)
	}
	if refs := daemon.SecretsRepoRefs(dataDir); len(refs) != 0 {
		t.Errorf("it forbade %v", refs)
	}
}

// The proxy refuses it to every sandbox, without asking what is in it:
// holding grain's ciphertext is the whole of what the repository is for.
func TestTheSecretsRepositoryIsForbiddenToEverySandbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := configured(t, "", "https://github.com/bwsalmon/private-secrets")
	forbidden, err := daemon.ForbiddenRepos(ctx, dataDir)
	if err != nil {
		t.Fatalf("asking what is forbidden: %v", err)
	}
	want := model.RepoRef{Owner: "bwsalmon", Name: "private-secrets"}
	found := false
	for _, ref := range forbidden {
		if ref == want {
			found = true
		}
	}
	if !found {
		t.Errorf("the git proxy would serve %s to a sandbox; forbidden = %v", want, forbidden)
	}
}

// Pointing both at one repository is a plausible typo and would undo the
// separation the ciphertext was moved out of the state repository for.
func TestTheSecretsRepositoryMayNotBeTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := configured(t, "https://github.com/owner/grain-state", "git@github.com:owner/grain-state.git")
	if _, err := daemon.SyncSecretsRepo(ctx, dataDir); err == nil ||
		!strings.Contains(err.Error(), "state repository") {
		t.Fatalf("syncing a secrets repository that is the state repository: %v", err)
	}
	// And the command refuses to write it in the first place, which is
	// where an operator finds out.
	var out bytes.Buffer
	err := secretsRepoCommand(ctx, dataDir, &out, []string{"https://github.com/owner/grain-state"})
	if err == nil || !strings.Contains(err.Error(), "state repository") {
		t.Fatalf("`grain secrets repo` accepted the state repository: %v", err)
	}
	// Once `grain state repo -none` clears the mirror, the former mirror
	// repository may become the secrets repository.
	if err := stateRepoCommand(ctx, dataDir, &out, []string{"-none"}); err != nil {
		t.Fatalf("clearing state repo mirror: %v", err)
	}
	if err := secretsRepoCommand(ctx, dataDir, &out, []string{"https://github.com/owner/grain-state"}); err != nil {
		t.Fatalf("using the former config mirror for secrets after -none: %v", err)
	}
	// And `grain state repo` then refuses the repository already used for secrets.
	if err := stateRepoCommand(ctx, dataDir, &out, []string{"git@github.com:owner/grain-state.git"}); err == nil ||
		!strings.Contains(err.Error(), "state repository") {
		t.Fatalf("`grain state repo` accepted the secrets repository: %v", err)
	}
}

func TestSecretsRepoCommandNamesAndClearsTheRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := configured(t, "https://github.com/bwsalmon/grain-home", "")

	var out bytes.Buffer
	if err := secretsRepoCommand(ctx, dataDir, &out, []string{"https://github.com/bwsalmon/private-secrets"}); err != nil {
		t.Fatalf("naming a secrets repository: %v", err)
	}
	settings, err := secretsrepo.LoadSettings(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Remote != "https://github.com/bwsalmon/private-secrets" {
		t.Errorf("wrote %+v", settings)
	}
	// The operator has to be told which file it will be, since that is
	// what they will look for in a repository holding other things.
	if !strings.Contains(out.String(), "grain-bwsalmon-grain-home.secrets.enc") {
		t.Errorf("it never said what the file is called:\n%s", out.String())
	}

	out.Reset()
	if err := secretsRepoCommand(ctx, dataDir, &out, []string{"-none"}); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	settings, err = secretsrepo.LoadSettings(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Remote != "" {
		t.Errorf("-none left %+v", settings)
	}
}

// The same two things from the State pane, for the operator who runs
// this deployment through a browser and has no terminal on the host:
// naming the repository, and taking it back.
//
// It writes the settings file and nothing else -- no clone, no push --
// which is what makes it safe to do from a request: the daemon re-reads
// which repository it is on every sync tick, so the first push follows a
// tick later.
func TestTheStatePaneNamesAndClearsTheSecretsRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	stateRemote := bareRemote(t)
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{Remote: stateRemote}); err != nil {
		t.Fatal(err)
	}
	forbidden := gitproxy.NewForbiddenSet()
	manager := stateManagerFor(t, dataDir, forbidden)

	status, err := manager.SetSecretsRepo(ctx, ui.SecretsRepoRequest{
		Remote: "https://github.com/bwsalmon/private-secrets", Branch: "trunk", Token: "ghp_x",
	})
	if err != nil {
		t.Fatalf("naming a secrets repository: %v", err)
	}
	// What the pane paints next: where the file goes, under what name,
	// and on what branch -- the operator has to be able to find it in a
	// repository that holds other deployments' files too.
	if status.SecretsRepo != "https://github.com/bwsalmon/private-secrets" ||
		status.SecretsRepoFile != secretsrepo.FileName(stateRemote) ||
		status.SecretsRepoBranch != "trunk" {
		t.Fatalf("the pane cannot say where the ciphertext now goes: %+v", status)
	}
	settings, err := secretsrepo.LoadSettings(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Remote != "https://github.com/bwsalmon/private-secrets" || settings.Branch != "trunk" {
		t.Fatalf("wrote %+v", settings)
	}
	// The pasted credential is a file the daemon reads, never the
	// settings file itself: that one is not encrypted and nothing in it
	// is marked as a secret.
	if settings.TokenFile == "" {
		t.Fatalf("the pasted token was not written anywhere the push can find it: %+v", settings)
	}
	token, err := os.ReadFile(settings.TokenFile)
	if err != nil {
		t.Fatalf("reading the token file: %v", err)
	}
	if strings.TrimSpace(string(token)) != "ghp_x" {
		t.Fatalf("the token file holds %q", token)
	}
	// And the git proxy is told at once rather than at the next restart:
	// a repository holding grain's ciphertext is one no sandbox may clone
	// from the moment the settings name it.
	want := model.RepoRef{Owner: "bwsalmon", Name: "private-secrets"}
	if !containsRef(forbidden.Repos(), want) {
		t.Fatalf("the git proxy would still serve %s to a sandbox: %v", want, forbidden.Repos())
	}

	status, err = manager.SetSecretsRepo(ctx, ui.SecretsRepoRequest{})
	if err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if status.SecretsRepo != "" {
		t.Fatalf("the pane still reports a backup: %+v", status)
	}
	if settings, err = secretsrepo.LoadSettings(dataDir); err != nil {
		t.Fatal(err)
	} else if settings.Remote != "" || settings.TokenFile != "" {
		t.Fatalf("clearing left %+v", settings)
	}
	// Nothing is grain's ciphertext any more, so the refusal is lifted --
	// leaving it would go on denying whoever owns that repository.
	if containsRef(forbidden.Repos(), want) {
		t.Fatalf("a repository this deployment no longer uses is still refused: %v", forbidden.Repos())
	}
}

func TestTheStatePaneRefusesTheStateRepositoryAsTheSecretsRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	manager := stateManagerFor(t, dataDir, nil)
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{
		Remote: "https://github.com/owner/grain-state",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.SetSecretsRepo(ctx, ui.SecretsRepoRequest{
		Remote: "git@github.com:owner/grain-state.git",
	}); err == nil || !strings.Contains(err.Error(), "state repository") {
		t.Fatalf("the pane accepted the state repository: %v", err)
	}
	settings, err := secretsrepo.LoadSettings(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Remote != "" {
		t.Fatalf("a refused repository was written anyway: %+v", settings)
	}
}

func containsRef(refs []model.RepoRef, want model.RepoRef) bool {
	for _, ref := range refs {
		if ref == want {
			return true
		}
	}
	return false
}

// `grain state status` is where an operator reads what survives a
// rebuilt host, so it is where the absence of an off-host copy has to be
// said out loud rather than implied by a missing line.
func TestStateStatusSaysWhetherTheSecretsAreBackedUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	var out bytes.Buffer
	if err := stateStatus(ctx, dataDir, &out); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "secrets backup: none") {
		t.Errorf("an unbacked deployment's status never says so:\n%s", out.String())
	}

	// A state repository this test can actually open, since the status
	// command clones one; the file name is derived from whatever it is.
	stateRemote := bareSecretsRemote(t)
	backed := configured(t, stateRemote, "https://github.com/bwsalmon/private-secrets")
	out.Reset()
	if err := stateStatus(ctx, backed, &out); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "secrets backup: https://github.com/bwsalmon/private-secrets") ||
		!strings.Contains(out.String(), secretsrepo.FileName(stateRemote)) {
		t.Errorf("a backed-up deployment's status does not say where or under what name:\n%s", out.String())
	}
}

// writeWildcardLadder is the credential shape scripts/setup.sh leaves: one
// token every repository's pattern falls through to.
func writeWildcardLadder(t *testing.T, dataDir, token string) {
	t.Helper()
	dir := filepath.Join(dataDir, "secrets", "github")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"*": "prodbot"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prodbot.token"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

// #2372: the ladder chose a credential by owner/repo alone, so a remote
// on any host whose path looked like a repository was sent the
// deployment's GitHub token.
func TestTheGitHubLadderIsOnlySentToGitHub(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	const token = "ghp_FAKE_TEST_TOKEN_do_not_use"
	writeWildcardLadder(t, dataDir, token)
	ctx := context.Background()
	// The deployment's git connections, which RepoToken reads once the
	// daemon has opened its store: the ladder above imported as one
	// (docs/single-tenant-reset.md, stage 1).
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateConfig(ctx, func(t *model.Config) error {
		t.GitConnections = append(t.GitConnections, model.TeamGitConnection{Credential: "prodbot"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(daemon.SetRepoTokenStore(dataDir, store))

	for _, remote := range []string{
		"https://github.com/test-owner/test-repo.git",
		"https://GitHub.com/test-owner/test-repo",
		"https://github.com:443/test-owner/test-repo.git",
	} {
		got, err := daemon.RepoToken(dataDir, remote, "")(ctx)
		if err != nil || got != token {
			t.Errorf("repoToken(%q) = %q, %v; want the ladder's token", remote, got, err)
		}
	}
	for _, remote := range []string{
		"http://127.0.0.1:18450/test-owner/test-repo.git",
		"https://evil.example/test-owner/test-repo.git",
		"http://github.com/test-owner/test-repo.git",
		"https://github.com.evil.example/test-owner/test-repo.git",
		"https://github.com@evil.example/test-owner/test-repo.git",
		"https://user@github.com/test-owner/test-repo.git",
		"https://github.com:8443/test-owner/test-repo.git",
		"https://ghe.example.com/test-owner/test-repo.git",
	} {
		got, err := daemon.RepoToken(dataDir, remote, "")(ctx)
		if err == nil || got != "" {
			t.Errorf("repoToken(%q) = %q, %v; want a refusal and no token", remote, got, err)
		}
		if err != nil && strings.Contains(err.Error(), token) {
			t.Errorf("repoToken(%q)'s refusal carries the token: %v", remote, err)
		}
	}
}

// A token named for a repository is kept when the remote is re-entered
// in another form, and dropped when the host changes -- otherwise
// re-pointing the same owner/repo path at another server sends it there.
func TestANamedSecretsRepoTokenDoesNotFollowTheRemoteToAnotherHost(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{Remote: bareRemote(t)}); err != nil {
		t.Fatal(err)
	}
	manager := stateManagerFor(t, dataDir, gitproxy.NewForbiddenSet())
	ctx := context.Background()

	if _, err := manager.SetSecretsRepo(ctx, ui.SecretsRepoRequest{
		Remote: "https://github.com/test-owner/test-repo.git", Token: "named-token",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetSecretsRepo(ctx, ui.SecretsRepoRequest{
		Remote: "git@github.com:test-owner/test-repo.git",
	}); err != nil {
		t.Fatal(err)
	}
	settings, err := secretsrepo.LoadSettings(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if settings.TokenFile == "" {
		t.Fatalf("the same repository over ssh dropped its named token")
	}

	if _, err := manager.SetSecretsRepo(ctx, ui.SecretsRepoRequest{
		Remote: "http://127.0.0.1:18450/test-owner/test-repo.git",
	}); err != nil {
		t.Fatal(err)
	}
	if settings, err = secretsrepo.LoadSettings(dataDir); err != nil {
		t.Fatal(err)
	}
	if settings.TokenFile != "" {
		t.Fatalf("the named token followed the remote to another host: %+v", settings)
	}
}
